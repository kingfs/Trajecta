package chatclient

import (
	"fmt"
	"strings"
	"testing"
)

// TestAggregateChatCompletionStreamKeepsLargeFrame pins that the local Responses runtime survives a
// large upstream frame.
//
// The aggregator reads a live stream with a 1 MiB bufio.Scanner cap, so an upstream frame above it
// failed the whole call with `bufio.Scanner: token too long` - a streamed /v1/responses translation
// returned nothing even though the same upstream call passes through the recording path untouched,
// which tolerates four times the frame size. The cap is now the shared recordfile bound.
func TestAggregateChatCompletionStreamKeepsLargeFrame(t *testing.T) {
	content := strings.Repeat("z", 2*1024*1024)
	body := fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\ndata: [DONE]\n\n", content)

	resp, err := AggregateChatCompletionStreamWithCallback(strings.NewReader(body), nil)
	if err != nil {
		t.Fatalf("AggregateChatCompletionStreamWithCallback() error = %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("aggregated %d choices, want 1", len(resp.Choices))
	}
	text, _ := resp.Choices[0].Message.Content.(string)
	if len(text) != len(content) {
		t.Fatalf("aggregated content = %d bytes, want %d", len(text), len(content))
	}
}
