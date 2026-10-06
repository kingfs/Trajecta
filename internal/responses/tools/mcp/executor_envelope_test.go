package mcp

import (
	"encoding/json"
	"testing"
)

// TestExtractJSONRPCMessageHandlesSSEFieldShapes pins which wrapped responses are unwrapped.
//
// The unwrapper only recognised a body starting with `event:` or `data:`, so an SSE response that began
// with the comment/keep-alive, `retry:` or `id:` field - all ordinary and all allowed before the data
// line - was returned as its own envelope and then failed to decode as JSON. It also returned only the
// first `data:` line, while the SSE spec joins the data lines of one event, so a payload split over two
// lines came out as invalid JSON. The unwrapping now walks the event's fields and stops at its blank
// line, and it still passes a plain JSON-RPC body through untouched - including one whose payload
// merely starts with a `data` key, which is why the detection only looks at the first field.
func TestExtractJSONRPCMessageHandlesSSEFieldShapes(t *testing.T) {
	payload := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`
	splitPayload := "{\"jsonrpc\":\"2.0\",\"id\":1,\n\"result\":{\"content\":[]}}"

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain json body", raw: payload, want: payload},
		{name: "json body starting with a data key", raw: `{"data":{"a":1}}`, want: `{"data":{"a":1}}`},
		{name: "unknown leading field", raw: "foo: bar\n" + payload, want: "foo: bar\n" + payload},
		{name: "data line only", raw: "data: " + payload + "\n\n", want: payload},
		{name: "event and data", raw: "event: message\ndata: " + payload + "\n\n", want: payload},
		{name: "comment preamble", raw: ": keep-alive\ndata: " + payload + "\n\n", want: payload},
		{name: "retry preamble", raw: "retry: 1000\ndata: " + payload + "\n\n", want: payload},
		{name: "id field preamble", raw: "id: 1\ndata: " + payload + "\n\n", want: payload},
		{name: "data line without a space", raw: "data:" + payload + "\n\n", want: payload},
		{name: "payload split over two data lines", raw: "data: {\"jsonrpc\":\"2.0\",\"id\":1,\ndata: \"result\":{\"content\":[]}}\n\n", want: splitPayload},
		{name: "first event wins", raw: "data: " + payload + "\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":2}\n\n", want: payload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(extractJSONRPCMessage([]byte(tc.raw)))
			if got != tc.want {
				t.Fatalf("extractJSONRPCMessage() = %q, want %q", got, tc.want)
			}
			if got == tc.raw && tc.want != tc.raw {
				t.Fatalf("extractJSONRPCMessage() returned the envelope unchanged: %q", got)
			}
		})
	}

	t.Run("unwrapped payloads decode", func(t *testing.T) {
		for _, raw := range []string{
			"event: message\ndata: " + payload + "\n\n",
			": keep-alive\ndata: " + payload + "\n\n",
			"retry: 1000\ndata: " + payload + "\n\n",
			"id: 1\ndata: " + payload + "\n\n",
			"data: " + payload + "\n\n",
		} {
			var decoded map[string]any
			if err := json.Unmarshal(extractJSONRPCMessage([]byte(raw)), &decoded); err != nil {
				t.Fatalf("unwrapped %q does not decode: %v", raw, err)
			}
		}
	})
}
