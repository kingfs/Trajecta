package observe

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func embeddingsInput() ParseInput {
	return ParseInput{
		TraceID: "trace-embeddings",
		Header: recordfile.RecordHeader{
			Meta: recordfile.MetaData{
				Provider:     llm.ProviderOpenAICompatible,
				Operation:    llm.OperationEmbeddings,
				Endpoint:     "/v1/embeddings",
				Model:        "text-embedding-3-small",
				ExchangeKind: "model",
				ExchangeRole: "primary_model_call",
			},
		},
		RequestBody:  []byte(`{"model":"text-embedding-3-small","input":"hello"}`),
		ResponseBody: []byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}]}`),
	}
}

// TestRegistryParseReportsNoParserForUnsupportedOperation pins that an exchange
// the parser set does not cover is reported as a distinct, identifiable outcome.
//
// No parser handles /v1/embeddings, and that used to surface as an untyped error
// that every caller had to treat as a parse failure, which left a permanent
// failed observation on a payload nothing was wrong with.
func TestRegistryParseReportsNoParserForUnsupportedOperation(t *testing.T) {
	registry := NewDefaultRegistry()
	_, err := registry.Parse(context.Background(), embeddingsInput())
	if err == nil {
		t.Fatal("Parse() error = nil, want ErrNoParser")
	}
	if !errors.Is(err, ErrNoParser) {
		t.Fatalf("Parse() error = %v, want it to satisfy errors.Is(err, ErrNoParser)", err)
	}
	if !strings.Contains(err.Error(), `no parser for provider="openai_compatible" operation="embeddings" endpoint="/v1/embeddings"`) {
		t.Fatalf("Parse() error = %q, want the diagnostic to name the provider, operation and endpoint", err)
	}
	var typed NoParserError
	if !errors.As(err, &typed) {
		t.Fatalf("Parse() error = %T, want NoParserError", err)
	}
	if typed.Provider != llm.ProviderOpenAICompatible || typed.Operation != llm.OperationEmbeddings || typed.Endpoint != "/v1/embeddings" {
		t.Fatalf("NoParserError = %+v, want the exchange identity", typed)
	}
}

// TestUnsupportedObservationIsNotAFailure pins the shape recorded for such an
// exchange: a successful outcome that keeps the exchange metadata and explains
// itself through a warning, so reanalysis can proceed instead of failing.
func TestUnsupportedObservationIsNotAFailure(t *testing.T) {
	obs := UnsupportedObservation(embeddingsInput())
	if obs.Status != ParseStatusUnsupported {
		t.Fatalf("Status = %q, want %q", obs.Status, ParseStatusUnsupported)
	}
	if obs.TraceID != "trace-embeddings" || obs.Provider != llm.ProviderOpenAICompatible || obs.Operation != llm.OperationEmbeddings || obs.Endpoint != "/v1/embeddings" || obs.Model != "text-embedding-3-small" {
		t.Fatalf("observation identity = %+v, want the exchange identity", obs)
	}
	if obs.ExchangeKind != "model" || obs.ExchangeRole != "primary_model_call" {
		t.Fatalf("exchange metadata = %q/%q, want model/primary_model_call", obs.ExchangeKind, obs.ExchangeRole)
	}
	if len(obs.Warnings) != 1 || obs.Warnings[0].Code != "no_parser" || obs.Warnings[0].Message == "" {
		t.Fatalf("Warnings = %+v, want one no_parser warning", obs.Warnings)
	}
}
