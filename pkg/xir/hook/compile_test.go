package hook_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

func TestCompileFiltersDisabled(t *testing.T) {
	t.Parallel()
	on := &testHook{id: "on", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot}
	reg := newRegistry(t, on)

	plan := mustCompile(t, reg,
		hook.Config{ID: "off", Enabled: false, Points: []hook.Point{"not.a.point"}},
		enabled("on"),
	)
	if plan.Empty() {
		t.Fatal("plan with one enabled hook is empty")
	}
	if got := hookIDs(plan.Hooks(hook.LaneHot, hook.PointRequestMessage)); !equalStrings(got, []string{"on"}) {
		t.Fatalf("Hooks = %v, want [on]", got)
	}
}

func TestCompileOnlyDisabledNeedsNoRegistry(t *testing.T) {
	t.Parallel()
	plan, err := hook.Compile(nil, []hook.Config{{ID: "off", Enabled: false}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !plan.Empty() {
		t.Fatal("plan with only disabled configs is not empty")
	}
}

func TestCompileValidationErrors(t *testing.T) {
	t.Parallel()

	factoryErr := errors.New("settings rejected")
	tests := []struct {
		name        string
		register    func(*hook.Registry)
		nilRegistry bool
		configs     []hook.Config
		want        string
		wantIs      error
	}{
		{
			name:    "empty config ID",
			configs: []hook.Config{{Enabled: true}},
			want:    "empty ID",
		},
		{
			name:    "unknown hook",
			configs: []hook.Config{enabled("missing")},
			want:    `unknown hook "missing"`,
		},
		{
			name: "duplicate config ID",
			register: func(reg *hook.Registry) {
				reg.Register("dup", func(hook.Config) (hook.Hook, error) {
					return &testHook{id: "dup", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot}, nil
				})
			},
			configs: []hook.Config{enabled("dup"), enabled("dup")},
			want:    `duplicate config ID "dup"`,
		},
		{
			name: "invalid config lanes",
			register: func(reg *hook.Registry) {
				reg.Register("h", func(hook.Config) (hook.Hook, error) {
					return &testHook{id: "h", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot}, nil
				})
			},
			configs: []hook.Config{{ID: "h", Enabled: true, Lanes: hook.Lane(8)}},
			want:    "invalid lanes",
		},
		{
			name: "unknown explicit point",
			register: func(reg *hook.Registry) {
				reg.Register("h", func(hook.Config) (hook.Hook, error) {
					return &testHook{id: "h", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot}, nil
				})
			},
			configs: []hook.Config{{ID: "h", Enabled: true, Points: []hook.Point{"request.nope"}}},
			want:    `unknown point "request.nope"`,
		},
		{
			name: "unknown point from hook default",
			register: func(reg *hook.Registry) {
				reg.Register("h", func(hook.Config) (hook.Hook, error) {
					return &testHook{id: "h", points: []hook.Point{"request.nope"}, lanes: hook.LaneHot}, nil
				})
			},
			configs: []hook.Config{enabled("h")},
			want:    `unknown point "request.nope"`,
		},
		{
			name: "no lanes",
			register: func(reg *hook.Registry) {
				reg.Register("h", func(hook.Config) (hook.Hook, error) {
					return &testHook{id: "h", points: []hook.Point{hook.PointRequestMessage}}, nil
				})
			},
			configs: []hook.Config{enabled("h")},
			want:    "no valid lanes",
		},
		{
			name: "unknown hook lane bits",
			register: func(reg *hook.Registry) {
				reg.Register("h", func(hook.Config) (hook.Hook, error) {
					return &testHook{id: "h", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.Lane(8)}, nil
				})
			},
			configs: []hook.Config{enabled("h")},
			want:    "no valid lanes",
		},
		{
			name: "no points",
			register: func(reg *hook.Registry) {
				reg.Register("h", func(hook.Config) (hook.Hook, error) {
					return &testHook{id: "h", lanes: hook.LaneHot}, nil
				})
			},
			configs: []hook.Config{enabled("h")},
			want:    "declares no points",
		},
		{
			name: "factory error",
			register: func(reg *hook.Registry) {
				reg.Register("bad", func(hook.Config) (hook.Hook, error) { return nil, factoryErr })
			},
			configs: []hook.Config{enabled("bad")},
			want:    `build "bad"`,
			wantIs:  factoryErr,
		},
		{
			name: "factory returns nil hook",
			register: func(reg *hook.Registry) {
				reg.Register("nil", func(hook.Config) (hook.Hook, error) { return nil, nil })
			},
			configs: []hook.Config{enabled("nil")},
			want:    "returned nil hook",
		},
		{
			name: "factory returns mismatched ID",
			register: func(reg *hook.Registry) {
				reg.Register("wrong", func(hook.Config) (hook.Hook, error) {
					return &testHook{id: "other", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot}, nil
				})
			},
			configs: []hook.Config{enabled("wrong")},
			want:    `returned hook with ID "other"`,
		},
		{
			name:        "nil registry",
			nilRegistry: true,
			configs:     []hook.Config{enabled("h")},
			want:        `unknown hook "h"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var reg *hook.Registry
			if !tc.nilRegistry {
				reg = &hook.Registry{}
				if tc.register != nil {
					tc.register(reg)
				}
			}
			plan, err := hook.Compile(reg, tc.configs)
			if err == nil {
				t.Fatalf("Compile returned nil error, want %q", tc.want)
			}
			if plan != nil {
				t.Fatal("Compile returned a plan alongside an error")
			}
			if got := err.Error(); !strings.Contains(got, tc.want) {
				t.Fatalf("error %q does not contain %q", got, tc.want)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("error %v does not wrap %v", err, tc.wantIs)
			}
		})
	}
}

func TestCompileUsesHookDefaults(t *testing.T) {
	t.Parallel()
	auditOnly := &testHook{
		id:       "audit",
		points:   []hook.Point{hook.PointResponseUsage},
		lanes:    hook.LaneAudit,
		priority: 10,
	}
	both := &testHook{
		id:       "both",
		points:   []hook.Point{hook.PointRequestMessage, hook.PointResponseOutput},
		lanes:    hook.LaneHot | hook.LaneAudit,
		priority: 20,
	}
	reg := newRegistry(t, both, auditOnly)
	plan := mustCompile(t, reg, enabled("both"), enabled("audit"))

	if got := hookIDs(plan.Hooks(hook.LaneAudit, hook.PointResponseUsage)); !equalStrings(got, []string{"audit"}) {
		t.Fatalf("audit lane Hooks = %v, want [audit]", got)
	}
	if got := plan.Hooks(hook.LaneHot, hook.PointRequestMessage); !equalStrings(hookIDs(got), []string{"both"}) {
		t.Fatalf("hot lane Hooks = %v, want [both]", got)
	}
	if got := plan.Hooks(hook.LaneAudit, hook.PointResponseOutput); !equalStrings(hookIDs(got), []string{"both"}) {
		t.Fatalf("audit/default point Hooks = %v, want [both]", got)
	}
	if got := plan.Hooks(hook.LaneHot, hook.PointResponseUsage); len(got) != 0 {
		t.Fatalf("hot lane matched an audit-only hook: %v", hookIDs(got))
	}
}

func TestCompileUsesExplicitOverrides(t *testing.T) {
	t.Parallel()
	h := &testHook{
		id:       "h",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 100,
	}
	reg := newRegistry(t, h)
	plan := mustCompile(t, reg, hook.Config{
		ID:       "h",
		Enabled:  true,
		Lanes:    hook.LaneAudit,
		Points:   []hook.Point{hook.PointResponseOutput},
		Priority: 1,
	})

	if got := hookIDs(plan.Hooks(hook.LaneAudit, hook.PointResponseOutput)); !equalStrings(got, []string{"h"}) {
		t.Fatalf("explicit lane/point Hooks = %v, want [h]", got)
	}
	if got := plan.Hooks(hook.LaneHot, hook.PointRequestMessage); len(got) != 0 {
		t.Fatalf("hook defaults leaked through explicit config: %v", hookIDs(got))
	}
}

func TestCompileOrdersByPriorityThenID(t *testing.T) {
	t.Parallel()
	a := &testHook{id: "a", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot, priority: 10}
	b := &testHook{id: "b", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot, priority: 20}
	c := &testHook{id: "c", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot, priority: 10}
	d := &testHook{id: "d", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot, priority: 100}
	reg := newRegistry(t, b, a, c, d)

	plan := mustCompile(t, reg,
		enabled("b"),
		enabled("a"),
		enabled("c"),
		hook.Config{ID: "d", Enabled: true, Priority: 1},
	)
	got := hookIDs(plan.Hooks(hook.LaneHot, hook.PointRequestMessage))
	want := []string{"d", "a", "c", "b"}
	if !equalStrings(got, want) {
		t.Fatalf("hook order = %v, want %v", got, want)
	}
}

func TestCompileDoesNotAliasInputs(t *testing.T) {
	t.Parallel()
	points := []hook.Point{hook.PointRequestMessage}
	settings := json.RawMessage(`{"mode":"test"}`)
	var factoryPoints []hook.Point
	var factorySettings json.RawMessage

	reg := &hook.Registry{}
	reg.Register("s", func(cfg hook.Config) (hook.Hook, error) {
		factoryPoints = cfg.Points
		factorySettings = cfg.Settings
		return &testHook{id: "s", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot}, nil
	})
	plan := mustCompile(t, reg, hook.Config{
		ID:       "s",
		Enabled:  true,
		Points:   points,
		Settings: settings,
	})

	points[0] = "mutated"
	settings[0] = '['

	if len(factoryPoints) == 0 || factoryPoints[0] != hook.PointRequestMessage {
		t.Fatalf("factory received aliased points: %v", factoryPoints)
	}
	if string(factorySettings) != `{"mode":"test"}` {
		t.Fatalf("factory received aliased settings: %q", factorySettings)
	}
	if got := plan.Hooks(hook.LaneHot, hook.PointRequestMessage); len(got) != 1 {
		t.Fatalf("caller mutation changed the compiled plan: %v", hookIDs(got))
	}
}

func TestCompileNilRegistryWithNoEnabledConfigs(t *testing.T) {
	t.Parallel()
	plan, err := hook.Compile(nil, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !plan.Empty() {
		t.Fatal("plan is not empty")
	}
}
