// Package recordfile reads and writes the `.http` cassette container: a short
// prelude (the `# trajecta/v3` magic, one `# meta:` JSON line and zero or more
// `# event:` JSON lines) followed by the raw HTTP request and response bytes.
//
// Writers emit V3 only. Readers additionally accept the legacy `LLM_PROXY_V2`
// layout with its fixed 2KB JSON header block and the pre-rename prelude magic
// `# llm-tracelab/v3`; `LLM_PROXY_V3` stays the stable format identifier and is
// deliberately not renamed.
package recordfile

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	// FileMagic is the prelude magic emitted for new V3 recordings.
	FileMagic = "# trajecta/v3"
	// LegacyFileMagic is the prelude magic written before the project was
	// renamed from llm-tracelab. Readers must keep accepting it: cassettes are
	// the durable replay artifact and are never rewritten by a rename.
	LegacyFileMagic = "# llm-tracelab/v3"
	metaPrefix      = "# meta: "
	eventPrefix     = "# event: "
	LegacyHeaderLen = 2048
)

// HasFileMagic reports whether content starts with a supported V3 prelude
// magic, current or legacy.
func HasFileMagic(content []byte) bool {
	return bytes.HasPrefix(content, []byte(FileMagic)) ||
		bytes.HasPrefix(content, []byte(LegacyFileMagic))
}

// IsV3Prelude reports whether content starts with a supported V3 prelude magic
// followed by the newline that terminates the magic line.
func IsV3Prelude(content []byte) bool {
	return bytes.HasPrefix(content, []byte(FileMagic+"\n")) ||
		bytes.HasPrefix(content, []byte(LegacyFileMagic+"\n"))
}

// isMagicLine reports whether line is exactly a supported V3 prelude magic.
func isMagicLine(line []byte) bool {
	return bytes.Equal(line, []byte(FileMagic)) || bytes.Equal(line, []byte(LegacyFileMagic))
}

type PromptTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type UsageInfo struct {
	PromptTokens       int                 `json:"prompt_tokens"`
	CompletionTokens   int                 `json:"completion_tokens"`
	TotalTokens        int                 `json:"total_tokens"`
	PromptTokenDetails *PromptTokenDetails `json:"prompt_tokens_details,omitempty"`
}

type LayoutInfo struct {
	ReqHeaderLen int64 `json:"req_header_len"`
	ReqBodyLen   int64 `json:"req_body_len"`
	ResHeaderLen int64 `json:"res_header_len"`
	ResBodyLen   int64 `json:"res_body_len"`
	IsStream     bool  `json:"is_stream"`
}

type MetaData struct {
	RequestID                      string    `json:"request_id"`
	RequestAuditID                 string    `json:"request_audit_id,omitempty"`
	ResponseID                     string    `json:"response_id,omitempty"`
	ConversationID                 string    `json:"conversation_id,omitempty"`
	ClientRequestID                string    `json:"client_request_id,omitempty"`
	ExchangeID                     string    `json:"exchange_id,omitempty"`
	ExchangeKind                   string    `json:"exchange_kind,omitempty"`
	ExchangeRole                   string    `json:"exchange_role,omitempty"`
	ParentExchangeID               string    `json:"parent_exchange_id,omitempty"`
	SequenceIndex                  int       `json:"sequence_index,omitempty"`
	TraceID                        string    `json:"trace_id,omitempty"`
	Time                           time.Time `json:"time"`
	Model                          string    `json:"model"`
	Provider                       string    `json:"provider,omitempty"`
	Operation                      string    `json:"operation,omitempty"`
	Endpoint                       string    `json:"endpoint,omitempty"`
	URL                            string    `json:"url"`
	Method                         string    `json:"method"`
	StatusCode                     int       `json:"status_code"`
	DurationMs                     int64     `json:"duration_ms"`
	TTFTMs                         int64     `json:"ttft_ms"`
	ClientIP                       string    `json:"client_ip"`
	ContentLength                  int64     `json:"content_length"`
	Error                          string    `json:"error,omitempty"`
	SelectedUpstreamID             string    `json:"selected_upstream_id,omitempty"`
	SelectedUpstreamBaseURL        string    `json:"selected_upstream_base_url,omitempty"`
	SelectedUpstreamProviderPreset string    `json:"selected_upstream_provider_preset,omitempty"`
	RoutingPolicy                  string    `json:"routing_policy,omitempty"`
	RoutingScore                   float64   `json:"routing_score,omitempty"`
	RoutingCandidateCount          int       `json:"routing_candidate_count,omitempty"`
	RoutingFailureReason           string    `json:"routing_failure_reason,omitempty"`
}

