package detector

import (
	"bytes"
	"testing"

	"github.com/kingfs/Trajecta/pkg/detect"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// spanHas reports whether needle occurs wholly inside one of spans.
func spanHas(body []byte, spans []detect.Span, needle string) bool {
	idx := bytes.Index(body, []byte(needle))
	if idx < 0 {
		return false
	}
	end := idx + len(needle)
	for _, s := range spans {
		if s.Start <= idx && end <= s.End {
			return true
		}
	}
	return false
}

func mustScope(t *testing.T, protocol, body string, opts ScopeOptions) (RequestScope, []byte) {
	t.Helper()
	scope, ok := ParseRequestScope(protocol, []byte(body), opts)
	if !ok {
		t.Fatalf("ParseRequestScope(%q) = false, want a scope", protocol)
	}
	return scope, []byte(body)
}

// The chat-completions codec can never produce a must-round-trip block, so the
// body-scoped hot path must not pay for the projection at all.
func TestScopeChatShortCircuits(t *testing.T) {
	body := `{"model":"gpt-5","messages":[{"role":"user","content":"key ` + corpusAWSKey + `"}]}`
	if _, ok := ParseRequestScope("chat_completions", []byte(body), ScopeOptions{}); ok {
		t.Fatal("chat_completions must not be projected when no plugin needs fields")
	}
}

// A field-scoped plugin forces the projection on chat too, and the message text
// lands on request.message.
func TestScopeChatFieldsWhenRequired(t *testing.T) {
	body := `{"model":"gpt-5","messages":[{"role":"system","content":"be careful"},{"role":"user","content":"key ` + corpusAWSKey + `"}]}`
	scope, raw := mustScope(t, "chat_completions", body, ScopeOptions{RequireFields: true})
	if !spanHas(raw, scope.Fields(hook.PointRequestSystem), "be careful") {
		t.Fatalf("system text missing from request.system: %+v", scope.Fields(hook.PointRequestSystem))
	}
	if !spanHas(raw, scope.Fields(hook.PointRequestMessage), corpusAWSKey) {
		t.Fatalf("user text missing from request.message: %+v", scope.Fields(hook.PointRequestMessage))
	}
}

func TestScopeSealedOnlyWhenMustRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		protocol  string
		body      string
		needle    string
		wantSeal  bool
		wantField hook.Point
	}{
		{
			name:     "responses encrypted reasoning",
			protocol: "responses",
			body: `{"model":"gpt-5","input":[{"type":"reasoning","encrypted_content":"` + corpusAWSKey +
				`gAAAA","summary":[]},{"type":"message","role":"user","content":[{"type":"input_text","text":"key ` + corpusAWSKey + `"}]}]}`,
			needle:   corpusAWSKey + `gAAAA`,
			wantSeal: true,
		},
		{
			name:     "messages redacted thinking",
			protocol: "messages",
			body: `{"model":"claude","max_tokens":8,"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"` +
				corpusAWSKey + `b64"}]}]}`,
			needle:   corpusAWSKey + `b64`,
			wantSeal: true,
		},
		{
			name:     "gemini thought part",
			protocol: "gemini",
			body:     `{"contents":[{"role":"model","parts":[{"text":"my key is ` + corpusAWSKey + `","thought":true}]}]}`,
			needle:   "my key is " + corpusAWSKey,
			wantSeal: true,
		},
		{
			// A signature can be attached to a functionCall part too; the whole
			// part must round-trip, arguments included.
			name:     "gemini signed function call",
			protocol: "gemini",
			body: `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"run","args":{"key":"` +
				corpusAWSKey + `"}},"thoughtSignature":"c2ln"}]}]}`,
			needle:   corpusAWSKey,
			wantSeal: true,
		},
		{
			// An unmodeled native part is preserved by the codec but is not
			// signature-bound: it is ordinary content and must stay scannable.
			name:      "gemini executable code is content",
			protocol:  "gemini",
			body:      `{"contents":[{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"key='` + corpusAWSKey + `'"}}]}]}`,
			needle:    corpusAWSKey,
			wantField: hook.PointRequestMessage,
		},
		{
			name:      "responses unknown item is content",
			protocol:  "responses",
			body:      `{"model":"gpt-5","input":[{"type":"web_search_call","id":"x","query":"` + corpusAWSKey + `"}]}`,
			needle:    corpusAWSKey,
			wantField: hook.PointRequestMessage,
		},
		{
			// An Anthropic thinking block has no Sealed value in the IR (the
			// signature is protected by the Vault's opaque guard instead), so
			// the scope must not claim it as sealed.
			name:      "messages thinking with signature stays ordinary",
			protocol:  "messages",
			body:      `{"model":"claude","max_tokens":8,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"key ` + corpusAWSKey + `","signature":"sig"}]}]}`,
			needle:    "key " + corpusAWSKey,
			wantField: hook.PointRequestMessage,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope, raw := mustScope(t, tc.protocol, tc.body, ScopeOptions{RequireFields: true})
			sealed := scope.SealedSpans()
			if spanHas(raw, sealed, tc.needle) != tc.wantSeal {
				t.Fatalf("sealed coverage = %v, want %v; sealed=%+v", spanHas(raw, sealed, tc.needle), tc.wantSeal, sealed)
			}
			if tc.wantField != "" {
				if !scope.HasPoint(tc.wantField) {
					t.Fatalf("no field spans for %s", tc.wantField)
				}
				if !spanHas(raw, scope.Fields(tc.wantField), tc.needle) {
					t.Fatalf("needle missing from %s fields: %+v", tc.wantField, scope.Fields(tc.wantField))
				}
			}
			if tc.wantSeal {
				for point, spans := range scope.fields {
					if spanHas(raw, spans, tc.needle) {
						t.Fatalf("sealed needle also appears in field %s", point)
					}
				}
			}
		})
	}
}

