package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/appdb"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/secretmask"
	"github.com/spf13/cobra"
)

// Warning text every destructive secret-mapping subcommand carries. It is put in
// both the text output and the JSON envelope so an automated caller cannot miss
// it.
const (
	dbSecretMappingRestartWarning         = "mapping deletions only take effect after serve restarts: a running serve keeps the pairs it loaded at startup in memory, so stop serve before applying and restart it afterwards"
	dbSecretMappingUnrecognisableWarning  = "deleting a real/stand-in pair makes an old stand-in unrecognisable: a value the model still holds can no longer be restored and the real value may be masked again as a new stand-in"
	dbSecretMappingBackupWarning          = "the .bak backup still contains every pair; remove it once the compacted file has been verified"
	dbSecretMappingConfigDisabledWarning  = "secret_masking.mapping_store.enabled is false; serve keeps mapping pairs process-local and does not read this file"
	dbSecretMappingMaskingDisabledWarning = "secret_masking.enabled is false; serve does not mask values, so this mapping file may be stale"
	dbSecretMappingPurgeAuditWarning      = "audit rows are deleted from credential_mask_audits only; the system_events notification rows are left in place"
	dbSecretMappingUnreadableAuditWarning = "audit rows whose original value cannot be decrypted are not used to select mappings; --purge-audits deletes them too, so restore the local key first if they must be kept"
)

// newDBSecretMappingCommand returns the `db secret-mapping` command group. It
// operates on the local bbolt file beside the cassettes, not on the application
// database, and every data-changing subcommand is a dry run unless --apply is
// given.
func newDBSecretMappingCommand(runtime *Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret-mapping",
		Short: "Inspect and clean the local secret-mask mapping file",
		Long: "Inspect and clean the local bbolt mapping file that records the real-to-stand-in pairs\n" +
			"used by credential masking ({{output_dir}}/secret_mask_mappings.db by default).\n" +
			"The file is keyed by the real value only; the per-trace pairs live in credential_mask_audits.\n" +
			"bbolt holds an exclusive file lock, so every subcommand requires serve to be stopped.\n" +
			"Deletions take effect only after serve restarts, and every data-changing subcommand is a\n" +
			"dry run unless --apply is given.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return RequireSubcommand(cmd)
		},
	}
	cmd.AddCommand(newDBSecretMappingStatsCommand(runtime))
	cmd.AddCommand(newDBSecretMappingListCommand(runtime))
	cmd.AddCommand(newDBSecretMappingDeleteCommand(runtime))
	cmd.AddCommand(newDBSecretMappingPruneCommand(runtime))
	cmd.AddCommand(newDBSecretMappingCompactCommand(runtime))
	return cmd
}

type dbSecretMappingListOptions struct {
	configPath string
	format     string
	stdout     io.Writer
	showValues bool
	contains   string
	limit      int
	all        bool
}

type dbSecretMappingDeleteOptions struct {
	configPath string
	format     string
	stdout     io.Writer
	real       string
	seq        uint64
	seqSet     bool
	contains   string
	apply      bool
}

type dbSecretMappingPruneOptions struct {
	configPath  string
	format      string
	stdout      io.Writer
	rule        string
	since       string
	until       string
	purgeAudits bool
	apply       bool
}

type dbSecretMappingCompactOptions struct {
	configPath string
	format     string
	stdout     io.Writer
	apply      bool
}

func newDBSecretMappingStatsCommand(runtime *Runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show the mapping file path, entry count, size and sequence range",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDBSecretMappingStats(runtime.ConfigPath(), runtime.OutputFormat(), cmd.OutOrStdout())
		},
	}
}

func newDBSecretMappingListCommand(runtime *Runtime) *cobra.Command {
	opts := dbSecretMappingListOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List mapping entries, redacted unless --show-values is given",
		Long: "List mapping entries newest first. Real values and stand-ins are redacted by default:\n" +
			"the output carries the sequence, both lengths and a SHA-256 prefix of each value.\n" +
			"--show-values prints the actual pair, so its output is as sensitive as the file itself.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.configPath = runtime.ConfigPath()
			opts.format = runtime.OutputFormat()
			opts.stdout = cmd.OutOrStdout()
			return runDBSecretMappingList(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.showValues, "show-values", false, "Print the real value and stand-in instead of redacted metadata")
	cmd.Flags().StringVar(&opts.contains, "contains", "", "Only entries whose real value or stand-in contains this substring")
	cmd.Flags().IntVar(&opts.limit, "limit", 50, "Maximum number of entries to print")
	cmd.Flags().BoolVar(&opts.all, "all", false, "Print every matching entry, ignoring --limit")
	return cmd
}

