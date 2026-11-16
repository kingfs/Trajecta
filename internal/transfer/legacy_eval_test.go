package transfer

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// legacyEvalRows are the retired eval tables with the row counts an old-style
// bundle carries. The row payloads keep the historical column shape; import
// must read them without decoding or writing anything.
var legacyEvalRows = map[string][]string{
	"datasets": {
		`{"id":"ds-1","name":"old dataset","description":"","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-01T00:00:00Z"}`,
	},
	"eval_runs": {
		`{"id":"run-1","dataset_id":"ds-1","source_type":"dataset","source_id":"ds-1","evaluator_set":"baseline_v4","created_at":"2026-09-01T00:00:00Z","completed_at":"2026-09-01T00:00:00Z","trace_count":1,"score_count":1,"pass_count":1,"fail_count":0}`,
	},
	"experiment_runs": {
		`{"id":"exp-1","name":"old experiment","description":"","baseline_eval_run_id":"run-1","candidate_eval_run_id":"run-1","created_at":"2026-09-01T00:00:00Z","baseline_score_count":1,"candidate_score_count":1,"baseline_pass_rate":100,"candidate_pass_rate":100,"pass_rate_delta":0,"matched_score_count":1,"improvement_count":0,"regression_count":0}`,
	},
	"dataset_examples": {
		`{"id":1,"dataset_id":"ds-1","trace_id":"trace-a","position":1,"added_at":"2026-09-01T00:00:00Z","source_type":"trace_list","source_id":"","note":""}`,
		`{"id":2,"dataset_id":"ds-1","trace_id":"trace-b","position":2,"added_at":"2026-09-01T00:00:00Z","source_type":"trace_list","source_id":"","note":""}`,
	},
	"scores": {
		`{"id":"score-1","trace_id":"trace-a","session_id":"","dataset_id":"ds-1","eval_run_id":"run-1","evaluator_key":"http_status_2xx","value":1,"status":"pass","label":"pass","explanation":"","created_at":"2026-09-01T00:00:00Z"}`,
		`{"id":"score-2","trace_id":"trace-b","session_id":"","dataset_id":"ds-1","eval_run_id":"run-1","evaluator_key":"http_status_2xx","value":0,"status":"fail","label":"fail","explanation":"","created_at":"2026-09-01T00:00:00Z"}`,
	},
}

// writeLegacyEvalBundle builds a plain (unencrypted, uncompressed) old-style
// bundle: one live table (app_settings) plus the five retired eval tables,
// declared in the manifest exactly the way an older Trajecta exported them.
func writeLegacyEvalBundle(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)

	writeEntry := func(name string, data []byte) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Now().UTC()}); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
		}
	}

	manifest := Manifest{
		Format:      BundleFormat,
		CreatedAt:   time.Now().UTC(),
		Window:      Window{All: true},
		Compression: compressionNone,
		Tables:      []TableCount{{Name: "app_settings", Rows: 1}},
	}
	for _, table := range legacyEvalImportTables {
		manifest.Tables = append(manifest.Tables, TableCount{Name: table, Rows: int64(len(legacyEvalRows[table]))})
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeEntry(entryManifest, manifestData)
	writeEntry(tableEntryName("app_settings"), []byte(`{"setting_key":"routing.settings","value_json":"{}","updated_at":"2026-09-01T00:00:00Z"}`+"\n"))
	for _, table := range legacyEvalImportTables {
		writeEntry(tableEntryName(table), []byte(strings.Join(legacyEvalRows[table], "\n")+"\n"))
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
}

func legacyTableReport(t *testing.T, res *ImportResult, name string) TableImportReport {
	t.Helper()
	for _, rep := range res.Tables {
		if rep.Name == name {
			return rep
		}
	}
	t.Fatalf("import report has no entry for table %q (tables: %+v)", name, res.Tables)
	return TableImportReport{}
}

// TestImportLegacyEvalBundleSkipsRetiredTables is the retirement contract: an
// old bundle that still carries the five eval tables imports successfully,
// each eval table is read to the end and reported as legacy-skipped (one log
// line per table), nothing is written for them, and the live tables around
// them merge normally.
func TestImportLegacyEvalBundleSkipsRetiredTables(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "legacy.tar")
	writeLegacyEvalBundle(t, bundle)

	target := newTargetStore(t)
	var logBuf bytes.Buffer
	log, err := NewLogger(&logBuf, "")
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	res, err := Import(context.Background(), target, targetDir(t, target), ImportOptions{
		Bundle: bundle,
		Apply:  true,
		Log:    log,
	})
	if err != nil {
		t.Fatalf("Import(legacy eval bundle) error = %v", err)
	}

	for _, table := range legacyEvalImportTables {
		want := int64(len(legacyEvalRows[table]))
		rep := legacyTableReport(t, res, table)
		if rep.Processed != want || rep.SkippedLegacy != want {
			t.Fatalf("table %s report = %+v, want processed/skipped_legacy = %d/%d", table, rep, want, want)
		}
		if rep.Inserted != 0 || rep.Updated != 0 {
			t.Fatalf("table %s report = %+v, want zero writes for a retired table", table, rep)
		}
		if !strings.Contains(logBuf.String(), "table "+table+" belongs to the retired eval feature") {
			t.Fatalf("import log has no legacy-skipped line for %q:\n%s", table, logBuf.String())
		}
	}

	// The live table next to the skipped ones still merges.
	if rep := legacyTableReport(t, res, "app_settings"); rep.Inserted != 1 {
		t.Fatalf("app_settings report = %+v, want one inserted row", rep)
	}

	// The target never creates the retired tables.
	for _, table := range legacyEvalImportTables {
		var count int64
		if err := target.TransferQueryRow(context.Background(),
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query sqlite_master for %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("import created the retired table %q", table)
		}
	}

	// A second import of the same legacy bundle stays a zero-write merge.
	again, err := Import(context.Background(), target, targetDir(t, target), ImportOptions{
		Bundle: bundle,
		Apply:  true,
	})
	if err != nil {
		t.Fatalf("second Import(legacy eval bundle) error = %v", err)
	}
	for _, rep := range again.Tables {
		if rep.Inserted != 0 || rep.Updated != 0 {
			t.Fatalf("second import wrote table %s: %+v", rep.Name, rep)
		}
	}
}

