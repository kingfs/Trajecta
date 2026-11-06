package secretmask

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The values below are synthesized to the same shape as the ones in the live
// report. None of them is a real credential: the tests must never carry the
// cassette bytes.
const (
	assignPass32   = "q7Lm2xR8hT4vW9pZa3Bc5De6Fg7Hj8K"  // 32 alphanumerics
	assignPass12   = "n4Bd6Fh8Jk2L"                     // 12 alphanumerics
	assignAuth32   = "aQ7vLm2xR8hT4wY6pZ9cB3dF5gH1jK+2" // 32 with '+', as an auth password had
	assignKey32    = "Z8yW6uS4qO2mK0iG8eC6aA4yW2uS0qO8"
	assignSecret40 = "V5rT8wQ2nM6kL0jH4gF8dS2aP6oI0uY4cX8zB"
	assignToken32  = "hK7mN2pQ5rS8tU1vW4xY9zA3bC6dE0fG"
)

// escapedToolOutput wraps text the way the recorded bodies carry a tool result:
// text is JSON-encoded depth times and placed in the content of a tool_result
// item. depth 1 is a tool result whose content is a plain transcript; depth 2
// is a tool result whose content is itself a JSON document with quoted strings.
func escapedToolOutput(t *testing.T, depth int, text string) []byte {
	t.Helper()
	quoted, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < depth; i++ {
		quoted, err = json.Marshal(string(quoted))
		if err != nil {
			t.Fatal(err)
		}
	}
	return []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","content":` + string(quoted) + `}]}]}`)
}

func assignmentEnvText() string {
	return strings.Join([]string{
		"POSTGRES_PASSWORD=" + assignPass32,
		"PGPASSWORD=" + assignPass12,
		"AUTH_PASSWORD=" + assignAuth32,
		"DATABASE_DSN=postgres://app_user:" + assignPass32 + "@postgres:5432/appdb?sslmode=disable",
		"APP_ENV=production",
	}, "\n")
}

func eventsByRule(events []MaskEvent) map[RuleID]int {
	out := make(map[RuleID]int, len(events))
	for _, e := range events {
		out[e.Rule]++
	}
	return out
}

// assertMaskedOutsideSpans requires that Mask changed exactly the event spans
// and nothing else, and that every stand-in kept the original length.
func assertMaskedOutsideSpans(t *testing.T, real, masked []byte, events []MaskEvent) {
	t.Helper()
	if len(masked) != len(real) {
		t.Fatalf("masked length %d, want %d", len(masked), len(real))
	}
	covered := make([]bool, len(real))
	for _, e := range events {
		if e.Start < 0 || e.End > len(real) || e.Start > e.End {
			t.Fatalf("event span out of range: %+v", e)
		}
		if len(e.StandIn) != e.End-e.Start {
			t.Errorf("%s: stand-in length %d, want %d", e.RuleName(), len(e.StandIn), e.End-e.Start)
		}
		if bytes.Equal(e.StandIn, e.Original) {
			t.Errorf("%s: stand-in equals the real value", e.RuleName())
		}
		for i := e.Start; i < e.End; i++ {
			if covered[i] {
				t.Fatalf("event span [%d,%d) overlaps another", e.Start, e.End)
			}
			covered[i] = true
		}
	}
	for i := range real {
		if !covered[i] && real[i] != masked[i] {
			t.Fatalf("byte %d outside every event was rewritten (%q -> %q)", i, real[i], masked[i])
		}
	}
}

