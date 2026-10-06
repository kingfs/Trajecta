package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestExtractJSONRPCMessageKeepsALargeDataLine pins the SSE-unwrapping bound against the size the
// caller already validated.
//
// readLimited accepts up to server.maxResultBytes, but extractJSONRPCMessage scanned the wrapped
// message with bufio.Scanner's 64 KiB default. A larger single `data:` line made the scan stop before
// any data: line was seen, so the function returned the raw SSE envelope (`event:` + `data:`) and the
// caller reported `decode mcp json-rpc response: invalid character 'e' ...` for a result the server
// had answered correctly. Tool results above 64 KiB (file contents, listings) are ordinary.
func TestExtractJSONRPCMessageKeepsALargeDataLine(t *testing.T) {
	payload := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":%q}]}}`, strings.Repeat("a", 256*1024))
	wrapped := "event: message\ndata: " + payload + "\n\n"

	got := extractJSONRPCMessage([]byte(wrapped))
	if string(got) != payload {
		t.Fatalf("extractJSONRPCMessage returned %d bytes starting with %q, want the %d-byte JSON-RPC payload",
			len(got), firstBytes(got, 24), len(payload))
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unwrapped message is not JSON: %v", err)
	}
}

func firstBytes(data []byte, n int) string {
	if len(data) < n {
		n = len(data)
	}
	return string(data[:n])
}
