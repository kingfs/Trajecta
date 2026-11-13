package identify

import (
	"sort"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// RuleInfo is the public metadata of one detection rule. It mirrors the
// engine's rule table without exposing the internal RuleID numbering, so a
// caller can list, group and display capabilities using stable names only.
type RuleInfo struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Vendor     string `json:"vendor"`
	Grade      string `json:"grade"`    // "A".."D": how well the format is documented
	Strength   string `json:"strength"` // "generic" | "anchored" | "strong": what a match proves alone
	PII        bool   `json:"pii"`
	ReportOnly bool   `json:"report_only"`
	MinLen     int    `json:"min_len"`
	MaxLen     int    `json:"max_len"` // 0 means unbounded
}

// Rules returns every detection rule in stable name order. It is the full
// capability catalog: the credential/PII shapes of the union DFA, the native
// assignment/DSN shapes, and the access indicators of the second machine.
func Rules() []RuleInfo {
	engineRules := secretmask.Rules()
	out := make([]RuleInfo, 0, len(engineRules))
	for _, r := range engineRules {
		if r.Name == "" {
			continue
		}
		out = append(out, RuleInfo{
			Name:       r.Name,
			Kind:       r.Kind.String(),
			Vendor:     r.Vendor,
			Grade:      grade(r.Grade),
			Strength:   strength(r.Strength),
			PII:        r.Kind.PII(),
			ReportOnly: r.ReportOnly,
			MinLen:     r.MinLen,
			MaxLen:     r.MaxLen,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RuleByName returns the metadata of one rule by its stable name.
func RuleByName(name string) (RuleInfo, bool) {
	r, ok := secretmask.RuleByName(name)
	if !ok || r.Name == "" {
		return RuleInfo{}, false
	}
	return RuleInfo{
		Name:       r.Name,
		Kind:       r.Kind.String(),
		Vendor:     r.Vendor,
		Grade:      grade(r.Grade),
		Strength:   strength(r.Strength),
		PII:        r.Kind.PII(),
		ReportOnly: r.ReportOnly,
		MinLen:     r.MinLen,
		MaxLen:     r.MaxLen,
	}, true
}

func grade(g secretmask.Grade) string {
	switch g {
	case secretmask.GradeA:
		return "A"
	case secretmask.GradeB:
		return "B"
	case secretmask.GradeC:
		return "C"
	case secretmask.GradeD:
		return "D"
	default:
		return ""
	}
}

func strength(s secretmask.Strength) string {
	switch s {
	case secretmask.StrengthStrong:
		return "strong"
	case secretmask.StrengthAnchored:
		return "anchored"
	default:
		return "generic"
	}
}

// Capability groups the detection rules into the families a reader recognizes:
// credentials, personal data and access indicators. It is the manifest the
// Trajecta plugin layer displays — "these are the shapes this engine can
// detect" — and it is derived from the same rule table the scanner runs, so the
// display can never drift from the algorithm.
type Capability struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	NameZh        string `json:"name_zh"`
	Description   string `json:"description"`
	DescriptionZh string `json:"description_zh"`
	// ReportOnly is true when every rule in the family is report-only (never
	// rewritten), as for access indicators.
	ReportOnly bool `json:"report_only"`
	// Kinds lists the shape families in the capability.
	Kinds []string `json:"kinds"`
	// Rules lists the stable rule names in the capability, in name order.
	Rules []string `json:"rules"`
}

// Capabilities returns the detection capability manifest in a stable order:
// credentials, personal data, access indicators.
func Capabilities() []Capability {
	var cred, pii, access []RuleInfo
	for _, r := range Rules() {
		switch {
		case r.Kind == secretmask.KindURL.String() || r.Kind == secretmask.KindLocalPath.String():
			access = append(access, r)
		case r.PII:
			pii = append(pii, r)
		default:
			cred = append(cred, r)
		}
	}
	return []Capability{
		buildCapability("credential", "Credentials", "凭据",
			"API keys, tokens, private keys, JWT/JWE, bearer tokens and name-anchored assignment/DSN passwords, detected by the merged Ragel DFA plus native passes.",
			"API key、token、私钥、JWT/JWE、Bearer token，以及环境变量赋值与 DSN 口令，由合并 Ragel DFA 与原生扫描检测。",
			false, cred),
		buildCapability("pii", "Personal data", "个人数据",
			"Mainland Chinese national ID numbers and mobile numbers, detected by the same union DFA with offline validators and context scoring.",
			"中国大陆身份证号与手机号，由同一联合 DFA 配合离线校验器与上下文评分检测。",
			false, pii),
		buildCapability("access_indicator", "Access indicators", "访问指示符",
			"URLs and local filesystem paths, detected by the separate report-only indicator machine; never rewritten.",
			"URL 与本地文件系统路径，由独立的仅报告指示符机器检测；从不改写。",
			true, access),
	}
}

func buildCapability(id, name, nameZh, desc, descZh string, reportOnly bool, rules []RuleInfo) Capability {
	kindSet := make(map[string]struct{})
	names := make([]string, 0, len(rules))
	for _, r := range rules {
		kindSet[r.Kind] = struct{}{}
		names = append(names, r.Name)
	}
	kinds := make([]string, 0, len(kindSet))
	for k := range kindSet {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	sort.Strings(names)
	return Capability{
		ID: id, Name: name, NameZh: nameZh,
		Description: desc, DescriptionZh: descZh,
		ReportOnly: reportOnly, Kinds: kinds, Rules: names,
	}
}
