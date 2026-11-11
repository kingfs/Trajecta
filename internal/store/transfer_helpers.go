// Export/import transfer helpers.
//
// internal/transfer drives whole-database exports and merges between two
// trajecta deployments over the same public store used by everything else.
// It needs two capabilities the store keeps private: raw SQL access that
// honours the placeholder rebinding and the transaction scope, and a
// caller-driven read transaction for a consistent snapshot during export.

package store

import (
	"context"
	"database/sql"
	"errors"
)

// TransferExec runs one raw SQL statement ("?" placeholders are rebound for
// Postgres) against the store's current transaction scope, if any.
func (s *Store) TransferExec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store is closed")
	}
	return s.db.ExecContext(ctx, query, args...)
}

// TransferQuery runs one raw SQL statement ("?" placeholders are rebound for
// Postgres) against the store's current transaction scope, if any.
//
// The caller must close the returned rows. Scan time columns into `any` for
// driver-agnostic handling (see transferAnyToTime in internal/transfer).
func (s *Store) TransferQuery(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store is closed")
	}
	return s.db.QueryContext(ctx, query, args...)
}

// TransferQueryRow runs one raw SQL statement ("?" placeholders are rebound
// for Postgres) and returns at most one row.
func (s *Store) TransferQueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.QueryRowContext(ctx, query, args...)
}

// TransferReadTx runs fn inside one read transaction so every query it issues
// through the view observes a consistent snapshot. Commit happens when fn
// returns nil; any error rolls back. The transaction takes no configuration
// and upstream write locks: it writes nothing and stays cheap enough to
// coexist with a quiesced deployment.
func (s *Store) TransferReadTx(ctx context.Context, fn func(*Store) error) error {
	if s == nil || s.db == nil || s.shared == nil {
		return errors.New("store is closed")
	}
	if s.TransactionScoped() {
		return errors.New("transfer read transaction is already inside a transaction")
	}
	if fn == nil {
		return errors.New("transfer read transaction requires a callback")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(s.txView(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// TransferEncryptSecretString seals one value with the local secret box, or
// returns it unchanged when no key is configured (the same semantics the
// credential mask audit writer uses).
func (s *Store) TransferEncryptSecretString(value string) (string, error) {
	if s == nil || s.secrets == nil || s.secrets.aead == nil {
		return value, nil
	}
	sealed, err := s.secrets.encryptSecretBytes([]byte(value))
	if err != nil {
		return "", err
	}
	return string(sealed), nil
}

// TransferDecryptSecretString opens one value with the local secret box, or
// returns it unchanged when no key is configured.
func (s *Store) TransferDecryptSecretString(value string) (string, error) {
	if s == nil || s.secrets == nil || s.secrets.aead == nil {
		return value, nil
	}
	plain, err := s.secrets.decryptSecretBytes([]byte(value))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// TransferDecryptHeadersJSON decrypts the secret-bearing members of one channel
// headers_json document, leaving every other value untouched.
func (s *Store) TransferDecryptHeadersJSON(headersJSON string) (string, error) {
	return s.decryptHeadersJSON(headersJSON)
}

// TransferEncryptHeadersJSON re-encrypts the secret-bearing members of one
// channel headers_json document with this deployment's local secret box.
func (s *Store) TransferEncryptHeadersJSON(headersJSON string) (string, error) {
	return s.encryptHeadersJSON(headersJSON)
}
