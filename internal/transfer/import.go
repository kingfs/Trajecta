package transfer

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

	rootPath := outputDir
	if rootPath == "" {
		rootPath = "."
	}
	if opts.Apply {
		if err := os.MkdirAll(rootPath, 0o700); err != nil {
			return nil, fmt.Errorf("create target output directory: %w", err)
		}
	}
	outputRoot, rootErr := os.OpenRoot(rootPath)
	if rootErr != nil {
		if opts.Apply || !errors.Is(rootErr, os.ErrNotExist) {
			return nil, fmt.Errorf("open target output directory: %w", rootErr)
		}
	} else {
		defer outputRoot.Close()
	}

	fileChanges := []cassetteFileChange{}
	filesPlaced := map[string]bool{}
	targetsLoaded := false
	var targets []targetChannel

	run := func(view *store.Store, apply bool) error {
		s := newImportState(ctx, view, log, outputDir, outputRoot, apply, result, opts.ChannelMap)
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
				rel, err := validateCassetteRelativePath(strings.TrimPrefix(hdr.Name, entryCassettesPrefix))
				if err != nil {
					return fmt.Errorf("unsafe cassette entry %q: %w", hdr.Name, err)
				}
				if err := s.placeCassette(hdr, cur.body(), rel, &fileChanges, filesPlaced); err != nil {
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
		if rollbackErr := rollbackCassetteFiles(outputRoot, fileChanges); rollbackErr != nil {
			log.Warnf("cassette rollback was incomplete: %v", rollbackErr)
		}
		return nil, err
	}
	if cleanupErr := removeCassetteBackups(outputRoot, fileChanges); cleanupErr != nil {
		log.Warnf("could not remove cassette import backups: %v", cleanupErr)
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
	// Project attribution is derived from the cassettes, so the bundle cannot
	// carry it. Queue a bounded reparse instead of leaving the project page
	// empty until an operator remembers the backfill endpoint; the parse worker
	// drains the queue when `serve` starts.
	if queued, err := st.EnqueueProjectBackfill(time.Time{}, 5000); err != nil {
		result.warn("project attribution backfill was not queued (%v); run POST /api/projects/backfill", err)
		log.Warnf("project attribution backfill not queued: %v", err)
	} else if queued > 0 {
		result.ProjectBackfillQueued = queued
		log.Infof("queued %d parse jobs to rebuild project attribution after import", queued)
	}
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
	case "project_aliases":
		return s.handleProjectAliases(body, size)
	case "model_catalog":
		return s.handleModelCatalog(body, size)
	case "app_settings":
		return s.handleAppSettings(body, size)
	case "channel_probe_runs":
		return s.handleChannelProbeRuns(body, size)
	case "datasets", "eval_runs", "experiment_runs", "dataset_examples", "scores":
		// Retired eval tables: an old bundle still carries them. Read the
		// entry to the end, report it as legacy-skipped and keep going.
		return s.skipLegacyEvalTable(table, body, size)
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
func (s *importState) placeCassette(hdr *tar.Header, body io.Reader, rel string, changes *[]cassetteFileChange, placed map[string]bool) error {
	safeRel, err := validateCassetteRelativePath(rel)
	if err != nil {
		return fmt.Errorf("unsafe cassette path %q: %w", rel, err)
	}
	rel = safeRel
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

	fi, statErr := s.statCassette(rel)
	exists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect cassette %s: %w", rel, statErr)
	}
	if exists {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("cassette target %s is a symlink", rel)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("cassette target %s is not a regular file", rel)
		}
	}
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
	if err := s.writeCassetteFile(hdr, body, rel, desired, exists, changes); err != nil {
		return fmt.Errorf("write cassette %s: %w", rel, err)
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

func (s *importState) statCassette(rel string) (os.FileInfo, error) {
	if s.outputRoot == nil {
		// A dry run must not create a missing output directory just to inspect it.
		return nil, os.ErrNotExist
	}
	if err := checkCassettePathComponents(s.outputRoot, rel); err != nil {
		return nil, err
	}
	return s.outputRoot.Lstat(rel)
}

func (s *importState) writeCassetteFile(hdr *tar.Header, body io.Reader, rel string, desired desiredFile, existed bool, changes *[]cassetteFileChange) error {
	if s.outputRoot == nil {
		return errors.New("target output directory is not open")
	}
	if hdr.Size < 0 {
		return errors.New("cassette entry has a negative size")
	}
	if changes == nil {
		return errors.New("cassette rollback journal is not configured")
	}
	if err := ensureCassetteParentDirs(s.outputRoot, rel); err != nil {
		return fmt.Errorf("create cassette directory: %w", err)
	}

	tmpRel, f, err := createCassetteTemp(s.outputRoot, rel)
	if err != nil {
		return err
	}
	tmpReady := false
	defer func() {
		if !tmpReady {
			_ = s.outputRoot.Remove(tmpRel)
		}
	}()
	n, copyErr := io.Copy(f, body)
	if copyErr != nil {
		_ = f.Close()
		return copyErr
	}
	if n != hdr.Size {
		_ = f.Close()
		return fmt.Errorf("cassette entry size mismatch: read %d bytes, expected %d", n, hdr.Size)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync temporary cassette: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temporary cassette: %w", err)
	}

	mt := hdr.ModTime
	if desired.modTimeNs > 0 {
		mt = time.Unix(0, desired.modTimeNs)
	}
	// Set the indexed mtime while the file is still temporary. If Chtimes
	// fails, the old destination is untouched and the defer removes the temp.
	if err := s.outputRoot.Chtimes(tmpRel, mt, mt); err != nil {
		return fmt.Errorf("set cassette mtime: %w", err)
	}

	if existed {
		if err := checkCassettePathComponents(s.outputRoot, rel); err != nil {
			return err
		}
		fi, err := s.outputRoot.Lstat(rel)
		if err != nil {
			return fmt.Errorf("recheck cassette target: %w", err)
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return fmt.Errorf("cassette target %s changed to a non-regular file", rel)
		}
		// The original may predate restrictive cassette modes. Tighten it before
		// staging so a crash between rename and rollback cannot leave a readable
		// backup beside the private temporary file.
		if err := s.outputRoot.Chmod(rel, 0o600); err != nil {
			return fmt.Errorf("restrict existing cassette permissions: %w", err)
		}
		backupRel, err := uniqueCassetteSibling(s.outputRoot, rel, "backup")
		if err != nil {
			return err
		}
		if err := s.outputRoot.Rename(rel, backupRel); err != nil {
			return fmt.Errorf("stage existing cassette for rollback: %w", err)
		}
		*changes = append(*changes, cassetteFileChange{target: rel, backup: backupRel})
	} else {
		if err := checkCassettePathComponents(s.outputRoot, rel); err != nil {
			return err
		}
		if _, err := s.outputRoot.Lstat(rel); err == nil {
			return fmt.Errorf("cassette target %s appeared during import", rel)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recheck cassette target: %w", err)
		}
	}
	if err := s.outputRoot.Rename(tmpRel, rel); err != nil {
		return fmt.Errorf("install cassette: %w", err)
	}
	tmpReady = true
	if !existed {
		*changes = append(*changes, cassetteFileChange{target: rel})
	}
	return nil
}

type cassetteFileChange struct {
	target string
	backup string
}

// validateCassetteRelativePath rejects path forms that could escape the output
// root or mean different things on Unix and Windows. Backslashes are normalized
// to separators first so a Windows-style traversal cannot bypass validation.
func validateCassetteRelativePath(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	if rel == "" || strings.ContainsRune(rel, 0) || strings.HasPrefix(rel, "/") {
		return "", errors.New("path must be a non-empty relative path")
	}
	if strings.Contains(rel, ":") || !fs.ValidPath(rel) || rel == "." {
		return "", errors.New("path is not a clean relative path")
	}
	if filepath.IsAbs(filepath.FromSlash(rel)) || filepath.VolumeName(filepath.FromSlash(rel)) != "" {
		return "", errors.New("path must not have an absolute or volume prefix")
	}
	return rel, nil
}

func checkCassettePathComponents(root *os.Root, rel string) error {
	if root == nil {
		return errors.New("target output directory is not open")
	}
	rel, err := validateCassetteRelativePath(rel)
	if err != nil {
		return err
	}
	parts := strings.Split(rel, "/")
	current := ""
	for i, part := range parts {
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %q is a symlink", current)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("path component %q is not a directory", current)
		}
	}
	return nil
}

func ensureCassetteParentDirs(root *os.Root, rel string) error {
	if root == nil {
		return errors.New("target output directory is not open")
	}
	rel, err := validateCassetteRelativePath(rel)
	if err != nil {
		return err
	}
	parts := strings.Split(rel, "/")
	current := ""
	for _, part := range parts[:len(parts)-1] {
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("cassette directory %q is not a real directory", current)
		}
		if err := root.Chmod(current, 0o700); err != nil {
			return fmt.Errorf("restrict cassette directory permissions: %w", err)
		}
	}
	return nil
}

