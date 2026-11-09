package hook_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

func TestPlanHooksFiltersLaneAndPoint(t *testing.T) {
	t.Parallel()
	hot := &testHook{
		id:       "hot",
		points:   []hook.Point{hook.PointRequestMessage, hook.PointResponseOutput},
		lanes:    hook.LaneHot,
		priority: 1,
	}
	audit := &testHook{
		id:       "audit",
		points:   []hook.Point{hook.PointResponseOutput},
		lanes:    hook.LaneAudit,
		priority: 2,
	}
	both := &testHook{
		id:       "both",
		points:   []hook.Point{hook.PointResponseUsage},
		lanes:    hook.LaneHot | hook.LaneAudit,
		priority: 3,
	}
	plan := mustCompile(t, newRegistry(t, hot, audit, both), enabled("hot"), enabled("audit"), enabled("both"))

	cases := []struct {
		name  string
		lane  hook.Lane
		point hook.Point
		want  []string
	}{
		{"hot request message", hook.LaneHot, hook.PointRequestMessage, []string{"hot"}},
		{"audit request message", hook.LaneAudit, hook.PointRequestMessage, nil},
		{"hot response output", hook.LaneHot, hook.PointResponseOutput, []string{"hot"}},
		{"audit response output", hook.LaneAudit, hook.PointResponseOutput, []string{"audit"}},
		{"hot usage", hook.LaneHot, hook.PointResponseUsage, []string{"both"}},
		{"audit usage", hook.LaneAudit, hook.PointResponseUsage, []string{"both"}},
		{"combined lane union", hook.LaneHot | hook.LaneAudit, hook.PointResponseOutput, []string{"hot", "audit"}},
		{"zero lane", 0, hook.PointResponseOutput, nil},
		{"unknown point", hook.LaneHot, hook.Point("bogus"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := plan.Hooks(tc.lane, tc.point)
			if tc.want == nil {
				if len(got) != 0 {
					t.Fatalf("Hooks(%v, %q) = %v, want none", tc.lane, tc.point, hookIDs(got))
				}
				return
			}
			if ids := hookIDs(got); !equalStrings(ids, tc.want) {
				t.Fatalf("Hooks(%v, %q) = %v, want %v", tc.lane, tc.point, ids, tc.want)
			}
		})
	}
}

func TestPlanHooksReturnsCopy(t *testing.T) {
	t.Parallel()
	h := &testHook{id: "h", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot}
	plan := mustCompile(t, newRegistry(t, h), enabled("h"))

	got := plan.Hooks(hook.LaneHot, hook.PointRequestMessage)
	if len(got) != 1 {
		t.Fatalf("Hooks = %v, want one hook", hookIDs(got))
	}
	got[0] = nil
	if again := plan.Hooks(hook.LaneHot, hook.PointRequestMessage); len(again) != 1 || again[0] == nil {
		t.Fatalf("mutating the returned slice changed the plan: %v", again)
	}
}

