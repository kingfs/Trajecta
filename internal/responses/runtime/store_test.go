package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/ent/dao/responseitem"
	"github.com/kingfs/Trajecta/internal/appdbmigrate"
	"github.com/kingfs/Trajecta/internal/responses/protocol"
	tracestore "github.com/kingfs/Trajecta/internal/store"
)

func TestMemoryStoreContinuationItemsWalksPreviousResponses(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	first := protocol.Response{
		ID:        "resp_1",
		Object:    "response",
		Status:    "completed",
		Model:     "model",
		CreatedAt: 1,
		Output: []protocol.OutputItem{{
			ID:      "out_1",
			Type:    "message",
			Role:    "assistant",
			Content: []protocol.ContentPart{{Type: "output_text", Text: "one"}},
		}},
	}
	second := protocol.Response{
		ID:                 "resp_2",
		Object:             "response",
		Status:             "completed",
		Model:              "model",
		CreatedAt:          2,
		PreviousResponseID: "resp_1",
		Output: []protocol.OutputItem{{
			ID:      "out_2",
			Type:    "message",
			Role:    "assistant",
			Content: []protocol.ContentPart{{Type: "output_text", Text: "two"}},
		}},
	}

	if err := store.Put(ctx, first, protocol.CreateResponseRequest{Input: "first"}, []protocol.InputItem{messageInput("in_1", "first")}, first.Output); err != nil {
		t.Fatalf("put first: %v", err)
	}
	if err := store.Put(ctx, second, protocol.CreateResponseRequest{Input: "second", PreviousResponseID: "resp_1"}, []protocol.InputItem{messageInput("in_2", "second")}, second.Output); err != nil {
		t.Fatalf("put second: %v", err)
	}

	items, ok, err := store.ContinuationItems(ctx, "resp_2")
	if err != nil || !ok {
		t.Fatalf("ContinuationItems ok=%v err=%v", ok, err)
	}
	if len(items) != 4 {
		t.Fatalf("len(items) = %d, want 4: %#v", len(items), items)
	}
	if got := items[0].Input.Content[0].Text; got != "first" {
		t.Fatalf("items[0] = %q, want first", got)
	}
	if got := items[1].Output.Content[0].Text; got != "one" {
		t.Fatalf("items[1] = %q, want one", got)
	}
	if got := items[2].Input.Content[0].Text; got != "second" {
		t.Fatalf("items[2] = %q, want second", got)
	}
	if got := items[3].Output.Content[0].Text; got != "two" {
		t.Fatalf("items[3] = %q, want two", got)
	}
}

func TestMemoryStoreContinuationItemsStopsAtCompactBoundary(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	first := protocol.Response{ID: "resp_1", Object: "response", Status: "completed", Model: "model"}
	second := protocol.Response{ID: "resp_2", Object: "response", Status: "completed", Model: "model", PreviousResponseID: "resp_1"}
	third := protocol.Response{ID: "resp_3", Object: "response", Status: "completed", Model: "model", PreviousResponseID: "resp_2"}

	if err := store.Put(ctx, first, protocol.CreateResponseRequest{Input: "first"}, []protocol.InputItem{messageInput("in_1", "first")}, nil); err != nil {
		t.Fatalf("put first: %v", err)
	}
	if err := store.Put(ctx, second, protocol.CreateResponseRequest{Input: "compact"}, []protocol.InputItem{{ID: "in_2", Type: "compact_request"}}, nil); err != nil {
		t.Fatalf("put second: %v", err)
	}
	if err := store.Put(ctx, third, protocol.CreateResponseRequest{Input: "third"}, []protocol.InputItem{messageInput("in_3", "third")}, nil); err != nil {
		t.Fatalf("put third: %v", err)
	}

	items, ok, err := store.ContinuationItems(ctx, "resp_3")
	if err != nil || !ok {
		t.Fatalf("ContinuationItems ok=%v err=%v", ok, err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want compact boundary plus current: %#v", len(items), items)
	}
	if items[0].Input.Type != "compact_request" {
		t.Fatalf("first item type = %q, want compact_request", items[0].Input.Type)
	}
	if got := items[1].Input.Content[0].Text; got != "third" {
		t.Fatalf("second item = %q, want third", got)
	}
}

func TestEntStorePutGetInputItemsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newEntStoreForTest(t)
	resp := protocol.Response{
		ID:        "resp_ent_1",
		Object:    "response",
		CreatedAt: 11,
		Status:    "completed",
		Model:     "model",
		Output: []protocol.OutputItem{{
			ID:      "out_ent_1",
			Type:    "message",
			Role:    "assistant",
			Content: []protocol.ContentPart{{Type: "output_text", Text: "pong"}},
		}},
		Usage: protocol.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
		Metadata: map[string]any{
			"codex": map[string]any{"thread_id": "thread_1"},
		},
	}
	inputs := []protocol.InputItem{messageInput("in_ent_1", "ping")}

	if err := store.Put(ctx, resp, protocol.CreateResponseRequest{Input: "ping"}, inputs, resp.Output); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	got, ok, err := store.Get(ctx, resp.ID)
	if err != nil || !ok {
		t.Fatalf("Get() ok=%v err=%v", ok, err)
	}
	if got.ID != resp.ID || got.Model != resp.Model || got.Status != resp.Status {
		t.Fatalf("Get() = %#v, want id/model/status from original", got)
	}
	if got.Usage != resp.Usage {
		t.Fatalf("Usage = %#v, want %#v", got.Usage, resp.Usage)
	}
	if len(got.Output) != 1 || got.Output[0].Content[0].Text != "pong" {
		t.Fatalf("Output = %#v, want pong", got.Output)
	}

	gotInputs, ok, err := store.InputItems(ctx, resp.ID)
	if err != nil || !ok {
		t.Fatalf("InputItems() ok=%v err=%v", ok, err)
	}
	if len(gotInputs) != 1 || gotInputs[0].Content[0].Text != "ping" {
		t.Fatalf("InputItems() = %#v, want ping", gotInputs)
	}
}

