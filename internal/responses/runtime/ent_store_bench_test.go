package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/kingfs/Trajecta/ent/dao"
	"github.com/kingfs/Trajecta/ent/dao/responseitem"
	"github.com/kingfs/Trajecta/internal/responses/protocol"
	tracestore "github.com/kingfs/Trajecta/internal/store"
)

// BenchmarkEntStoreItemHistoryIO measures the two directions of the Responses
// item history: reading it back and writing a response with it. Both arms do the
// same work; the per-item arm issues one statement per item, the batched arm one
// statement per chunk on the write side and one query on the read side.
func BenchmarkEntStoreItemHistoryIO(b *testing.B) {
	ctx := context.Background()
	traces, err := tracestore.New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer traces.Close()
	store := NewEntStore(traces.EntClient())

	const itemCount = 120
	inputs := make([]protocol.InputItem, 0, itemCount)
	for i := 0; i < itemCount; i++ {
		inputs = append(inputs, messageInput(fmt.Sprintf("bench_in_%03d", i), fmt.Sprintf("ping-%03d", i)))
	}
	resp := protocol.Response{ID: "bench_resp", Object: "response", Status: "completed", Model: "model", CreatedAt: 1}
	if err := store.Put(ctx, resp, protocol.CreateResponseRequest{Input: "ping"}, inputs, nil); err != nil {
		b.Fatal(err)
	}
	itemIDs := make([]string, 0, itemCount)
	for i, input := range inputs {
		itemIDs = append(itemIDs, storedItemID(resp.ID, "input", input.ID, i))
	}

	b.Run("read/per-item", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, itemID := range itemIDs {
				if _, err := store.client.ResponseItem.Get(ctx, itemID); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("read/batched", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := store.itemsByID(ctx, itemIDs); err != nil {
				b.Fatal(err)
			}
		}
	})

	// The item counter runs across iterations and calibration runs, so every
	// inserted id stays unique.
	writeItemSeq := 0
	writeArm := func(b *testing.B, batched bool) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			// Every iteration inserts its own items through the same transaction
			// shape the store uses, so only the statement count differs.
			tx, err := store.client.Tx(ctx)
			if err != nil {
				b.Fatal(err)
			}
			client := tx.Client()
			builders := make([]*dao.ResponseItemCreate, 0, itemCount)
			for n := 0; n < itemCount; n++ {
				writeItemSeq++
				builder := client.ResponseItem.Create().
					SetID(fmt.Sprintf("bench_write_%06d", writeItemSeq)).
					SetKind(responseitem.KindInput).
					SetResponseID(resp.ID).
					SetConversationID("").
					SetPayload(map[string]any{"type": "message", "text": "ping"})
				if batched {
					builders = append(builders, builder)
					continue
				}
				if err := builder.Exec(ctx); err != nil {
					_ = tx.Rollback()
					b.Fatal(err)
				}
			}
			for start := 0; start < len(builders); start += responseItemInsertChunk {
				end := start + responseItemInsertChunk
				if end > len(builders) {
					end = len(builders)
				}
				if err := client.ResponseItem.CreateBulk(builders[start:end]...).Exec(ctx); err != nil {
					_ = tx.Rollback()
					b.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("write/per-item", func(b *testing.B) { writeArm(b, false) })
	b.Run("write/chunked", func(b *testing.B) { writeArm(b, true) })
}