func TestPlanRunOrderAndMerge(t *testing.T) {
	t.Parallel()
	var calls []string
	h1Tags := map[string]string{"a": "1", "shared": "h1"}
	h1Meta := map[string]string{"src": "h1"}
	h1EditBytes := []byte("x")
	h1Usage := &usage.Usage{InputTokens: 10, OutputTokens: 5}
	h2EditBytes := []byte("yy")

	h1 := &testHook{
		id:       "h1",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 1,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "h1")
			return hook.Result{
				Findings: []hook.Finding{{
					Hook:     "h1",
					Rule:     "rule.one",
					Severity: hook.SeverityMedium,
					Message:  "first",
					Meta:     h1Meta,
				}},
				Edits: []wire.Edit{{
					Span:   wire.Span{Start: 0, End: 1},
					Bytes:  h1EditBytes,
					Mode:   wire.ReplaceRaw,
					Reason: "h1",
				}},
				Tags:  h1Tags,
				Usage: h1Usage,
			}, nil
		},
	}
	h2 := &testHook{
		id:       "h2",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 2,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "h2")
			return hook.Result{
				Findings: []hook.Finding{
					{Hook: "h2", Rule: "rule.two", Severity: hook.SeverityHigh},
					{Hook: "h2", Rule: "rule.three", Severity: hook.SeverityLow},
				},
				Edits: []wire.Edit{{
					Span:   wire.Span{Start: 3, End: 5},
					Bytes:  h2EditBytes,
					Mode:   wire.ReplaceRaw,
					Reason: "h2",
				}},
				Tags:  map[string]string{"b": "2", "shared": "h2"},
				Usage: &usage.Usage{InputTokens: 20, CacheReadTokens: 3},
			}, nil
		},
	}
	h3 := &testHook{
		id:       "h3",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 3,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "h3")
			return hook.Result{}, nil
		},
	}
	plan := mustCompile(t, newRegistry(t, h2, h3, h1), enabled("h1"), enabled("h2"), enabled("h3"))

	res, err := plan.Run(context.Background(), &hook.Event{
		Lane:  hook.LaneHot,
		Point: hook.PointRequestMessage,
		Path:  "$.messages[0]",
		Span:  wire.Span{Start: 10, End: 20},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"h1", "h2", "h3"}; !equalStrings(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}

	if len(res.Findings) != 3 {
		t.Fatalf("findings = %d, want 3", len(res.Findings))
	}
	if got := []string{res.Findings[0].Rule, res.Findings[1].Rule, res.Findings[2].Rule}; !equalStrings(got, []string{"rule.one", "rule.two", "rule.three"}) {
		t.Fatalf("finding order = %v", got)
	}
	if len(res.Edits) != 2 {
		t.Fatalf("edits = %d, want 2", len(res.Edits))
	}
	if res.Edits[0].Span != (wire.Span{Start: 0, End: 1}) || string(res.Edits[0].Bytes) != "x" {
		t.Fatalf("first edit = %+v", res.Edits[0])
	}
	if res.Edits[1].Span != (wire.Span{Start: 3, End: 5}) || string(res.Edits[1].Bytes) != "yy" {
		t.Fatalf("second edit = %+v", res.Edits[1])
	}

	wantTags := map[string]string{"a": "1", "b": "2", "shared": "h2"}
	if !reflect.DeepEqual(res.Tags, wantTags) {
		t.Fatalf("tags = %v, want %v", res.Tags, wantTags)
	}

	wantUsage := usage.Merge(
		usage.Merge(usage.Usage{}, usage.Usage{InputTokens: 10, OutputTokens: 5}),
		usage.Usage{InputTokens: 20, CacheReadTokens: 3},
	)
	if res.Usage == nil {
		t.Fatal("merged usage is nil")
	}
	if *res.Usage != wantUsage {
		t.Fatalf("usage = %+v, want %+v", *res.Usage, wantUsage)
	}

	// The plan copies every mutable container it carries, so caller or hook
	// mutation after Run cannot rewrite the merged result's inputs.
	res.Edits[0].Bytes[0] = 'z'
	if h1EditBytes[0] != 'x' {
		t.Fatal("merged edit bytes alias the hook's buffer")
	}
	res.Tags["a"] = "changed"
	if h1Tags["a"] != "1" {
		t.Fatal("merged tags alias the hook's map")
	}
	res.Findings[0].Meta["src"] = "changed"
	if h1Meta["src"] != "h1" {
		t.Fatal("merged finding meta aliases the hook's map")
	}
	if h1Usage.InputTokens != 10 {
		t.Fatal("merged usage aliases the hook's value")
	}
}

