package proxy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
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
		// A local variable can carry the selector's reason: recordSelectionFailureWithBody
		// resolves it once and hands it to the envelope builder.
		localSelectorReason := map[string]struct{}{}
		ast.Inspect(file, func(node ast.Node) bool {
			assign, ok := node.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, rhs := range assign.Rhs {
				call, ok := rhs.(*ast.CallExpr)
				if !ok {
					continue
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "SelectionFailureReason" || i >= len(assign.Lhs) {
					continue
				}
				if ident, ok := assign.Lhs[i].(*ast.Ident); ok {
					localSelectorReason[ident.Name] = struct{}{}
				}
			}
			return true
		})

		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			var codeArg ast.Expr
			switch ident.Name {
			case "writeProxyError":
				// writeProxyError(w, r, statusCode, code, message)
				if len(call.Args) != 5 {
					t.Errorf("%s: writeProxyError called with %d arguments, want 5; the error code is argument 4", fset.Position(call.Pos()), len(call.Args))
					return true
				}
				codeArg = call.Args[3]
			case "proxyErrorEnvelope":
				// proxyErrorEnvelope(r, statusCode, code, message)
				if len(call.Args) != 4 {
					t.Errorf("%s: proxyErrorEnvelope called with %d arguments, want 4; the error code is argument 3", fset.Position(call.Pos()), len(call.Args))
					return true
				}
				if name == "errors.go" {
					// writeProxyError forwards its own code argument here; the caller's site is
					// already counted.
					return true
				}
				codeArg = call.Args[2]
			default:
				return true
			}
			switch code := codeArg.(type) {
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
			case *ast.Ident:
				if _, ok := localSelectorReason[code.Name]; !ok {
					t.Errorf("%s: the error code argument is neither a literal nor router.SelectionFailureReason(), so this gate cannot verify it", fset.Position(code.Pos()))
					return true
				}
				usesSelectorReason = true
			default:
				t.Errorf("%s: the error code argument is neither a literal nor router.SelectionFailureReason(), so this gate cannot verify it", fset.Position(codeArg.Pos()))
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

// TestClientVisibleErrorCodeStatusesAreDocumented pins the other half of the same contract: the
// HTTP status each code is answered with.
//
// Code and status together are what a client branches on - 4xx means "fix the request", 5xx means
// "retry" - so a code that keeps its name while moving between statuses is a silent retry-policy
// change. The table documents both columns and TestClientVisibleErrorCodesAreDocumented verifies
// only the code column, which left the status column as a claim nothing checked.
//
// Statuses are read from the source as well: a `http.Status*` selector is resolved to its number,
// and a status held in a local variable is resolved through the function that produced it (the
// rate-limit rejection status, for instance, is 429 or 503 depending on the rejection reason). A
// code whose status is genuinely not a fixed set must be declared in dynamicStatusCodes next to
// the reason, and its documentation cell must not spell out a number, so neither a new dynamic
// code nor a typo can quietly opt out of the comparison.
func TestClientVisibleErrorCodeStatusesAreDocumented(t *testing.T) {
	static, dynamic, complete := emittedProxyErrorStatuses(t)
	if !complete {
		t.Fatal("the status scan could not read a status expression; fix that first, because the comparison below would report the affected codes as undocumented")
	}
	documented := documentedClientErrorStatuses(t)

	if len(static) == 0 {
		t.Fatal("no error statuses were found in the package; the source scan is broken, not the code")
	}

	codes := documentedClientErrorCodes(t)
	for code, cell := range documented {
		if _, ok := codes[code]; !ok {
			continue // the code column is owned by the other gate
		}
		if len(cell) == 0 {
			if _, declared := dynamicStatusCodes[code]; !declared {
				t.Errorf("docs/PROXY_USAGE_EXAMPLES.md gives error code %q a status that is not a number; declare it in dynamicStatusCodes here with the reason its status is not a fixed set", code)
			}
			if !dynamic[code] {
				t.Errorf("docs/PROXY_USAGE_EXAMPLES.md documents the status of %q as dynamic, but the code resolves it to a fixed set %v", code, static[code])
			}
			continue
		}
		if _, declared := dynamicStatusCodes[code]; declared {
			t.Errorf("dynamicStatusCodes declares the status of %q dynamic, but the table documents %v; drop the declaration", code, cell)
		}
		got, ok := static[code]
		if !ok {
			t.Errorf("docs/PROXY_USAGE_EXAMPLES.md documents status %v for error code %q, which no code path emits", cell, code)
			continue
		}
		if !sameStatusSet(cell, got) {
			t.Errorf("error code %q is documented with status %v but the code answers it with %v", code, cell, got)
		}
	}
	if len(dynamicStatusCodes) > 0 && len(dynamic) == 0 {
		t.Fatalf("dynamicStatusCodes is declared but no code path resolves to a dynamic status; the scan is broken, not the code")
	}
}

func sameStatusSet(want, got []int) bool {
	if len(want) != len(got) {
		return false
	}
	sortedWant := append([]int(nil), want...)
	sortedGot := append([]int(nil), got...)
	sort.Ints(sortedWant)
	sort.Ints(sortedGot)
	for i := range sortedWant {
		if sortedWant[i] != sortedGot[i] {
			return false
		}
	}
	return true
}

// httpStatusNumbers maps the net/http status constants the proxy passes to its envelope builders to
// their numbers, because the documentation spells numbers. An unknown constant fails the scan
// instead of being guessed.
var httpStatusNumbers = map[string]int{
	"StatusBadRequest":            http.StatusBadRequest,
	"StatusUnauthorized":          http.StatusUnauthorized,
	"StatusRequestEntityTooLarge": http.StatusRequestEntityTooLarge,
	"StatusNotFound":              http.StatusNotFound,
	"StatusTooManyRequests":       http.StatusTooManyRequests,
	"StatusInternalServerError":   http.StatusInternalServerError,
	"StatusBadGateway":            http.StatusBadGateway,
	"StatusServiceUnavailable":    http.StatusServiceUnavailable,
}

// dynamicStatusCodes names the error codes whose status is not a fixed set the documentation can
// spell out, with the reason. Every documented code whose status cell is not numeric must appear
// here.
var dynamicStatusCodes = map[string]string{
	// chaos_injected: syntheticChaosResponse answers with chaosStatusCode(res.StatusCode), which
	// passes the chaos rule's status_code through and only normalises values below 100 or above
	// 999 to 500.
	"chaos_injected": "the status comes from the chaos rule's status_code",
}

// emittedProxyErrorStatuses returns the static status of every error code a client can see, plus
// the set of codes whose status is not a static net/http constant.
func emittedProxyErrorStatuses(t *testing.T) (map[string][]int, map[string]bool, bool) {
	t.Helper()

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.) error = %v", err)
	}
	type parsedFile struct {
		name string
		file *ast.File
	}
	var files []parsedFile
	funcs := map[string]*ast.FuncDecl{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s) error = %v", name, err)
		}
		files = append(files, parsedFile{name: name, file: file})
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs[fn.Name.Name] = fn
			}
		}
	}

	static := map[string][]int{}
	dynamic := map[string]bool{}
	complete := true
	addStatic := func(code string, value int) {
		for _, existing := range static[code] {
			if existing == value {
				return
			}
		}
		static[code] = append(static[code], value)
	}
	statusOf := func(expr ast.Expr) (int, bool) {
		selector, ok := expr.(*ast.SelectorExpr)
		if !ok {
			return 0, false
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "http" {
			return 0, false
		}
		value, ok := httpStatusNumbers[selector.Sel.Name]
		if !ok {
			t.Errorf("http.%s appears as an error status but is not in this gate's status table; add it so the documented status can be verified", selector.Sel.Name)
			complete = false
			return 0, false
		}
		return value, true
	}

	for _, item := range files {
		for _, decl := range item.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// One level of local resolution, for both the status and the code argument.
			localStatus := map[string]resolvedStatus{}
			localCodes := map[string][]string{}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				assign, ok := node.(*ast.AssignStmt)
				if !ok {
					return true
				}
				// callAt returns the call feeding the i-th name on the left, plus which of
				// that call's results the name receives: `statusCode, eventType := f()` pairs
				// left-hand name i with result i, while `x := f()` takes result 0.
				callAt := func(i int) (*ast.CallExpr, int, bool) {
					switch {
					case len(assign.Rhs) == len(assign.Lhs):
						call, ok := assign.Rhs[i].(*ast.CallExpr)
						return call, 0, ok
					case len(assign.Rhs) == 1:
						call, ok := assign.Rhs[0].(*ast.CallExpr)
						return call, i, ok
					}
					return nil, 0, false
				}
				for i, lhs := range assign.Lhs {
					ident, ok := lhs.(*ast.Ident)
					if !ok {
						continue
					}
					call, resultIndex, ok := callAt(i)
					if !ok {
						continue
					}
					if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "SelectionFailureReason" {
						localCodes[ident.Name] = selectionFailureCodeList(t)
						continue
					}
					callee := ""
					switch fun := call.Fun.(type) {
					case *ast.Ident:
						callee = fun.Name
					case *ast.SelectorExpr:
						callee = fun.Sel.Name
					}
					producer, ok := funcs[callee]
					if !ok {
						continue
					}
					// A producer is static only when every status it returns is a net/http
					// constant; one pass-through return (chaosStatusCode echoes the rule's
					// status) makes the local dynamic, because the caller's status is then not a
					// fixed set the documentation could spell out.
					resolved := resolvedStatus{}
					ast.Inspect(producer.Body, func(inner ast.Node) bool {
						ret, ok := inner.(*ast.ReturnStmt)
						if !ok {
							return true
						}
						// Only the result this name receives is a status; the sibling result
						// (`limitRejectionHTTP` also returns an event type) is not.
						if resultIndex >= len(ret.Results) {
							return true
						}
						if value, ok := statusOf(ret.Results[resultIndex]); ok {
							resolved.values = append(resolved.values, value)
						} else {
							resolved.dynamic = true
						}
						return true
					})
					localStatus[ident.Name] = resolved
				}
				return true
			})

			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				var codeArg, statusArg ast.Expr
				switch ident.Name {
				case "writeProxyError":
					if len(call.Args) != 5 {
						return true
					}
					statusArg, codeArg = call.Args[2], call.Args[3]
				case "proxyErrorEnvelope":
					if len(call.Args) != 4 || item.name == "errors.go" {
						return true
					}
					statusArg, codeArg = call.Args[1], call.Args[2]
				default:
					return true
				}

				var codes []string
				switch value := codeArg.(type) {
				case *ast.BasicLit:
					if value.Kind == token.STRING {
						if code, err := strconv.Unquote(value.Value); err == nil {
							codes = []string{code}
						}
					}
				case *ast.Ident:
					codes = localCodes[value.Name]
				case *ast.CallExpr:
					if selector, ok := value.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "SelectionFailureReason" {
						codes = selectionFailureCodeList(t)
					}
				}
				if len(codes) == 0 {
					return true // the code column owns unreadable code arguments
				}

				if value, ok := statusOf(statusArg); ok {
					for _, code := range codes {
						addStatic(code, value)
					}
					return true
				}
				if name, ok := statusArg.(*ast.Ident); ok {
					if resolved, ok := localStatus[name.Name]; ok && (len(resolved.values) > 0 || resolved.dynamic) {
						for _, code := range codes {
							if resolved.dynamic {
								dynamic[code] = true
								continue
							}
							for _, value := range resolved.values {
								addStatic(code, value)
							}
						}
						return true
					}
				}
				for _, code := range codes {
					if len(static[code]) == 0 {
						dynamic[code] = true
					}
				}
				return true
			})
		}
	}
	return static, dynamic, complete
}

