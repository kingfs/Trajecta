package recorder

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// TestAsyncMaskAuditWriterRetriesTransientFailure pins the retry contract: a
// database that is briefly unavailable must not turn one audit batch into a lost
// notification, and the retry count must be observable.
func TestAsyncMaskAuditWriterRetriesTransientFailure(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	writer := newAsyncMaskAuditWriter(func(records []store.CredentialMaskAuditRecord) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return errors.New("database temporarily unavailable")
		}
		return nil
	}, 1, 4)
	writer.start()
	defer func() {
		_ = writer.close(context.Background(), 2*time.Second)
	}()

	if err := writer.submit([]store.CredentialMaskAuditRecord{{TraceID: "trace-retry", Rule: "pii.chinese_id"}}); err != nil {
		t.Fatalf("submit() error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := calls >= 3
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("write attempts = %d, want the third attempt to succeed", calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stats := writer.stats(); stats.Retried < 2 {
		t.Errorf("retried = %d, want at least two retries", stats.Retried)
	}
}

// TestMaskAuditSideQueueWritesRowsAndEvents is the end-to-end contract the event
// feed depends on: finalising a cassette with a masked value writes the audit row
// and its notification through the side queue, without the finalizer calling the
// store directly.
func TestMaskAuditSideQueueWritesRowsAndEvents(t *testing.T) {
	const original = "11010519491231002X"
	const standIn = "110101195001011234"

	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	rec := New(dir, false, st)
	rec.StartFinalizeWorkers()
	defer func() {
		if err := rec.Close(context.Background()); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	info, err := rec.PrepareLogFile(req, "https://api.openai.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}
	info.Header.Meta.StatusCode = 200
	info.Header.Meta.DurationMs = 12
	info.Header.Layout.ResHeaderLen = int64(len("HTTP/1.1 200 OK\r\n\r\n"))
	info.Header.Layout.ResBodyLen = int64(len(`{"ok":true}`))
	info.MaskAudit = []store.CredentialMaskAuditRecord{{
		EventIndex: 0,
		Rule:       "pii.chinese_id",
		Kind:       "national_id",
		Confidence: "high",
		Score:      95,
		Start:      42,
		End:        42 + len(original),
		Original:   original,
		StandIn:    standIn,
	}}
	if _, err := info.File.Write([]byte("\nHTTP/1.1 200 OK\r\n\r\n{\"ok\":true}")); err != nil {
		t.Fatalf("write cassette error = %v", err)
	}
	if err := rec.SubmitLogFile(info); err != nil {
		t.Fatalf("SubmitLogFile() error = %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		audits, auditErr := st.ListCredentialMaskAudits(store.CredentialMaskAuditFilter{TraceID: ""})
		if auditErr != nil {
			t.Fatalf("ListCredentialMaskAudits() error = %v", auditErr)
		}
		event, eventErr := st.GetSystemEventByFingerprint("secretmask:rule:pii_chinese_id")
		if auditErr == nil && eventErr == nil && audits.Total == 1 {
			if event.TraceID != audits.Items[0].TraceID {
				t.Fatalf("event trace = %q, audit trace = %q", event.TraceID, audits.Items[0].TraceID)
			}
			if event.Category != "credential_masked" || event.Source != "secretmask" {
				t.Fatalf("event source/category = %q/%q", event.Source, event.Category)
			}
			if audits.Items[0].Original != original || audits.Items[0].StandIn != standIn {
				t.Fatalf("audit pair = (%q, %q)", audits.Items[0].Original, audits.Items[0].StandIn)
			}
			if stats := rec.FinalizeStats(); stats.AuditQueued == 0 {
				t.Errorf("audit_queued = 0, want the side queue to have handled the batch: %+v", stats)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("audit rows = %d, event error = %v; want one row and its event", audits.Total, eventErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
