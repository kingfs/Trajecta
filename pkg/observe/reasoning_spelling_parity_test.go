package observe

import (
	"context"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// reasoningSpellingStream is one OpenAI-compatible chat stream that carries the reasoning field under
// both spellings: `reasoning_content` (DeepSeek, vLLM) and `reasoning` (OpenRouter).
const reasoningSpellingStream = `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"deep "}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning":"router "}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"answer"}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

// TestReasoningSpellingAgreesAcrossParsers pins the two independent readers of the same bytes together.
//
// The observation parser already accepted both spellings of the reasoning field while the replay/stream
// parser and the recorder only looked at `reasoning_content`, so a stream that used the OpenRouter
// spelling produced an observation with reasoning and a cassette event stream (and a parsed response)
// without it. Both readers now fold `reasoning_content` first and `reasoning` second, and this gate
// measures that on one body instead of trusting either side alone.
func TestReasoningSpellingAgreesAcrossParsers(t *testing.T) {
	streamed, err := llm.ParseStreamResponse(llm.ProviderOpenAICompatible, "/v1/chat/completions", []byte(reasoningSpellingStream))
	if err != nil {
		t.Fatalf("ParseStreamResponse() error = %v", err)
	}
	if len(streamed.Candidates) != 1 {
		t.Fatalf("ParseStreamResponse() candidates = %d, want 1", len(streamed.Candidates))
	}
	streamReasoning := ""
	streamText := ""
	for _, part := range streamed.Candidates[0].Content {
		switch part.Type {
		case "thinking":
			streamReasoning += part.Text
		case "text":
			streamText += part.Text
		}
	}
	if !strings.Contains(streamReasoning, "deep ") || !strings.Contains(streamReasoning, "router ") {
		t.Fatalf("ParseStreamResponse() reasoning = %q, want both the `reasoning_content` and the `reasoning` spelling", streamReasoning)
	}
	if streamText != "answer" {
		t.Fatalf("ParseStreamResponse() text = %q, want %q", streamText, "answer")
	}

	registry := NewDefaultRegistry()
	observation, err := registry.Parse(context.Background(), ParseInput{
		TraceID: "trace-reasoning-spelling",
		Header: recordfile.RecordHeader{Meta: recordfile.MetaData{
			Provider:  llm.ProviderOpenAICompatible,
			Operation: llm.OperationChatCompletions,
			Endpoint:  "/v1/chat/completions",
		}},
		ResponseBody: []byte(reasoningSpellingStream),
		IsStream:     true,
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if observation.Stream.AccumulatedReasoning == "" {
		t.Fatalf("Parse() produced no stream reasoning: %+v", observation.Stream)
	}
	if got, want := observation.Stream.AccumulatedReasoning, streamReasoning; got != want {
		t.Fatalf("observation reasoning = %q, stream parser reasoning = %q: the two readers of the same cassette must agree", got, want)
	}
	if !strings.Contains(observation.Stream.AccumulatedReasoning, "router ") {
		t.Fatalf("observation reasoning = %q, want the `reasoning` spelling to be kept", observation.Stream.AccumulatedReasoning)
	}
}
