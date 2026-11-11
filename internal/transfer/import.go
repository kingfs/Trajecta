package transfer

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// ImportOptions controls one bundle import.
type ImportOptions struct {
	Bundle   string
	Password string

	// Apply writes the merge. Without it every check runs and a full report is
	// produced, but nothing in the database or on disk changes.
	Apply bool

	// ChannelMap forces source channel id -> target channel id. Both sides must
	// exist (source in the bundle, target in the database).
	ChannelMap map[string]string

	// ChannelStrategy is "reuse" (default: merge by base_url+api key) or
	// "create" (always create a new channel).
	ChannelStrategy string

	// KeepExistingFiles keeps a target cassette that differs from the bundle
	// instead of overwriting it, and reports the conflict.
	KeepExistingFiles bool

	Log *Logger
}

// Import merges one bundle into the target deployment. It is idempotent: an
// unchanged bundle imports to zero writes, and a newer bundle only updates
// what changed. Cassette files are written during the database transaction;
// if the transaction rolls back, the files this run created are removed so the
// index and the disk cannot disagree.
func Import(ctx context.Context, st *store.Store, outputDir string, opts ImportOptions) (*ImportResult, error) {
	start := time.Now()
	if st == nil {
		return nil, errors.New("import requires a store")
	}
	log := opts.Log
	if log == nil {
		log = NewDiscardLogger()
	}
	strategy := strings.TrimSpace(opts.ChannelStrategy)
	if strategy == "" {
		strategy = "reuse"
	}
	if strategy != "reuse" && strategy != "create" {
		return nil, fmt.Errorf("unsupported --channel-strategy %q (want reuse|create)", strategy)
	}

	result := &ImportResult{Bundle: opts.Bundle, DryRun: !opts.Apply}

	cur, err := openBundle(opts.Bundle, opts.Password)
	if err != nil {
		return nil, err
	}
	defer cur.Close()

	manifest, err := cur.readManifest()
	if err != nil {
		return nil, err
	}
	log.Infof("bundle %s: format %s, created %s%s, window %s",
		opts.Bundle, manifest.Format, manifest.CreatedAt.Format(time.RFC3339),
		sourceLabel(manifest.SourceName), describeWindow(manifest.Window))
	if manifest.PasswordProtected && opts.Password == "" {
		return nil, errors.New("bundle is password-protected; pass --password or set TRAJECTA_IMPORT_PASSWORD")
	}
	if manifest.Compression != compressionGzip && manifest.Compression != compressionNone {
		return nil, fmt.Errorf("bundle compression %q is not supported", manifest.Compression)
	}
	if !opts.Apply {
		log.Noticef("dry run: nothing will be written; re-run with --apply to import")
	}

	createdFiles := []string{}
	filesPlaced := map[string]bool{}
	targetsLoaded := false
	var targets []targetChannel

	run := func(view *store.Store, apply bool) error {
		s := newImportState(ctx, view, log, outputDir, apply, result, opts.ChannelMap)
		s.manifest = manifest
		s.resolver.strategyCreate = strategy == "create"
		s.keepExistingFiles = opts.KeepExistingFiles
		s.desiredFiles = map[string]desiredFile{}

		if !targetsLoaded {
			loaded, err := loadTargetChannels(ctx, view)
			if err != nil {
				return fmt.Errorf("load target channels: %w", err)
			}
			targets = loaded
			targetsLoaded = true
			log.Infof("target has %d channels", len(targets))
		}

		seenTables := map[string]bool{}
		for {
			hdr, err := cur.next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			switch {
			case strings.HasPrefix(hdr.Name, entryCassettesPrefix):
				rel := strings.TrimPrefix(hdr.Name, entryCassettesPrefix)
				targetPath := filepath.Join(outputDir, filepath.FromSlash(rel))
				if err := s.placeCassette(hdr, cur.body(), rel, targetPath, &createdFiles, filesPlaced); err != nil {
					return err
				}
			case strings.HasPrefix(hdr.Name, entryChannelsPrefix) && strings.HasSuffix(hdr.Name, ".jsonl"):
				table := strings.TrimSuffix(strings.TrimPrefix(hdr.Name, entryChannelsPrefix), ".jsonl")
				if seenTables[table] {
					return fmt.Errorf("bundle contains table %q twice", table)
				}
				seenTables[table] = true
				if err := dispatchTable(s, table, cur.body(), hdr.Size, targets); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unexpected bundle entry %q", hdr.Name)
			}
		}

		// The manifest is the integrity contract: every table it declares must
		// have arrived with exactly that many rows.
		for _, want := range manifest.Tables {
			got := int64(0)
			if rep, ok := s.byName[want.Name]; ok {
				got = rep.Processed
			}
			if got != want.Rows {
				return fmt.Errorf("bundle table %s has %d rows but the manifest declares %d (truncated bundle)", want.Name, got, want.Rows)
			}
		}
		s.flushTableReports()
		result.ChannelReuses = s.resolver.reused
		result.ChannelsCreated = append([]string(nil), s.resolver.created...)
		result.Cassettes = s.files
		return nil
	}

	if !opts.Apply {
		if err := run(st, false); err != nil {
			return nil, err
		}
		result.Duration = time.Since(start)
		return result, nil
	}

	err = st.ConfigurationTransaction(ctx, func(tx *store.Store, commit func() error) error {
		if err := run(tx, true); err != nil {
			return err
		}
		if len(result.ChannelReuses) > 0 || len(result.ChannelsCreated) > 0 {
			if err := tx.SaveAppSettingJSON(ctx, "channels.initialized", true); err != nil {
				return fmt.Errorf("mark channels configured: %w", err)
			}
		}
		return commit()
	})
	if err != nil {
		for _, path := range createdFiles {
			if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				log.Warnf("rollback could not remove %s: %v", path, rmErr)
			}
		}
		return nil, err
	}

	// Session summaries are derived from logs; rebuilding them here is what
	// makes the imported sessions visible in the Monitor's history views.
	rebuildStart := time.Now()
	if err := st.RebuildSessionSummaries(); err != nil {
		result.warn("session summaries were not rebuilt (%v); run `trajecta db summary rebuild sessions`", err)
		log.Warnf("session summaries rebuild failed: %v", err)
	} else {
		result.SessionRebuilt = true
	}
	result.SessionRebuildDur = time.Since(rebuildStart)
	result.Duration = time.Since(start)
	log.Infof("import committed: %d tables, %d channels created, %d channels reused, %d cassettes copied, %d updated",
		len(result.Tables), len(result.ChannelsCreated), len(result.ChannelReuses),
		result.Cassettes.Copied, result.Cassettes.Updated)
	return result, nil
}

