// Package legacymigrate implements the one-off migration helpers used by the
// trajecta CLI: it loads pre-rename .env files, merges a legacy
// SQLite application database into Postgres, and rewrites or validates .http
// cassettes.
//
// Everything in this package is additive and idempotent: no helper deletes a
// cassette, and the SQLite merge only ever inserts rows that are missing from
// the target database.
package legacymigrate

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	// LegacyEnvPrefix is the environment-variable prefix used before the
	// project was renamed from llm-tracelab.
	LegacyEnvPrefix = "LLM_TRACELAB_"
	// EnvPrefix is the environment-variable prefix the current binaries read.
	EnvPrefix = "TRAJECTA_"
)

// EnvEntry describes a single variable read from a .env file.
type EnvEntry struct {
	// SourceKey is the name as written in the file.
	SourceKey string `json:"source_key"`
	// Key is the effective variable name after legacy prefix mapping.
	Key string `json:"key"`
	// Value is the parsed value.
	Value string `json:"value,omitempty"`
	// Renamed reports whether SourceKey used the legacy prefix.
	Renamed bool `json:"renamed,omitempty"`
	// Applied reports whether the variable was written to the process
	// environment.
	Applied bool `json:"applied"`
	// Note explains why a variable was not applied.
	Note string `json:"note,omitempty"`
}

// EnvLoadResult is the outcome of loading a .env file.
type EnvLoadResult struct {
	Path     string     `json:"path"`
	Entries  []EnvEntry `json:"entries"`
	Warnings []string   `json:"warnings,omitempty"`
	// Applied counts variables written to the process environment.
	Applied int `json:"applied"`
	// Kept counts variables skipped because the process environment already
	// defined them.
	Kept int `json:"kept"`
}

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// MapLegacyEnvKey maps a pre-rename LLM_TRACELAB_* variable name onto the
// TRAJECTA_* name the current binaries read. Names without the legacy prefix
// are returned unchanged with renamed == false.
func MapLegacyEnvKey(key string) (mapped string, renamed bool) {
	if !strings.HasPrefix(key, LegacyEnvPrefix) {
		return key, false
	}
	return EnvPrefix + strings.TrimPrefix(key, LegacyEnvPrefix), true
}

// ParseEnvFile parses dotenv content. It supports comments, blank lines, an
// optional `export ` prefix, single/double quoted values, and unquoted values
// with trailing `#` comments. Variable expansion is deliberately not
// performed: the values in a Trajecta .env are literal.
//
// The returned entries keep file order and the last assignment of a variable
// wins when the same effective key appears more than once.
func ParseEnvFile(data []byte) (entries []EnvEntry, warnings []string) {
	for i, raw := range strings.Split(string(data), "\n") {
		lineNo := i + 1
		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		trimmed = strings.TrimPrefix(trimmed, "export ")
		trimmed = strings.TrimSpace(trimmed)
		eq := strings.Index(trimmed, "=")
		if eq <= 0 {
			warnings = append(warnings, fmt.Sprintf("line %d: ignored (expected KEY=VALUE)", lineNo))
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		if !envKeyPattern.MatchString(key) {
			warnings = append(warnings, fmt.Sprintf("line %d: ignored (invalid variable name %q)", lineNo, key))
			continue
		}
		value, err := parseEnvValue(trimmed[eq+1:])
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("line %d: %v", lineNo, err))
			continue
		}
		mapped, renamed := MapLegacyEnvKey(key)
		entries = append(entries, EnvEntry{SourceKey: key, Key: mapped, Value: value, Renamed: renamed})
	}
	return entries, warnings
}

// parseEnvValue interprets the right-hand side of a dotenv assignment.
func parseEnvValue(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	switch value[0] {
	case '"':
		end := indexUnescapedQuote(value, '"')
		if end < 0 {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		if err := rejectTrailingContent(value[end+1:]); err != nil {
			return "", err
		}
		return unescapeDoubleQuoted(value[1:end])
	case '\'':
		if len(value) < 2 {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		end := strings.IndexByte(value[1:], '\'')
		if end < 0 {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		end++
		if err := rejectTrailingContent(value[end+1:]); err != nil {
			return "", err
		}
		// single quotes are literal, matching shell and docker-compose rules
		return value[1:end], nil
	default:
		if idx := strings.Index(value, " #"); idx >= 0 {
			value = strings.TrimSpace(value[:idx])
		}
		return value, nil
	}
}

// indexUnescapedQuote returns the index of the first closing quote that is not
// escaped with a backslash.
func indexUnescapedQuote(value string, quote byte) int {
	for i := 1; i < len(value); i++ {
		switch value[i] {
		case '\\':
			i++
		case quote:
			return i
		}
	}
	return -1
}

// rejectTrailingContent allows only whitespace and a trailing comment after a
// quoted value, the way .env readers and docker-compose behave.
func rejectTrailingContent(rest string) error {
	rest = strings.TrimSpace(rest)
	if rest == "" || strings.HasPrefix(rest, "#") {
		return nil
	}
	return fmt.Errorf("unexpected %q after a quoted value", rest)
}

func unescapeDoubleQuoted(value string) (string, error) {
	if !strings.Contains(value, `\`) {
		return value, nil
	}
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			b.WriteByte(value[i])
			continue
		}
		i++
		if i >= len(value) {
			return "", fmt.Errorf("trailing backslash in double-quoted value")
		}
		switch value[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		case '$':
			b.WriteByte('$')
		default:
			b.WriteByte('\\')
			b.WriteByte(value[i])
		}
	}
	return b.String(), nil
}

// LoadEnvFile reads a .env file, maps legacy variable names onto the current
// prefix and applies the values to the process environment. Variables already
// present in the process environment win unless override is true, which keeps
// explicit shell exports authoritative.
func LoadEnvFile(path string, override bool) (*EnvLoadResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries, warnings := ParseEnvFile(data)
	result := &EnvLoadResult{Path: path, Entries: entries, Warnings: warnings}

	lastIndex := make(map[string]int, len(entries))
	for i, e := range entries {
		if _, dup := lastIndex[e.Key]; dup {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("line for %s is overridden by a later assignment", e.Key))
		}
		lastIndex[e.Key] = i
	}
	for i := range result.Entries {
		entry := &result.Entries[i]
		if lastIndex[entry.Key] != i {
			entry.Note = "overridden by a later assignment"
			continue
		}
		if _, exists := os.LookupEnv(entry.Key); exists && !override {
			entry.Note = "kept existing process environment value"
			result.Kept++
			continue
		}
		if err := os.Setenv(entry.Key, entry.Value); err != nil {
			return nil, fmt.Errorf("set %s: %w", entry.Key, err)
		}
		entry.Applied = true
		result.Applied++
	}
	return result, nil
}

// EnvFileCandidate returns the default .env path for a working directory.
func EnvFileCandidate(dir string) string {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	return strings.TrimSuffix(dir, string(os.PathSeparator)) + string(os.PathSeparator) + ".env"
}