func createCassetteTemp(root *os.Root, target string) (string, *os.File, error) {
	for attempt := 0; attempt < 10; attempt++ {
		tmpRel, err := randomCassetteSibling(target, "import")
		if err != nil {
			return "", nil, err
		}
		f, err := root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		return tmpRel, f, nil
	}
	return "", nil, errors.New("could not allocate a unique cassette temporary file")
}

func uniqueCassetteSibling(root *os.Root, target, kind string) (string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		candidate, err := randomCassetteSibling(target, kind)
		if err != nil {
			return "", err
		}
		_, err = root.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate a unique cassette backup path")
}

func randomCassetteSibling(target, kind string) (string, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate cassette temporary name: %w", err)
	}
	name := ".trajecta-import-" + kind + "-" + hex.EncodeToString(nonce[:])
	if index := strings.LastIndex(target, "/"); index >= 0 {
		return target[:index+1] + name, nil
	}
	return name, nil
}

func rollbackCassetteFiles(root *os.Root, changes []cassetteFileChange) error {
	if len(changes) == 0 {
		return nil
	}
	if root == nil {
		return errors.New("cannot restore cassette files: output directory is not open")
	}
	var rollbackErr error
	for i := len(changes) - 1; i >= 0; i-- {
		change := changes[i]
		if err := root.Remove(change.target); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove %s: %w", change.target, err))
			continue
		}
		if change.backup != "" {
			if err := root.Rename(change.backup, change.target); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore %s: %w", change.target, err))
			}
		}
	}
	return rollbackErr
}

func removeCassetteBackups(root *os.Root, changes []cassetteFileChange) error {
	var cleanupErr error
	for _, change := range changes {
		if change.backup == "" || root == nil {
			continue
		}
		if err := root.Remove(change.backup); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove %s: %w", change.backup, err))
		}
	}
	return cleanupErr
}
