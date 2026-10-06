// Package replay provides an http.RoundTripper that answers requests from a
// recorded `.http` cassette instead of the network, so unit tests can drive
// real SDK code with no API key and no connectivity. It reads cassettes through
// pkg/recordfile and does not depend on the application database.
package replay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// RequestMatcher decides whether the recorded request in the cassette may
// answer the actual outbound request. Returning a non-nil error fails the
// replay with that error, so a test fails instead of silently accepting a
// request the cassette never recorded.
type RequestMatcher func(recorded RequestSnapshot, actual RequestSnapshot) error

// RequestSnapshot is the comparable view of an HTTP request: method, path,
// sorted query and the request body.
type RequestSnapshot struct {
	Method string
	Path   string
	Query  url.Values
	Body   []byte
}

// Transport 实现 http.RoundTripper 接口，用于回放本地 .http 文件
type Transport struct {
	Filename string

	// StrictRequest turns on the built-in request/response contract: the replay
	// only answers when method, path, sorted query and a normalized body match
	// the recorded request. It is off by default so existing cassettes (legacy
	// V2 files and payloads an SDK no longer sends byte-for-byte) keep
	// replaying; tests that want to prove request construction should enable it.
	StrictRequest bool
	// RequestMatcher replaces the built-in matcher when set. It receives the
	// recorded request and the actual one and must return an error on mismatch.
	RequestMatcher RequestMatcher

	mu    sync.Mutex
	cache *transportCache
}

type transportCache struct {
	filename       string
	size           int64
	modTime        time.Time
	responseOffset int64
	request        RequestSnapshot
}

type SummaryOptions struct {
	BodyLimit int
}

type Summary struct {
	RequestMethod string              `json:"request_method"`
	RequestURL    string              `json:"request_url"`
	Status        string              `json:"status"`
	StatusCode    int                 `json:"status_code"`
	ContentType   string              `json:"content_type,omitempty"`
	Header        map[string][]string `json:"header"`
	Body          string              `json:"body,omitempty"`
	BodyBytes     int                 `json:"body_bytes"`
	BodyTruncated bool                `json:"body_truncated"`
	IsStream      bool                `json:"is_stream"`
}

// NewTransport 创建一个新的回放 Transport
func NewTransport(filename string) *Transport {
	return &Transport{Filename: filename}
}

func ReplayFile(filename string, opts SummaryOptions) (*Summary, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("replay: failed to read file %s: %w", filename, err)
	}

	parsed, err := recordfile.ParsePrelude(content)
	if err != nil {
		return nil, fmt.Errorf("replay: invalid record prelude: %w", err)
	}

	reqFull, _, resFull, _ := recordfile.ExtractSections(content, parsed)
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(reqFull)))
	if err != nil {
		return nil, fmt.Errorf("replay: failed to parse http request: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(resFull)), req)
	if err != nil {
		return nil, fmt.Errorf("replay: failed to parse http response: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("replay: failed to read response body: %w", err)
	}

	limit := opts.BodyLimit
	if limit <= 0 {
		limit = 4096
	}
	if limit > 20000 {
		limit = 20000
	}

	bodyOut := body
	truncated := false
	if len(bodyOut) > limit {
		bodyOut = bodyOut[:limit]
		truncated = true
	}

	return &Summary{
		RequestMethod: req.Method,
		RequestURL:    req.URL.String(),
		Status:        resp.Status,
		StatusCode:    resp.StatusCode,
		ContentType:   resp.Header.Get("Content-Type"),
		Header:        resp.Header,
		Body:          string(bodyOut),
		BodyBytes:     len(body),
		BodyTruncated: truncated,
		IsStream:      parsed.Header.Layout.IsStream,
	}, nil
}

// RoundTrip 执行请求回放逻辑
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	respOffset, err := t.cachedResponseOffset()
	if err != nil {
		return nil, err
	}

	if t.StrictRequest || t.RequestMatcher != nil {
		recorded, err := t.recordedRequest()
		if err != nil {
			return nil, err
		}
		actual, err := snapshotRequest(req)
		if err != nil {
			return nil, err
		}
		matcher := t.RequestMatcher
		if matcher == nil {
			matcher = MatchRecordedRequest
		}
		if err := matcher(recorded, actual); err != nil {
			return nil, fmt.Errorf("replay: %s: %w", t.Filename, err)
		}
	}

	f, err := os.Open(t.Filename)
	if err != nil {
		return nil, fmt.Errorf("replay: failed to open file %s: %w", t.Filename, err)
	}

	if _, err := f.Seek(respOffset, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("replay: seek failed: %w", err)
	}

	bufReader := bufio.NewReader(f)
	resp, err := http.ReadResponse(bufReader, req)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("replay: failed to parse http response: %w", err)
	}

	resp.Body = &fileCloser{
		ReadCloser: resp.Body,
		File:       f,
	}

	return resp, nil
}

