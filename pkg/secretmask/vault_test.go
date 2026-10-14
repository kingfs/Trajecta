package secretmask

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

func testVault() *Vault { return NewVault([]byte("secretmask-test-seed")) }

// TestFakeKeepsShapeAndLength is the load-bearing property of the whole
// masking path. A fake that changed length would desynchronise the
// Content-Length the proxy copies verbatim; a fake that dropped the prefix
// would stop looking like the vendor's credential to any tool that validates
// on shape.
func TestFakeKeepsShapeAndLength(t *testing.T) {
	v := testVault()
	for _, tt := range positiveSamples {
		t.Run(ruleTable[tt.rule].Name, func(t *testing.T) {
			r := &ruleTable[tt.rule]
			real := []byte(tt.text)
			fake := v.fakeFor(r, real)

			if len(fake) != len(real) {
				t.Fatalf("length %d, want %d: %q -> %q", len(fake), len(real), real, fake)
			}
			if fake == tt.text {
				t.Fatalf("fake is the real value: %q", fake)
			}
			head := protectedHead(r, real)
			if fake[:head] != tt.text[:head] {
				t.Errorf("kept head %q, want %q (%q -> %q)", fake[:head], tt.text[:head], real, fake)
			}
			for i := range real {
				if !isAlnumByte(real[i]) && real[i] != fake[i] {
					t.Errorf("position %d: separator %q became %q (%q -> %q)", i, real[i], fake[i], real, fake)
				}
			}
			for _, lit := range r.Literals {
				if !strings.Contains(fake, lit) {
					t.Errorf("lost literal %q: %q -> %q", lit, real, fake)
				}
			}
			if again := v.fakeFor(r, real); again != fake {
				t.Errorf("not deterministic: %q then %q", fake, again)
			}
		})
	}
}

// TestFakeKeepsSegmentAlphabet checks the per-run alphabet choice: a hex
// segment stays hex even when the value also has a free-form segment.
func TestFakeKeepsSegmentAlphabet(t *testing.T) {
	v := testVault()
	for _, id := range []RuleID{RuleDatabricksPAT, RulePromptLayerAPIKey, RuleLangfuseSecretKey, RuleLangfusePublicKey, RuleOpenRouterAPIKey} {
		r := &ruleTable[id]
		sample := sampleFor(id)
		fake := v.fakeFor(r, []byte(sample))
		head := protectedHead(r, []byte(sample))
		for i := head; i < len(fake); i++ {
			c := fake[i]
			if !isAlnumByte(c) {
				continue
			}
			if c < '0' || c > '9' && (c < 'a' || c > 'f') {
				t.Fatalf("%s: fake %q has non-hex byte %q at position %d (real %q)", r.Name, fake, c, i, sample)
			}
		}
	}
}

// TestFakeKeepsZhipuHexSegment covers a value that mixes a hex segment with a
// free-form one: only the hex segment may be rendered from hex.
func TestFakeKeepsZhipuHexSegment(t *testing.T) {
	v := testVault()
	sample := sampleFor(RuleZhipuAPIKey)
	fake := v.fakeFor(&ruleTable[RuleZhipuAPIKey], []byte(sample))
	first, _, ok := strings.Cut(fake, ".")
	if !ok {
		t.Fatalf("fake lost its separator: %q", fake)
	}
	if !allLowerHex([]byte(first)) || first == strings.Split(sample, ".")[0] {
		t.Fatalf("hex segment %q is not a fresh hex value (real %q)", first, sample)
	}
}

// TestJWTHeaderSurvivesRendering checks the exception to prefix preservation:
// a JWT keeps its header so the algorithm stays readable, and loses its claims
// and signature.
func TestJWTHeaderSurvivesRendering(t *testing.T) {
	v := testVault()
	real := sampleFor(RuleJWT)
	fake := v.fakeFor(&ruleTable[RuleJWT], []byte(real))

	head := real[:strings.IndexByte(real, '.')]
	if !strings.HasPrefix(fake, head+".") {
		t.Fatalf("header not preserved: %q -> %q", real, fake)
	}
	if fake == real {
		t.Fatal("fake equals the real value")
	}
	parts := strings.Split(fake, ".")
	if len(parts) != 3 {
		t.Fatalf("fake is not three segments: %q", fake)
	}
	if parts[1] == strings.Split(real, ".")[1] {
		t.Error("payload was not replaced")
	}
	if parts[2] == strings.Split(real, ".")[2] {
		t.Error("signature was not replaced")
	}
}

