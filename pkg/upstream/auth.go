package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AuthMode is the authentication mechanism used to reach an upstream.
type AuthMode string

const (
	// AuthModeAPIKey authenticates with a static API key or plan key.
	AuthModeAPIKey AuthMode = "api_key"
	// AuthModeOAuth authenticates with an OAuth access token. Only token
	// application is implemented; the OAuth flow itself is reserved.
	AuthModeOAuth AuthMode = "oauth"
	// AuthModeCLICredential authenticates with a credential read from an
	// already-logged-in CLI. Reading that credential happens outside this
	// package; only token application is implemented here.
	AuthModeCLICredential AuthMode = "cli_credential"
	// AuthModeNone sends no authentication header.
	AuthModeNone AuthMode = "none"
)

// Valid reports whether m is a known auth mode.
func (m AuthMode) Valid() bool {
	switch m {
	case AuthModeAPIKey, AuthModeOAuth, AuthModeCLICredential, AuthModeNone:
		return true
	}
	return false
}

// HeaderTemplate is one additional header derived from the credential or the
// resolved upstream. Values may reference {{api_key}}, {{access_token}},
// {{account_id}}, {{plan}}, {{api_version}} and {{preset}}; unknown
// placeholders are left untouched.
type HeaderTemplate struct {
	// Name is the header name. An empty name skips the template.
	Name string
	// Value is the header value template.
	Value string
	// IfAbsent sets the header only when the request does not carry it yet.
	IfAbsent bool
}

// AuthSpec describes how a preset or a resolved upstream authenticates.
type AuthSpec struct {
	// Mode selects the credential kind.
	Mode AuthMode
	// Header receives the credential. Empty means Authorization.
	Header string
	// Scheme prefixes the credential, for example "Bearer". Empty means the raw
	// credential value, except that an Authorization header defaults to Bearer.
	Scheme string
	// OAuth describes a reserved interactive OAuth flow. The flow itself is not
	// implemented by this package.
	OAuth *OAuthSpec
	// Refresh describes a reserved token refresh endpoint.
	Refresh *RefreshSpec
	// Headers are credential- or upstream-derived headers applied before the
	// static Preset.Headers.
	Headers []HeaderTemplate
}

// OAuthSpec is the reserved description of an OAuth authorization-code or
// device-code flow. The package stores it for configuration and forward
// compatibility but implements no network flow.
type OAuthSpec struct {
	AuthorizeURL  string
	TokenURL      string
	DeviceCodeURL string
	ClientID      string
	Scopes        []string
	RedirectURI   string
	PKCE          bool
	ExtraParams   map[string]string
}

// RefreshSpec is the reserved description of an OAuth token refresh request.
type RefreshSpec struct {
	TokenURL    string
	ClientID    string
	Scopes      []string
	ExtraParams map[string]string
}

// Credential is a resolved secret plus its provenance. It never reaches the
// catalog and is not persisted by this package.
type Credential struct {
	Mode         AuthMode
	APIKey       string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	AccountID    string
	Plan         string
	Scopes       []string
}

// IsZero reports whether c carries no credential data at all.
func (c Credential) IsZero() bool {
	return c.Mode == "" && c.APIKey == "" && c.AccessToken == "" && c.RefreshToken == "" &&
		c.ExpiresAt.IsZero() && c.AccountID == "" && c.Plan == "" && len(c.Scopes) == 0
}

// CredentialStatus is the coarse status an AuthDriver reports for a credential.
type CredentialStatus string

const (
	// CredentialStatusUnknown means the driver cannot classify the credential.
	CredentialStatusUnknown CredentialStatus = "unknown"
	// CredentialStatusValid means the credential is usable.
	CredentialStatusValid CredentialStatus = "valid"
	// CredentialStatusExpired means the credential is expired and needs a
	// refresh or re-authentication.
	CredentialStatusExpired CredentialStatus = "expired"
	// CredentialStatusRevoked means the provider rejected the credential.
	CredentialStatusRevoked CredentialStatus = "revoked"
)

// AuthRequest is the reserved input of an interactive authentication flow.
type AuthRequest struct {
	PresetID    string
	Scopes      []string
	RedirectURI string
	State       string
	ExtraParams map[string]string
}

// Flow is the reserved result of beginning an interactive auth flow. It carries
// either an authorization URL or a device/user code.
type Flow struct {
	URL             string
	DeviceCode      string
	UserCode        string
	VerificationURL string
	State           string
	CodeVerifier    string
	ExpiresIn       time.Duration
}

// AuthDriver is the reserved interface for OAuth and CLI-credential drivers.
// This package intentionally provides no implementation: an internal/credential
// package will register drivers later.
type AuthDriver interface {
	ID() string
	Begin(ctx context.Context, req AuthRequest) (Flow, error)
	Exchange(ctx context.Context, req AuthRequest, code string) (Credential, error)
	Refresh(ctx context.Context, cred Credential) (Credential, error)
	Apply(ctx context.Context, cred Credential, req *http.Request) error
	Status(cred Credential) CredentialStatus
}

