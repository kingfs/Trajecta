package config

import (
	"errors"
	"fmt"
	"strings"
)

// NormalizeMCPPath canonicalizes the Monitor/MCP endpoint path: an empty value
// means /mcp, a missing leading slash is added, and a trailing slash is
// dropped. It is shared by the serve preflight, the management mux and the
// config inspection and doctor commands.
func NormalizeMCPPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/mcp", nil
	}
	if path == "/" {
		return "", errors.New("mcp.path must not be /")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "", errors.New("mcp.path must not be empty")
	}
	return path, nil
}

// EffectiveMCPPath is the mounted MCP path, or "" when MCP is disabled. It
// panics on an invalid non-empty path, which ValidateServe rejects first.
func EffectiveMCPPath(cfg *Config) string {
	if cfg == nil || !cfg.MCP.Enabled {
		return ""
	}
	normalized, err := NormalizeMCPPath(cfg.MCP.Path)
	if err != nil {
		panic(err)
	}
	return normalized
}

// ValidateServe reports the configuration problems that stop the proxy from
// starting: an unsupported database driver, and an MCP surface without a
// Monitor port or with an unusable path.
func ValidateServe(cfg *Config) error {
	switch driver := cfg.DatabaseDriver(); driver {
	case "sqlite", "postgres", "postgresql":
	default:
		return fmt.Errorf("database.driver %q is not supported yet; use sqlite or postgres", driver)
	}
	if cfg.MCP.Enabled && cfg.Monitor.Port == "" {
		return errors.New("monitor.port is required when mcp.enabled=true")
	}
	if cfg.MCP.Enabled {
		if _, err := NormalizeMCPPath(cfg.MCP.Path); err != nil {
			return err
		}
	}
	return nil
}
