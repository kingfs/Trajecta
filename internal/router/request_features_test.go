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
