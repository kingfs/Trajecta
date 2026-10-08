package store

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	entmigrate "github.com/kingfs/Trajecta/ent/dao/migrate"
)

var helperCallRe = regexp.MustCompile(`s\.(ensure\w+)\(`)

// sqliteSchema reads the schema initSchema actually built, so the test asserts
// against the real SQLite schema rather than against a second parse of the same
// DDL text.
func sqliteSchema(t *testing.T) map[string]map[string]struct{} {
	t.Helper()
	st := configTransactionTestStore(t)

	rows, err := st.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list sqlite tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan sqlite table: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite tables: %v", err)
	}
	_ = rows.Close()

	out := make(map[string]map[string]struct{}, len(tables))
	for _, table := range tables {
		cols, err := st.db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY name`, table)
		if err != nil {
			t.Fatalf("list columns of %s: %v", table, err)
		}
		set := map[string]struct{}{}
		for cols.Next() {
			var name string
			if err := cols.Scan(&name); err != nil {
				t.Fatalf("scan column of %s: %v", table, err)
			}
			set[name] = struct{}{}
		}
		if err := cols.Err(); err != nil {
			t.Fatalf("iterate columns of %s: %v", table, err)
		}
		_ = cols.Close()
		out[table] = set
	}
	return out
}

// postgresSchema is ent's generated table list. initSchema applies exactly this
// to Postgres through `Schema.Create`, so it — not the checked-in migrations —
// is the schema a Postgres deployment ends up with.
func postgresSchema() map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{}, len(entmigrate.Tables))
	for _, table := range entmigrate.Tables {
		if table == nil {
			continue
		}
		set := make(map[string]struct{}, len(table.Columns))
		for _, column := range table.Columns {
			if column == nil {
				continue
			}
			set[column.Name] = struct{}{}
		}
		out[table.Name] = set
	}
	return out
}

// storePackageSource returns the concatenated source of every non-test file in
// the store package, so a schema check does not depend on which file a
// declaration happens to live in.
func storePackageSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/store: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		t.Fatal("no non-test Go files found in internal/store")
	}
	sort.Strings(names)
	var out strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out.WriteString("\n// file: ")
		out.WriteString(name)
		out.WriteString("\n")
		out.Write(data)
	}
	return out.String()
}

// postgresSchemaReach returns the part of the package that runs when the driver
// is Postgres: the body of initSchema's Postgres branch plus the body of every
// helper that branch calls. It is how the test proves that a hand-written SQLite
// table also has a Postgres creator, instead of taking an allowlist entry's word
// for it.
//
// It searches the whole package rather than one file: the store is split across
// several files and the split moves with refactors, so a gate pinned to a
// filename reports drift every time a declaration is relocated - which is a
// false alarm about the schema and a real one about the gate.
func postgresSchemaReach(t *testing.T, src string) string {
	t.Helper()
	// Anchor on initSchema itself: several functions branch on the driver, and a
	// plain search for the first `if s.driver == "postgres" {` in the package
	// finds whichever file sorts first, which made this reach a span covering
	// unrelated files and let a missing Postgres creator pass unnoticed.
	initSchemaSrc, ok := functionBody(src, "initSchema")
	if !ok {
		t.Fatal("cannot locate func (s *Store) initSchema in the store package")
	}
	// initSchema lives in one file, so its body must not span a file boundary in
	// the concatenated source. It would if the anchor were wrong, or if
	// functionBody's "up to the next func" slice ran past the end of the file,
	// and either makes the branch boundaries meaningless.
	if strings.Contains(initSchemaSrc, "// file: ") {
		t.Fatal("initSchema's body spans a file boundary in the concatenated source, so its Postgres branch cannot be located reliably")
	}
	start := strings.Index(initSchemaSrc, `if s.driver == "postgres" {`)
	// The branch ends where initSchema starts applying the SQLite schema, whose
	// statement list moved to sqlite_schema.go, so the marker is the assignment
	// rather than the literal.
	end := strings.Index(initSchemaSrc, "\n\tstmts := ")
	if start < 0 || end <= start {
		t.Fatalf("cannot locate initSchema's Postgres branch (start=%d end=%d)", start, end)
	}
	branch := initSchemaSrc[start:end]
	reach := branch
	for _, match := range helperCallRe.FindAllStringSubmatch(branch, -1) {
		body, ok := functionBody(src, match[1])
		if !ok {
			t.Fatalf("initSchema's Postgres branch calls s.%s() but no such method is defined in store.go", match[1])
		}
		reach += "\n" + body
	}
	return reach
}

// createTableRe matches the CREATE TABLE that defines a table, whether the
// hand-written DDL quotes the name (the Postgres-only tables in initSchema) or
// not (the shared ensure*Schema helpers).
func createTableRe(table string) *regexp.Regexp {
	return regexp.MustCompile(`CREATE TABLE (?:IF NOT EXISTS )"?` + regexp.QuoteMeta(table) + `"?`)
}

// functionBody returns the source of `func (s *Store) name(`, up to the next
// top-level function declaration.
func functionBody(src string, name string) (string, bool) {
	marker := "func (s *Store) " + name + "("
	start := strings.Index(src, marker)
	if start < 0 {
		return "", false
	}
	rest := src[start+len(marker):]
	if next := strings.Index(rest, "\nfunc "); next >= 0 {
		return rest[:next], true
	}
	return rest, true
}

// TestSQLiteAndPostgresSchemasAgree pins that the hand-written SQLite schema and
// ent's schema cannot drift apart.
//
// SQLite gets inline DDL from initSchema while Postgres gets ent's schema, so a
// table or column added to one and not the other yields a deployment that works
// on one driver and fails on the other. Nothing else compares them: every unit
// test runs on SQLite and the Postgres tests need a live server, so such a
// difference would surface first in production.
func TestSQLiteAndPostgresSchemasAgree(t *testing.T) {
	sqlite := sqliteSchema(t)
	postgres := postgresSchema()
	if len(postgres) == 0 {
		t.Fatal("ent's table list is empty; the generated schema was not loaded")
	}

	// The only intentional differences, each with its reason. Every entry must
	// still describe a difference that exists: a stale entry fails the test, so
	// this list cannot rot into a blanket exemption.
	postgresOnlyTables := map[string]string{
		"api_tokens": "owned by internal/auth, which keeps its own database on SQLite (auth.database_path) and its own Postgres schema",
		"users":      "owned by internal/auth, which keeps its own database on SQLite (auth.database_path) and its own Postgres schema",
	}
	postgresOnlyColumns := map[string]string{
		"parser_versions.id": "Postgres keys parser_versions on an ent surrogate id plus a unique index on (parser, version); SQLite keys it on PRIMARY KEY(parser, version). Equivalent uniqueness, and the application never reads the surrogate.",
	}
	sqliteOnlyTables := map[string]string{
		"app_schema_status": "SQLite applies its schema at startup and records the applied version in this table; Postgres versions its schema with ent migrations and has no equivalent",
	}
	// Tables that are not ent-managed but are created by hand for both drivers.
	// The test checks that initSchema's Postgres path really creates each one.
	handWrittenTables := []string{
		"app_settings",
		"model_aliases",
		"session_summaries",
	}

	postgresReach := postgresSchemaReach(t, storePackageSource(t))
	handWritten := make(map[string]bool, len(handWrittenTables))
	for _, table := range handWrittenTables {
		handWritten[table] = true
	}
	usedHandWrittenTables := map[string]bool{}

	var problems []string
	usedPostgresOnlyTables := map[string]bool{}
	usedPostgresOnlyColumns := map[string]bool{}
	usedSQLiteOnlyTables := map[string]bool{}

	for table, pgColumns := range postgres {
		cols, ok := sqlite[table]
		if !ok {
			if _, allowed := postgresOnlyTables[table]; allowed {
				usedPostgresOnlyTables[table] = true
				continue
			}
			problems = append(problems, fmt.Sprintf("table %q is in ent's schema but not in the SQLite schema", table))
			continue
		}
		for column := range pgColumns {
			if _, ok := cols[column]; ok {
				continue
			}
			key := table + "." + column
			if _, allowed := postgresOnlyColumns[key]; allowed {
				usedPostgresOnlyColumns[key] = true
				continue
			}
			problems = append(problems, fmt.Sprintf("column %s is in ent's schema but not in the SQLite schema", key))
		}
	}
	for table, cols := range sqlite {
		pgColumns, ok := postgres[table]
		if !ok {
			if _, allowed := sqliteOnlyTables[table]; allowed {
				usedSQLiteOnlyTables[table] = true
				continue
			}
			if handWritten[table] {
				usedHandWrittenTables[table] = true
				if !createTableRe(table).MatchString(postgresReach) {
					problems = append(problems, fmt.Sprintf("table %q is hand-written for SQLite but nothing in initSchema's Postgres path creates it, so Postgres would not have it", table))
				}
				continue
			}
			problems = append(problems, fmt.Sprintf("table %q is in the SQLite schema but is neither in ent's schema nor declared as a hand-written table", table))
			continue
		}
		for column := range cols {
			if _, ok := pgColumns[column]; !ok {
				problems = append(problems, fmt.Sprintf("column %s.%s is in the SQLite schema but not in ent's schema", table, column))
			}
		}
	}

	for table := range postgresOnlyTables {
		if !usedPostgresOnlyTables[table] {
			problems = append(problems, fmt.Sprintf("stale exception: table %q is listed as Postgres-only but both schemas have it, or neither does; remove the entry", table))
		}
	}
	for key := range postgresOnlyColumns {
		if !usedPostgresOnlyColumns[key] {
			problems = append(problems, fmt.Sprintf("stale exception: column %q is listed as Postgres-only but the SQLite schema has it; remove the entry", key))
		}
	}
	for table := range sqliteOnlyTables {
		if !usedSQLiteOnlyTables[table] {
			problems = append(problems, fmt.Sprintf("stale exception: table %q is listed as SQLite-only but both schemas have it, or neither does; remove the entry", table))
		}
	}
	for _, table := range handWrittenTables {
		if !usedHandWrittenTables[table] {
			problems = append(problems, fmt.Sprintf("stale entry: table %q is listed as hand-written but it is not a table missing from ent's schema; remove it from the list", table))
		}
	}

	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("the SQLite and Postgres schemas drifted:\n  %s", strings.Join(problems, "\n  "))
	}
}
