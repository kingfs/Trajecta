package convert_test

// Cross-engine parity corpus (P5): the same input is converted through the
// legacy engine (pkg/protocolconvert via the pkg/llm IR, production default)
// and the XIR hub (pkg/xir/convert, opt-in) under the exact production
// semantics of internal/proxy:
//
//   - request: legacy ConvertRequest + FatalWarnings() refusal
//     (protocol_conversion.go convertRequestBody); xir ConvertRequest with
//     Options{Model: wireModel, Vertex: vertex, Policy: Strict}
//     (protocol_conversion_xir.go convertRequestBodyXIR).
//   - response: legacy ConvertResponse (no fatal gate; response warnings are
//     never fatal in production); xir ConvertResponse with the same Strict
//     options (convertResponseBodyXIR).
//
// The comparison is decision-first (accept/reject, then rejection category),
// then semantic JSON equality (key order / whitespace normalized) with a
// per-key field report; byte-level differences between semantically equal
// bodies are recorded as formatting, not divergence.
//
// Known divergences live in parity_divergence_test.go: the parity tests skip
// them with a reason, and TestParityKnownDivergences pins each one so an
// engine fix surfaces as a promotion signal instead of silent drift.
//
// Set TRAJECTA_PARITY_DUMP=<path> to write the full machine-readable result
// matrix (TestParityDump) for the audit report; it is skipped otherwise.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/protocolconvert"
	"github.com/kingfs/Trajecta/pkg/xir"
	convert "github.com/kingfs/Trajecta/pkg/xir/convert"
)

// parityWireModel mirrors the production wire-model override: the proxy always
// passes a non-empty upstream model name into both engines.
const parityWireModel = "wire-model"

var parityProtocols = []protocolconvert.Protocol{
	protocolconvert.ProtocolChatCompletions,
	protocolconvert.ProtocolResponses,
	protocolconvert.ProtocolMessages,
	protocolconvert.ProtocolGemini,
}

type parityCase struct {
	ID       string          `json:"id"`
	Protocol string          `json:"protocol"`
	Note     string          `json:"note"`
	Vertex   bool            `json:"vertex,omitempty"`
	Body     json.RawMessage `json:"body,omitempty"`
	BodyRaw  *string         `json:"body_raw,omitempty"`
}

func (c parityCase) bytes() []byte {
	if c.BodyRaw != nil {
		return []byte(*c.BodyRaw)
	}
	return []byte(c.Body)
}

func loadParityCorpus(t *testing.T, name string) []parityCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "parity", name))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var cases []parityCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse corpus %s: %v", name, err)
	}
	seen := make(map[string]bool, len(cases))
	for _, c := range cases {
		if c.ID == "" || c.Protocol == "" {
			t.Fatalf("corpus %s: case missing id/protocol: %+v", name, c)
		}
		if seen[c.ID] {
			t.Fatalf("corpus %s: duplicate case id %q", name, c.ID)
		}
		seen[c.ID] = true
		if c.BodyRaw == nil && len(c.Body) == 0 {
			t.Fatalf("corpus %s: case %q has no body", name, c.ID)
		}
	}
	return cases
}

func xirProtocol(p protocolconvert.Protocol) xir.Protocol {
	switch p {
	case protocolconvert.ProtocolChatCompletions:
		return xir.ProtocolChatCompletions
	case protocolconvert.ProtocolResponses:
		return xir.ProtocolResponses
	case protocolconvert.ProtocolMessages:
		return xir.ProtocolMessages
	case protocolconvert.ProtocolGemini:
		return xir.ProtocolGemini
	}
	panic("unknown protocol " + p)
}

