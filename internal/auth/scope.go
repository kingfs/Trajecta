package auth

import (
	"context"
	"fmt"
	"strings"
)

// Personal tokens carry a scope so one installation can hand out credentials
// that are useful for exactly one plane.
//
// ScopeAll keeps the historical behaviour: the token works on the proxy, the
// MCP endpoint and anything else that accepts a personal token. ScopeProxy is
// what an SDK needs (the forwarding surface only) and ScopeMCP is what an MCP
// client needs (the read-mostly management surface only). A proxy-scoped token
// therefore cannot read recorded traffic through MCP even though both surfaces
// share the same auth store.
const (
	ScopeAll   = "all"
	ScopeProxy = "proxy"
	ScopeMCP   = "mcp"
)

// NormalizeTokenScope canonicalizes a scope supplied when creating a token.
// An empty scope keeps the historical default, ScopeAll.
func NormalizeTokenScope(scope string) string {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		return ScopeAll
	}
	return scope
}

// ValidateTokenScope prevents newly issued tokens from silently receiving the
// historical full-access behavior due to a typo or an unsupported scope. Old
// rows with custom scopes remain readable and are still interpreted by
// ScopeAllows for compatibility.
func ValidateTokenScope(scope string) (string, error) {
	normalized := NormalizeTokenScope(scope)
	switch normalized {
	case "api":
		// Older Monitor versions created this value for full-access tokens.
		// Preserve that intentional alias while rejecting unknown values that
		// would otherwise silently acquire full access.
		return ScopeAll, nil
	case ScopeAll, ScopeProxy, ScopeMCP:
		return normalized, nil
	default:
		return "", fmt.Errorf("unsupported token scope %q; use all, proxy or mcp", normalized)
	}
}

// ScopeAllows reports whether a token granted `granted` may act on a surface
// that requires `required`.
//
// Empty, ScopeAll and any custom scope keep full access. The last part is
// deliberate: before the scope split every value was informational, so an
// installation can already hold tokens created with values such as "api". They
// must keep working. Only the two explicit plane scopes restrict a token, and
// those are what an operator gets from `auth create-token --scope proxy|mcp`
// and from the Monitor's token dialog.
func ScopeAllows(granted, required string) bool {
	granted = NormalizeTokenScope(granted)
	required = strings.ToLower(strings.TrimSpace(required))
	if granted == ScopeAll {
		return true
	}
	switch granted {
	case ScopeProxy, ScopeMCP:
		return required == "" || required == ScopeAll || granted == required
	default:
		// Legacy/custom scope: the value never had an enforcement meaning.
		return true
	}
}

// ScopedVerifier narrows a TokenVerifier to the tokens valid for one plane. A
// nil inner verifier stays nil so the "no auth store configured" test/embedded
// mode is unchanged.
func ScopedVerifier(inner TokenVerifier, required string) TokenVerifier {
	if inner == nil {
		return nil
	}
	required = strings.ToLower(strings.TrimSpace(required))
	if required == "" || required == ScopeAll {
		return inner
	}
	return scopedVerifier{inner: inner, required: required}
}

type scopedVerifier struct {
	inner    TokenVerifier
	required string
}

func (v scopedVerifier) VerifyToken(ctx context.Context, token string) (Principal, bool, error) {
	principal, ok, err := v.inner.VerifyToken(ctx, token)
	if err != nil || !ok {
		return Principal{}, false, err
	}
	if !ScopeAllows(principal.Scope, v.required) {
		return Principal{}, false, nil
	}
	return principal, true, nil
}