func TestPlanRunFiltersByLaneAndPoint(t *testing.T) {
	t.Parallel()
	var calls int
	h := &testHook{
		id:     "h",
		points: []hook.Point{hook.PointRequestMessage},
		lanes:  hook.LaneHot,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls++
			return hook.Result{}, nil
		},
	}
	plan := mustCompile(t, newRegistry(t, h), enabled("h"))

	if _, err := plan.Run(context.Background(), &hook.Event{Lane: hook.LaneAudit, Point: hook.PointRequestMessage}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := plan.Run(context.Background(), &hook.Event{Lane: hook.LaneHot, Point: hook.PointResponseOutput}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 0 {
		t.Fatalf("hook ran for a non-matching event (%d calls)", calls)
	}
	if _, err := plan.Run(context.Background(), &hook.Event{Lane: hook.LaneHot, Point: hook.PointRequestMessage}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("matching hook ran %d times, want 1", calls)
	}
}

func TestPlanRunFailOpen(t *testing.T) {
	t.Parallel()
	var calls []string
	bad := &testHook{
		id:       "bad",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 1,
		policy:   hook.FailOpen,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "bad")
			return hook.Result{
				Findings: []hook.Finding{{Hook: "bad", Rule: "partial"}},
				Tags:     map[string]string{"partial": "1"},
				Usage:    &usage.Usage{InputTokens: 99},
			}, errors.New("boom")
		},
	}
	good := &testHook{
		id:       "good",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 2,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "good")
			return hook.Result{
				Findings: []hook.Finding{{Hook: "good", Rule: "good.rule"}},
				Tags:     map[string]string{"good": "1"},
			}, nil
		},
	}
	plan := mustCompile(t, newRegistry(t, bad, good), enabled("bad"), enabled("good"))

	ev := &hook.Event{
		Lane:  hook.LaneHot,
		Point: hook.PointRequestMessage,
		Path:  "$.messages[0]",
		Span:  wire.Span{Start: 4, End: 8},
	}
	res, err := plan.Run(context.Background(), ev)
	if err != nil {
		t.Fatalf("fail-open Run returned error: %v", err)
	}
	if want := []string{"bad", "good"}; !equalStrings(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("findings = %d, want 2 (diagnostic + good)", len(res.Findings))
	}
	diag := res.Findings[0]
	if diag.Hook != "bad" || diag.Rule != hook.RuleHookError {
		t.Fatalf("diagnostic finding = %+v", diag)
	}
	if diag.Severity != hook.SeverityMedium {
		t.Fatalf("diagnostic severity = %q, want %q", diag.Severity, hook.SeverityMedium)
	}
	if diag.Point != ev.Point || diag.Path != ev.Path || diag.Span != ev.Span {
		t.Fatalf("diagnostic location = %+v", diag)
	}
	if !strings.Contains(diag.Message, "boom") {
		t.Fatalf("diagnostic message = %q, want the hook error", diag.Message)
	}
	if res.Findings[1].Rule != "good.rule" {
		t.Fatalf("second finding = %+v", res.Findings[1])
	}
	if !reflect.DeepEqual(res.Tags, map[string]string{"good": "1"}) {
		t.Fatalf("fail-open kept the failed hook's tags: %v", res.Tags)
	}
	if res.Usage != nil {
		t.Fatalf("fail-open kept the failed hook's usage: %+v", res.Usage)
	}
}

func TestPlanRunFailClosed(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("boom")
	var calls []string
	good := &testHook{
		id:       "good",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 1,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "good")
			return hook.Result{Findings: []hook.Finding{{Hook: "good", Rule: "good.rule"}}}, nil
		},
	}
	bad := &testHook{
		id:       "bad",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 2,
		policy:   hook.FailClosed,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "bad")
			return hook.Result{}, sentinel
		},
	}
	after := &testHook{
		id:       "after",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 3,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "after")
			return hook.Result{}, nil
		},
	}
	plan := mustCompile(t, newRegistry(t, good, bad, after), enabled("good"), enabled("bad"), enabled("after"))

	res, err := plan.Run(context.Background(), &hook.Event{Lane: hook.LaneHot, Point: hook.PointRequestMessage})
	if err == nil {
		t.Fatal("fail-closed Run returned nil error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error %v does not wrap the hook error", err)
	}
	if !strings.Contains(err.Error(), `"bad"`) {
		t.Fatalf("error %q does not name the hook", err)
	}
	if want := []string{"good", "bad"}; !equalStrings(calls, want) {
		t.Fatalf("calls = %v, want %v (hook after the failure must not run)", calls, want)
	}
	if len(res.Findings) != 1 || res.Findings[0].Rule != "good.rule" {
		t.Fatalf("partial result lost prior findings: %+v", res.Findings)
	}
}

