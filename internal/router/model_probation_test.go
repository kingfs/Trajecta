package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
)

func TestModelProbationUsesModelScopedInflight(t *testing.T) {
	now := time.Now()
	target := &Target{
		ID:          "baizhiyun",
		models:      map[string]struct{}{"gpt-6.1-sol": {}, "deepseek-flash": {}},
		healthState: HealthHealthy,
		modelHealth: map[string]*modelHealthState{
			"gpt-6.1-sol": {healthState: HealthProbation},
		},
		inflight: 1, // a deepseek-flash request is running
		modelInflight: map[string]int64{
			"deepseek-flash": 1,
		},
	}
	if !target.canSelect(now, "gpt-6.1-sol") {
		t.Fatal("a busy sibling model must not block the recovering model's probation probe")
	}
	if target.modelProbationFullLocked("gpt-6.1-sol") {
		t.Fatal("model probation is full with zero in-flight requests for that model")
	}

	target.modelInflight["gpt-6.1-sol"] = 1
	if target.canSelect(now, "gpt-6.1-sol") {
		t.Fatal("a second concurrent request must respect the per-model probation limit")
	}
	if !target.modelProbationFullLocked("gpt-6.1-sol") {
		t.Fatal("model probation must be full once its own probe is in flight")
	}

	// The channel-wide probation limit still uses the channel total.
	target.healthState = HealthProbation
	target.modelInflight["gpt-6.1-sol"] = 0
	if target.canSelect(now, "gpt-6.1-sol") {
		t.Fatal("channel probation must still gate on the channel's in-flight count")
	}
}

func TestTargetTracksModelInflightOnStartAndRelease(t *testing.T) {
	target := &Target{}
	target.onStart(RequestFeatures{ModelName: "gpt-6.1-sol", Stream: true})
	if got := target.modelInflight["gpt-6.1-sol"]; got != 1 {
		t.Fatalf("modelInflight after start = %d, want 1", got)
	}
	target.onRelease(RequestFeatures{ModelName: "gpt-6.1-sol", Stream: true})
	if got := target.modelInflight["gpt-6.1-sol"]; got != 0 {
		t.Fatalf("modelInflight after release = %d, want 0", got)
	}
	if target.inflight != 0 || target.inflightStreaming != 0 {
		t.Fatalf("target inflight counters = %d/%d, want 0/0", target.inflight, target.inflightStreaming)
	}
}

func TestModelProbationStarvationIsSelectableAndReported(t *testing.T) {
	responsesEnabled := true
	chatEnabled := true
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "baizhiyun",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-6.1-sol", "deepseek-flash"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.example.com/v1",
					ProviderPreset: "openai",
					APIType:        "responses",
					Capabilities: config.UpstreamCapabilitiesConfig{
						Responses:       &responsesEnabled,
						ChatCompletions: &chatEnabled,
					},
				},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable
	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	rtr.mu.Lock()
	rtr.rebuildCatalog()
	rtr.mu.Unlock()
	body := []byte(`{"model":"gpt-6.1-sol","input":"hello","stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	target := rtr.targets[0]
	target.mu.Lock()
	target.healthState = HealthHealthy
	target.modelHealth["gpt-6.1-sol"] = &modelHealthState{healthState: HealthProbation}
	target.inflight = 1
	target.modelInflight = map[string]int64{"deepseek-flash": 1}
	target.mu.Unlock()

	if !rtr.HasNativeResponsesTargetWithBody(req, body) {
		t.Fatal("the model must still be structurally present")
	}
	if !rtr.HasSelectableNativeResponsesCandidateWithBody(req, body) {
		t.Fatal("a busy sibling model must not starve the recovering model")
	}

	target.mu.Lock()
	target.modelInflight["gpt-6.1-sol"] = 1
	target.mu.Unlock()
	if rtr.HasSelectableNativeResponsesCandidateWithBody(req, body) {
		t.Fatal("a full per-model probation probe must not select another concurrent request")
	}
	availableAt, ok := rtr.EarliestAvailabilityFor(req, body)
	if !ok {
		t.Fatal("EarliestAvailabilityFor() must report a full probation window")
	}
	if delay := time.Until(availableAt); delay <= 0 || delay > 2*time.Second {
		t.Fatalf("EarliestAvailabilityFor() delay = %v, want a short transient hint", delay)
	}
}
