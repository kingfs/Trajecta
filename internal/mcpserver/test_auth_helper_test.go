package mcpserver

import (
	"context"

	"github.com/kingfs/Trajecta/internal/auth"
)

// mcpTestAdminContext is the in-process identity the MCP tools require. The
// real server receives it from the management middleware; tests call the tool
// handlers directly, so they provide it explicitly.
func mcpTestAdminContext() context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{
		UserID:   1,
		Username: "admin-test",
		Role:     auth.RoleAdmin,
		Scope:    auth.ScopeAll,
	})
}