// TestImportLegacyEvalBundleDryRun checks the tolerance on the dry-run path:
// the report carries the legacy-skipped counts and nothing is written.
func TestImportLegacyEvalBundleDryRun(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "legacy-dry.tar")
	writeLegacyEvalBundle(t, bundle)

	target := newTargetStore(t)
	res, err := Import(context.Background(), target, targetDir(t, target), ImportOptions{
		Bundle: bundle,
		Apply:  false,
	})
	if err != nil {
		t.Fatalf("Import(legacy eval bundle, dry run) error = %v", err)
	}
	if !res.DryRun {
		t.Fatal("DryRun = false")
	}
	for _, table := range legacyEvalImportTables {
		want := int64(len(legacyEvalRows[table]))
		rep := legacyTableReport(t, res, table)
		if rep.Processed != want || rep.SkippedLegacy != want || rep.Inserted != 0 {
			t.Fatalf("dry-run table %s report = %+v, want %d read and skipped", table, rep, want)
		}
	}
	var settings int64
	if err := target.TransferQueryRow(context.Background(), `SELECT COUNT(*) FROM app_settings`).Scan(&settings); err != nil {
		t.Fatalf("count app_settings: %v", err)
	}
	if settings != 0 {
		t.Fatalf("dry run wrote %d app_settings rows", settings)
	}
}

// TestImportUnknownTableStillFails pins that the tolerance is scoped to the
// five retired eval tables: any other unknown table keeps aborting the import.
func TestImportUnknownTableStillFails(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "unknown.tar")
	f, err := os.Create(bundle)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	writeEntry := func(name string, data []byte) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
		}
	}
	manifestData, err := json.Marshal(Manifest{
		Format:      BundleFormat,
		CreatedAt:   time.Now().UTC(),
		Window:      Window{All: true},
		Compression: compressionNone,
		Tables:      []TableCount{{Name: "mystery_table", Rows: 0}},
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeEntry(entryManifest, manifestData)
	writeEntry(tableEntryName("mystery_table"), nil)
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	target := newTargetStore(t)
	_, err = Import(context.Background(), target, targetDir(t, target), ImportOptions{Bundle: bundle, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "bundle contains unknown table") {
		t.Fatalf("Import(unknown table) error = %v, want the unknown-table rejection", err)
	}
}

// TestExportOmitsRetiredEvalTables pins the export side of the retirement: a
// fresh bundle carries no eval entry and declares no eval table.
func TestExportOmitsRetiredEvalTables(t *testing.T) {
	source := newFixture(t)
	bundle := filepath.Join(t.TempDir(), "noeval.tar.gz")
	if _, err := Export(context.Background(), source.store, source.dir, ExportOptions{
		Since:    ptrTime(source.recordedAt.Add(-time.Hour)),
		Until:    ptrTime(source.recordedAt.Add(time.Hour)),
		Out:      bundle,
		Password: "pw",
	}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}

	cur, err := openBundle(bundle, "pw")
	if err != nil {
		t.Fatalf("openBundle() error = %v", err)
	}
	defer cur.Close()
	manifest, err := cur.readManifest()
	if err != nil {
		t.Fatalf("readManifest() error = %v", err)
	}
	for _, table := range manifest.Tables {
		if isLegacyEvalTable(table.Name) {
			t.Fatalf("manifest still declares the retired table %q", table.Name)
		}
	}
	for {
		hdr, err := cur.next()
		if err != nil {
			break // io.EOF: the bundle is walked to the end
		}
		for _, table := range legacyEvalImportTables {
			if hdr.Name == tableEntryName(table) {
				t.Fatalf("bundle still carries the retired eval entry %q", hdr.Name)
			}
		}
	}
}
