package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// SessionState is the per-user state a stateless JWT cannot carry.
//
// A monitor session is an HMAC-signed JWT, so on its own it lives until `exp`.
// That made disabling a user, deleting a user or resetting a password
// ineffective for up to auth.session_ttl (24h by default): the only way to end
// a session was to wait, or to rotate the installation-wide signing key and log
// every operator out. SessionVerifier closes that gap by re-reading these three
// facts once per request and binding the token to the password it was issued
// against.
type SessionState struct {
	Enabled             bool
	Role                string
	PasswordFingerprint string
}

// SessionStateReader is the subset of Store the verifier needs.
type SessionStateReader interface {
	SessionState(ctx context.Context, userID int) (SessionState, bool, error)
}

// passwordFingerprint is a one-way binding between a JWT and the password hash
// that was current when it was issued. It is not a password hash and it is
// never sent to a client: it only travels inside the signed JWT and inside this
// process.
func passwordFingerprint(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:])
}

// SessionVerifier verifies a monitor JWT and then confirms against the auth
// store that the user still exists, is enabled, still has the role the token
// surface should see, and has not changed password since the token was issued.
//
// It is fail-closed: a store error, a deleted user, a disabled user, an empty
// fingerprint (a token issued before this check existed) or a fingerprint
// mismatch all reject the request. The extra lookup is one primary-key read per
// Monitor API request, on the auth store's own pool.
type SessionVerifier struct {
	jwt   *JWTManager
	store SessionStateReader
}

// NewSessionVerifier composes a JWT manager with the auth store. A nil store
// returns nil so a caller cannot accidentally build an open verifier.
func NewSessionVerifier(jwt *JWTManager, store SessionStateReader) TokenVerifier {
	if jwt == nil || store == nil {
		return nil
	}
	return &SessionVerifier{jwt: jwt, store: store}
}

func (v *SessionVerifier) VerifyToken(ctx context.Context, token string) (Principal, bool, error) {
	principal, ok, err := v.jwt.VerifyToken(ctx, token)
	if err != nil || !ok {
		return Principal{}, false, err
	}
	state, found, err := v.store.SessionState(ctx, principal.UserID)
	if err != nil {
		return Principal{}, false, err
	}
	if !found || !state.Enabled {
		return Principal{}, false, nil
	}
	if principal.SessionFingerprint == "" || state.PasswordFingerprint == "" {
		return Principal{}, false, nil
	}
	if subtle.ConstantTimeCompare([]byte(principal.SessionFingerprint), []byte(state.PasswordFingerprint)) != 1 {
		return Principal{}, false, nil
	}
	// Role comes from the store, not from the token, so a promotion or demotion
	// takes effect on the next request instead of at the next login.
	principal.Role = state.Role
	principal.SessionFingerprint = ""
	return principal, true, nil
}
