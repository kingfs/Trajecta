package unittest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFormatGatesCoverEveryGoDirectory keeps the gofmt gates aligned with the
// Go package layout. `./tests` was missing from task fmt, task fmt:check and the
// CI job, so format regressions in a real package could pass every gate.
func TestFormatGatesCoverEveryGoDirectory(t *testing.T) {
	t.Parallel()

	gates := []struct {
		name     string
		path     string
		contains []string
	}{
		{name: "Taskfile fmt", path: "Taskfile.yml", contains: []string{"gofmt -w "}},
		{name: "Taskfile fmt:check", path: "Taskfile.yml", contains: []string{"gofmt -l "}},
		{name: "CI gofmt", path: filepath.Join(".github", "workflows", "ci.yml"), contains: []string{"gofmt -l "}},
	}
	topLevel, err := goDirectories(t)
	if err != nil {
		t.Fatalf("goDirectories() error = %v", err)
	}
	if len(topLevel) == 0 {
		t.Fatalf("goDirectories() returned no directories")
	}

	for _, gate := range gates {
		content, err := os.ReadFile(filepath.Join("..", gate.path))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", gate.path, err)
		}
		text := string(content)
		for _, needle := range gate.contains {
			line := findLine(text, needle)
			if line == "" {
				t.Fatalf("%s: no %q line found in %s", gate.name, needle, gate.path)
			}
			arguments := strings.Fields(strings.SplitN(line, needle, 2)[1])
			for _, dir := range topLevel {
				if !containsArgument(arguments, dir) {
					t.Fatalf("%s: %q lists %v, which does not cover %s; every top-level Go directory must be formatted by the gate", gate.name, needle, arguments, dir)
				}
			}
		}
	}
}

// goDirectories lists the top-level directories that hold Go packages.
func goDirectories(t *testing.T) ([]string, error) {
	t.Helper()

	entries, err := os.ReadDir("..")
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := entry.Name()
		if name == "node_modules" || name == "ent" {
			// `ent` is generated code; gofmt still covers it through ./internal
			// imports but it is not part of the hand-written gate list.
			continue
		}
		matches, err := filepath.Glob(filepath.Join("..", name, "*.go"))
		if err != nil {
			return nil, err
		}
		if len(matches) > 0 {
			dirs = append(dirs, "./"+name)
		}
	}
	return dirs, nil
}

// findLine returns the first line containing needle.
func findLine(text string, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// containsArgument reports whether the argument list mentions dir, either as a
// bare token or as the first path of a gofmt-style argument group.
func containsArgument(arguments []string, dir string) bool {
	for _, argument := range arguments {
		trimmed := strings.Trim(argument, `"'`)
		if trimmed == dir || strings.HasPrefix(trimmed, dir+"/") {
			return true
		}
	}
	return false
}
