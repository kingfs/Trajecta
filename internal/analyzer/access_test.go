package analyzer

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kingfs/Trajecta/pkg/observe"
)

func accessFindings(t *testing.T, obs observe.TraceObservation) []observe.Finding {
	t.Helper()
	findings, err := NewRunner().Analyze(context.Background(), obs)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	var out []observe.Finding
	for _, f := range findings {
		switch f.Category {
		case accessCategoryURL, accessCategoryLocalPath:
			out = append(out, f)
		}
	}
	return out
}

func accessByValue(t *testing.T, findings []observe.Finding) map[string]observe.Finding {
	t.Helper()
	out := make(map[string]observe.Finding, len(findings))
	for _, f := range findings {
		value, _ := f.Metadata["value"].(string)
		if value == "" {
			t.Fatalf("finding %+v has no value metadata", f)
		}
		if _, ok := out[value]; ok {
			t.Fatalf("value %q was reported twice", value)
		}
		out[value] = f
	}
	return out
}

// TestAccessDetectorDedupesValuesAcrossRequestAndResponse pins the contract the
// audit is built on: one info finding per distinct path/URL in the trace, even
// when the request and the response both carry it, anchored at the first node
// that showed it.
func TestAccessDetectorDedupesValuesAcrossRequestAndResponse(t *testing.T) {
	request := observe.SemanticNode{
		ID:             "node-request",
		NormalizedType: observe.NodeText,
		Path:           "$.messages[0].content",
		Text: "I edited /home/alice/project/main.go and called " +
			"https://api.example.com/repos/1?page=2, then called " +
			"https://api.example.com/repos/1?page=2 again.",
	}
	response := observe.SemanticNode{
		ID:             "node-response",
		NormalizedType: observe.NodeText,
		Path:           "$.choices[0].message.content",
		Text:           "Reading /home/alice/project/main.go now; docs at https://docs.example.com/guide.",
	}
	obs := observe.TraceObservation{
		TraceID:      "trace-access",
		ExchangeKind: "model",
		Request:      observe.ObservationRequest{Nodes: []observe.SemanticNode{request}},
		Response:     observe.ObservationResponse{Nodes: []observe.SemanticNode{response}},
	}

	findings := accessFindings(t, obs)
	if len(findings) != 3 {
		t.Fatalf("access findings = %d, want 3: %+v", len(findings), findings)
	}
	byValue := accessByValue(t, findings)

	path, ok := byValue["/home/alice/project/main.go"]
	if !ok {
		t.Fatalf("the path was not reported: %+v", byValue)
	}
	if path.Category != accessCategoryLocalPath {
		t.Errorf("path category = %q", path.Category)
	}
	if path.NodeID != request.ID || !strings.Contains(path.EvidencePath, request.ID) {
		t.Errorf("path is not anchored at its first node: %+v", path)
	}
	if got := path.Metadata["steps"]; got != 2 {
		t.Errorf("path steps = %v, want 2 (request and response)", got)
	}

	url1, ok := byValue["https://api.example.com/repos/1?page=2"]
	if !ok {
		t.Fatalf("the first URL was not reported: %+v", byValue)
	}
	if url1.NodeID != request.ID {
		t.Errorf("first URL anchored at %q", url1.NodeID)
	}
	// The URL is written twice in the request text but lives in one node; the
	// count is model steps, not raw matches, so overlapping text views and
	// within-message repeats do not inflate it.
	if got := url1.Metadata["steps"]; got != 1 {
		t.Errorf("first URL steps = %v, want 1", got)
	}

	url2, ok := byValue["https://docs.example.com/guide"]
	if !ok {
		t.Fatalf("the second URL was not reported: %+v", byValue)
	}
	if url2.NodeID != response.ID {
		t.Errorf("second URL anchored at %q, want %q", url2.NodeID, response.ID)
	}

	ids := map[string]bool{}
	for value, f := range byValue {
		if f.Severity != observe.SeverityInfo {
			t.Errorf("%s severity = %q, want info", value, f.Severity)
		}
		if f.Detector != "access" || f.DetectorVersion == "" {
			t.Errorf("%s detector = %q/%q", value, f.Detector, f.DetectorVersion)
		}
		if !strings.Contains(f.EvidenceExcerpt, value) {
			t.Errorf("%s excerpt %q does not carry the value", value, f.EvidenceExcerpt)
		}
		if ids[f.ID] {
			t.Errorf("%s reuses finding id %q", value, f.ID)
		}
		ids[f.ID] = true
	}
}