// desiredFile carries the mtime a cassette must have so the next index Sync
// does not consider it new, and whether the file must not be written because
// its index row was skipped as a conflict.
type desiredFile struct {
	modTimeNs int64
	skip      bool
}

func sourceLabel(name string) string {
	if name == "" {
		return ""
	}
	return " from " + name
}

func describeWindow(w Window) string {
	if w.All {
		return "all recorded data"
	}
	parts := []string{}
	if w.Since != nil {
		parts = append(parts, "since "+w.Since.Format(time.RFC3339))
	}
	if w.Until != nil {
		parts = append(parts, "until "+w.Until.Format(time.RFC3339))
	}
	return strings.Join(parts, " ")
}

// dispatchTable routes one db/<table>.jsonl entry to its handler.
func dispatchTable(s *importState, table string, body io.Reader, size int64, targets []targetChannel) error {
	switch table {
	case "channel_configs":
		return s.handleChannelConfigs(body, size, targets)
	case "channel_models":
		return s.handleChannelModels(body, size)
	case "model_aliases":
		return s.handleModelAliases(body, size)
	case "model_catalog":
		return s.handleModelCatalog(body, size)
	case "app_settings":
		return s.handleAppSettings(body, size)
	case "channel_probe_runs":
		return s.handleChannelProbeRuns(body, size)
	case "datasets":
		return s.handleDatasets(body, size)
	case "eval_runs":
		return s.handleEvalRuns(body, size)
	case "experiment_runs":
		return s.handleExperimentRuns(body, size)
	case "dataset_examples":
		return s.handleDatasetExamples(body, size)
	case "scores":
		return s.handleScores(body, size)
	case "responses":
		return s.handleResponses(body, size)
	case "response_items":
		return s.handleResponseItems(body, size)
	case "request_audits":
		return s.handleRequestAudits(body, size)
	case "execution_events":
		return s.handleExecutionEvents(body, size)
	case "tool_call_audits":
		return s.handleToolCallAudits(body, size)
	case "system_events":
		return s.handleSystemEvents(body, size)
	case "analysis_runs":
		return s.handleAnalysisRuns(body, size)
	case "logs":
		return s.handleLogs(body, size)
	case "upstream_exchanges":
		return s.handleUpstreamExchanges(body, size)
	case "credential_mask_audits":
		return s.handleCredentialMaskAudits(body, size)
	case "credential_mask_event_states":
		return s.handleCredentialMaskEventStates(body, size)
	default:
		return fmt.Errorf("bundle contains unknown table %q", table)
	}
}