func TestEntStoreChunksLongItemHistories(t *testing.T) {
	ctx := context.Background()
	store := newEntStoreForTest(t)
	// More items than one insert chunk carries, so the round trip also covers
	// the chunk boundary and the order the batched reads have to restore.
	const itemCount = responseItemInsertChunk + 3
	inputs := make([]protocol.InputItem, 0, itemCount)
	for i := 0; i < itemCount; i++ {
		inputs = append(inputs, messageInput(fmt.Sprintf("in_ent_chunk_%03d", i), fmt.Sprintf("ping-%03d", i)))
	}
	outputs := []protocol.OutputItem{{
		ID:      "out_ent_chunk",
		Type:    "message",
		Role:    "assistant",
		Content: []protocol.ContentPart{{Type: "output_text", Text: "pong"}},
	}}
	resp := protocol.Response{
		ID:        "resp_ent_chunked",
		Object:    "response",
		CreatedAt: 21,
		Status:    "completed",
		Model:     "model",
		Output:    outputs,
	}
	if err := store.Put(ctx, resp, protocol.CreateResponseRequest{Input: "ping"}, inputs, outputs); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	gotInputs, ok, err := store.InputItems(ctx, resp.ID)
	if err != nil || !ok {
		t.Fatalf("InputItems() ok=%v err=%v", ok, err)
	}
	if len(gotInputs) != itemCount {
		t.Fatalf("len(InputItems()) = %d, want %d", len(gotInputs), itemCount)
	}
	for i, item := range gotInputs {
		if want := fmt.Sprintf("ping-%03d", i); item.Content[0].Text != want {
			t.Fatalf("InputItems()[%d] = %q, want %q", i, item.Content[0].Text, want)
		}
	}

	got, ok, err := store.Get(ctx, resp.ID)
	if err != nil || !ok {
		t.Fatalf("Get() ok=%v err=%v", ok, err)
	}
	if len(got.Output) != 1 || got.Output[0].Content[0].Text != "pong" {
		t.Fatalf("Get().Output = %#v, want pong", got.Output)
	}

	ledger, ok, err := store.ContinuationItems(ctx, resp.ID)
	if err != nil || !ok {
		t.Fatalf("ContinuationItems() ok=%v err=%v", ok, err)
	}
	if len(ledger) != itemCount+1 {
		t.Fatalf("len(ContinuationItems()) = %d, want %d", len(ledger), itemCount+1)
	}
	if got := ledger[0].Input.Content[0].Text; got != "ping-000" {
		t.Fatalf("first ledger item = %q, want ping-000", got)
	}
	if last := ledger[len(ledger)-1]; last.Output == nil || last.Output.Content[0].Text != "pong" {
		t.Fatalf("last ledger item = %#v, want the pong output", last)
	}
}

