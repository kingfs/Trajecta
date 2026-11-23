package transfer

import "time"

// Manifest is the bundle manifest. It is the first tar entry and the only
// part of the bundle an operator can sanity-check without unpacking. Counts
// are pre-computed by a cheap COUNT pass at export time and re-verified by
// import; a mismatch means the bundle was truncated or corrupted in transit
// and the import is aborted before anything lands on disk.
type Manifest struct {
	Format            string        `json:"format"`
	CreatedAt         time.Time     `json:"created_at"`
	TrajectaVersion   string        `json:"trajecta_version"`
	MigrationsVersion int64         `json:"migrations_version"`
	SourceName        string        `json:"source_name,omitempty"`
	SourceOutputDir   string        `json:"source_output_dir"`
	Window            Window        `json:"window"`
	PasswordProtected bool          `json:"password_protected"`
	Compression       string        `json:"compression"` // "gzip" or "none"
	Tables            []TableCount  `json:"tables"`
	Cassettes         CassetteCount `json:"cassettes"`
}

type Window struct {
	Since *time.Time `json:"since,omitempty"`
	Until *time.Time `json:"until,omitempty"`
	All   bool       `json:"all"`
}

type TableCount struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

type CassetteCount struct {
	Count           int64 `json:"count"`
	Bytes           int64 `json:"bytes"`
	ReferencedCount int64 `json:"referenced_count"` // indexed in logs and copied
	OrphanCount     int64 `json:"orphan_count"`     // present on disk but not in logs (--all only)
	MissingFiles    int64 `json:"missing_files"`    // referenced by logs but absent on disk
}

// Table entry names inside the bundle, relative to the bundle root.
const (
	entryManifest        = "manifest.json"
	entryChannelsPrefix  = "db/"
	entryCassettesPrefix = "cassettes/"
)

func tableEntryName(name string) string { return entryChannelsPrefix + name + ".jsonl" }

// tableExportOrder is the tar write order. Imported rows reference other
// rows by id; every idMap a table needs is built by an earlier entry, and the
// logs row a trace belongs to is imported before anything that carries its
// trace_id, or marked skip-on-conflict before it is needed.
var tableExportOrder = []string{
	"channel_configs",
	"channel_models",
	"model_aliases",
	"model_catalog",
	"project_aliases",
	"app_settings",
	"channel_probe_runs",
	"responses",
	"response_items",
	"request_audits",
	"execution_events",
	"tool_call_audits",
	"system_events",
	"analysis_runs",
	"logs",
	"upstream_exchanges",
	"credential_mask_audits",
	"credential_mask_event_states",
}

// legacyEvalImportTables are the retired evaluation tables. Export no longer
// produces them, but a bundle written by an older Trajecta still carries them;
// import reads each entry to the end, counts its rows as legacy-skipped and
// moves on instead of failing on an unknown table. The order here only fixes
// the report order of the skipped tables.
var legacyEvalImportTables = []string{
	"datasets",
	"eval_runs",
	"experiment_runs",
	"dataset_examples",
	"scores",
}

func isLegacyEvalTable(name string) bool {
	for _, table := range legacyEvalImportTables {
		if table == name {
			return true
		}
	}
	return false
}
