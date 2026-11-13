package identify

import (
	"reflect"
	"testing"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// These tests are the consistency gate: a single-point probe must be a pure
// view of the combined scan, and the capability catalog must be derived from
// the same rule table the engine runs. If a probe ever grows its own algorithm,
// these fail.

// corpus mixes every shape family so the probes are exercised together.
func corpus() []byte {
	return []byte(
		"OPENAI_API_KEY=" + openAIKey + "\n" +
			"aws " + awsKey + "\n" +
			"身份证 " + validID + "\n" +
			"手机号：" + validMobile + "\n" +
			"postgres://user:secretpw@db.example.com:5432/app\n" +
			"see https://example.com/a/b?x=1 and /usr/local/bin/tool\n" +
			"Bearer abcdefghijklmnopqrstuvwxyz123456\n",
	)
}

func TestSinglePointIsViewOfCombined(t *testing.T) {
	src := corpus()
	combined := Scan(src)

	// Every credential/PII probe equals filtering the combined scan.
	if got, want := ChineseID(src), filterRule(combined, RuleChineseID); !reflect.DeepEqual(got, want) {
		t.Errorf("ChineseID diverges from combined view:\n got %+v\nwant %+v", got, want)
	}
	if got, want := CNMobile(src), filterRule(combined, RuleCNMobile); !reflect.DeepEqual(got, want) {
		t.Errorf("CNMobile diverges from combined view:\n got %+v\nwant %+v", got, want)
	}
	if got, want := PII(src), filterKind(combined, "national_id", "mobile"); !reflect.DeepEqual(got, want) {
		t.Errorf("PII diverges from combined view:\n got %+v\nwant %+v", got, want)
	}

	// ScanRule over the union machine equals filtering Scan for a credential rule.
	if got, want := ScanRule(src, "openai.legacy_key"), filterRule(combined, "openai.legacy_key"); !reflect.DeepEqual(got, want) {
		t.Errorf("ScanRule diverges from combined view:\n got %+v\nwant %+v", got, want)
	}
}

func TestScanAllEqualsUnionOfMachines(t *testing.T) {
	src := corpus()
	all := ScanAll(src)
	// Every combined match and every indicator match appears in ScanAll.
	for _, m := range append(Scan(src), ScanIndicators(src)...) {
		if !containsMatch(all, m) {
			t.Errorf("ScanAll missing match %+v", m)
		}
	}
	if len(all) != len(Scan(src))+len(ScanIndicators(src)) {
		t.Errorf("ScanAll count mismatch")
	}
}

func TestCatalogMatchesEngine(t *testing.T) {
	rules := Rules()
	engine := secretmask.Rules()
	var engineNames []string
	for _, r := range engine {
		if r.Name != "" {
			engineNames = append(engineNames, r.Name)
		}
	}
	if len(rules) != len(engineNames) {
		t.Fatalf("Rules() = %d, engine = %d", len(rules), len(engineNames))
	}
	index := map[string]RuleInfo{}
	for _, r := range rules {
		index[r.Name] = r
	}
	for _, name := range engineNames {
		if _, ok := index[name]; !ok {
			t.Errorf("Rules() missing engine rule %q", name)
		}
	}
	// RuleByName agrees with the catalog entry.
	for _, r := range rules {
		got, ok := RuleByName(r.Name)
		if !ok || got != r {
			t.Errorf("RuleByName(%q) = %+v, ok=%v; want %+v", r.Name, got, ok, r)
		}
	}
	if _, ok := RuleByName("no.such.rule"); ok {
		t.Error("RuleByName should reject an unknown name")
	}
}

func TestCapabilitiesCoverEveryRuleExactlyOnce(t *testing.T) {
	caps := Capabilities()
	seen := map[string]int{}
	for _, c := range caps {
		if len(c.Rules) == 0 {
			t.Errorf("capability %q has no rules", c.ID)
		}
		for _, name := range c.Rules {
			seen[name]++
		}
	}
	for _, r := range Rules() {
		if seen[r.Name] != 1 {
			t.Errorf("rule %q covered %d times, want 1", r.Name, seen[r.Name])
		}
	}
	// The access-indicator capability is report-only; the others are not.
	for _, c := range caps {
		if c.ID == "access_indicator" && !c.ReportOnly {
			t.Error("access_indicator capability should be report-only")
		}
		if c.ID != "access_indicator" && c.ReportOnly {
			t.Errorf("capability %q should not be report-only", c.ID)
		}
	}
}

func filterKind(matches []Match, kinds ...string) []Match {
	var out []Match
	for _, m := range matches {
		for _, k := range kinds {
			if m.Kind == k {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

func containsMatch(matches []Match, want Match) bool {
	for _, m := range matches {
		if m == want {
			return true
		}
	}
	return false
}
