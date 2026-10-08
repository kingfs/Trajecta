package recorder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kingfs/Trajecta/internal/redaction"
	responsesaudit "github.com/kingfs/Trajecta/internal/responses/audit"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

type PromptTokenDetails = recordfile.PromptTokenDetails
type UsageInfo = recordfile.UsageInfo
type LayoutInfo = recordfile.LayoutInfo
type MetaData = recordfile.MetaData
type RecordHeader = recordfile.RecordHeader
type RecordEvent = recordfile.RecordEvent

type LogInfo struct {
	File   *os.File
	Path   string
	Header RecordHeader
	Events []RecordEvent
}

type PrepareOptions struct {
	SiteURL                        string
	SelectedUpstreamID             string
	SelectedUpstreamProviderPreset string
	RoutingPolicy                  string
	RoutingScore                   float64
	RoutingCandidateCount          int
	RoutingFailureReason           string
	ExchangeID                     string
	ExchangeKind                   string
	ExchangeRole                   string
	ParentExchangeID               string
	SequenceIndex                  int
	TraceID                        string
	ResponseID                     string
}

type Recorder struct {
	OutputDir string
	MaskKey   bool
	store     *store.Store
	finalizer *asyncFinalizer
}

func New(outputDir string, maskKey bool, st *store.Store) *Recorder {
	r := &Recorder{
		OutputDir: outputDir,
		MaskKey:   maskKey,
		store:     st,
	}
	r.finalizer = newAsyncFinalizer(r.UpdateLogFile, DefaultFinalizeWorkers, DefaultFinalizeQueueDepth)
	return r
}

// SubmitLogFile finalises a completed recording on the background finalize
// queue and returns as soon as it is queued, so the proxy's request goroutine
// does not wait for the cassette rewrite. A full queue finalises inline instead
// of dropping the recording, so the return value still reports a real failure.
//
// The recording must not be touched after this call: ownership passes to the
// worker, exactly as it did to the synchronous call this replaces.
func (r *Recorder) SubmitLogFile(info *LogInfo) error {
	if r == nil {
		return nil
	}
	if r.finalizer == nil {
		return r.UpdateLogFile(info)
	}
	return r.finalizer.submit(info)
}

// StartFinalizeWorkers starts the background finalizers. Nothing is queued
// before this call: SubmitLogFile falls back to the inline path until then, so a
// caller that never starts them keeps today's behaviour.
func (r *Recorder) StartFinalizeWorkers() {
	if r == nil || r.finalizer == nil {
		return
	}
	r.finalizer.start()
}

func (r *Recorder) PrepareLogFile(req *http.Request, siteURL string) (*LogInfo, error) {
	return r.PrepareLogFileWithOptions(req, PrepareOptions{SiteURL: siteURL})
}

// tracePathSegment turns an untrusted value into a path below the trace root.
//
// The model name is read from the request body, so it is client input on a
// filesystem path, and it used to be joined in raw. A NUL byte made os.MkdirAll
// fail with `invalid argument`, which lost the whole trace - the cassette was
// never written and only an ERROR line remained - and a `..` component escaped
// the trace root because filepath.Join cleans the result, so a request could
// create directories anywhere the process can write.
//
// Slashes are kept because a model slug such as `qwen/qwen3.6-35b-a3b` nests
// legitimately, but an empty, `.` or `..` component is path structure rather than
// a name and is dropped, control characters and backslashes (a separator on
// Windows) are removed, and the result is bounded so an over-long model name
// cannot fail the mkdir with ENAMETOOLONG.
func tracePathSegment(value string, limit int) string {
	parts := strings.Split(value, "/")
	safe := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Map(func(r rune) rune {
			switch {
			case r < 0x20, r == 0x7f:
				return -1
			case r == '\\':
				return '_'
			default:
				return r
			}
		}, part)
		part = strings.Trim(part, " ")
		if part == "" || part == "." || part == ".." {
			continue
		}
		safe = append(safe, part)
	}
	if len(safe) == 0 {
		return "unknown-model"
	}
	joined := strings.Join(safe, "/")
	if limit > 0 && len(joined) > limit {
		joined = joined[:limit]
		// Truncating can leave a trailing separator or a partial rune, neither of
		// which belongs in a path component.
		joined = strings.TrimRight(joined, "/")
		for joined != "" && !utf8.ValidString(joined) {
			joined = joined[:len(joined)-1]
		}
		joined = strings.TrimRight(joined, "/")
		if joined == "" {
			return "unknown-model"
		}
	}
	return joined
}

