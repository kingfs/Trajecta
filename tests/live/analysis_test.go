package live

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// TestLiveBatchReanalyze drives the reanalysis pipeline through the Monitor API
// and waits for the child trace jobs to finish, which is what proves the async
// worker actually drains them.
func TestLiveBatchReanalyze(t *testing.T) {
	requireLive(t)

	// Seed one trace so there is something to reanalyze.
	body := map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "reanalysis probe"}},
		"max_tokens": 64,
	}
	if res := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil); res.Status != http.StatusOK {
		t.Fatalf("seed request: want 200, got %s", res)
	}

	res := monitorRequest(t, http.MethodPost, "/api/analysis/batch/reanalyze", map[string]any{
		"mode":    "sync",
		"limit":   3,
		"reparse": true,
		"scan":    true,
	})
	if res.Status != http.StatusOK && res.Status != http.StatusAccepted && res.Status != http.StatusCreated {
		t.Fatalf("batch reanalyze: want 2xx, got %s", res)
	}

	var parsed struct {
		Job *struct {
			ID     json.Number `json:"id"`
			Status string      `json:"status"`
			Steps  []string    `json:"steps"`
		} `json:"job"`
	}
	decodeJSON(t, res.Body, &parsed)
	if parsed.Job == nil {
		t.Fatalf("batch reanalyze: no job in response: %s", truncate(string(res.Body), 400))
	}

	// Wait for the job list to settle: every child must reach a terminal state.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		list := monitorGET(t, "/api/analysis/jobs?limit=20")
		if list.Status != http.StatusOK {
			t.Fatalf("analysis jobs: want 200, got %s", list)
		}
		var jobs struct {
			Items []struct {
				ID     json.Number `json:"id"`
				Status string      `json:"status"`
				Steps  []string    `json:"steps"`
			} `json:"items"`
		}
		decodeJSON(t, list.Body, &jobs)
		if len(jobs.Items) == 0 {
			time.Sleep(time.Second)
			continue
		}
		pending := 0
		failed := 0
		for _, job := range jobs.Items {
			switch job.Status {
			case "queued", "running", "pending":
				pending++
			case "failed", "error":
				failed++
			}
		}
		if failed > 0 {
			t.Errorf("analysis jobs: %d job(s) failed: %s", failed, truncate(string(list.Body), 600))
			return
		}
		if pending == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Errorf("analysis jobs did not reach a terminal state within 60s")
}
