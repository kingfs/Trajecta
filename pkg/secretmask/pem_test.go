package secretmask

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// armor builds a test block the way an RFC 7468 writer would. newline is "\n"
// for a file, "" for a body that was minified, and "\r\n" for the CRLF form.
func armor(label string, raw []byte, newline string) string {
	body := base64.StdEncoding.EncodeToString(raw)
	var b strings.Builder
	b.WriteString("-----BEGIN " + label + "-----" + newline)
	for len(body) > 64 {
		b.WriteString(body[:64] + newline)
		body = body[64:]
	}
	b.WriteString(body + newline)
	b.WriteString("-----END " + label + "-----")
	return b.String()
}

func testSSHKey() []byte {
	raw := make([]byte, 387)
	copy(raw, []byte("openssh-key-v1\x00"))
	for i := len(raw) - 1; i >= 0; i-- {
		raw[i] ^= byte(i * 31)
	}
	return raw
}

// TestPEMBlockIsOneSpan pins the whole point of the block rule: a complete
// armored key is one finding that covers the key material, not a header
// finding that covers none of it.
func TestPEMBlockIsOneSpan(t *testing.T) {
	block := armor("OPENSSH PRIVATE KEY", testSSHKey(), "\n")
	src := []byte("please copy this to the host:\n" + block + "\nand restart sshd")
	findings := Scan(src)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one block", findings)
	}
	f := findings[0]
	if f.ID != RulePEMPrivateKeyBlock {
		t.Fatalf("rule = %s, want %s", ruleName(f.ID), ruleName(RulePEMPrivateKeyBlock))
	}
	if got := string(f.Bytes(src)); got != block {
		t.Fatalf("span = %q, want the whole block", got)
	}
	if !f.Maskable() {
		t.Fatal("a complete private-key block must be maskable")
	}
}

// TestPEMBlockInAJSONString is the shape a request body actually carries: the
// newlines between armor lines are the two bytes '\' 'n'.
func TestPEMBlockInAJSONString(t *testing.T) {
	block := armor("OPENSSH PRIVATE KEY", testSSHKey(), "\n")
	escaped := strings.ReplaceAll(block, "\n", `\n`)
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"my key is ` + escaped + `, deploy it"}]}`)

	findings := Scan(body)
	if len(findings) != 1 || findings[0].ID != RulePEMPrivateKeyBlock {
		t.Fatalf("findings = %+v, want one block", findings)
	}
	if got := string(findings[0].Bytes(body)); got != escaped {
		t.Fatalf("span lost the escaped block: %q", got)
	}

	v := testVault()
	masked, events := v.Mask(body)
	if len(events) != 1 {
		t.Fatalf("masked %d value(s), want 1", len(events))
	}
	if len(masked) != len(body) {
		t.Fatalf("masked length %d, want %d", len(masked), len(body))
	}
	if bytes.Contains(masked, []byte(escaped)) {
		t.Fatal("the real block survived masking")
	}
	if !bytes.HasPrefix(masked, []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"my key is -----BEGIN OPENSSH PRIVATE KEY-----\n`)) {
		t.Fatalf("stand-in lost the JSON prefix or the armor header: %q", masked)
	}
	if !bytes.HasSuffix(masked, []byte(`, deploy it"}]}`)) {
		t.Fatalf("stand-in lost the JSON suffix: %q", masked)
	}
	if got := v.Restore(masked); !bytes.Equal(got, body) {
		t.Fatal("restore did not return the original request body")
	}
}

// TestPEMBlockCRLFAndYAMLIndent covers the two whitespace forms a file-based
// key takes when it is pasted into YAML: CRLF line endings and an indentation
// in front of every line.
func TestPEMBlockCRLFAndYAMLIndent(t *testing.T) {
	block := armor("RSA PRIVATE KEY", testSSHKey(), "\r\n")
	indented := "  " + strings.ReplaceAll(block, "\r\n", "\r\n  ")
	src := []byte("ssh_key: |\r\n" + indented + "\r\n")

	findings := Scan(src)
	if len(findings) != 1 || findings[0].ID != RulePEMPrivateKeyBlock {
		t.Fatalf("findings = %+v, want one block", findings)
	}
	if got, want := string(findings[0].Bytes(src)), indented[len("  "):]; got != want {
		t.Fatalf("span = %q, want the indented block %q", got, want)
	}

	v := testVault()
	masked, events := v.Mask(src)
	if len(events) != 1 || len(masked) != len(src) {
		t.Fatalf("masked %d value(s) length %d, want 1 / %d", len(events), len(masked), len(src))
	}
	if !bytes.Equal(v.Restore(masked), src) {
		t.Fatal("restore did not return the original YAML body")
	}
}