func (t *Transport) cachedResponseOffset() (int64, error) {
	info, err := os.Stat(t.Filename)
	if err != nil {
		return 0, fmt.Errorf("replay: failed to stat file %s: %w", t.Filename, err)
	}

	t.mu.Lock()
	if t.cache != nil &&
		t.cache.filename == t.Filename &&
		t.cache.size == info.Size() &&
		t.cache.modTime.Equal(info.ModTime()) {
		offset := t.cache.responseOffset
		t.mu.Unlock()
		return offset, nil
	}
	t.mu.Unlock()

	content, err := os.ReadFile(t.Filename)
	if err != nil {
		return 0, fmt.Errorf("replay: failed to read file %s: %w", t.Filename, err)
	}

	parsed, err := recordfile.ParsePrelude(content)
	if err != nil {
		return 0, fmt.Errorf("replay: invalid record prelude: %w", err)
	}

	respOffset := parsed.PayloadOffset + parsed.Header.Layout.ReqHeaderLen + parsed.Header.Layout.ReqBodyLen + 1
	reqFull, reqBody, _, _ := recordfile.ExtractSections(content, parsed)
	snapshot := RequestSnapshot{Body: append([]byte(nil), reqBody...)}
	if recorded, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(reqFull))); err == nil {
		snapshot.Method = recorded.Method
		snapshot.Path = recorded.URL.Path
		snapshot.Query = recorded.URL.Query()
	}
	t.mu.Lock()
	t.cache = &transportCache{
		filename:       t.Filename,
		size:           info.Size(),
		modTime:        info.ModTime(),
		responseOffset: respOffset,
		request:        snapshot,
	}
	t.mu.Unlock()
	return respOffset, nil
}