type RecordHeader struct {
	Version string     `json:"version"`
	Meta    MetaData   `json:"meta"`
	Layout  LayoutInfo `json:"layout"`
	Usage   UsageInfo  `json:"usage"`
}

type RecordEvent struct {
	Type        string                 `json:"type"`
	Time        time.Time              `json:"time,omitempty"`
	Method      string                 `json:"method,omitempty"`
	URL         string                 `json:"url,omitempty"`
	StatusCode  int                    `json:"status_code,omitempty"`
	IsStream    bool                   `json:"is_stream,omitempty"`
	HeaderBytes int64                  `json:"header_bytes,omitempty"`
	BodyBytes   int64                  `json:"body_bytes,omitempty"`
	Message     string                 `json:"message,omitempty"`
	Attributes  map[string]interface{} `json:"attributes,omitempty"`
}

type ParsedPrelude struct {
	Header        RecordHeader
	Events        []RecordEvent
	PayloadOffset int64
}

type CassetteSummaryOptions struct {
	BodyLimit int
}

type HTTPRequestSummary struct {
	Method        string              `json:"method"`
	URL           string              `json:"url"`
	Header        map[string][]string `json:"header,omitempty"`
	Body          string              `json:"body,omitempty"`
	BodyBytes     int                 `json:"body_bytes"`
	BodySHA256    string              `json:"body_sha256,omitempty"`
	BodyTruncated bool                `json:"body_truncated"`
}

type HTTPResponseSummary struct {
	Status        string              `json:"status"`
	StatusCode    int                 `json:"status_code"`
	ContentType   string              `json:"content_type,omitempty"`
	Header        map[string][]string `json:"header,omitempty"`
	Body          string              `json:"body,omitempty"`
	BodyBytes     int                 `json:"body_bytes"`
	BodySHA256    string              `json:"body_sha256,omitempty"`
	BodyTruncated bool                `json:"body_truncated"`
	IsStream      bool                `json:"is_stream"`
}

type HTTPExchangeSummary struct {
	Header   RecordHeader        `json:"header"`
	Events   []RecordEvent       `json:"events,omitempty"`
	Request  HTTPRequestSummary  `json:"request"`
	Response HTTPResponseSummary `json:"response"`
}

