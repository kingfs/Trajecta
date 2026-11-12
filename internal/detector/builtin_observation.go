package detector

import (
	"github.com/kingfs/Trajecta/internal/analyzer"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// Plugin IDs of the builtin Observation-IR detectors. They wrap the existing
// analyzer detectors unchanged; the plugin layer adds catalog identity and
// enablement, not new detection logic.
const (
	PluginDangerousShell = "behavior.dangerous_shell"
	PluginCredentialRe   = "credential.regex"
	PluginPIIObservation = "pii.observation"
	PluginProviderSafety = "safety.provider_refusal"
	PluginToolError      = "tool.error"
)

func init() {
	// Points on an observation plugin are catalog metadata: the analyzer
	// detector scopes itself over the Observation IR internally, so the audit
	// node engine does not enforce them. They document which XIR fields the
	// detector reads, the same vocabulary the scanner plugins enforce.
	observation := func(id, name, nameZh, kind, desc, descZh string, points []hook.Point, factory func(Settings) (analyzer.Detector, error)) {
		mustRegister(Registration{
			Plugin: Plugin{
				ID:             id,
				Name:           name,
				NameZh:         nameZh,
				Description:    desc,
				DescriptionZh:  descZh,
				Kind:           kind,
				Contract:       ContractObservation,
				Lanes:          []Lane{LaneAudit},
				Points:         points,
				Builtin:        true,
				DefaultEnabled: true,
			},
			NewDetector: factory,
		})
	}
	textPoints := []hook.Point{
		hook.PointRequestSystem,
		hook.PointRequestMessage,
		hook.PointRequestToolResult,
		hook.PointResponseOutput,
		hook.PointResponseToolResult,
	}
	observation(PluginDangerousShell, "Dangerous shell commands", "危险 Shell 命令", "behavior",
		"Flags destructive or high-risk shell commands in the model's tool calls.",
		"标记模型工具调用中的破坏性或高风险 shell 命令。",
		[]hook.Point{hook.PointResponseToolCall},
		func(Settings) (analyzer.Detector, error) { return analyzer.DangerousShellDetector{}, nil })
	observation(PluginCredentialRe, "Credential leakage (regex)", "凭据泄漏（正则）", "credential",
		"Pattern-based credential leakage detector over the Observation IR; independent of the masking DFA.",
		"基于正则的凭据泄漏检测器，作用于 Observation IR；独立于遮蔽 DFA。",
		textPoints,
		func(Settings) (analyzer.Detector, error) { return analyzer.CredentialDetector{}, nil })
	observation(PluginPIIObservation, "Personal data (observation)", "个人数据（观测）", "pii",
		"Reports maskable personal-data findings (national ID, mobile) from the masking detector over the Observation IR.",
		"基于遮蔽检测器，在 Observation IR 上报告可遮蔽的个人数据命中（身份证、手机号）。",
		textPoints,
		func(Settings) (analyzer.Detector, error) { return analyzer.PIIDetector{}, nil })
	observation(PluginProviderSafety, "Provider safety / refusal", "提供方安全拦截", "safety",
		"Detects provider safety blocks and refusals in the model response.",
		"检测模型响应中的提供方安全拦截与拒答。",
		[]hook.Point{hook.PointResponseOutput, hook.PointResponseError},
		func(Settings) (analyzer.Detector, error) { return analyzer.ProviderSafetyDetector{}, nil })
	observation(PluginToolError, "Tool errors", "工具错误", "tool",
		"Detects failed tool executions surfaced in the exchange.",
		"检测交换中呈现的工具执行失败。",
		[]hook.Point{hook.PointRequestToolResult, hook.PointResponseToolResult},
		func(Settings) (analyzer.Detector, error) { return analyzer.ToolErrorDetector{}, nil })
}
