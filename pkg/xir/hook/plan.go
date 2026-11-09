package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Plan is an immutable, compiled hook chain. It is safe for concurrent use;
// Compile builds it once and Run never mutates it.
type Plan struct {
	entries []entry
	byLane  map[Lane]map[Point][]Hook
}

// entry is one compiled hook with its effective configuration.
type entry struct {
	id       string
	hook     Hook
	lanes    Lane
	priority int
	points   map[Point]struct{}
}

// Compile validates configs, builds every enabled hook and returns a Plan
// ordered by ascending Priority (lower runs first) and then by ID.
//
// Disabled configs are ignored and never looked up. For an enabled config an
// empty Points slice falls back to Hook.Points(), zero Lanes falls back to
// Hook.Lanes() and zero Priority falls back to Hook.Priority(). A hook that
// ends up with no point or no lane is a configuration error.
//
// Compile does not mutate configs; it copies the settings it passes to the
// factory and the point lists it keeps.
func Compile(reg *Registry, configs []Config) (*Plan, error) {
	plan := &Plan{}
	seen := make(map[string]struct{}, len(configs))
	entries := make([]entry, 0, len(configs))

	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		if cfg.ID == "" {
			return nil, errors.New("hook: config has empty ID")
		}
		if _, exists := seen[cfg.ID]; exists {
			return nil, fmt.Errorf("hook: duplicate config ID %q", cfg.ID)
		}
		seen[cfg.ID] = struct{}{}

		if cfg.Lanes != 0 && !cfg.Lanes.Valid() {
			return nil, fmt.Errorf("hook: config %q: invalid lanes 0x%02x", cfg.ID, uint8(cfg.Lanes))
		}
		configPoints, err := normalizePoints(fmt.Sprintf("config %q", cfg.ID), cfg.Points)
		if err != nil {
			return nil, err
		}

		factory, ok := reg.Lookup(cfg.ID)
		if !ok {
			return nil, fmt.Errorf("hook: unknown hook %q", cfg.ID)
		}
		// The factory gets its own point and settings copies so it cannot
		// retain or mutate the caller's config or the list Compile keeps.
		if configPoints != nil {
			cfg.Points = append([]Point(nil), configPoints...)
		}
		if cfg.Settings != nil {
			settings := make(json.RawMessage, len(cfg.Settings))
			copy(settings, cfg.Settings)
			cfg.Settings = settings
		}
		h, err := factory(cfg)
		if err != nil {
			return nil, fmt.Errorf("hook: build %q: %w", cfg.ID, err)
		}
		if h == nil {
			return nil, fmt.Errorf("hook: factory %q returned nil hook", cfg.ID)
		}
		if h.ID() != cfg.ID {
			return nil, fmt.Errorf("hook: factory %q returned hook with ID %q", cfg.ID, h.ID())
		}

		lanes := cfg.Lanes
		if lanes == 0 {
			lanes = h.Lanes()
		}
		if !lanes.Valid() {
			return nil, fmt.Errorf("hook: hook %q declares no valid lanes (0x%02x)", cfg.ID, uint8(lanes))
		}

		points := configPoints
		if len(points) == 0 {
			points, err = normalizePoints(fmt.Sprintf("hook %q", cfg.ID), h.Points())
			if err != nil {
				return nil, err
			}
		}
		if len(points) == 0 {
			return nil, fmt.Errorf("hook: hook %q declares no points", cfg.ID)
		}
		pointSet := make(map[Point]struct{}, len(points))
		for _, point := range points {
			pointSet[point] = struct{}{}
		}

		priority := cfg.Priority
		if priority == 0 {
			priority = h.Priority()
		}
		entries = append(entries, entry{
			id:       cfg.ID,
			hook:     h,
			lanes:    lanes,
			priority: priority,
			points:   pointSet,
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].priority != entries[j].priority {
			return entries[i].priority < entries[j].priority
		}
		return entries[i].id < entries[j].id
	})
	plan.entries = entries
	plan.buildIndex()
	return plan, nil
}

