package reanalysis

// Worker Run 循环专属测试：claim→execute→完成/失败状态落库、无任务时
// ticker 空转、ticker 周期内拾取新任务、context 取消退出、BatchSize 上限。
// 使用 sqlite 临时库 + 真实 Service（setup 方式与 service_test.go 一致，
// 但 helper 自包含，避免与并行修改中的既有测试文件耦合）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func newWorkerTestStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dir
}

// writeWorkerTestTrace 写一条最小合法 V3 cassette 并建立索引，供真实
// Service 的 reanalyze 作业解析。
func writeWorkerTestTrace(t *testing.T, st *store.Store, dir, requestID string) string {
	t.Helper()
	reqHead := "POST /v1/responses HTTP/1.1\r\nHost: example.com\r\n\r\n"
	reqBody := `{"model":"gpt-5.1","input":"hello"}`
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"id":"resp_1","object":"response","created_at":1741476777,"status":"completed","model":"gpt-5.1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:     requestID,
			Time:          time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC),
			Model:         "gpt-5.1",
			Provider:      "openai_compatible",
			Operation:     "responses",
			Endpoint:      "/v1/responses",
			URL:           "/v1/responses",
			Method:        "POST",
			StatusCode:    200,
			DurationMs:    20,
			ClientIP:      "127.0.0.1",
			ContentLength: int64(len(reqBody)),
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHead)),
			ReqBodyLen:   int64(len(reqBody)),
			ResHeaderLen: int64(len(resHead)),
			ResBodyLen:   int64(len(resBody)),
		},
	}
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	logPath := filepath.Join(dir, requestID+".http")
	if err := os.WriteFile(logPath, []byte(string(prelude)+reqHead+reqBody+"\n"+resHead+resBody), 0o644); err != nil {
		t.Fatalf("WriteFile(trace) error = %v", err)
	}
	if err := st.UpsertLog(logPath, header); err != nil {
		t.Fatalf("UpsertLog() error = %v", err)
	}
	entry, err := st.GetByRequestID(requestID)
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	return entry.ID
}

// waitWorkerJobStatus 轮询作业状态直到匹配或超时。
func waitWorkerJobStatus(t *testing.T, st *store.Store, id int64, status string) store.AnalysisJobRecord {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, err := st.GetAnalysisJob(id)
		if err != nil {
			t.Fatalf("GetAnalysisJob() error = %v", err)
		}
		if job.Status == status {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %d status = %q, want %q before deadline: %+v", id, job.Status, status, job)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// runWorkerInBackground 启动 Run 并返回取消函数与退出通道。
func runWorkerInBackground(t *testing.T, w *Worker) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker Run did not exit after cancel")
		}
	})
	return cancel, done
}

func TestWorkerRunClaimsExecutesAndPersistsCompletion(t *testing.T) {
	st, dir := newWorkerTestStore(t)
	traceID := writeWorkerTestTrace(t, st, dir, "req-worker-run")
	job, err := New(st, Options{}).EnqueueTraceReanalyze(traceID)
	if err != nil {
		t.Fatalf("EnqueueTraceReanalyze() error = %v", err)
	}
	w := NewWorker(st, WorkerOptions{Interval: 5 * time.Millisecond})
	cancel, done := runWorkerInBackground(t, w)

	got := waitWorkerJobStatus(t, st, job.ID, "completed")
	// claim 必须记录一次 attempt 与 started_at；完成必须落 finished_at 与结果。
	if got.Attempts != 1 || got.StartedAt.IsZero() || got.FinishedAt.IsZero() {
		t.Fatalf("job lifecycle = %+v, want one attempt with started/finished stamps", got)
	}
	if got.LastError != "" {
		t.Fatalf("completed job carries last_error %q", got.LastError)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(got.ResultJSON), &summary); err != nil {
		t.Fatalf("ResultJSON not valid JSON: %v", err)
	}
	if _, ok := summary["observation"]; !ok {
		t.Fatalf("ResultJSON missing observation summary: %s", got.ResultJSON)
	}
	if _, err := st.GetObservationSummary(traceID); err != nil {
		t.Fatalf("GetObservationSummary() error = %v", err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker Run did not exit after cancel")
	}
}

func TestWorkerRunPicksUpJobEnqueuedAfterStart(t *testing.T) {
	st, dir := newWorkerTestStore(t)
	traceID := writeWorkerTestTrace(t, st, dir, "req-worker-late")
	w := NewWorker(st, WorkerOptions{Interval: 5 * time.Millisecond})
	runWorkerInBackground(t, w)

	// 空队列先空转几个 tick，再入队，验证 ticker 驱动的 claim。
	time.Sleep(30 * time.Millisecond)
	job, err := New(st, Options{}).EnqueueTraceReanalyze(traceID)
	if err != nil {
		t.Fatalf("EnqueueTraceReanalyze() error = %v", err)
	}
	got := waitWorkerJobStatus(t, st, job.ID, "completed")
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", got.Attempts)
	}
}

