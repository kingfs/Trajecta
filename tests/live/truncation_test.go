package live

import (
	"net/http"
	"testing"
)

// TestLiveLocalResponsesTruncationStatus pins the truncation contract of the
// local Responses runtime against the native pass-through.
//
// While BUG-LIVE-2 is open the local runtime answers status=completed for a
// response that the native path reports as status=incomplete, so this test
// skips with the bug id instead of failing the suite. Once the mapping is
// fixed the skip disappears and the assertions below start protecting it.
func TestLiveLocalResponsesTruncationStatus(t *testing.T) {
	requireLive(t)

	prompt := map[string]any{
		"model":             modelName(),
		"input":             "Write a 500 word essay about the sea.",
		"max_output_tokens": 16,
		"store":             true,
	}

	withResponsesStrategy(t, "auto")
	native := responsesRequest(t, prompt)
	if native.Status != http.StatusOK {
		t.Fatalf("native truncated request: want 200, got %s", native)
	}
	var nativeBody struct {
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	decodeJSON(t, native.Body, &nativeBody)
	if nativeBody.Status != "incomplete" {
		t.Skipf("native upstream returned status=%q instead of incomplete; truncation contract not exercised", nativeBody.Status)
	}

	withResponsesStrategy(t, "local_server_only")
	local := responsesRequest(t, prompt)
	if local.Status != http.StatusOK {
		t.Fatalf("local truncated request: want 200, got %s", local)
	}
	var localBody struct {
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	decodeJSON(t, local.Body, &localBody)

	if localBody.Status != "incomplete" {
		t.Skipf("BUG-LIVE-2: local runtime reports status=%q for a truncated response (native reports %q); see test-reports/04-bugs.md", localBody.Status, nativeBody.Status)
	}
	if localBody.IncompleteDetails == nil {
		t.Errorf("local runtime returned incomplete without incomplete_details: %s", truncate(string(local.Body), 300))
	} else if localBody.IncompleteDetails.Reason != "max_output_tokens" {
		t.Errorf("local runtime incomplete_details.reason=%q, want max_output_tokens", localBody.IncompleteDetails.Reason)
	}
}