func MarshalPrelude(header RecordHeader, events []RecordEvent) ([]byte, error) {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteString(FileMagic)
	buf.WriteByte('\n')
	buf.WriteString(metaPrefix)
	buf.Write(headerJSON)
	buf.WriteByte('\n')

	for _, event := range events {
		eventJSON, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		buf.WriteString(eventPrefix)
		buf.Write(eventJSON)
		buf.WriteByte('\n')
	}

	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func BuildEvents(header RecordHeader) []RecordEvent {
	events := []RecordEvent{
		{
			Type:        "request",
			Time:        header.Meta.Time,
			Method:      header.Meta.Method,
			URL:         header.Meta.URL,
			HeaderBytes: header.Layout.ReqHeaderLen,
			BodyBytes:   header.Layout.ReqBodyLen,
		},
		{
			Type:        "response",
			Time:        header.Meta.Time.Add(time.Duration(header.Meta.DurationMs) * time.Millisecond),
			StatusCode:  header.Meta.StatusCode,
			IsStream:    header.Layout.IsStream,
			HeaderBytes: header.Layout.ResHeaderLen,
			BodyBytes:   header.Layout.ResBodyLen,
		},
	}

	if header.Meta.Error != "" {
		events = append(events, RecordEvent{
			Type:    "error",
			Time:    header.Meta.Time.Add(time.Duration(header.Meta.DurationMs) * time.Millisecond),
			Message: header.Meta.Error,
		})
	}

	return events
}

func ParsePrelude(content []byte) (*ParsedPrelude, error) {
	lineEnd := bytes.IndexByte(content, '\n')
	if lineEnd < 0 {
		return nil, errors.New("failed to read prelude: missing first line")
	}

	line := bytes.TrimSuffix(content[:lineEnd], []byte("\r"))
	if isMagicLine(line) {
		return parseV3Prelude(content)
	}

	var header RecordHeader
	if err := json.Unmarshal(line, &header); err != nil {
		return nil, fmt.Errorf("invalid record prelude: %w", err)
	}

	return &ParsedPrelude{
		Header:        header,
		PayloadOffset: LegacyHeaderLen,
	}, nil
}

// ErrUnusablePrelude marks a cassette whose prelude magic is present but whose
// prelude cannot be used: the file ends inside it, or a prelude line cannot be
// parsed. Callers that index a corpus treat it as a recording still in progress and
// skip the file instead of failing, which is what the substring matching on the error
// text used to express. It is deliberately not returned for a legacy file whose fixed
// header block is malformed, because that is a corrupt recording rather than an
// unfinished one.
var ErrUnusablePrelude = errors.New("cassette prelude is unusable")

// MaxPreludeBytes bounds how much of a cassette ReadPreludeFile will read and hold
// while looking for the end of the prelude. A real prelude is a meta line plus one
// line per event, so this is far above any recording the proxy produces; it exists so
// a corrupt or truncated file cannot make a reader grow without limit.
const MaxPreludeBytes = 8 << 20

// MaxStreamLineBytes bounds one SSE line (one `data:` frame) while recording and while reading a
// recorded stream back. The recorder has always accepted lines this large, so every reader has to
// accept them too: a reader with a smaller cap silently drops a frame the recorder stored in full
// (bufio.Scanner stops with ErrTooLong and the parsers do not check it), which shows up as empty
// content and missing usage for a trace the proxy itself handled. Providers do emit frames above a
// megabyte - a large tool-call argument delta or a base64 content delta - and widening the reader
// bound is the only fix that keeps what was already written readable.
const MaxStreamLineBytes = 4 << 20

// preludeReadChunk is the read size ReadPreludeFile grows its buffer by.
const preludeReadChunk = 32 << 10

// ReadPreludeFile reads the prelude of the cassette at path and nothing else.
//
// A prelude-only caller - anything that wants the metadata, the recorded events or
// the layout rather than the exchange itself - should not read the whole recording,
// because a recording is as large as the upstream response body it holds. The
// prelude sits at the start of the file, so this reads it in chunks and stops at the
// blank line that terminates it.
//
// It requires that terminator rather than trusting ParsePrelude's success: a buffer
// cut at a line boundary before the blank line parses without error and reports a
// payload offset that is short, which would silently misplace every section a caller
// extracts from it afterwards. A file that ends inside its prelude is an error.
func ReadPreludeFile(path string) (*ParsedPrelude, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// The first line tells the two layouts apart. V3 and the pre-rename V3 magic
	// start with a magic line and end the prelude with a blank line; the legacy
	// layout is a fixed-size JSON header block with no terminator.
	reader := bufio.NewReader(f)
	first, err := reader.ReadBytes('\n')
	if len(first) == 0 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("read prelude of %s: %w: %w", path, err, ErrUnusablePrelude)
	}

	if !isMagicLine(bytes.TrimSuffix(first, []byte("\n"))) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		block := make([]byte, LegacyHeaderLen)
		n, readErr := io.ReadFull(f, block)
		if readErr != nil && readErr != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("read legacy prelude of %s: %w", path, readErr)
		}
		if n < LegacyHeaderLen {
			// Deliberately not ErrUnusablePrelude: a file this short is not a recording
			// still being written, because a recording being written arrives without a
			// prelude and starts with its request line. It is a corrupt file, and the
			// indexers report it rather than skipping it silently.
			return nil, fmt.Errorf("prelude of %s ends before its %d-byte header block", path, LegacyHeaderLen)
		}
		return ParsePrelude(block)
	}

	head := make([]byte, 0, preludeReadChunk)
	head = append(head, first...)
	// `first` is loop-invariant: the prelude ends at the first blank line, which
	// is either the line already read or one the loop reads below.
	for !isBlankPreludeLine(first) {
		next, readErr := reader.ReadBytes('\n')
		if len(next) == 0 {
			return nil, fmt.Errorf("prelude of %s has no terminating blank line: %w", path, ErrUnusablePrelude)
		}
		head = append(head, next...)
		if len(head) > MaxPreludeBytes {
			return nil, fmt.Errorf("prelude of %s exceeds %d bytes without a terminator: %w", path, MaxPreludeBytes, ErrUnusablePrelude)
		}
		if isBlankPreludeLine(next) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("prelude of %s has no terminating blank line: %w", path, ErrUnusablePrelude)
		}
	}

	// Only the V3 prelude is wrapped: a malformed legacy header block is a corrupt
	// recording, not an unfinished one.
	if parsed, err := ParsePrelude(head); err != nil {
		return nil, fmt.Errorf("parse prelude of %s: %w: %w", path, err, ErrUnusablePrelude)
	} else {
		return parsed, nil
	}
}

// isBlankPreludeLine reports whether a prelude line is the empty line that ends the
// prelude, in either line ending.
func isBlankPreludeLine(line []byte) bool {
	trimmed := bytes.TrimSuffix(line, []byte("\n"))
	trimmed = bytes.TrimSuffix(trimmed, []byte("\r"))
	return len(trimmed) == 0
}

