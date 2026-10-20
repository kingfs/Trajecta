// Package appdb owns the config-driven access paths to the application
// database: which driver is in use, whether migrations are versioned SQL or
// startup schema initialization, and how to open the trace index and the auth
// store.
//
// Both binaries need this. The proxy process applies migrations before it opens
// its store, and the trajecta CLI opens the same database for its db, auth,
// audit and analyze commands.
package appdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/internal/appdbmigrate"
	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
)

// Auth migration and open hooks stay as variables so a test can observe which
// path the sqlite and postgres drivers take.
var (
	AuthMigrateDatabaseUp = auth.MigrateDatabaseUp
	AuthOpenDatabase      = auth.OpenDatabase
)

// NormalizeDriver canonicalizes a database.driver value. An unset driver means
// Postgres; see store.normalizeDatabaseDriver for the same rule.
func NormalizeDriver(driver string) string {
	driver = strings.ToLower(strings.TrimSpace(driver))
	switch driver {
	case "":
		return "postgres"
	case "postgresql":
		return "postgres"
	default:
		return driver
	}
}

// MigrationMode reports which migration contract a driver follows.
func MigrationMode(driver string) string {
	switch NormalizeDriver(driver) {
	case "postgres":
		return "versioned-sql"
	default:
		return "schema-init"
	}
}

// MigrateUp applies the application migrations for cfg: versioned SQL for
// Postgres, startup schema initialization for SQLite.
func MigrateUp(cfg *config.Config, steps int) error {
	if MigrationMode(cfg.DatabaseDriver()) == "versioned-sql" {
		return appdbmigrate.MigrateUp(cfg.DatabaseDriver(), cfg.DatabaseDSN(), steps)
	}
	st, err := Initialize(cfg)
	if err != nil {
		return err
	}
	return st.Close()
}

// OptimizeIndexes applies the non-transactional PostgreSQL index optimizations.
// SQLite reports a no-op result.
func OptimizeIndexes(cfg *config.Config) (appdbmigrate.IndexOptimizationResult, error) {
	if MigrationMode(cfg.DatabaseDriver()) != "versioned-sql" {
		return appdbmigrate.IndexOptimizationResult{
			Driver:  NormalizeDriver(cfg.DatabaseDriver()),
			Applied: false,
		}, nil
	}
	return appdbmigrate.OptimizeIndexes(context.Background(), cfg.DatabaseDriver(), cfg.DatabaseDSN())
}

// Open migrates when cfg asks for it and returns the application store.
func Open(cfg *config.Config) (*store.Store, error) {
	if cfg.DatabaseAutoMigrate() {
		if err := MigrateUp(cfg, 0); err != nil {
			return nil, err
		}
	}
	return store.NewWithDatabaseOptions(
		cfg.TraceOutputDir(),
		cfg.DatabaseDriver(),
		cfg.DatabaseDSN(),
		cfg.DatabaseMaxOpenConns(),
		cfg.DatabaseMaxIdleConns(),
		store.DatabaseOptions{
			AutoMigrate:           false,
			UseSessionSummaryRead: cfg.DatabaseUseSessionSummaryRead(),
		},
	)
}

// Initialize applies the startup schema without consulting auto_migrate.
func Initialize(cfg *config.Config) (*store.Store, error) {
	return store.NewWithDatabase(
		cfg.TraceOutputDir(),
		cfg.DatabaseDriver(),
		cfg.DatabaseDSN(),
		cfg.DatabaseMaxOpenConns(),
		cfg.DatabaseMaxIdleConns(),
	)
}

// OpenAuthStore opens the auth tables, applying the SQLite auth migration
// first when auto_migrate is on. Postgres auth tables are owned by the
// application migration set, which the caller applies before opening.
func OpenAuthStore(cfg *config.Config) (*auth.Store, error) {
	driver := NormalizeDriver(cfg.DatabaseDriver())
	switch driver {
	case "sqlite":
		if cfg.DatabaseAutoMigrate() {
			if err := AuthMigrateDatabaseUp(driver, cfg.DatabaseDSN(), 0); err != nil {
				return nil, fmt.Errorf("migrate database: %w", err)
			}
		}
	case "postgres":
		// Postgres auth tables are owned by the application migration set, which
		// serve applies before opening the auth store.
	default:
		return nil, fmt.Errorf("auth store driver %q is not supported yet", driver)
	}

	st, err := AuthOpenDatabase(
		driver,
		cfg.DatabaseDSN(),
		cfg.DatabaseMaxOpenConns(),
		cfg.DatabaseMaxIdleConns(),
	)
	if err != nil {
		return nil, err
	}
	return st, nil
}
