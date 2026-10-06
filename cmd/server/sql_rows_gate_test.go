package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSQLLoopsCheckTheirError keeps a driver error at the end of a result set from being read as "no
// more rows".
//
// LoadObservationMetadata was the one place in the repository that iterated rows.Next() and returned
// without checking rows.Err(): a connection or decode failure mid-iteration produced a short map, the
// caller then treated every trace missing from it as `unparsed`, and nothing anywhere reported an
// error. golangci-lint only enables govet, ineffassign, staticcheck and unused, so no linter notices a
// missing Err() call. This gate parses each file and requires an Err() call on the same receiver inside
// the enclosing function; it fails if it finds no loops at all so it cannot pass vacuously.
func TestSQLLoopsCheckTheirError(t *testing.T) {
	roots := []string{"../../cmd", "../../internal", "../../pkg"}
	loops := 0
	var missing []string

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" || entry.Name() == "node_modules" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			found, unchecked := auditRowsLoops(path, body)
			loops += found
			missing = append(missing, unchecked...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if loops == 0 {
		t.Fatalf("found no rows.Next() loops under cmd, internal or pkg: this gate would pass vacuously")
	}
	if len(missing) > 0 {
		t.Fatalf("rows.Next() loop without an Err() check on the same receiver in the enclosing function: %s\n"+
			"Without it a driver error part-way through the result set looks like a complete, shorter answer. "+
			"Check the error after the loop (and close the rows) before using the result.",
			strings.Join(missing, ", "))
	}
}

// auditRowsLoops returns how many rows.Next() loops the file declares and the file:line of each one
// whose enclosing function never calls Err() on that same receiver.
func auditRowsLoops(path string, body []byte) (int, []string) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, body, 0)
	if err != nil {
		return 0, nil // an unparsable file is the compiler's problem, not this gate's
	}

	var loops int
	var unchecked []string
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		errPositions := rowsErrPositions(fn.Body)
		reassignments := rowsReassignments(fn.Body)
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			loop, ok := node.(*ast.ForStmt)
			if !ok || loop.Post != nil || loop.Cond == nil {
				return true
			}
			call, ok := loop.Cond.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Next" {
				return true
			}
			receiver, ok := selector.X.(*ast.Ident)
			if !ok || !strings.Contains(strings.ToLower(receiver.Name), "row") {
				return true
			}
			loops++
			checked := false
			// The error has to be checked after this loop and before the receiver is handed to the
			// next query: one function with two result sets checked Err() once, after the second
			// loop, while the first loop's rows were read unchecked.
			bound := fn.End()
			for _, position := range reassignments[receiver.Name] {
				if position > loop.End() && position < bound {
					bound = position
				}
			}
			for _, position := range errPositions[receiver.Name] {
				if position > loop.End() && position < bound {
					checked = true
					break
				}
			}
			if !checked {
				unchecked = append(unchecked, path+":"+strconv.Itoa(fset.Position(loop.Pos()).Line))
			}
			return true
		})
	}
	return loops, unchecked
}

// rowsReassignments maps each rows-like receiver to the positions where it is assigned again, which is
// where one result set ends and the next begins.
func rowsReassignments(body *ast.BlockStmt) map[string][]token.Pos {
	positions := map[string][]token.Pos{}
	ast.Inspect(body, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok || !strings.Contains(strings.ToLower(ident.Name), "row") {
				continue
			}
			positions[ident.Name] = append(positions[ident.Name], assign.Pos())
		}
		return true
	})
	return positions
}

// rowsErrPositions maps each rows-like receiver to the positions of the Err() calls on it.
func rowsErrPositions(body *ast.BlockStmt) map[string][]token.Pos {
	positions := map[string][]token.Pos{}
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Err" {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || !strings.Contains(strings.ToLower(receiver.Name), "row") {
			return true
		}
		positions[receiver.Name] = append(positions[receiver.Name], call.Pos())
		return true
	})
	return positions
}