// TestMaskSkipsWhatItShouldNotRewrite covers the three reasons a detected
// credential is reported but left alone.
func TestMaskSkipsWhatItShouldNotRewrite(t *testing.T) {
	v := testVault()
	cases := []struct {
		name string
		text string
		want int
	}{
		{"a bare sk- value has no evidence behind it", "sk-" + rep("a1", 16), 0},
		{"the PEM rule matches a marker, not key material", "-----BEGIN RSA PRIVATE KEY-----", 0},
		{"a strong shape is rewritten", "AIza" + rep("A", 35), 1},
		{"context promotes a generic shape", "DEEPSEEK_API_KEY=sk-" + rep("a1", 16), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, events := v.Mask([]byte(tc.text))
			if len(events) != tc.want {
				t.Fatalf("masked %d value(s), want %d", len(events), tc.want)
			}
		})
	}
}

// TestMaskSkipsThePrivateKeyHeaderButStillReportsIt pins that the PEM header
// stays visible to an operator even though it is never rewritten.
func TestMaskSkipsThePrivateKeyHeaderButStillReportsIt(t *testing.T) {
	got := Scan([]byte("-----BEGIN OPENSSH PRIVATE KEY-----"))
	if len(got) != 1 || got[0].ID != RulePEMPrivateKey {
		t.Fatalf("findings = %+v, want one structural.pem_private_key", got)
	}
	if !got[0].Maskable() {
		t.Fatal("the header should score high enough to be reported")
	}
}

// TestMaskIsIdempotent matters because a format-preserving fake still looks
// like a credential: a second pass must recognise its own output and stop.
func TestMaskIsIdempotent(t *testing.T) {
	v := testVault()
	src := []byte(realisticBody())
	masked, events := v.Mask(src)
	if len(events) != 4 {
		t.Fatalf("masked %d value(s), want 4: %+v", len(events), events)
	}
	again, secondEvents := v.Mask(masked)
	if len(secondEvents) != 0 {
		t.Fatalf("second pass masked %d more value(s): %+v", len(secondEvents), secondEvents)
	}
	if !bytes.Equal(again, masked) {
		t.Fatal("second pass changed the bytes")
	}
}

// TestMaskRemovesEveryRealValue is the point of the exercise.
func TestMaskRemovesEveryRealValue(t *testing.T) {
	v := testVault()
	src := realisticBody()
	masked, _ := v.Mask([]byte(src))
	for _, secret := range realSecrets() {
		if bytes.Contains(masked, []byte(secret)) {
			t.Errorf("the real value %q survived masking", secret)
		}
		if !strings.Contains(src, secret) {
			t.Fatalf("test fixture is wrong: %q is not in the body", secret)
		}
	}
}

// TestRestoreRoundTrip checks that a whole body comes back byte for byte.
func TestRestoreRoundTrip(t *testing.T) {
	v := testVault()
	src := []byte(realisticBody())
	masked, _ := v.Mask(src)
	if bytes.Equal(masked, src) {
		t.Fatal("nothing was masked")
	}
	if got := v.Restore(masked); !bytes.Equal(got, src) {
		t.Fatalf("restore did not round-trip\n got %q\nwant %q", got, src)
	}
}

// TestRestoreAcrossChunkBoundaries is the streamed-body hazard: an SSE event
// boundary or an io.Copy buffer can split a fake anywhere, so the restorer has
// to hold back enough of every read to never release a partial fake.
func TestRestoreAcrossChunkBoundaries(t *testing.T) {
	v := testVault()
	src := []byte(realisticBody())
	masked, _ := v.Mask(src)

	for _, chunk := range []int{1, 2, 3, 7, 16, 63, 64, 65, 1024, len(masked)} {
		t.Run(fmt.Sprintf("chunk=%d", chunk), func(t *testing.T) {
			r := NewRestorer(&chunkReader{data: append([]byte(nil), masked...), n: chunk}, v)
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("round-trip failed at chunk %d\ngot  %q\nwant %q", chunk, got, src)
			}
		})
	}
}

// TestRestorerWithoutFakesIsPassthrough pins that an untouched stream is not
// buffered at all: Holdback is zero, so reads line up one for one.
func TestRestorerWithoutFakesIsPassthrough(t *testing.T) {
	v := testVault()
	src := []byte(strings.Repeat("ordinary text with no credentials. ", 64))
	if h := v.Holdback(); h != 0 {
		t.Fatalf("Holdback = %d on an empty vault, want 0", h)
	}
	cr := &chunkReader{data: append([]byte(nil), src...), n: 7}
	r := NewRestorer(cr, v)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("passthrough changed the bytes")
	}
}