func TestPlanRunPanicFollowsFailPolicy(t *testing.T) {
	t.Parallel()
	t.Run("fail open", func(t *testing.T) {
		t.Parallel()
		var nextRan bool
		panicker := &testHook{
			id:       "panicker",
			points:   []hook.Point{hook.PointRequestMessage},
			lanes:    hook.LaneHot,
			priority: 1,
			policy:   hook.FailOpen,
			handle: func(context.Context, *hook.Event) (hook.Result, error) {
				panic("kaboom")
			},
		}
		next := &testHook{
			id:       "next",
			points:   []hook.Point{hook.PointRequestMessage},
			lanes:    hook.LaneHot,
			priority: 2,
			handle: func(context.Context, *hook.Event) (hook.Result, error) {
				nextRan = true
				return hook.Result{}, nil
			},
		}
		plan := mustCompile(t, newRegistry(t, panicker, next), enabled("panicker"), enabled("next"))
		res, err := plan.Run(context.Background(), &hook.Event{Lane: hook.LaneHot, Point: hook.PointRequestMessage})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !nextRan {
			t.Fatal("fail-open panic stopped the plan")
		}
		if len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Message, "panic: kaboom") {
			t.Fatalf("panic diagnostic = %+v", res.Findings)
		}
	})

	t.Run("fail closed", func(t *testing.T) {
		t.Parallel()
		var nextRan bool
		panicker := &testHook{
			id:       "panicker",
			points:   []hook.Point{hook.PointRequestMessage},
			lanes:    hook.LaneHot,
			priority: 1,
			policy:   hook.FailClosed,
			handle: func(context.Context, *hook.Event) (hook.Result, error) {
				panic("kaboom")
			},
		}
		next := &testHook{
			id:       "next",
			points:   []hook.Point{hook.PointRequestMessage},
			lanes:    hook.LaneHot,
			priority: 2,
			handle: func(context.Context, *hook.Event) (hook.Result, error) {
				nextRan = true
				return hook.Result{}, nil
			},
		}
		plan := mustCompile(t, newRegistry(t, panicker, next), enabled("panicker"), enabled("next"))
		_, err := plan.Run(context.Background(), &hook.Event{Lane: hook.LaneHot, Point: hook.PointRequestMessage})
		if err == nil {
			t.Fatal("fail-closed panic returned nil error")
		}
		if !strings.Contains(err.Error(), `"panicker"`) || !strings.Contains(err.Error(), "kaboom") {
			t.Fatalf("error = %q", err)
		}
		if nextRan {
			t.Fatal("fail-closed panic kept running hooks")
		}
	})
}

func TestPlanRunBlock(t *testing.T) {
	t.Parallel()
	var calls []string
	block := &hook.Block{Status: 403, Code: "blocked", Message: "policy"}
	first := &testHook{
		id:       "first",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 1,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "first")
			return hook.Result{
				Findings: []hook.Finding{{Hook: "first", Rule: "blocked"}},
				Block:    block,
				Tags:     map[string]string{"blocked": "1"},
			}, nil
		},
	}
	second := &testHook{
		id:       "second",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 2,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "second")
			return hook.Result{}, nil
		},
	}
	plan := mustCompile(t, newRegistry(t, first, second), enabled("first"), enabled("second"))

	res, err := plan.Run(context.Background(), &hook.Event{Lane: hook.LaneHot, Point: hook.PointRequestMessage})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"first"}; !equalStrings(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if res.Block == nil || *res.Block != *block {
		t.Fatalf("block = %+v, want %+v", res.Block, block)
	}
	if len(res.Findings) != 1 || len(res.Tags) != 1 {
		t.Fatalf("block result lost findings/tags: %+v", res)
	}

	block.Status = 500
	if res.Block.Status != 403 {
		t.Fatalf("result block aliases the hook's block: %+v", res.Block)
	}
}