func (r *Recorder) PrepareLogFileWithOptions(req *http.Request, opts PrepareOptions) (*LogInfo, error) {
	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	}
	return r.PrepareLogFileWithOptionsAndBody(req, opts, bodyBytes)
}

func (r *Recorder) PrepareLogFileWithOptionsAndBody(req *http.Request, opts PrepareOptions, bodyBytes []byte) (*LogInfo, error) {
	modelName := "unknown-model"
	if len(bodyBytes) > 0 {
		if parsedReq, err := llm.ParseRequestForPath(req.URL.Path, opts.SiteURL, bodyBytes); err == nil && parsedReq.Model != "" {
			modelName = parsedReq.Model
		} else {
			var payload struct {
				Model string `json:"model"`
				Name  string `json:"name"`
			}
			if json.Unmarshal(bodyBytes, &payload) == nil && payload.Model != "" {
				modelName = payload.Model
			} else if payload.Name != "" {
				modelName = payload.Name
			}
		}
	}
	if modelName == "unknown-model" && strings.HasSuffix(req.URL.Path, "/models") {
		modelName = "list_models"
	}
	if modelName == "unknown-model" {
		if inferred := llm.ModelFromPath(req.URL.Path); inferred != "" {
			modelName = inferred
		}
	}

	u, _ := url.Parse(opts.SiteURL)
	siteHost := "unknown"
	if u != nil {
		siteHost = u.Host
	}

	now := time.Now()
	semantics := llm.ClassifyHTTPRequest(req, opts.SiteURL)
	dirPath := filepath.Join(
		r.OutputDir,
		tracePathSegment(siteHost, 128),
		tracePathSegment(modelName, 200),
		now.Format("2006"),
		now.Format("01"),
		now.Format("02"),
	)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return nil, err
	}

	fileName := fmt.Sprintf("%s_%d.http", now.Format("20060102_150405"), now.Nanosecond())
	logPath := filepath.Join(dirPath, fileName)

	f, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}

	originalHeaders := map[string]string{}
	if r.MaskKey {
		for _, name := range []string{"Authorization", "api-key", "x-api-key", "x-goog-api-key"} {
			if value := req.Header.Get(name); value != "" {
				originalHeaders[name] = value
				switch name {
				case "Authorization":
					req.Header.Set(name, "Bearer fake-key-logging")
				default:
					req.Header.Set(name, "fake-key-logging")
				}
			}
		}
	}
	reqDump, err := httputil.DumpRequest(req, false)
	if r.MaskKey {
		for name, value := range originalHeaders {
			req.Header.Set(name, value)
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}

	nHead, err := f.Write(reqDump)
	if err != nil {
		f.Close()
		return nil, err
	}

	nBody, err := f.Write(bodyBytes)
	if err != nil {
		f.Close()
		return nil, err
	}

	header := RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: MetaData{
			RequestID:                      fmt.Sprintf("%d", now.UnixNano()),
			RequestAuditID:                 requestAuditIDFromRequest(req),
			ClientRequestID:                req.Header.Get("X-Client-Request-Id"),
			ExchangeID:                     opts.ExchangeID,
			ExchangeKind:                   opts.ExchangeKind,
			ExchangeRole:                   opts.ExchangeRole,
			ParentExchangeID:               opts.ParentExchangeID,
			SequenceIndex:                  opts.SequenceIndex,
			TraceID:                        opts.TraceID,
			ResponseID:                     opts.ResponseID,
			Time:                           now,
			Model:                          modelName,
			Provider:                       semantics.Provider,
			Operation:                      semantics.Operation,
			Endpoint:                       semantics.Endpoint,
			URL:                            redaction.DisplayURL(req.URL.String()),
			Method:                         req.Method,
			ClientIP:                       req.RemoteAddr,
			SelectedUpstreamID:             opts.SelectedUpstreamID,
			SelectedUpstreamBaseURL:        redaction.DisplayURL(opts.SiteURL),
			SelectedUpstreamProviderPreset: opts.SelectedUpstreamProviderPreset,
			RoutingPolicy:                  opts.RoutingPolicy,
			RoutingScore:                   opts.RoutingScore,
			RoutingCandidateCount:          opts.RoutingCandidateCount,
			RoutingFailureReason:           opts.RoutingFailureReason,
		},
		Layout: LayoutInfo{
			ReqHeaderLen: int64(nHead),
			ReqBodyLen:   int64(nBody),
		},
	}

	return &LogInfo{
		File:   f,
		Path:   logPath,
		Header: header,
	}, nil
}