// recordedRequest returns the request half of the cassette.
func (t *Transport) recordedRequest() (RequestSnapshot, error) {
	if _, err := t.cachedResponseOffset(); err != nil {
		return RequestSnapshot{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cache == nil {
		return RequestSnapshot{}, fmt.Errorf("replay: no cached request for %s", t.Filename)
	}
	return t.cache.request, nil
}

// VerifyRequest checks one outbound request against the cassette without
// replaying it, for tests that want an explicit request-side assertion.
func (t *Transport) VerifyRequest(req *http.Request) error {
	recorded, err := t.recordedRequest()
	if err != nil {
		return err
	}
	actual, err := snapshotRequest(req)
	if err != nil {
		return err
	}
	matcher := t.RequestMatcher
	if matcher == nil {
		matcher = MatchRecordedRequest
	}
	if err := matcher(recorded, actual); err != nil {
		return fmt.Errorf("replay: %s: %w", t.Filename, err)
	}
	return nil
}

// snapshotRequest reads the request body (restoring it for the caller) and
// returns the comparable view.
func snapshotRequest(req *http.Request) (RequestSnapshot, error) {
	snapshot := RequestSnapshot{Method: req.Method, Path: req.URL.Path, Query: req.URL.Query()}
	if req.Body == nil {
		return snapshot, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return RequestSnapshot{}, fmt.Errorf("replay: failed to read request body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	if req.GetBody != nil {
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
	snapshot.Body = body
	return snapshot, nil
}

// MatchRecordedRequest is the built-in matcher: method, path, sorted query and
// a normalized body must match. The host is deliberately ignored because a
// cassette records the upstream host, not the proxy the SDK talks to.
func MatchRecordedRequest(recorded RequestSnapshot, actual RequestSnapshot) error {
	var diffs []string
	if recorded.Method != "" && !strings.EqualFold(recorded.Method, actual.Method) {
		diffs = append(diffs, fmt.Sprintf("method: recorded %q, got %q", recorded.Method, actual.Method))
	}
	if recorded.Path != "" && recorded.Path != actual.Path {
		diffs = append(diffs, fmt.Sprintf("path: recorded %q, got %q", recorded.Path, actual.Path))
	}
	if recorded.Query != nil && !queryEqual(recorded.Query, actual.Query) {
		diffs = append(diffs, fmt.Sprintf("query: recorded %q, got %q", recorded.Query.Encode(), actual.Query.Encode()))
	}
	if len(recorded.Body) > 0 && !bodyEqual(recorded.Body, actual.Body) {
		diffs = append(diffs, fmt.Sprintf("body: recorded %s, got %s", bodyPreview(recorded.Body), bodyPreview(actual.Body)))
	}
	if len(diffs) == 0 {
		return nil
	}
	return fmt.Errorf("request does not match the cassette: %s", strings.Join(diffs, "; "))
}

// MatchRecordedRequestPath is the compatibility-safe matcher: it only requires
// the recorded method and path. Legacy V2 cassettes record a request body the
// current SDK no longer sends byte-for-byte, so tests that replay them can still
// assert the SDK did not change its endpoint or verb.
func MatchRecordedRequestPath(recorded RequestSnapshot, actual RequestSnapshot) error {
	var diffs []string
	if recorded.Method != "" && !strings.EqualFold(recorded.Method, actual.Method) {
		diffs = append(diffs, fmt.Sprintf("method: recorded %q, got %q", recorded.Method, actual.Method))
	}
	if recorded.Path != "" && recorded.Path != actual.Path {
		diffs = append(diffs, fmt.Sprintf("path: recorded %q, got %q", recorded.Path, actual.Path))
	}
	if len(diffs) == 0 {
		return nil
	}
	return fmt.Errorf("request does not match the cassette: %s", strings.Join(diffs, "; "))
}

func queryEqual(a, b url.Values) bool {
	return a.Encode() == b.Encode()
}

// bodyEqual compares two bodies after JSON normalization so formatting and key
// order changes do not fail an otherwise identical request.
func bodyEqual(recorded, actual []byte) bool {
	if bytes.Equal(bytes.TrimSpace(recorded), bytes.TrimSpace(actual)) {
		return true
	}
	var recordedJSON, actualJSON any
	if err := json.Unmarshal(recorded, &recordedJSON); err != nil {
		return false
	}
	if err := json.Unmarshal(actual, &actualJSON); err != nil {
		return false
	}
	recordedNorm, err := json.Marshal(recordedJSON)
	if err != nil {
		return false
	}
	actualNorm, err := json.Marshal(actualJSON)
	if err != nil {
		return false
	}
	return bytes.Equal(recordedNorm, actualNorm)
}

func bodyPreview(body []byte) string {
	const limit = 200
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > limit {
		return fmt.Sprintf("%s… (%d bytes)", trimmed[:limit], len(trimmed))
	}
	return string(trimmed)
}

// fileCloser 包装器，确保 Body 关闭时文件句柄也被释放
type fileCloser struct {
	io.ReadCloser
	File *os.File
}

func (fc *fileCloser) Close() error {
	// 先关 Body (虽然 http.Response.Body 通常是基于 bufio 的 wrapper，不持有 fd)
	err1 := fc.ReadCloser.Close()
	// 再关实际的文件句柄
	err2 := fc.File.Close()

	if err1 != nil {
		return err1
	}
	return err2
}