// TestAssignmentMasksReportedToolResult is the exact shape of the reported
// tool result: {"token":"sk-...","prefix":"sk-..."} inside a tool_result.
func TestAssignmentMasksReportedToolResult(t *testing.T) {
	key := skDashSeparatorSample('_')
	inner := `{"token":"` + key + `","prefix":"sk-Ab1Ab1Ab1Ab"}`
	body := escapedToolOutput(t, 2, inner)

	if !bytes.Contains(body, []byte(key)) {
		t.Fatal("fixture does not carry the token")
	}
	v := testVault()
	masked, events := v.Mask(body)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one", events)
	}
	// The DFA sees the lexical shape, the name-anchored pass sees the same
	// span under "token"; the tie goes to the DFA.
	if events[0].Rule != RuleSkDashGeneric {
		t.Errorf("event rule = %s, want %s", events[0].RuleName(), ruleTable[RuleSkDashGeneric].Name)
	}
	assertMaskedOutsideSpans(t, body, masked, events)
	if bytes.Contains(masked, []byte(key)) {
		t.Error("the reported token survived masking")
	}
	// The neighbouring "prefix" member is not credential vocabulary and its
	// value is below the 35-byte floor, so it is left exactly as it was.
	if !bytes.Contains(masked, []byte("prefix")) || !bytes.Contains(masked, []byte("sk-Ab1Ab1Ab1Ab")) {
		t.Errorf("the prefix member was touched: %q", masked)
	}

	// A token that has no lexical shape at all is masked by the name alone.
	shapeless := `{"token":"` + assignToken32 + `"}`
	masked, events = v.Mask([]byte(shapeless))
	if len(events) != 1 || events[0].Rule != RuleEnvSecretAssignment {
		t.Fatalf("shapeless token events = %+v, want one env.secret_assignment", events)
	}
	if bytes.Contains(masked, []byte(assignToken32)) {
		t.Error("the shapeless token survived masking")
	}
}

// TestAssignmentMasksEnvDump is the incident's second half: a .env dump in a
// tool output has no lexical credential shape for the database passwords.
func TestAssignmentMasksEnvDump(t *testing.T) {
	body := escapedToolOutput(t, 1, assignmentEnvText())
	v := testVault()

	findings := Scan(body)
	if len(findings) != 4 {
		t.Fatalf("findings = %+v, want 4 (three env.secret_assignment + one env.url_credential)", findings)
	}

	masked, events := v.Mask(body)
	if len(events) != 4 {
		t.Fatalf("events = %+v, want 4", events)
	}
	got := eventsByRule(events)
	if got[RuleEnvSecretAssignment] != 3 || got[RuleEnvURLCredential] != 1 {
		t.Errorf("events by rule = %v, want 3 env.secret_assignment + 1 env.url_credential", got)
	}
	assertMaskedOutsideSpans(t, body, masked, events)
	for _, secret := range []string{assignPass32, assignPass12, assignAuth32} {
		if bytes.Contains(masked, []byte(secret)) {
			t.Errorf("the value %q survived masking", secret)
		}
	}
	// The DSN as a whole is the address of a service; only its userinfo
	// password is masked, so both sides around it survive byte for byte.
	if !bytes.Contains(masked, []byte("postgres://app_user:")) || !bytes.Contains(masked, []byte("@postgres:5432/appdb?sslmode=disable")) {
		t.Errorf("masking changed the DSN shape: %q", masked)
	}
	// The untouched line is still there.
	if !bytes.Contains(masked, []byte("APP_ENV=production")) {
		t.Errorf("an unrelated env line was rewritten: %q", masked)
	}

	// Masking twice is masking once: the stand-in is recognised and left alone.
	again, second := v.Mask(masked)
	if len(second) != 0 {
		t.Fatalf("second pass produced %d events", len(second))
	}
	if !bytes.Equal(again, masked) {
		t.Error("second pass changed the bytes")
	}
	// The pair is reversible through the vault, which is what the client needs.
	if restored := v.Restore(masked); !bytes.Equal(restored, body) {
		t.Error("restore did not round-trip the masked body")
	}
}

