package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/router"
)

// fidelityBody carries the number shapes a client can legitimately send and that a decoder
// going through float64 rewrites: an integer above 2^53 (float64 cannot represent every
// one, so it rounds), a 64-bit seed, a decimal with a trailing zero, exponent notation, a
// deeply nested integer, and a string of digits that must stay a string.
const fidelityBody = `{"model":"gpt-5","stream":true,` +
	`"max_tokens":9007199254740993,` +
	`"seed":123456789012345678901,` +
	`"temperature":0.70,` +
	`"top_p":1e-3,` +
	`"metadata":{"account":9007199254740993,"tags":[{"n":123456789012345678901}]},` +
	`"user":"123456789012345678901"}`

// TestInjectStreamOptionsPreservesTheClientsNumbers pins that adding stream_options does not
// rewrite the rest of the request.
//
// The injection is unconditional on a streamed chat completion whose stream_options does not
// already ask for usage, which is the common case, and it re-serializes the body to add the
// field. Decoded through map[string]interface{} every number became a float64, so the
// re-serialized body carried values the client never sent: measured before the fix,
// max_tokens 9007199254740993 came back as 9007199254740992 and seed
// 123456789012345678901 came back as 123456789012345680000. Because the cassette records
// what the proxy forwards, that altered value is also what replay reproduces.
func TestInjectStreamOptionsPreservesTheClientsNumbers(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")

	got := string(injectStreamOptions(req, []byte(fidelityBody)))

	for _, literal := range []string{
		"9007199254740993",
		"123456789012345678901",
		"0.70",
		"1e-3",
		`"user":"123456789012345678901"`,
	} {
		if !strings.Contains(got, literal) {
			t.Fatalf("the rewritten body lost the literal %s:\n got %s", literal, got)
		}
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("the rewritten body is not valid JSON: %v (%s)", err, got)
	}
	opts, ok := payload["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatalf("stream_options was not injected: %s", got)
	}
	if include, _ := opts["include_usage"].(bool); !include {
		t.Fatalf("stream_options.include_usage is not true: %s", got)
	}
	model, _ := payload["model"].(string)
	if model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5 untouched", model)
	}
}

// TestRewriteRequestModelAliasPreservesTheClientsNumbers holds the same guarantee for the
// other body rewrite on this path, the swap from a requested alias to the upstream model.
func TestRewriteRequestModelAliasPreservesTheClientsNumbers(t *testing.T) {
	selection := &router.Selection{
		Target: &router.Target{
			ID:           "channel-a",
			ModelAliases: map[string]string{"fast-alias": "gpt-5-turbo"},
		},
		Request: router.RequestFeatures{ModelName: "fast-alias"},
	}

	body := strings.Replace(fidelityBody, `"model":"gpt-5"`, `"model":"fast-alias"`, 1)
	got, rewritten := rewriteRequestModelAlias([]byte(body), selection)
	if !rewritten {
		t.Fatal("the alias was not rewritten at all")
	}
	out := string(got)
	if !strings.Contains(out, `"model":"gpt-5-turbo"`) {
		t.Fatalf("the alias was not replaced with the upstream model: %s", out)
	}
	for _, literal := range []string{
		"9007199254740993",
		"123456789012345678901",
		"0.70",
		"1e-3",
	} {
		if !strings.Contains(out, literal) {
			t.Fatalf("the rewritten body lost the literal %s:\n got %s", literal, out)
		}
	}
}

// TestBodyRewritesLeaveNonObjectBodiesAlone keeps the rewriter out of bodies it cannot
// faithfully reproduce: a malformed or non-object body is forwarded byte-for-byte.
func TestBodyRewritesLeaveNonObjectBodiesAlone(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	for _, body := range []string{
		`[1,2,3]`,
		`{"stream":true} trailing`,
		`not json at all`,
		``,
	} {
		if got := string(injectStreamOptions(req, []byte(body))); got != body {
			t.Fatalf("injectStreamOptions rewrote %q into %q", body, got)
		}
	}
}
