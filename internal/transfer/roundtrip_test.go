package transfer

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// fixture builds one source deployment: a channel with one model, two cassettes
// recorded in a time window, a mask audit row, an analysis run and a session.
type fixture struct {
	dir        string
	store      *store.Store
	channelID  string
	cassetteA  string
	cassetteB  string
	traceA     string
	traceB     string
	recordedAt time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })

	f := &fixture{dir: dir, store: st, channelID: "office-channel", recordedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               f.channelID,
		Name:             "Office",
		BaseURL:          "https://api.example.com/v1/",
		ProviderPreset:   "openai",
		APIKeyCiphertext: []byte("sk-office-secret"),
		HeadersJSON:      `{"X-Test":"1"}`,
		Enabled:          true,
		Priority:         5,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if _, err := st.UpsertChannelModel(f.channelID, store.ChannelModelRecord{
		ChannelID:     f.channelID,
		Model:         "gpt-office",
		DisplayName:   "GPT Office",
		Source:        "manual",
		Enabled:       true,
		UpstreamModel: "gpt-5",
	}); err != nil {
		t.Fatalf("UpsertChannelModel() error = %v", err)
	}

	f.cassetteA = f.writeCassette(t, "api.example.com/gpt-office/2026/09/10/a.http", "a")
	f.cassetteB = f.writeCassette(t, "api.example.com/gpt-office/2026/09/10/b.http", "b")
	f.traceA = f.indexCassette(t, f.cassetteA, "gpt-office", "sess-office", f.recordedAt)
	f.traceB = f.indexCassette(t, f.cassetteB, "gpt-office", "sess-office", f.recordedAt.Add(time.Minute))

	if err := st.UpsertCredentialMaskAudits([]store.CredentialMaskAuditRecord{{
		TraceID:    f.traceA,
		EventIndex: 0,
		Rule:       "openai_api_key",
		Kind:       "api-key",
		Original:   "sk-original-value",
		StandIn:    "sk-standin-value",
		DetectedAt: f.recordedAt,
	}}); err != nil {
		t.Fatalf("SaveCredentialMaskAudits() error = %v", err)
	}
	return f
}

func (f *fixture) writeCassette(t *testing.T, rel, marker string) string {
	t.Helper()
	path := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	body := "# trajecta/v3\n# meta: {\"request_id\":\"" + marker + "\"}\nPOST /v1/chat/completions HTTP/1.1\n\n{}\nHTTP/1.1 200 OK\n\n{}"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	// A stable mtime so the export/import round trip can be compared exactly.
	mt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	return path
}

func (f *fixture) indexCassette(t *testing.T, path, model, session string, at time.Time) string {
	t.Helper()
	header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
	header.Meta.RequestID = "req-" + filepath.Base(path)
	header.Meta.Time = at
	header.Meta.URL = "https://api.example.com/v1/chat/completions"
	header.Meta.Method = http.MethodPost
	header.Meta.StatusCode = http.StatusOK
	header.Meta.Model = model
	header.Meta.Provider = "openai"
	header.Meta.Operation = "chat"
	header.Meta.Endpoint = "/v1/chat/completions"
	header.Meta.ChannelID = f.channelID
	header.Meta.RouteTargetID = f.channelID + ":default"
	header.Layout.IsStream = false
	header.Usage.TotalTokens = 7
	traceID, err := f.store.UpsertLogWithGroupingAndTraceID(path, header, store.GroupingInfo{SessionID: session, SessionSource: "header"})
	if err != nil {
		t.Fatalf("UpsertLogWithGroupingAndTraceID() error = %v", err)
	}
	return traceID
}