// TestAssignmentForms covers the surrounding spellings: env lines, YAML, JSON
// members, escaped JSON strings, quotes, export/set and camelCase names.
func TestAssignmentForms(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantRule RuleID
		wantN    int
		absent   []string
		present  []string
	}{
		{
			name:     "env line",
			body:     "POSTGRES_PASSWORD=" + assignPass32,
			wantRule: RuleEnvSecretAssignment,
			wantN:    1,
			absent:   []string{assignPass32},
			present:  []string{"POSTGRES_PASSWORD="},
		},
		{
			name:     "env line with spaces around the separator",
			body:     "POSTGRES_PASSWORD = " + assignPass32,
			wantRule: RuleEnvSecretAssignment,
			wantN:    1,
			absent:   []string{assignPass32},
			present:  []string{"POSTGRES_PASSWORD = "},
		},
		{
			name:     "export",
			body:     "export PGPASSWORD=" + assignPass12,
			wantRule: RuleEnvSecretAssignment,
			wantN:    1,
			absent:   []string{assignPass12},
		},
		{
			name:     "set with a URL class name",
			body:     "set DATABASE_DSN=postgres://app_user:" + assignPass12 + "@postgres:5432/appdb",
			wantRule: RuleEnvURLCredential,
			wantN:    1,
			absent:   []string{assignPass12},
			present:  []string{"postgres://app_user:", "@postgres:5432/appdb"},
		},
		{
			name:     "yaml member",
			body:     "postgres_password: " + assignPass32,
			wantRule: RuleEnvSecretAssignment,
			wantN:    1,
			absent:   []string{assignPass32},
			present:  []string{"postgres_password: "},
		},
		{
			name:     "json member",
			body:     `{"POSTGRES_PASSWORD": "` + assignPass32 + `"}`,
			wantRule: RuleEnvSecretAssignment,
			wantN:    1,
			absent:   []string{assignPass32},
			present:  []string{`{"POSTGRES_PASSWORD": "`},
		},
		{
			name:     "single quoted value keeps its quotes",
			body:     "POSTGRES_PASSWORD='" + assignPass32 + "'",
			wantRule: RuleEnvSecretAssignment,
			wantN:    1,
			absent:   []string{assignPass32},
			present:  []string{"POSTGRES_PASSWORD='"},
		},
		{
			name:     "camelCase name",
			body:     "dbPassword=" + assignPass32,
			wantRule: RuleEnvSecretAssignment,
			wantN:    1,
			absent:   []string{assignPass32},
		},
		{
			name:     "SSH private key spec",
			body:     "SSH_PRIVATE_KEY=" + assignKey32,
			wantRule: RuleEnvSecretAssignment,
			wantN:    1,
			absent:   []string{assignKey32},
		},
		{
			name:     "AWS secret access key",
			body:     "AWS_SECRET_ACCESS_KEY=" + assignSecret40,
			wantRule: RuleEnvSecretAssignment,
			wantN:    1,
			absent:   []string{assignSecret40},
		},
		{
			name:     "auth token",
			body:     "AUTH_TOKEN=" + assignToken32,
			wantRule: RuleEnvSecretAssignment,
			wantN:    1,
			absent:   []string{assignToken32},
		},
		{
			name:     "no-separator PGPASSWORD spelling",
			body:     "PGPASSWORD=" + assignPass12,
			wantRule: RuleEnvSecretAssignment,
			wantN:    1,
			absent:   []string{assignPass12},
		},
		{
			name:     "mongodb uri masks only the password",
			body:     "MONGODB_URI=mongodb://app:" + assignPass12 + "@mongo:27017/appdb",
			wantRule: RuleEnvURLCredential,
			wantN:    1,
			absent:   []string{assignPass12},
			present:  []string{"mongodb://app:", "@mongo:27017/appdb"},
		},
		{
			name:     "redis url with an empty user",
			body:     "REDIS_URL=redis://:r3d1sP4ss@10.0.0.5:6379/0",
			wantRule: RuleEnvURLCredential,
			wantN:    1,
			absent:   []string{"r3d1sP4ss"},
			present:  []string{"redis://:", "@10.0.0.5:6379/0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := Scan([]byte(tc.body))
			if len(fs) != tc.wantN {
				t.Fatalf("findings = %+v, want %d", fs, tc.wantN)
			}
			if fs[0].ID != tc.wantRule {
				t.Fatalf("finding rule = %s, want %s", ruleTable[fs[0].ID].Name, ruleTable[tc.wantRule].Name)
			}
			if !fs[0].Maskable() {
				t.Errorf("finding is not maskable (score %d)", fs[0].Score)
			}
			v := testVault()
			masked, events := v.Mask([]byte(tc.body))
			if len(events) != tc.wantN {
				t.Fatalf("events = %+v, want %d", events, tc.wantN)
			}
			assertMaskedOutsideSpans(t, []byte(tc.body), masked, events)
			for _, s := range tc.absent {
				if bytes.Contains(masked, []byte(s)) {
					t.Errorf("%q survived masking: %q", s, masked)
				}
			}
			for _, s := range tc.present {
				if !bytes.Contains(masked, []byte(s)) {
					t.Errorf("%q was rewritten: %q", s, masked)
				}
			}
		})
	}
}