func TestEntStoreDanglingItemReferenceStillFails(t *testing.T) {
	ctx := context.Background()
	store := newEntStoreForTest(t)
	outputs := []protocol.OutputItem{{
		ID:      "out_ent_dangling",
		Type:    "message",
		Role:    "assistant",
		Content: []protocol.ContentPart{{Type: "output_text", Text: "pong"}},
	}}
	resp := protocol.Response{
		ID:        "resp_ent_dangling",
		Object:    "response",
		CreatedAt: 22,
		Status:    "completed",
		Model:     "model",
		Output:    outputs,
	}
	inputs := []protocol.InputItem{messageInput("in_ent_dangling", "ping")}
	if err := store.Put(ctx, resp, protocol.CreateResponseRequest{Input: "ping"}, inputs, outputs); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	// The history keeps referring to the item after the row is gone, which the
	// per-item read reported as an error and the batched read must keep doing
	// instead of dropping the item from the history.
	if err := store.client.ResponseItem.DeleteOneID(storedItemID(resp.ID, "input", inputs[0].ID, 0)).Exec(ctx); err != nil {
		t.Fatalf("delete input item: %v", err)
	}
	if _, _, err := store.InputItems(ctx, resp.ID); err == nil {
		t.Fatal("InputItems() error = nil, want the dangling reference reported")
	}
	if _, ok, err := store.ContinuationItems(ctx, resp.ID); err == nil || ok {
		t.Fatalf("ContinuationItems() ok=%v err=%v, want an error", ok, err)
	}

	if err := store.client.ResponseItem.DeleteOneID(storedItemID(resp.ID, "output", outputs[0].ID, 0)).Exec(ctx); err != nil {
		t.Fatalf("delete output item: %v", err)
	}
	if _, ok, err := store.Get(ctx, resp.ID); err == nil || ok {
		t.Fatalf("Get() ok=%v err=%v, want an error", ok, err)
	}
}

func TestEntStoreFunctionToolItemsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newEntStoreForTest(t)
	resp := protocol.Response{
		ID:        "resp_ent_tool_1",
		Object:    "response",
		CreatedAt: 12,
		Status:    "completed",
		Model:     "model",
		Output: []protocol.OutputItem{{
			ID:        "fc_call_lookup",
			Type:      "function_call",
			Status:    "completed",
			CallID:    "call_lookup",
			Name:      "lookup",
			Arguments: `{"q":"codex"}`,
			Extra:     map[string]any{"provider_item_id": "item_1"},
		}},
	}
	inputs := []protocol.InputItem{{
		ID:     "in_tool_result_1",
		Type:   "function_call_output",
		CallID: "call_lookup",
		Name:   "lookup",
		Output: map[string]any{"ok": true, "value": "42"},
		Extra:  map[string]any{"status": "completed"},
	}}

	if err := store.Put(ctx, resp, protocol.CreateResponseRequest{Input: "tool"}, inputs, resp.Output); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	got, ok, err := store.Get(ctx, resp.ID)
	if err != nil || !ok {
		t.Fatalf("Get() ok=%v err=%v", ok, err)
	}
	if len(got.Output) != 1 || got.Output[0].Extra["provider_item_id"] != "item_1" {
		t.Fatalf("output item extra round-trip mismatch: %#v", got.Output)
	}
	items, ok, err := store.ContinuationItems(ctx, resp.ID)
	if err != nil || !ok {
		t.Fatalf("ContinuationItems() ok=%v err=%v", ok, err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want input and output: %#v", len(items), items)
	}
	input := items[0].Input
	if input == nil || input.Type != "function_call_output" || input.Extra["status"] != "completed" {
		t.Fatalf("function output input round-trip mismatch: %#v", items[0])
	}
	outputMap, ok := input.Output.(map[string]any)
	if !ok || outputMap["ok"] != true || outputMap["value"] != "42" {
		t.Fatalf("function output payload = %#v, want object payload", input.Output)
	}
}

