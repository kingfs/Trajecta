package transfer

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateCassetteRelativePath(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", ".", "../outside.http", "a/../../outside.http", "/tmp/outside.http", `C:\outside.http`, "a//b.http", "a/./b.http", "a/../b.http", "a:b.http"} {
		if got, err := validateCassetteRelativePath(input); err == nil {
			t.Errorf("validateCassetteRelativePath(%q) = %q, nil; want rejection", input, got)
		}
	}
	for input, want := range map[string]string{
		"host/model/2026/09/trace.http": "host/model/2026/09/trace.http",
		`host\model\trace.http`:         "host/model/trace.http",
	} {
		got, err := validateCassetteRelativePath(input)
		if err != nil || got != want {
			t.Errorf("validateCassetteRelativePath(%q) = %q, %v; want %q, nil", input, got, err, want)
		}
	}
}

func TestTargetCassettePathRejectsTraversal(t *testing.T) {
	t.Parallel()

	state := &importState{
		manifest:  &Manifest{SourceOutputDir: "/source/data"},
		outputDir: "/target/traces",
	}
	for _, source := range []string{
		"/source/data/../outside.http",
		"/source/data/traces/../../outside.http",
		"../../source/traces/../../../outside.http",
		`C:\source\data\..\outside.http`,
	} {
		if got, rel, err := state.targetCassettePath(source); err == nil {
			t.Errorf("targetCassettePath(%q) = %q, %q, nil; want rejection", source, got, rel)
		}
	}
	got, rel, err := state.targetCassettePath(`/source/data/traces/host/model/trace.http`)
	if err != nil {
		t.Fatalf("targetCassettePath(valid) error = %v", err)
	}
	if rel != "traces/host/model/trace.http" || got != filepath.Join("/target/traces", "traces", "host", "model", "trace.http") {
		t.Fatalf("targetCassettePath(valid) = %q, %q; want target under output root", got, rel)
	}
	got, rel, err = (&importState{
		manifest:  &Manifest{SourceOutputDir: `C:\source\data`},
		outputDir: "/target",
	}).targetCassettePath(`C:\source\data\traces\host\trace.http`)
	if err != nil || rel != "traces/host/trace.http" || got != filepath.Join("/target", "traces", "host", "trace.http") {
		t.Fatalf("targetCassettePath(windows paths) = %q, %q, %v", got, rel, err)
	}
}

func TestCassetteImportRejectsSymlinkedDirectory(t *testing.T) {
	t.Parallel()

	rootPath := t.TempDir()
	outsidePath := t.TempDir()
	if err := os.Symlink(outsidePath, filepath.Join(rootPath, "traces")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("OpenRoot() error = %v", err)
	}
	defer root.Close()
	if err := ensureCassetteParentDirs(root, "traces/host/trace.http"); err == nil {
		t.Fatal("ensureCassetteParentDirs() error = nil, want symlink rejection")
	}
	if _, err := os.Stat(filepath.Join(outsidePath, "host")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("symlink target was modified: stat error = %v", err)
	}
}

func TestImportRollbackRestoresReplacedCassetteAndRemovesNewCassette(t *testing.T) {
	t.Parallel()

	store := newTargetStore(t)
	outputDir := filepath.Join(t.TempDir(), "traces")
	existingRel := "host/model/existing.http"
	newRel := "host/model/new.http"
	existingPath := filepath.Join(outputDir, filepath.FromSlash(existingRel))
	if err := os.MkdirAll(filepath.Dir(existingPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(existing) error = %v", err)
	}
	oldContent := []byte("old cassette contents")
	if err := os.WriteFile(existingPath, oldContent, 0o644); err != nil {
		t.Fatalf("WriteFile(existing) error = %v", err)
	}
	newBundle := filepath.Join(t.TempDir(), "rollback.tar")
	writeTarBundle(t, newBundle, []tarFixtureEntry{
		{name: entryCassettesPrefix + existingRel, body: []byte("replacement cassette content"), modTime: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)},
		{name: entryCassettesPrefix + newRel, body: []byte("new cassette content"), modTime: time.Date(2026, 9, 10, 12, 1, 0, 0, time.UTC)},
		{name: "unexpected-after-cassettes", body: []byte("abort import")},
	})

	_, err := Import(context.Background(), store, outputDir, ImportOptions{Bundle: newBundle, Apply: true})
	if err == nil {
		t.Fatal("Import() error = nil, want malformed trailing entry to abort")
	}
	got, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatalf("ReadFile(existing) error = %v", err)
	}
	if string(got) != string(oldContent) {
		t.Fatalf("existing cassette after rollback = %q, want %q", got, oldContent)
	}
	info, err := os.Stat(existingPath)
	if err != nil {
		t.Fatalf("Stat(existing cassette) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("existing cassette permissions after staging = %04o, want restrictive 0600", got)
	}
	if _, err := os.Stat(filepath.Join(outputDir, filepath.FromSlash(newRel))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("new cassette survived rollback: stat error = %v", err)
	}
	var leftovers []string
	if err := filepath.WalkDir(outputDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.Contains(entry.Name(), ".trajecta-import-") {
			leftovers = append(leftovers, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("rollback left import staging files: %v", leftovers)
	}
}

type tarFixtureEntry struct {
	name    string
	body    []byte
	modTime time.Time
}

func writeTarBundle(t *testing.T, destination string, entries []tarFixtureEntry) {
	t.Helper()
	file, err := os.Create(destination)
	if err != nil {
		t.Fatalf("Create(bundle) error = %v", err)
	}
	writer := tar.NewWriter(file)
	manifest := Manifest{
		Format:      BundleFormat,
		CreatedAt:   time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
		Window:      Window{All: true},
		Compression: compressionNone,
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	allEntries := append([]tarFixtureEntry{{name: entryManifest, body: manifestData}}, entries...)
	for _, entry := range allEntries {
		modTime := entry.modTime
		if modTime.IsZero() {
			modTime = manifest.CreatedAt
		}
		if err := writer.WriteHeader(&tar.Header{
			Name:     entry.name,
			Mode:     0o600,
			Size:     int64(len(entry.body)),
			Typeflag: tar.TypeReg,
			ModTime:  modTime,
		}); err != nil {
			_ = writer.Close()
			_ = file.Close()
			t.Fatalf("WriteHeader(%q) error = %v", entry.name, err)
		}
		if _, err := writer.Write(entry.body); err != nil {
			_ = writer.Close()
			_ = file.Close()
			t.Fatalf("Write(%q) error = %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		t.Fatalf("tar.Close() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("file.Close() error = %v", err)
	}
}
