package store

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

func openTestSecretMappingStore(t *testing.T, st *Store, outputDir string) (*SecretMappingStore, string) {
	t.Helper()
	path := filepath.Join(outputDir, SecretMappingFileName)
	ms, err := st.OpenSecretMappingStore(path)
	if err != nil {
		t.Fatalf("OpenSecretMappingStore() error = %v", err)
	}
	return ms, path
}

func equalLengthPair(prefix, realFill, fakeFill string) secretmask.Mapping {
	return secretmask.Mapping{
		Real: prefix + strings.Repeat(realFill, 64),
		Fake: prefix + strings.Repeat(fakeFill, 64),
	}
}

// TestSecretMappingStoreRoundTrip pins the contract a Vault depends on: pairs
// survive a close and reopen, the newest load first, a real value has exactly
// one row however often it is appended, and the file does not contain either
// side in the clear.
func TestSecretMappingStoreRoundTrip(t *testing.T) {
	outputDir := t.TempDir()
	st, err := New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	first := equalLengthPair("sk-live-", "A", "B")
	second := equalLengthPair("sk-live-", "C", "D")
	duplicate := first
	duplicate.Fake = "sk-live-" + strings.Repeat("E", 64)

	ms, path := openTestSecretMappingStore(t, st, outputDir)
	for _, mapping := range []secretmask.Mapping{first, second, duplicate} {
		if err := ms.AppendMapping(mapping); err != nil {
			t.Fatalf("AppendMapping() error = %v", err)
		}
	}
	if err := ms.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// The duplicate was appended while the first pair was already queued or
	// written; either way it must not create a second row.

	reopened, err := st.OpenSecretMappingStore(path)
	if err != nil {
		t.Fatalf("reopen OpenSecretMappingStore() error = %v", err)
	}
	defer reopened.Close()

	// Zero means every pair, which is the load-all default.
	loaded, err := reopened.LoadMappings(0)
	if err != nil {
		t.Fatalf("LoadMappings() error = %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d mappings, want 2 (duplicate Real must not add a row): %+v", len(loaded), loaded)
	}
	// Newest first: the second append is newer than the first.
	if loaded[0].Real != second.Real || loaded[1].Real != first.Real {
		t.Fatalf("load order = %q, %q; want the newest first", loaded[0].Real, loaded[1].Real)
	}

	// The duplicate append must not have replaced the first fake.
	got, found, err := reopened.LookupMapping(first.Real)
	if err != nil {
		t.Fatalf("LookupMapping() error = %v", err)
	}
	if !found {
		t.Fatal("LookupMapping() did not find the first pair")
	}
	if got.Fake != first.Fake {
		t.Fatalf("LookupMapping().Fake = %q, want the first fake %q", got.Fake, first.Fake)
	}
	if _, found, err := reopened.LookupMapping("sk-live-missing"); err != nil || found {
		t.Fatalf("LookupMapping(missing) = %v, %v; want not found and no error", found, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mapping file: %v", err)
	}
	for _, secret := range []string{first.Real, first.Fake, second.Real, second.Fake} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("the mapping file contains a value in the clear: %q", secret)
		}
	}
	// The dedupe index must not be an unkeyed digest: a mobile number has too
	// little entropy for that.
	unkeyed := sha256.Sum256([]byte(first.Real))
	if bytes.Contains(raw, unkeyed[:]) {
		t.Fatal("the mapping file contains an unkeyed SHA-256 of a real value")
	}
}

