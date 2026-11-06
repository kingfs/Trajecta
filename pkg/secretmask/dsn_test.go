package secretmask

import (
	"bytes"
	"strings"
	"testing"
)

// dsnPassword is a synthetic 32-byte password. It must never be a real one.
const dsnPassword = "q7Lm2xR8hT4vW9pZa3Bc5De6Fg7Hj8K"

// assertOnlyPasswordChanged checks the DSN policy: the password span is
// replaced and every other byte of the body survives.
func assertOnlyPasswordChanged(t *testing.T, body string, passwordStart, passwordEnd int, masked []byte, events []MaskEvent) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one", events)
	}
	if events[0].Rule != RuleDSNPassword {
		t.Fatalf("event rule = %s, want dsn.password", events[0].RuleName())
	}
	if events[0].Start != passwordStart || events[0].End != passwordEnd {
		t.Fatalf("event span = [%d,%d), want [%d,%d)", events[0].Start, events[0].End, passwordStart, passwordEnd)
	}
	if got := string(events[0].Original); got != body[passwordStart:passwordEnd] {
		t.Fatalf("masked value = %q, want %q", got, body[passwordStart:passwordEnd])
	}
	assertMaskedOutsideSpans(t, []byte(body), masked, events)
}

// TestDSNPasswordMasksOnlyUserinfo is the core of the format rule: a DSN pasted
// into prose has no variable name, and only its password is a credential.
func TestDSNPasswordMasksOnlyUserinfo(t *testing.T) {
	body := "connect with postgres://app_user:" + dsnPassword + "@postgres:5432/appdb?sslmode=disable now"
	start := strings.Index(body, dsnPassword)
	if start < 0 {
		t.Fatal("fixture does not carry the password")
	}
	v := testVault()
	masked, events := v.Mask([]byte(body))
	assertOnlyPasswordChanged(t, body, start, start+len(dsnPassword), masked, events)
	for _, keep := range []string{"postgres://app_user:", "@postgres:5432/appdb?sslmode=disable"} {
		if !bytes.Contains(masked, []byte(keep)) {
			t.Errorf("%q was rewritten: %q", keep, masked)
		}
	}
	if bytes.Contains(masked, []byte(dsnPassword)) {
		t.Error("the DSN password survived masking")
	}
	// The stand-in is a function of the value, so masking twice is masking once
	// and the client-side restore returns the original DSN.
	again, second := v.Mask(masked)
	if len(second) != 0 || !bytes.Equal(again, masked) {
		t.Fatalf("second pass produced %d events", len(second))
	}
	if restored := v.Restore(masked); !bytes.Equal(restored, []byte(body)) {
		t.Error("restore did not round-trip the masked body")
	}
}