// ApplyAuth sets the upstream authentication header and then the configured
// header templates and static headers on req.
//
// The credential's Mode wins over the resolved Auth.Mode. API-key, OAuth and
// CLI-credential modes all place their secret in the configured header; OAuth
// refresh and interactive flows are not performed here. AuthModeNone sends no
// credential. Competing standard auth headers are removed first so a client
// Authorization header never leaks to the upstream.
func (r Resolved) ApplyAuth(cred Credential, req *http.Request) error {
	if req == nil {
		return errors.New("upstream: apply auth: nil request")
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	mode := cred.Mode
	if mode == "" {
		mode = r.Auth.Mode
	}
	if mode == "" {
		mode = AuthModeNone
	}
	if !mode.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidAuthMode, mode)
	}
	if mode != AuthModeNone {
		secret, err := credentialSecret(mode, cred)
		if err != nil {
			return err
		}
		header, scheme := r.authHeader()
		clearAuthHeaders(req.Header)
		value := secret
		if scheme != "" {
			value = scheme + " " + secret
		}
		req.Header.Set(header, value)
	}
	r.applyConfiguredHeaders(cred, req)
	return nil
}

func credentialSecret(mode AuthMode, cred Credential) (string, error) {
	secret := ""
	switch mode {
	case AuthModeAPIKey:
		secret = cred.APIKey
	case AuthModeOAuth:
		secret = cred.AccessToken
	case AuthModeCLICredential:
		secret = cred.AccessToken
		if secret == "" {
			secret = cred.APIKey
		}
	default:
		return "", nil
	}
	if strings.TrimSpace(secret) == "" {
		return "", fmt.Errorf("%w: auth mode %q has no secret", ErrMissingCredential, mode)
	}
	return secret, nil
}

func (r Resolved) authHeader() (header string, scheme string) {
	header = strings.TrimSpace(r.Auth.Header)
	scheme = strings.TrimSpace(r.Auth.Scheme)
	if header == "" {
		header = "Authorization"
	}
	if strings.EqualFold(header, "Authorization") && scheme == "" {
		scheme = "Bearer"
	}
	return header, scheme
}

func clearAuthHeaders(header http.Header) {
	for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key"} {
		header.Del(name)
	}
}

// applyConfiguredHeaders applies AuthSpec.Headers and then the static
// Resolved.Headers. Static headers are last so a preset or caller can override a
// default header deliberately.
func (r Resolved) applyConfiguredHeaders(cred Credential, req *http.Request) {
	if req == nil {
		return
	}
	for _, tmpl := range r.Auth.Headers {
		name := strings.TrimSpace(tmpl.Name)
		if name == "" {
			continue
		}
		if tmpl.IfAbsent && req.Header.Get(name) != "" {
			continue
		}
		req.Header.Set(name, renderHeaderValue(tmpl.Value, cred, r))
	}
	applyStaticHeaders(req.Header, r.Headers)
}

func applyStaticHeaders(header http.Header, values map[string]string) {
	if header == nil {
		return
	}
	for name, value := range values {
		if strings.TrimSpace(name) == "" || value == "" {
			continue
		}
		header.Set(name, value)
	}
}

func renderHeaderValue(value string, cred Credential, resolved Resolved) string {
	replacer := strings.NewReplacer(
		"{{api_key}}", cred.APIKey,
		"{{access_token}}", cred.AccessToken,
		"{{account_id}}", cred.AccountID,
		"{{plan}}", cred.Plan,
		"{{api_version}}", resolved.APIVersion,
		"{{preset}}", resolved.Preset.ID,
	)
	return replacer.Replace(value)
}

func cloneAuthSpec(spec AuthSpec) AuthSpec {
	out := spec
	out.OAuth = cloneOAuthSpec(spec.OAuth)
	out.Refresh = cloneRefreshSpec(spec.Refresh)
	if spec.Headers != nil {
		out.Headers = append([]HeaderTemplate(nil), spec.Headers...)
	}
	return out
}

func cloneOAuthSpec(spec *OAuthSpec) *OAuthSpec {
	if spec == nil {
		return nil
	}
	out := *spec
	out.Scopes = append([]string(nil), spec.Scopes...)
	out.ExtraParams = cloneStringMap(spec.ExtraParams)
	return &out
}

func cloneRefreshSpec(spec *RefreshSpec) *RefreshSpec {
	if spec == nil {
		return nil
	}
	out := *spec
	out.Scopes = append([]string(nil), spec.Scopes...)
	out.ExtraParams = cloneStringMap(spec.ExtraParams)
	return &out
}
