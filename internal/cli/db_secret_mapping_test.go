package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// dbSecretMappingFixture is one sqlite database plus a mapping file and audit
// rows, laid out the way serve would lay them out under one output directory.
type dbSecretMappingFixture struct {
	dir         string
	configPath  string
	dbPath      string
	mappingPath string
}

type dbSecretMappingAuditSeed struct {
	rule       string
	original   string
	standIn    string
	detectedAt time.Time
	traceID    string
}

func secretMappingCLITestPair(tag string, realFill string, fakeFill string) secretmask.Mapping {
	return secretmask.Mapping{
		Real: tag + strings.Repeat(realFill, 48),
		Fake: tag + strings.Repeat(fakeFill, 48),
	}
}

// newDBSecretMappingFixture builds the on-disk state the CLI reads: the sqlite
// index (so the store can open), the bbolt mapping file with the given pairs, and
// the credential mask audit rows prune resolves values through. A nil pairs slice
// leaves the mapping file absent, which every command must tolerate.
func newDBSecretMappingFixture(t *testing.T, pairs []secretmask.Mapping, audits []dbSecretMappingAuditSeed, autoMigrate bool) *dbSecretMappingFixture {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "trajecta.sqlite3")
	mappingPath := filepath.Join(dir, store.SecretMappingFileName)
	st, err := store.NewWithDatabase(dir, "sqlite", dbPath, 4, 4)
	if err != nil {
		t.Fatalf("NewWithDatabase() error = %v", err)
	}
	if pairs != nil {
		ms, err := st.OpenSecretMappingStore(mappingPath)
		if err != nil {
			_ = st.Close()
			t.Fatalf("OpenSecretMappingStore() error = %v", err)
		}
		for _, pair := range pairs {
			if err := ms.AppendMapping(pair); err != nil {
				_ = ms.Close()
				_ = st.Close()
				t.Fatalf("AppendMapping() error = %v", err)
			}
		}
		if err := ms.Close(); err != nil {
			_ = st.Close()
			t.Fatalf("mapping Close() error = %v", err)
		}
	}
	if len(audits) > 0 {
		records := make([]store.CredentialMaskAuditRecord, 0, len(audits))
		for i, audit := range audits {
			detectedAt := audit.detectedAt
			if detectedAt.IsZero() {
				detectedAt = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
			}
			traceID := audit.traceID
			if traceID == "" {
				traceID = fmt.Sprintf("trace-secret-mapping-%d", i)
			}
			records = append(records, store.CredentialMaskAuditRecord{
				TraceID:    traceID,
				EventIndex: 0,
				Rule:       audit.rule,
				Kind:       "api_key",
				Confidence: "high",
				Score:      90,
				Original:   audit.original,
				StandIn:    audit.standIn,
				DetectedAt: detectedAt,
			})
		}
		if err := st.UpsertCredentialMaskAudits(records); err != nil {
			_ = st.Close()
			t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Store.Close() error = %v", err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	configBody := fmt.Sprintf(`server:
  port: "0"
monitor:
  port: "0"
database:
  driver: sqlite
  dsn: %q
  auto_migrate: %t
trace:
  output_dir: %q
secret_masking:
  enabled: true
  mapping_store:
    enabled: true
`, dbPath, autoMigrate, dir)
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	return &dbSecretMappingFixture{dir: dir, configPath: configPath, dbPath: dbPath, mappingPath: mappingPath}
}

type secretMappingCLIEnvelope struct {
	OK       bool            `json:"ok"`
	Command  string          `json:"command"`
	Warnings []string        `json:"warnings"`
	Result   json.RawMessage `json:"result"`
	Error    *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeSecretMappingCLIEnvelope(t *testing.T, payload string) secretMappingCLIEnvelope {
	t.Helper()
	var envelope secretMappingCLIEnvelope
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", payload, err)
	}
	return envelope
}

type secretMappingStatsResult struct {
	Path                 string `json:"path"`
	Exists               bool   `json:"exists"`
	EncryptionMode       string `json:"encryption_mode"`
	FileSizeBytes        int64  `json:"file_size_bytes"`
	EntryCount           int    `json:"entry_count"`
	IndexCount           int    `json:"index_count"`
	EncryptedRows        int    `json:"encrypted_rows"`
	PlaintextRows        int    `json:"plaintext_rows"`
	UnreadableRows       int    `json:"unreadable_rows"`
	DanglingIndexEntries int    `json:"dangling_index_entries"`
	MinSeq               uint64 `json:"min_seq"`
	MaxSeq               uint64 `json:"max_seq"`
	NextSeq              uint64 `json:"next_seq"`
	MappingStoreFlag     bool   `json:"mapping_store_enabled"`
}

type secretMappingEntryResult struct {
	Seq              uint64 `json:"seq"`
	Encrypted        bool   `json:"encrypted"`
	Unreadable       bool   `json:"unreadable"`
	Real             string `json:"real"`
	Fake             string `json:"fake"`
	RealLength       int    `json:"real_length"`
	FakeLength       int    `json:"fake_length"`
	RealFingerprint  string `json:"real_fingerprint"`
	FakeSHA256Prefix string `json:"fake_sha256_prefix"`
}

type secretMappingListResult struct {
	Path     string                     `json:"path"`
	Exists   bool                       `json:"exists"`
	Redacted bool                       `json:"redacted"`
	Total    int                        `json:"total"`
	Returned int                        `json:"returned"`
	Limit    int                        `json:"limit"`
	Entries  []secretMappingEntryResult `json:"entries"`
	Warnings []string                   `json:"warnings"`
}

type secretMappingDeleteResult struct {
	Path        string                     `json:"path"`
	DryRun      bool                       `json:"dry_run"`
	Mutated     bool                       `json:"mutated"`
	Selector    string                     `json:"selector"`
	Matched     int                        `json:"matched"`
	Deleted     int                        `json:"deleted"`
	WouldDelete int                        `json:"would_delete"`
	Entries     []secretMappingEntryResult `json:"entries"`
	Warnings    []string                   `json:"warnings"`
}

type secretMappingPruneResult struct {
	Path                string                     `json:"path"`
	DryRun              bool                       `json:"dry_run"`
	Mutated             bool                       `json:"mutated"`
	Rule                string                     `json:"rule"`
	Since               string                     `json:"since"`
	Until               string                     `json:"until"`
	PurgeAudits         bool                       `json:"purge_audits"`
	AuditRows           int                        `json:"audit_rows"`
	OriginalValues      int                        `json:"original_values"`
	MatchedMappings     int                        `json:"matched_mappings"`
	DeletedMappings     int                        `json:"deleted_mappings"`
	WouldDeleteMappings int                        `json:"would_delete_mappings"`
	PurgedAuditRows     int                        `json:"purged_audit_rows"`
	UndecryptableRows   int                        `json:"undecryptable_audit_rows"`
	Entries             []secretMappingEntryResult `json:"entries"`
	Warnings            []string                   `json:"warnings"`
}

type secretMappingCompactResult struct {
	Path           string   `json:"path"`
	Exists         bool     `json:"exists"`
	DryRun         bool     `json:"dry_run"`
	Mutated        bool     `json:"mutated"`
	FileSizeBefore int64    `json:"file_size_before"`
	FileSizeAfter  int64    `json:"file_size_after"`
	Entries        int      `json:"entries"`
	IndexEntries   int      `json:"index_entries"`
	BackupPath     string   `json:"backup_path"`
	Reclaimed      int64    `json:"reclaimed_bytes"`
	Warnings       []string `json:"warnings"`
}

func TestDBSecretMappingStatsJSONEnvelope(t *testing.T) {
	pairs := []secretmask.Mapping{
		secretMappingCLITestPair("cli-stats-one-", "A", "a"),
		secretMappingCLITestPair("cli-stats-two-", "B", "b"),
	}
	fixture := newDBSecretMappingFixture(t, pairs, nil, true)

	code, stdout, stderr := captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "stats"})
	if code != 0 {
		t.Fatalf("stats exit = %d, want 0 (stderr=%q)", code, stderr)
	}
	envelope := decodeSecretMappingCLIEnvelope(t, stdout)
	if !envelope.OK || envelope.Command != "db.secret-mapping.stats" {
		t.Fatalf("envelope = %+v, want ok db.secret-mapping.stats", envelope)
	}
	var result secretMappingStatsResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("unmarshal stats result error = %v", err)
	}
	if result.Path != fixture.mappingPath {
		t.Fatalf("path = %q, want %q", result.Path, fixture.mappingPath)
	}
	if !result.Exists || !result.MappingStoreFlag {
		t.Fatalf("exists/mapping_store_enabled = %v/%v, want true/true", result.Exists, result.MappingStoreFlag)
	}
	if result.EncryptionMode != "encrypted-local" {
		t.Fatalf("encryption_mode = %q, want encrypted-local", result.EncryptionMode)
	}
	if result.EntryCount != 2 || result.IndexCount != 2 {
		t.Fatalf("entries/index = %d/%d, want 2/2", result.EntryCount, result.IndexCount)
	}
	if result.MinSeq != 1 || result.MaxSeq != 2 || result.NextSeq != 2 {
		t.Fatalf("seq = %d..%d last %d, want 1..2 last 2", result.MinSeq, result.MaxSeq, result.NextSeq)
	}
	if result.FileSizeBytes <= 0 {
		t.Fatalf("file_size_bytes = %d, want a positive size", result.FileSizeBytes)
	}
	if result.UnreadableRows != 0 || result.PlaintextRows != 0 {
		t.Fatalf("unreadable/plaintext = %d/%d, want 0/0", result.UnreadableRows, result.PlaintextRows)
	}
}

func TestDBSecretMappingStatsMissingFileIsNotAnError(t *testing.T) {
	fixture := newDBSecretMappingFixture(t, nil, nil, true)

	code, stdout, stderr := captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "stats"})
	if code != 0 {
		t.Fatalf("stats exit = %d, want 0 for a missing mapping file (stderr=%q)", code, stderr)
	}
	envelope := decodeSecretMappingCLIEnvelope(t, stdout)
	var result secretMappingStatsResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("unmarshal stats result error = %v", err)
	}
	if result.Exists {
		t.Fatal("exists = true, want false for a missing mapping file")
	}
	if result.EntryCount != 0 || result.MaxSeq != 0 {
		t.Fatalf("entries/max_seq = %d/%d, want zeroes", result.EntryCount, result.MaxSeq)
	}
}

func TestDBSecretMappingListRedactsValuesByDefault(t *testing.T) {
	first := secretMappingCLITestPair("cli-list-one-", "A", "a")
	second := secretMappingCLITestPair("cli-list-two-", "B", "b")
	fixture := newDBSecretMappingFixture(t, []secretmask.Mapping{first, second}, nil, true)

	code, stdout, stderr := captureRunOutput(t, []string{"-c", fixture.configPath, "db", "secret-mapping", "list"})
	if code != 0 {
		t.Fatalf("list exit = %d, want 0 (stderr=%q)", code, stderr)
	}
	for _, want := range []string{"redacted: true", "total: 2", "returned: 2", "seq=2", "real_length=", "real_fingerprint="} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("list text = %q, want contain %q", stdout, want)
		}
	}
	for _, secret := range []string{first.Real, first.Fake, second.Real, second.Fake} {
		if strings.Contains(stdout, secret) {
			t.Fatalf("list text leaked a mapping value %q", secret)
		}
	}

	code, stdout, stderr = captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "list", "--show-values"})
	if code != 0 {
		t.Fatalf("list --show-values exit = %d, want 0 (stderr=%q)", code, stderr)
	}
	envelope := decodeSecretMappingCLIEnvelope(t, stdout)
	var result secretMappingListResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("unmarshal list result error = %v", err)
	}
	if result.Redacted || !result.Entries[0].Encrypted {
		t.Fatalf("redacted/encrypted = %v/%v, want false/true", result.Redacted, result.Entries[0].Encrypted)
	}
	if result.Entries[0].Real != second.Real || result.Entries[0].Fake != second.Fake {
		t.Fatalf("newest entry = %q -> %q, want the newest pair", result.Entries[0].Real, result.Entries[0].Fake)
	}
	if result.Entries[0].RealFingerprint == "" || len(result.Entries[0].RealFingerprint) != 16 {
		t.Fatalf("real_fingerprint = %q, want a 16-character prefix", result.Entries[0].RealFingerprint)
	}
}

func TestDBSecretMappingListContainsAndLimit(t *testing.T) {
	pairs := []secretmask.Mapping{
		secretMappingCLITestPair("cli-shared-", "A", "a"),
		secretMappingCLITestPair("cli-shared-", "B", "b"),
		secretMappingCLITestPair("cli-solo-", "C", "c"),
	}
	fixture := newDBSecretMappingFixture(t, pairs, nil, true)

	code, stdout, stderr := captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "list", "--contains", "cli-shared-"})
	if code != 0 {
		t.Fatalf("list --contains exit = %d (stderr=%q)", code, stderr)
	}
	envelope := decodeSecretMappingCLIEnvelope(t, stdout)
	var result secretMappingListResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("unmarshal list result error = %v (stderr=%q)", err, stderr)
	}
	if result.Total != 2 || result.Returned != 2 {
		t.Fatalf("contains total/returned = %d/%d, want 2/2", result.Total, result.Returned)
	}

	code, stdout, stderr = captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "list", "--contains", "cli-shared-", "--limit", "1"})
	if code != 0 {
		t.Fatalf("list --limit exit = %d (stderr=%q)", code, stderr)
	}
	result = secretMappingListResult{}
	if err := json.Unmarshal(decodeSecretMappingCLIEnvelope(t, stdout).Result, &result); err != nil {
		t.Fatalf("unmarshal list --limit result error = %v", err)
	}
	if result.Total != 2 || result.Returned != 1 {
		t.Fatalf("limit total/returned = %d/%d, want 2/1", result.Total, result.Returned)
	}

	code, stdout, stderr = captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "list", "--contains", "cli-shared-", "--all"})
	if code != 0 {
		t.Fatalf("list --all exit = %d (stderr=%q)", code, stderr)
	}
	result = secretMappingListResult{}
	if err := json.Unmarshal(decodeSecretMappingCLIEnvelope(t, stdout).Result, &result); err != nil {
		t.Fatalf("unmarshal list --all result error = %v", err)
	}
	if result.Total != 2 || result.Returned != 2 || result.Limit != 0 {
		t.Fatalf("all total/returned/limit = %d/%d/%d, want 2/2/0", result.Total, result.Returned, result.Limit)
	}
}

func TestDBSecretMappingDeleteRequiresExactlyOneSelector(t *testing.T) {
	fixture := newDBSecretMappingFixture(t, nil, nil, true)
	cases := [][]string{
		{"-c", fixture.configPath, "db", "secret-mapping", "delete"},
		{"-c", fixture.configPath, "db", "secret-mapping", "delete", "--real", "x", "--seq", "1"},
		{"-c", fixture.configPath, "db", "secret-mapping", "delete", "--contains", "x", "--seq", "1"},
	}
	for _, args := range cases {
		code, _, stderr := captureRunOutput(t, args)
		if code != ExitCodeUsage {
			t.Fatalf("delete %v exit = %d, want %d (stderr=%q)", args, code, ExitCodeUsage, stderr)
		}
		if !strings.Contains(stderr, "exactly one of --real, --seq or --contains") {
			t.Fatalf("delete %v stderr = %q, want the selector usage message", args, stderr)
		}
	}

	code, _, stderr := captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "delete"})
	if code != ExitCodeUsage {
		t.Fatalf("delete JSON exit = %d, want %d", code, ExitCodeUsage)
	}
	envelope := decodeCLIEnvelope(t, stderr)
	if envelope.Error == nil || envelope.Error.Code != "USAGE_ERROR" {
		t.Fatalf("delete JSON envelope = %+v, want USAGE_ERROR", envelope)
	}
}

func TestDBSecretMappingDeleteDryRunThenApply(t *testing.T) {
	pairs := []secretmask.Mapping{
		secretMappingCLITestPair("cli-del-one-", "A", "a"),
		secretMappingCLITestPair("cli-del-two-", "B", "b"),
		secretMappingCLITestPair("cli-del-three-", "C", "c"),
	}
	fixture := newDBSecretMappingFixture(t, pairs, nil, true)

	code, stdout, stderr := captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "delete", "--seq", "2"})
	if code != 0 {
		t.Fatalf("delete dry-run exit = %d, want 0 (stderr=%q)", code, stderr)
	}
	envelope := decodeSecretMappingCLIEnvelope(t, stdout)
	var result secretMappingDeleteResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("unmarshal delete result error = %v", err)
	}
	if !result.DryRun || result.Mutated || result.Matched != 1 || result.WouldDelete != 1 || result.Deleted != 0 {
		t.Fatalf("delete dry-run result = %+v, want dry-run matched 1", result)
	}
	if result.Selector != "seq" || result.Entries[0].Seq != 2 {
		t.Fatalf("delete dry-run selector/seq = %q/%d, want seq/2", result.Selector, result.Entries[0].Seq)
	}
	if !containsWarning(envelope.Warnings, dbSecretMappingRestartWarning) || !containsWarning(envelope.Warnings, dbSecretMappingUnrecognisableWarning) {
		t.Fatalf("delete dry-run warnings = %v, want the restart and unrecognisable warnings", envelope.Warnings)
	}

	statsBefore := secretMappingStatsForTest(t, fixture.configPath)
	if statsBefore.EntryCount != 3 {
		t.Fatalf("entry count after dry-run = %d, want the file untouched at 3", statsBefore.EntryCount)
	}

	code, stdout, stderr = captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "delete", "--seq", "2", "--apply"})
	if code != 0 {
		t.Fatalf("delete --apply exit = %d, want 0 (stderr=%q)", code, stderr)
	}
	result = secretMappingDeleteResult{}
	if err := json.Unmarshal(decodeSecretMappingCLIEnvelope(t, stdout).Result, &result); err != nil {
		t.Fatalf("unmarshal delete --apply result error = %v", err)
	}
	if result.DryRun || !result.Mutated || result.Deleted != 1 || result.Matched != 1 {
		t.Fatalf("delete --apply result = %+v, want mutated deleted 1", result)
	}
	statsAfter := secretMappingStatsForTest(t, fixture.configPath)
	if statsAfter.EntryCount != 2 || statsAfter.IndexCount != 2 || statsAfter.DanglingIndexEntries != 0 {
		t.Fatalf("stats after delete = %+v, want 2 entries and a clean index", statsAfter)
	}
}

func TestDBSecretMappingDeleteByRealAndContains(t *testing.T) {
	first := secretMappingCLITestPair("cli-real-delete-", "A", "a")
	sharedOne := secretMappingCLITestPair("cli-contains-", "B", "b")
	sharedTwo := secretMappingCLITestPair("cli-contains-", "C", "c")
	keep := secretMappingCLITestPair("cli-keep-", "D", "d")
	fixture := newDBSecretMappingFixture(t, []secretmask.Mapping{first, sharedOne, sharedTwo, keep}, nil, true)

	code, stdout, stderr := captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "delete", "--real", first.Real, "--apply"})
	if code != 0 {
		t.Fatalf("delete --real exit = %d (stderr=%q)", code, stderr)
	}
	var result secretMappingDeleteResult
	if err := json.Unmarshal(decodeSecretMappingCLIEnvelope(t, stdout).Result, &result); err != nil {
		t.Fatalf("unmarshal delete --real result error = %v", err)
	}
	if result.Selector != "real" || result.Deleted != 1 {
		t.Fatalf("delete --real result = %+v, want selector real and one deletion", result)
	}

	code, stdout, stderr = captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "delete", "--contains", "cli-contains-", "--apply"})
	if code != 0 {
		t.Fatalf("delete --contains exit = %d (stderr=%q)", code, stderr)
	}
	result = secretMappingDeleteResult{}
	if err := json.Unmarshal(decodeSecretMappingCLIEnvelope(t, stdout).Result, &result); err != nil {
		t.Fatalf("unmarshal delete --contains result error = %v", err)
	}
	if result.Selector != "contains" || result.Deleted != 2 {
		t.Fatalf("delete --contains result = %+v, want selector contains and two deletions", result)
	}

	stats := secretMappingStatsForTest(t, fixture.configPath)
	if stats.EntryCount != 1 || stats.IndexCount != 1 {
		t.Fatalf("stats after selector deletes = %d/%d, want 1/1", stats.EntryCount, stats.IndexCount)
	}
	list := secretMappingListForTest(t, fixture.configPath)
	if len(list.Entries) != 1 {
		t.Fatalf("surviving entries = %d, want 1", len(list.Entries))
	}
	if list.Entries[0].Real != keep.Real {
		t.Fatalf("surviving entry = %q, want the kept pair", list.Entries[0].Real)
	}
}

func TestDBSecretMappingPruneUsesAuditOriginals(t *testing.T) {
	one := secretMappingCLITestPair("cli-prune-one-", "A", "a")
	two := secretMappingCLITestPair("cli-prune-two-", "B", "b")
	other := secretMappingCLITestPair("cli-prune-other-", "C", "c")
	base := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	audits := []dbSecretMappingAuditSeed{
		{rule: "env.secret_assignment", original: one.Real, standIn: one.Fake, detectedAt: base},
		{rule: "env.secret_assignment", original: two.Real, standIn: two.Fake, detectedAt: base.Add(2 * time.Hour)},
		{rule: "openai.project_key", original: other.Real, standIn: other.Fake, detectedAt: base.Add(4 * time.Hour)},
	}
	fixture := newDBSecretMappingFixture(t, []secretmask.Mapping{one, two, other}, audits, true)

	code, stdout, stderr := captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "prune", "--rule", "env.secret_assignment"})
	if code != 0 {
		t.Fatalf("prune dry-run exit = %d (stderr=%q)", code, stderr)
	}
	envelope := decodeSecretMappingCLIEnvelope(t, stdout)
	var result secretMappingPruneResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("unmarshal prune result error = %v", err)
	}
	if !result.DryRun || result.Mutated || result.AuditRows != 2 || result.OriginalValues != 2 {
		t.Fatalf("prune dry-run result = %+v, want dry-run with 2 audit rows and 2 originals", result)
	}
	if result.MatchedMappings != 2 || result.DeletedMappings != 0 || result.WouldDeleteMappings != 2 {
		t.Fatalf("prune dry-run matches = %+v, want 2 matched / 0 deleted", result)
	}
	if stats := secretMappingStatsForTest(t, fixture.configPath); stats.EntryCount != 3 {
		t.Fatalf("entry count after prune dry-run = %d, want the file untouched at 3", stats.EntryCount)
	}

	code, stdout, stderr = captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "prune", "--rule", "env.secret_assignment", "--apply"})
	if code != 0 {
		t.Fatalf("prune --apply exit = %d (stderr=%q)", code, stderr)
	}
	result = secretMappingPruneResult{}
	if err := json.Unmarshal(decodeSecretMappingCLIEnvelope(t, stdout).Result, &result); err != nil {
		t.Fatalf("unmarshal prune --apply result error = %v", err)
	}
	if result.DryRun || !result.Mutated || result.DeletedMappings != 2 || result.PurgedAuditRows != 0 {
		t.Fatalf("prune --apply result = %+v, want 2 mapping deletions and no audit purge", result)
	}
	if stats := secretMappingStatsForTest(t, fixture.configPath); stats.EntryCount != 1 {
		t.Fatalf("entry count after prune = %d, want only the unmapped rule's entry left", stats.EntryCount)
	}
	// The audits are kept unless --purge-audits is given.
	if total := secretMappingAuditTotalForTest(t, fixture); total != 3 {
		t.Fatalf("audit rows after plain prune = %d, want 3", total)
	}

	// A window excludes the second row, so only the first mapping is pruned; the
	// second run also purges the matching audit rows it selected.
	code, stdout, stderr = captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "prune", "--rule", "env.secret_assignment", "--since", base.Add(1 * time.Hour).Format(time.RFC3339), "--apply", "--purge-audits"})
	if code != 0 {
		t.Fatalf("prune --since --purge-audits exit = %d (stderr=%q)", code, stderr)
	}
	result = secretMappingPruneResult{}
	if err := json.Unmarshal(decodeSecretMappingCLIEnvelope(t, stdout).Result, &result); err != nil {
		t.Fatalf("unmarshal windowed prune result error = %v", err)
	}
	if result.AuditRows != 1 || result.PurgedAuditRows != 1 || !containsWarning(result.Warnings, dbSecretMappingPurgeAuditWarning) {
		t.Fatalf("windowed prune result = %+v, want one purged audit row and the purge warning", result)
	}
	if total := secretMappingAuditTotalForTest(t, fixture); total != 2 {
		t.Fatalf("audit rows after purge = %d, want 2 (one windowed rule row and the other rule)", total)
	}
}

func TestDBSecretMappingPruneValidation(t *testing.T) {
	fixture := newDBSecretMappingFixture(t, nil, nil, true)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing rule", args: []string{"prune"}, want: "--rule is required"},
		{name: "bad since", args: []string{"prune", "--rule", "r", "--since", "yesterday"}, want: "RFC3339"},
		{name: "bad until", args: []string{"prune", "--rule", "r", "--until", "2026-11-01"}, want: "RFC3339"},
		{name: "since after until", args: []string{"prune", "--rule", "r", "--since", "2026-11-02T00:00:00Z", "--until", "2026-11-01T00:00:00Z"}, want: "must not be after"},
	}
	for _, tc := range cases {
		args := append([]string{"-c", fixture.configPath, "db", "secret-mapping"}, tc.args...)
		code, _, stderr := captureRunOutput(t, args)
		if code != ExitCodeUsage {
			t.Fatalf("%s exit = %d, want %d (stderr=%q)", tc.name, code, ExitCodeUsage, stderr)
		}
		if !strings.Contains(stderr, tc.want) {
			t.Fatalf("%s stderr = %q, want contain %q", tc.name, stderr, tc.want)
		}
	}
}

func TestDBSecretMappingCompactDryRunThenApply(t *testing.T) {
	pairs := []secretmask.Mapping{
		secretMappingCLITestPair("cli-compact-one-", "A", "a"),
		secretMappingCLITestPair("cli-compact-two-", "B", "b"),
		secretMappingCLITestPair("cli-compact-three-", "C", "c"),
	}
	fixture := newDBSecretMappingFixture(t, pairs, nil, true)

	code, stdout, stderr := captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "compact"})
	if code != 0 {
		t.Fatalf("compact dry-run exit = %d (stderr=%q)", code, stderr)
	}
	var result secretMappingCompactResult
	if err := json.Unmarshal(decodeSecretMappingCLIEnvelope(t, stdout).Result, &result); err != nil {
		t.Fatalf("unmarshal compact dry-run result error = %v", err)
	}
	if !result.Exists || !result.DryRun || result.Mutated {
		t.Fatalf("compact dry-run result = %+v, want existing, dry-run, not mutated", result)
	}
	if result.FileSizeBefore <= 0 || result.BackupPath != "" {
		t.Fatalf("compact dry-run sizes/backup = %d/%q, want a size and no backup yet", result.FileSizeBefore, result.BackupPath)
	}

	code, stdout, stderr = captureRunOutput(t, []string{"-c", fixture.configPath, "--format", "json", "db", "secret-mapping", "compact", "--apply"})
	if code != 0 {
		t.Fatalf("compact --apply exit = %d (stderr=%q)", code, stderr)
	}
	envelope := decodeSecretMappingCLIEnvelope(t, stdout)
	result = secretMappingCompactResult{}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("unmarshal compact --apply result error = %v", err)
	}
	if !result.Mutated || result.Entries != 3 || result.IndexEntries != 3 {
		t.Fatalf("compact --apply result = %+v, want mutated with 3 entries", result)
	}
	if result.FileSizeAfter <= 0 || result.BackupPath == "" {
		t.Fatalf("compact --apply sizes/backup = %d/%q, want a size and a backup path", result.FileSizeAfter, result.BackupPath)
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("Stat(backup) error = %v", err)
	}
	if !containsWarning(envelope.Warnings, dbSecretMappingBackupWarning) {
		t.Fatalf("compact warnings = %v, want the backup warning", envelope.Warnings)
	}
	stats := secretMappingStatsForTest(t, fixture.configPath)
	if stats.EntryCount != 3 || stats.IndexCount != 3 || stats.UnreadableRows != 0 {
		t.Fatalf("stats after compact = %+v, want 3 readable entries", stats)
	}
}

func TestDBSecretMappingLockedReportsStopServe(t *testing.T) {
	fixture := newDBSecretMappingFixture(t, []secretmask.Mapping{secretMappingCLITestPair("cli-lock-", "A", "a")}, nil, false)

	// Hold the bbolt file the way a running serve would.
	st, err := store.NewWithDatabase(fixture.dir, "sqlite", fixture.dbPath, 4, 4)
	if err != nil {
		t.Fatalf("reopen store error = %v", err)
	}
	defer st.Close()
	held, err := st.OpenSecretMappingStore(fixture.mappingPath)
	if err != nil {
		t.Fatalf("hold OpenSecretMappingStore() error = %v", err)
	}
	defer held.Close()

	code, _, stderr := captureRunOutput(t, []string{"-c", fixture.configPath, "db", "secret-mapping", "stats"})
	if code != ExitCodeAPI {
		t.Fatalf("stats while locked exit = %d, want %d (stderr=%q)", code, ExitCodeAPI, stderr)
	}
	for _, want := range []string{"stop serve first", "locked by another process"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("locked stats stderr = %q, want contain %q", stderr, want)
		}
	}
}

func TestDBSecretMappingCommandsAreRegistered(t *testing.T) {
	cmd := newRootCommand()
	for _, want := range []string{"db secret-mapping", "db secret-mapping stats", "db secret-mapping list", "db secret-mapping delete", "db secret-mapping prune", "db secret-mapping compact"} {
		found, _, err := cmd.Find(strings.Fields(want))
		if err != nil || found.CommandPath() != rootName+" "+want {
			t.Fatalf("root command missing %q: found=%v err=%v", want, found, err)
		}
	}
}

func TestDBSecretMappingBareGroupRequiresSubcommand(t *testing.T) {
	code, _, stderr := captureRunOutput(t, []string{"db", "secret-mapping"})
	if code != ExitCodeUsage {
		t.Fatalf("bare group exit = %d, want %d (stderr=%q)", code, ExitCodeUsage, stderr)
	}
}

func containsWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if warning == want {
			return true
		}
	}
	return false
}

func secretMappingStatsForTest(t *testing.T, configPath string) secretMappingStatsResult {
	t.Helper()
	code, stdout, stderr := captureRunOutput(t, []string{"-c", configPath, "--format", "json", "db", "secret-mapping", "stats"})
	if code != 0 {
		t.Fatalf("stats helper exit = %d (stderr=%q)", code, stderr)
	}
	var result secretMappingStatsResult
	if err := json.Unmarshal(decodeSecretMappingCLIEnvelope(t, stdout).Result, &result); err != nil {
		t.Fatalf("unmarshal stats helper result error = %v", err)
	}
	return result
}

func secretMappingListForTest(t *testing.T, configPath string) secretMappingListResult {
	t.Helper()
	code, stdout, stderr := captureRunOutput(t, []string{"-c", configPath, "--format", "json", "db", "secret-mapping", "list", "--all", "--show-values"})
	if code != 0 {
		t.Fatalf("list helper exit = %d (stderr=%q)", code, stderr)
	}
	var result secretMappingListResult
	if err := json.Unmarshal(decodeSecretMappingCLIEnvelope(t, stdout).Result, &result); err != nil {
		t.Fatalf("unmarshal list helper result error = %v", err)
	}
	return result
}

func secretMappingAuditTotalForTest(t *testing.T, fixture *dbSecretMappingFixture) int {
	t.Helper()
	st, err := store.NewWithDatabase(fixture.dir, "sqlite", fixture.dbPath, 4, 4)
	if err != nil {
		t.Fatalf("reopen store error = %v", err)
	}
	defer st.Close()
	page, err := st.ListCredentialMaskAudits(store.CredentialMaskAuditFilter{})
	if err != nil {
		t.Fatalf("ListCredentialMaskAudits() error = %v", err)
	}
	return page.Total
}
