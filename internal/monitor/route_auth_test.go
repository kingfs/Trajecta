package monitor

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/store"
)

// publicMonitorAPIRoutes are the only /api routes that may answer without a session, with the
// reason each one has to stay reachable. Everything else under /api must reject an
// unauthenticated request, because the monitor is where provider credentials and recorded
// traffic are readable.
var publicMonitorAPIRoutes = map[string]string{
	"/api/auth/status": "the UI asks whether a login is required before it has a session",
	"/api/auth/login":  "the login form itself cannot require a login",
}

// rejectingTokenVerifier stands in for a configured auth store: every token is rejected, so
// any route that answers anything other than 401 is answering an unauthenticated caller.
type rejectingTokenVerifier struct{}

func (rejectingTokenVerifier) VerifyToken(context.Context, string) (auth.Principal, bool, error) {
	return auth.Principal{}, false, nil
}

// TestEveryMonitorAPIRouteRequiresAuthentication makes the auth coverage of the management API
// a checked invariant instead of a property of how each route happens to be registered.
//
// RegisterRoutes wraps every route in monitorAuthRequired individually, so a new route that
// forgets the wrapper would silently serve data to an unauthenticated caller - and because
// monitorAuthRequired returns the handler unchanged when no verifier is configured, the same
// route behaves as public in a deployment without an auth store. The test reads the patterns
// out of the registration function itself (so a route cannot be missed by not being listed)
// and then asks the real mux for each one with a verifier that rejects every token.
//
// A registration whose pattern is not a string literal fails the test rather than being
// skipped: a dynamic pattern could otherwise hide a route from this check entirely.
func TestEveryMonitorAPIRouteRequiresAuthentication(t *testing.T) {
	t.Parallel()

	patterns := monitorAPIRoutePatternsFromSource(t)
	// A floor rather than the exact count: its job is to catch a parse that silently found
	// almost nothing (the parse is what lets a new route be covered without editing a list),
	// while an added route must not break the gate.
	if len(patterns) < 40 {
		t.Fatalf("found %d /api patterns, want the whole registration list", len(patterns))
	}

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{
		AuthVerifier:        rejectingTokenVerifier{},
		MonitorAuthVerifier: rejectingTokenVerifier{},
	})

	for _, pattern := range patterns {
		if reason, public := publicMonitorAPIRoutes[pattern]; public {
			_ = reason
			continue
		}
		t.Run(strings.TrimPrefix(pattern, "/api/"), func(t *testing.T) {
			path := pattern
			if strings.HasSuffix(path, "/") {
				// A subtree pattern does not match its own prefix without a child path.
				path += "unauthenticated-probe"
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s without credentials = %d, want %d; body=%s",
					path, rr.Code, http.StatusUnauthorized, rr.Body.String())
			}
		})
	}
}

// TestPublicMonitorAPIRoutesAreExactlyTheLoginSurface keeps the allowlist above honest: the
// public routes must still answer without a session, so the gate cannot be satisfied by
// wrapping login in auth and locking every operator out.
func TestPublicMonitorAPIRoutesAreExactlyTheLoginSurface(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, RouteOptions{
		AuthVerifier:        rejectingTokenVerifier{},
		MonitorAuthVerifier: rejectingTokenVerifier{},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"auth_required":true`) {
		t.Fatalf("GET /api/auth/status body = %s, want auth_required true", rr.Body.String())
	}
}

// monitorAPIRoutePatternsFromSource returns every /api pattern registered in RegisterRoutes.
func monitorAPIRoutePatternsFromSource(t *testing.T) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parser.ParseFile(server.go) error = %v", err)
	}
	var registered *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name != nil && fn.Name.Name == "RegisterRoutes" {
			registered = fn
			break
		}
	}
	if registered == nil {
		t.Fatal("RegisterRoutes was not found in server.go; the auth gate cannot enumerate routes")
	}

	var patterns []string
	seen := map[string]struct{}{}
	ast.Inspect(registered, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (selector.Sel.Name != "HandleFunc" && selector.Sel.Name != "Handle") {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			t.Errorf("a %s registration in RegisterRoutes does not use a string literal pattern; the auth gate cannot verify it", selector.Sel.Name)
			return true
		}
		pattern, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Errorf("unquote %s: %v", literal.Value, err)
			return true
		}
		if !strings.HasPrefix(pattern, "/api/") {
			return true
		}
		if _, ok := seen[pattern]; ok {
			return true
		}
		seen[pattern] = struct{}{}
		patterns = append(patterns, pattern)
		return true
	})
	return patterns
}
