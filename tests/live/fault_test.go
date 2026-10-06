package live

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The failover / outage / chaos checks run against three auxiliary instances
// that each carry their own configuration:
//
//	tests/live/config/live-failover.yaml  ports 18094/18095  one unreachable + one healthy channel
//	tests/live/config/live-outage.yaml    ports 18096/18097  every channel unreachable
//	tests/live/config/live-chaos.yaml     ports 18092/18093  chaos delay/error rules
//
// They are optional: each test skips unless its base URL (and token) is set, so
// the default suite keeps running against the single container stack.

func altProxyToken(t *testing.T, envVar string) string {
	t.Helper()
	if token := strings.TrimSpace(os.Getenv(envVar)); token != "" {
		return token
	}
	t.Skipf("set %s to the API token of the auxiliary instance", envVar)
	return ""
}

func altURL(t *testing.T, envVar string) string {
	t.Helper()
	value := strings.TrimRight(strings.TrimSpace(os.Getenv(envVar)), "/")
	if value == "" {
		t.Skipf("set %s to the base URL of the auxiliary instance", envVar)
	}
	return value
}

// newestCassetteContaining returns the newest cassette under root whose bytes
// contain marker.
func newestCassetteContaining(t *testing.T, root, marker string) string {
	t.Helper()
	if strings.TrimSpace(root) == "" {
		t.Skip("cassette directory not configured")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var newest string
		var newestMod time.Time
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".http") {
				return nil
			}
			info, statErr := d.Info()
			if statErr != nil {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil || !strings.Contains(string(raw), marker) {
				return nil
			}
			if newest == "" || info.ModTime().After(newestMod) {
				newest, newestMod = path, info.ModTime()
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
		if newest != "" {
			return newest
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("no cassette containing %q under %s", marker, root)
	return ""
}

func TestLiveFailoverToHealthyCandidate(t *testing.T) {
	requireLive(t)
	base := altURL(t, "TRAJECTA_LIVE_FAILOVER_URL")
	token := altProxyToken(t, "TRAJECTA_LIVE_FAILOVER_TOKEN")
	traceDir := strings.TrimSpace(os.Getenv("TRAJECTA_LIVE_FAILOVER_DIR"))

	marker := fmt.Sprintf("failover-%d", time.Now().UnixNano())
	res := request(t, http.MethodPost, base+"/v1/chat/completions", map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly: ok. " + marker}},
		"max_tokens": 128,
	}, map[string]string{"Authorization": "Bearer " + token})

	// The higher-priority channel for this model points at an unreachable
	// address, so a 200 proves the retry loop failed over to the healthy one.
	if res.Status != http.StatusOK {
		t.Fatalf("failover request: want 200 (via the healthy candidate), got %s", res)
	}

	if traceDir == "" {
		t.Skip("set TRAJECTA_LIVE_FAILOVER_DIR to also assert the recorded routing events")
	}
	cassette := newestCassetteContaining(t, traceDir, marker)
	raw, err := os.ReadFile(cassette)
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `"selected_upstream_id":"baizhi-openai"`) {
		t.Errorf("cassette %s did not select the healthy channel", filepath.Base(cassette))
	}
	if !strings.Contains(text, "broken-openai") {
		t.Errorf("cassette %s contains no trace of the unreachable candidate", filepath.Base(cassette))
	}
	// Either the request retried within this exchange (circuit closed), or the
	// circuit was already open and the candidate was skipped up front.
	if !strings.Contains(text, "routing.retry_candidate") && !strings.Contains(text, `"health_state":"open"`) {
		t.Errorf("cassette %s shows neither a retry nor an open circuit: %s", filepath.Base(cassette), truncate(text, 400))
	}

	// Same per-candidate consistency check as
	// TestLiveRoutingCandidatesHealthSelectability, evaluated on this exchange.
	for _, candidate := range routingCandidatesFromCassette(t, cassette) {
		if health, _ := candidate["health_state"].(string); health != "open" {
			continue
		}
		if selectable, _ := candidate["selectable"].(bool); selectable {
			t.Errorf("channel %v reports health_state=open and selectable=true in %s", candidate["id"], filepath.Base(cassette))
		}
	}
}

func TestLiveOutageErrorContract(t *testing.T) {
	requireLive(t)
	base := altURL(t, "TRAJECTA_LIVE_OUTAGE_URL")
	token := altProxyToken(t, "TRAJECTA_LIVE_OUTAGE_TOKEN")
	traceDir := strings.TrimSpace(os.Getenv("TRAJECTA_LIVE_OUTAGE_DIR"))

	marker := fmt.Sprintf("outage-%d", time.Now().UnixNano())
	res := request(t, http.MethodPost, base+"/v1/chat/completions", map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly: ok. " + marker}},
		"max_tokens": 128,
	}, map[string]string{"Authorization": "Bearer " + token})

	if res.Status != http.StatusBadGateway {
		t.Fatalf("outage request: want 502 when every candidate is unreachable, got %s", res)
	}
	if !strings.Contains(string(res.Body), "connection refused") {
		t.Errorf("outage body %q does not carry the transport error", truncate(string(res.Body), 200))
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("outage error Content-Type = %q, want a JSON error envelope", ct)
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(res.Body, &envelope); err != nil {
		t.Errorf("outage error body is not a JSON error envelope: %v (%s)", err, truncate(string(res.Body), 200))
	} else if envelope.Error.Message == "" {
		t.Errorf("outage error envelope carries no message: %s", truncate(string(res.Body), 200))
	}

	if traceDir == "" {
		t.Skip("set TRAJECTA_LIVE_OUTAGE_DIR to also assert the recorded failure")
	}
	cassette := newestCassetteContaining(t, traceDir, marker)
	raw, err := os.ReadFile(cassette)
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `"status_code":502`) {
		t.Errorf("cassette %s does not record the 502", filepath.Base(cassette))
	}
	if !strings.Contains(text, "routing.failure") {
		t.Errorf("cassette %s records no routing.failure event", filepath.Base(cassette))
	}
	if !strings.Contains(text, "routing.retry_candidate") {
		t.Errorf("cassette %s records no retry attempts", filepath.Base(cassette))
	}
}