// TestPEMBlockRequiresAMatchingLabel is the back-reference the DFA cannot
// express: an OPENSSH body closed by an RSA footer is not a block.
func TestPEMBlockRequiresAMatchingLabel(t *testing.T) {
	src := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("A", 200) + "\n-----END RSA PRIVATE KEY-----")
	findings := Scan(src)
	if len(findings) != 1 || findings[0].ID != RulePEMPrivateKey {
		t.Fatalf("findings = %+v, want the header finding only", findings)
	}
	v := testVault()
	if _, events := v.Mask(src); len(events) != 0 {
		t.Fatalf("mismatched footer was rewritten: %+v", events)
	}
}

// TestPEMBlockTruncatedStaysAHeader keeps the policy for a paste that lost its
// footer: the header is reported, and the bytes are left exactly as they are,
// because a truncated block cannot be rewritten without guessing where it ends.
func TestPEMBlockTruncatedStaysAHeader(t *testing.T) {
	src := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("A", 200))
	findings := Scan(src)
	if len(findings) != 1 || findings[0].ID != RulePEMPrivateKey {
		t.Fatalf("findings = %+v, want the header finding only", findings)
	}
	v := testVault()
	masked, events := v.Mask(src)
	if len(events) != 0 || !bytes.Equal(masked, src) {
		t.Fatalf("a truncated block was rewritten: %+v", events)
	}
}

// TestPEMBlockDoesNotSpanProse is the guard against one header and a distant
// footer swallowing the JSON between them.
func TestPEMBlockDoesNotSpanProse(t *testing.T) {
	src := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nnot key material at all, just a sentence\n-----END OPENSSH PRIVATE KEY-----")
	findings := Scan(src)
	for _, f := range findings {
		if f.ID == RulePEMPrivateKeyBlock {
			t.Fatalf("prose was accepted as a block: %q", f.Bytes(src))
		}
	}
	if len(findings) != 1 || findings[0].ID != RulePEMPrivateKey {
		t.Fatalf("findings = %+v, want the header finding only", findings)
	}
}

// TestPublicKeyAndCertificateAreNotCandidates pins the deliberate exclusion:
// a public key is not a secret, and a certificate is not a private key.
func TestPublicKeyAndCertificateAreNotCandidates(t *testing.T) {
	cert := armor("CERTIFICATE", testSSHKey(), "\n")
	public := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILn9GfQ0q0h9p3sV0o3nhj3O8pQ1Tk0l4lG7JmYw2xZq user@host"
	for _, text := range []string{cert, public, public + "\n" + cert} {
		if findings := Scan([]byte(text)); len(findings) != 0 {
			var got []string
			for _, f := range findings {
				got = append(got, ruleName(f.ID))
			}
			t.Errorf("non-secret matched %v: %q", got, text)
		}
		if masked, events := testVault().Mask([]byte(text)); len(events) != 0 || string(masked) != text {
			t.Errorf("non-secret was rewritten: %+v", events)
		}
	}
}

