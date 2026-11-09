package store

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// TestSecretMappingDeleteRowBySeqFlushesQueuedAppend covers the race the live
// delete path has to close: AppendMapping returns as soon as the pair is queued,
// so a delete that reads the file immediately could remove nothing and then see
// the queued append recreate the row. DeleteMappingRowBySeq takes the flush
// barrier first, which is what makes the Monitor's delete immediate.
func TestSecretMappingDeleteRowBySeqFlushesQueuedAppend(t *testing.T) {
	st := configTransactionTestStore(t)
	ms, _ := openTestSecretMappingStore(t, st, st.OutputDir())
	defer ms.Close()

	pair := equalLengthPair("live-delete-", "A", "a")
	if err := ms.AppendMapping(pair); err != nil {
		t.Fatalf("AppendMapping() error = %v", err)
	}
	// No sleep and no explicit Flush here: the point is that the delete itself
	// covers the batch buffer.
	row, found, err := ms.DeleteMappingRowBySeq(1)
	if err != nil {
		t.Fatalf("DeleteMappingRowBySeq() error = %v", err)
	}
	if !found {
		t.Fatal("DeleteMappingRowBySeq() found = false, want the queued pair")
	}
	if row.Real != pair.Real || row.Fake != pair.Fake {
		t.Fatalf("deleted row = %+v, want the appended pair", row)
	}
	mappings, err := ms.LoadMappings(0)
	if err != nil {
		t.Fatalf("LoadMappings() error = %v", err)
	}
	if len(mappings) != 0 {
		t.Fatalf("LoadMappings() = %+v, want the row gone", mappings)
	}

	second := equalLengthPair("live-keep-", "B", "b")
	if err := ms.AppendMapping(second); err != nil {
		t.Fatalf("AppendMapping() error = %v", err)
	}
	if err := ms.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	var rows []secretmask.MappingRow
	if err := ms.IterateMappingRows(true, func(row secretmask.MappingRow) error {
		rows = append(rows, row)
		return nil
	}); err != nil {
		t.Fatalf("IterateMappingRows() error = %v", err)
	}
	if len(rows) != 1 || rows[0].Real != second.Real || rows[0].Fake != second.Fake {
		t.Fatalf("IterateMappingRows() = %+v, want the surviving pair", rows)
	}
	stats, err := ms.MappingRowStats()
	if err != nil {
		t.Fatalf("MappingRowStats() error = %v", err)
	}
	if stats.EntryCount != 1 || stats.Path == "" {
		t.Fatalf("MappingRowStats() = %+v, want one row and a path", stats)
	}
}