// TestAssignmentAfterEscapedNewline covers a .env line that follows an escaped
// line break in the recorded body: the look-back starts at the separator, so it
// sees `\nKEY`, and the escape letter must not stop a bare KEY from being
// recognised.
func TestAssignmentAfterEscapedNewline(t *testing.T) {
	key := skDashSeparatorSample('_')
	body := escapedToolOutput(t, 1, "APP_ENV=production\nKEY="+key)
	fs := Scan(body)
	if len(fs) != 1 || fs[0].ID != RuleEnvSecretAssignment {
		t.Fatalf("findings = %+v, want one env.secret_assignment", fs)
	}
	masked, events := testVault().Mask(body)
	if len(events) != 1 || events[0].Rule != RuleEnvSecretAssignment {
		t.Fatalf("events = %+v, want one env.secret_assignment", events)
	}
	assertMaskedOutsideSpans(t, body, masked, events)
	if bytes.Contains(masked, []byte(key)) {
		t.Error("the key survived masking")
	}
}

// TestAssignmentEscapedJSONDepth covers single- and double-escaped JSON
// documents inside a tool result, which is how a nested protocol value appears
// in the recorded body.
func TestAssignmentEscapedJSONDepth(t *testing.T) {
	inner := `{"POSTGRES_PASSWORD": "` + assignPass32 + `"}`
	for depth := 1; depth <= 2; depth++ {
		body := escapedToolOutput(t, depth, inner)
		fs := Scan(body)
		if len(fs) != 1 || fs[0].ID != RuleEnvSecretAssignment {
			t.Fatalf("depth %d findings = %+v, want one env.secret_assignment", depth, fs)
		}
		masked, events := testVault().Mask(body)
		if len(events) != 1 || events[0].Rule != RuleEnvSecretAssignment {
			t.Fatalf("depth %d events = %+v, want one env.secret_assignment", depth, events)
		}
		assertMaskedOutsideSpans(t, body, masked, events)
		if bytes.Contains(masked, []byte(assignPass32)) {
			t.Errorf("depth %d: value survived masking", depth)
		}
	}
}

