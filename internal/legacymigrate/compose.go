package legacymigrate

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// ComposeScan reports which environment-variable prefix a docker-compose file
// (or any deployment manifest) still references.
type ComposeScan struct {
	Path        string   `json:"path"`
	LegacyKeys  []string `json:"legacy_keys,omitempty"`
	CurrentKeys []string `json:"current_keys,omitempty"`
	ReadError   string   `json:"read_error,omitempty"`
}

// UsesLegacyPrefix reports whether the file still passes pre-rename variables
// to the container.
func (c *ComposeScan) UsesLegacyPrefix() bool {
	return c != nil && len(c.LegacyKeys) > 0
}

var envReferencePattern = regexp.MustCompile(`\b(?:LLM_TRACELAB|TRAJECTA)_[A-Z0-9_]+`)

// ScanComposeEnvPrefix inspects a deployment manifest for environment-variable
// references. The current binaries only read TRAJECTA_*, so a compose file that
// still passes LLM_TRACELAB_* silently sends nothing to the application.
func ScanComposeEnvPrefix(path string) *ComposeScan {
	scan := &ComposeScan{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		scan.ReadError = err.Error()
		return scan
	}
	seenLegacy := map[string]bool{}
	seenCurrent := map[string]bool{}
	for _, match := range envReferencePattern.FindAllString(string(data), -1) {
		if len(match) >= len(LegacyEnvPrefix) && match[:len(LegacyEnvPrefix)] == LegacyEnvPrefix {
			seenLegacy[match] = true
			continue
		}
		seenCurrent[match] = true
	}
	scan.LegacyKeys = sortedKeys(seenLegacy)
	scan.CurrentKeys = sortedKeys(seenCurrent)
	return scan
}

// Summary renders a one-line description of the scan.
func (c *ComposeScan) Summary() string {
	if c == nil {
		return ""
	}
	if c.ReadError != "" {
		return fmt.Sprintf("%s is not readable (%s)", c.Path, c.ReadError)
	}
	switch {
	case len(c.LegacyKeys) > 0 && len(c.CurrentKeys) > 0:
		return fmt.Sprintf("%s mixes %d legacy LLM_TRACELAB_* and %d TRAJECTA_* variables",
			c.Path, len(c.LegacyKeys), len(c.CurrentKeys))
	case len(c.LegacyKeys) > 0:
		return fmt.Sprintf("%s only passes legacy LLM_TRACELAB_* variables (%d); the current binaries read none of them",
			c.Path, len(c.LegacyKeys))
	case len(c.CurrentKeys) > 0:
		return fmt.Sprintf("%s passes %d TRAJECTA_* variables", c.Path, len(c.CurrentKeys))
	default:
		return fmt.Sprintf("%s references no Trajecta environment variables", c.Path)
	}
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// MaskSecret hides the sensitive part of a configuration value so that
// diagnostics can be pasted into an issue safely.
func MaskSecret(key, value string) string {
	if value == "" {
		return ""
	}
	if !looksSensitive(key) {
		return value
	}
	if masked, ok := maskURLPassword(value); ok {
		return masked
	}
	if len(value) <= 8 {
		return "***"
	}
	return value[:4] + "***" + value[len(value)-2:]
}

func looksSensitive(key string) bool {
	upper := strings.ToUpper(key)
	for _, needle := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "DSN", "CREDENTIAL"} {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	return false
}

// maskURLPassword rewrites the userinfo section of a URL-like DSN.
func maskURLPassword(value string) (string, bool) {
	scheme := -1
	for i := 0; i+3 <= len(value); i++ {
		if value[i:i+3] == "://" {
			scheme = i
			break
		}
	}
	if scheme < 0 {
		return "", false
	}
	at := -1
	for i := scheme + 3; i < len(value); i++ {
		if value[i] == '@' {
			at = i
			break
		}
		if value[i] == '/' {
			break
		}
	}
	if at < 0 {
		return value, true
	}
	userinfo := value[scheme+3 : at]
	colon := -1
	for i := 0; i < len(userinfo); i++ {
		if userinfo[i] == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return value[:scheme+3] + "***@" + value[at+1:], true
	}
	return value[:scheme+3] + userinfo[:colon] + ":***@" + value[at+1:], true
}
