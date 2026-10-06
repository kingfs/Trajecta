package llm

import (
	"testing"
)

// TestRecorderKeepsBothReasoningSpellings covers the writer half of the reasoning-field fix.
//
// `reasoning_content` (DeepSeek, vLLM) and `reasoning` (OpenRouter) are two names for the same delta
// field. The observation parser accepted both, but the recorder only emitted `llm.reasoning.delta` for
// `reasoning_content`, so a cassette recorded from an OpenRouter-style stream carried no reasoning
// event at all while the observation derived from the same bytes did - the recorded event stream and
// the derived one disagreed about what the upstream sent.
func TestRecorderKeepsBothReasoningSpellings(t *testing.T) {
	body := "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"deep\"}}]}\n" +
		"data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"router\"}}]}\n" +
		"data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n" +
		"data: [DONE]\n"

	pipeline := NewResponsePipeline(ProviderOpenAICompatible, "/v1/chat/completions", true)
	pipeline.Feed([]byte(body))
	pipeline.Finalize()

	reasoning := ""
	for _, event := range pipeline.Events() {
		if event.Type == "llm.reasoning.delta" {
			reasoning += event.Message
		}
	}
	if reasoning != "deeprouter" {
		t.Fatalf("recorded reasoning events = %q, want %q: both spellings must reach the cassette", reasoning, "deeprouter")
	}
}