// normalizePoints validates and de-duplicates points while preserving the
// caller's order.
func normalizePoints(owner string, points []Point) ([]Point, error) {
	if len(points) == 0 {
		return nil, nil
	}
	out := make([]Point, 0, len(points))
	seen := make(map[Point]struct{}, len(points))
	for _, point := range points {
		if !point.Valid() {
			return nil, fmt.Errorf("hook: %s: unknown point %q", owner, point)
		}
		if _, exists := seen[point]; exists {
			continue
		}
		seen[point] = struct{}{}
		out = append(out, point)
	}
	return out, nil
}

// buildIndex precomputes the ordered hook list for each single lane and point.
// Hooks that declare both lanes appear in both indexes.
func (p *Plan) buildIndex() {
	p.byLane = make(map[Lane]map[Point][]Hook)
	for _, lane := range []Lane{LaneHot, LaneAudit} {
		index := make(map[Point][]Hook)
		for i := range p.entries {
			e := &p.entries[i]
			if e.lanes&lane == 0 {
				continue
			}
			for point := range e.points {
				index[point] = append(index[point], e.hook)
			}
		}
		if len(index) > 0 {
			p.byLane[lane] = index
		}
	}
}

// Empty reports whether the plan contains no hooks. A nil Plan is empty.
func (p *Plan) Empty() bool { return p == nil || len(p.entries) == 0 }

// hooksFor returns the internal ordered slice for a (lane, point) pair. The
// caller must not mutate or retain it.
func (p *Plan) hooksFor(lane Lane, point Point) []Hook {
	if p == nil || lane == 0 {
		return nil
	}
	if lane&(lane-1) == 0 {
		index := p.byLane[lane]
		if index == nil {
			return nil
		}
		return index[point]
	}
	// A combined lane is legal input; return the union in plan order.
	var out []Hook
	for i := range p.entries {
		e := &p.entries[i]
		if e.lanes&lane == 0 {
			continue
		}
		if _, ok := e.points[point]; ok {
			out = append(out, e.hook)
		}
	}
	return out
}

// Hooks returns the hooks that run for lane and point, in plan order. The
// returned slice is a fresh copy and is safe for the caller to modify.
func (p *Plan) Hooks(lane Lane, point Point) []Hook {
	matched := p.hooksFor(lane, point)
	if len(matched) == 0 {
		return nil
	}
	out := make([]Hook, len(matched))
	copy(out, matched)
	return out
}

// Run executes the hooks that match ev's lane and point in plan order and
// merges their results.
//
// A non-nil Block from a hook stops the plan and is returned in the result.
// When a hook fails, its FailPolicy decides: FailClosed returns an error that
// wraps the hook error and names the hook; FailOpen discards that hook's
// partial result, appends a RuleHookError diagnostic finding and continues.
// A context cancellation stops the plan regardless of the hook fail policy.
func (p *Plan) Run(ctx context.Context, ev *Event) (Result, error) {
	var out Result
	if ctx == nil {
		return out, errors.New("hook: nil context")
	}
	if ev == nil {
		return out, errors.New("hook: nil event")
	}
	if p.Empty() {
		return out, nil
	}

	for _, h := range p.hooksFor(ev.Lane, ev.Point) {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		res, err := invoke(ctx, h, ev)
		if err != nil {
			if h.FailPolicy() != FailOpen {
				return out, fmt.Errorf("hook %q: %w", h.ID(), err)
			}
			out.Findings = append(out.Findings, Finding{
				Hook:     h.ID(),
				Rule:     RuleHookError,
				Severity: SeverityMedium,
				Point:    ev.Point,
				Path:     ev.Path,
				Span:     ev.Span,
				Message:  err.Error(),
			})
			continue
		}
		out.merge(res)
		if out.Block != nil {
			return out, nil
		}
	}
	return out, nil
}

// invoke calls a hook and converts a panic into an error so one faulty hook
// cannot take down the forwarding path. The caller applies the fail policy.
func invoke(ctx context.Context, h Hook, ev *Event) (res Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return h.Handle(ctx, ev)
}
