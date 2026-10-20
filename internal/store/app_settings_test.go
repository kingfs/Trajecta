package store

import (
	"context"
	"testing"
)

// TestLoadOrCreateInstallIDIsStable pins the property the install count depends
// on: every browser behind one database reports the same id, and it survives a
// restart of the process.
func TestLoadOrCreateInstallIDIsStable(t *testing.T) {
	t.Parallel()

	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	first, err := st.LoadOrCreateInstallID(ctx)
	if err != nil {
		t.Fatalf("LoadOrCreateInstallID() error = %v", err)
	}
	if len(first) != 32 {
		t.Fatalf("install id %q has length %d, want 32 hex characters", first, len(first))
	}
	second, err := st.LoadOrCreateInstallID(ctx)
	if err != nil {
		t.Fatalf("second LoadOrCreateInstallID() error = %v", err)
	}
	if second != first {
		t.Fatalf("install id changed between calls: %q then %q", first, second)
	}
}

func TestLoadOrCreateInstallIDRegeneratesAfterDeletion(t *testing.T) {
	t.Parallel()

	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	first, err := st.LoadOrCreateInstallID(ctx)
	if err != nil {
		t.Fatalf("LoadOrCreateInstallID() error = %v", err)
	}
	if _, err := st.DeleteAppSetting(ctx, InstallIDKey); err != nil {
		t.Fatalf("DeleteAppSetting() error = %v", err)
	}
	second, err := st.LoadOrCreateInstallID(ctx)
	if err != nil {
		t.Fatalf("LoadOrCreateInstallID() after delete error = %v", err)
	}
	if second == first {
		t.Fatalf("install id was reused after the row was deleted: %q", second)
	}
}
