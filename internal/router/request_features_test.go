package router

import "testing"

// TestExtractRequestFeaturesTreatsLegacyFunctionsAsToolCalling pins the feature the
// `capabilities.tool_calling` filter is driven by.
//
// OpenAI's deprecated `functions` field asks for the same tool calling as `tools`, so a target
// declared `tool_calling: false` must not be a candidate for either. Reading only `tools` let a
// legacy function-calling request through to a target that had said it cannot serve tools.
func TestExtractRequestFeaturesTreatsLegacyFunctionsAsToolCalling(t *testing.T) {
	const prefix = `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]`

	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{
			name: "openai tools",
			body: prefix + `,"tools":[{"type":"function","function":{"name":"f"}}]}`,
			want: true,
		},
		{
			name: "legacy functions",
			body: prefix + `,"functions":[{"name":"f","parameters":{"type":"object"}}]}`,
			want: true,
		},
		{
			name: "legacy functions and function_call",
			body: prefix + `,"functions":[{"name":"f"}],"function_call":"auto"}`,
			want: true,
		},
		{
			name: "legacy function_call without functions",
			body: prefix + `,"function_call":"auto"}`,
			want: false,
		},
		{
			name: "empty functions list",
			body: prefix + `,"functions":[]}`,
			want: false,
		},
		{
			name: "empty tools list",
			body: prefix + `,"tools":[]}`,
			want: false,
		},
		{
			name: "no tools at all",
			body: prefix + `}`,
			want: false,
		},
		{
			name: "functions of the wrong shape",
			body: prefix + `,"functions":"auto"}`,
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractRequestFeatures("/v1/chat/completions", []byte(tc.body)).HasTools; got != tc.want {
				t.Fatalf("HasTools = %v, want %v for body %s", got, tc.want, tc.body)
			}
		})
	}
}

// TestExtractRequestFeaturesReadsTheGoogleRequestShape covers the request-line and
// generationConfig parts of the Google generateContent family.
//
// The family signals streaming in the request line (`:streamGenerateContent`), which is also the
// signal upstream.BuildURL uses to add `alt=sse`, and carries the output cap at
// `generationConfig.maxOutputTokens` (pkg/llm.GeminiGenerationConfig). Reading only
// `stream`/`max_tokens` left a streaming Gemini request in the non-streaming inflight bucket and
// costed its decode at the 512-token default.
func TestExtractRequestFeaturesReadsTheGoogleRequestShape(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		body       string
		wantStream bool
		wantMax    float64
	}{
		{
			name:       "google streaming endpoint",
			path:       "/v1beta/models/gemini-2.5-pro:streamGenerateContent",
			body:       `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":8192}}`,
			wantStream: true,
			wantMax:    8192,
		},
		{
			name:       "google vertex streaming endpoint",
			path:       "/v1/publishers/google/models/gemini-2.5-pro:streamGenerateContent",
			body:       `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			wantStream: true,
			wantMax:    256,
		},
		{
			name:       "google non-streaming endpoint",
			path:       "/v1beta/models/gemini-2.5-pro:generateContent",
			body:       `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":1024}}`,
			wantStream: false,
			wantMax:    1024,
		},
		{
			name:       "openai body keeps its own signal",
			path:       "/v1/chat/completions",
			body:       `{"model":"m","stream":true,"max_tokens":64,"generationConfig":{"maxOutputTokens":8192}}`,
			wantStream: true,
			wantMax:    64,
		},
		{
			name:       "google zero cap keeps the default",
			path:       "/v1beta/models/gemini-2.5-pro:generateContent",
			body:       `{"contents":[],"generationConfig":{"maxOutputTokens":0}}`,
			wantStream: false,
			wantMax:    256,
		},
		{
			name:       "google cap of the wrong shape keeps the default",
			path:       "/v1beta/models/gemini-2.5-pro:generateContent",
			body:       `{"contents":[],"generationConfig":{"maxOutputTokens":"8192"}}`,
			wantStream: false,
			wantMax:    256,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			features := extractRequestFeatures(tc.path, []byte(tc.body))
			if features.Stream != tc.wantStream {
				t.Errorf("Stream = %v, want %v for %s", features.Stream, tc.wantStream, tc.path)
			}
			if features.MaxTokens != tc.wantMax {
				t.Errorf("MaxTokens = %v, want %v for body %s", features.MaxTokens, tc.wantMax, tc.body)
			}
		})
	}
}
