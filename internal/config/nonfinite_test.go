package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadRejectsNonFiniteNumbers pins that a YAML value that is not a finite number is refused
// instead of reaching the hot path.
//
// `.nan`, `.inf` and `-.inf` are valid YAML and parse into float64, and they slip through every
// rule written as a comparison: the chaos rate check `rate < 0 || rate > 1` is false for NaN, and
// the router's non-positive-means-default rule (`v <= 0`) is false for NaN too. The consequence is
// not a loud failure but silently different routing - weight and capacity_hint divide a target's
// expected cost, so a NaN cost wins against every other candidate and the router snapshot cannot
// be marshalled for the Monitor API (`json: unsupported value: NaN`).
func TestLoadRejectsNonFiniteNumbers(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "weight NaN",
			body: "upstreams:\n  - id: a\n    base_url: https://a.test/v1\n    api_key: k\n    weight: .nan\n",
			want: "upstreams[0].weight",
		},
		{
			name: "capacity_hint +Inf",
			body: "upstreams:\n  - id: a\n    base_url: https://a.test/v1\n    api_key: k\n    capacity_hint: .inf\n",
			want: "upstreams[0].capacity_hint",
		},
		{
			name: "weight -Inf on the second target",
			body: "upstreams:\n  - id: a\n    base_url: https://a.test/v1\n    api_key: k\n  - id: b\n    base_url: https://b.test/v1\n    api_key: k\n    weight: -.inf\n",
			want: "upstreams[1].weight",
		},
		{
			name: "router epsilon NaN",
			body: "router:\n  selection:\n    epsilon: .nan\n",
			want: "router.selection.epsilon",
		},
		{
			name: "chaos rate NaN",
			body: "chaos:\n  rules:\n    - action: delay\n      rate: .nan\n",
			want: "chaos.rules[0].rate",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("WriteFile(%s) error = %v", path, err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load(%s) accepted a non-finite number, want an error mentioning %s", tc.body, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "finite") {
				t.Fatalf("Load() error = %q, want it to name %s and say the value must be finite", err, tc.want)
			}
		})
	}
}

// TestLoadAcceptsFiniteNumbers keeps the check from drifting into a blanket float check: the
// documented non-positive-means-default values stay loadable.
func TestLoadAcceptsFiniteNumbers(t *testing.T) {
	body := "upstreams:\n  - id: a\n    base_url: https://a.test/v1\n    api_key: k\n    weight: -3\n    capacity_hint: 0\nrouter:\n  selection:\n    epsilon: 0\nchaos:\n  rules:\n    - action: delay\n      rate: 0.5\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want the documented finite values to load", err)
	}
	if cfg.Upstreams[0].Weight != -3 || cfg.Upstreams[0].CapacityHint != 0 {
		t.Fatalf("loaded weight/capacity = %v/%v, want -3/0", cfg.Upstreams[0].Weight, cfg.Upstreams[0].CapacityHint)
	}
}
