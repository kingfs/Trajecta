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

// TestExtractIgnoresEscapedCodeShapes pins the false positives a real transcript
// produced after the index normalized doubled separators: escape sequences,
// regex groups and printf formats were stored as if they were paths. The text is
// scanned both as decoded prose and inside one layer of JSON escaping, which is
// the shape an entry parser's raw payload has.
func TestExtractIgnoresEscapedCodeShapes(t *testing.T) {
	texts := []string{
		`print("q:\n%s" % name)`,
		`payload = "\\n\\tid"`,
		`out = "\\n\\n"`,
		`re := regexp.MustCompile(` + "`" + `\\d+)\\.(\\d+` + "`" + `)`,
		`subst := "\\1.\\2"`,
		`code = "if x {\\n\\t\\treturn\\n\\t}"`,
		`cd ../a && ls ../`,
	}
	raw := `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"shell","arguments":"{\"command\":\"printf 'q:\\\\n%s' && cd ../a\"}"}}]}]}`
	for _, text := range texts {
		node := observe.SemanticNode{ID: "escaped-code", Text: text}
		obs := observe.TraceObservation{Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}}}
		if got := Extract(obs); len(got) != 0 {
			t.Errorf("%q extracted %+v, want none", text, got)
		}
	}
	node := observe.SemanticNode{ID: "escaped-code-raw", Raw: []byte(raw)}
	obs := observe.TraceObservation{Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}}}
	if got := Extract(obs); len(got) != 0 {
		t.Errorf("raw payload extracted %+v, want none", got)
	}
}

// TestExtractKeepsRealPathsInProse fixes the other half: removing the prose
// around a path must not remove the path, and a real relative path that follows
// an assignment is still indexed.
func TestExtractKeepsRealPathsInProse(t *testing.T) {
	node := observe.SemanticNode{
		ID:   "prose",
		Text: `registry_file=internal/infrastructure/config/config.go and (modules/identity-tenancy.md`,
	}
	obs := observe.TraceObservation{Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}}}
	values := map[string]bool{}
	for _, access := range Extract(obs) {
		values[access.Value] = true
	}
	for _, want := range []string{
		"internal/infrastructure/config/config.go",
		"modules/identity-tenancy.md",
	} {
		if !values[want] {
			t.Errorf("missing %q from %+v", want, values)
		}
	}
	if len(values) != 2 {
		t.Errorf("extracted %+v, want exactly the two paths", values)
	}
}

// TestLocateFindsTheNodeBehindAValue pins the jump target the resource tree
// uses: the value is searched through the same views Extract indexed, and the
// result names the node and view it came from. A value the detector would not
// index has no occurrence, so an old catalogue row cannot send the reader to an
// arbitrary step.
func TestLocateFindsTheNodeBehindAValue(t *testing.T) {
	request := observe.SemanticNode{
		ID:   "node-message",
		Path: "$.messages[0]",
		Text: "read (modules/identity-tenancy.md and registry_file=internal/infrastructure/config/config.go",
	}
	tool := observe.SemanticNode{
		ID:       "node-call",
		Path:     "$.messages[1].tool_calls[0]",
		Metadata: map[string]any{"arguments": `{"file_path":"C:\\Users\\alice\\project\\main.go"}`},
	}
	obs := observe.TraceObservation{
		Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{request, tool}},
	}

	cases := []struct {
		kind  string
		value string
		node  string
		view  string
	}{
		{kind: KindLocalPath, value: "modules/identity-tenancy.md", node: "node-message", view: "text"},
		{kind: KindLocalPath, value: "internal/infrastructure/config/config.go", node: "node-message", view: "text"},
		{kind: KindLocalPath, value: `C:\Users\alice\project\main.go`, node: "node-call", view: "arguments"},
	}
	for _, tc := range cases {
		got := Locate(obs, tc.kind, tc.value)
		if len(got) != 1 {
			t.Fatalf("Locate(%q) = %+v, want one occurrence", tc.value, got)
		}
		if got[0].NodeID != tc.node || got[0].View != tc.view || got[0].End <= got[0].Start {
			t.Errorf("Locate(%q) = %+v, want node %q view %q", tc.value, got[0], tc.node, tc.view)
		}
		if got[0].NodePath == "" {
			t.Errorf("Locate(%q) has no node path: %+v", tc.value, got[0])
		}
	}

	for _, value := range []string{"", "modules/identity-tenancy.md ", "../a", `\n\tid`} {
		if got := Locate(obs, KindLocalPath, value); len(got) != 0 {
			t.Errorf("Locate(%q) = %+v, want none", value, got)
		}
	}
	if got := Locate(obs, "other", "modules/identity-tenancy.md"); len(got) != 0 {
		t.Errorf("Locate with an unknown kind = %+v, want none", got)
	}
}

// TestLocateReportsEachNodeOnce keeps the jump list short when the same value
// appears in several views of one node (a message and its raw payload, or a tool
// call and its arguments): the conversation only needs the first one.
func TestLocateReportsEachNodeOnce(t *testing.T) {
	node := observe.SemanticNode{
		ID:       "node-both",
		Path:     "$.messages[0]",
		Text:     "open /home/alice/project/main.go",
		Metadata: map[string]any{"arguments": `{"file_path":"/home/alice/project/main.go"}`},
	}
	obs := observe.TraceObservation{Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}}}
	got := Locate(obs, KindLocalPath, "/home/alice/project/main.go")
	if len(got) != 1 {
		t.Fatalf("Locate = %+v, want one occurrence for the node", got)
	}
	if got[0].View != "text" {
		t.Errorf("first view = %q, want text", got[0].View)
	}
}
