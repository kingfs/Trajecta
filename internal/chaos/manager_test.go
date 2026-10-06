package chaos

import (
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
)

// TestEvaluateNormalizesTheActionAndFillsErrorDefaults pins the two facts the proxy relies on.
//
// The proxy compares the action with an exact string, so the manager has to normalize it the
// same way the router normalizes its selection policies: `ERROR` and ` Delay ` marked the rule
// as hit and then fell through both branches, and the request reached the upstream as if chaos
// were disabled. The error action also fills the status and message a rule may omit.
func TestEvaluateNormalizesTheActionAndFillsErrorDefaults(t *testing.T) {
	cfg := &config.Config{}
	cfg.Chaos.Enabled = true
	cfg.Chaos.Rules = []config.ChaosRule{
		{Model: "*", Rate: 1, Action: "ERROR", StatusCode: 503, Message: "injected"},
	}
	res := New(cfg).Evaluate("gpt-5")
	if !res.ShouldInject || res.Action != "error" {
		t.Fatalf("Evaluate() = %+v, want an injected error action", res)
	}
	if res.StatusCode != 503 || res.Message != "injected" {
		t.Fatalf("Evaluate() status/message = %d/%q, want the configured 503/injected", res.StatusCode, res.Message)
	}

	cfg.Chaos.Rules = []config.ChaosRule{{Model: "*", Rate: 1, Action: " Delay ", Delay: 250 * time.Millisecond}}
	res = New(cfg).Evaluate("gpt-5")
	if !res.ShouldInject || res.Action != "delay" || res.Delay != 250*time.Millisecond {
		t.Fatalf("Evaluate() = %+v, want a trimmed delay action with its duration", res)
	}

	cfg.Chaos.Rules = []config.ChaosRule{{Model: "*", Rate: 1, Action: "error"}}
	res = New(cfg).Evaluate("gpt-5")
	if res.StatusCode != 500 || res.Message != "Chaos Injection Error" {
		t.Fatalf("Evaluate() defaults = %d/%q, want 500 and the default message", res.StatusCode, res.Message)
	}
}

// TestEvaluateMatchesModelsCaseInsensitively keeps the documented `*` and name matching
// behaviour pinned while the action normalization is in place.
func TestEvaluateMatchesModelsCaseInsensitively(t *testing.T) {
	cfg := &config.Config{}
	cfg.Chaos.Enabled = true
	cfg.Chaos.Rules = []config.ChaosRule{{Model: "GPT-5", Rate: 1, Action: "error"}}

	if res := New(cfg).Evaluate("gpt-5"); !res.ShouldInject {
		t.Fatalf("Evaluate(gpt-5) = %+v, want the GPT-5 rule to match", res)
	}
	if res := New(cfg).Evaluate("gpt-4"); res.ShouldInject {
		t.Fatalf("Evaluate(gpt-4) = %+v, want no injection", res)
	}

	cfg.Chaos.Enabled = false
	if res := New(cfg).Evaluate("gpt-5"); res.ShouldInject {
		t.Fatalf("Evaluate() with chaos disabled = %+v, want no injection", res)
	}
}
