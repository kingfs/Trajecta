// Package detector is the detection-algorithm plugin layer.
//
// It mirrors the channel-plugin pattern (pkg/upstream + internal/monitor's
// server_plugins.go): a compiled catalog of plugin descriptors, a runtime
// enable/disable list stored in the application database under the app_settings
// key plugins.detectors, and an admin API that toggles a plugin without touching
// the forwarding core. A plugin is one detection algorithm family — the
// credential/PII DFA, the access-indicator machine, or one observation detector
// — not a single rule. Rule-level switches are a plugin's Settings.
//
// Two execution contracts coexist because the codebase already has two:
//
//   - ContractScanner: a pkg/detect.Scanner over bytes and spans. It feeds the
//     audit XIR projection (read-only) and the hot masking lane (its maskable
//     matches become secretmask.MaskCandidate values the Vault renders).
//   - ContractObservation: an internal/analyzer.Detector over the Observation
//     IR. It feeds the reanalysis runner that writes trace_findings.
//
// The catalog, enablement, API and UI are shared across both; only the execution
// adapter differs. Replacement itself is never pluggable: the hot lane hands
// candidates to secretmask.Vault, which centrally enforces equal-length
// stand-ins, opaque protection, idempotency, suppression and stream restore. A
// plugin decides what is detected, never how a value is rewritten or restored.
package detector

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/kingfs/Trajecta/internal/analyzer"
	"github.com/kingfs/Trajecta/pkg/detect"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// Contract names the execution interface a plugin implements.
type Contract string

const (
	// ContractScanner is a byte/span scanner (pkg/detect.Scanner).
	ContractScanner Contract = "scanner"
	// ContractObservation is an Observation-IR detector (analyzer.Detector).
	ContractObservation Contract = "observation"
)

// Lane names an execution path a plugin participates in.
type Lane string

const (
	// LaneHot is the forwarding path (masking).
	LaneHot Lane = "hot"
	// LaneAudit is the asynchronous/read analysis path.
	LaneAudit Lane = "audit"
)

// RuleInfo is one rule a scanner plugin exposes, for the catalog detail view.
// It carries no pattern or validator: those stay inside the plugin.
type RuleInfo struct {
	Name       string `json:"name"`
	Kind       string `json:"kind,omitempty"`
	Vendor     string `json:"vendor,omitempty"`
	ReportOnly bool   `json:"report_only,omitempty"`
}

// Plugin is the catalog descriptor of one detection algorithm family. It is
// immutable metadata; the executable factory lives in the Registration.
type Plugin struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	NameZh        string   `json:"name_zh,omitempty"`
	Description   string   `json:"description,omitempty"`
	DescriptionZh string   `json:"description_zh,omitempty"`
	Kind          string   `json:"kind"`
	Contract      Contract `json:"contract"`
	Lanes         []Lane   `json:"lanes"`
	// Points declares the XIR fields the plugin acts on, using the pkg/xir/hook
	// point vocabulary. The audit lane feeds a plugin only the projection nodes
	// whose point it declares, so a plugin scoped to message text never sees a
	// sealed (encrypted reasoning / media) block: those nodes carry no text by
	// construction. An empty Points means the plugin is not point-scoped (the
	// audit lane runs it on every text node, the pre-scope behaviour).
	Points  []hook.Point `json:"points,omitempty"`
	Builtin bool         `json:"builtin"`
	// DefaultEnabled is the state used when no app_settings row exists yet. It
	// preserves the behaviour the binary had before pluginisation: masking and
	// the builtin analysis detectors start on, unlike channel plugins which
	// start disabled.
	DefaultEnabled bool       `json:"default_enabled"`
	Rules          []RuleInfo `json:"rules,omitempty"`
}

// HasLane reports whether the plugin participates in lane.
func (p Plugin) HasLane(lane Lane) bool {
	for _, l := range p.Lanes {
		if l == lane {
			return true
		}
	}
	return false
}

// HasPoint reports whether the plugin declares point. A plugin with no Points
// is not point-scoped and matches every point (see Plugin.Points).
func (p Plugin) HasPoint(point hook.Point) bool {
	if len(p.Points) == 0 {
		return true
	}
	for _, pt := range p.Points {
		if pt == point {
			return true
		}
	}
	return false
}

// Settings is opaque per-plugin configuration, passed to the factory. The
// plugin owns its schema; the catalog only carries the bytes.
type Settings = json.RawMessage

// Registration binds a descriptor to the factory that builds its executable.
// Exactly one factory is set, matching Plugin.Contract.
type Registration struct {
	Plugin Plugin
	// NewScanner builds a byte/span scanner (ContractScanner).
	NewScanner func(Settings) (detect.Scanner, error)
	// NewDetector builds an Observation-IR detector (ContractObservation).
	NewDetector func(Settings) (analyzer.Detector, error)
}

// Registry maps plugin IDs to registrations. The zero value is ready to use.
// Register is normally called once at startup (builtin plugins register from
// init); Lookup and Plugins are safe for concurrent use.
type Registry struct {
	mu   sync.RWMutex
	regs map[string]Registration
}

// Register adds a registration. It rejects an empty ID, a nil factory for the
// declared contract, and a duplicate ID.
func (r *Registry) Register(reg Registration) error {
	p := reg.Plugin
	if p.ID == "" {
		return fmt.Errorf("detector: registration has empty ID")
	}
	switch p.Contract {
	case ContractScanner:
		if reg.NewScanner == nil {
			return fmt.Errorf("detector: plugin %q has no scanner factory", p.ID)
		}
	case ContractObservation:
		if reg.NewDetector == nil {
			return fmt.Errorf("detector: plugin %q has no detector factory", p.ID)
		}
	default:
		return fmt.Errorf("detector: plugin %q has unknown contract %q", p.ID, p.Contract)
	}
	if len(p.Lanes) == 0 {
		return fmt.Errorf("detector: plugin %q declares no lanes", p.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.regs == nil {
		r.regs = make(map[string]Registration)
	}
	if _, exists := r.regs[p.ID]; exists {
		return fmt.Errorf("detector: duplicate plugin %q", p.ID)
	}
	r.regs[p.ID] = reg
	return nil
}

// Lookup returns the registration for id.
func (r *Registry) Lookup(id string) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.regs[id]
	return reg, ok
}

// Plugins returns every descriptor sorted by ID. The returned slice is a copy.
func (r *Registry) Plugins() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Plugin, 0, len(r.regs))
	for _, reg := range r.regs {
		out = append(out, reg.Plugin)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// IDs returns every registered plugin ID in sorted order.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.regs))
	for id := range r.regs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// defaultRegistry holds the compiled builtin catalog. Third-party plugins may
// register into it at startup; the enablement list still gates what runs.
var defaultRegistry = &Registry{}

// Register adds a registration to the default catalog.
func Register(reg Registration) error { return defaultRegistry.Register(reg) }

// Lookup resolves a plugin from the default catalog.
func Lookup(id string) (Registration, bool) { return defaultRegistry.Lookup(id) }

// Plugins returns the default catalog's descriptors sorted by ID.
func Plugins() []Plugin { return defaultRegistry.Plugins() }

// IDs returns the default catalog's plugin IDs sorted.
func IDs() []string { return defaultRegistry.IDs() }