func newDBSecretMappingDeleteCommand(runtime *Runtime) *cobra.Command {
	opts := dbSecretMappingDeleteOptions{}
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete mapping entries by real value, sequence or substring",
		Long: "Delete mapping rows and their by_real index entries. Exactly one of --real, --seq or\n" +
			"--contains is required. Without --apply the command only reports what it would delete.\n" +
			"Deletions take effect only after serve restarts: a running serve keeps the pairs it\n" +
			"loaded at startup, and a deleted pair makes an old stand-in unrecognisable.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.configPath = runtime.ConfigPath()
			opts.format = runtime.OutputFormat()
			opts.stdout = cmd.OutOrStdout()
			opts.seqSet = cmd.Flags().Changed("seq")
			return runDBSecretMappingDelete(opts)
		},
	}
	cmd.Flags().StringVar(&opts.real, "real", "", "Delete the entry recorded for this exact real value")
	cmd.Flags().Uint64Var(&opts.seq, "seq", 0, "Delete the entry with this insertion sequence")
	cmd.Flags().StringVar(&opts.contains, "contains", "", "Delete entries whose real value or stand-in contains this substring")
	cmd.Flags().BoolVar(&opts.apply, "apply", false, "Actually delete; without it the command is a dry run")
	return cmd
}

func newDBSecretMappingPruneCommand(runtime *Runtime) *cobra.Command {
	opts := dbSecretMappingPruneOptions{}
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete mapping entries for the values a masking rule produced",
		Long: "Read credential_mask_audits for the given rule and optional --since/--until window,\n" +
			"collect the original values that rule masked, then delete the mapping rows recorded for\n" +
			"those values. The mapping file itself stores no timestamps; the time window selects audit\n" +
			"rows only. Without --apply the command only reports what it would delete.\n" +
			"Deletions take effect only after serve restarts: a running serve keeps the pairs it loaded\n" +
			"at startup, and a deleted pair makes an old stand-in unrecognisable.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.configPath = runtime.ConfigPath()
			opts.format = runtime.OutputFormat()
			opts.stdout = cmd.OutOrStdout()
			return runDBSecretMappingPrune(opts)
		},
	}
	cmd.Flags().StringVar(&opts.rule, "rule", "", "Masking rule whose original values should be pruned (for example env.secret_assignment)")
	cmd.Flags().StringVar(&opts.since, "since", "", "Only audit rows detected at or after this RFC3339 timestamp")
	cmd.Flags().StringVar(&opts.until, "until", "", "Only audit rows detected at or before this RFC3339 timestamp")
	cmd.Flags().BoolVar(&opts.purgeAudits, "purge-audits", false, "Also delete the matching credential_mask_audits rows (system_events is left untouched)")
	cmd.Flags().BoolVar(&opts.apply, "apply", false, "Actually delete; without it the command is a dry run")
	return cmd
}

func newDBSecretMappingCompactCommand(runtime *Runtime) *cobra.Command {
	opts := dbSecretMappingCompactOptions{}
	cmd := &cobra.Command{
		Use:   "compact",
		Short: "Rebuild the mapping file to reclaim space freed by deleted entries",
		Long: "bbolt never shrinks a file after rows are deleted. Compact rebuilds the mapping file\n" +
			"from its live rows, keeps the sequence range, and swaps the old file to a .bak\n" +
			"backup. Without --apply the command only reports the current file size.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.configPath = runtime.ConfigPath()
			opts.format = runtime.OutputFormat()
			opts.stdout = cmd.OutOrStdout()
			return runDBSecretMappingCompact(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.apply, "apply", false, "Actually rebuild the file; without it the command is a dry run")
	return cmd
}

// dbSecretMappingContext bundles the resolved mapping file and the store that
// owns the local encryption key. The store is also what prune queries the audit
// table through.
type dbSecretMappingContext struct {
	config     *config.Config
	store      *store.Store
	mapping    *store.SecretMappingStore
	path       string
	fileExists bool
}

func secretMappingPath(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if path := strings.TrimSpace(cfg.SecretMasking.MappingStore.Path); path != "" {
		return path
	}
	return filepath.Join(cfg.TraceOutputDir(), store.SecretMappingFileName)
}

func openDBSecretMappingContext(configPath string) (*dbSecretMappingContext, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, ExitError{
			code:     ExitCodeAPI,
			category: ErrorCategoryAPI,
			errCode:  "CONFIG_LOAD_FAILED",
			message:  err.Error(),
		}
	}
	st, err := appdb.Open(cfg)
	if err != nil {
		return nil, ExitError{
			code:     ExitCodeAPI,
			category: ErrorCategoryAPI,
			errCode:  "STORE_OPEN_FAILED",
			message:  err.Error(),
		}
	}
	ctx := &dbSecretMappingContext{
		config: cfg,
		store:  st,
		path:   secretMappingPath(cfg),
	}
	info, statErr := os.Stat(ctx.path)
	switch {
	case statErr == nil && !info.IsDir():
		ctx.fileExists = true
	case statErr == nil:
		_ = st.Close()
		return nil, ExitError{
			code:     ExitCodeAPI,
			category: ErrorCategoryAPI,
			errCode:  "SECRET_MAPPING_PATH_INVALID",
			message:  fmt.Sprintf("secret mapping path %s is a directory", ctx.path),
		}
	case !os.IsNotExist(statErr):
		_ = st.Close()
		return nil, ExitError{
			code:     ExitCodeAPI,
			category: ErrorCategoryAPI,
			errCode:  "SECRET_MAPPING_STAT_FAILED",
			message:  fmt.Sprintf("stat secret mapping file %s: %v", ctx.path, statErr),
		}
	}
	if !ctx.fileExists {
		return ctx, nil
	}
	mappingStore, err := st.OpenSecretMappingStore(ctx.path)
	if err != nil {
		_ = st.Close()
		if store.IsSecretMappingStoreLocked(err) {
			return nil, ExitError{
				code:     ExitCodeAPI,
				category: ErrorCategoryAPI,
				errCode:  "SECRET_MAPPING_STORE_LOCKED",
				message: fmt.Sprintf("cannot open secret mapping file %s: %v; stop serve first, then rerun the command with serve stopped",
					ctx.path, store.ErrSecretMappingStoreLocked),
			}
		}
		return nil, ExitError{
			code:     ExitCodeAPI,
			category: ErrorCategoryAPI,
			errCode:  "SECRET_MAPPING_OPEN_FAILED",
			message:  fmt.Sprintf("open secret mapping file %s: %v", ctx.path, err),
		}
	}
	ctx.mapping = mappingStore
	return ctx, nil
}

