// The two worker queues behind those results: the parse jobs that turn a cassette into
// Observation IR rows, and the analysis jobs that run the detectors over them. Both are
// claimed, marked running and finished by their worker, so the claim path and the state
// transitions live together here.

package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type AnalysisJobRecord struct {
	ID          int64
	JobType     string
	TargetType  string
	TargetID    string
	Status      string
	StepsJSON   string
	RequestJSON string
	ResultJSON  string
	LastError   string
	Attempts    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
}

type ParseJobRecord struct {
	ID        int64
	TraceID   string
	Status    string
	Priority  string
	Attempts  int
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Store) EnqueueParseJob(traceID string) error {
	return s.EnqueueParseJobInLane(traceID, ParsePriorityFresh)
}

// EnqueueParseJobInLane queues one trace in the given lane.
func (s *Store) EnqueueParseJobInLane(traceID string, lane string) error {
	if strings.TrimSpace(traceID) == "" {
		return errors.New("enqueue parse job: trace id is required")
	}
	_, err := s.EnqueueParseJobsInLane([]string{traceID}, lane)
	return err
}

// enqueueParseJobBatchSize bounds one multi-row INSERT. Four bind parameters per
// row keeps a batch well under both drivers' limits (SQLite 32766, Postgres
// 65535), and a historical chunk of 1000 traces becomes two statements instead
// of a thousand.
const enqueueParseJobBatchSize = 500

// EnqueueParseJobsInLane queues many traces in one statement per batch.
//
// A trace that already has a job is moved to the requested lane: a fresh trace
// promoted over a pending backfill job must not wait behind the historical
// queue, and a trace that is already fresh must not be demoted by a historical
// sweep that happens to see it before the parser does. `(trace_id)` is unique,
// so re-enqueueing resets the existing row instead of adding a second one.
func (s *Store) EnqueueParseJobsInLane(traceIDs []string, lane string) (int, error) {
	lane = ParseJobLane(lane)
	now := time.Now().UTC()
	queued := 0
	for start := 0; start < len(traceIDs); start += enqueueParseJobBatchSize {
		end := start + enqueueParseJobBatchSize
		if end > len(traceIDs) {
			end = len(traceIDs)
		}
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*4)
		for _, traceID := range traceIDs[start:end] {
			traceID = strings.TrimSpace(traceID)
			if traceID == "" {
				continue
			}
			values = append(values, "(?, 'queued', ?, 0, ?, ?)")
			args = append(args, traceID, lane, now, now)
		}
		if len(values) == 0 {
			continue
		}
		result, err := s.db.Exec(`
			INSERT INTO parse_jobs (trace_id, status, priority, attempts, created_at, updated_at)
			VALUES `+strings.Join(values, ",")+`
			ON CONFLICT (trace_id) DO UPDATE
			SET status = 'queued', attempts = 0, last_error = '', updated_at = excluded.updated_at,
			    -- A trace that is already waiting (or running) in the fresh lane keeps
			    -- its place: the historical sweep must not demote work the proxy just
			    -- queued. Any other prior lane is replaced, which is what lets the
			    -- sweep pick up a trace whose earlier parse finished before project
			    -- attribution existed.
			    priority = CASE
			        WHEN parse_jobs.priority = 'fresh' AND parse_jobs.status IN ('queued', 'running') THEN 'fresh'
			        ELSE excluded.priority
			    END
		`, args...)
		if err != nil {
			return queued, err
		}
		if count, err := result.RowsAffected(); err == nil {
			queued += int(count)
		} else {
			queued += len(values)
		}
	}
	return queued, nil
}

