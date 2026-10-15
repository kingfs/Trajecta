package mcpserver

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestServerListsCredentialMasks covers the whole reader chain for the credential
// masking audit - MCP tool to the in-process monitor route to the store - because
// the tool is the surface the operator-facing audit consumes and nothing else
// exercises the route from outside the monitor package.
//
// What it pins beyond "the tool answers": the pair comes back decrypted, and the
// event feed entry raises the audit table by name without repeating a value.
func TestServerListsCredentialMasks(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	const original = "sk-proj-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAT3BlbkFJBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	const standIn = "sk-proj-AQIDB2V6N7R8S9T0U1V2W3X4Y5Z6A7B8C9D0E1F2G3H4I5J6KT3BlbkFJQ7R8S9T0U1V2W3X4Y5Z6A7B8C9D0E1F2G3H4I5J6K7L8M9N0O1P2Q3R4S5T6U7V8W9X0Y1Z2"
	if err := st.UpsertCredentialMaskAudits([]store.CredentialMaskAuditRecord{{
		TraceID:    "trace-mcp-mask",
		EventIndex: 0,
		Rule:       "openai.project_key",
		Vendor:     "OpenAI",
		Kind:       "api_key",
		Confidence: "high",
		Score:      100,
		Reasons:    "strong_shape,context",
		Start:      76,
		End:        76 + len(original),
		Original:   original,
		StandIn:    standIn,
	}}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}

	server := New(st, Options{})
	session, err := connectClient(context.Background(), server)
	if err != nil {
		t.Fatalf("connectClient() error = %v", err)
	}
	defer session.Close()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_credential_masks",
		Arguments: map[string]any{"trace_id": "trace-mcp-mask"},
	})
	if err != nil {
		t.Fatalf("CallTool(list_credential_masks) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("list_credential_masks returned IsError: %+v", result.Content)
	}
	payload := result.StructuredContent.(map[string]any)
	if got := int(payload["total"].(float64)); got != 1 {
		t.Fatalf("total = %d, want 1", got)
	}
	if got := payload["storage_mode"].(string); got != st.SecretStorageMode() {
		t.Fatalf("storage_mode = %q, want %q", got, st.SecretStorageMode())
	}
	items := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0].(map[string]any)
	if item["original"] != original {
		t.Errorf("original = %v, want the value that was masked", item["original"])
	}
	if item["stand_in"] != standIn {
		t.Errorf("stand_in = %v, want the stand-in the upstream received", item["stand_in"])
	}
	if item["rule"] != "openai.project_key" || item["vendor"] != "OpenAI" {
		t.Errorf("rule/vendor = %v/%v, want openai.project_key/OpenAI", item["rule"], item["vendor"])
	}
	if int(item["start"].(float64)) != 76 || int(item["end"].(float64)) != 76+len(original) {
		t.Errorf("offsets = (%v, %v), want (76, %d)", item["start"], item["end"], 76+len(original))
	}

	// The event that announces the rewrite must not be a second copy of the pair,
	// since the feed is fanned out to every monitor session.
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_system_events",
		// The default window is deliberate: the event was just written, so a
		// "today" bound has to include it, and that is the bound the event feed
		// hands the operator by default.
		Arguments: map[string]any{"category": "credential_masked"},
	})
	if err != nil {
		t.Fatalf("CallTool(list_system_events) error = %v", err)
	}
	eventPayload := result.StructuredContent.(map[string]any)
	if got := int(eventPayload["total"].(float64)); got != 1 {
		t.Fatalf("credential_masked events = %d, want 1", got)
	}
	event := eventPayload["items"].([]any)[0].(map[string]any)
	details := event["details_json"].(map[string]any)
	if _, ok := details["original"]; ok {
		t.Errorf("system event details carry the credential: %+v", details)
	}
	if details["rule"] != "openai.project_key" {
		t.Errorf("system event details rule = %v, want openai.project_key", details["rule"])
	}
	if event["trace_id"] != "trace-mcp-mask" {
		t.Errorf("system event trace_id = %v, want trace-mcp-mask", event["trace_id"])
	}
}