// TestRestoreIgnoresLookalikes checks that restoration is exact-set matching,
// never shape matching: a credential that was never masked must not be
// rewritten into something else.
func TestRestoreIgnoresLookalikes(t *testing.T) {
	v := testVault()
	v.Mask([]byte("AIza" + rep("A", 35)))
	other := []byte("AIza" + rep("B", 35))
	if got := v.Restore(other); !bytes.Equal(got, other) {
		t.Fatalf("restore rewrote an unknown value: %q -> %q", other, got)
	}
}

// TestVaultEvictsTheOldestEntry keeps a long-running process bounded.
func TestVaultEvictsTheOldestEntry(t *testing.T) {
	v := testVault()
	v.capacity = 2
	first := "gsk_" + rep("A", 52)
	second := "gsk_" + rep("B", 52)
	third := "gsk_" + rep("C", 52)

	firstFake := v.fakeFor(&ruleTable[RuleGroqAPIKey], []byte(first))
	v.fakeFor(&ruleTable[RuleGroqAPIKey], []byte(second))
	if v.Len() != 2 {
		t.Fatalf("Len = %d, want 2", v.Len())
	}
	v.fakeFor(&ruleTable[RuleGroqAPIKey], []byte(third))
	if v.Len() != 2 {
		t.Fatalf("Len = %d, want 2 after eviction", v.Len())
	}
	if got := v.Restore([]byte(firstFake)); string(got) != firstFake {
		t.Errorf("the evicted fake was still restored: %q -> %q", firstFake, got)
	}
}

// TestMaskEventAttributes covers the audit rendering: a record-file event must
// describe the rewrite without carrying the value.
func TestMaskEventAttributes(t *testing.T) {
	v := testVault()
	_, events := v.Mask([]byte("AIza" + rep("A", 35)))
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one", events)
	}
	attrs := events[0].Attributes()
	for _, key := range []string{"rule", "vendor", "kind", "start", "end", "length", "score", "confidence", "reasons"} {
		if _, ok := attrs[key]; !ok {
			t.Errorf("attribute %q is missing from %+v", key, attrs)
		}
	}
	if attrs["rule"] != "google.api_key" {
		t.Errorf("rule = %v, want google.api_key", attrs["rule"])
	}
}

// TestVaultSeedChangesTheFake checks that the seed is what makes the mapping
// process-local, so a fake observed on the wire is useless without this vault.
func TestVaultSeedChangesTheFake(t *testing.T) {
	real := []byte("AIza" + rep("A", 35))
	a := NewVault([]byte("seed-a")).fakeFor(&ruleTable[RuleGoogleAPIKey], real)
	b := NewVault([]byte("seed-b")).fakeFor(&ruleTable[RuleGoogleAPIKey], real)
	if a == b {
		t.Fatalf("different seeds rendered the same fake %q", a)
	}
	if len(a) != len(b) {
		t.Fatalf("fakes differ in length: %d and %d", len(a), len(b))
	}
}

func sampleFor(id RuleID) string {
	for _, tt := range positiveSamples {
		if tt.rule == id {
			return tt.text
		}
	}
	panic("no sample for rule " + ruleTable[id].Name)
}

func realisticBody() string {
	return `{"model":"gpt-5","messages":[` +
		`{"role":"system","content":"DEEPSEEK_API_KEY=sk-a1b2c3d4e5f60718293a4b5c6d7e8f90"},` +
		`{"role":"user","content":"sk-proj-` + rep("A", 58) + `T3BlbkFJ` + rep("B", 58) + `"},` +
		`{"role":"user","content":"Authorization: Bearer ` + sampleFor(RuleJWT) + `"},` +
		`{"role":"user","content":"ANTHROPIC_API_KEY=sk-ant-api03-` + rep("C", 93) + `AA"}` +
		`]}`
}

func realSecrets() []string {
	return []string{
		"sk-a1b2c3d4e5f60718293a4b5c6d7e8f90",
		"sk-proj-" + rep("A", 58) + "T3BlbkFJ" + rep("B", 58),
		sampleFor(RuleJWT),
		"sk-ant-api03-" + rep("C", 93) + "AA",
	}
}