// resolvedStatus is what a local status variable was resolved to: the net/http constants its
// producer returns, or a flag saying the producer can echo a value that is not one.
type resolvedStatus struct {
	values  []int
	dynamic bool
}

// selectionFailureCodeList returns the router selection failure reasons in a stable order.
func selectionFailureCodeList(t *testing.T) []string {
	t.Helper()
	out := make([]string, 0, len(routerSelectionFailureCodes(t)))
	for code := range routerSelectionFailureCodes(t) {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// documentedClientErrorStatuses parses the status column of the table: numeric cells become the
// list of statuses, and cells that spell out no number become an empty list.
func documentedClientErrorStatuses(t *testing.T) map[string][]int {
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

	numeric := regexp.MustCompile("^\\| `([a-z0-9_]+)` \\| ([0-9]+(?: / [0-9]+)*) \\|")
	anyRow := regexp.MustCompile("^\\| `([a-z0-9_]+)` \\| ([^|]+) \\|")
	statuses := map[string][]int{}
	for _, line := range lines[start+1 : end] {
		match := numeric.FindStringSubmatch(line)
		if match == nil {
			if other := anyRow.FindStringSubmatch(line); other != nil {
				statuses[other[1]] = nil
			}
			continue
		}
		var values []int
		for _, field := range strings.Split(match[2], "/") {
			value, err := strconv.Atoi(strings.TrimSpace(field))
			if err != nil {
				t.Fatalf("cannot read the documented status %q of %q: %v", match[2], match[1], err)
			}
			values = append(values, value)
		}
		statuses[match[1]] = values
	}
	if len(statuses) == 0 {
		t.Fatalf("no statuses found in the client error code table in %s", clientErrorTableDoc)
	}
	return statuses
}
