// Package detect defines the generic detection framework shared by the hot
// and audit lanes.
//
// A Scanner inspects a caller-owned byte buffer and reports byte-ranged
// Matches. The Engine owns a fixed, ordered set of scanners and is the only
// place that enforces the contract between them:
//
//   - scanners run in registration order;
//   - a match must be a non-empty span inside the raw buffer and, when the
//     caller declares a scan scope, fully inside one declared span;
//   - confidence is clamped into [0,1];
//   - duplicate (start, end, rule) triples collapse to one match;
//   - the result is sorted by start, then end, then rule.
//
// The package contains no detection rule and never rewrites bytes: credential,
// opaque-block and policy scanners live in their own packages and are adapted
// to Scanner. Detection is read-only and stdlib-only.
package detect

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Errors reported by Register.
var (
	// ErrInvalidScanner means the scanner is nil or has an empty Name.
	ErrInvalidScanner = errors.New("detect: invalid scanner")
	// ErrDuplicateScanner means another scanner with the same Name is
	// already registered.
	ErrDuplicateScanner = errors.New("detect: duplicate scanner name")
)

// Input is one request to the detection framework.
type Input struct {
	// Raw is the buffer every match span points into. It is not copied and
	// not modified.
	Raw []byte
	// Spans, when non-empty, restricts detection to those regions: a match
	// that is not fully inside one of them is rejected. Spans are clamped
	// into Raw before use. An empty Spans means the whole buffer.
	Spans []Span
	// Protocol names the wire protocol of Raw, for example "chat_completions".
	// The Engine passes it through untouched.
	Protocol string
	// Meta carries caller context such as a trace id. The Engine passes it
	// through untouched.
	Meta map[string]string
}

// Scanner is one detector. Name is its identity in an Engine and must be
// non-empty and unique; Scan reports matches in any order.
//
// Scan must not retain or modify in.Raw. It may be called concurrently on the
// same Scanner if the Engine is shared, so an implementation that is not
// read-only must synchronize internally.
type Scanner interface {
	Name() string
	Scan(ctx context.Context, in Input) ([]Match, error)
}

// Engine runs a set of scanners and returns one normalized match list.
//
// The zero value is ready to use and has no scanners. An Engine is safe for
// concurrent Scan and Scanners calls; Register may also run concurrently with
// them. Matching does not depend on the order in which concurrent Register
// calls happen beyond their actual completion order.
type Engine struct {
	mu       sync.RWMutex
	scanners []Scanner
	names    map[string]struct{}
}

// New returns an Engine holding scanners in argument order.
//
// New cannot fail, so it skips nil scanners, scanners with an empty name and
// later scanners whose name repeats an earlier one; use Register to learn
// about those.
func New(scanners ...Scanner) *Engine {
	e := &Engine{names: make(map[string]struct{}, len(scanners))}
	for _, s := range scanners {
		if s == nil {
			continue
		}
		_ = e.Register(s)
	}
	return e
}

// Register appends s to the end of the scanner order. It returns
// ErrInvalidScanner for a nil scanner or an empty name, and ErrDuplicateScanner
// for a name that is already taken. A failed Register leaves the Engine
// unchanged.
func (e *Engine) Register(s Scanner) error {
	if s == nil {
		return fmt.Errorf("%w: nil", ErrInvalidScanner)
	}
	name := s.Name()
	if name == "" {
		return fmt.Errorf("%w: empty name", ErrInvalidScanner)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.names[name]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicateScanner, name)
	}
	if e.names == nil {
		e.names = make(map[string]struct{})
	}
	e.names[name] = struct{}{}
	e.scanners = append(e.scanners, s)
	return nil
}

// Scanners returns a copy of the registered scanners in registration order.
// The Engine keeps its own slice, so the caller may modify the result.
func (e *Engine) Scanners() []Scanner {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.scanners) == 0 {
		return nil
	}
	out := make([]Scanner, len(e.scanners))
	copy(out, e.scanners)
	return out
}

// Scan runs the registered scanners in registration order against in and
// returns the normalized matches.
//
// Matches whose span is invalid or does not fit in Raw are dropped, not
// clamped: a span outside the buffer means the scanner and the caller disagree
// about the bytes, and moving it would report a value the caller never
// supplied. The same applies to a match outside the declared scan scope, while
// the declared spans themselves are clamped into Raw. When every declared span
// clamps away, nothing matches.
//
// A scanner error aborts the scan and is returned wrapped with the scanner
// name; matches collected so far are discarded. A nil ctx is treated as
// context.Background().
func (e *Engine) Scan(ctx context.Context, in Input) ([]Match, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	scanners := e.Scanners()
	if len(scanners) == 0 {
		return nil, nil
	}
	collected := make([]Match, 0, 16)
	for _, s := range scanners {
		matches, err := s.Scan(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("detect: scanner %q: %w", s.Name(), err)
		}
		collected = append(collected, matches...)
	}
	return normalizeMatches(collected, in), nil
}

// normalizeMatches enforces the match contract and returns the sorted,
// deduplicated result. It never modifies the input slice.
func normalizeMatches(matches []Match, in Input) []Match {
	var scope []Span
	if len(in.Spans) > 0 {
		scope = clampSpans(in.Spans, len(in.Raw))
		if len(scope) == 0 {
			return nil
		}
	}
	out := make([]Match, 0, len(matches))
	for _, m := range matches {
		if !m.Span.Valid() || m.End > len(in.Raw) {
			continue
		}
		if len(scope) > 0 && !ContainsSpan(scope, m.Span) {
			continue
		}
		m.Confidence = clampConfidence(m.Confidence)
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil
	}
	sortMatches(out)
	return dedupeMatches(out)
}

// clampSpans clamps declared scan scopes into a buffer of n bytes and drops
// the ones that clamp away. Path and Kind survive clamping.
func clampSpans(spans []Span, n int) []Span {
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		if s.Start < 0 {
			s.Start = 0
		}
		if s.Start > n {
			s.Start = n
		}
		if s.End > n {
			s.End = n
		}
		if s.End <= s.Start {
			continue
		}
		out = append(out, s)
	}
	return out
}

// clampConfidence maps a scanner score into [0,1]. NaN and negative values
// become 0, values above 1 become 1.
func clampConfidence(c float64) float64 {
	if !(c > 0) {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}