// TestMaskSSHPrivateKeyKeepsArmorAndLength pins the stand-in's shape: the two
// delimiter lines and every line break stay where they were, so the body is
// still base64-shaped and the length never changes, while the key material
// itself is gone.
func TestMaskSSHPrivateKeyKeepsArmorAndLength(t *testing.T) {
	block := armor("OPENSSH PRIVATE KEY", testSSHKey(), "\n")
	src := []byte("deploy this key to web-1:\n" + block + "\nthanks")
	v := testVault()

	masked, events := v.Mask(src)
	if len(events) != 1 {
		t.Fatalf("masked %d value(s), want 1", len(events))
	}
	if len(masked) != len(src) {
		t.Fatalf("masked length %d, want %d", len(masked), len(src))
	}
	start := events[0].Start
	standIn := string(masked[start:events[0].End])
	if standIn == block {
		t.Fatal("the stand-in is the real block")
	}
	if !strings.HasPrefix(standIn, "-----BEGIN OPENSSH PRIVATE KEY-----\n") {
		t.Fatalf("stand-in lost the header: %q", standIn)
	}
	if !strings.HasSuffix(standIn, "\n-----END OPENSSH PRIVATE KEY-----") {
		t.Fatalf("stand-in lost the footer: %q", standIn)
	}
	if got, want := strings.Count(standIn, "\n"), strings.Count(block, "\n"); got != want {
		t.Fatalf("stand-in has %d line breaks, want %d", got, want)
	}
	// Every body byte has to stay in the base64 alphabet, or the value stops
	// looking like the armor it replaced.
	body := standIn[strings.Index(standIn, "\n")+1 : strings.LastIndex(standIn, "\n-----END ")]
	for _, line := range strings.Split(body, "\n") {
		if len(line) > 64 {
			t.Fatalf("stand-in line is %d bytes, want at most 64", len(line))
		}
		if _, err := base64.StdEncoding.DecodeString(line); err != nil {
			t.Fatalf("stand-in body line is not base64: %q", line)
		}
	}

	if restored := v.Restore(masked); !bytes.Equal(restored, src) {
		t.Fatal("restore did not return the original body")
	}
	// The same key renders the same stand-in on a second pass, and a second
	// pass over the stand-in is a no-op.
	if again, more := v.Mask(masked); len(more) != 0 || !bytes.Equal(again, masked) {
		t.Fatalf("masking is not idempotent: %d more event(s)", len(more))
	}
}

// TestPEMBlockSuppressesInnerFindings covers the body that happens to contain
// another rule's shape. The block finding must replace every candidate inside
// it, or two overlapping spans would rewrite the same bytes twice.
func TestPEMBlockSuppressesInnerFindings(t *testing.T) {
	body := "AIza" + strings.Repeat("A", 35)
	block := "-----BEGIN OPENSSH PRIVATE KEY-----\n" + body + "\n-----END OPENSSH PRIVATE KEY-----"
	findings := Scan([]byte(block))
	if len(findings) != 1 || findings[0].ID != RulePEMPrivateKeyBlock {
		t.Fatalf("findings = %+v, want only the block", findings)
	}
	v := testVault()
	masked, events := v.Mask([]byte(block))
	if len(events) != 1 || events[0].Rule != RulePEMPrivateKeyBlock {
		t.Fatalf("events = %+v, want only the block", events)
	}
	if len(masked) != len(block) {
		t.Fatalf("masked length %d, want %d", len(masked), len(block))
	}
}

// TestLongFakeDoesNotChainOnAnotherPEMHeader pins the restore-side threshold.
// A 4 KB stand-in shares "-----BEGIN " with every certificate and public key,
// so an eight-byte chain entry would let any such header stall the stream for
// chainMaxGap bytes. Sixteen bytes is past that shared prefix.
func TestLongFakeDoesNotChainOnAnotherPEMHeader(t *testing.T) {
	block := armor("RSA PRIVATE KEY", bytes.Repeat([]byte{0x42}, 3000), "\n")
	v := testVault()
	masked, events := v.Mask([]byte(block))
	if len(events) != 1 {
		t.Fatalf("masked %d value(s), want 1", len(events))
	}
	if len(masked) < chainLongFake {
		t.Fatalf("fixture is only %d bytes, want a long fake", len(masked))
	}

	buf := []byte("intro -----BEGIN CERTIFICATE-----\n" + strings.Repeat("MI", 200) + "\n-----END CERTIFICATE-----\n")
	v.mu.RLock()
	settled, _ := v.restoreScan(buf, &streamState{})
	v.mu.RUnlock()
	if settled != len(buf) {
		t.Fatalf("restore held %d of %d bytes on a certificate header", len(buf)-settled, len(buf))
	}
}

