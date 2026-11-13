package identify

import "github.com/kingfs/Trajecta/pkg/secretmask"

// Single-point probes. Each is a typed view of the same engine scan, filtered
// to one shape or family — not a second implementation. A probe's verdicts are
// identical to the combined scan's by construction, so a standalone caller and
// the full pipeline always agree. The credential/PII probes run the union DFA
// once; ScanRule and ScanKind also cover the indicator machine so they work for
// any rule.

// Stable rule names for the most common single-point probes. The full catalog
// is Rules(); these are the shapes callers reach for most often.
const (
	RuleChineseID = "pii.chinese_id"
	RuleCNMobile  = "pii.cn_mobile"
	RuleURL       = "access.url"
	RuleLocalPath = "access.local_path"
)

// ChineseID returns the mainland Chinese national ID numbers in src. A match
// passes the offline validator (region, calendar birthdate and check
// character), so it is conclusive on its own; an eighteen-digit run inside a
// longer identifier is rejected by the word boundary. One pass over src.
func ChineseID(src []byte) []Match {
	return filterRule(Scan(src), RuleChineseID)
}

// CNMobile returns the mainland Chinese mobile numbers in src. A mobile number
// has no check digit, so a match is reported when its allocated 1[3-9]x head
// passes the candidate check; Maskable additionally requires surrounding phone
// vocabulary, exactly as the masking policy decides. One pass over src.
func CNMobile(src []byte) []Match {
	return filterRule(Scan(src), RuleCNMobile)
}

// PII returns every personal-data match in src (national IDs and mobile
// numbers) — the shapes Kind.PII separates from credentials. One pass over src.
func PII(src []byte) []Match {
	var out []Match
	for _, m := range Scan(src) {
		if m.Kind == secretmask.KindNationalID.String() || m.Kind == secretmask.KindMobile.String() {
			out = append(out, m)
		}
	}
	return out
}

// Credentials returns every credential match in src (API keys, tokens, private
// keys, JWT/JWE, bearer tokens, assignments and DSN passwords) — everything the
// union machine finds that is not personal data. One pass over src.
func Credentials(src []byte) []Match {
	var out []Match
	for _, m := range Scan(src) {
		if m.Kind != secretmask.KindNationalID.String() && m.Kind != secretmask.KindMobile.String() {
			out = append(out, m)
		}
	}
	return out
}

// ScanRule returns the matches for one stable rule name, e.g. "anthropic.api_key"
// or "access.url". It runs both machines so it works for any rule in Rules();
// an unknown name returns nil. Use a credential/PII convenience probe above when
// only the union machine is needed.
func ScanRule(src []byte, rule string) []Match {
	if rule == "" {
		return nil
	}
	return filterRule(ScanAll(src), rule)
}

// ScanKind returns the matches for one shape family, e.g. "api_key",
// "national_id" or "url". It runs both machines; an unknown kind returns nil.
func ScanKind(src []byte, kind string) []Match {
	if kind == "" {
		return nil
	}
	var out []Match
	for _, m := range ScanAll(src) {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

func filterRule(matches []Match, rule string) []Match {
	var out []Match
	for _, m := range matches {
		if m.Rule == rule {
			out = append(out, m)
		}
	}
	return out
}