func TestScopeFieldsPerPoint(t *testing.T) {
	body := `{"model":"gpt-5","messages":[` +
		`{"role":"system","content":"system text"},` +
		`{"role":"assistant","content":"hi","tool_calls":[{"id":"call_1","type":"function","function":{"name":"run","arguments":"{\"cmd\":\"echo ` + corpusAWSKey + `\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"tool result ` + corpusAWSKey + `"},` +
		`{"role":"user","content":[{"type":"text","text":"user text"}]}],` +
		`"tools":[{"type":"function","function":{"name":"run","description":"desc ` + corpusAWSKey + `"}}]}`
	scope, raw := mustScope(t, "chat_completions", body, ScopeOptions{RequireFields: true})
	for point, needle := range map[hook.Point]string{
		hook.PointRequestSystem:     "system text",
		hook.PointRequestToolCall:   `echo ` + corpusAWSKey,
		hook.PointRequestToolResult: "tool result " + corpusAWSKey,
		hook.PointRequestToolDecl:   "desc " + corpusAWSKey,
		hook.PointRequestMessage:    "user text",
	} {
		if !spanHas(raw, scope.Fields(point), needle) {
			t.Fatalf("%s does not cover %q: %+v", point, needle, scope.Fields(point))
		}
	}
	// FieldsFor merges the requested points and nothing else.
	combined := scope.FieldsFor([]hook.Point{hook.PointRequestSystem, hook.PointRequestMessage})
	if !spanHas(raw, combined, "system text") || !spanHas(raw, combined, "user text") {
		t.Fatalf("FieldsFor lost a requested point: %+v", combined)
	}
	if spanHas(raw, combined, "desc "+corpusAWSKey) {
		t.Fatalf("FieldsFor leaked an undeclared point: %+v", combined)
	}
}

