package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/kingfs/Trajecta/ent/dao"
)

// ConfigurationTransaction serializes management writes in this process. Both
// raw SQL and ent operations share one transaction. The callback must commit
// explicitly, after preparing its runtime snapshot; all other exits roll back.
//
// It holds the upstream write lock (upstreamMu) for its whole duration,
// serializing Monitor configuration writes against bundle imports, which hold
// the same lock for the whole merge. upstream_targets/upstream_models are
// legacy projection tables with no writer left: neither the router nor the
// channel probe writes them any more, and routing state is published in memory
// only. The lock is kept so the two transaction writers stay serialized.
func (s *Store) ConfigurationTransaction(ctx context.Context, apply func(*Store, func() error) error) error {
	if s == nil || s.db == nil || s.shared == nil {
		return fmt.Errorf("configuration transaction: %w", errorsNewStoreClosed())
	}
	if apply == nil {
		return errors.New("configuration transaction: apply callback is required")
	}
	if s.TransactionScoped() {
		return fmt.Errorf("configuration transaction: %w", ErrNestedTransaction)
	}
	s.shared.configMu.Lock()
	defer s.shared.configMu.Unlock()
	s.shared.upstreamMu.Lock()
	defer s.shared.upstreamMu.Unlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	view := s.txView(tx)
	committed := false
	commit := func() error {
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}
	if err := apply(view, commit); err != nil {
		return err
	}
	if !committed {
		return errors.New("configuration transaction was not committed")
	}
	return nil
}

// txView builds the Store view shared by ConfigurationTransaction and the
// transfer read transaction: raw SQL and ent operations both run inside tx,
// with the same secrets box, shared locks and output directory.
func (s *Store) txView(tx *sql.Tx) *Store {
	name := dialect.SQLite
	if s.driver == "postgres" {
		name = dialect.Postgres
	}
	driver := &configurationTxDriver{Driver: entsql.NewDriver(name, entsql.Conn{ExecQuerier: tx})}
	return &Store{
		db:        &rebindingDB{DB: s.db.DB, tx: tx, driver: s.driver},
		client:    dao.NewClient(dao.Driver(driver)),
		outputDir: s.outputDir, dbPath: s.dbPath, driver: s.driver,
		secrets: s.secrets, useSessionSummaryRead: s.useSessionSummaryRead,
		shared: s.shared,
	}
}

// TransactionScoped reports whether this store view writes through one open
// transaction instead of the connection pool. A transaction-scoped store
// already holds the configuration and upstream write locks.
func (s *Store) TransactionScoped() bool {
	return s != nil && s.db != nil && s.db.tx != nil
}

// Store helpers may open nested ent transactions. Their commits are scoped to
// the outer configuration operation, so a later failure rolls back everything.
type configurationTxDriver struct{ *entsql.Driver }

func (d *configurationTxDriver) Tx(context.Context) (dialect.Tx, error) {
	return dialect.NopTx(d), nil
}