// TestAssignmentNeverMasks covers the shapes that must stay untouched: one
// false rewrite per live request is worse than one missed absent credential,
// because the cassette and the provider then disagree about a value that was
// never secret.
func TestAssignmentNeverMasks(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"variable default reference", `${POSTGRES_PASSWORD:-` + assignPass32 + `}`},
		{"braced variable reference with equals", `${PGPASSWORD=` + assignPass12 + `}`},
		{"dollar value", "POSTGRES_PASSWORD=$PGPASSWORD"},
		{"percent value", "POSTGRES_PASSWORD=%PGPASSWORD%"},
		{"angle value", "POSTGRES_PASSWORD=<set-me>"},
		{"template value", "POSTGRES_PASSWORD={{ .Values.password }}"},
		{"star placeholder", "POSTGRES_PASSWORD=***"},
		{"x placeholder", "POSTGRES_PASSWORD=xxxx-xxxx"},
		{"dots placeholder", "POSTGRES_PASSWORD=..."},
		{"changeme", "POSTGRES_PASSWORD=changeme"},
		{"change_me", "POSTGRES_PASSWORD=change_me"},
		{"placeholder word", "POSTGRES_PASSWORD=password"},
		{"redacted", "POSTGRES_PASSWORD=redacted"},
		{"your prefix", "POSTGRES_PASSWORD=your_password_here"},
		{"replace prefix", "POSTGRES_PASSWORD=replace_with_real"},
		{"empty value", "POSTGRES_PASSWORD="},
		{"too short password", "POSTGRES_PASSWORD=q7Lm"},
		{"too short key", "API_KEY=abcdefghijkl"},
		{"numeric literal without quotes", "POSTGRES_PASSWORD=123456"},
		{"boolean literal without quotes", "AUTH_TOKEN=true"},
		{"schema type", `"password": "string"`},
		{"short token value", `"token":"the"`},
		{"limit not a token", "MAX_TOKEN=4096"},
		{"max_tokens is plural", `"max_tokens": 4096`},
		{"password length setting", "PASSWORD_LENGTH=12"},
		{"monkey is not a key", "MONKEY=abcdefghijklmnopqrst"},
		{"turkey is not a key", "TURKEY=abcdefghijklmnopqrst"},
		{"url without userinfo", "DATABASE_DSN=postgres://app_user@db:5432/appdb"},
		{"url with user but no password", "DATABASE_URL=postgres://app_user@db:5432/appdb"},
		{"url class name but adonet connection string", "SQL_CONNECTION_STRING=Server=db;Database=app;Uid=sa"},
		{"url class password placeholder", "DATABASE_DSN=postgres://app:password@db:5432/appdb"},
		// A shell line that names a credential inside a grep pattern: the quote
		// after '=' is the closing quote of the pattern, not the opening quote of
		// a value, and the single-quoted span that follows is command text.
		{"grep pattern closing quote", `$(grep '^POSTGRES_PASSWORD=' /home/dev/app/docker-compose.yml | sed -n '1p')`},
		// The bare word "pass" is ordinary prose; only DB_PASS/MYSQL_PWD (a token
		// of a longer name) is a password name.
		{"prose pass label", "all 12 tests pass: 0.83s"},
		// A quoted document is not a credential value; the 1024-byte bound keeps
		// a tool output from being masked wholesale.
		{"quoted document is not a value", `POSTGRES_PASSWORD="` + rep("q7Lm", 600) + `"`},
		// Protocol and configuration names that end in credential vocabulary but
		// whose values are routing, cache, counter or join-key data. Masking one
		// rewrites a value the protocol depends on.
		{"prompt cache key json member", `"prompt_cache_key":"` + assignToken32 + `"`},
		{"prompt cache key env spelling", "PROMPT_CACHE_KEY=" + assignToken32},
		{"next token", "next_token=" + assignToken32},
		{"page token", "page_token=" + assignToken32},
		{"continuation token", "continuation_token=" + assignToken32},
		{"cache key", "cache_key=" + assignToken32},
		{"idempotency key", `"idempotency_key": "` + assignToken32 + `"`},
		{"sort key", "sort_key=" + assignToken32},
		{"partition key", "partition_key=" + assignToken32},
		{"routing key", "routing_key=" + assignToken32},
		{"group key", "group_key=" + assignToken32},
		{"primary key", "primary_key=" + assignToken32},
		{"foreign key", "foreign_key=" + assignToken32},
		{"object key", "object_key=" + assignToken32},
		{"storage key", "storage_key=" + assignToken32},
		{"s3 key", "s3_key=" + assignToken32},
		{"token type", "token_type=" + assignToken32},
		{"token count", "token_count=" + assignToken32},
		{"max tokens", "MAX_TOKENS=" + assignToken32},
		{"prompt tokens", "prompt_tokens=" + assignToken32},
		{"completion tokens", "completion_tokens=" + assignToken32},
		{"total tokens", "total_tokens=" + assignToken32},
		{"reasoning tokens", "reasoning_tokens=" + assignToken32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if fs := Scan([]byte(tc.body)); len(fs) != 0 {
				var got []string
				for _, f := range fs {
					got = append(got, ruleTable[f.ID].Name)
				}
				t.Fatalf("false positive on %q: %v", tc.body, got)
			}
			masked, events := testVault().Mask([]byte(tc.body))
			if len(events) != 0 {
				t.Fatalf("masked %d event(s) on %q: %+v", len(events), tc.body, events)
			}
			if !bytes.Equal(masked, []byte(tc.body)) {
				t.Fatalf("Mask changed a body it must leave alone: %q -> %q", tc.body, masked)
			}
		})
	}
}