func TestPlanRunNilInputs(t *testing.T) {
	t.Parallel()
	h := &testHook{id: "h", points: []hook.Point{hook.PointRequestMessage}, lanes: hook.LaneHot}
	plan := mustCompile(t, newRegistry(t, h), enabled("h"))

	if _, err := plan.Run(nil, &hook.Event{}); err == nil || !strings.Contains(err.Error(), "nil context") {
		t.Fatalf("nil context error = %v", err)
	}
	if _, err := plan.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "nil event") {
		t.Fatalf("nil event error = %v", err)
	}
}

func TestPlanRunCanceledContext(t *testing.T) {
	t.Parallel()
	var calls int
	h := &testHook{
		id:     "h",
		points: []hook.Point{hook.PointRequestMessage},
		lanes:  hook.LaneHot,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls++
			return hook.Result{}, nil
		},
	}
	plan := mustCompile(t, newRegistry(t, h), enabled("h"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := plan.Run(ctx, &hook.Event{Lane: hook.LaneHot, Point: hook.PointRequestMessage}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("hook ran under a canceled context (%d calls)", calls)
	}
}

func TestPlanRunStopsWhenContextCancelsBetweenHooks(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var calls []string
	first := &testHook{
		id:       "first",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 1,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "first")
			cancel()
			return hook.Result{}, nil
		},
	}
	second := &testHook{
		id:       "second",
		points:   []hook.Point{hook.PointRequestMessage},
		lanes:    hook.LaneHot,
		priority: 2,
		handle: func(context.Context, *hook.Event) (hook.Result, error) {
			calls = append(calls, "second")
			return hook.Result{}, nil
		},
	}
	plan := mustCompile(t, newRegistry(t, first, second), enabled("first"), enabled("second"))

	if _, err := plan.Run(ctx, &hook.Event{Lane: hook.LaneHot, Point: hook.PointRequestMessage}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if want := []string{"first"}; !equalStrings(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestEmptyAndNilPlan(t *testing.T) {
	t.Parallel()
	plan, err := hook.Compile(nil, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !plan.Empty() {
		t.Fatal("compiled empty plan is not Empty")
	}
	if got := plan.Hooks(hook.LaneHot, hook.PointRequestMessage); got != nil {
		t.Fatalf("empty plan Hooks = %v", got)
	}
	res, err := plan.Run(context.Background(), &hook.Event{Lane: hook.LaneHot, Point: hook.PointRequestMessage})
	if err != nil {
		t.Fatalf("empty plan Run: %v", err)
	}
	if !reflect.DeepEqual(res, hook.Result{}) {
		t.Fatalf("empty plan result = %+v, want zero Result", res)
	}

	var nilPlan *hook.Plan
	if !nilPlan.Empty() {
		t.Fatal("nil plan is not Empty")
	}
	if got := nilPlan.Hooks(hook.LaneHot, hook.PointRequestMessage); got != nil {
		t.Fatalf("nil plan Hooks = %v", got)
	}
	res, err = nilPlan.Run(context.Background(), &hook.Event{Lane: hook.LaneHot, Point: hook.PointRequestMessage})
	if err != nil || !reflect.DeepEqual(res, hook.Result{}) {
		t.Fatalf("nil plan Run = (%+v, %v)", res, err)
	}
}
