package secretmask

// Byte-level gate in front of the credential DFA.
//
// The DFA is a longest-match scanner over a union of 70+ patterns, and it pays
// its per-byte cost even for a body that cannot contain any of them. Almost
// every maskable rule is anchored by a literal (a vendor prefix, "://", a
// variable-name suffix) or by a shape that is cheap to recognise (a digit run
// for the personal-data classes, 32 hex bytes plus '.' for Zhipu). Refusing the
// whole scan when none of those necessary conditions holds is exact, not a
// heuristic: the gate is derived from the same rule table the machine is
// generated from, and a test asserts that every positive sample for every rule
// passes it. A false positive only costs a scan; a false negative would hide a
// credential, so the completeness test fails the build when a new rule is added
// without a trigger or a shape.
//
// The gate also covers the native passes (assignment, URL credential, DSN):
// they are anchored by env-name suffixes and "://" as well.

// nativeTriggerLiterals are the anchors of the native passes in assignment.go
// and dsn.go. assignment matches only uppercase env spellings that end in
// "_KEY" or "_PASSWORD"; url_credential matches "_URL"/"_URI"/"_DSN"/
// "_CONNECTION_STRING"; dsn.password requires "://".
var nativeTriggerLiterals = [...]string{
	"_KEY", "_PASSWORD", "_URL", "_URI", "_DSN", "_CONNECTION_STRING", "://",
	// JWT and JWE both start with the base64url of '{"' (eyJ). They have no
	// Prefixes entry because the rendering code never needs to protect a head.
	"eyJ",
}

// nativeTriggerRuleNames are the rules guaranteed by a nativeTriggerLiterals
// anchor rather than by a Rule.Prefixes entry.
var nativeTriggerRuleNames = map[string]bool{
	"structural.jwt":        true,
	"structural.jwe":        true,
	"env.secret_assignment": true,
	"env.url_credential":    true,
	"dsn.password":          true,
}

// shapeTriggerNames are the rules whose guarantee is a byte shape rather than a
// literal. RuleName -> the shape check it needs. Any rule that is neither
// prefixed nor listed here fails TestPrefilterCoversEveryRule.
var shapeTriggerNames = map[string]string{
	"pii.chinese_id": "digit_run_18",
	"pii.cn_mobile":  "digit_run_11",
	"zhipu.api_key":  "hex32_dot",
}

// prefilter is the compiled multi-pattern automaton for the literal triggers.
var prefilter = buildPrefilter()

type prefilterNode struct {
	// next is indexed by the low 7 bits of a byte. Every trigger is ASCII, so
	// a byte >= 0x80 never matches and never needs a transition.
	next [128]int32
	fail int32
	out  bool
}

type prefilterAutomaton struct {
	nodes []prefilterNode
}

func buildPrefilter() *prefilterAutomaton {
	a := &prefilterAutomaton{nodes: []prefilterNode{{}}}
	add := func(pattern string) {
		if pattern == "" {
			return
		}
		state := int32(0)
		for i := 0; i < len(pattern); i++ {
			b := pattern[i]
			if b >= 0x80 {
				return
			}
			next := a.nodes[state].next[b]
			if next == 0 {
				a.nodes = append(a.nodes, prefilterNode{})
				next = int32(len(a.nodes) - 1)
				a.nodes[state].next[b] = next
			}
			state = next
		}
		a.nodes[state].out = true
	}
	for _, rule := range ruleTable {
		if rule.ReportOnly {
			continue
		}
		for _, prefix := range rule.Prefixes {
			add(prefix)
		}
	}
	for _, literal := range nativeTriggerLiterals {
		add(literal)
	}
	a.buildFailure()
	return a
}

// buildFailure fills the Aho-Corasick failure links with a breadth-first walk.
func (a *prefilterAutomaton) buildFailure() {
	queue := make([]int32, 0, len(a.nodes))
	for b := 0; b < 128; b++ {
		if next := a.nodes[0].next[b]; next != 0 {
			a.nodes[next].fail = 0
			queue = append(queue, next)
		}
	}
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		for b := 0; b < 128; b++ {
			next := a.nodes[state].next[b]
			if next == 0 {
				continue
			}
			fail := a.nodes[state].fail
			for fail != 0 && a.nodes[fail].next[b] == 0 {
				fail = a.nodes[fail].fail
			}
			if candidate := a.nodes[fail].next[b]; candidate != 0 && candidate != next {
				a.nodes[next].fail = candidate
			} else {
				a.nodes[next].fail = 0
			}
			a.nodes[next].out = a.nodes[next].out || a.nodes[a.nodes[next].fail].out
			queue = append(queue, next)
		}
	}
}

// containsAnyTrigger reports whether src contains one of the literal triggers.
func (a *prefilterAutomaton) containsAnyTrigger(src []byte) bool {
	if a == nil || len(a.nodes) == 0 {
		return true
	}
	state := int32(0)
	for i := 0; i < len(src); i++ {
		b := src[i]
		if b >= 0x80 {
			state = 0
			continue
		}
		for state != 0 && a.nodes[state].next[b] == 0 {
			state = a.nodes[state].fail
		}
		state = a.nodes[state].next[b]
		if state != 0 && a.nodes[state].out {
			return true
		}
	}
	return false
}

// mayContainSecret is the exact necessary condition for Detector.Scan to find
// anything: every credentialed rule is anchored by a literal trigger or by one
// of the two numeric shapes.
func mayContainSecret(src []byte) bool {
	if len(src) == 0 {
		return false
	}
	if prefilter.containsAnyTrigger(src) {
		return true
	}
	return hasNumericTrigger(src)
}

// hasNumericTrigger recognises the digit-run and hex-run shapes.
//
//   - a run of 11 or more digits can carry a mainland mobile number (11);
//   - a run of 18 or more digits can carry an identity number (18) or a mobile
//     number embedded in a longer run (the DFA's word-boundary check decides);
//   - 17 digits followed by X/x is the identity number's check character;
//   - exactly 32 hex bytes followed by '.' is Zhipu's prefixless key.
//
// False positives (an order number, a hash) only cost a DFA pass. The hex shape
// is only checked when a '.' is found, so the hot loop stays one class lookup
// and one predictable branch per byte.
func hasNumericTrigger(src []byte) bool {
	digits := 0
	for i := 0; i < len(src); i++ {
		b := src[i]
		if b >= '0' && b <= '9' {
			digits++
			continue
		}
		if digits >= 11 {
			return true
		}
		if digits == 17 && (b == 'X' || b == 'x') {
			return true
		}
		digits = 0
		if b == '.' && i >= 32 && isLowerHexRun(src[i-32:i]) {
			return true
		}
	}
	return digits >= 11
}

// isLowerHexRun reports whether b is 32 lowercase hex bytes, the Zhipu prefix.
func isLowerHexRun(b []byte) bool {
	for _, c := range b {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