// TestSecretMappingStoreLoadLimit pins that LoadMappings keeps the newest rows,
// which is what bounds a restart's memory.
func TestSecretMappingStoreLoadLimit(t *testing.T) {
	outputDir := t.TempDir()
	st, err := New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	ms, path := openTestSecretMappingStore(t, st, outputDir)
	fills := []string{"A", "B", "C", "D", "E"}
	for _, fill := range fills {
		if err := ms.AppendMapping(equalLengthPair("key-", fill, strings.ToLower(fill))); err != nil {
			t.Fatalf("AppendMapping() error = %v", err)
		}
	}
	if err := ms.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := st.OpenSecretMappingStore(path)
	if err != nil {
		t.Fatalf("reopen OpenSecretMappingStore() error = %v", err)
	}
	defer reopened.Close()
	loaded, err := reopened.LoadMappings(2)
	if err != nil {
		t.Fatalf("LoadMappings() error = %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d mappings, want 2", len(loaded))
	}
	if loaded[0].Real != "key-"+strings.Repeat("E", 64) {
		t.Fatalf("first loaded = %q, want the newest", loaded[0].Real)
	}
	if loaded[1].Real != "key-"+strings.Repeat("D", 64) {
		t.Fatalf("second loaded = %q, want the second newest", loaded[1].Real)
	}
}

// TestSecretMappingStoreFlushesOnClose pins that an append that has not reached
// a batch yet is still durable when the process shuts down cleanly.
func TestSecretMappingStoreFlushesOnClose(t *testing.T) {
	outputDir := t.TempDir()
	st, err := New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	ms, path := openTestSecretMappingStore(t, st, outputDir)
	mapping := equalLengthPair("sk-close-", "A", "B")
	if err := ms.AppendMapping(mapping); err != nil {
		t.Fatalf("AppendMapping() error = %v", err)
	}
	if err := ms.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := ms.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := ms.AppendMapping(mapping); !errors.Is(err, ErrSecretMappingStoreClosed) {
		t.Fatalf("AppendMapping() after Close() error = %v, want ErrSecretMappingStoreClosed", err)
	}

	reopened, err := st.OpenSecretMappingStore(path)
	if err != nil {
		t.Fatalf("reopen OpenSecretMappingStore() error = %v", err)
	}
	defer reopened.Close()
	got, found, err := reopened.LookupMapping(mapping.Real)
	if err != nil || !found {
		t.Fatalf("LookupMapping() = %v, %v; want the pair flushed by Close", found, err)
	}
	if got.Fake != mapping.Fake {
		t.Fatalf("flushed fake = %q, want %q", got.Fake, mapping.Fake)
	}
}

func TestSecretMappingStoreRejectsMalformedPair(t *testing.T) {
	outputDir := t.TempDir()
	st, err := New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()
	ms, _ := openTestSecretMappingStore(t, st, outputDir)
	defer ms.Close()

	for _, mapping := range []secretmask.Mapping{
		{},
		{Real: "real", Fake: ""},
		{Real: "", Fake: "fake"},
		{Real: "same", Fake: "same"},
		{Real: "short", Fake: "longer-than-short"},
	} {
		if err := ms.AppendMapping(mapping); err == nil {
			t.Fatalf("AppendMapping(%+v) error = nil, want a rejection", mapping)
		}
	}
}

// TestSecretMappingStoreConcurrentAppends pins that the background queue and
// its synchronous fallback together lose nothing under the burst a busy proxy
// can produce, and that closing under that burst still flushes everything.
func TestSecretMappingStoreConcurrentAppends(t *testing.T) {
	outputDir := t.TempDir()
	st, err := New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()
	ms, path := openTestSecretMappingStore(t, st, outputDir)

	const workers = 200
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			mapping := secretmask.Mapping{
				Real: fmt.Sprintf("burst-real-%03d-%s", i, strings.Repeat("r", 40)),
				Fake: fmt.Sprintf("burst-fake-%03d-%s", i, strings.Repeat("f", 40)),
			}
			if err := ms.AppendMapping(mapping); err != nil {
				t.Errorf("AppendMapping(%d) error = %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if err := ms.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := st.OpenSecretMappingStore(path)
	if err != nil {
		t.Fatalf("reopen OpenSecretMappingStore() error = %v", err)
	}
	defer reopened.Close()
	loaded, err := reopened.LoadMappings(workers)
	if err != nil {
		t.Fatalf("LoadMappings() error = %v", err)
	}
	if len(loaded) != workers {
		t.Fatalf("loaded %d mappings, want %d", len(loaded), workers)
	}
}