// ListParseJobs is a test seam: the production worker claims jobs through
// ClaimParseJobs, and tests use this read-only query to assert queue states
// (for example failed jobs) without mutating them.
func (s *Store) ListParseJobs(status string, limit int) ([]ParseJobRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, trace_id, status, priority, attempts, last_error, created_at, updated_at
		FROM parse_jobs
		WHERE status = ?
		ORDER BY updated_at ASC, id ASC
		LIMIT ?
	`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanParseJobs(rows)
}

func (s *Store) getParseJob(id int64) (ParseJobRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, trace_id, status, priority, attempts, last_error, created_at, updated_at
		FROM parse_jobs
		WHERE id = ?
	`, id)
	var job ParseJobRecord
	var createdAt, updatedAt any
	if err := row.Scan(&job.ID, &job.TraceID, &job.Status, &job.Priority, &job.Attempts, &job.LastError, &createdAt, &updatedAt); err != nil {
		return ParseJobRecord{}, err
	}
	var err error
	if job.CreatedAt, err = timeParseValue(createdAt); err != nil {
		return ParseJobRecord{}, err
	}
	if job.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
		return ParseJobRecord{}, err
	}
	return job, nil
}

// ClaimParseJobs moves up to limit queued parse jobs to running in one statement
// and returns them. The worker used to list queued jobs and then mark each one
// running, which let a second process (another `trajecta serve`, or a CLI run)
// pick up the same job in between. The claim is a single UPDATE with RETURNING:
// Postgres takes the oldest queued rows with FOR UPDATE SKIP LOCKED so concurrent
// claimers step over each other's rows, SQLite has no row locks so its statement
// relies on the database write lock plus claimMu and gives up the row when the
// outer status predicate no longer matches.
func (s *Store) ClaimParseJobs(limit int) ([]ParseJobRecord, error) {
	return s.claimParseJobsInLane(ParsePriorityFresh, limit)
}

// ClaimBackfillParseJobs claims from the historical lane. The two lanes are
// claimed separately so a long historical queue can never delay a trace the
// proxy recorded a moment ago.
func (s *Store) ClaimBackfillParseJobs(limit int) ([]ParseJobRecord, error) {
	return s.claimParseJobsInLane(ParsePriorityBackfill, limit)
}