func (ctx *dbSecretMappingContext) Close() {
	if ctx == nil {
		return
	}
	if ctx.mapping != nil {
		_ = ctx.mapping.Close()
	}
	if ctx.store != nil {
		_ = ctx.store.Close()
	}
}

func (ctx *dbSecretMappingContext) warnings() []string {
	if ctx == nil || ctx.config == nil {
		return nil
	}
	var warnings []string
	if !ctx.config.SecretMasking.Enabled {
		warnings = append(warnings, dbSecretMappingMaskingDisabledWarning)
	}
	if !ctx.config.SecretMasking.MappingStore.Enabled {
		warnings = append(warnings, dbSecretMappingConfigDisabledWarning)
	}
	return warnings
}

func (ctx *dbSecretMappingContext) encryptionMode() string {
	if ctx == nil || ctx.store == nil {
		return ""
	}
	return ctx.store.SecretStatus().Mode
}

// fingerprint returns the keyed redacted digest for a real value. A nil mapping
// store has no rows to render, so the empty string is safe here.
func (ctx *dbSecretMappingContext) fingerprint(real string) string {
	if ctx == nil || ctx.mapping == nil {
		return ""
	}
	return ctx.mapping.Fingerprint(real)
}

func dbSecretMappingOperationalError(errCode string, err error) error {
	return ExitError{
		code:     ExitCodeAPI,
		category: ErrorCategoryAPI,
		errCode:  errCode,
		message:  err.Error(),
	}
}

// dbSecretMappingEntryView is the redacted-or-not shape the list and delete
// commands print. Real and Fake are empty unless --show-values was given; the
// lengths and SHA-256 prefixes let an operator correlate a row without the file
// exposing the value in a terminal scrollback.
type dbSecretMappingEntryView struct {
	Seq         uint64 `json:"seq"`
	Encrypted   bool   `json:"encrypted"`
	Unreadable  bool   `json:"unreadable,omitempty"`
	DecodeError string `json:"decode_error,omitempty"`
	Real        string `json:"real,omitempty"`
	Fake        string `json:"fake,omitempty"`
	RealLength  int    `json:"real_length"`
	// RealFingerprint is a keyed HMAC prefix, not a plain SHA-256: a bare digest
	// prefix of a low-entropy value (a mobile number) is brute-forceable, which
	// is why the mapping index itself is keyed.
	RealFingerprint  string `json:"real_fingerprint,omitempty"`
	FakeLength       int    `json:"fake_length"`
	FakeSHA256Prefix string `json:"fake_sha256_prefix,omitempty"`
}

// newDBSecretMappingEntryView renders one entry for output. fingerprint is the
// keyed digest function; it is nil only when the mapping store is not open (the
// file does not exist), in which case there is no entry to render either.
func newDBSecretMappingEntryView(entry store.SecretMappingEntry, showValues bool, fingerprint func(string) string) dbSecretMappingEntryView {
	view := dbSecretMappingEntryView{
		Seq:         entry.Seq,
		Encrypted:   entry.Encrypted,
		Unreadable:  entry.DecodeError != "",
		DecodeError: entry.DecodeError,
	}
	if entry.DecodeError != "" {
		return view
	}
	view.RealLength = len(entry.Mapping.Real)
	view.FakeLength = len(entry.Mapping.Fake)
	if fingerprint != nil {
		view.RealFingerprint = fingerprint(entry.Mapping.Real)
	}
	view.FakeSHA256Prefix = shortSHA256Prefix(entry.Mapping.Fake)
	if showValues {
		view.Real = entry.Mapping.Real
		view.Fake = entry.Mapping.Fake
	}
	return view
}