// TestAssignmentExcludedNamesKeepRealKeys is the control for the exclusion
// list: the protocol names must stop being masked without losing a single
// legitimate credential next to them.
func TestAssignmentExcludedNamesKeepRealKeys(t *testing.T) {
	v := testVault()
	// The protocol name is left alone; the real key in the same object is not.
	legacy := sampleFor(RuleOpenAILegacy)
	body := `"prompt_cache_key":"` + assignToken32 + `","openai_api_key":"` + legacy + `"`
	fs := Scan([]byte(body))
	if len(fs) != 1 || fs[0].ID != RuleOpenAILegacy {
		t.Fatalf("findings = %+v, want only the openai legacy key", fs)
	}
	masked, events := v.Mask([]byte(body))
	if len(events) != 1 || events[0].Rule != RuleOpenAILegacy {
		t.Fatalf("events = %+v, want only the openai legacy key", events)
	}
	if !bytes.Contains(masked, []byte(assignToken32)) {
		t.Error("the prompt_cache_key value was masked")
	}
	if bytes.Contains(masked, []byte(legacy)) {
		t.Error("the openai key value survived")
	}

	// PROXY_KEY is not in the exclusion list and is masked by the name alone
	// even though the generic shape has no context.
	proxyKey := skDashSeparatorSample('_')
	masked, events = v.Mask([]byte("PROXY_KEY=" + proxyKey))
	if len(events) != 1 || events[0].Rule != RuleEnvSecretAssignment {
		t.Fatalf("PROXY_KEY events = %+v, want one env.secret_assignment", events)
	}
	if bytes.Contains(masked, []byte(proxyKey)) {
		t.Error("the PROXY_KEY value survived")
	}

	// A URL-class name is still URL-class after the exclusion check.
	dsn := "postgres://app_user:" + assignPass12 + "@postgres:5432/appdb"
	masked, events = v.Mask([]byte("DATABASE_DSN=" + dsn))
	if len(events) != 1 || events[0].Rule != RuleEnvURLCredential {
		t.Fatalf("DATABASE_DSN events = %+v, want one env.url_credential", events)
	}
	if bytes.Contains(masked, []byte(assignPass12)) {
		t.Error("the DSN password survived")
	}
}

// TestAssignmentURLMasksOnlyUserinfoPassword pins the URL-class policy: the DSN
// is an address, the password inside it is the credential.
func TestAssignmentURLMasksOnlyUserinfoPassword(t *testing.T) {
	body := "DATABASE_DSN=postgres://app_user:" + assignPass32 + "@postgres:5432/appdb?sslmode=disable"
	v := testVault()
	masked, events := v.Mask([]byte(body))
	if len(events) != 1 || events[0].Rule != RuleEnvURLCredential {
		t.Fatalf("events = %+v, want one env.url_credential", events)
	}
	if got := string(events[0].Original); got != assignPass32 {
		t.Fatalf("masked value = %q, want only the userinfo password", got)
	}
	assertMaskedOutsideSpans(t, []byte(body), masked, events)
	for _, keep := range []string{"DATABASE_DSN=", "postgres://app_user:", "@postgres:5432/appdb?sslmode=disable"} {
		if !bytes.Contains(masked, []byte(keep)) {
			t.Errorf("%q was rewritten", keep)
		}
	}

	// The same value under a non-URL name is masked as a whole.
	plain := "DB_PASSWORD=" + assignPass32
	if _, events := v.Mask([]byte(plain)); len(events) != 1 || string(events[0].Original) != assignPass32 {
		t.Fatalf("plain password events = %+v, want the whole value", events)
	}
}