// chunkReader serves fixed-size chunks so a test can force a fake to straddle
// two reads.
type chunkReader struct {
	data []byte
	n    int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := c.n
	if n > len(p) {
		n = len(p)
	}
	if n > len(c.data) {
		n = len(c.data)
	}
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

// benchBody builds a payload large enough that the scan and the replacement
// cost are both visible: repeated prose plus the four credentials.
func benchBody() []byte {
	block := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 6)
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString(block)
		b.WriteString("\n")
	}
	b.WriteString(realisticBody())
	return []byte(b.String())
}

// BenchmarkMaskWarm measures the steady state, where every value is already in
// the vault and masking is a scan plus four copies.
func BenchmarkMaskWarm(b *testing.B) {
	v := testVault()
	body := benchBody()
	if _, events := v.Mask(body); len(events) != 4 {
		b.Fatalf("expected 4 events, got %d", len(events))
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, events := v.Mask(body); len(events) != 4 {
			b.Fatalf("expected 4 events, got %d", len(events))
		}
	}
}

// BenchmarkMaskCold includes rendering the fakes, which is what a first
// sighting of a credential costs.
func BenchmarkMaskCold(b *testing.B) {
	body := benchBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		NewVault([]byte("secretmask-bench-seed")).Mask(body)
	}
}

// BenchmarkRestoreNoSecrets is the common case: a vault exists but the body
// contains none of its fakes, so the scan must reject almost every position
// with a single lookup.
func BenchmarkRestoreNoSecrets(b *testing.B) {
	v := testVault()
	v.Mask([]byte(realisticBody()))
	body := benchBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v.Restore(body)
	}
}

// BenchmarkRestoreStreaming measures the restorer end to end, including the
// hold-back bookkeeping on every read.
func BenchmarkRestoreStreaming(b *testing.B) {
	v := testVault()
	src := benchBody()
	masked, _ := v.Mask(src)
	b.SetBytes(int64(len(masked)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewRestorer(bytes.NewReader(masked), v)
		if _, err := io.Copy(io.Discard, r); err != nil {
			b.Fatal(err)
		}
	}
}

// streamEnvelope is what a streamed provider response puts between two pieces of
// one message: the tail of the first JSON delta and the head of the next. A
// client concatenates the delta strings, so the pieces of a stand-in that a
// provider cut at a token boundary arrive with this between them.
const streamEnvelope = `"}}]}` + "\n\ndata: " +
	`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"`

// cutIntoRuns splits a value into n pieces with the streaming envelope between
// each pair, which is the shape the restorer has to reassemble.
func cutIntoRuns(value string, n int) string {
	step := (len(value) + n - 1) / n
	var b strings.Builder
	for i := 0; i < len(value); i += step {
		end := i + step
		if end > len(value) {
			end = len(value)
		}
		if i > 0 {
			b.WriteString(streamEnvelope)
		}
		b.WriteString(value[i:end])
	}
	return b.String()
}

// withoutEnvelope is the text a client ends up with once it has applied every
// delta: the pieces joined back together with nothing between them.
func withoutEnvelope(s string) string {
	return strings.ReplaceAll(s, streamEnvelope, "")
}

// TestRestoreReassemblesAStandInSplitAcrossRuns covers the shape a real stream
// produces. A provider streams one message as many JSON deltas and cuts the
// string at token boundaries, so a stand-in arrives in pieces with the envelope
// between them. The pieces are what has to be rewritten: the value is restored
// run by run, in place, so the client that joins the deltas gets the real one.
func TestRestoreReassemblesAStandInSplitAcrossRuns(t *testing.T) {
	v := testVault()
	real := "sk-proj-" + rep("A", 58) + "T3BlbkFJ" + rep("B", 58)
	masked, _ := v.Mask([]byte(real))
	fake := string(masked)

	for _, runs := range []int{2, 3, 5, 9} {
		t.Run(fmt.Sprint(runs), func(t *testing.T) {
			body := "data: " + cutIntoRuns(fake, runs) + `"}}]}`
			want := "data: " + cutIntoRuns(real, runs) + `"}}]}`

			got := string(v.Restore([]byte(body)))
			if got != want {
				t.Fatalf("restore across %d runs\n got %q\nwant %q", runs, got, want)
			}
			if joined := withoutEnvelope(got); !strings.Contains(joined, real) {
				t.Errorf("the client that joins the deltas does not see the real value: %q", joined)
			}
			if strings.Contains(withoutEnvelope(got), fake) {
				t.Errorf("the stand-in survived the restore: %q", withoutEnvelope(got))
			}
		})
	}
}

// TestRestoreKeepsAFailedChainUntouched is the property that makes a chain
// acceptable at all. A run that matches the start of a stand-in but whose
// continuation never arrives must be released byte for byte: a value is
// rewritten completely or not at all, so resembling a stand-in can never corrupt
// the text it sits in.
func TestRestoreKeepsAFailedChainUntouched(t *testing.T) {
	v := testVault()
	real := "sk-proj-" + rep("A", 58) + "T3BlbkFJ" + rep("B", 58)
	masked, _ := v.Mask([]byte(real))
	fake := []byte(masked)

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{
			// The first half really is the stand-in; the continuation never is.
			name: "truncated stand-in",
			body: append(append([]byte(nil), fake[:70]...), []byte(`"}`)...),
		},
		{
			// Every keystream byte of the tail is wrong, so the value is not ours.
			name: "near miss",
			body: append(append([]byte(nil), fake[:100]...), 'Z'),
		},
		{
			name: "run shorter than the entry threshold",
			body: append(append([]byte(nil), fake[:12]...), []byte(`"}}]}`)...),
		},
		{
			// Two runs that never add up to the whole stand-in.
			name: "runs that stop short",
			body: []byte(fake[:40]),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := v.Restore(tc.body)
			if !bytes.Equal(got, tc.body) {
				t.Fatalf("restore rewrote an incomplete stand-in:\n got %q\nwant %q", got, tc.body)
			}
		})
	}
}