// Content nested inside a Messages tool_result belongs to request.tool_result,
// not request.message.
func TestScopeStructuredToolResultPoint(t *testing.T) {
	body := `{"model":"claude","max_tokens":8,"messages":[{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"nested OUTPUT"}]}]}]}`
	scope, raw := mustScope(t, "messages", body, ScopeOptions{RequireFields: true})
	if !spanHas(raw, scope.Fields(hook.PointRequestToolResult), "nested OUTPUT") {
		t.Fatalf("nested tool result text missing from request.tool_result: %+v", scope.fields)
	}
	if spanHas(raw, scope.Fields(hook.PointRequestMessage), "nested OUTPUT") {
		t.Fatalf("nested tool result text leaked into request.message: %+v", scope.Fields(hook.PointRequestMessage))
	}
}

func TestScopePrefilter(t *testing.T) {
	plain := `{"model":"claude","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`
	if _, ok := ParseRequestScope("messages", []byte(plain), ScopeOptions{}); ok {
		t.Fatal("a body with no sealed marker must not be projected for the body-only hot path")
	}
	if _, ok := ParseRequestScope("messages", []byte(plain), ScopeOptions{RequireFields: true}); !ok {
		t.Fatal("a field-scoped plugin must force the projection")
	}
}

func TestScopeMaxBytes(t *testing.T) {
	body := `{"model":"claude","max_tokens":8,"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"` + corpusAWSKey + `"}]}]}`
	if _, ok := ParseRequestScope("messages", []byte(body), ScopeOptions{MaxBytes: 10}); ok {
		t.Fatal("a body over MaxBytes must fall back")
	}
	if _, ok := ParseRequestScope("messages", []byte(body), ScopeOptions{MaxBytes: -1}); !ok {
		t.Fatal("a negative MaxBytes disables the bound")
	}
}

func TestScopeUnknownProtocolAndMalformed(t *testing.T) {
	if _, ok := ParseRequestScope("unknown", []byte(`{}`), ScopeOptions{}); ok {
		t.Fatal("unknown protocol must fall back")
	}
	if _, ok := ParseRequestScope("messages", []byte(`not json`), ScopeOptions{}); ok {
		t.Fatal("malformed body must fall back")
	}
}

func TestMergeSpans(t *testing.T) {
	cases := []struct {
		name string
		in   []detect.Span
		n    int
		want []detect.Span
	}{
		{"none", nil, 10, nil},
		{"overlap", []detect.Span{{Start: 2, End: 5}, {Start: 3, End: 7}}, 10, []detect.Span{{Start: 2, End: 7}}},
		{"unsorted", []detect.Span{{Start: 6, End: 8}, {Start: 1, End: 3}}, 10, []detect.Span{{Start: 1, End: 3}, {Start: 6, End: 8}}},
		{"adjacent", []detect.Span{{Start: 1, End: 3}, {Start: 3, End: 5}}, 10, []detect.Span{{Start: 1, End: 5}}},
		{"invalid", []detect.Span{{Start: -1, End: 3}, {Start: 5, End: 5}}, 10, nil},
		{"bounded", []detect.Span{{Start: 1, End: 3}, {Start: 8, End: 20}}, 10, []detect.Span{{Start: 1, End: 3}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeSpans(tc.in, tc.n)
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %+v, want %+v", got, tc.want)
				}
			}
		})
	}
}

// The builtin scanner keeps whole-body coverage even when the projection exists
// for a field-scoped plugin: a credential in a structurally unprojected position
// (a JSON key, user, metadata) must still be found.
func TestScopedCollectionKeepsBuiltinCoverage(t *testing.T) {
	body := []byte(`{"model":"gpt-5","user":"` + corpusAWSKey + `","metadata":{"token":"` + corpusAWSKey +
		`"},"messages":[{"role":"user","content":"hi"}],"` + corpusAWSKey + `":"v"}`)
	rt, err := NewRuntime(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	snap := rt.Snapshot()
	scope, ok := ParseRequestScope("chat_completions", body, ScopeOptions{RequireFields: true})
	if !ok {
		t.Fatal("scope required")
	}
	cands := snap.collectCandidatesScoped(body, scope)
	if len(cands) != 3 {
		t.Fatalf("builtin candidates = %d, want 3 (user, metadata, key): %+v", len(cands), cands)
	}
}