func parseV3Prelude(content []byte) (*ParsedPrelude, error) {
	var (
		offset  int64
		header  RecordHeader
		events  []RecordEvent
		gotMeta bool
	)

	for len(content) > 0 {
		lineEnd := bytes.IndexByte(content, '\n')
		if lineEnd < 0 {
			return nil, errors.New("scan v3 prelude: missing blank line")
		}

		rawLine := content[:lineEnd]
		line := bytes.TrimSuffix(rawLine, []byte("\r"))
		offset += int64(lineEnd + 1)
		content = content[lineEnd+1:]

		if isMagicLine(line) {
			continue
		}
		if len(line) == 0 {
			break
		}
		if bytes.HasPrefix(line, []byte(metaPrefix)) {
			if err := json.Unmarshal(line[len(metaPrefix):], &header); err != nil {
				return nil, fmt.Errorf("invalid v3 meta line: %w", err)
			}
			gotMeta = true
			continue
		}
		if bytes.HasPrefix(line, []byte(eventPrefix)) {
			var event RecordEvent
			if err := json.Unmarshal(line[len(eventPrefix):], &event); err != nil {
				return nil, fmt.Errorf("invalid v3 event line: %w", err)
			}
			events = append(events, event)
			continue
		}
		return nil, fmt.Errorf("invalid v3 prelude line: %q", string(line))
	}

	if !gotMeta {
		return nil, errors.New("missing v3 meta line")
	}

	return &ParsedPrelude{
		Header:        header,
		Events:        events,
		PayloadOffset: offset,
	}, nil
}

func ExtractSections(content []byte, parsed *ParsedPrelude) (reqFull, reqBody, resFull, resBody []byte) {
	if parsed == nil {
		return nil, nil, nil, nil
	}

	payloadOffset := parsed.PayloadOffset
	if payloadOffset > int64(len(content)) {
		payloadOffset = int64(len(content))
	}

	reqStart := payloadOffset
	reqEnd := reqStart + parsed.Header.Layout.ReqHeaderLen + parsed.Header.Layout.ReqBodyLen
	if reqEnd > int64(len(content)) {
		reqEnd = int64(len(content))
	}
	if reqStart < reqEnd {
		reqFull = content[reqStart:reqEnd]
	}

	reqBodyStart := reqStart + parsed.Header.Layout.ReqHeaderLen
	if reqBodyStart < reqEnd {
		reqBody = content[reqBodyStart:reqEnd]
	}

	resStart := reqEnd + 1
	if resStart > int64(len(content)) {
		resStart = int64(len(content))
	}
	if resStart < int64(len(content)) {
		resFull = content[resStart:]
	}

	resBodyStart := resStart + parsed.Header.Layout.ResHeaderLen
	if resBodyStart < int64(len(content)) {
		resBody = content[resBodyStart:]
	}

	return reqFull, reqBody, resFull, resBody
}

func SummarizeHTTPExchange(content []byte, opts CassetteSummaryOptions) (*HTTPExchangeSummary, error) {
	parsed, err := ParsePrelude(content)
	if err != nil {
		return nil, err
	}
	reqFull, reqBody, resFull, resBody := ExtractSections(content, parsed)

	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(reqFull)))
	if err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(resFull)), req)
	if err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &HTTPExchangeSummary{
		Header: parsed.Header,
		Events: append([]RecordEvent(nil), parsed.Events...),
		Request: HTTPRequestSummary{
			Method:        req.Method,
			URL:           req.URL.String(),
			Header:        cloneHeader(req.Header),
			Body:          summarizeBody(reqBody, normalizeBodyLimit(opts.BodyLimit)),
			BodyBytes:     len(reqBody),
			BodySHA256:    bodySHA256(reqBody),
			BodyTruncated: len(reqBody) > normalizeBodyLimit(opts.BodyLimit),
		},
		Response: HTTPResponseSummary{
			Status:        resp.Status,
			StatusCode:    resp.StatusCode,
			ContentType:   resp.Header.Get("Content-Type"),
			Header:        cloneHeader(resp.Header),
			Body:          summarizeBody(resBody, normalizeBodyLimit(opts.BodyLimit)),
			BodyBytes:     len(resBody),
			BodySHA256:    bodySHA256(resBody),
			BodyTruncated: len(resBody) > normalizeBodyLimit(opts.BodyLimit),
			IsStream:      parsed.Header.Layout.IsStream,
		},
	}, nil
}

func normalizeBodyLimit(limit int) int {
	if limit <= 0 {
		return 4096
	}
	if limit > 20000 {
		return 20000
	}
	return limit
}

func summarizeBody(body []byte, limit int) string {
	if len(body) == 0 {
		return ""
	}
	if len(body) > limit {
		return string(body[:limit])
	}
	return string(body)
}

func bodySHA256(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func cloneHeader(header http.Header) map[string][]string {
	out := make(map[string][]string, len(header))
	for key, values := range header {
		out[key] = append([]string(nil), values...)
	}
	return out
}
