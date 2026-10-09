package store

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// The push is only as good as its coverage: a write path that forgets to publish
// leaves a page that silently stops updating, which is exactly the bug the
// console's old refresh timers hid. These are the two paths that run in the
// background rather than from a click - the reanalysis pass that fills in a
// usage trailer, and the parse worker that moves a job - so both are pinned here.
func TestBackgroundWritesPublishTrafficChanges(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	changes, stop := st.SubscribeChanges(16)
	t.Cleanup(stop)

	waitForTopic := func(what string) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case topic := <-changes:
				if topic == ChangeTraffic {
					return
				}
			case <-deadline:
				t.Fatalf("%s published no %s change", what, ChangeTraffic)
			}
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "usage.http")
	if err := os.WriteFile(path, []byte("# trajecta/v3\n"), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
	header.Meta.RequestID = "req_usage"
	header.Meta.Time = time.Now().UTC()
	header.Meta.URL = "https://api.openai.com/v1/chat/completions"
	header.Meta.Method = http.MethodPost
	header.Meta.StatusCode = http.StatusOK
	header.Meta.Model = "gpt-change"
	header.Meta.Endpoint = "/v1/chat/completions"
	if err := st.UpsertLogWithGrouping(path, header, GroupingInfo{}); err != nil {
		t.Fatalf("UpsertLogWithGrouping error = %v", err)
	}
	waitForTopic("a recorded request")
	entry, err := st.GetByRequestID("req_usage")
	if err != nil {
		t.Fatalf("GetByRequestID error = %v", err)
	}

	drain(changes)
	if err := st.UpdateLogUsage(entry.ID, recordfile.UsageInfo{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}); err != nil {
		t.Fatalf("UpdateLogUsage error = %v", err)
	}
	waitForTopic("a usage update")

	drain(changes)
	if err := st.MarkParseJobDone(1); err != nil {
		t.Fatalf("MarkParseJobDone error = %v", err)
	}
	waitForTopic("a parse job transition")
}

func drain(ch <-chan string) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