func shortSHA256Prefix(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func writeDBSecretMappingWarnings(w io.Writer, warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

func writeDBSecretMappingEntriesText(w io.Writer, entries []dbSecretMappingEntryView) {
	for _, entry := range entries {
		if entry.Unreadable {
			fmt.Fprintf(w, "  seq=%d unreadable=true decode_error=%q\n", entry.Seq, entry.DecodeError)
			continue
		}
		fmt.Fprintf(w, "  seq=%d real_length=%d real_fingerprint=%s fake_length=%d fake_sha256=%s encrypted=%t\n",
			entry.Seq, entry.RealLength, entry.RealFingerprint, entry.FakeLength, entry.FakeSHA256Prefix, entry.Encrypted)
		if entry.Real != "" || entry.Fake != "" {
			fmt.Fprintf(w, "    real=%s fake=%s\n", entry.Real, entry.Fake)
		}
	}
}

func runDBSecretMappingStats(configPath string, format string, stdout io.Writer) error {
	ctx, err := openDBSecretMappingContext(configPath)
	if err != nil {
		return err
	}
	defer ctx.Close()

	secretStatus := ctx.store.SecretStatus()
	result := map[string]any{
		"path":                   ctx.path,
		"exists":                 ctx.fileExists,
		"mapping_store_enabled":  ctx.config.SecretMasking.MappingStore.Enabled,
		"secret_masking_enabled": ctx.config.SecretMasking.Enabled,
		"encryption_mode":        secretStatus.Mode,
		"secret_key_path":        secretStatus.KeyPath,
		"secret_key_fingerprint": secretStatus.Fingerprint,
		"file_size_bytes":        int64(0),
		"entry_count":            0,
		"index_count":            0,
		"encrypted_rows":         0,
		"plaintext_rows":         0,
		"unreadable_rows":        0,
		"dangling_index_entries": 0,
		"min_seq":                uint64(0),
		"max_seq":                uint64(0),
		"next_seq":               uint64(0),
		"warnings":               ctx.warnings(),
	}
	if ctx.fileExists {
		stats, err := ctx.mapping.Stats()
		if err != nil {
			return dbSecretMappingOperationalError("SECRET_MAPPING_STATS_FAILED", err)
		}
		result["file_size_bytes"] = stats.FileSizeBytes
		result["entry_count"] = stats.EntryCount
		result["index_count"] = stats.IndexCount
		result["encrypted_rows"] = stats.EncryptedRows
		result["plaintext_rows"] = stats.PlaintextRows
		result["unreadable_rows"] = stats.UnreadableRows
		result["dangling_index_entries"] = stats.DanglingIndexEntries
		result["min_seq"] = stats.MinSeq
		result["max_seq"] = stats.MaxSeq
		result["next_seq"] = stats.NextSeq
	}
	if err := WriteResult(stdoutOrDefault(stdout), format, "db.secret-mapping.stats", result, func(w io.Writer) error {
		fmt.Fprintf(w, "path: %s\n", result["path"])
		fmt.Fprintf(w, "exists: %t\n", result["exists"])
		fmt.Fprintf(w, "mapping_store_enabled: %t\n", result["mapping_store_enabled"])
		fmt.Fprintf(w, "secret_masking_enabled: %t\n", result["secret_masking_enabled"])
		fmt.Fprintf(w, "encryption_mode: %s\n", result["encryption_mode"])
		if fingerprint := secretStatus.Fingerprint; fingerprint != "" {
			fmt.Fprintf(w, "secret_key_fingerprint: %s\n", fingerprint)
		}
		fmt.Fprintf(w, "file_size_bytes: %d\n", result["file_size_bytes"])
		fmt.Fprintf(w, "entry_count: %d\n", result["entry_count"])
		fmt.Fprintf(w, "index_count: %d\n", result["index_count"])
		fmt.Fprintf(w, "encrypted_rows: %d\n", result["encrypted_rows"])
		fmt.Fprintf(w, "plaintext_rows: %d\n", result["plaintext_rows"])
		fmt.Fprintf(w, "unreadable_rows: %d\n", result["unreadable_rows"])
		fmt.Fprintf(w, "dangling_index_entries: %d\n", result["dangling_index_entries"])
		fmt.Fprintf(w, "min_seq: %d\n", result["min_seq"])
		fmt.Fprintf(w, "max_seq: %d\n", result["max_seq"])
		fmt.Fprintf(w, "next_seq: %d\n", result["next_seq"])
		writeDBSecretMappingWarnings(w, StringSlice(result["warnings"]))
		return nil
	}); err != nil {
		return dbSecretMappingOperationalError("SECRET_MAPPING_RESULT_WRITE_FAILED", err)
	}
	return nil
}

func runDBSecretMappingList(opts dbSecretMappingListOptions) error {
	if opts.limit <= 0 && !opts.all {
		return UsageError("--limit must be greater than 0; use --all to print every entry", "limit")
	}
	ctx, err := openDBSecretMappingContext(opts.configPath)
	if err != nil {
		return err
	}
	defer ctx.Close()

	contains := strings.TrimSpace(opts.contains)
	warnings := ctx.warnings()
	entries := make([]dbSecretMappingEntryView, 0)
	total := 0
	if ctx.fileExists {
		limit := opts.limit
		if opts.all {
			limit = 0
		}
		if err := ctx.mapping.IterateMappings(true, func(entry store.SecretMappingEntry) error {
			mapping := entry.Mapping
			if contains != "" && !strings.Contains(mapping.Real, contains) && !strings.Contains(mapping.Fake, contains) {
				return nil
			}
			total++
			if limit == 0 || len(entries) < limit {
				entries = append(entries, newDBSecretMappingEntryView(entry, opts.showValues, ctx.fingerprint))
			}
			return nil
		}); err != nil {
			return dbSecretMappingOperationalError("SECRET_MAPPING_LIST_FAILED", err)
		}
	}
	if !opts.showValues {
		warnings = append(warnings, "values are redacted; pass --show-values to print the real value and stand-in")
	}
	result := map[string]any{
		"path":        ctx.path,
		"exists":      ctx.fileExists,
		"redacted":    !opts.showValues,
		"show_values": opts.showValues,
		"contains":    contains,
		"total":       total,
		"returned":    len(entries),
		"entries":     entries,
		"warnings":    warnings,
	}
	if opts.all {
		result["limit"] = 0
	} else {
		result["limit"] = opts.limit
	}
	if err := WriteResult(stdoutOrDefault(opts.stdout), opts.format, "db.secret-mapping.list", result, func(w io.Writer) error {
		fmt.Fprintf(w, "path: %s\n", ctx.path)
		fmt.Fprintf(w, "exists: %t\n", ctx.fileExists)
		fmt.Fprintf(w, "redacted: %t\n", !opts.showValues)
		fmt.Fprintf(w, "total: %d\n", total)
		fmt.Fprintf(w, "returned: %d\n", len(entries))
		fmt.Fprintf(w, "entries:\n")
		writeDBSecretMappingEntriesText(w, entries)
		writeDBSecretMappingWarnings(w, warnings)
		return nil
	}); err != nil {
		return dbSecretMappingOperationalError("SECRET_MAPPING_RESULT_WRITE_FAILED", err)
	}
	return nil
}

func runDBSecretMappingDelete(opts dbSecretMappingDeleteOptions) error {
	// --real is matched byte for byte: a mapping's real value may legitimately
	// begin or end with a space, so trimming it here would make that row
	// impossible to select. --contains is a substring filter and is trimmed.
	real := opts.real
	contains := strings.TrimSpace(opts.contains)
	selectors := 0
	if real != "" {
		selectors++
	}
	if opts.seqSet {
		selectors++
	}
	if contains != "" {
		selectors++
	}
	if selectors != 1 {
		return UsageError("exactly one of --real, --seq or --contains is required", "real")
	}

	ctx, err := openDBSecretMappingContext(opts.configPath)
	if err != nil {
		return err
	}
	defer ctx.Close()

	warnings := append([]string{}, ctx.warnings()...)
	warnings = append(warnings, dbSecretMappingRestartWarning, dbSecretMappingUnrecognisableWarning)

	var entries []dbSecretMappingEntryView
	selector := ""
	switch {
	case real != "":
		selector = "real"
	case opts.seqSet:
		selector = "seq"
	default:
		selector = "contains"
	}

	if ctx.fileExists {
		if opts.apply {
			var removed []store.SecretMappingEntry
			switch selector {
			case "real":
				entry, found, err := ctx.mapping.DeleteMappingByReal(real)
				if err != nil {
					return dbSecretMappingOperationalError("SECRET_MAPPING_DELETE_FAILED", err)
				}
				if found {
					removed = append(removed, entry)
				}
			case "seq":
				entry, found, err := ctx.mapping.DeleteMappingBySeq(opts.seq)
				if err != nil {
					return dbSecretMappingOperationalError("SECRET_MAPPING_DELETE_FAILED", err)
				}
				if found {
					removed = append(removed, entry)
				}
			default:
				matched, err := ctx.mapping.DeleteMappingsMatching(func(mapping secretmask.Mapping) bool {
					return strings.Contains(mapping.Real, contains) || strings.Contains(mapping.Fake, contains)
				})
				if err != nil {
					return dbSecretMappingOperationalError("SECRET_MAPPING_DELETE_FAILED", err)
				}
				removed = matched
			}
			for _, entry := range removed {
				entries = append(entries, newDBSecretMappingEntryView(entry, false, ctx.fingerprint))
			}
		} else {
			if err := ctx.mapping.IterateMappings(true, func(entry store.SecretMappingEntry) error {
				if !dbSecretMappingEntryMatches(entry, selector, real, opts.seq, contains) {
					return nil
				}
				entries = append(entries, newDBSecretMappingEntryView(entry, false, ctx.fingerprint))
				return nil
			}); err != nil {
				return dbSecretMappingOperationalError("SECRET_MAPPING_LIST_FAILED", err)
			}
		}
	}

	matched := len(entries)
	deleted := 0
	if opts.apply {
		deleted = matched
	}
	result := map[string]any{
		"path":         ctx.path,
		"exists":       ctx.fileExists,
		"dry_run":      !opts.apply,
		"mutated":      deleted > 0,
		"selector":     selector,
		"matched":      matched,
		"deleted":      deleted,
		"would_delete": 0,
		"entries":      entries,
		"warnings":     warnings,
	}
	if opts.seqSet {
		result["seq"] = opts.seq
	}
	if !opts.apply {
		result["would_delete"] = matched
	}
	command := "db.secret-mapping.delete"
	if err := WriteResult(stdoutOrDefault(opts.stdout), opts.format, command, result, func(w io.Writer) error {
		if !opts.apply {
			fmt.Fprintf(w, "dry-run %s: no changes will be applied\n", command)
		}
		fmt.Fprintf(w, "path: %s\n", ctx.path)
		fmt.Fprintf(w, "selector: %s\n", selector)
		if opts.seqSet {
			fmt.Fprintf(w, "seq: %d\n", opts.seq)
		}
		fmt.Fprintf(w, "matched: %d\n", matched)
		if opts.apply {
			fmt.Fprintf(w, "deleted: %d\n", deleted)
		} else {
			fmt.Fprintf(w, "would_delete: %d\n", matched)
		}
		fmt.Fprintf(w, "entries:\n")
		writeDBSecretMappingEntriesText(w, entries)
		writeDBSecretMappingWarnings(w, warnings)
		return nil
	}); err != nil {
		return dbSecretMappingOperationalError("SECRET_MAPPING_RESULT_WRITE_FAILED", err)
	}
	return nil
}

func dbSecretMappingEntryMatches(entry store.SecretMappingEntry, selector string, real string, seq uint64, contains string) bool {
	if entry.DecodeError != "" {
		return selector == "seq" && entry.Seq == seq
	}
	switch selector {
	case "real":
		return entry.Mapping.Real == real
	case "seq":
		return entry.Seq == seq
	default:
		return strings.Contains(entry.Mapping.Real, contains) || strings.Contains(entry.Mapping.Fake, contains)
	}
}

func runDBSecretMappingPrune(opts dbSecretMappingPruneOptions) error {
	rule := strings.TrimSpace(opts.rule)
	if rule == "" {
		return UsageError("--rule is required", "rule")
	}
	since, err := parseDBSecretMappingTime(opts.since, "since")
	if err != nil {
		return err
	}
	until, err := parseDBSecretMappingTime(opts.until, "until")
	if err != nil {
		return err
	}
	if !since.IsZero() && !until.IsZero() && since.After(until) {
		return UsageError("--since must not be after --until", "since")
	}

	ctx, err := openDBSecretMappingContext(opts.configPath)
	if err != nil {
		return err
	}
	defer ctx.Close()

	filter := store.CredentialMaskAuditFilter{Rule: rule, Since: since, Until: until}
	originals, auditRows, undecryptable, err := collectDBSecretMappingOriginals(ctx.store, filter)
	if err != nil {
		return dbSecretMappingOperationalError("SECRET_MAPPING_AUDIT_QUERY_FAILED", err)
	}
	originalSet := make(map[string]struct{}, len(originals))
	for _, original := range originals {
		originalSet[original] = struct{}{}
	}

	warnings := append([]string{}, ctx.warnings()...)
	warnings = append(warnings, dbSecretMappingRestartWarning, dbSecretMappingUnrecognisableWarning)
	if opts.purgeAudits {
		warnings = append(warnings, dbSecretMappingPurgeAuditWarning)
	}
	if undecryptable > 0 {
		warnings = append(warnings, dbSecretMappingUnreadableAuditWarning)
	}

	var entries []dbSecretMappingEntryView
	deleted := 0
	if ctx.fileExists {
		if opts.apply {
			removed, err := ctx.mapping.DeleteMappingsMatching(func(mapping secretmask.Mapping) bool {
				_, wanted := originalSet[mapping.Real]
				return wanted
			})
			if err != nil {
				return dbSecretMappingOperationalError("SECRET_MAPPING_PRUNE_FAILED", err)
			}
			deleted = len(removed)
			for _, entry := range removed {
				entries = append(entries, newDBSecretMappingEntryView(entry, false, ctx.fingerprint))
			}
		} else {
			if err := ctx.mapping.IterateMappings(false, func(entry store.SecretMappingEntry) error {
				if entry.DecodeError != "" {
					return nil
				}
				if _, wanted := originalSet[entry.Mapping.Real]; !wanted {
					return nil
				}
				entries = append(entries, newDBSecretMappingEntryView(entry, false, ctx.fingerprint))
				return nil
			}); err != nil {
				return dbSecretMappingOperationalError("SECRET_MAPPING_LIST_FAILED", err)
			}
		}
	}
	matched := len(entries)

	purgedAudits := 0
	if opts.apply && opts.purgeAudits {
		purged, err := ctx.store.DeleteCredentialMaskAudits(filter)
		if err != nil {
			return dbSecretMappingOperationalError("SECRET_MAPPING_AUDIT_PURGE_FAILED", err)
		}
		purgedAudits = purged
	}

	result := map[string]any{
		"path":                     ctx.path,
		"exists":                   ctx.fileExists,
		"dry_run":                  !opts.apply,
		"mutated":                  opts.apply && (deleted > 0 || purgedAudits > 0),
		"rule":                     rule,
		"purge_audits":             opts.purgeAudits,
		"audit_rows":               auditRows,
		"undecryptable_audit_rows": undecryptable,
		"original_values":          len(originals),
		"matched_mappings":         matched,
		"deleted_mappings":         deleted,
		"would_delete_mappings":    0,
		"purged_audit_rows":        purgedAudits,
		"entries":                  entries,
		"warnings":                 warnings,
	}
	if !since.IsZero() {
		result["since"] = since.UTC().Format(time.RFC3339)
	}
	if !until.IsZero() {
		result["until"] = until.UTC().Format(time.RFC3339)
	}
	if !opts.apply {
		result["would_delete_mappings"] = matched
	}
	command := "db.secret-mapping.prune"
	if err := WriteResult(stdoutOrDefault(opts.stdout), opts.format, command, result, func(w io.Writer) error {
		if !opts.apply {
			fmt.Fprintf(w, "dry-run %s: no changes will be applied\n", command)
		}
		fmt.Fprintf(w, "path: %s\n", ctx.path)
		fmt.Fprintf(w, "rule: %s\n", rule)
		if result["since"] != nil {
			fmt.Fprintf(w, "since: %v\n", result["since"])
		}
		if result["until"] != nil {
			fmt.Fprintf(w, "until: %v\n", result["until"])
		}
		fmt.Fprintf(w, "audit_rows: %d\n", auditRows)
		fmt.Fprintf(w, "original_values: %d\n", len(originals))
		fmt.Fprintf(w, "matched_mappings: %d\n", matched)
		if opts.apply {
			fmt.Fprintf(w, "deleted_mappings: %d\n", deleted)
			fmt.Fprintf(w, "purged_audit_rows: %d\n", purgedAudits)
		} else {
			fmt.Fprintf(w, "would_delete_mappings: %d\n", matched)
		}
		fmt.Fprintf(w, "entries:\n")
		writeDBSecretMappingEntriesText(w, entries)
		writeDBSecretMappingWarnings(w, warnings)
		return nil
	}); err != nil {
		return dbSecretMappingOperationalError("SECRET_MAPPING_RESULT_WRITE_FAILED", err)
	}
	return nil
}

func parseDBSecretMappingTime(value string, field string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, UsageError("--"+field+" must be an RFC3339 timestamp, for example 2026-11-01T08:00:00Z", field)
	}
	return parsed.UTC(), nil
}

// collectDBSecretMappingOriginals pages through the audit table and returns the
// distinct original values of the matching rows. It also reports the matched row
// count and how many rows could not be decrypted (their Original is empty), which
// prune reports as a limitation rather than failing the whole command.
func collectDBSecretMappingOriginals(st *store.Store, filter store.CredentialMaskAuditFilter) ([]string, int, int, error) {
	const pageSize = 200
	seen := make(map[string]struct{})
	originals := make([]string, 0)
	auditRows := 0
	undecryptable := 0
	for page := 1; ; page++ {
		filter.Page = page
		filter.PageSize = pageSize
		result, err := st.ListCredentialMaskAudits(filter)
		if err != nil {
			return nil, 0, 0, err
		}
		auditRows = result.Total
		for _, item := range result.Items {
			if item.Original == "" {
				if item.ValueLength > 0 {
					undecryptable++
				}
				continue
			}
			if _, ok := seen[item.Original]; ok {
				continue
			}
			seen[item.Original] = struct{}{}
			originals = append(originals, item.Original)
		}
		if result.TotalPages <= page || len(result.Items) == 0 {
			break
		}
	}
	return originals, auditRows, undecryptable, nil
}

func runDBSecretMappingCompact(opts dbSecretMappingCompactOptions) error {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return ExitError{
			code:     ExitCodeAPI,
			category: ErrorCategoryAPI,
			errCode:  "CONFIG_LOAD_FAILED",
			message:  err.Error(),
		}
	}
	path := secretMappingPath(cfg)
	warnings := make([]string, 0, 2)
	if !cfg.SecretMasking.Enabled {
		warnings = append(warnings, dbSecretMappingMaskingDisabledWarning)
	}
	if !cfg.SecretMasking.MappingStore.Enabled {
		warnings = append(warnings, dbSecretMappingConfigDisabledWarning)
	}
	result := map[string]any{
		"path":             path,
		"exists":           false,
		"dry_run":          !opts.apply,
		"mutated":          false,
		"file_size_before": int64(0),
		"file_size_after":  int64(0),
		"entries":          0,
		"index_entries":    0,
		"warnings":         warnings,
	}
	info, statErr := os.Stat(path)
	switch {
	case statErr == nil && !info.IsDir():
		result["exists"] = true
		result["file_size_before"] = info.Size()
	case statErr == nil:
		return ExitError{
			code:     ExitCodeAPI,
			category: ErrorCategoryAPI,
			errCode:  "SECRET_MAPPING_PATH_INVALID",
			message:  fmt.Sprintf("secret mapping path %s is a directory", path),
		}
	case !os.IsNotExist(statErr):
		return ExitError{
			code:     ExitCodeAPI,
			category: ErrorCategoryAPI,
			errCode:  "SECRET_MAPPING_STAT_FAILED",
			message:  fmt.Sprintf("stat secret mapping file %s: %v", path, statErr),
		}
	}
	command := "db.secret-mapping.compact"
	if !boolValue(result["exists"]) {
		if err := WriteResult(stdoutOrDefault(opts.stdout), opts.format, command, result, func(w io.Writer) error {
			fmt.Fprintf(w, "path: %s\n", path)
			fmt.Fprintf(w, "exists: false\n")
			fmt.Fprintf(w, "nothing to compact: the mapping file does not exist\n")
			writeDBSecretMappingWarnings(w, warnings)
			return nil
		}); err != nil {
			return dbSecretMappingOperationalError("SECRET_MAPPING_RESULT_WRITE_FAILED", err)
		}
		return nil
	}
	if !opts.apply {
		if err := WriteResult(stdoutOrDefault(opts.stdout), opts.format, command, result, func(w io.Writer) error {
			fmt.Fprintf(w, "dry-run %s: no changes will be applied\n", command)
			fmt.Fprintf(w, "path: %s\n", path)
			fmt.Fprintf(w, "file_size_before: %d\n", info.Size())
			fmt.Fprintf(w, "entries: unknown without opening the file; run with --apply to rebuild it\n")
			writeDBSecretMappingWarnings(w, warnings)
			return nil
		}); err != nil {
			return dbSecretMappingOperationalError("SECRET_MAPPING_RESULT_WRITE_FAILED", err)
		}
		return nil
	}
	compactResult, err := store.CompactSecretMappingFile(path)
	if err != nil {
		if store.IsSecretMappingStoreLocked(err) {
			return ExitError{
				code:     ExitCodeAPI,
				category: ErrorCategoryAPI,
				errCode:  "SECRET_MAPPING_STORE_LOCKED",
				message:  fmt.Sprintf("cannot compact secret mapping file %s: %v; stop serve first, then rerun the command with serve stopped", path, store.ErrSecretMappingStoreLocked),
			}
		}
		return dbSecretMappingOperationalError("SECRET_MAPPING_COMPACT_FAILED", err)
	}
	warnings = append(warnings, dbSecretMappingBackupWarning)
	result["mutated"] = true
	result["file_size_before"] = compactResult.FileSizeBefore
	result["file_size_after"] = compactResult.FileSizeAfter
	result["entries"] = compactResult.Entries
	result["index_entries"] = compactResult.IndexEntries
	result["backup_path"] = compactResult.BackupPath
	result["reclaimed_bytes"] = compactResult.FileSizeBefore - compactResult.FileSizeAfter
	result["warnings"] = warnings
	if err := WriteResult(stdoutOrDefault(opts.stdout), opts.format, command, result, func(w io.Writer) error {
		fmt.Fprintf(w, "path: %s\n", path)
		fmt.Fprintf(w, "backup_path: %s\n", compactResult.BackupPath)
		fmt.Fprintf(w, "entries: %d\n", compactResult.Entries)
		fmt.Fprintf(w, "index_entries: %d\n", compactResult.IndexEntries)
		fmt.Fprintf(w, "file_size_before: %d\n", compactResult.FileSizeBefore)
		fmt.Fprintf(w, "file_size_after: %d\n", compactResult.FileSizeAfter)
		fmt.Fprintf(w, "reclaimed_bytes: %d\n", compactResult.FileSizeBefore-compactResult.FileSizeAfter)
		writeDBSecretMappingWarnings(w, warnings)
		return nil
	}); err != nil {
		return dbSecretMappingOperationalError("SECRET_MAPPING_RESULT_WRITE_FAILED", err)
	}
	return nil
}

func boolValue(value any) bool {
	typed, _ := value.(bool)
	return typed
}
