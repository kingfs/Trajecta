package resources

import (
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/observe"
)

func TestExtractDedupesValuesAcrossNodesAndTextViews(t *testing.T) {
	request := observe.SemanticNode{
		ID:   "node-request",
		Text: "edited /home/alice/project/main.go, then called https://api.example.com/repos/1?page=2 twice",
		Raw:  []byte(`{"content":"edited /home/alice/project/main.go"}`),
	}
	response := observe.SemanticNode{
		ID:   "node-response",
		Text: "reading /home/alice/project/main.go; docs https://docs.example.com/guide",
	}
	tool := observe.SemanticNode{
		ID:       "node-tool",
		Metadata: map[string]any{"arguments": `{"file_path":"C:\\Users\\alice\\project\\main.go","url":"https://api.example.com/repos/1?page=2"}`},
	}
	obs := observe.TraceObservation{
		Request:  observe.ObservationRequest{Nodes: []observe.SemanticNode{request, tool}},
		Response: observe.ObservationResponse{Nodes: []observe.SemanticNode{response}},
	}

	got := Extract(obs)
	byValue := make(map[string]Access, len(got))
	for _, access := range got {
		if _, ok := byValue[access.Value]; ok {
			t.Fatalf("value %q was extracted twice", access.Value)
		}
		byValue[access.Value] = access
	}
	if len(got) != 4 {
		t.Fatalf("extracted %d resources, want 4: %+v", len(got), got)
	}

	path := byValue["/home/alice/project/main.go"]
	if path.Kind != KindLocalPath || path.Count != 2 {
		t.Errorf("path = %+v, want local_path seen in 2 nodes", path)
	}
	url := byValue["https://api.example.com/repos/1?page=2"]
	if url.Kind != KindURL || url.Count != 2 {
		t.Errorf("url = %+v, want a URL seen in 2 nodes", url)
	}
	if _, ok := byValue["https://docs.example.com/guide"]; !ok {
		t.Errorf("response URL missing: %+v", byValue)
	}
	if _, ok := byValue[`C:\Users\alice\project\main.go`]; !ok {
		t.Errorf("escaped Windows path was not normalized: %+v", byValue)
	}
	if got[0].Position != 0 || got[1].Position != 1 {
		t.Errorf("positions are not first-appearance order: %+v", got)
	}
}

func TestExtractIgnoresNonLocalSlashText(t *testing.T) {
	node := observe.SemanticNode{
		ID:   "node",
		Text: "POST /v1/chat/completions application/vnd.api+json net/http example.com/path #/definitions/Foo",
	}
	obs := observe.TraceObservation{Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}}}
	if got := Extract(obs); len(got) != 0 {
		t.Fatalf("extracted %+v, want none", got)
	}
}

func TestExtractSkipsPathologicallyLongValues(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", maxValueBytes)
	node := observe.SemanticNode{ID: "node", Text: long}
	obs := observe.TraceObservation{Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}}}
	if got := Extract(obs); len(got) != 0 {
		t.Fatalf("extracted an over-long value: %+v", got)
	}
}

// TestExtractReadsTheEntryRawPayload covers the entry parser's shape: it keeps
// the client request as one raw JSON node rather than decoding tool calls into
// per-node arguments, so the paths and URLs in a tool call are only in Raw.
func TestExtractReadsTheEntryRawPayload(t *testing.T) {
	raw := `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"read","arguments":"{\"file_path\":\"/data/src/github.com/lyramakesmusic/neuron-explorer/README.md\"}"}}]},{"role":"user","content":"see https://gist.github.com/shkr/5e38fedf265feccd733061ecc6a6ad03"}]}`
	node := observe.SemanticNode{ID: "entry-raw", Raw: []byte(raw)}
	obs := observe.TraceObservation{Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}}}
	values := map[string]bool{}
	for _, access := range Extract(obs) {
		values[access.Value] = true
	}
	if !values["/data/src/github.com/lyramakesmusic/neuron-explorer/README.md"] {
		t.Fatalf("tool-call path from the entry raw payload was not extracted: %+v", values)
	}
	if !values["https://gist.github.com/shkr/5e38fedf265feccd733061ecc6a6ad03"] {
		t.Fatalf("URL from the entry raw payload was not extracted: %+v", values)
	}
}

// TestExtractReadsDoubleEscapedWindowsPath covers the entry parser's extra
// escaping layer: a Windows path inside a tool call's arguments is already JSON
// text (two backslashes), and the entry node's re-marshalled raw JSON doubles
// them again. Decoding the raw payload once must still yield one readable path.
func TestExtractReadsDoubleEscapedWindowsPath(t *testing.T) {
	raw := `{"messages":[{"role":"assistant","tool_calls":[{"function":{"arguments":"{\"file_path\":\"C:\\\\Users\\\\alice\\\\project\\\\main.go\"}"}}]}]}`
	node := observe.SemanticNode{ID: "entry-raw-windows", Raw: []byte(raw)}
	obs := observe.TraceObservation{Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}}}
	got := Extract(obs)
	if len(got) != 1 {
		t.Fatalf("extracted %+v, want one path", got)
	}
	if got[0].Kind != KindLocalPath || got[0].Value != `C:\Users\alice\project\main.go` {
		t.Fatalf("path = %+v, want the decoded Windows path", got[0])
	}
}

// TestExtractSkipsPathsInsideCredentialMaterial covers the false positives a
// real trace produced: a private-key body is base64, and a base64 run with two
// slash-separated segments looks exactly like an absolute path. The whole block
// is a credential span, so those candidates are dropped.
func TestExtractSkipsPathsInsideCredentialMaterial(t *testing.T) {
	text := "key:\n-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ/abc/def\n" +
		"-----END OPENSSH PRIVATE KEY-----\n"
	node := observe.SemanticNode{ID: "pem", Text: text}
	obs := observe.TraceObservation{Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}}}
	if got := Extract(obs); len(got) != 0 {
		t.Fatalf("extracted %+v from a private-key body, want none", got)
	}
}

// TestExtractKeepsAURLThatCarriesACredential pins the other side of the
// containment rule: the URL is the access being audited and starts before the
// key, so it must be kept even though a credential span sits inside it.
func TestExtractKeepsAURLThatCarriesACredential(t *testing.T) {
	key := "sk-" + "A" + "AAAAAAAAAAAAAAAAAAAT3BlbkFJ" + "BBBBBBBBBBBBBBBBBBBB"
	url := "https://api.example.com/v1?key=" + key
	node := observe.SemanticNode{ID: "url-with-key", Text: "fetched " + url}
	obs := observe.TraceObservation{Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}}}
	got := Extract(obs)
	if len(got) != 1 || got[0].Kind != KindURL || got[0].Value != url {
		t.Fatalf("extracted %+v, want the URL that carries the key", got)
	}
}