func TestEntStoreContinuationItemsWalksPreviousResponses(t *testing.T) {
	ctx := context.Background()
	store := newEntStoreForTest(t)
	first := protocol.Response{
		ID:        "resp_ent_1",
		Object:    "response",
		Status:    "completed",
		Model:     "model",
		CreatedAt: 1,
		Output: []protocol.OutputItem{{
			ID:      "out_ent_1",
			Type:    "message",
			Role:    "assistant",
			Content: []protocol.ContentPart{{Type: "output_text", Text: "one"}},
		}},
	}
	second := protocol.Response{
		ID:                 "resp_ent_2",
		Object:             "response",
		Status:             "completed",
		Model:              "model",
		CreatedAt:          2,
		PreviousResponseID: "resp_ent_1",
		Output: []protocol.OutputItem{{
			ID:      "out_ent_2",
			Type:    "message",
			Role:    "assistant",
			Content: []protocol.ContentPart{{Type: "output_text", Text: "two"}},
		}},
	}

	if err := store.Put(ctx, first, protocol.CreateResponseRequest{Input: "first"}, []protocol.InputItem{messageInput("in_ent_1", "first")}, first.Output); err != nil {
		t.Fatalf("put first: %v", err)
	}
	if err := store.Put(ctx, second, protocol.CreateResponseRequest{Input: "second", PreviousResponseID: "resp_ent_1"}, []protocol.InputItem{messageInput("in_ent_2", "second")}, second.Output); err != nil {
		t.Fatalf("put second: %v", err)
	}

	items, ok, err := store.ContinuationItems(ctx, "resp_ent_2")
	if err != nil || !ok {
		t.Fatalf("ContinuationItems ok=%v err=%v", ok, err)
	}
	if len(items) != 4 {
		t.Fatalf("len(items) = %d, want 4: %#v", len(items), items)
	}
	if got := items[0].Input.Content[0].Text; got != "first" {
		t.Fatalf("items[0] = %q, want first", got)
	}
	if got := items[1].Output.Content[0].Text; got != "one" {
		t.Fatalf("items[1] = %q, want one", got)
	}
	if got := items[2].Input.Content[0].Text; got != "second" {
		t.Fatalf("items[2] = %q, want second", got)
	}
	if got := items[3].Output.Content[0].Text; got != "two" {
		t.Fatalf("items[3] = %q, want two", got)
	}
}