// TestRestoreChainsThroughAStreamingReader runs the reassembly through the
// streaming path, where the runs of one stand-in are split across reads as well
// as across deltas, so the scan state has to survive both at once.
func TestRestoreChainsThroughAStreamingReader(t *testing.T) {
	v := testVault()
	real := "sk-proj-" + rep("A", 58) + "T3BlbkFJ" + rep("B", 58)
	masked, _ := v.Mask([]byte(real))
	fake := string(masked)

	body := "data: " + cutIntoRuns(fake, 2) + `"}}]}`
	want := "data: " + cutIntoRuns(real, 2) + `"}}]}`

	for _, chunk := range []int{1, 2, 3, 17, 40, 41, 64, 65, 133, len(body)} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			r := NewRestorer(&chunkReader{data: []byte(body), n: chunk}, v)
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if string(got) != want {
				t.Fatalf("restore at %d bytes per read\n got %q\nwant %q", chunk, got, want)
			}
			if r.Restored() != 1 {
				t.Errorf("Restored() = %d, want 1", r.Restored())
			}
		})
	}
}

// TestRestoreLeavesRunsSmallerThanTheEntryThresholdAlone pins the boundary of
// the reassembly rather than pretending it has none. Reassembly follows a
// stand-in from its first run, so a provider that cuts the value into pieces
// shorter than chainMinEntry - which token-by-token streaming can do - never
// presents a run long enough to start one. Nothing is rewritten in that case,
// which is the safe outcome: the client assembles the stand-in instead of the
// value, and no unrelated bytes are touched on the way.
func TestRestoreLeavesRunsSmallerThanTheEntryThresholdAlone(t *testing.T) {
	v := testVault()
	real := "sk-proj-" + rep("A", 58) + "T3BlbkFJ" + rep("B", 58)
	masked, _ := v.Mask([]byte(real))
	fake := string(masked)

	step := chainMinEntry - 4
	body := "data: " + cutIntoFixedRuns(fake, step) + `"}}]}`

	got := v.Restore([]byte(body))
	if !bytes.Equal(got, []byte(body)) {
		t.Fatalf("restore rewrote a stand-in cut into %d-byte runs:\n got %q\nwant %q", step, got, body)
	}
	if joined := withoutEnvelope(string(got)); !strings.Contains(joined, fake) {
		t.Errorf("expected the client to assemble the stand-in, got %q", joined)
	}
}

// cutIntoFixedRuns is cutIntoRuns with a fixed piece size instead of a piece
// count, so a test can drive the run length below chainMinEntry.
func cutIntoFixedRuns(value string, step int) string {
	var b strings.Builder
	for i := 0; i < len(value); i += step {
		end := i + step
		if end > len(value) {
			end = len(value)
		}
		if i > 0 {
			b.WriteString(streamEnvelope)
		}
		b.WriteString(value[i:end])
	}
	return b.String()
}
