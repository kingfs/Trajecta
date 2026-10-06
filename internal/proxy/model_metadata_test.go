package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewAggregatedModelListEntryEnrichesAliasFromSpecs(t *testing.T) {
	entry := newAggregatedModelListEntry("qwen3.6-35b-a3b")

	if entry.ID != "qwen3.6-35b-a3b" {
		t.Fatalf("ID = %q, want qwen3.6-35b-a3b", entry.ID)
	}
	if entry.CanonicalSlug != "qwen/qwen3.6-35b-a3b" {
		t.Fatalf("CanonicalSlug = %q, want qwen/qwen3.6-35b-a3b", entry.CanonicalSlug)
	}
	if entry.Name == "" {
		t.Fatalf("Name is empty")
	}
	if entry.ContextLength != 262144 {
		t.Fatalf("ContextLength = %d, want 262144", entry.ContextLength)
	}
	if entry.MaxModelLen != 262144 {
		t.Fatalf("MaxModelLen = %d, want 262144", entry.MaxModelLen)
	}
	if entry.MaxOutput != 65536 {
		t.Fatalf("MaxOutput = %d, want 65536", entry.MaxOutput)
	}
	if entry.TopProvider == nil || entry.TopProvider.ContextLength == nil || *entry.TopProvider.ContextLength != 262144 {
		t.Fatalf("TopProvider.ContextLength = %#v, want 262144", entry.TopProvider)
	}
	if entry.TopProvider.MaxCompletionTokens == nil || *entry.TopProvider.MaxCompletionTokens != 65536 {
		t.Fatalf("TopProvider.MaxCompletionTokens = %#v, want 65536", entry.TopProvider.MaxCompletionTokens)
	}
	if entry.Architecture == nil || entry.Architecture.Modality != "text+image+video->text" {
		t.Fatalf("Architecture = %#v, want text+image+video->text", entry.Architecture)
	}
}

func TestNewAggregatedModelListEntryKeepsUnknownModelMinimal(t *testing.T) {
	entry := newAggregatedModelListEntry("local-only-model")

	if entry.ID != "local-only-model" {
		t.Fatalf("ID = %q, want local-only-model", entry.ID)
	}
	if entry.Object != "model" {
		t.Fatalf("Object = %q, want model", entry.Object)
	}
	if entry.OwnedBy != "trajecta" {
		t.Fatalf("OwnedBy = %q, want trajecta", entry.OwnedBy)
	}
	if entry.ContextLength != 0 || entry.CanonicalSlug != "" || entry.TopProvider != nil {
		t.Fatalf("unknown model was unexpectedly enriched: %#v", entry)
	}
}

// TestRequestModelFromBodyFallsBackForUncoveredEntrypoint pins that the
// route-plan event names the model a client asked for even when the entrypoint
// has no adapter.
//
// The route-plan event is what an operator reads to find out which model a
// request was about, so losing the name for an endpoint like /v1/embeddings
// makes the event unusable exactly when routing failed.
func TestRequestModelFromBodyFallsBackForUncoveredEntrypoint(t *testing.T) {
	body := []byte(`{"model":"bge-m3","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
	if got := requestModelFromBody(req, body); got != "bge-m3" {
		t.Fatalf("requestModelFromBody(embeddings) = %q, want %q", got, "bge-m3")
	}

	// A covered entrypoint keeps using the adapter, and a body without a model
	// stays empty rather than inventing one.
	chatBody := []byte(`{"model":"gpt-5.1","messages":[]}`)
	chatReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(chatBody))
	if got := requestModelFromBody(chatReq, chatBody); got != "gpt-5.1" {
		t.Fatalf("requestModelFromBody(chat) = %q, want %q", got, "gpt-5.1")
	}
	if got := requestModelFromBody(req, []byte(`{"input":"hello"}`)); got != "" {
		t.Fatalf("requestModelFromBody(no model) = %q, want empty", got)
	}
}