// TestRestoreStreamsALongBlock is the positive half: a 4 KB stand-in cut into
// several deltas by the JSON envelope still comes back as the real key.
func TestRestoreStreamsALongBlock(t *testing.T) {
	block := armor("RSA PRIVATE KEY", bytes.Repeat([]byte{0x42}, 3000), "\n")
	v := testVault()
	masked, events := v.Mask([]byte(block))
	if len(events) != 1 {
		t.Fatalf("masked %d value(s), want 1", len(events))
	}
	fake := masked
	third := len(fake) / 3
	var body []byte
	body = append(body, []byte("thinking... ")...)
	body = append(body, fake[:third]...)
	body = append(body, []byte(`","content":"`)...)
	body = append(body, fake[third:2*third]...)
	body = append(body, []byte(`"}`)...)
	body = append(body, fake[2*third:]...)
	body = append(body, []byte(" done")...)

	r := NewRestorer(&chunkReader{data: body, n: 37}, v)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The restorer rewrites each run where it sits and leaves the stream
	// envelope alone, exactly as a client's SSE parser expects: the key arrives
	// in pieces separated by the JSON that carried it.
	real := []byte(block)
	for i, part := range [][]byte{real[:third], real[third : 2*third], real[2*third:]} {
		if !bytes.Contains(got, part) {
			t.Fatalf("piece %d of the real block was not restored", i)
		}
	}
	if bytes.Contains(got, fake) {
		t.Fatal("the stand-in reached the client unrestored")
	}
	if len(got) != len(body) {
		t.Fatalf("restore changed the length %d -> %d", len(body), len(got))
	}
}

func ruleName(id RuleID) string {
	r, ok := RuleFor(id)
	if !ok {
		return "unknown"
	}
	return r.Name
}