// TestDSNSchemesTable covers every accepted scheme and pins the password-only
// span for each of them.
func TestDSNSchemesTable(t *testing.T) {
	user := "app_user"
	cases := []string{
		"postgres://" + user + ":" + dsnPassword + "@db:5432/appdb",
		"postgresql://" + user + ":" + dsnPassword + "@db:5432/appdb",
		"pgsql://" + user + ":" + dsnPassword + "@db:5432/appdb",
		"cockroachdb://" + user + ":" + dsnPassword + "@db:26257/appdb",
		"redis://" + user + ":" + dsnPassword + "@cache:6379/0",
		"rediss://:" + dsnPassword + "@cache:6379/0",
		"mysql://" + user + ":" + dsnPassword + "@db:3306/appdb",
		"mariadb://" + user + ":" + dsnPassword + "@db:3306/appdb",
		"mongodb://" + user + ":" + dsnPassword + "@mongo:27017/appdb",
		"mongodb+srv://" + user + ":" + dsnPassword + "@cluster.example.com/appdb",
		"amqp://" + user + ":" + dsnPassword + "@broker:5672/vhost",
		"amqps://" + user + ":" + dsnPassword + "@broker:5671/vhost",
		"mssql://" + user + ":" + dsnPassword + "@sql:1433/appdb",
		"sqlserver://" + user + ":" + dsnPassword + "@sql:1433/appdb",
		"clickhouse://" + user + ":" + dsnPassword + "@ch:9000/appdb",
	}
	for _, dsn := range cases {
		t.Run(strings.Split(dsn, "://")[0], func(t *testing.T) {
			body := "value: " + dsn + " (from the config)"
			fs := Scan([]byte(body))
			if len(fs) != 1 || fs[0].ID != RuleDSNPassword {
				t.Fatalf("findings = %+v, want one dsn.password", fs)
			}
			if got := string(fs[0].Bytes([]byte(body))); got != dsnPassword {
				t.Fatalf("masked span = %q, want only the password", got)
			}
			if !fs[0].Maskable() {
				t.Fatal("dsn.password finding is not maskable")
			}
			masked, events := testVault().Mask([]byte(body))
			if events[0].Rule != RuleDSNPassword {
				t.Fatalf("event rule = %s", events[0].RuleName())
			}
			assertMaskedOutsideSpans(t, []byte(body), masked, events)
			if bytes.Contains(masked, []byte(dsnPassword)) {
				t.Error("the password survived masking")
			}
			// The rest of the DSN is the address, not the credential.
			if !bytes.Contains(masked, []byte(strings.Split(dsn, ":"+dsnPassword)[0])) {
				t.Errorf("the DSN user/scheme was rewritten: %q", masked)
			}
		})
	}
}

// TestDSNSchemeIsCaseInsensitive: URL schemes are case-insensitive per RFC 3986.
func TestDSNSchemeIsCaseInsensitive(t *testing.T) {
	body := "PostgreSQL://app_user:" + dsnPassword + "@db:5432/appdb"
	masked, events := testVault().Mask([]byte(body))
	if len(events) != 1 || events[0].Rule != RuleDSNPassword {
		t.Fatalf("events = %+v, want one dsn.password", events)
	}
	assertMaskedOutsideSpans(t, []byte(body), masked, events)
}

// TestDSNEscapedJSONToolResult is the recorded-body shape: the DSN arrives in
// prose inside a tool result string that is itself a JSON document.
func TestDSNEscapedJSONToolResult(t *testing.T) {
	body := escapedToolOutput(t, 2, "connect postgres://app_user:"+dsnPassword+"@postgres:5432/appdb now")
	fs := Scan(body)
	if len(fs) != 1 || fs[0].ID != RuleDSNPassword {
		t.Fatalf("findings = %+v, want one dsn.password", fs)
	}
	v := testVault()
	masked, events := v.Mask(body)
	if len(events) != 1 || events[0].Rule != RuleDSNPassword {
		t.Fatalf("events = %+v, want one dsn.password", events)
	}
	assertMaskedOutsideSpans(t, body, masked, events)
	if bytes.Contains(masked, []byte(dsnPassword)) {
		t.Error("the escaped DSN password survived masking")
	}
	if restored := v.Restore(masked); !bytes.Equal(restored, body) {
		t.Error("restore did not recover the escaped DSN")
	}
}

// TestDSNPrefersTheNameAnchoredRuleWhenAssigned: a DSN that is the value of an
// uppercase URL-class assignment is reported as env.url_credential, because the
// name is the stronger evidence; the same password must not be reported twice.
func TestDSNPrefersTheNameAnchoredRuleWhenAssigned(t *testing.T) {
	body := "DATABASE_DSN=postgres://app_user:" + dsnPassword + "@postgres:5432/appdb"
	fs := Scan([]byte(body))
	if len(fs) != 1 || fs[0].ID != RuleEnvURLCredential {
		t.Fatalf("findings = %+v, want one env.url_credential", fs)
	}
}

