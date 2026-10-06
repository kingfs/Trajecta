package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

// TestToolCallingCapabilityCoversLegacyFunctions is the behavioural half of the feature
// extraction: the request never reaches an upstream that declared it cannot serve tool
// calling.
//
// `capabilities.tool_calling: false` is enforced by extracting the request's features before
// selection, and that extraction read only the `tools` field. A request that used OpenAI's
// deprecated `functions` field - still emitted by older SDKs and still accepted by providers -
// was therefore routed to a target that had declared it cannot do tool calling, and the
// upstream received it: measured on this fixture before the fix, `status = 200` with the
// upstream hit count at 1. The controls below pin that the fix does not over-reach: a request
// with no tools, an empty `tools` list, an empty `functions` list and a `function_call` without
// `functions` all still reach the target.
func TestToolCallingCapabilityCoversLegacyFunctions(t *testing.T) {
	const prefix = `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]`
	proxySrv, upstreamPaths := proxyWithCapabilities(t, config.UpstreamCapabilitiesConfig{
		ToolCalling: boolPtr(false),
	})

	for _, tc := range []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantHits   int
	}{
		{
			name:       "legacy functions are tool calling",
			body:       prefix + `,"functions":[{"name":"f","description":"d","parameters":{"type":"object"}}]}`,
			wantStatus: http.StatusBadGateway,
			wantCode:   "no_supporting_target",
			wantHits:   0,
		},
		{
			name:       "legacy functions with function_call are tool calling",
			body:       prefix + `,"functions":[{"name":"f"}],"function_call":"auto"}`,
			wantStatus: http.StatusBadGateway,
			wantCode:   "no_supporting_target",
			wantHits:   0,
		},
		{
			name:       "the tools field is still honoured",
			body:       prefix + `,"tools":[{"type":"function","function":{"name":"f"}}]}`,
			wantStatus: http.StatusBadGateway,
			wantCode:   "no_supporting_target",
			wantHits:   0,
		},
		{
			name:       "no tools",
			body:       prefix + `}`,
			wantStatus: http.StatusOK,
			wantHits:   1,
		},
		{
			name:       "empty tools list",
			body:       prefix + `,"tools":[]}`,
			wantStatus: http.StatusOK,
			wantHits:   1,
		},
		{
			name:       "empty functions list",
			body:       prefix + `,"functions":[]}`,
			wantStatus: http.StatusOK,
			wantHits:   1,
		},
		{
			name:       "function_call without functions",
			body:       prefix + `,"function_call":"auto"}`,
			wantStatus: http.StatusOK,
			wantHits:   1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(upstreamPaths())
			resp, err := http.Post(proxySrv.URL+"/v1/chat/completions", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("POST error = %v", err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatalf("read body error = %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", resp.StatusCode, tc.wantStatus, body)
			}
			if tc.wantCode != "" && !strings.Contains(string(body), `"code":"`+tc.wantCode+`"`) {
				t.Fatalf("body = %s, want it to name the %q code", body, tc.wantCode)
			}
			if got := len(upstreamPaths()) - before; got != tc.wantHits {
				t.Fatalf("upstream received %d request(s), want %d; body = %s", got, tc.wantHits, body)
			}
		})
	}
}