func requestAuditIDFromRequest(req *http.Request) string {
	if req == nil {
		return ""
	}
	id, _ := responsesaudit.RequestAuditIDFromContext(req.Context())
	return id
}

// ApplyRoutingDetailFromEvents mirrors the routing facts the Monitor's routing
// summary aggregates out of the prelude events and into the meta header.
//
// The summary used to get those facts by opening every cassette in its window and
// parsing the prelude, at one random seek per trace - measured at 57 ms on the
// deployment's rotational disk, which made the default "today" window take 49 s.
// The events remain the durable, replayable record; this copies the handful of
// values the summary groups by into the indexed row. The proxy fills in the
// columns it already knows (the selected upstream and the failure reason) and
// this only supplies them when it did not.
//
// A request can emit several routing events - one selection per retry attempt, a
// sticky decision, a filter rejection - but the row holds one value each. The
// last event that carries a value wins, so the recorded facts describe the
// outcome the request finished with rather than an attempt it abandoned. That is
// a deliberate narrowing: the summary now reports one decision per request
// instead of counting every event, which is also why it no longer counts a
// request twice when `routing.selection` and `routing.selected` agree.
func ApplyRoutingDetailFromEvents(events []RecordEvent, meta *MetaData) {
	if meta == nil {
		return
	}
	for _, event := range events {
		switch {
		case event.Type == "routing.selection" || event.Type == "routing.selected":
			// The meta header already carries the dispatched upstream when the
			// proxy wrote it; this is the reconstruction path used when a row is
			// rebuilt from the cassette alone.
			setIfEmpty(&meta.SelectedUpstreamID, eventStringAttr(event.Attributes, "upstream_id"))
			setLastNonEmpty(&meta.RouteTargetID, eventStringAttr(event.Attributes, "route_target_id"))
			setLastNonEmpty(&meta.ChannelID, eventStringAttr(event.Attributes, "channel_id"))
			setLastNonEmpty(&meta.CredentialID, eventStringAttr(event.Attributes, "credential_id"))
		case event.Type == "routing.filtered":
			setIfEmpty(&meta.RoutingFailureReason, eventStringAttr(event.Attributes, "routing_failure_reason"))
		case event.Type == "routing.retry_queue_saturated":
			setIfEmpty(&meta.RoutingFailureReason, "retry_queue_saturated")
		case strings.HasPrefix(event.Type, "routing.sticky."):
			status := strings.TrimPrefix(event.Type, "routing.sticky.")
			if attrStatus := eventStringAttr(event.Attributes, "sticky_status"); attrStatus != "" {
				status = attrStatus
			}
			setLastNonEmpty(&meta.StickyStatus, status)
			setLastNonEmpty(&meta.StickyPreviousUpstreamID, eventStringAttr(event.Attributes, "previous_upstream_id"))
			// A sticky decision repeats the selected identity, so it is also a
			// valid source when a trace recorded sticky state without a
			// `routing.selection` event.
			setLastNonEmpty(&meta.RouteTargetID, eventStringAttr(event.Attributes, "route_target_id"))
			setLastNonEmpty(&meta.ChannelID, eventStringAttr(event.Attributes, "channel_id"))
			setLastNonEmpty(&meta.CredentialID, eventStringAttr(event.Attributes, "credential_id"))
		}
	}
}