// TestDSNPasswordSubDelims pins the RFC 3986 userinfo alphabet: the sub-delims
// are legal unencoded in a password, so they must not truncate the authority
// (the earlier terminator set treated them as document punctuation and returned
// no finding at all for these payloads).
func TestDSNPasswordSubDelims(t *testing.T) {
	for _, password := range []string{"p&x", "p,x", "p;x", "p)x", "p=x", "p(x", "p!x", "p$x", "p*x", "p+x", "p:x", "p'x"} {
		t.Run(password, func(t *testing.T) {
			body := "postgres://app_user:" + password + "@db:5432/appdb"
			fs := Scan([]byte(body))
			if len(fs) != 1 || fs[0].ID != RuleDSNPassword {
				t.Fatalf("%q findings = %+v, want one dsn.password", password, fs)
			}
			if got := string(fs[0].Bytes([]byte(body))); got != password {
				t.Fatalf("masked span = %q, want the whole password %q", got, password)
			}
			masked, events := testVault().Mask([]byte(body))
			if len(events) != 1 || bytes.Contains(masked, []byte(password)) {
				t.Fatalf("%q: events = %+v, masked = %q", password, events, masked)
			}
		})
	}
}

// TestDSNUnencodedAtSplitsUserinfo pins the documented boundary: RFC 3986
// requires '@' in a password to be percent-encoded; an unencoded one is the
// userinfo delimiter, so only the part before it is masked. Taking the last '@'
// instead would run across a second URL on the same line and mask unrelated
// bytes.
func TestDSNUnencodedAtSplitsUserinfo(t *testing.T) {
	body := "postgres://app_user:paa@ss@db:5432/appdb"
	fs := Scan([]byte(body))
	if len(fs) != 1 || fs[0].ID != RuleDSNPassword {
		t.Fatalf("findings = %+v, want one dsn.password", fs)
	}
	if got := string(fs[0].Bytes([]byte(body))); got != "paa" {
		t.Fatalf("masked span = %q, want the part before the first '@' (documented boundary)", got)
	}
}

// TestDSNTwoDSNsOnOneLine guards the skip-ahead end: after a DSN is found the
// walk resumes right after '@', so the ':' of a second `scheme://` later on the
// same line is still seen. Skipping the whole authority hid it before.
func TestDSNTwoDSNsOnOneLine(t *testing.T) {
	body := "a=postgres://u1:pA1@h1/db,b=redis://:pB22@h2:6379/0"
	fs := Scan([]byte(body))
	if len(fs) != 2 {
		t.Fatalf("findings = %+v, want two dsn.password findings", fs)
	}
	for _, f := range fs {
		if f.ID != RuleDSNPassword {
			t.Errorf("finding rule = %s, want dsn.password", ruleTable[f.ID].Name)
		}
	}
	masked, events := testVault().Mask([]byte(body))
	if len(events) != 2 {
		t.Fatalf("events = %+v, want 2", events)
	}
	assertMaskedOutsideSpans(t, []byte(body), masked, events)
}

// TestDSNSchemeDriverSuffix covers SQLAlchemy-style `postgresql+psycopg://`
// scheme spellings: the base scheme names the same service.
func TestDSNSchemeDriverSuffix(t *testing.T) {
	for _, dsn := range []string{
		"postgresql+psycopg://app_user:" + dsnPassword + "@db:5432/appdb",
		"postgresql+asyncpg://app_user:" + dsnPassword + "@db:5432/appdb",
		"mysql+pymysql://app_user:" + dsnPassword + "@db:3306/appdb",
		"mongodb+motor://app_user:" + dsnPassword + "@mongo:27017/appdb",
	} {
		fs := Scan([]byte(dsn))
		if len(fs) != 1 || fs[0].ID != RuleDSNPassword {
			t.Fatalf("%q findings = %+v, want one dsn.password", dsn, fs)
		}
		if got := string(fs[0].Bytes([]byte(dsn))); got != dsnPassword {
			t.Fatalf("%q masked span = %q, want only the password", dsn, got)
		}
	}
}

