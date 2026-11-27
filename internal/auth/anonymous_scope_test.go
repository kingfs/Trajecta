package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnonymousFixtureWorksThroughScopedVerifier(t *testing.T) {
	for _, scope := range []string{ScopeProxy, ScopeMCP} {
		t.Run(scope, func(t *testing.T) {
			verifier := ScopedVerifier(AllowAnonymousForTest(), scope)
			principal, ok := VerifyRequest(httptest.NewRequest(http.MethodGet, "/", nil), verifier)
			if !ok || principal.Role != RoleAdmin {
				t.Fatalf("principal = %+v, ok = %v", principal, ok)
			}
		})
	}
}