// TestRestoreSSHKeyThroughJSONEncodings covers the two depths a response can
// carry the stand-in at: plain message text, where the JSON encoder escapes the
// newlines once, and a tool call's arguments, which is a JSON string inside the
// JSON response and therefore escapes them twice. The client has to receive the
// real key at the same depth, or the local tool gets a stand-in it cannot use.
func TestRestoreSSHKeyThroughJSONEncodings(t *testing.T) {
	block := armor("OPENSSH PRIVATE KEY", testSSHKey(), "\n")
	v := testVault()

	requestBody := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"` + strings.ReplaceAll(block, "\n", `\n`) + `"}]}`)
	maskedRequest, events := v.Mask(requestBody)
	if len(events) != 1 {
		t.Fatalf("masked %d value(s), want 1", len(events))
	}
	fakeEscaped := string(maskedRequest[events[0].Start:events[0].End])
	realEscaped := string(requestBody[events[0].Start:events[0].End])
	if !strings.Contains(fakeEscaped, `\n`) {
		t.Fatal("the request did not carry an escaped block")
	}

	// The provider decodes the request before the model sees it, so the model
	// echoes a value with real newlines; encoding the response escapes them once.
	fakeDecoded := jsonUnescapeValue(fakeEscaped)
	textResponse, err := json.Marshal(map[string]string{"echo": fakeDecoded})
	if err != nil {
		t.Fatalf("marshal text response: %v", err)
	}
	argsJSON, err := json.Marshal(map[string]string{"content": fakeDecoded})
	if err != nil {
		t.Fatalf("marshal tool arguments: %v", err)
	}
	toolResponse, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{
				"tool_calls": []any{map[string]any{
					"function": map[string]any{"name": "write_file", "arguments": string(argsJSON)},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("marshal tool response: %v", err)
	}

	cases := []struct {
		name string
		body []byte
		want []byte
	}{
		{"message text", textResponse, []byte(realEscaped)},
		{"tool arguments", toolResponse, []byte(jsonEscapeValue(realEscaped))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !bytes.Contains(tc.body, []byte(fakeEscaped)) && !bytes.Contains(tc.body, []byte(jsonEscapeValue(fakeEscaped))) {
				t.Fatalf("fixture does not carry the stand-in: %s", tc.body)
			}
			restored := v.Restore(tc.body)
			if len(restored) != len(tc.body) {
				t.Fatalf("restore changed the length %d -> %d", len(tc.body), len(restored))
			}
			if !bytes.Contains(restored, tc.want) {
				t.Fatalf("the real key was not restored\nwant contains: %q\nbody: %s", tc.want, restored)
			}
			if bytes.Contains(restored, []byte(fakeEscaped)) || bytes.Contains(restored, []byte(jsonEscapeValue(fakeEscaped))) {
				t.Fatal("a stand-in survived the restore")
			}
			// The response must still be valid JSON at both depths.
			if !json.Valid(restored) {
				t.Fatalf("restored body is not valid JSON: %s", restored)
			}
			for _, chunk := range []int{7, 37, 512} {
				r := NewRestorer(&chunkReader{data: append([]byte(nil), tc.body...), n: chunk}, v)
				got, err := io.ReadAll(r)
				if err != nil {
					t.Fatalf("chunk %d: read: %v", chunk, err)
				}
				if !bytes.Contains(got, tc.want) {
					t.Fatalf("chunk %d: the real key was not restored", chunk)
				}
				if !json.Valid(got) {
					t.Fatalf("chunk %d: restored stream is not valid JSON", chunk)
				}
			}
		})
	}
}

// TestRestoreSSHKeyAcrossStreamedDeltas is the shape a provider really streams:
// one value cut into small deltas, each wrapped in its own JSON object. The run
// that opens the chain stops at the first envelope, before the escape depth is
// visible, so the chain has to look past that envelope to decide which depth the
// stream carries.
func TestRestoreSSHKeyAcrossStreamedDeltas(t *testing.T) {
	block := armor("OPENSSH PRIVATE KEY", testSSHKey(), "\n")
	v := testVault()

	requestBody := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"` + strings.ReplaceAll(block, "\n", `\n`) + `"}]}`)
	maskedRequest, events := v.Mask(requestBody)
	if len(events) != 1 {
		t.Fatalf("masked %d value(s), want 1", len(events))
	}
	fakeEscaped := string(maskedRequest[events[0].Start:events[0].End])
	fakeDecoded := jsonUnescapeValue(fakeEscaped)

	// Plain message text: the model echoes the decoded stand-in, so the wire
	// carries the once-escaped form between the delta envelopes.
	var text strings.Builder
	for i := 0; i < len(fakeDecoded); i += 17 {
		end := i + 17
		if end > len(fakeDecoded) {
			end = len(fakeDecoded)
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"content": fakeDecoded[i:end]},
		}}})
		text.WriteString("data: " + string(chunk) + "\n\n")
	}
	text.WriteString("data: [DONE]\n\n")

	// Tool-call arguments: the model streams an inner JSON document as a string,
	// so the wire carries each newline escaped twice.
	inner, err := json.Marshal(map[string]string{"content": fakeDecoded})
	if err != nil {
		t.Fatalf("marshal inner arguments: %v", err)
	}
	var tool strings.Builder
	for i := 0; i < len(inner); i += 17 {
		end := i + 17
		if end > len(inner) {
			end = len(inner)
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index":    0,
				"function": map[string]any{"name": "write_file", "arguments": string(inner[i:end])},
			}}},
		}}})
		tool.WriteString("data: " + string(chunk) + "\n\n")
	}
	tool.WriteString("data: [DONE]\n\n")

	parseSSE := func(body []byte, field string) string {
		var out strings.Builder
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
				continue
			}
			var ev struct {
				Choices []struct {
					Delta struct {
						Content   string `json:"content"`
						ToolCalls []struct {
							Function struct {
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatalf("decode delta: %v", err)
			}
			for _, ch := range ev.Choices {
				if field == "content" {
					out.WriteString(ch.Delta.Content)
				} else if len(ch.Delta.ToolCalls) > 0 {
					out.WriteString(ch.Delta.ToolCalls[0].Function.Arguments)
				}
			}
		}
		return out.String()
	}

	head := func(s string) string {
		if len(s) > 96 {
			return s[:96]
		}
		return s
	}

	for _, chunk := range []int{5, 11, 64} {
		r := NewRestorer(&chunkReader{data: []byte(text.String()), n: chunk}, v)
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("text chunk %d: read: %v", chunk, err)
		}
		if restored := parseSSE(got, "content"); restored != block {
			t.Fatalf("text chunk %d: the real key was not restored across deltas (restored=%d)\ngot  %q\nwant %q", chunk, r.Restored(), head(restored), head(block))
		}

		r = NewRestorer(&chunkReader{data: []byte(tool.String()), n: chunk}, v)
		got, err = io.ReadAll(r)
		if err != nil {
			t.Fatalf("tool chunk %d: read: %v", chunk, err)
		}
		args := parseSSE(got, "arguments")
		var decoded struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(args), &decoded); err != nil {
			t.Fatalf("tool chunk %d: restored arguments are not valid JSON: %v; head=%q", chunk, err, head(args))
		}
		if decoded.Content != block {
			t.Fatalf("tool chunk %d: the real key was not restored across deltas; head=%q", chunk, head(args))
		}
	}
}