// TestDSNNeverMasks covers the shapes that must stay untouched.
func TestDSNNeverMasks(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no userinfo", "postgres://db:5432/appdb"},
		{"user only", "postgres://app_user@db:5432/appdb"},
		{"empty password", "postgres://app_user:@db:5432/appdb"},
		{"placeholder password", "postgres://app_user:password@db:5432/appdb"},
		{"short placeholder password", "postgres://app_user:pass@db:5432/appdb"},
		{"template password", "postgres://app_user:${POSTGRES_PASSWORD}@db:5432/appdb"},
		{"printf password", "postgres://app_user:%s@db:5432/appdb"},
		{"short password", "postgres://app_user:ab@db:5432/appdb"},
		{"non-dsn scheme https", "https://app_user:" + dsnPassword + "@example.com/path"},
		{"non-dsn scheme ftp", "ftp://app_user:" + dsnPassword + "@example.com/path"},
		{"scheme-like word", "my_postgres://app_user:" + dsnPassword + "@db:5432/appdb"},
		{"host port colon", "postgres://db:5432/appdb"},
		{"mime type", "application/vnd.api+json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if fs := Scan([]byte(tc.body)); len(fs) != 0 {
				var got []string
				for _, f := range fs {
					got = append(got, ruleTable[f.ID].Name+"="+string(f.Bytes([]byte(tc.body))))
				}
				t.Fatalf("false positive on %q: %v", tc.body, got)
			}
			masked, events := testVault().Mask([]byte(tc.body))
			if len(events) != 0 || !bytes.Equal(masked, []byte(tc.body)) {
				t.Fatalf("body was rewritten: %+v", events)
			}
		})
	}
}

// TestDSNInCodeIsStillMasked: a DSN literal in a source fragment is the one
// code shape that stays a candidate, because the password is the value itself.
func TestDSNInCodeIsStillMasked(t *testing.T) {
	body := `const dsn = "postgres://app_user:` + dsnPassword + `@postgres:5432/appdb"`
	fs := Scan([]byte(body))
	if len(fs) != 1 || fs[0].ID != RuleDSNPassword {
		t.Fatalf("findings = %+v, want one dsn.password", fs)
	}
	masked, events := testVault().Mask([]byte(body))
	if len(events) != 1 || bytes.Contains(masked, []byte(dsnPassword)) {
		t.Fatalf("events = %+v", events)
	}
	assertMaskedOutsideSpans(t, []byte(body), masked, events)
}

// TestDSNMultipleAndDelimiters: two DSNs in one body each report once, and the
// authority stops at the punctuation a JSON value, markdown or a shell line
// uses.
func TestDSNMultipleAndDelimiters(t *testing.T) {
	body := "a: postgres://u1:" + dsnPassword + "@h1/db, b: redis://:r2d1sP4ss@h2:6379/0 " +
		"and `mysql://u3:aQ7vLm2xR8hT4wY6pZ9cB3dF5gH1jK@h3/db`"
	fs := Scan([]byte(body))
	if len(fs) != 3 {
		t.Fatalf("findings = %+v, want 3", fs)
	}
	for _, f := range fs {
		if f.ID != RuleDSNPassword {
			t.Errorf("finding rule = %s, want dsn.password", ruleTable[f.ID].Name)
		}
	}
	masked, events := testVault().Mask([]byte(body))
	if len(events) != 3 {
		t.Fatalf("events = %+v, want 3", events)
	}
	assertMaskedOutsideSpans(t, []byte(body), masked, events)
	if !bytes.Contains(masked, []byte("a: postgres://u1:")) || !bytes.Contains(masked, []byte("@h1/db, b: redis://:")) {
		t.Errorf("masking changed the surroundings: %q", masked)
	}
}

// TestDSNNumericPassword: a numeric DSN password can be real, unlike a numeric
// whole-value assignment, so the password path must not apply the JSON-literal
// skip.
func TestDSNNumericPassword(t *testing.T) {
	body := "redis://:" + "987654321" + "@cache:6379/0"
	fs := Scan([]byte(body))
	if len(fs) != 1 || fs[0].ID != RuleDSNPassword {
		t.Fatalf("findings = %+v, want one dsn.password", fs)
	}
}
