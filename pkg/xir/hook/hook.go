// Package hook defines the XIR hook contract and its compiled execution plan.
//
// A hook is a small operation bound to one or more Points. Hooks are declared
// as configuration (Config), built by a Factory and compiled once into an
// immutable Plan. The hot path only looks up the hooks for a (lane, point)
// pair; it never parses configuration or reflects.
//
// A hook inspects an Event, which carries both the lossless wire view and the
// typed semantic view, and returns a Result: findings, byte edits, an optional
// block, usage and tags. Plan.Run executes matching hooks in priority order,
// merges their results, stops on the first block and applies each hook's
// FailPolicy.
//
// Hooks must be stateless and safe for concurrent use: one hook value is
// shared by every request that runs the plan.
package hook

import (
	"context"
	"encoding/json"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// RuleHookError is the Rule of the diagnostic finding Plan.Run records when a
// FailOpen hook returns an error or panics.
const RuleHookError = "hook.error"

// Finding is one observation or policy violation produced by a hook.
//
// Path and Span locate the evidence in the exchange (for example a JSON path
// and the byte range backing it); Evidence is an optional human-readable
// excerpt or reference. Meta carries hook-private labels.
type Finding struct {
	Hook     string
	Rule     string
	Severity Severity
	Point    Point
	Path     string
	Span     wire.Span
	Message  string
	Evidence string
	Meta     map[string]string
}

// Block is a hook's request to stop the exchange. Status is the HTTP status
// the gateway should return (0 means the gateway default).
type Block struct {
	Status  int
	Code    string
	Message string
}

// Result is what one hook returns. The zero value is an empty result.
//
// Edits are byte-level changes against the event's wire document; callers
// apply them with wire.Apply. A non-nil Block stops the plan immediately.
type Result struct {
	Findings []Finding
	Edits    []wire.Edit
	Block    *Block
	Usage    *usage.Usage
	Tags     map[string]string
}

// merge appends and folds other into r. It copies every mutable container it
// keeps so the result never aliases hook state or plan state.
func (r *Result) merge(other Result) {
	for _, f := range other.Findings {
		if f.Meta != nil {
			meta := make(map[string]string, len(f.Meta))
			for k, v := range f.Meta {
				meta[k] = v
			}
			f.Meta = meta
		}
		r.Findings = append(r.Findings, f)
	}
	for _, e := range other.Edits {
		if e.Bytes != nil {
			e.Bytes = append([]byte(nil), e.Bytes...)
		}
		r.Edits = append(r.Edits, e)
	}
	if other.Block != nil {
		block := *other.Block
		r.Block = &block
	}
	if other.Usage != nil {
		if r.Usage == nil {
			u := *other.Usage
			r.Usage = &u
		} else {
			u := usage.Merge(*r.Usage, *other.Usage)
			r.Usage = &u
		}
	}
	for k, v := range other.Tags {
		if r.Tags == nil {
			r.Tags = make(map[string]string, len(other.Tags))
		}
		r.Tags[k] = v
	}
}

// Event is the input to every hook.
//
// The plan passes the same Event pointer to the matching hooks in order, so a
// hook must treat every field as read-only. Hooks communicate through Result.
// The representation pointers are nil unless the caller decoded that view:
// Doc/Node for the wire view, Request/Response/Part for the semantic view.
type Event struct {
	Lane     Lane
	Point    Point
	Protocol xir.Protocol
	Doc      *xir.Document
	Request  *xir.Request
	Response *xir.Response
	Part     *xir.Part
	Node     *wire.Node
	Path     string
	Span     wire.Span
	Text     string
	Meta     map[string]string
}

// Hook is one pluggable operation. Implementations must be stateless and safe
// for concurrent use.
//
// Points, Lanes and Priority are defaults: Compile uses the matching Config
// field when it is explicitly set. Lower Priority values run first.
type Hook interface {
	ID() string
	Points() []Point
	Lanes() Lane
	Needs() Needs
	Priority() int
	FailPolicy() FailPolicy

	Handle(ctx context.Context, ev *Event) (Result, error)
}

// Config binds a registered hook implementation to an execution plan.
//
// Enabled must be true for the config to be compiled. Points and Lanes are
// optional overrides: an empty Points slice means "use Hook.Points()", and a
// zero Lanes means "use Hook.Lanes()". Priority has the same rule (zero means
// "use Hook.Priority()"); a hook that needs priority zero should return zero
// from Hook.Priority, which is also the case when neither side names one.
//
// Settings is opaque hook configuration. Compile passes a copy to the Factory,
// so the factory may retain it without aliasing the caller's value.
type Config struct {
	ID       string
	Enabled  bool
	Lanes    Lane
	Points   []Point
	Priority int
	Settings json.RawMessage
}

// Factory builds one hook from its config. It is called by Compile and may
// reject a config it does not understand.
type Factory func(Config) (Hook, error)
