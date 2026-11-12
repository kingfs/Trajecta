package detector

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kingfs/Trajecta/pkg/detect"
	detectsecretmask "github.com/kingfs/Trajecta/pkg/detect/secretmask"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// Plugin IDs of the builtin byte/span scanners.
const (
	PluginSecretmask = "secretmask"
	PluginIndicators = "access.indicators"
)

func init() {
	mustRegister(Registration{
		Plugin: Plugin{
			ID:             PluginSecretmask,
			Name:           "Credential & PII masking",
			NameZh:         "凭据与个人数据遮蔽",
			Description:    "The compiled credential/PII DFA plus the name-anchored assignment and DSN passes. Detects API keys, tokens, private keys, national IDs and mobile numbers; maskable findings are rendered into format-preserving stand-ins by the Vault on the hot lane.",
			DescriptionZh:  "编译期凭据/个人数据 DFA，外加环境变量赋值与 DSN 原生扫描。识别 API key、token、私钥、身份证号与手机号；热车道上由 Vault 将可遮蔽命中渲染为保形假值。",
			Kind:           "credential_masking",
			Contract:       ContractScanner,
			Lanes:          []Lane{LaneHot, LaneAudit},
			Builtin:        true,
			DefaultEnabled: true,
			Rules:          secretmaskRuleCatalog(false),
		},
		NewScanner: newSecretmaskScanner,
	})
	mustRegister(Registration{
		Plugin: Plugin{
			ID:             PluginIndicators,
			Name:           "Access indicators",
			NameZh:         "访问指示符",
			Description:    "The separate path/URL indicator machine. Report-only: it surfaces the local paths and URLs an exchange touched, never rewrites them. Gates the audit detection lane; the resources index keeps its own dedicated path.",
			DescriptionZh:  "独立的路径/URL 指示符机器。仅报告：呈现交换触及的本地路径与 URL，绝不改写。门控审计检测车道；资源索引仍走其专用路径。",
			Kind:           "access_indicator",
			Contract:       ContractScanner,
			Lanes:          []Lane{LaneAudit},
			Builtin:        true,
			DefaultEnabled: false,
			Rules:          secretmaskRuleCatalog(true),
		},
		NewScanner: newIndicatorScanner,
	})
}

func mustRegister(reg Registration) {
	if err := Register(reg); err != nil {
		panic(err)
	}
}

// secretmaskRuleCatalog projects the compiled rule table into catalog entries,
// selecting either the maskable credential/PII rules or the report-only access
// indicators.
func secretmaskRuleCatalog(reportOnly bool) []RuleInfo {
	var out []RuleInfo
	for _, r := range secretmask.Rules() {
		if r.ReportOnly != reportOnly {
			continue
		}
		out = append(out, RuleInfo{Name: r.Name, Kind: r.Kind.String(), Vendor: r.Vendor, ReportOnly: r.ReportOnly})
	}
	return out
}

// secretmaskSettings is the per-plugin configuration of the secretmask scanner.
// Rule-level switches live here rather than in separate plugins: the DFA is one
// compiled machine and cannot be split per rule.
type secretmaskSettings struct {
	// DisablePII drops the personal-data rules (national ID, mobile number)
	// while keeping credential detection.
	DisablePII bool `json:"disable_pii,omitempty"`
	// DisabledRules drops individual rules by stable name.
	DisabledRules []string `json:"disabled_rules,omitempty"`
}

func newSecretmaskScanner(s Settings) (detect.Scanner, error) {
	var cfg secretmaskSettings
	if len(s) > 0 {
		if err := json.Unmarshal(s, &cfg); err != nil {
			return nil, fmt.Errorf("detector: secretmask settings: %w", err)
		}
	}
	base := detectsecretmask.New()
	if !cfg.DisablePII && len(cfg.DisabledRules) == 0 {
		return base, nil
	}
	disabled := make(map[string]bool, len(cfg.DisabledRules))
	for _, name := range cfg.DisabledRules {
		disabled[name] = true
	}
	return &filteredScanner{name: PluginSecretmask, base: base, disabled: disabled, disablePII: cfg.DisablePII}, nil
}

// filteredScanner wraps a scanner and drops matches by rule name or PII kind.
// It is how a rule-level Settings switch narrows the compiled DFA's output
// without touching the machine.
type filteredScanner struct {
	name       string
	base       detect.Scanner
	disabled   map[string]bool
	disablePII bool
}

func (s *filteredScanner) Name() string { return s.name }

func (s *filteredScanner) Scan(ctx context.Context, in detect.Input) ([]detect.Match, error) {
	matches, err := s.base.Scan(ctx, in)
	if err != nil {
		return nil, err
	}
	out := matches[:0]
	for _, m := range matches {
		if s.disabled[m.Rule] {
			continue
		}
		if s.disablePII {
			if rule, ok := secretmask.RuleByName(m.Rule); ok && rule.Kind.PII() {
				continue
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// newIndicatorScanner builds the report-only access-indicator scanner.
func newIndicatorScanner(Settings) (detect.Scanner, error) {
	return indicatorScanner{}, nil
}

// indicatorScanner adapts the separate indicator machine to detect.Scanner. Its
// matches are always report-only: the audit lane shows them, the hot lane never
// rewrites them (the Vault also refuses ReportOnly candidates).
type indicatorScanner struct{}

func (indicatorScanner) Name() string { return PluginIndicators }

func (indicatorScanner) Scan(ctx context.Context, in detect.Input) ([]detect.Match, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if len(in.Raw) == 0 {
		return nil, nil
	}
	spans := in.Spans
	if len(spans) == 0 {
		spans = []detect.Span{{Start: 0, End: len(in.Raw)}}
	}
	var out []detect.Match
	for _, scope := range spans {
		if !scope.Valid() || scope.Start < 0 || scope.End > len(in.Raw) || scope.Start > scope.End {
			continue
		}
		findings := secretmask.Detector{}.IndicatorsWithOptions(in.Raw[scope.Start:scope.End], secretmask.IndicatorOptions{})
		for _, f := range findings {
			rule, _ := f.Rule()
			out = append(out, detect.Match{
				Span: detect.Span{
					Start: scope.Start + f.Start,
					End:   scope.Start + f.End,
					Path:  scope.Path,
					Kind:  scope.Kind,
				},
				Rule:       rule.Name,
				Confidence: 0.4,
				Value:      string(f.Bytes(in.Raw[scope.Start:scope.End])),
				Metadata: map[string]string{
					"maskable":    "false",
					"report_only": "true",
					"kind":        rule.Kind.String(),
				},
			})
		}
	}
	return out, nil
}
