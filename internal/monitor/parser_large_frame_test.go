package monitor

import (
	"fmt"
	"strings"
	"testing"
)

// TestMonitorStreamParsersKeepLargeFrames pins the Monitor side of the recording bound: a frame the
// recorder accepted has to be parsed here too.
//
// All three stream parsers used a 1 MiB bufio.Scanner cap without checking scanner.Err(), so a larger
// single-line `data:` frame (a large tool-call argument delta, a base64 content delta) made the parser
// return the frames before it and silently drop everything from that line on - the trace detail and the
// Observation IR showed partial content for a cassette that was complete on disk. Each parser gets its
// real event shape: the Anthropic parser only maps a delta after content_block_start.
func TestMonitorStreamParsersKeepLargeFrames(t *testing.T) {
	large := strings.Repeat("y", 2*1024*1024)

	t.Run("anthropic", func(t *testing.T) {
		body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"content\":[]}}\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n" +
			fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n", large)
		content, _, _, _ := parseAnthropicStreamOutput([]byte(body))
		if len(content) != len(large) {
			t.Fatalf("parseAnthropicStreamOutput content = %d bytes, want %d", len(content), len(large))
		}
	})

	t.Run("chat completions", func(t *testing.T) {
		body := fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n", large)
		content, _, _ := parseChatCompletionsOutput([]byte(body), true)
		if len(content) != len(large) {
			t.Fatalf("parseChatCompletionsOutput content = %d bytes, want %d", len(content), len(large))
		}
	})

	t.Run("responses", func(t *testing.T) {
		body := fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n", large)
		content, _, _, _ := parseResponsesStreamOutput([]byte(body))
		if len(content) != len(large) {
			t.Fatalf("parseResponsesStreamOutput content = %d bytes, want %d", len(content), len(large))
		}
	})
}
