package identify

import (
	"encoding/json"
	"testing"
)

// The public types are serialized by the Monitor API (capabilities) and by
// external callers (matches). The JSON field names are part of the contract:
// a missing tag would silently emit Go's capitalized names and break every
// reader. These tests pin the lowercase wire names.

func TestCapabilityJSONWireNames(t *testing.T) {
	raw, err := json.Marshal(Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	var caps []map[string]any
	if err := json.Unmarshal(raw, &caps); err != nil {
		t.Fatal(err)
	}
	if len(caps) != 3 {
		t.Fatalf("capabilities = %d, want 3", len(caps))
	}
	for _, c := range caps {
		for _, key := range []string{"id", "name", "name_zh", "description", "description_zh", "report_only", "kinds", "rules"} {
			if _, ok := c[key]; !ok {
				t.Errorf("capability missing JSON key %q: %v", key, c)
			}
		}
		if id, _ := c["id"].(string); id == "" {
			t.Errorf("capability id is empty: %v", c)
		}
		if rules, _ := c["rules"].([]any); len(rules) == 0 {
			t.Errorf("capability %v has no rules on the wire", c["id"])
		}
	}
}

func TestRuleInfoJSONWireNames(t *testing.T) {
	raw, err := json.Marshal(Rules())
	if err != nil {
		t.Fatal(err)
	}
	var rules []map[string]any
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatal(err)
	}
	if len(rules) == 0 {
		t.Fatal("no rules")
	}
	for _, key := range []string{"name", "kind", "vendor", "grade", "strength", "pii", "report_only", "min_len", "max_len"} {
		if _, ok := rules[0][key]; !ok {
			t.Errorf("rule missing JSON key %q", key)
		}
	}
}

func TestMatchJSONWireNames(t *testing.T) {
	matches := Scan([]byte("aws " + awsKey))
	if len(matches) == 0 {
		t.Fatal("expected a match")
	}
	raw, err := json.Marshal(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"start", "end", "rule", "kind", "vendor", "score", "confidence", "maskable", "reasons"} {
		if _, ok := m[key]; !ok {
			t.Errorf("match missing JSON key %q", key)
		}
	}
}