// parityOutcome is one engine's production-visible result. Category values:
// "ok", "streaming", "fatal" (fatal loss / fatal warnings), "invalid"
// (parse/unsupported-content/other deterministic rejection).
type parityOutcome struct {
	Accepted bool     `json:"accepted"`
	Category string   `json:"category"`
	Err      string   `json:"err,omitempty"`
	Body     string   `json:"body,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

func legacyWarningStrings(warnings []protocolconvert.Warning) []string {
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		if w.Fatal {
			out = append(out, w.Field+"!")
		} else {
			out = append(out, w.Field)
		}
	}
	return out
}

func xirLossStrings(losses []xir.Loss) []string {
	out := make([]string, 0, len(losses))
	for _, l := range losses {
		if l.Fatal || l.Kind == xir.LossRejected {
			out = append(out, l.Field+"!")
		} else {
			out = append(out, l.Field)
		}
	}
	return out
}

func classifyLegacyErr(err error) string {
	if strings.Contains(err.Error(), "streaming") {
		return "streaming"
	}
	return "invalid"
}

func classifyXIRErr(err error) string {
	switch {
	case errors.Is(err, convert.ErrStreamingConversion):
		return "streaming"
	case errors.Is(err, convert.ErrFatalLoss):
		return "fatal"
	default:
		return "invalid"
	}
}

func runLegacyRequest(body []byte, from, to protocolconvert.Protocol, vertex bool) parityOutcome {
	result, err := protocolconvert.ConvertRequest(body, from, to, parityWireModel, vertex)
	if err != nil {
		return parityOutcome{Category: classifyLegacyErr(err), Err: err.Error()}
	}
	if fatal := result.FatalWarnings(); len(fatal) > 0 {
		fields := make([]string, 0, len(fatal))
		for _, w := range fatal {
			fields = append(fields, w.Field)
		}
		return parityOutcome{
			Category: "fatal",
			Err:      "protocol conversion refused: " + strings.Join(fields, ", "),
			Warnings: legacyWarningStrings(result.Warnings),
		}
	}
	return parityOutcome{Accepted: true, Category: "ok", Body: string(result.Body), Warnings: legacyWarningStrings(result.Warnings)}
}

func runXIRRequest(body []byte, from, to protocolconvert.Protocol, vertex bool) parityOutcome {
	result, err := convert.ConvertRequest(context.Background(), body, xirProtocol(from), xirProtocol(to), convert.Options{
		Model:  parityWireModel,
		Vertex: vertex,
		Policy: convert.Strict,
	})
	if err != nil {
		return parityOutcome{Category: classifyXIRErr(err), Err: err.Error(), Warnings: xirLossStrings(result.Loss)}
	}
	return parityOutcome{Accepted: true, Category: "ok", Body: string(result.Bytes), Warnings: xirLossStrings(result.Loss)}
}

func runLegacyResponse(body []byte, from, to protocolconvert.Protocol, vertex bool) parityOutcome {
	// Production response direction: upstream protocol -> client protocol.
	result, err := protocolconvert.ConvertResponse(body, from, to, parityWireModel, vertex)
	if err != nil {
		return parityOutcome{Category: classifyLegacyErr(err), Err: err.Error()}
	}
	return parityOutcome{Accepted: true, Category: "ok", Body: string(result.Body), Warnings: legacyWarningStrings(result.Warnings)}
}

func runXIRResponse(body []byte, from, to protocolconvert.Protocol, vertex bool) parityOutcome {
	result, err := convert.ConvertResponse(context.Background(), body, xirProtocol(from), xirProtocol(to), convert.Options{
		Model:  parityWireModel,
		Vertex: vertex,
		Policy: convert.Strict,
	})
	if err != nil {
		return parityOutcome{Category: classifyXIRErr(err), Err: err.Error(), Warnings: xirLossStrings(result.Loss)}
	}
	return parityOutcome{Accepted: true, Category: "ok", Body: string(result.Bytes), Warnings: xirLossStrings(result.Loss)}
}

// parityComparison is the verdict for one (case, pair, direction).
type parityComparison struct {
	DecisionMatch bool     `json:"decision_match"`
	CategoryMatch bool     `json:"category_match"`
	SemanticMatch bool     `json:"semantic_match"`
	ByteEqual     bool     `json:"byte_equal"`
	Class         string   `json:"class"`
	DiffPaths     []string `json:"diff_paths,omitempty"`
	KeyFieldDiffs []string `json:"key_field_diffs,omitempty"`
}

// normalizeJSON decodes to generic values so key order and whitespace do not
// matter. Numbers become float64 on both sides, which is exact for every
// integer in this corpus (token counts, seeds, penalties).
func normalizeJSON(raw string) (any, bool) {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, false
	}
	return v, true
}

// jsonDiffPaths walks two normalized JSON values and reports mismatched leaf
// paths (bounded so a wholly different body stays readable).
func jsonDiffPaths(a, b any, path string, out *[]string) {
	if len(*out) >= 24 {
		return
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, path+" (type)")
			return
		}
		keys := make(map[string]bool, len(av)+len(bv))
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			ak, aok := av[k]
			bk, bok := bv[k]
			sub := k
			if path != "" {
				sub = path + "." + k
			}
			switch {
			case !aok:
				*out = append(*out, sub+" (xir only)")
			case !bok:
				*out = append(*out, sub+" (legacy only)")
			default:
				jsonDiffPaths(ak, bk, sub, out)
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			*out = append(*out, path+" (type)")
			return
		}
		if len(av) != len(bv) {
			*out = append(*out, fmt.Sprintf("%s (len %d vs %d)", path, len(av), len(bv)))
		}
		n := len(av)
		if len(bv) < n {
			n = len(bv)
		}
		for i := 0; i < n; i++ {
			jsonDiffPaths(av[i], bv[i], fmt.Sprintf("%s[%d]", path, i), out)
		}
	default:
		if !reflect.DeepEqual(a, b) {
			*out = append(*out, fmt.Sprintf("%s (%v vs %v)", path, a, b))
		}
	}
}

// parityKeyFields are the fields the audit compares item by item, spelled per
// target protocol. A full-body DeepEqual already covers them; this list names
// which of the audit-critical fields differ when a body diverges.
var parityKeyFields = []string{
	"model", "messages", "input", "contents", "system", "instructions", "systemInstruction",
	"tools", "tool_choice", "toolConfig", "usage", "usageMetadata",
	"max_tokens", "max_output_tokens", "max_completion_tokens", "generationConfig",
	"choices", "output", "content", "candidates", "stop_reason", "finishReason", "status",
}

func keyFieldDiffs(a, b any) []string {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if !aok || !bok {
		return nil
	}
	var out []string
	for _, key := range parityKeyFields {
		av, ahas := am[key]
		bv, bhas := bm[key]
		if !ahas && !bhas {
			continue
		}
		if !reflect.DeepEqual(av, bv) {
			out = append(out, key)
		}
	}
	return out
}

func compareParityOutcomes(legacy, xir parityOutcome) parityComparison {
	c := parityComparison{
		DecisionMatch: legacy.Accepted == xir.Accepted,
		CategoryMatch: legacy.Category == xir.Category,
	}
	switch {
	case !legacy.Accepted && !xir.Accepted:
		c.SemanticMatch = true // nothing converted on either side
		c.ByteEqual = true
		if !c.DecisionMatch {
			c.Class = "decision-divergence"
		} else if !c.CategoryMatch {
			c.Class = "category-divergence"
		} else {
			c.Class = "parity"
		}
	case legacy.Accepted && xir.Accepted:
		c.ByteEqual = legacy.Body == xir.Body
		la, lok := normalizeJSON(legacy.Body)
		xa, xok := normalizeJSON(xir.Body)
		if lok && xok {
			c.SemanticMatch = reflect.DeepEqual(la, xa)
			if !c.SemanticMatch {
				jsonDiffPaths(la, xa, "", &c.DiffPaths)
				c.KeyFieldDiffs = keyFieldDiffs(la, xa)
			}
		} else {
			// Non-JSON accepted bodies (same-protocol passthrough of raw
			// bytes): only byte identity is meaningful.
			c.SemanticMatch = c.ByteEqual
		}
		switch {
		case !c.SemanticMatch:
			c.Class = "semantic-divergence"
		case !c.ByteEqual:
			c.Class = "format-only"
		default:
			c.Class = "parity"
		}
	default:
		c.Class = "decision-divergence"
	}
	return c
}

type parityRecord struct {
	Direction string           `json:"direction"`
	From      string           `json:"from"`
	To        string           `json:"to"`
	Case      string           `json:"case"`
	Note      string           `json:"note,omitempty"`
	Vertex    bool             `json:"vertex,omitempty"`
	Input     string           `json:"input"`
	Legacy    parityOutcome    `json:"legacy"`
	XIR       parityOutcome    `json:"xir"`
	Verdict   parityComparison `json:"verdict"`
}

func parityKey(direction string, from, to protocolconvert.Protocol, caseID string) string {
	return direction + "|" + string(from) + "|" + string(to) + "|" + caseID
}

func runParityMatrix(t *testing.T, direction string, cases []parityCase) []parityRecord {
	t.Helper()
	var records []parityRecord
	for _, c := range cases {
		from := protocolconvert.Protocol(c.Protocol)
		if !from.Valid() {
			t.Fatalf("case %q: unknown protocol %q", c.ID, c.Protocol)
		}
		for _, to := range parityProtocols {
			body := c.bytes()
			var legacy, xir parityOutcome
			if direction == "request" {
				legacy = runLegacyRequest(body, from, to, c.Vertex)
				xir = runXIRRequest(body, from, to, c.Vertex)
			} else {
				legacy = runLegacyResponse(body, from, to, c.Vertex)
				xir = runXIRResponse(body, from, to, c.Vertex)
			}
			records = append(records, parityRecord{
				Direction: direction,
				From:      c.Protocol,
				To:        string(to),
				Case:      c.ID,
				Note:      c.Note,
				Vertex:    c.Vertex,
				Input:     string(body),
				Legacy:    legacy,
				XIR:       xir,
				Verdict:   compareParityOutcomes(legacy, xir),
			})
		}
	}
	return records
}

func assertParityRecord(t *testing.T, rec parityRecord) {
	t.Helper()
	key := parityKey(rec.Direction, protocolconvert.Protocol(rec.From), protocolconvert.Protocol(rec.To), rec.Case)
	if entry, known := knownParityDivergences[key]; known {
		t.Skipf("known %s divergence: %s (see .scratch/audit/p5-parity.md)", entry.Kind, entry.Reason)
	}
	v := rec.Verdict
	if v.Class == "parity" || v.Class == "format-only" {
		if v.Class == "format-only" {
			t.Logf("byte-level formatting difference (semantically equal): %s", key)
		}
		return
	}
	t.Errorf("%s divergence [%s]\ninput:  %s\nlegacy: accepted=%v category=%s err=%q warnings=%v\n        body=%s\nxir:    accepted=%v category=%s err=%q losses=%v\n        body=%s\ndiff paths: %v\nkey fields: %v",
		key, v.Class, rec.Input,
		rec.Legacy.Accepted, rec.Legacy.Category, rec.Legacy.Err, rec.Legacy.Warnings, rec.Legacy.Body,
		rec.XIR.Accepted, rec.XIR.Category, rec.XIR.Err, rec.XIR.Warnings, rec.XIR.Body,
		v.DiffPaths, v.KeyFieldDiffs)
}

func TestParityRequests(t *testing.T) {
	cases := loadParityCorpus(t, "requests.json")
	for _, rec := range runParityMatrix(t, "request", cases) {
		t.Run(rec.Case+"/"+rec.From+"_to_"+rec.To, func(t *testing.T) {
			assertParityRecord(t, rec)
		})
	}
}

func TestParityResponses(t *testing.T) {
	cases := loadParityCorpus(t, "responses.json")
	for _, rec := range runParityMatrix(t, "response", cases) {
		t.Run(rec.Case+"/"+rec.From+"_to_"+rec.To, func(t *testing.T) {
			assertParityRecord(t, rec)
		})
	}
}

// TestParityDump writes the full result matrix when TRAJECTA_PARITY_DUMP names
// an output file. It is an audit tool, not a CI assertion; without the env var
// it skips. Known divergences are included so the report stays complete.
func TestParityDump(t *testing.T) {
	out := os.Getenv("TRAJECTA_PARITY_DUMP")
	if out == "" {
		t.Skip("set TRAJECTA_PARITY_DUMP=<path> to dump the parity result matrix")
	}
	var records []parityRecord
	records = append(records, runParityMatrix(t, "request", loadParityCorpus(t, "requests.json"))...)
	records = append(records, runParityMatrix(t, "response", loadParityCorpus(t, "responses.json"))...)
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("marshal dump: %v", err)
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		t.Fatalf("write dump: %v", err)
	}
	counts := map[string]int{}
	for _, rec := range records {
		counts[rec.Verdict.Class]++
	}
	t.Logf("dumped %d records to %s: %v", len(records), out, counts)
}

// TestParityGeminiPathControl pins the engine-independent path contract: the
// proxy builds the converted upstream path with protocolconvert.RequestPath
// for BOTH engines (protocol_conversion.go upstreamPath), so Gemini path-model
// validation (":action" suffix, traversal, whitespace) cannot diverge between
// engines. This control asserts the shared validator's behavior.
func TestParityGeminiPathControl(t *testing.T) {
	for _, model := range []string{"", "a:b", "gemini-2.5-flash:generateContent", "../../v1/messages", "a b", "/leading", "trailing/", "a..b"} {
		if _, err := protocolconvert.ProtocolGemini.RequestPath(model, false, false); err == nil {
			t.Errorf("RequestPath(%q) must be rejected for both engines (shared path builder)", model)
		}
	}
	for _, vertex := range []bool{false, true} {
		path, err := protocolconvert.ProtocolGemini.RequestPath("gemini-2.5-flash", false, vertex)
		if err != nil {
			t.Fatalf("vertex=%v: %v", vertex, err)
		}
		want := "/v1beta/models/gemini-2.5-flash:generateContent"
		if vertex {
			want = "/v1/publishers/models/gemini-2.5-flash:generateContent"
		}
		if path != want {
			t.Errorf("vertex=%v path = %q, want %q", vertex, path, want)
		}
	}
}

// TestParityOutOfBandStreamControl covers the streaming flag that lives outside
// the body (Gemini ?alt=sse / Options.Stream). Production rejects it in the
// proxy layer before either engine runs (PlanForRequest / the alt=sse check in
// conversionForSelection), and the xir engine additionally rejects
// Options.Stream itself. The legacy engine has no stream parameter; this is an
// API-surface difference, not a corpus divergence.
func TestParityOutOfBandStreamControl(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	for _, from := range parityProtocols {
		for _, to := range parityProtocols {
			if from == to {
				continue
			}
			_, err := convert.ConvertRequest(context.Background(), body, xirProtocol(from), xirProtocol(to), convert.Options{
				Model:  parityWireModel,
				Stream: true,
				Policy: convert.Strict,
			})
			if !errors.Is(err, convert.ErrStreamingConversion) {
				t.Errorf("%s -> %s: Options.Stream err = %v, want ErrStreamingConversion", from, to, err)
			}
		}
	}
}

// TestParitySameProtocolByteIdentity is the passthrough control: both engines
// must return the exact input bytes for a same-protocol conversion, including
// bodies that are not valid JSON.
func TestParitySameProtocolByteIdentity(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{ "model" : "m" , "messages" : [ ] , "unknown_future_field" : { "x" : 1 } }`),
		[]byte("this is not json  "),
		[]byte(""),
	}
	for _, p := range parityProtocols {
		for _, body := range bodies {
			legacy, err := protocolconvert.ConvertRequest(body, p, p, parityWireModel, false)
			if err != nil {
				t.Fatalf("legacy %s same-protocol: %v", p, err)
			}
			if !bytes.Equal(legacy.Body, body) {
				t.Errorf("legacy %s same-protocol bytes changed", p)
			}
			xirResult, err := convert.ConvertRequest(context.Background(), body, xirProtocol(p), xirProtocol(p), convert.Options{Model: parityWireModel, Policy: convert.Strict})
			if err != nil {
				t.Fatalf("xir %s same-protocol: %v", p, err)
			}
			if !bytes.Equal(xirResult.Bytes, body) {
				t.Errorf("xir %s same-protocol bytes changed", p)
			}
		}
	}
}