func (s *Store) claimParseJobsInLane(lane string, limit int) ([]ParseJobRecord, error) {
	if limit <= 0 {
		limit = 10
	}
	lane = ParseJobLane(lane)
	now := time.Now().UTC()
	s.shared.claimMu.Lock()
	defer s.shared.claimMu.Unlock()
	rows, err := s.db.Query(`
		UPDATE parse_jobs
		SET status = 'running', attempts = attempts + 1, updated_at = ?
		WHERE status = 'queued' AND priority = ? AND id IN (
			SELECT id FROM parse_jobs
			WHERE status = 'queued' AND priority = ?
			ORDER BY updated_at ASC, id ASC
			LIMIT ?`+s.claimRowLockSQL()+`
		)
		RETURNING id, trace_id, status, priority, attempts, last_error, created_at, updated_at
	`, now, lane, lane, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanParseJobs(rows)
}

// CountParseJobs reports how many jobs sit in one state (optionally one lane).
// The Monitor's queue panel and the backfill planner both need this without
// pulling rows.
func (s *Store) CountParseJobs(status string, lane string) (int, error) {
	query := `SELECT COUNT(*) FROM parse_jobs WHERE status = ?`
	args := []any{strings.TrimSpace(status)}
	if strings.TrimSpace(lane) != "" {
		query += ` AND priority = ?`
		args = append(args, ParseJobLane(lane))
	}
	var count int
	if err := s.db.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ReleaseParseJob returns a claimed job to the queue without counting the
// attempt.
//
// The claim increments attempts, and that is right for a parse that failed on
// its own. A parse interrupted by shutdown is not the trace's fault, though: if
// it were recorded as an attempt, three redeploys during active parses would
// bury a healthy trace as failed. This un-counts the attempt the claim just
// added.
func (s *Store) ReleaseParseJob(id int64) error {
	_, err := s.db.Exec(`
		UPDATE parse_jobs
		SET status = 'queued',
		    attempts = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END,
		    updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	s.notifyChange(ChangeTraffic)
	return nil
}

// RequeueOrFailParseJob records a failed attempt.
//
// A parse error is usually permanent (a corrupt cassette) but a database hiccup
// is not, so a trace gets ParseMaxAttempts claims before it is buried. A requeued
// job keeps its lane and goes to the back of that lane (ordering is by
// updated_at), which spaces retries out without a separate backoff column.
func (s *Store) RequeueOrFailParseJob(id int64, maxAttempts int, lastError string) (string, error) {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	job, err := s.getParseJob(id)
	if err != nil {
		return "", err
	}
	if job.Attempts < maxAttempts {
		_, err := s.db.Exec(`
			UPDATE parse_jobs
			SET status = 'queued', last_error = ?, updated_at = ?
			WHERE id = ?
		`, textPreview(lastError, 2000), time.Now().UTC(), id)
		if err != nil {
			return "", err
		}
		s.notifyChange(ChangeTraffic)
		return "queued", nil
	}
	if err := s.MarkParseJobFailed(id, lastError); err != nil {
		return "", err
	}
	return "failed", nil
}

// ReclaimStaleParseJobs returns jobs whose worker died mid-run to the queue.
//
// The claim moves a row to running and nothing moves it back: a process killed
// while parsing (a redeploy, an OOM) left its job running forever, so that trace
// was never re-parsed and never got an observation, a finding, a resource index
// or a project attribution. 734 such rows had accumulated on the reference
// deployment, and they are what kept a project backfill from doing anything.
//
// The cutoff is what makes this safe next to a live worker: no parse takes
// minutes, so a row that has been running past the cutoff belongs to a process
// that is gone. Re-parsing is idempotent (every write is an upsert by trace).
func (s *Store) ReclaimStaleParseJobs(olderThan time.Time) (int64, error) {
	if olderThan.IsZero() {
		return 0, nil
	}
	result, err := s.db.Exec(`
		UPDATE parse_jobs
		SET status = 'queued', last_error = ?, updated_at = ?
		WHERE status = 'running' AND updated_at < ?
	`, "reclaimed after an interrupted run", time.Now().UTC(), olderThan.UTC())
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, nil
	}
	if count > 0 {
		s.notifyChange(ChangeTraffic)
	}
	return count, nil
}

// ReclaimStaleAnalysisJobs is the analysis-job counterpart of
// ReclaimStaleParseJobs, with the same cutoff reasoning.
func (s *Store) ReclaimStaleAnalysisJobs(olderThan time.Time) (int64, error) {
	if olderThan.IsZero() {
		return 0, nil
	}
	result, err := s.db.Exec(`
		UPDATE analysis_jobs
		SET status = 'queued', last_error = ?, updated_at = ?
		WHERE status = 'running' AND updated_at < ?
	`, "reclaimed after an interrupted run", time.Now().UTC(), olderThan.UTC())
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, nil
	}
	if count > 0 {
		s.notifyChange(ChangeTraffic)
	}
	return count, nil
}

func (s *Store) MarkParseJobDone(id int64) error {
	s.notifyChange(ChangeTraffic)
	_, err := s.db.Exec(`
		UPDATE parse_jobs
		SET status = 'parsed', last_error = '', updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), id)
	return err
}

func (s *Store) MarkParseJobFailed(id int64, lastError string) error {
	s.notifyChange(ChangeTraffic)
	_, err := s.db.Exec(`
		UPDATE parse_jobs
		SET status = 'failed', last_error = ?, updated_at = ?
		WHERE id = ?
	`, textPreview(lastError, 2000), time.Now().UTC(), id)
	if err != nil {
		return err
	}
	job, err := s.getParseJob(id)
	if err != nil {
		return err
	}
	_, err = s.UpsertSystemEvent(systemEventForParseFailure(job))
	return err
}

func (s *Store) CreateAnalysisJob(job AnalysisJobRecord) (AnalysisJobRecord, error) {
	s.notifyChange(ChangeTraffic)
	if strings.TrimSpace(job.JobType) == "" {
		return AnalysisJobRecord{}, errors.New("create analysis job: job type is required")
	}
	if strings.TrimSpace(job.TargetType) == "" {
		return AnalysisJobRecord{}, errors.New("create analysis job: target type is required")
	}
	if strings.TrimSpace(job.TargetID) == "" {
		return AnalysisJobRecord{}, errors.New("create analysis job: target id is required")
	}
	if strings.TrimSpace(job.Status) == "" {
		job.Status = "queued"
	}
	if strings.TrimSpace(job.StepsJSON) == "" {
		job.StepsJSON = "[]"
	}
	if strings.TrimSpace(job.RequestJSON) == "" {
		job.RequestJSON = "{}"
	}
	if strings.TrimSpace(job.ResultJSON) == "" {
		job.ResultJSON = "{}"
	}
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = job.CreatedAt
	}
	if s.driver == "postgres" {
		err := s.db.QueryRow(`
			INSERT INTO analysis_jobs (
				job_type, target_type, target_id, status, steps_json, request_json, result_json, last_error,
				attempts, created_at, updated_at, started_at, finished_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING id
		`, job.JobType, job.TargetType, job.TargetID, job.Status, job.StepsJSON, job.RequestJSON, job.ResultJSON, job.LastError,
			job.Attempts, job.CreatedAt, job.UpdatedAt, nullableTime(job.StartedAt), nullableTime(job.FinishedAt)).Scan(&job.ID)
		if err != nil {
			return AnalysisJobRecord{}, err
		}
		return job, nil
	}
	result, err := s.db.Exec(`
		INSERT INTO analysis_jobs (
			job_type, target_type, target_id, status, steps_json, request_json, result_json, last_error,
			attempts, created_at, updated_at, started_at, finished_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, job.JobType, job.TargetType, job.TargetID, job.Status, job.StepsJSON, job.RequestJSON, job.ResultJSON, job.LastError,
		job.Attempts, job.CreatedAt, job.UpdatedAt, nullableTime(job.StartedAt), nullableTime(job.FinishedAt))
	if err != nil {
		return AnalysisJobRecord{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AnalysisJobRecord{}, err
	}
	job.ID = id
	return job, nil
}

func (s *Store) GetAnalysisJob(id int64) (AnalysisJobRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, job_type, target_type, target_id, status, steps_json, request_json, result_json, last_error,
			attempts, created_at, updated_at, started_at, finished_at
		FROM analysis_jobs
		WHERE id = ?
	`, id)
	return scanAnalysisJob(row)
}

