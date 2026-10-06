package llm

import (
	"fmt"
	"strings"
	"testing"
)

// TestRecordedLargeSSEFrameIsReadBackInFull pins the recording/reading bound: whatever the recorder
// accepted has to come back out of the derivation readers.
//
// Production frames above the readers' old 1 MiB cap (a large tool-call argument delta, a base64
// content delta) were recorded in full by ResponsePipeline and then silently dropped by the stream
// readers - bufio.Scanner stops with ErrTooLong and the parsers do not check scanner.Err() - so a
// trace the proxy handled showed empty content and no usage in the Monitor and in the derived
// metadata, with no error anywhere. The frame below is 2 MiB on a single `data:` line and is followed
// by the usage frame, which used to disappear with it.
func TestRecordedLargeSSEFrameIsReadBackInFull(t *testing.T) {
	content := strings.Repeat("x", 2*1024*1024)
	// The small frame after the large one matters too: the scanner used to stop at the large line, so
	// everything from there on - including this tail and the usage frame - was lost with it.
	body := fmt.Sprintf("data: {\"id\":\"1\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n"+
		"data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"tail\"}}]}\n"+
		"data: {\"id\":\"1\",\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":5,\"total_tokens\":12}}\n"+
		"data: [DONE]\n", content)

	pipeline := NewResponsePipeline(ProviderOpenAICompatible, "/v1/chat/completions", true)
	pipeline.Feed([]byte(body))
	pipeline.Finalize()
	usage, ok := pipeline.Usage()
	if !ok || usage.TotalTokens != 12 {
		t.Fatalf("recorder usage = %+v (ok=%v), want total 12", usage, ok)
	}
	if len(pipeline.Events()) < 2 {
		t.Fatalf("recorder kept %d events, want the content frame and the usage frame", len(pipeline.Events()))
	}

	resp, err := ParseStreamResponse(ProviderOpenAICompatible, "/v1/chat/completions", []byte(body))
	if err != nil {
		t.Fatalf("ParseStreamResponse() error = %v", err)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("reader returned %d candidates, want 1", len(resp.Candidates))
	}
	got := 0
	for _, part := range resp.Candidates[0].Content {
		got += len(part.Text)
	}
	if got != len(content)+len("tail") {
		t.Fatalf("reader content = %d bytes, want %d (the large frame plus the tail frame after it): the recorder accepted the frame, so the reader may not drop it", got, len(content)+len("tail"))
	}
}