// TestAccessDetectorReadsToolArguments covers the shape the audit actually
// cares about: a tool call carries the directory it read in its arguments, not
// in prose.
func TestAccessDetectorReadsToolArguments(t *testing.T) {
	node := observe.SemanticNode{
		ID:             "node-tool",
		NormalizedType: observe.NodeToolCall,
		Path:           "$.messages[1].tool_calls[0]",
		Metadata: map[string]any{
			"arguments": `{"file_path":"/etc/hosts","url":"http://localhost:8080/health"}`,
		},
	}
	obs := observe.TraceObservation{
		TraceID: "trace-access-tool",
		Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
	byValue := accessByValue(t, accessFindings(t, obs))
	if _, ok := byValue["/etc/hosts"]; !ok {
		t.Errorf("tool argument path not reported: %+v", byValue)
	}
	if _, ok := byValue["http://localhost:8080/health"]; !ok {
		t.Errorf("tool argument URL not reported: %+v", byValue)
	}
}

// TestAccessDetectorNormalizesEscapedWindowsPaths covers the form a tool call
// actually carries: its arguments are JSON text, so a real backslash is
// doubled. The finding records the readable path, while a decoded UNC root
// keeps its doubled prefix.
func TestAccessDetectorNormalizesEscapedWindowsPaths(t *testing.T) {
	node := observe.SemanticNode{
		ID:             "node-win",
		NormalizedType: observe.NodeText,
		Text:           `{"file_path":"C:\\Users\\alice\\project\\main.go","share":"\\\\fileserver\\share\\report.txt"}`,
	}
	obs := observe.TraceObservation{
		TraceID: "trace-access-win",
		Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
	byValue := accessByValue(t, accessFindings(t, obs))
	if _, ok := byValue[`C:\Users\alice\project\main.go`]; !ok {
		t.Errorf("escaped drive path was not normalized: %+v", byValue)
	}
	if _, ok := byValue[`\\fileserver\share\report.txt`]; !ok {
		t.Errorf("escaped UNC path was not normalized: %+v", byValue)
	}
}

// TestAccessDetectorIgnoresAPIRoutesAndMIMETypes keeps the info findings
// useful: an API path in a transcript is not a directory the agent read.
func TestAccessDetectorIgnoresAPIRoutesAndMIMETypes(t *testing.T) {
	texts := []string{
		"POST /v1/chat/completions",
		"/api/v1/models",
		"application/vnd.api+json",
		"net/http",
		"example.com/path",
		"and/or",
	}
	for _, text := range texts {
		node := observe.SemanticNode{ID: "node", NormalizedType: observe.NodeText, Text: text}
		obs := observe.TraceObservation{
			TraceID: "trace-access-negative",
			Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
		}
		if findings := accessFindings(t, obs); len(findings) != 0 {
			t.Errorf("%q produced %+v, want no access finding", text, findings)
		}
	}
}

// TestAccessDetectorRunsOnEntryExchanges makes the scope rule explicit: the
// client request is exactly where the directories of an already executed call
// appear, so the entry exchange must not be skipped.
func TestAccessDetectorRunsOnEntryExchanges(t *testing.T) {
	node := observe.SemanticNode{
		ID:             "node-entry",
		NormalizedType: observe.NodeText,
		Text:           "read /home/alice/.ssh/config",
	}
	obs := observe.TraceObservation{
		TraceID:      "trace-access-entry",
		ExchangeKind: "entry",
		Request:      observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
	if findings := accessFindings(t, obs); len(findings) != 1 {
		t.Fatalf("entry access findings = %d, want 1: %+v", len(findings), findings)
	}
}

// TestClipAccessValueKeepsUTF8Intact covers the long-value path: the limit is a
// byte count, and a non-ASCII path must not be cut in the middle of a rune.
func TestClipAccessValueKeepsUTF8Intact(t *testing.T) {
	// 5 bytes per unit, so byte 2048 lands inside a rune rather than on its
	// first byte.
	value := strings.Repeat("/a目", 700)
	got := clipAccessValue(value)
	if !utf8.ValidString(got) {
		t.Fatalf("clip produced invalid UTF-8: %q", got)
	}
	if len(got) > accessValueLimit+len("…") {
		t.Fatalf("clip returned %d bytes, limit %d", len(got), accessValueLimit)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clip did not mark the truncation: %q", got)
	}
}
