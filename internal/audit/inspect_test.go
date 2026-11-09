package audit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "unittest", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestInspectNonStreamCassette(t *testing.T) {
	result, err := New().InspectBytes(context.Background(), fixture(t, "non-stream.http"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Protocol != "chat_completions" || result.Streaming {
		t.Fatalf("protocol/streaming = %q/%v", result.Protocol, result.Streaming)
	}
	obs := result.Observation
	if obs == nil || obs.Model != "qwen3-max" {
		t.Fatalf("observation = %+v", obs)
	}
	if len(obs.Request.Messages) == 0 {
		t.Fatal("request messages missing")
	}
	if len(obs.Response.Parts) == 0 {
		t.Fatal("response parts missing")
	}
	if obs.Usage == nil || obs.Usage.TotalTokens == 0 {
		t.Fatalf("usage = %+v", obs.Usage)
	}
}

func TestInspectStreamCassetteKeepsRequestOnly(t *testing.T) {
	result, err := New().InspectBytes(context.Background(), fixture(t, "stream.http"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Streaming {
		t.Fatal("streaming cassette reported as non-streaming")
	}
	if result.Observation == nil || len(result.Observation.Request.Messages) == 0 {
		t.Fatalf("request side not decoded: %+v", result.Observation)
	}
	if len(result.Observation.Response.Parts) == 0 {
		t.Fatalf("chat stream response not assembled: %+v", result.Observation.Response)
	}
	if result.Observation.Response.Parts[0].Text != "AI小助手" {
		t.Fatalf("response text = %q", result.Observation.Response.Parts[0].Text)
	}
	if result.Observation.Usage == nil || result.Observation.Usage.TotalTokens == 0 {
		t.Fatalf("usage = %+v", result.Observation.Usage)
	}
}

func TestInspectMalformedCassette(t *testing.T) {
	_, err := New().InspectBytes(context.Background(), []byte("not a cassette"))
	if err == nil {
		t.Fatal("malformed cassette accepted")
	}
}

func TestUnsupportedProtocol(t *testing.T) {
	_, _, ok := protocolFor(recordfile.RecordHeader{Meta: recordfile.MetaData{Endpoint: "/v1/embeddings"}})
	if ok {
		t.Fatal("embeddings endpoint reported as a supported protocol")
	}
}
