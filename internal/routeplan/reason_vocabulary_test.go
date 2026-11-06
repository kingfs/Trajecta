package routeplan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryReasonConstantHasAnEmitter keeps the reason vocabulary honest.
//
// A constant in the reason block is a promise that internal/router.Explain can
// report that string to the Monitor. After the routeplan planner was deleted the
// package still declared six reasons nothing could emit, which made the live
// vocabulary and the declared vocabulary disagree. This gate fails when a
// declared reason is not referenced by any non-test file outside this package,
// so a dead value cannot be added silently again.
func TestEveryReasonConstantHasAnEmitter(t *testing.T) {
	declared := declaredReasonConstants(t)
	emitted := emittedReasonConstants(t)
	for _, name := range declared {
		if !emitted[name] {
			t.Errorf("routeplan.%s is declared but no non-test code outside internal/routeplan emits it; delete it or add the code that reports it", name)
		}
	}
}

// declaredReasonConstants collects the exported reason constant names from this
// package's non-test sources.
func declaredReasonConstants(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			decl, ok := node.(*ast.GenDecl)
			if !ok || decl.Tok != token.CONST {
				return true
			}
			for _, spec := range decl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, ident := range valueSpec.Names {
					if strings.HasPrefix(ident.Name, "Reason") {
						names = append(names, ident.Name)
					}
				}
			}
			return true
		})
	}
	if len(names) == 0 {
		t.Fatal("found no Reason constants in internal/routeplan; the scan is looking in the wrong place")
	}
	return names
}

// emittedReasonConstants scans the Go packages that can import this one for
// `routeplan.<Reason…>` references in non-test files.
func emittedReasonConstants(t *testing.T) map[string]bool {
	t.Helper()
	root := filepath.Join("..", "..")
	reference := regexp.MustCompile(`\brouteplan\.(Reason[A-Za-z0-9_]+)\b`)
	out := map[string]bool{}
	for _, dir := range []string{"internal", "pkg", "cmd"} {
		base := filepath.Join(root, dir)
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "node_modules" || entry.Name() == "worktrees" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, match := range reference.FindAllStringSubmatch(string(source), -1) {
				out[match[1]] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
	if len(out) == 0 {
		t.Fatal("found no routeplan.Reason references outside internal/routeplan; the scan is looking in the wrong place")
	}
	return out
}
