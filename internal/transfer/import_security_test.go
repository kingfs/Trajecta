package transfer

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateCassetteRelativePath(t *testing.T) {
	valid := []string{"a.http", "site/model/2026/01/01/a.http"}
	for _, rel := range valid {
		if _, err := validateCassetteRelativePath(rel); err != nil {
			t.Fatalf("validateCassetteRelativePath(%q) error = %v", rel, err)
		}
	}
	invalid := []string{
		"",
		".",
		"..",
		"../escape",
		"a/../../escape",
		"/absolute",
		`a\..\..\escape`,
		"C:/escape",
		"a\x00b",
	}
	for _, rel := range invalid {
		if _, err := validateCassetteRelativePath(rel); err == nil {
			t.Fatalf("validateCassetteRelativePath(%q) = nil, want error", rel)
		}
	}
}

func newImportSecurityState(t *testing.T, dir string) *importState {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	s := newImportState(context.Background(), nil, NewDiscardLogger(), dir, true, &ImportResult{}, nil)
	s.root = root
	return s
}

func TestWriteCassetteFileRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "escape.http")
	s := newImportSecurityState(t, dir)
	created := []string{}

	err := s.writeCassetteFile(&tar.Header{Name: "escape", Size: 1}, strings.NewReader("x"), "../escape.http", desiredFile{}, &created, false)
	if err == nil {
		t.Fatal("writeCassetteFile(../escape.http) = nil, want error")
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Fatalf("file escaped the output root: %s", outside)
	}
}

func TestWriteCassetteFileRejectsSymlinkedDirectory(t *testing.T) {
	rootDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(rootDir, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	s := newImportSecurityState(t, rootDir)
	created := []string{}

	err := s.writeCassetteFile(&tar.Header{Name: "x", Size: 1}, strings.NewReader("x"), "link/x.http", desiredFile{}, &created, false)
	if err == nil {
		t.Fatal("writeCassetteFile(link/x.http) = nil, want error")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "x.http")); statErr == nil {
		t.Fatal("file was written through a symlinked directory")
	}
}