// setIfEmpty fills a column the proxy owns only when it left it blank.
func setIfEmpty(dst *string, value string) {
	if *dst == "" && value != "" {
		*dst = value
	}
}

// setLastNonEmpty keeps the most recent value, so a retried request records the
// attempt it finished with.
func setLastNonEmpty(dst *string, value string) {
	if value != "" {
		*dst = value
	}
}

func eventStringAttr(attrs map[string]interface{}, key string) string {
	if attrs == nil {
		return ""
	}
	value, ok := attrs[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

func (r *Recorder) UpdateLogFile(info *LogInfo) error {
	if info.File == nil {
		return nil
	}
	defer info.File.Close()

	events := recordfile.BuildEvents(info.Header)
	if len(info.Events) > 0 {
		events = append(events, info.Events...)
	}
	// Mirror the aggregated routing facts into the meta header before it is
	// marshalled: the header is what the index row and the cassette prelude are
	// both built from, so deriving it here keeps the write path and the
	// `Sync` re-index of the same file in agreement without a second parser.
	ApplyRoutingDetailFromEvents(events, &info.Header.Meta)
	prelude, err := recordfile.MarshalPrelude(info.Header, events)
	if err != nil {
		return err
	}

	// The cassette is written record-first and the prelude can only be prepended
	// once the exchange is complete, so finalising it means rewriting the file.
	// Stream the record through a temporary file in the same directory and rename
	// it over the original: a recording is as large as the response body it holds,
	// and neither of those should have to fit in memory or be briefly visible as a
	// truncated file.
	requestHead, err := prependPrelude(info, prelude)
	if err != nil {
		return err
	}

	if r.store != nil {
		// The grouping identifiers come from the request header block alone, which
		// prependPrelude read back while the record was still available.
		grouping, err := store.ExtractGroupingInfoFromRequestHeaders(requestHead)
		if err != nil {
			return err
		}
		if err := r.store.UpsertLogWithGrouping(info.Path, info.Header, grouping); err != nil {
			return err
		}
		entry, err := r.store.GetByRequestID(info.Header.Meta.RequestID)
		if err != nil {
			slog.Warn("Trace parse job enqueue skipped: indexed trace not found", "request_id", info.Header.Meta.RequestID, "error", err)
		} else if err := r.store.EnqueueParseJob(entry.ID); err != nil {
			slog.Warn("Trace parse job enqueue failed", "trace_id", entry.ID, "error", err)
		}
	}

	return nil
}

// prependPrelude rewrites the cassette at info.Path as prelude followed by the
// record it already holds, and returns the request header block it read back on
// the way. The record is copied with io.Copy through a temporary file in the same
// directory and renamed into place, so the memory cost is the copy buffer rather
// than the recording, and a reader sees either the previous complete file or the
// new one instead of the truncated window a rewrite in place would expose.
func prependPrelude(info *LogInfo, prelude []byte) ([]byte, error) {
	tmp, err := os.CreateTemp(filepath.Dir(info.Path), ".cassette-rewrite-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	// os.CreateTemp creates the file 0600; a cassette keeps the permissions it was
	// created with so a reader group does not lose access after finalisation.
	if stat, statErr := os.Stat(info.Path); statErr == nil {
		if chmodErr := tmp.Chmod(stat.Mode().Perm()); chmodErr != nil {
			discard()
			return nil, chmodErr
		}
	}

	if _, err := tmp.Write(prelude); err != nil {
		discard()
		return nil, err
	}
	if _, err := info.File.Seek(0, io.SeekStart); err != nil {
		discard()
		return nil, err
	}
	if _, err := io.Copy(tmp, info.File); err != nil {
		discard()
		return nil, err
	}

	var requestHead []byte
	if headLen := info.Header.Layout.ReqHeaderLen; headLen > 0 {
		requestHead = make([]byte, headLen)
		if _, err := tmp.ReadAt(requestHead, int64(len(prelude))); err != nil {
			discard()
			return nil, err
		}
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if err := os.Rename(tmpPath, info.Path); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	return requestHead, nil
}