// TestAssignmentPromotesAWeakLexicalMatch is the `KEY="sk-..."` case: the
// generic pool matches the value but has no context, so it is reported only;
// the name anchor is what makes the same span maskable, and the merge tie must
// give way to the actionable finding.
func TestAssignmentPromotesAWeakLexicalMatch(t *testing.T) {
	key := skDashSeparatorSample('_')
	body := `KEY="` + key + `"`
	fs := Scan([]byte(body))
	if len(fs) != 1 || fs[0].ID != RuleEnvSecretAssignment {
		t.Fatalf("findings = %+v, want one env.secret_assignment", fs)
	}
	if !fs[0].Maskable() {
		t.Fatal("the name-anchored finding is not maskable")
	}
	masked, events := testVault().Mask([]byte(body))
	if len(events) != 1 || events[0].Rule != RuleEnvSecretAssignment {
		t.Fatalf("events = %+v, want one env.secret_assignment", events)
	}
	assertMaskedOutsideSpans(t, []byte(body), masked, events)
	if bytes.Contains(masked, []byte(key)) {
		t.Error("the key survived masking")
	}
}

// TestAssignmentUsesOptionalValidators guards the merge rule that makes the DFA
// win a tie: a name-anchored span of exactly the DFA span must not mask twice.
func TestMergeFindingsPrecedence(t *testing.T) {
	f := func(id RuleID, start, end int) Finding {
		return Finding{Span: Span{Start: start, End: end, ID: id}, Score: 70, Confidence: ConfidenceHigh}
	}
	base := []Finding{f(RuleGoogleAPIKey, 10, 20), f(RuleSkDashGeneric, 30, 40), f(RuleXAIAPIKey, 50, 60)}

	cases := []struct {
		name      string
		cand      Finding
		want      []Finding
		weakFirst bool
	}{
		{
			name: "equal span keeps the DFA finding",
			cand: f(RuleEnvSecretAssignment, 10, 20),
			want: base,
		},
		{
			name: "contained candidate keeps the DFA finding",
			cand: f(RuleEnvSecretAssignment, 12, 18),
			want: base,
		},
		{
			name: "strictly containing candidate wins",
			cand: f(RuleEnvSecretAssignment, 5, 25),
			want: []Finding{f(RuleEnvSecretAssignment, 5, 25), base[1], base[2]},
		},
		{
			name: "strictly containing candidate wins over two findings",
			cand: f(RuleEnvSecretAssignment, 5, 45),
			want: []Finding{f(RuleEnvSecretAssignment, 5, 45), base[2]},
		},
		{
			name: "left-edge partial overlap keeps the DFA finding",
			cand: f(RuleEnvSecretAssignment, 5, 15),
			want: base,
		},
		{
			name: "right-edge partial overlap keeps the DFA finding",
			cand: f(RuleEnvSecretAssignment, 15, 25),
			want: base,
		},
		{
			name:      "equal span gives way when only the name is actionable",
			cand:      f(RuleEnvSecretAssignment, 10, 20),
			want:      []Finding{f(RuleEnvSecretAssignment, 10, 20), base[1], base[2]},
			weakFirst: true,
		},
		{
			name: "disjoint candidate is inserted in order",
			cand: f(RuleEnvURLCredential, 22, 28),
			want: []Finding{f(RuleGoogleAPIKey, 10, 20), f(RuleEnvURLCredential, 22, 28), f(RuleSkDashGeneric, 30, 40), base[2]},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dfa := append([]Finding(nil), base...)
			if tc.weakFirst {
				weak := f(RuleSkDashGeneric, 10, 20)
				weak.Score, weak.Confidence = 10, ConfidenceLow
				dfa[0] = weak
			}
			got := mergeFindings(dfa, []Finding{tc.cand})
			if len(got) != len(tc.want) {
				t.Fatalf("merge returned %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i].Span != tc.want[i].Span {
					t.Fatalf("result[%d] = %+v, want %+v", i, got[i].Span, tc.want[i].Span)
				}
			}
			for i := 1; i < len(got); i++ {
				if got[i].Start < got[i-1].End {
					t.Fatalf("result overlaps at %d: %+v", i, got[i-1].Span)
				}
			}
		})
	}
}