func TestExportImportRoundTrip(t *testing.T) {
	source := newFixture(t)
	bundle := filepath.Join(t.TempDir(), "office.tar.gz")

	exp, err := Export(context.Background(), source.store, source.dir, ExportOptions{
		Since:    ptrTime(source.recordedAt.Add(-time.Hour)),
		Until:    ptrTime(source.recordedAt.Add(time.Hour)),
		Out:      bundle,
		Password: "test-password",
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if exp.Cassettes.Count != 2 {
		t.Fatalf("exported cassettes = %d, want 2", exp.Cassettes.Count)
	}

	target := newTargetStore(t)
	res, err := Import(context.Background(), target, targetDir(t, target), ImportOptions{
		Bundle:   bundle,
		Password: "test-password",
		Apply:    true,
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	// The trace ids must survive: every derived table joins on them.
	var gotTraceA string
	if err := target.TransferQueryRow(context.Background(), `SELECT trace_id FROM logs WHERE model = ?`, "gpt-office").Scan(&gotTraceA); err != nil {
		t.Fatalf("read imported logs: %v", err)
	}
	if gotTraceA != source.traceA {
		t.Fatalf("imported trace_id = %q, want %q", gotTraceA, source.traceA)
	}
	if res.SessionRebuilt != true {
		t.Fatal("SessionRebuilt = false, want the summaries rebuilt after an apply import")
	}
	var sessions int64
	if err := target.TransferQueryRow(context.Background(), `SELECT COUNT(*) FROM session_summaries WHERE session_id = ?`, "sess-office").Scan(&sessions); err != nil {
		t.Fatalf("read session_summaries: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("session_summaries rows = %d, want 1", sessions)
	}

	// A second import of the same bundle must change nothing.
	again, err := Import(context.Background(), target, targetDir(t, target), ImportOptions{
		Bundle:   bundle,
		Password: "test-password",
		Apply:    true,
	})
	if err != nil {
		t.Fatalf("second Import() error = %v", err)
	}
	for _, table := range again.Tables {
		if table.Inserted != 0 || table.Updated != 0 {
			t.Fatalf("second import wrote table %s: inserted=%d updated=%d, want 0/0", table.Name, table.Inserted, table.Updated)
		}
	}
	if again.Cassettes.Copied != 0 || again.Cassettes.Updated != 0 {
		t.Fatalf("second import touched cassettes: copied=%d updated=%d", again.Cassettes.Copied, again.Cassettes.Updated)
	}
}

func TestImportMergesChannelByBaseURLAndKey(t *testing.T) {
	source := newFixture(t)
	bundle := filepath.Join(t.TempDir(), "merge.tar.gz")
	if _, err := Export(context.Background(), source.store, source.dir, ExportOptions{
		Since:    ptrTime(source.recordedAt.Add(-time.Hour)),
		Until:    ptrTime(source.recordedAt.Add(time.Hour)),
		Out:      bundle,
		Password: "pw",
	}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}

	target := newTargetStore(t)
	// Same endpoint and same credential, different id and name: the resolver
	// must reuse this channel and only union the model.
	if _, err := target.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "home-channel",
		Name:             "Home",
		BaseURL:          "https://api.example.com/v1",
		ProviderPreset:   "openai",
		APIKeyCiphertext: []byte("sk-office-secret"),
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}

	res, err := Import(context.Background(), target, targetDir(t, target), ImportOptions{
		Bundle: bundle, Password: "pw", Apply: true,
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if len(res.ChannelReuses) != 1 || res.ChannelReuses[0].Matched != "base_url+key" {
		t.Fatalf("channel reuses = %+v, want one base_url+key match", res.ChannelReuses)
	}
	if res.ChannelReuses[0].TargetID != "home-channel" {
		t.Fatalf("reused target = %q, want home-channel", res.ChannelReuses[0].TargetID)
	}

	// The imported logs must point at the target channel id.
	var channelID string
	if err := target.TransferQueryRow(context.Background(), `SELECT channel_id FROM logs LIMIT 1`).Scan(&channelID); err != nil {
		t.Fatalf("read logs channel_id: %v", err)
	}
	if channelID != "home-channel" {
		t.Fatalf("logs.channel_id = %q, want home-channel", channelID)
	}
	// And the model must now exist under the target channel.
	var models int64
	if err := target.TransferQueryRow(context.Background(),
		`SELECT COUNT(*) FROM channel_models WHERE channel_id = ? AND model = ?`, "home-channel", "gpt-office").Scan(&models); err != nil {
		t.Fatalf("read channel_models: %v", err)
	}
	if models != 1 {
		t.Fatalf("channel_models rows = %d, want 1", models)
	}
}

func TestImportCreatesChannelWhenKeyDiffers(t *testing.T) {
	source := newFixture(t)
	bundle := filepath.Join(t.TempDir(), "diffkey.tar.gz")
	if _, err := Export(context.Background(), source.store, source.dir, ExportOptions{
		Since:    ptrTime(source.recordedAt.Add(-time.Hour)),
		Until:    ptrTime(source.recordedAt.Add(time.Hour)),
		Out:      bundle,
		Password: "pw",
	}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}

	target := newTargetStore(t)
	if _, err := target.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "home-channel",
		Name:             "Home",
		BaseURL:          "https://api.example.com/v1",
		ProviderPreset:   "openai",
		APIKeyCiphertext: []byte("sk-a-different-key"),
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}

	res, err := Import(context.Background(), target, targetDir(t, target), ImportOptions{
		Bundle: bundle, Password: "pw", Apply: true,
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if len(res.ChannelReuses) != 0 {
		t.Fatalf("channel reuses = %+v, want none when the api key differs", res.ChannelReuses)
	}
	if len(res.ChannelsCreated) != 1 {
		t.Fatalf("channels created = %v, want exactly one", res.ChannelsCreated)
	}
}

func TestImportDryRunWritesNothing(t *testing.T) {
	source := newFixture(t)
	bundle := filepath.Join(t.TempDir(), "dry.tar.gz")
	if _, err := Export(context.Background(), source.store, source.dir, ExportOptions{
		Since:    ptrTime(source.recordedAt.Add(-time.Hour)),
		Until:    ptrTime(source.recordedAt.Add(time.Hour)),
		Out:      bundle,
		Password: "pw",
	}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}

	target := newTargetStore(t)
	res, err := Import(context.Background(), target, targetDir(t, target), ImportOptions{
		Bundle: bundle, Password: "pw", Apply: false,
	})
	if err != nil {
		t.Fatalf("Import(dry run) error = %v", err)
	}
	if !res.DryRun {
		t.Fatal("DryRun = false")
	}
	var logs int64
	if err := target.TransferQueryRow(context.Background(), `SELECT COUNT(*) FROM logs`).Scan(&logs); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if logs != 0 {
		t.Fatalf("dry run wrote %d logs rows", logs)
	}
	if _, err := os.Stat(filepath.Join(targetDir(t, target), "api.example.com")); !os.IsNotExist(err) {
		t.Fatalf("dry run created cassette directories: %v", err)
	}
}

func TestImportWrongPasswordFailsCleanly(t *testing.T) {
	source := newFixture(t)
	bundle := filepath.Join(t.TempDir(), "wrongpw.tar.gz")
	if _, err := Export(context.Background(), source.store, source.dir, ExportOptions{
		Since:    ptrTime(source.recordedAt.Add(-time.Hour)),
		Until:    ptrTime(source.recordedAt.Add(time.Hour)),
		Out:      bundle,
		Password: "right",
	}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	target := newTargetStore(t)
	if _, err := Import(context.Background(), target, targetDir(t, target), ImportOptions{
		Bundle: bundle, Password: "wrong", Apply: true,
	}); err == nil {
		t.Fatal("Import() with the wrong password succeeded")
	}
	var logs int64
	if err := target.TransferQueryRow(context.Background(), `SELECT COUNT(*) FROM logs`).Scan(&logs); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if logs != 0 {
		t.Fatalf("failed import wrote %d logs rows", logs)
	}
}

func TestImportRebasesCassettePath(t *testing.T) {
	source := newFixture(t)
	bundle := filepath.Join(t.TempDir(), "rebase.tar.gz")
	if _, err := Export(context.Background(), source.store, source.dir, ExportOptions{
		Since:    ptrTime(source.recordedAt.Add(-time.Hour)),
		Until:    ptrTime(source.recordedAt.Add(time.Hour)),
		Out:      bundle,
		Password: "pw",
	}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}

	target := newTargetStore(t)
	// A different output directory on this side: every path must move with it.
	targetDir := filepath.Join(t.TempDir(), "other-traces")
	if _, err := Import(context.Background(), target, targetDir, ImportOptions{
		Bundle: bundle, Password: "pw", Apply: true,
	}); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	var path string
	if err := target.TransferQueryRow(context.Background(), `SELECT path FROM logs LIMIT 1`).Scan(&path); err != nil {
		t.Fatalf("read logs path: %v", err)
	}
	want := filepath.Join(targetDir, "api.example.com", "gpt-office", "2026", "09", "10", "a.http")
	if path != want {
		t.Fatalf("logs.path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("imported cassette missing at %q: %v", path, err)
	}
}

// --- helpers ---------------------------------------------------------------

func ptrTime(v time.Time) *time.Time { return &v }

func newTargetStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New(target) error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// targetDir returns the output directory the target store was created with.
func targetDir(t *testing.T, st *store.Store) string {
	t.Helper()
	return st.OutputDir()
}

func TestImportUpdatesChangedLogRow(t *testing.T) {
	source := newFixture(t)
	bundle := filepath.Join(t.TempDir(), "update.tar.gz")
	if _, err := Export(context.Background(), source.store, source.dir, ExportOptions{
		Since:    ptrTime(source.recordedAt.Add(-time.Hour)),
		Until:    ptrTime(source.recordedAt.Add(time.Hour)),
		Out:      bundle,
		Password: "pw",
	}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	target := newTargetStore(t)
	if _, err := Import(context.Background(), target, targetDir(t, target), ImportOptions{
		Bundle: bundle, Password: "pw", Apply: true,
	}); err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	// Simulate a stale index row: the cassette on disk is newer than what the
	// row claims, which is what a re-recorded or re-exported trace looks like.
	if _, err := target.TransferExec(context.Background(), `UPDATE logs SET mod_time_ns = 1, file_size = 1`); err != nil {
		t.Fatalf("poke logs: %v", err)
	}

	res, err := Import(context.Background(), target, targetDir(t, target), ImportOptions{
		Bundle: bundle, Password: "pw", Apply: true,
	})
	if err != nil {
		t.Fatalf("second Import() error = %v", err)
	}
	var logs *TableImportReport
	for i := range res.Tables {
		if res.Tables[i].Name == "logs" {
			logs = &res.Tables[i]
		}
	}
	if logs == nil || logs.Updated != 2 {
		t.Fatalf("logs report = %+v, want 2 updated rows", logs)
	}
	var modTime int64
	if err := target.TransferQueryRow(context.Background(),
		`SELECT mod_time_ns FROM logs WHERE path LIKE ?`, "%a.http").Scan(&modTime); err != nil {
		t.Fatalf("read logs mod_time_ns: %v", err)
	}
	if modTime == 1 {
		t.Fatal("changed log row was not refreshed")
	}
}
