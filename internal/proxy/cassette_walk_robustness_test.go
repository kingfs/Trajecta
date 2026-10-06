package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestRecordedFileWalksTolerateAtomicRewrites pins that scanning a cassette directory while the
// recorder is writing into it cannot fail the scan.
//
// The recorder publishes a cassette by writing a hidden `.cassette-rewrite-*` file next to it and
// renaming it into place, so a directory read can name an entry that is gone by the time a walker
// stats it. `filepath.Walk` lstats every entry and returns ENOENT for that entry, which turned the
// recorded-output helpers into a flake under load (observed once in a full `task check:full` run
// as `Walk(...) error = lstat .../.cassette-rewrite-828511044: no such file or directory`);
// `filepath.WalkDir` uses the entry it read and does not re-stat it.
func TestRecordedFileWalksTolerateAtomicRewrites(t *testing.T) {
	root := t.TempDir()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for writer := 0; writer < 4; writer++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			state := seed
			next := func() int {
				state = state*6364136223846793005 + 1442695040888963407
				return int(state >> 33)
			}
			for {
				select {
				case <-stop:
					return
				default:
				}
				temp := filepath.Join(root, fmt.Sprintf(".cassette-rewrite-%d", next()%4096))
				if err := os.WriteFile(temp, []byte("partial"), 0o600); err != nil {
					continue
				}
				final := filepath.Join(root, fmt.Sprintf("cassette-%d.http", next()%4096))
				_ = os.Rename(temp, final)
			}
		}(uint64(writer) + 1)
	}

	// The strict walk this gate replaces failed at iterations 3, 4 and 15 during the fix's own
	// verification, so a few hundred attempts reproduce the race with room to spare while keeping
	// the gate fast enough for the default suite.
	for i := 0; i < 300; i++ {
		if err := walkRecordedHTTPFiles(root, func(string) bool { return false }); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("walkRecordedHTTPFiles() iteration %d error = %v; a cassette the recorder rewrote mid-walk must not fail the scan", i, err)
		}
	}
	close(stop)
	wg.Wait()

	// The helper still finds what is there: a completed cassette is reported.
	final := filepath.Join(root, "expected.http")
	if err := os.WriteFile(final, []byte("HTTP/1.1 200 OK\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", final, err)
	}
	found := 0
	if err := walkRecordedHTTPFiles(root, func(string) bool { found++; return false }); err != nil {
		t.Fatalf("walkRecordedHTTPFiles() error = %v", err)
	}
	if found == 0 {
		t.Fatal("walkRecordedHTTPFiles() reported no .http files; the scan found nothing to visit")
	}
}