func (s *Store) ListAnalysisJobs(status string, targetType string, targetID string, limit int) ([]AnalysisJobRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT id, job_type, target_type, target_id, status, steps_json, request_json, result_json, last_error,
			attempts, created_at, updated_at, started_at, finished_at
		FROM analysis_jobs
		WHERE 1 = 1
	`
	var args []any
	if status = strings.TrimSpace(status); status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if targetType = strings.TrimSpace(targetType); targetType != "" {
		query += ` AND target_type = ?`
		args = append(args, targetType)
	}
	if targetID = strings.TrimSpace(targetID); targetID != "" {
		query += ` AND target_id = ?`
		args = append(args, targetID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnalysisJobs(rows)
}

func (s *Store) MarkAnalysisJobRunning(id int64) error {
	s.notifyChange(ChangeTraffic)
	_, err := s.db.Exec(`
		UPDATE analysis_jobs
		SET status = 'running', attempts = attempts + 1, started_at = COALESCE(started_at, ?), updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), time.Now().UTC(), id)
	return err
}

// ClaimAnalysisJobsForWorker is the analysis-job counterpart of ClaimParseJobs:
// it flips up to limit queued jobs to running in one UPDATE ... RETURNING so the
// server worker and a second process cannot execute the same job twice. The
// reanalysis service no longer marks a job running itself; the claim already
// incremented attempts and stamped started_at.
func (s *Store) ClaimAnalysisJobsForWorker(limit int) ([]AnalysisJobRecord, error) {
	if limit <= 0 {
		limit = 10
	}
	now := time.Now().UTC()
	s.shared.claimMu.Lock()
	defer s.shared.claimMu.Unlock()
	rows, err := s.db.Query(`
		UPDATE analysis_jobs
		SET status = 'running', attempts = attempts + 1,
			started_at = COALESCE(started_at, ?), updated_at = ?
		WHERE status = 'queued' AND id IN (
			SELECT id FROM analysis_jobs
			WHERE status = 'queued'
			ORDER BY updated_at ASC, id ASC
			LIMIT ?`+s.claimRowLockSQL()+`
		)
		RETURNING id, job_type, target_type, target_id, status, steps_json, request_json, result_json, last_error,
			attempts, created_at, updated_at, started_at, finished_at
	`, now, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnalysisJobs(rows)
}

func (s *Store) MarkAnalysisJobCompleted(id int64, resultJSON string) error {
	s.notifyChange(ChangeTraffic)
	if strings.TrimSpace(resultJSON) == "" {
		resultJSON = "{}"
	}
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE analysis_jobs
		SET status = 'completed', result_json = ?, last_error = '', finished_at = ?, updated_at = ?
		WHERE id = ?
	`, resultJSON, now, now, id)
	return err
}

func (s *Store) MarkAnalysisJobFailed(id int64, lastError string) error {
	s.notifyChange(ChangeTraffic)
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE analysis_jobs
		SET status = 'failed', last_error = ?, finished_at = ?, updated_at = ?
		WHERE id = ?
	`, textPreview(lastError, 2000), now, now, id)
	if err != nil {
		return err
	}
	job, err := s.GetAnalysisJob(id)
	if err != nil {
		return err
	}
	_, err = s.UpsertSystemEvent(systemEventForAnalysisJobFailure(job))
	return err
}

