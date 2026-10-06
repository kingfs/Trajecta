package legacymigrate

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/kingfs/Trajecta/internal/redaction"
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
//
// Whether a key is sensitive, and how a URL or DSN is rewritten, are owned by
// internal/redaction: a second implementation here had drifted from it, keeping a
// secret that sits in the URL username and missing a secret query parameter
// entirely, and it used a marker list that this file maintained separately.
func MaskSecret(key, value string) string {
	if value == "" {
		return ""
	}
	if !redaction.IsSensitiveURLParam(key) {
		return value
	}
	if strings.Contains(value, "://") {
		return redaction.DisplayURL(value)
	}
	if len(value) <= 8 {
		return "***"
	}
	return value[:4] + "***" + value[len(value)-2:]
}
