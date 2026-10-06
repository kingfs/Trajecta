package proxy

import (
	"bytes"
	"encoding/json"
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

// TestAggregatedModelEntryKeepsTheOpenAIModelShape pins the field set the OpenAI model schema
// requires, on the raw JSON rather than on the Go struct.
//
// `/v1/models` and `/v1/models/{id}` are answered locally, but the clients that call them are the
// typed OpenAI SDKs, whose Model type requires id, object, created and owned_by. `created` is the
// one field this catalog has no natural value for (an entry exists because a channel declares or
// discovers the model, not because the model was released at a known time), which is exactly why
// it is easy to drop: the object still marshals, so nothing but a client-side validation error
// would notice. Decoding the marshalled bytes keeps `omitempty` and tag mistakes visible.
func TestAggregatedModelEntryKeepsTheOpenAIModelShape(t *testing.T) {
	for _, model := range []string{"qwen3.6-35b-a3b", "local-only-model"} {
		raw, err := json.Marshal(newAggregatedModelListEntry(model))
		if err != nil {
			t.Fatalf("marshal %s: %v", model, err)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("unmarshal %s: %v; body=%s", model, err, raw)
		}

		var id string
		if err := json.Unmarshal(payload["id"], &id); err != nil || id != model {
			t.Errorf("id = %s (err %v), want %q; body=%s", payload["id"], err, model, raw)
		}
		var object string
		if err := json.Unmarshal(payload["object"], &object); err != nil || object != "model" {
			t.Errorf("object = %s (err %v), want \"model\"; body=%s", payload["object"], err, raw)
		}
		var ownedBy string
		if err := json.Unmarshal(payload["owned_by"], &ownedBy); err != nil || ownedBy == "" {
			t.Errorf("owned_by = %s (err %v), want a non-empty owner; body=%s", payload["owned_by"], err, raw)
		}

		created, ok := payload["created"]
		if !ok {
			t.Fatalf("body=%s has no created field; the OpenAI Model type requires it", raw)
		}
		var createdAt int64
		if err := json.Unmarshal(created, &createdAt); err != nil {
			t.Fatalf("created = %s is not a number: %v; body=%s", created, err, raw)
		}
		if createdAt != aggregatedModelCreatedAt {
			t.Errorf("created = %d, want the fixed %d so the payload is stable for a given configuration", createdAt, aggregatedModelCreatedAt)
		}
	}
}
