package live

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/replay"

	"github.com/sashabaranov/go-openai"
)

// These tests close the project's core loop with real traffic: a live request
// is recorded to a .http cassette by the running proxy, and that cassette is
// then replayed through the official Go SDK with no network access.

func traceDir(t *testing.T) string {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("TRAJECTA_LIVE_TRACE_DIR"))
	if dir == "" {
		t.Skip("set TRAJECTA_LIVE_TRACE_DIR to the host directory bind-mounted at /app/data/traces")
	}
	return dir
}

func waitForCassette(t *testing.T, marker string, timeout time.Duration) string {
	t.Helper()
	dir := traceDir(t)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		found, err := findCassette(dir, marker)
		if err != nil {
			t.Fatalf("scan cassettes: %v", err)
		}
		if found != "" {
			return found
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("no cassette containing marker %q appeared under %s within %s", marker, dir, timeout)
	return ""
}

func replayClient(path string) *openai.Client {
	cfg := openai.DefaultConfig("replay-only")
	cfg.BaseURL = "http://replay.invalid/v1"
	cfg.HTTPClient = &http.Client{Transport: replay.NewTransport(path)}
	return openai.NewClientWithConfig(cfg)
}

func TestLiveReplayFidelityNonStream(t *testing.T) {
	requireLive(t)

	marker := fmt.Sprintf("replay-fidelity-%d", time.Now().UnixNano())
	body := map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly the token " + marker}},
		"max_tokens": 128,
	}
	live := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil)
	if live.Status != http.StatusOK {
		t.Fatalf("live request: want 200, got %s", live)
	}

	cassette := waitForCassette(t, marker, 15*time.Second)
	raw, err := os.ReadFile(cassette)
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte("# trajecta/v3")) {
		t.Fatalf("cassette %s does not start with the V3 prelude: %s", cassette, firstLine(raw))
	}

	// What the raw cassette says the assistant answered.
	summary, err := replay.ReplayFile(cassette, replay.SummaryOptions{BodyLimit: 20000})
	if err != nil {
		t.Fatalf("replay summary: %v", err)
	}
	var rawCompletion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(summary.Body), &rawCompletion); err != nil {
		t.Fatalf("cassette response body is not a chat completion: %v (%s)", err, truncate(summary.Body, 300))
	}

	// What the official SDK sees when it only has the cassette.
	client := replayClient(cassette)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	completion, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: modelName(),
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "Reply with exactly the token " + marker},
		},
	})
	if err != nil {
		t.Fatalf("SDK replay of %s failed: %v", cassette, err)
	}
	if len(completion.Choices) != len(rawCompletion.Choices) {
		t.Errorf("replay choices=%d, cassette choices=%d", len(completion.Choices), len(rawCompletion.Choices))
	}
	if got, want := completion.Choices[0].Message.Content, rawCompletion.Choices[0].Message.Content; got != want {
		t.Errorf("replay content=%q, cassette content=%q", got, want)
	}
	if completion.Usage.TotalTokens != rawCompletion.Usage.TotalTokens {
		t.Errorf("replay total_tokens=%d, cassette total_tokens=%d", completion.Usage.TotalTokens, rawCompletion.Usage.TotalTokens)
	}
	if strings.TrimSpace(completion.Choices[0].Message.Content) == "" {
		t.Errorf("replayed completion has empty content")
	}
}

func TestLiveReplayFidelityStream(t *testing.T) {
	requireLive(t)

	marker := fmt.Sprintf("replay-stream-%d", time.Now().UnixNano())
	body := map[string]any{
		"model":    modelName(),
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": "Reply with exactly the word pong. Marker: " + marker}},
		// deepseek-flash spends reasoning tokens before emitting content, and a
		// small budget can be consumed entirely by reasoning (finish_reason=length,
		// empty content). Keep this generous so the delta path is exercised.
		"max_tokens": 512,
	}
	live := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil)
	if live.Status != http.StatusOK {
		t.Fatalf("live streaming request: want 200, got %s", live)
	}

	cassette := waitForCassette(t, marker, 15*time.Second)
	summary, err := replay.ReplayFile(cassette, replay.SummaryOptions{BodyLimit: 20000})
	if err != nil {
		t.Fatalf("replay summary: %v", err)
	}
	if !summary.IsStream {
		t.Fatalf("cassette %s is not marked as a stream", cassette)
	}
	wantText := assembleSSEText(t, summary.Body)

	client := replayClient(cassette)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := client.CreateChatCompletionStream(ctx, openai.ChatCompletionRequest{
		Model:    modelName(),
		Stream:   true,
		Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "Reply with exactly the word pong. Marker: " + marker}},
	})
	if err != nil {
		t.Fatalf("SDK replay stream of %s failed: %v", cassette, err)
	}
	defer stream.Close()

	var got strings.Builder
	chunks := 0
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading replayed stream: %v", err)
		}
		chunks++
		for _, choice := range chunk.Choices {
			got.WriteString(choice.Delta.Content)
		}
	}
	if chunks == 0 {
		t.Errorf("replayed stream produced no chunks")
	}
	if got.String() != wantText {
		t.Errorf("replayed stream text=%q, cassette stream text=%q", got.String(), wantText)
	}
	if strings.TrimSpace(got.String()) == "" {
		t.Errorf("replayed stream produced no content")
	}
}

// assembleSSEText concatenates the delta content of an OpenAI SSE body.
func assembleSSEText(t *testing.T, body string) string {
	t.Helper()
	var out strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			out.WriteString(choice.Delta.Content)
		}
	}
	return out.String()
}
