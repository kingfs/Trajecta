package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) SaveAppSettingJSON(ctx context.Context, key string, value any) error {
	if s == nil || s.db == nil {
		return errorsNewStoreClosed()
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("setting key is required")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal app setting %q: %w", key, err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO app_settings (setting_key, value_json, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(setting_key) DO UPDATE SET
			value_json = excluded.value_json,
			updated_at = excluded.updated_at`, key, string(payload)); err != nil {
		return fmt.Errorf("save app setting %q: %w", key, err)
	}
	return nil
}

func (s *Store) LoadAppSettingJSON(ctx context.Context, key string, out any) (bool, error) {
	if s == nil || s.db == nil {
		return false, errorsNewStoreClosed()
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false, errors.New("setting key is required")
	}
	if out == nil {
		return false, errors.New("setting output is required")
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT value_json FROM app_settings WHERE setting_key = ?`, key).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("load app setting %q: %w", key, err)
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return false, fmt.Errorf("decode app setting %q: %w", key, err)
	}
	return true, nil
}

// DeleteAppSetting removes one app setting and reports whether a row existed.
func (s *Store) DeleteAppSetting(ctx context.Context, key string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errorsNewStoreClosed()
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false, errors.New("setting key is required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM app_settings WHERE setting_key = ?`, key)
	if err != nil {
		return false, fmt.Errorf("delete app setting %q: %w", key, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete app setting %q: %w", key, err)
	}
	return affected > 0, nil
}

// InstallIDKey names the app_settings row that holds this deployment's
// anonymous install id.
const InstallIDKey = "telemetry.install_id"

// LoadOrCreateInstallID returns a stable, random identifier for this
// deployment, creating one the first time it is asked for.
//
// It exists because the thing worth counting is an installation, not a browser:
// one Monitor is routinely opened by several people, and a per-browser id would
// report five installs for one deployment. The id is generated here rather than
// in the page so that every browser behind this database reports the same one,
// which lets a collector count distinct ids.
//
// It is not a secret and it identifies nobody: it is sixteen random bytes, it is
// never derived from the machine, the database or the operator, and deleting the
// row simply makes the next call generate a new one.
func (s *Store) LoadOrCreateInstallID(ctx context.Context) (string, error) {
	var existing string
	found, err := s.LoadAppSettingJSON(ctx, InstallIDKey, &existing)
	if err != nil {
		return "", err
	}
	if found {
		if trimmed := strings.TrimSpace(existing); trimmed != "" {
			return trimmed, nil
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate install id: %w", err)
	}
	created := hex.EncodeToString(buf)
	if err := s.SaveAppSettingJSON(ctx, InstallIDKey, created); err != nil {
		return "", err
	}
	return created, nil
}
