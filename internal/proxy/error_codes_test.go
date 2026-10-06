package proxy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestClientVisibleErrorCodesAreDocumented pins the set of error codes a client can receive
// against the table clients are told to branch on.
//
// The code is a stable identifier in the failure envelope: an SDK that retries on
// `rate_limited` or surfaces `no_supporting_target` breaks the moment a code appears, changes
// or disappears without the documentation moving with it. The proxy has two ways to produce
// one - a literal passed to writeProxyError, and whatever router.SelectionFailureReason
// returns, which the forwarding error paths pass straight through - so the test reads both
// out of the source rather than keeping a hand-written list that would drift in the same way
// the documentation did.
//
// Both directions are checked: a code the source can emit and the table omits fails, and a
// table row no source can emit fails as a stale claim. An argument that is neither a literal
// nor the selector's reason is rejected instead of skipped, so a future call site cannot slip
// through by being unreadable.
func TestClientVisibleErrorCodesAreDocumented(t *testing.T) {
	emitted := emittedProxyErrorCodes(t)
	documented := documentedClientErrorCodes(t)

	if len(emitted) == 0 {
		t.Fatal("no error codes were found in the package; the source scan is broken, not the code")
	}

	var missing, stale []string
	for code := range emitted {
		if _, ok := documented[code]; !ok {
			missing = append(missing, code)
		}
	}
	for code := range documented {
		if _, ok := emitted[code]; !ok {
			stale = append(stale, code)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	if len(missing) != 0 {
		t.Errorf("the proxy can answer with error codes that docs/PROXY_USAGE_EXAMPLES.md does not document: %s", strings.Join(missing, ", "))
	}
	if len(stale) != 0 {
		t.Errorf("docs/PROXY_USAGE_EXAMPLES.md documents error codes no code path emits: %s", strings.Join(stale, ", "))
	}
}

// emittedProxyErrorCodes returns every code a client can see, read from the package's own
// source: the literal codes at writeProxyError call sites plus every router selection failure
// constant those call sites pass through.
func emittedProxyErrorCodes(t *testing.T) map[string]struct{} {
	t.Helper()

	codes := map[string]struct{}{}
	usesSelectorReason := false
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.) error = %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s) error = %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); !ok || ident.Name != "writeProxyError" {
				return true
			}
			// writeProxyError(w, r, statusCode, code, message)
			if len(call.Args) != 5 {
				t.Errorf("%s: writeProxyError called with %d arguments, want 5; the error code is argument 4", fset.Position(call.Pos()), len(call.Args))
				return true
			}
			switch code := call.Args[3].(type) {
			case *ast.BasicLit:
				if code.Kind != token.STRING {
					t.Errorf("%s: the error code argument is not a string literal", fset.Position(code.Pos()))
					return true
				}
				value, err := strconv.Unquote(code.Value)
				if err != nil {
					t.Errorf("%s: cannot read the error code literal: %v", fset.Position(code.Pos()), err)
					return true
				}
				codes[value] = struct{}{}
			case *ast.CallExpr:
				selector, ok := code.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "SelectionFailureReason" {
					t.Errorf("%s: the error code argument is neither a literal nor router.SelectionFailureReason(), so this gate cannot verify it", fset.Position(code.Pos()))
					return true
				}
				usesSelectorReason = true
			default:
				t.Errorf("%s: the error code argument is neither a literal nor router.SelectionFailureReason(), so this gate cannot verify it", fset.Position(call.Args[3].Pos()))
			}
			return true
		})
	}

	if usesSelectorReason {
		for code := range routerSelectionFailureCodes(t) {
			codes[code] = struct{}{}
		}
	}
	return codes
}

// routerSelectionFailureCodes reads the SelectionFailure* constants the forwarding paths pass
// to writeProxyError, so the reasons the router can report are covered without listing them
// twice.
func routerSelectionFailureCodes(t *testing.T) map[string]struct{} {
	t.Helper()
	const source = "../../internal/router/router.go"
	parsed, err := parser.ParseFile(token.NewFileSet(), source, nil, 0)
	if err != nil {
		t.Fatalf("ParseFile(%s) error = %v", source, err)
	}
	codes := map[string]struct{}{}
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Values) != 1 {
				continue
			}
			lit, ok := value.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			for _, name := range value.Names {
				if !strings.HasPrefix(name.Name, "SelectionFailure") {
					continue
				}
				code, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: cannot read %s: %v", source, name.Name, err)
				}
				codes[code] = struct{}{}
			}
		}
	}
	if len(codes) == 0 {
		t.Fatalf("%s declares no SelectionFailure* constants; the scan is broken, not the code", source)
	}
	return codes
}

const (
	clientErrorTableDoc     = "../../docs/PROXY_USAGE_EXAMPLES.md"
	clientErrorTableStart   = "<!-- trajecta:client-error-codes:start -->"
	clientErrorTableEnd     = "<!-- trajecta:client-error-codes:end -->"
	clientErrorTableRowExpr = "^\\| `([a-z0-9_]+)` \\|"
)

// documentedClientErrorCodes parses the marked table in the client usage document.
func documentedClientErrorCodes(t *testing.T) map[string]struct{} {
	t.Helper()
	content, err := os.ReadFile(clientErrorTableDoc)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", clientErrorTableDoc, err)
	}
	lines := strings.Split(string(content), "\n")
	start, end := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case clientErrorTableStart:
			start = i
		case clientErrorTableEnd:
			end = i
		}
	}
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("%s must contain the %s and %s markers around the client error code table", clientErrorTableDoc, clientErrorTableStart, clientErrorTableEnd)
	}

	row := regexp.MustCompile(clientErrorTableRowExpr)
	codes := map[string]struct{}{}
	for _, line := range lines[start+1 : end] {
		match := row.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		codes[match[1]] = struct{}{}
	}
	if len(codes) == 0 {
		t.Fatalf("no rows found in the client error code table in %s", clientErrorTableDoc)
	}

	// The table must sit in the repository, not in a copy of it: fail loudly if the path
	// resolved to something else (for example a vendored checkout).
	if abs, err := filepath.Abs(clientErrorTableDoc); err == nil && !strings.Contains(abs, string(filepath.Separator)+"docs"+string(filepath.Separator)) {
		t.Fatalf("the client error code table resolved to %s, want a path under docs/", abs)
	}
	return codes
}
