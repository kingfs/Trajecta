package store

// OutputDir reports the configured trace output directory: the directory that
// holds the raw `.http` cassettes and the derived local state next to them (the
// SQLite index and the local secret key). Read-path caches place their files
// under it so they follow the same `trace.output_dir` as everything else and do
// not need their own configuration key.
func (s *Store) OutputDir() string {
	if s == nil {
		return ""
	}
	return s.outputDir
}
