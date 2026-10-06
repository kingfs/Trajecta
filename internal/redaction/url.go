package redaction

import (
	"net/url"
	"regexp"
	"strings"
)

const redactedValue = "REDACTED"

// sensitiveURLParamMarkers is the single marker list for URL and DSN
// redaction. It is the union of what the recorder/proxy path and the CLI
// `config inspect` path each used to carry privately, because the two copies
// had drifted in both directions: the CLI copy was missing credential,
// signature, sig, access_token and api_key, while this one was missing
// authorization and auth. A match is deliberately broad: over-redacting a
// diagnostic value is harmless, while a missing marker prints the secret.
var sensitiveURLParamMarkers = []string{
	"key",
	"token",
	"secret",
	"password",
	"passwd",
	"credential",
	"signature",
	"sig",
	"access_token",
	"api_key",
	"authorization",
	"auth",
}

var metadataSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|oauth[_-]?token|client[_-]?secret|authorization|x[_-]?api[_-]?key|x[_-]?auth[_-]?token)(\s*[:=]\s*)("[^"]+"|'[^']+'|[^\s,;{}]+)`),
	regexp.MustCompile(`(?is)\{[^{}]*"(type)"\s*:\s*"service_account"[^{}]*\}`),
}

// DisplayURL returns a diagnostics-safe URL string without destroying malformed
// input that may still be useful while debugging configuration.
func DisplayURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.Scheme == "" && parsed.Host == "" && !strings.HasPrefix(raw, "/") {
		return raw
	}
	if parsed.User != nil && parsed.Host != "" {
		// The userinfo is a credential position: some gateways are configured
		// with the API key as the URL username (`https://<key>@host/v1`, or a
		// `user` that is really a token). Keep an ordinary username, because it
		// helps identify the upstream, but never keep one that names a secret.
		// This output is written into cassette meta and the logs table, so a
		// userinfo secret would be recorded in plain text.
		username := parsed.User.Username()
		if username == "" || IsSensitiveURLParam(username) {
			username = redactedValue
		}
		parsed.User = url.UserPassword(username, redactedValue)
	}
	if parsed.RawQuery != "" {
		query := parsed.Query()
		for key := range query {
			if IsSensitiveURLParam(key) {
				query.Set(key, redactedValue)
			}
		}
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func MetadataText(raw string) string {
	if raw == "" {
		return ""
	}
	out := raw
	for _, pattern := range metadataSecretPatterns {
		out = pattern.ReplaceAllStringFunc(out, redactMetadataSecretMatch)
	}
	return out
}

func SafeCredentialHint(raw string) string {
	hint := strings.TrimSpace(MetadataText(raw))
	if hint == "" || strings.EqualFold(hint, redactedValue) {
		return ""
	}
	if len(hint) > 32 {
		return hint[:32]
	}
	return hint
}

func redactMetadataSecretMatch(match string) string {
	lower := strings.ToLower(match)
	if strings.HasPrefix(lower, "bearer ") {
		return "Bearer " + redactedValue
	}
	if strings.Contains(lower, `"type"`) && strings.Contains(lower, `"service_account"`) {
		return redactedValue
	}
	for _, sep := range []string{":", "="} {
		if idx := strings.Index(match, sep); idx >= 0 {
			return match[:idx+1] + redactedValue
		}
	}
	return redactedValue
}

// IsSensitiveURLParam reports whether a query parameter name carries a secret.
// It is exported so that every place which prints a URL or a DSN shares one
// marker list: a second copy drifts, and the copy that omits a marker leaks the
// value it was supposed to hide.
func IsSensitiveURLParam(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return false
	}
	for _, marker := range sensitiveURLParamMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