// placeCassette writes one cassette file. An identical existing file is left
// alone, a differing one is replaced (unless --keep-existing-files), and a
// path whose index row was skipped is never touched.
func (s *importState) placeCassette(hdr *tar.Header, body io.Reader, rel, targetPath string, created *[]string, placed map[string]bool) error {
	if placed[rel] {
		return nil
	}
	placed[rel] = true
	desired, wanted := s.desiredFiles[rel]
	if !wanted && !s.manifest.Window.All {
		// Nothing indexes this file, so a windowed import does not scatter it
		// into the target vault.
		return nil
	}
	if desired.skip {
		return nil
	}
	s.files.Processed++

	fi, statErr := os.Stat(targetPath)
	exists := statErr == nil
	identical := false
	if exists {
		identical = fi.Size() == hdr.Size
		if identical && desired.modTimeNs > 0 {
			identical = fi.ModTime().UnixNano() == desired.modTimeNs
		}
	}
	switch {
	case exists && identical:
		s.files.SkippedIdentical++
		s.log.Infof("cassette %s already present and identical; skipped", rel)
		return nil
	case exists && s.keepExistingFiles:
		s.files.Conflicts++
		if len(s.files.ConflictSamples) < 20 {
			s.files.ConflictSamples = append(s.files.ConflictSamples, rel)
		}
		s.log.Warnf("cassette %s differs from the existing file; kept existing (--keep-existing-files)", rel)
		return nil
	case !s.apply:
		if exists {
			s.files.Updated++
			s.log.Infof("cassette %s would be updated (existing file differs)", rel)
		} else {
			s.files.Copied++
			s.log.Infof("cassette %s would be copied", rel)
		}
		return nil
	}
	if err := s.writeCassetteFile(hdr, body, targetPath, desired, created, exists); err != nil {
		return err
	}
	if exists {
		s.files.Updated++
		s.log.Infof("cassette %s updated (existing file differed)", rel)
	} else {
		s.files.Copied++
		s.log.Infof("cassette %s copied", rel)
	}
	return nil
}

func (s *importState) writeCassetteFile(hdr *tar.Header, body io.Reader, targetPath string, desired desiredFile, created *[]string, existed bool) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create cassette directory: %w", err)
	}
	tmp := targetPath + ".importing"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, targetPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	mt := hdr.ModTime
	if desired.modTimeNs > 0 {
		mt = time.Unix(0, desired.modTimeNs)
	}
	// The index compares mtime and size to decide whether a file is new; giving
	// the copy the recorded mtime keeps the next Sync from re-indexing the whole
	// imported set.
	if err := os.Chtimes(targetPath, mt, mt); err != nil {
		return err
	}
	if !existed {
		*created = append(*created, targetPath)
	}
	return nil
}
