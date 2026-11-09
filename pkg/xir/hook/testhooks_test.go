package hook_test

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// testHook is a configurable Hook implementation for the package tests.
type testHook struct {
	id       string
	points   []hook.Point
	lanes    hook.Lane
	needs    hook.Needs
	priority int
	policy   hook.FailPolicy
	handle   func(context.Context, *hook.Event) (hook.Result, error)
}

func (h *testHook) ID() string { return h.id }

func (h *testHook) Points() []hook.Point { return h.points }

func (h *testHook) Lanes() hook.Lane { return h.lanes }

func (h *testHook) Needs() hook.Needs { return h.needs }

func (h *testHook) Priority() int { return h.priority }

func (h *testHook) FailPolicy() hook.FailPolicy { return h.policy }

func (h *testHook) Handle(ctx context.Context, ev *hook.Event) (hook.Result, error) {
	if h.handle == nil {
		return hook.Result{}, nil
	}
	return h.handle(ctx, ev)
}

// newRegistry registers one factory per hook that returns that hook value
// unchanged.
func newRegistry(t *testing.T, hooks ...*testHook) *hook.Registry {
	t.Helper()
	reg := &hook.Registry{}
	for _, h := range hooks {
		reg.Register(h.id, func(hook.Config) (hook.Hook, error) { return h, nil })
	}
	return reg
}

func mustCompile(t *testing.T, reg *hook.Registry, configs ...hook.Config) *hook.Plan {
	t.Helper()
	plan, err := hook.Compile(reg, configs)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return plan
}

func enabled(id string) hook.Config { return hook.Config{ID: id, Enabled: true} }

func hookIDs(hooks []hook.Hook) []string {
	out := make([]string, len(hooks))
	for i, h := range hooks {
		out[i] = h.ID()
	}
	return out
}
