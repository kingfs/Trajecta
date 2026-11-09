package router

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

func TestExtractRequestFeaturesFromEnvelope(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5",
		"stream":true,
		"tools":[{"type":"function","function":{"name":"f"}}],
		"response_format":{"type":"json_schema"},
		"max_output_tokens":256
	}`)
	features := extractRequestFeatures("/v1/responses", body)
	if features.ModelName != "gpt-5" {
		t.Fatalf("ModelName = %q", features.ModelName)
	}
	if !features.Stream || !features.HasTools || !features.HasStructuredOutput {
		t.Fatalf("features = %+v", features)
	}
	if features.MaxTokens != 256 {
		t.Fatalf("MaxTokens = %v, want 256", features.MaxTokens)
	}
	if features.Protocol != xir.ProtocolResponses {
		t.Fatalf("Protocol = %q, want responses", features.Protocol)
	}
}

func TestExtractRequestFeaturesLegacyFunctions(t *testing.T) {
	features := extractRequestFeatures("/v1/chat/completions", []byte(`{"model":"m","functions":[{"name":"f"}]}`))
	if !features.HasTools {
		t.Fatal("legacy functions were not treated as tools")
	}
	if features.Protocol != xir.ProtocolChatCompletions {
		t.Fatalf("Protocol = %q, want chat_completions", features.Protocol)
	}
}

func TestExtractRequestFeaturesMalformedIsStreaming(t *testing.T) {
	features := extractRequestFeatures("/v1/chat/completions", []byte(`{bad`))
	if !features.Stream {
		t.Fatal("malformed body must be treated as streaming")
	}
}

func TestExtractRequestFeaturesGeminiPathStreaming(t *testing.T) {
	features := extractRequestFeatures("/v1beta/models/gemini-2.5-pro:streamGenerateContent", []byte(`{"contents":[]}`))
	if !features.Stream {
		t.Fatal("Gemini streamGenerateContent path must be streaming")
	}
}

func TestRequestModelGeminiPathWinsOverBody(t *testing.T) {
	body := []byte(`{"model":"body-model","contents":[]}`)
	if got := requestModel("/v1beta/models/path-model:generateContent", body); got != "path-model" {
		t.Fatalf("requestModel = %q, want path-model", got)
	}
}