func (s *Store) MarkAnalysisJobCanceled(id int64) error {
	s.notifyChange(ChangeTraffic)
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE analysis_jobs
		SET status = 'canceled', finished_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('queued', 'running')
	`, now, now, id)
	return err
}

func scanParseJobs(rows *sql.Rows) ([]ParseJobRecord, error) {
	var out []ParseJobRecord
	for rows.Next() {
		var job ParseJobRecord
		var createdAt, updatedAt any
		if err := rows.Scan(&job.ID, &job.TraceID, &job.Status, &job.Priority, &job.Attempts, &job.LastError, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		var err error
		if job.CreatedAt, err = timeParseValue(createdAt); err != nil {
			return nil, err
		}
		if job.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

type analysisJobScanner interface {
	Scan(dest ...any) error
}

func scanAnalysisJob(row analysisJobScanner) (AnalysisJobRecord, error) {
	var job AnalysisJobRecord
	var createdAt, updatedAt, startedAt, finishedAt any
	if err := row.Scan(&job.ID, &job.JobType, &job.TargetType, &job.TargetID, &job.Status, &job.StepsJSON, &job.RequestJSON, &job.ResultJSON, &job.LastError,
		&job.Attempts, &createdAt, &updatedAt, &startedAt, &finishedAt); err != nil {
		return AnalysisJobRecord{}, err
	}
	var err error
	if job.CreatedAt, err = timeParseValue(createdAt); err != nil {
		return AnalysisJobRecord{}, err
	}
	if job.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
		return AnalysisJobRecord{}, err
	}
	if job.StartedAt, err = timeParseNullableValue(startedAt); err != nil {
		return AnalysisJobRecord{}, err
	}
	if job.FinishedAt, err = timeParseNullableValue(finishedAt); err != nil {
		return AnalysisJobRecord{}, err
	}
	return job, nil
}

func scanAnalysisJobs(rows *sql.Rows) ([]AnalysisJobRecord, error) {
	var out []AnalysisJobRecord
	for rows.Next() {
		job, err := scanAnalysisJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}