// TestLiveChaosErrorInjection pins the documented chaos contract
// (docs/ARCHITECTURE.md:48: "配置驱动的故障注入（延迟/错误）"). The chaos
// instance is configured with rate=1.0 and action=error, so every request for
// the model must come back with the injected status and message.
func TestLiveChaosErrorInjection(t *testing.T) {
	requireLive(t)
	base := altURL(t, "TRAJECTA_LIVE_CHAOS_URL")
	token := altProxyToken(t, "TRAJECTA_LIVE_CHAOS_TOKEN")

	res := request(t, http.MethodPost, base+"/v1/chat/completions", map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly: ok."}},
		"max_tokens": 64,
	}, map[string]string{"Authorization": "Bearer " + token})
	if res.Status != http.StatusServiceUnavailable {
		t.Fatalf("chaos-injected request: want 503 from the configured error rule, got %s", res)
	}
	raw := string(res.Body)
	if !strings.Contains(raw, "chaos-injected") {
		t.Fatalf("chaos response body does not carry the configured message: %s", truncate(raw, 200))
	}
	// The injected error must use the protocol envelope, not plain text.
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("chaos error Content-Type = %q, want application/json", ct)
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(res.Body, &envelope); err != nil {
		t.Fatalf("chaos error body is not a JSON error envelope: %v (%s)", err, truncate(raw, 200))
	}
	if envelope.Error.Message == "" {
		t.Errorf("chaos error envelope carries no message: %s", truncate(raw, 200))
	}
}

// TestLiveRoutingCandidatesHealthSelectability checks that the recorded
// routing.candidates event does not advertise a channel whose circuit is open
// as selectable. The real routing decision excludes such a channel
// (route_plan filter_reason=target_open), so the two views must agree.
func TestLiveRoutingCandidatesHealthSelectability(t *testing.T) {
	requireLive(t)
	base := altURL(t, "TRAJECTA_LIVE_FAILOVER_URL")
	token := altProxyToken(t, "TRAJECTA_LIVE_FAILOVER_TOKEN")
	traceDir := strings.TrimSpace(os.Getenv("TRAJECTA_LIVE_FAILOVER_DIR"))
	if traceDir == "" {
		t.Skip("set TRAJECTA_LIVE_FAILOVER_DIR to inspect the recorded routing candidates")
	}

	marker := fmt.Sprintf("selectability-%d", time.Now().UnixNano())
	res := request(t, http.MethodPost, base+"/v1/chat/completions", map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly: ok. " + marker}},
		"max_tokens": 128,
	}, map[string]string{"Authorization": "Bearer " + token})
	if res.Status != http.StatusOK {
		t.Fatalf("request: want 200, got %s", res)
	}

	cassette := newestCassetteContaining(t, traceDir, marker)
	candidates := routingCandidatesFromCassette(t, cassette)
	if len(candidates) == 0 {
		t.Fatalf("cassette %s carries no routing.candidates event", filepath.Base(cassette))
	}
	openSeen := false
	for _, candidate := range candidates {
		health, _ := candidate["health_state"].(string)
		if health != "open" {
			continue
		}
		openSeen = true
		// Compare the candidate with itself, not with a substring of the whole
		// file: an unrelated healthy channel legitimately reports
		// selectable=true.
		if selectable, _ := candidate["selectable"].(bool); selectable {
			t.Errorf("BUG-FAULT-3: channel %v reports health_state=open and selectable=true in %s", candidate["id"], filepath.Base(cassette))
		}
		if reason, _ := candidate["filter_reason"].(string); reason == "" {
			t.Errorf("open-circuit channel %v carries no filter_reason in %s", candidate["id"], filepath.Base(cassette))
		}
	}
	if !openSeen {
		t.Skip("no channel had an open circuit for this exchange; nothing to compare")
	}
}

// routingCandidatesFromCassette parses the `routing.candidates` event of a
// cassette into its per-candidate attribute maps.
func routingCandidatesFromCassette(t *testing.T, cassette string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(cassette)
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "# event:") {
			continue
		}
		var event struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "# event:"))), &event); err != nil {
			continue
		}
		if event.Type != "routing.candidates" {
			continue
		}
		raw, ok := event.Attributes["candidates"].([]any)
		if !ok {
			continue
		}
		for _, entry := range raw {
			if candidate, ok := entry.(map[string]any); ok {
				out = append(out, candidate)
			}
		}
	}
	return out
}