func TestWorkerRunOncePersistsFailure(t *testing.T) {
	st, _ := newWorkerTestStore(t)
	// 目标 trace 不存在：真实 Service 执行失败，必须落 failed + last_error。
	job, err := New(st, Options{}).EnqueueTraceReanalyze("trace-does-not-exist")
	if err != nil {
		t.Fatalf("EnqueueTraceReanalyze() error = %v", err)
	}
	NewWorker(st, WorkerOptions{Interval: time.Hour}).RunOnce(context.Background())

	got, err := st.GetAnalysisJob(job.ID)
	if err != nil {
		t.Fatalf("GetAnalysisJob() error = %v", err)
	}
	if got.Status != "failed" {
		t.Fatalf("job status = %q, want failed: %+v", got.Status, got)
	}
	if got.LastError == "" {
		t.Fatalf("failed job has empty last_error: %+v", got)
	}
	if got.Attempts != 1 || got.FinishedAt.IsZero() {
		t.Fatalf("failed job lifecycle = %+v, want one attempt and finished stamp", got)
	}
	// 失败后不应再被 claim：第二次 RunOnce 不改变 attempts。
	NewWorker(st, WorkerOptions{Interval: time.Hour}).RunOnce(context.Background())
	again, err := st.GetAnalysisJob(job.ID)
	if err != nil {
		t.Fatalf("GetAnalysisJob(second) error = %v", err)
	}
	if again.Attempts != got.Attempts || again.Status != "failed" {
		t.Fatalf("failed job was re-claimed: %+v", again)
	}
}

func TestWorkerRunIdleTickerIsHarmless(t *testing.T) {
	st, _ := newWorkerTestStore(t)
	w := NewWorker(st, WorkerOptions{Interval: 5 * time.Millisecond})
	cancel, done := runWorkerInBackground(t, w)

	// 无任务空转多个 tick：不产生作业、不 panic、取消后立即退出。
	time.Sleep(50 * time.Millisecond)
	jobs, err := st.ListAnalysisJobs("", "", "", 10)
	if err != nil {
		t.Fatalf("ListAnalysisJobs() error = %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("idle worker created jobs: %+v", jobs)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle worker Run did not exit after cancel")
	}
}

func TestWorkerRunExitsOnCanceledContext(t *testing.T) {
	st, _ := newWorkerTestStore(t)
	w := NewWorker(st, WorkerOptions{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run with canceled context did not return")
	}
}

func TestWorkerRunOnceRespectsBatchSize(t *testing.T) {
	st, dir := newWorkerTestStore(t)
	traceID := writeWorkerTestTrace(t, st, dir, "req-worker-batch")
	svc := New(st, Options{})
	ids := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		job, err := svc.EnqueueTraceReanalyze(traceID)
		if err != nil {
			t.Fatalf("EnqueueTraceReanalyze() error = %v", err)
		}
		ids = append(ids, job.ID)
	}
	NewWorker(st, WorkerOptions{Interval: time.Hour, BatchSize: 1}).RunOnce(context.Background())

	completed := 0
	queued := 0
	for _, id := range ids {
		job, err := st.GetAnalysisJob(id)
		if err != nil {
			t.Fatalf("GetAnalysisJob() error = %v", err)
		}
		switch job.Status {
		case "completed":
			completed++
		case "queued":
			queued++
		default:
			t.Fatalf("unexpected job status %q: %+v", job.Status, job)
		}
	}
	if completed != 1 || queued != 2 {
		t.Fatalf("BatchSize=1 claimed completed=%d queued=%d, want 1/2", completed, queued)
	}
}