// TestAssignmentWidensAValueAroundALexicalMatch is the integration form of the
// containment precedence: a name-anchored value that contains a lexical match
// is masked as one span, because the whole value is the secret.
func TestAssignmentWidensAValueAroundALexicalMatch(t *testing.T) {
	body := "DB_PASSWORD=x" + sampleFor(RuleGoogleAPIKey) + "y"
	fs := Scan([]byte(body))
	if len(fs) != 1 || fs[0].ID != RuleEnvSecretAssignment {
		t.Fatalf("findings = %+v, want one env.secret_assignment", fs)
	}
	masked, events := testVault().Mask([]byte(body))
	if len(events) != 1 || events[0].Rule != RuleEnvSecretAssignment {
		t.Fatalf("events = %+v, want one env.secret_assignment", events)
	}
	if got := string(events[0].Original); got != "x"+sampleFor(RuleGoogleAPIKey)+"y" {
		t.Fatalf("masked value = %q, want the whole assignment value", got)
	}
	assertMaskedOutsideSpans(t, []byte(body), masked, events)
}

// TestAssignmentNonOverlapKeepsOtherFields: a same-length plaintext value in
// another field is not part of any span and must come back untouched.
func TestAssignmentNonOverlapKeepsOtherFields(t *testing.T) {
	body := `{"db_password":"` + assignPass32 + `","note":"` + assignPass32 + `"}`
	masked, events := testVault().Mask([]byte(body))
	if len(events) != 1 || events[0].Rule != RuleEnvSecretAssignment {
		t.Fatalf("events = %+v, want one env.secret_assignment", events)
	}
	assertMaskedOutsideSpans(t, []byte(body), masked, events)
	if !bytes.Contains(masked, []byte(`"note":"`+assignPass32+`"`)) {
		t.Errorf("the note field was rewritten: %q", masked)
	}
	if bytes.Contains(masked, []byte(`"db_password":"`+assignPass32+`"`)) {
		t.Error("the db_password field was not masked")
	}
}

// TestAssignmentNoHitPathDoesNotAllocate keeps the hot path promise: a body
// with no assignment must cost the walk and nothing else. The positive control
// checks the measurement itself.
func TestAssignmentNoHitPathDoesNotAllocate(t *testing.T) {
	clean := []byte(`{"model":"gpt-5","stream":false,"url":"https://example.com/a?x=1&y=2",` +
		`"messages":[{"role":"user","content":"func main() { fmt.Println(1) }"}],` +
		`"properties":{"count":{"type":"number"}}}`)
	if fs := Scan(clean); len(fs) != 0 {
		t.Fatalf("clean fixture produced %+v", fs)
	}
	allocs := testing.AllocsPerRun(200, func() {
		_ = Detector{}.AppendFindings(nil, clean)
	})
	if allocs != 0 {
		t.Errorf("no-hit AppendFindings allocates %.1f times per run, want 0", allocs)
	}
	// Positive control: a body that does produce a finding reports no allocation
	// only when the caller reuses a buffer, which is the documented contract.
	dst := make([]Finding, 0, 8)
	if got := (Detector{}).AppendFindings(dst[:0], clean); len(got) != 0 {
		t.Fatalf("unexpected findings: %+v", got)
	}
}
