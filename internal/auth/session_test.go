package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newSessionTestStore(t *testing.T) (*Store, *JWTManager) {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "control.sqlite3")
	if err := MigrateUp(dbPath, 0); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.CreateUser(ctx, "admin", "change-me-123"); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	manager, err := NewJWTManager(JWTOptions{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
		TTL:    time.Hour,
	})
	if err != nil {
		t.Fatalf("NewJWTManager() error = %v", err)
	}
	return st, manager
}

func issueSessionToken(t *testing.T, st *Store, manager *JWTManager) TokenResult {
	t.Helper()

	principal, err := st.AuthenticatePassword(context.Background(), "admin", "change-me-123")
	if err != nil {
		t.Fatalf("AuthenticatePassword() error = %v", err)
	}
	token, err := manager.IssueToken(principal)
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	return token
}

func TestSessionVerifierTracksUserState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st, manager := newSessionTestStore(t)
	verifier := NewSessionVerifier(manager, st)
	token := issueSessionToken(t, st, manager)

	principal, ok, err := verifier.VerifyToken(ctx, token.Token)
	if err != nil || !ok {
		t.Fatalf("VerifyToken(fresh) = ok %v err %v, want true nil", ok, err)
	}
	if principal.Role != RoleAdmin {
		t.Fatalf("role = %q, want %q", principal.Role, RoleAdmin)
	}
	if principal.SessionFingerprint != "" {
		t.Fatal("SessionFingerprint leaked out of the verifier")
	}

	// A role change applies to the live session, not at the next login.
	if err := st.SetUserRole(ctx, "admin", RoleViewer); err != nil {
		t.Fatalf("SetUserRole() error = %v", err)
	}
	principal, ok, err = verifier.VerifyToken(ctx, token.Token)
	if err != nil || !ok || principal.Role != RoleViewer {
		t.Fatalf("VerifyToken(after demotion) = %+v ok %v err %v, want viewer true nil", principal, ok, err)
	}

	// Disabling and re-enabling a user is immediate.
	if err := st.SetUserEnabled(ctx, "admin", false); err != nil {
		t.Fatalf("SetUserEnabled(false) error = %v", err)
	}
	if _, ok, _ := verifier.VerifyToken(ctx, token.Token); ok {
		t.Fatal("VerifyToken(disabled user) = true, want false")
	}
	if err := st.SetUserEnabled(ctx, "admin", true); err != nil {
		t.Fatalf("SetUserEnabled(true) error = %v", err)
	}
	if _, ok, _ := verifier.VerifyToken(ctx, token.Token); !ok {
		t.Fatal("VerifyToken(re-enabled user) = false, want true")
	}
}

func TestSessionVerifierRejectsAfterPasswordReset(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st, manager := newSessionTestStore(t)
	verifier := NewSessionVerifier(manager, st)
	oldToken := issueSessionToken(t, st, manager)

	if _, ok, _ := verifier.VerifyToken(ctx, oldToken.Token); !ok {
		t.Fatal("VerifyToken(before reset) = false, want true")
	}
	if err := st.ResetPassword(ctx, "admin", "changed-password-123"); err != nil {
		t.Fatalf("ResetPassword() error = %v", err)
	}
	if _, ok, _ := verifier.VerifyToken(ctx, oldToken.Token); ok {
		t.Fatal("VerifyToken(after reset) = true, want false")
	}

	principal, err := st.AuthenticatePassword(ctx, "admin", "changed-password-123")
	if err != nil {
		t.Fatalf("AuthenticatePassword(new) error = %v", err)
	}
	fresh, err := manager.IssueToken(principal)
	if err != nil {
		t.Fatalf("IssueToken(new) error = %v", err)
	}
	if _, ok, _ := verifier.VerifyToken(ctx, fresh.Token); !ok {
		t.Fatal("VerifyToken(fresh after reset) = false, want true")
	}
}

func TestSessionVerifierRejectsLegacyAndDeletedSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st, manager := newSessionTestStore(t)
	verifier := NewSessionVerifier(manager, st)

	// A token issued before the fingerprint claim existed has no binding and
	// must be rejected rather than trusted for its remaining TTL.
	legacy, err := manager.IssueToken(Principal{UserID: 1, Username: "admin", Role: RoleAdmin, Scope: ScopeAll})
	if err != nil {
		t.Fatalf("IssueToken(legacy) error = %v", err)
	}
	if _, ok, _ := verifier.VerifyToken(ctx, legacy.Token); ok {
		t.Fatal("VerifyToken(legacy token) = true, want false")
	}

	token := issueSessionToken(t, st, manager)
	if err := st.DeleteUser(ctx, "admin"); err != nil {
		t.Fatalf("DeleteUser() error = %v", err)
	}
	if _, ok, _ := verifier.VerifyToken(ctx, token.Token); ok {
		t.Fatal("VerifyToken(deleted user) = true, want false")
	}
}

func TestNormalizeRole(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "", want: RoleAdmin, ok: true},
		{in: "Admin", want: RoleAdmin, ok: true},
		{in: "viewer", want: RoleViewer, ok: true},
		{in: "root", ok: false},
	} {
		got, err := normalizeRole(tc.in)
		if (err == nil) != tc.ok {
			t.Fatalf("normalizeRole(%q) err = %v, want ok %v", tc.in, err, tc.ok)
		}
		if tc.ok && got != tc.want {
			t.Fatalf("normalizeRole(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
