package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeEndpointSupportsOpenAICompatibleVariants(t *testing.T) {
	assert.Equal(t, "/v1/responses", NormalizeEndpoint("/openai/v1/responses?api-version=preview"))
	assert.Equal(t, "/v1/chat/completions", NormalizeEndpoint("/openai/deployments/gpt-4o/chat/completions"))
	assert.Equal(t, "/v1/models", NormalizeEndpoint("/v1/models"))
	assert.Equal(t, "/tokenize", NormalizeEndpoint("/v1/tokenize"))
	assert.Equal(t, "/tokenize", NormalizeEndpoint("/tokenize"))
	assert.Equal(t, "/detokenize", NormalizeEndpoint("/detokenize"))
	assert.Equal(t, "/v1beta/models:generateContent", NormalizeEndpoint("/v1beta/models/gemini-2.5-flash:generateContent"))
	assert.Equal(t, "/v1/publishers/models:generateContent", NormalizeEndpoint("/v1/projects/demo/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"))
}

func TestClassifyPathSupportsDerivedOpenAIProviders(t *testing.T) {
	semantics := ClassifyPath("/openai/v1/responses?api-version=preview", "https://demo-resource.openai.azure.com/openai/v1")
	assert.Equal(t, ProviderAzureOpenAI, semantics.Provider)
	assert.Equal(t, OperationResponses, semantics.Operation)
	assert.Equal(t, "/v1/responses", semantics.Endpoint)

	semantics = ClassifyPath("/v1/chat/completions", "http://vllm.local:8000/v1")
	assert.Equal(t, ProviderVLLM, semantics.Provider)
	assert.Equal(t, OperationChatCompletions, semantics.Operation)

	semantics = ClassifyPath("/v1/tokenize", "http://vllm.local:8000")
	assert.Equal(t, ProviderVLLM, semantics.Provider)
	assert.Equal(t, OperationTokenize, semantics.Operation)
	semantics = ClassifyPath("/tokenize", "http://vllm.local:8000")
	assert.Equal(t, ProviderVLLM, semantics.Provider)
	assert.Equal(t, OperationTokenize, semantics.Operation)
	assert.Equal(t, "/tokenize", semantics.Endpoint)

	semantics = ClassifyPath("/v1beta/models/gemini-2.5-flash:generateContent", "https://generativelanguage.googleapis.com")
	assert.Equal(t, ProviderGoogleGenAI, semantics.Provider)
	assert.Equal(t, OperationGenerateContent, semantics.Operation)
	assert.Equal(t, "/v1beta/models:generateContent", semantics.Endpoint)

	semantics = ClassifyPath("/v1/projects/demo/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent", "https://us-central1-aiplatform.googleapis.com")
	assert.Equal(t, ProviderVertexNative, semantics.Provider)
	assert.Equal(t, OperationGenerateContent, semantics.Operation)
	assert.Equal(t, "/v1/publishers/models:generateContent", semantics.Endpoint)
}

func TestModelFromPath(t *testing.T) {
	assert.Equal(t, "gemini-2.5-flash", ModelFromPath("/v1beta/models/gemini-2.5-flash:generateContent"))
	assert.Equal(t, "gemini-2.5-flash", ModelFromPath("/v1/projects/demo/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"))
	assert.Equal(t, "", ModelFromPath("/v1/messages"))
}

// TestNormalizeEndpointKeepsModelsPathActions pins the model-path actions: a
// `:generateContent`/`:countTokens` path is an operation on one model, so it
// must never normalize to a listing endpoint. Normalizing it to `/v1/models`
// made the proxy answer a generation call with the model catalog (HTTP 200).
func TestNormalizeEndpointKeepsModelsPathActions(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/v1beta/models/gemini-2.0-flash:generateContent", want: "/v1beta/models:generateContent"},
		{path: "/v1beta/models/gemini-2.0-flash:streamGenerateContent", want: "/v1beta/models:streamGenerateContent"},
		{path: "/v1beta/models/gemini-2.0-flash:countTokens", want: "/v1beta/models:countTokens"},
		{path: "/v1/models/deepseek-flash:generateContent", want: "/v1/publishers/models:generateContent"},
		{path: "/v1/models/deepseek-flash:streamGenerateContent", want: "/v1/publishers/models:streamGenerateContent"},
		{path: "/v1/models/deepseek-flash:countTokens", want: "/v1/models:countTokens"},
		{path: "/v1/publishers/google/models/gemini:generateContent", want: "/v1/publishers/models:generateContent"},
		// A bare listing still resolves to the catalog endpoint (the earlier
		// `.../models` suffix rule), and so does a model path with no action.
		{path: "/v1beta/models", want: "/v1/models"},
		{path: "/v1beta/models/", want: "/v1/models"},
		{path: "/v1/models", want: "/v1/models"},
		{path: "/v1/models/deepseek-flash", want: "/v1/models"},
	} {
		if got := NormalizeEndpoint(tc.path); got != tc.want {
			t.Errorf("NormalizeEndpoint(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestModelFromPathReadsTheActionForm confirms the model is still extracted from
// a suffixed path, so a caller is told which model the operation named.
func TestModelFromPathReadsTheActionForm(t *testing.T) {
	if got := ModelFromPath("/v1beta/models/gemini-2.0-flash:countTokens"); got != "gemini-2.0-flash" {
		t.Fatalf("ModelFromPath() = %q, want gemini-2.0-flash", got)
	}
	if got := ModelFromPath("/v1/models/deepseek-flash:generateContent"); got != "deepseek-flash" {
		t.Fatalf("ModelFromPath() = %q, want deepseek-flash", got)
	}
}

// TestParseRequestForPathPrefersTheModelInThePath keeps the diagnostic model
// name honest. `/v1beta/models/gemini-2.0-flash` normalizes to the catalog
// endpoint and reaches the model-list adapter, whose synthetic request names
// the model `list_models`; a caller that reported that name would hide which
// model the request was about.
func TestParseRequestForPathPrefersTheModelInThePath(t *testing.T) {
	req, err := ParseRequestForPath("/v1beta/models/gemini-2.0-flash", "", nil)
	if err != nil {
		t.Fatalf("ParseRequestForPath() error = %v", err)
	}
	if req.Model != "gemini-2.0-flash" {
		t.Fatalf("req.Model = %q, want gemini-2.0-flash", req.Model)
	}

	listReq, err := ParseRequestForPath("/v1/models", "", nil)
	if err != nil {
		t.Fatalf("ParseRequestForPath() error = %v", err)
	}
	if listReq.Model != ModelListSentinel {
		t.Fatalf("req.Model = %q, want %q for a real listing", listReq.Model, ModelListSentinel)
	}
}
