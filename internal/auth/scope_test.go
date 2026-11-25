package auth

import (
	"context"
	"testing"
)

func TestValidateTokenScopeRejectsNewCustomScopes(t *testing.T) {
	t.Parallel()

	for _, scope := range []string{"", " ALL ", "Proxy", "mcp"} {
		if _, err := ValidateTokenScope(scope); err != nil {
			t.Errorf("ValidateTokenScope(%q) error = %v, want accepted", scope, err)
		}
	}
	if got, err := ValidateTokenScope("api"); err != nil || got != ScopeAll {
		t.Errorf("ValidateTokenScope(api) = %q, %v; want all, nil", got, err)
	}
	for _, scope := range []string{"typo", "proxy,mcp"} {
		if _, err := ValidateTokenScope(scope); err == nil {
			t.Errorf("ValidateTokenScope(%q) error = nil, want rejection", scope)
		}
	}
}

func TestScopeAllows(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		granted  string
		required string
		want     bool
	}{
		{granted: "", required: ScopeProxy, want: true},
		{granted: ScopeAll, required: ScopeProxy, want: true},
		{granted: ScopeProxy, required: ScopeProxy, want: true},
		{granted: ScopeProxy, required: ScopeMCP, want: false},
		{granted: ScopeMCP, required: ScopeMCP, want: true},
		{granted: ScopeMCP, required: ScopeProxy, want: false},
		// A custom value was informational before the scope split; keeping it
		// permissive is what lets an existing token keep working.
		{granted: "api", required: ScopeMCP, want: true},
	} {
		if got := ScopeAllows(tc.granted, tc.required); got != tc.want {
			t.Fatalf("ScopeAllows(%q, %q) = %v, want %v", tc.granted, tc.required, got, tc.want)
		}
	}
}

func TestScopedVerifierKeepsPlanesApart(t *testing.T) {
	t.Parallel()

	proxyPrincipal := verifierFunc(func(context.Context, string) (Principal, bool, error) {
		return Principal{Username: "sdk", Scope: ScopeProxy}, true, nil
	})
	if _, ok, err := ScopedVerifier(proxyPrincipal, ScopeProxy).VerifyToken(context.Background(), "t"); err != nil || !ok {
		t.Fatalf("proxy-scoped token on proxy plane = ok %v err %v, want true nil", ok, err)
	}
	if _, ok, err := ScopedVerifier(proxyPrincipal, ScopeMCP).VerifyToken(context.Background(), "t"); err != nil || ok {
		t.Fatalf("proxy-scoped token on MCP plane = ok %v err %v, want false nil", ok, err)
	}
	if ScopedVerifier(nil, ScopeProxy) != nil {
		t.Fatal("ScopedVerifier(nil) must stay nil")
	}
}
