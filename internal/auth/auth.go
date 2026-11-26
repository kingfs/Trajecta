package auth

import (
	"context"
	"net/http"
	"strings"
)

type principalContextKey struct{}

type TokenVerifier interface {
	VerifyToken(ctx context.Context, token string) (Principal, bool, error)
}

type Principal struct {
	UserID   int
	Username string
	Role     string
	Scope    string
	// SessionFingerprint binds a monitor JWT to the password hash that was
	// current when it was issued. It is carried only inside the signed token and
	// is cleared by SessionVerifier before the principal reaches a handler.
	SessionFingerprint string
}

func BearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 {
		return "", false
	}
	if !strings.EqualFold(parts[0], "bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	return token, token != ""
}

// anonymousTestVerifier is an explicit escape hatch for isolated test fixtures.
// Production code must use a real verifier; a nil verifier always fails closed.
type anonymousTestVerifier struct{}

func (anonymousTestVerifier) VerifyToken(context.Context, string) (Principal, bool, error) {
	return Principal{UserID: 1, Username: "anonymous-test", Role: RoleAdmin, Scope: ScopeAll}, true, nil
}

// AllowAnonymousForTest opts an isolated test server into anonymous access.
// Keep this call site explicit and grep-auditable; never use it in production.
func AllowAnonymousForTest() TokenVerifier { return anonymousTestVerifier{} }

// IsAnonymousForTest reports whether verifier is the explicit test-only
// anonymous sentinel. It is used only to make the auth status endpoint report
// that a test fixture intentionally runs without authentication.
func IsAnonymousForTest(verifier TokenVerifier) bool {
	_, ok := verifier.(anonymousTestVerifier)
	return ok
}

func VerifyRequest(r *http.Request, verifier TokenVerifier) (Principal, bool) {
	if verifier == nil {
		return Principal{}, false
	}
	if IsAnonymousForTest(verifier) {
		return Principal{UserID: 1, Username: "anonymous-test", Role: RoleAdmin, Scope: ScopeAll}, true
	}
	provided, ok := BearerToken(r.Header.Get("Authorization"))
	if !ok {
		return Principal{}, false
	}
	principal, verified, err := verifier.VerifyToken(r.Context(), provided)
	return principal, err == nil && verified
}

func RequestAuthorized(r *http.Request, verifier TokenVerifier) bool {
	_, ok := VerifyRequest(r, verifier)
	return ok
}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func Middleware(next http.Handler, realm string, verifier TokenVerifier) http.Handler {
	if verifier == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "authentication is not configured", http.StatusServiceUnavailable)
		})
	}
	if strings.TrimSpace(realm) == "" {
		realm = "trajecta"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := VerifyRequest(r, verifier)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}
