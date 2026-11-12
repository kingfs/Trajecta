package detector

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/kingfs/Trajecta/internal/analyzer"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/detect"
)

// Snapshot is an immutable compiled execution state built from a Config. The
// hot path and the read paths load it atomically; rebuilding happens only at
// startup and when an operator changes the plugin state (Republish), never per
// request.
type Snapshot struct {
	Config Config

	auditEngine *detect.Engine
	runner      *analyzer.Runner

	// secretmaskHot records whether the builtin credential/PII plugin runs on
	// the hot lane. Its findings are collected natively (keeping their RuleID so
	// rendering uses the exact compiled rule metadata) rather than through the
	// scanner adapter, which is the audit-lane path.
	secretmaskHot bool
	smSettings    secretmaskSettings
	smDisabled    map[string]bool

	// hotScanners are the enabled hot-lane scanner plugins other than the
	// builtin secretmask plugin (third-party detectors). Their maskable matches
	// become Vault candidates carrying rule metadata.
	hotScanners []detect.Scanner
}

// AuditEngine returns the compiled read-only engine for the audit detection
// lane. It is never nil for a built snapshot.
func (s *Snapshot) AuditEngine() *detect.Engine {
	if s == nil {
		return detect.New()
	}
	return s.auditEngine
}

// Runner returns the compiled Observation-IR runner for the reanalysis lane.
func (s *Snapshot) Runner() *analyzer.Runner {
	if s == nil {
		return analyzer.NewRunnerList(analyzer.DefaultDetectors())
	}
	return s.runner
}

// buildSnapshot compiles a Config into an executable snapshot. A factory error
// aborts the build so a misconfigured plugin cannot silently drop out of a lane.
func buildSnapshot(cfg Config) (*Snapshot, error) {
	enabled := cfg.enabledSet()
	snap := &Snapshot{Config: cfg}

	engine := detect.New()
	var detectors []analyzer.Detector
	for _, p := range Plugins() {
		if !enabled[p.ID] {
			continue
		}
		reg, ok := Lookup(p.ID)
		if !ok {
			continue
		}
		switch p.Contract {
		case ContractScanner:
			if p.HasLane(LaneAudit) {
				scanner, err := reg.NewScanner(cfg.SettingsFor(p.ID))
				if err != nil {
					return nil, fmt.Errorf("detector: build %q: %w", p.ID, err)
				}
				if err := engine.Register(scanner); err != nil {
					return nil, fmt.Errorf("detector: register %q: %w", p.ID, err)
				}
			}
			if p.HasLane(LaneHot) {
				if p.ID == PluginSecretmask {
					snap.secretmaskHot = true
					if raw := cfg.SettingsFor(p.ID); len(raw) > 0 {
						if err := json.Unmarshal(raw, &snap.smSettings); err != nil {
							return nil, fmt.Errorf("detector: secretmask settings: %w", err)
						}
					}
					snap.smDisabled = make(map[string]bool, len(snap.smSettings.DisabledRules))
					for _, name := range snap.smSettings.DisabledRules {
						snap.smDisabled[name] = true
					}
					continue
				}
				scanner, err := reg.NewScanner(cfg.SettingsFor(p.ID))
				if err != nil {
					return nil, fmt.Errorf("detector: build %q: %w", p.ID, err)
				}
				snap.hotScanners = append(snap.hotScanners, scanner)
			}
		case ContractObservation:
			det, err := reg.NewDetector(cfg.SettingsFor(p.ID))
			if err != nil {
				return nil, fmt.Errorf("detector: build %q: %w", p.ID, err)
			}
			detectors = append(detectors, det)
		}
	}
	snap.auditEngine = engine
	snap.runner = analyzer.NewRunnerList(detectors)
	return snap, nil
}

// Runtime owns the published snapshot and rebuilds it from the application
// database on demand. It is the detection analogue of the router's snapshot
// publication: configuration is read once per change, and the hot path only
// loads an immutable pointer.
type Runtime struct {
	st   *store.Store
	snap atomic.Pointer[Snapshot]
}

// NewRuntime loads the plugin state and publishes the first snapshot.
func NewRuntime(ctx context.Context, st *store.Store) (*Runtime, error) {
	rt := &Runtime{st: st}
	if err := rt.Republish(ctx); err != nil {
		return nil, err
	}
	return rt, nil
}

// Snapshot returns the currently published snapshot.
func (r *Runtime) Snapshot() *Snapshot {
	if r == nil {
		return nil
	}
	return r.snap.Load()
}

// Republish reloads the plugin state and atomically replaces the snapshot. The
// monitor calls it after a successful enable/disable write so a change takes
// effect without a restart; a build failure leaves the previous snapshot live.
func (r *Runtime) Republish(ctx context.Context) error {
	cfg, err := LoadConfig(ctx, r.st)
	if err != nil {
		return err
	}
	snap, err := buildSnapshot(cfg)
	if err != nil {
		return err
	}
	r.snap.Store(snap)
	return nil
}
