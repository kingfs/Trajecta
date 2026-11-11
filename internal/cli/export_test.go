package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeneratedBundleNamePutsTheWriteMomentFirst(t *testing.T) {
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	got := generatedBundleName(exportTargetOptions{
		Since: &since,
		Until: &until,
		Now:   time.Date(2026, 11, 11, 14, 32, 55, 0, time.UTC),
	}, ".tar.gz")
	want := "trajecta-export-20261111T143255Z-20260901-20261001.tar.gz"
	if got != want {
		t.Fatalf("generatedBundleName() = %q, want %q", got, want)
	}
}

func TestGeneratedBundleNameUsesAllForAnUnwindowedExport(t *testing.T) {
	got := generatedBundleName(exportTargetOptions{
		All: true,
		Now: time.Date(2026, 11, 11, 14, 32, 55, 0, time.UTC),
	}, ".tar")
	want := "trajecta-export-20261111T143255Z-all.tar"
	if got != want {
		t.Fatalf("generatedBundleName() = %q, want %q", got, want)
	}
}

func TestGeneratedBundleNameFillsAnOpenEndedWindow(t *testing.T) {
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	got := generatedBundleName(exportTargetOptions{
		Since: &since,
		Now:   time.Date(2026, 11, 11, 14, 32, 55, 0, time.UTC),
	}, ".tar.gz")
	want := "trajecta-export-20261111T143255Z-20260901-now.tar.gz"
	if got != want {
		t.Fatalf("generatedBundleName() = %q, want %q", got, want)
	}
}

func TestNameSortsChronologically(t *testing.T) {
	earlier := generatedBundleName(exportTargetOptions{All: true, Now: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}, ".tar.gz")
	later := generatedBundleName(exportTargetOptions{All: true, Now: time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC)}, ".tar.gz")
	if !(earlier < later) {
		t.Fatalf("names do not sort chronologically: %q >= %q", earlier, later)
	}
}

func TestResolveExportBundlePathDefaultsToBackupsDirectory(t *testing.T) {
	dir := t.TempDir()
	path, err := resolveExportBundlePath(exportTargetOptions{
		OutputDir: dir,
		All:       true,
		Now:       time.Date(2026, 11, 11, 14, 32, 55, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("resolveExportBundlePath() error = %v", err)
	}
	wantDir := filepath.Join(dir, "backups")
	if filepath.Dir(path) != wantDir {
		t.Fatalf("bundle path = %q, want a file inside %q", path, wantDir)
	}
	if !strings.HasSuffix(path, ".tar.gz") {
		t.Fatalf("bundle path = %q, want a .tar.gz suffix", path)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Fatalf("backups directory was not created: %v", err)
	}
}

func TestResolveExportBundlePathCreatesTheGivenDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backup")
	path, err := resolveExportBundlePath(exportTargetOptions{
		Out:       dir,
		OutputDir: t.TempDir(),
		All:       true,
		Now:       time.Date(2026, 11, 11, 14, 32, 55, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("resolveExportBundlePath() error = %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("bundle path = %q, want a file inside %q", path, dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("--out directory was not created: %v", err)
	}
}

func TestResolveExportBundlePathNeverReusesAGeneratedName(t *testing.T) {
	dir := t.TempDir()
	opts := exportTargetOptions{
		Out:       dir,
		OutputDir: t.TempDir(),
		All:       true,
		Now:       time.Date(2026, 11, 11, 14, 32, 55, 0, time.UTC),
	}
	first, err := resolveExportBundlePath(opts)
	if err != nil {
		t.Fatalf("first resolve error = %v", err)
	}
	if err := os.WriteFile(first, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := resolveExportBundlePath(opts)
	if err != nil {
		t.Fatalf("second resolve error = %v", err)
	}
	if first == second {
		t.Fatalf("second resolve reused %q, want a new name", first)
	}
	if !strings.HasSuffix(second, "-2.tar.gz") {
		t.Fatalf("second resolve = %q, want a -2 suffix", second)
	}
}

func TestResolveExportBundlePathHonoursAnExplicitFileName(t *testing.T) {
	dir := t.TempDir()
	path, err := resolveExportBundlePath(exportTargetOptions{
		Out:       dir,
		OutputDir: t.TempDir(),
		Name:      "office-september",
		All:       true,
	})
	if err != nil {
		t.Fatalf("resolveExportBundlePath() error = %v", err)
	}
	if got, want := path, filepath.Join(dir, "office-september.tar.gz"); got != want {
		t.Fatalf("bundle path = %q, want %q", got, want)
	}
}

func TestResolveExportBundlePathKeepsAnExplicitFileForScripts(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "bundle.tar")
	path, err := resolveExportBundlePath(exportTargetOptions{
		Out:         explicit,
		OutputDir:   t.TempDir(),
		Compression: "none",
		All:         true,
	})
	if err != nil {
		t.Fatalf("resolveExportBundlePath() error = %v", err)
	}
	if path != explicit {
		t.Fatalf("bundle path = %q, want the explicit %q", path, explicit)
	}
}

func TestResolveExportBundlePathRejectsAFileNameAsOut(t *testing.T) {
	dir := t.TempDir()
	// An existing regular file passed as --out, with no archive suffix, is a
	// mistake: the operator meant a directory.
	file := filepath.Join(dir, "backup")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExportBundlePath(exportTargetOptions{Out: file, All: true}); err == nil {
		t.Fatal("resolveExportBundlePath() accepted a regular file as --out")
	}
}

func TestResolveExportBundlePathRejectsAPathInName(t *testing.T) {
	if _, err := resolveExportBundlePath(exportTargetOptions{
		Out:       t.TempDir(),
		OutputDir: t.TempDir(),
		Name:      "sub/dir/bundle",
		All:       true,
	}); err == nil {
		t.Fatal("resolveExportBundlePath() accepted a path separator in --name")
	}
}