func TestEntStoreContinuationItemsStopsAtCompactBoundary(t *testing.T) {
	ctx := context.Background()
	store := newEntStoreForTest(t)
	first := protocol.Response{ID: "resp_ent_1", Object: "response", Status: "completed", Model: "model", CreatedAt: 1}
	second := protocol.Response{ID: "resp_ent_2", Object: "response", Status: "completed", Model: "model", CreatedAt: 2, PreviousResponseID: "resp_ent_1"}
	third := protocol.Response{ID: "resp_ent_3", Object: "response", Status: "completed", Model: "model", CreatedAt: 3, PreviousResponseID: "resp_ent_2"}

	if err := store.Put(ctx, first, protocol.CreateResponseRequest{Input: "first"}, []protocol.InputItem{messageInput("in_ent_1", "first")}, nil); err != nil {
		t.Fatalf("put first: %v", err)
	}
	if err := store.Put(ctx, second, protocol.CreateResponseRequest{Input: "compact"}, []protocol.InputItem{{ID: "in_ent_2", Type: "compact_request"}}, nil); err != nil {
		t.Fatalf("put second: %v", err)
	}
	if err := store.Put(ctx, third, protocol.CreateResponseRequest{Input: "third"}, []protocol.InputItem{messageInput("in_ent_3", "third")}, nil); err != nil {
		t.Fatalf("put third: %v", err)
	}

	items, ok, err := store.ContinuationItems(ctx, "resp_ent_3")
	if err != nil || !ok {
		t.Fatalf("ContinuationItems ok=%v err=%v", ok, err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want compact boundary plus current: %#v", len(items), items)
	}
	if items[0].Input.Type != "compact_request" {
		t.Fatalf("first item type = %q, want compact_request", items[0].Input.Type)
	}
	if got := items[1].Input.Content[0].Text; got != "third" {
		t.Fatalf("second item = %q, want third", got)
	}
}

func TestEntStoreLatestResponseIDByConversation(t *testing.T) {
	ctx := context.Background()
	store := newEntStoreForTest(t)
	first := protocol.Response{
		ID:        "resp_thread_1",
		Object:    "response",
		Status:    "completed",
		Model:     "model",
		CreatedAt: 1,
		Metadata:  map[string]any{"codex": map[string]any{"thread_id": "thread_1"}},
	}
	second := protocol.Response{
		ID:        "resp_thread_2",
		Object:    "response",
		Status:    "completed",
		Model:     "model",
		CreatedAt: 2,
		Metadata:  map[string]any{"codex": map[string]any{"thread_id": "thread_1"}},
	}
	other := protocol.Response{
		ID:        "resp_thread_other",
		Object:    "response",
		Status:    "completed",
		Model:     "model",
		CreatedAt: 3,
		Metadata:  map[string]any{"codex": map[string]any{"thread_id": "thread_2"}},
	}

	for _, resp := range []protocol.Response{first, second, other} {
		if err := store.Put(ctx, resp, protocol.CreateResponseRequest{Input: "x"}, []protocol.InputItem{messageInput("in_"+resp.ID, resp.ID)}, nil); err != nil {
			t.Fatalf("put %s: %v", resp.ID, err)
		}
	}

	got, ok, err := store.LatestResponseIDByConversation(ctx, "thread_1")
	if err != nil || !ok {
		t.Fatalf("LatestResponseIDByConversation ok=%v err=%v", ok, err)
	}
	if got != "resp_thread_2" {
		t.Fatalf("latest response = %q, want resp_thread_2", got)
	}
}

func TestEntStorePostgresPersistenceRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to a disposable Postgres test database DSN")
	}
	if err := appdbmigrate.MigrateUp("postgres", dsn, 0); err != nil {
		t.Fatalf("MigrateUp(postgres) error = %v", err)
	}

	st, err := tracestore.NewWithDatabaseOptions(t.TempDir(), "postgres", dsn, 4, 4, tracestore.DatabaseOptions{AutoMigrate: false})
	if err != nil {
		t.Fatalf("NewWithDatabaseOptions(postgres) error = %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("store.Close() error = %v", err)
		}
	})

	ctx := context.Background()
	store := NewEntStore(st.EntClient())
	suffix := strings.ReplaceAll(t.Name(), "/", "_") + "_" + time.Now().UTC().Format("20060102150405.000000000")
	resp := protocol.Response{
		ID:                 "resp_pg_" + suffix,
		Object:             "response",
		CreatedAt:          time.Now().UTC().Unix(),
		Status:             "completed",
		Model:              "gpt-postgres-test",
		PreviousResponseID: "",
		Output: []protocol.OutputItem{{
			ID:      "out_pg_" + suffix,
			Type:    "message",
			Role:    "assistant",
			Status:  "completed",
			Content: []protocol.ContentPart{{Type: "output_text", Text: "postgres pong"}},
		}},
		Usage: protocol.Usage{InputTokens: 4, OutputTokens: 3, TotalTokens: 7},
		Metadata: map[string]any{
			"codex": map[string]any{"thread_id": "thread_pg_" + suffix},
			"test":  "postgres_responses_round_trip",
		},
	}
	inputs := []protocol.InputItem{messageInput("in_pg_"+suffix, "postgres ping")}
	t.Cleanup(func() {
		_, _ = store.client.ResponseItem.Delete().Where(responseitem.ResponseID(resp.ID)).Exec(context.Background())
		_ = store.client.Response.DeleteOneID(resp.ID).Exec(context.Background())
	})

	if err := store.Put(ctx, resp, protocol.CreateResponseRequest{Input: "postgres ping"}, inputs, resp.Output); err != nil {
		t.Fatalf("Put(postgres) error = %v", err)
	}
	got, ok, err := store.Get(ctx, resp.ID)
	if err != nil || !ok {
		t.Fatalf("Get(postgres) ok=%v err=%v", ok, err)
	}
	if got.ID != resp.ID || got.Model != resp.Model || got.Status != resp.Status {
		t.Fatalf("Get(postgres) = %#v, want id/model/status from original", got)
	}
	if got.Usage != resp.Usage {
		t.Fatalf("Usage(postgres) = %#v, want %#v", got.Usage, resp.Usage)
	}
	if len(got.Output) != 1 || got.Output[0].ID != resp.Output[0].ID || got.Output[0].Content[0].Text != "postgres pong" {
		t.Fatalf("Output(postgres) = %#v, want stored output item", got.Output)
	}

	gotInputs, ok, err := store.InputItems(ctx, resp.ID)
	if err != nil || !ok {
		t.Fatalf("InputItems(postgres) ok=%v err=%v", ok, err)
	}
	if len(gotInputs) != 1 || gotInputs[0].ID != inputs[0].ID || gotInputs[0].Content[0].Text != "postgres ping" {
		t.Fatalf("InputItems(postgres) = %#v, want stored input item", gotInputs)
	}
}

func newEntStoreForTest(t *testing.T) *EntStore {
	t.Helper()
	st, err := tracestore.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("store.Close() error = %v", err)
		}
	})
	return NewEntStore(st.EntClient())
}

func messageInput(id string, text string) protocol.InputItem {
	return protocol.InputItem{
		ID:      id,
		Type:    "message",
		Role:    "user",
		Content: []protocol.ContentPart{{Type: "input_text", Text: text}},
	}
}
