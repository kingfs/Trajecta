package detector

import (
	"bytes"
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// maskOutcome is the comparable result of one masking pass: the wire bytes and
// a normalized view of every replacement event. Two paths are correct iff they
// produce the same outcome on the same body with an equally-seeded vault.
type maskOutcome struct {
	wire   []byte
	events []eventView
}

type eventView struct {
	rule       string
	start, end int
	score      int
	confidence string
	reasons    string
	original   string
	standIn    string
}

func outcomeOf(wire []byte, events []secretmask.MaskEvent) maskOutcome {
	views := make([]eventView, len(events))
	for i, e := range events {
		original, standIn := e.AuditValues()
		views[i] = eventView{
			rule:       e.RuleName(),
			start:      e.Start,
			end:        e.End,
			score:      e.Score,
			confidence: e.Confidence.String(),
			reasons:    e.Reasons.String(),
			original:   original,
			standIn:    standIn,
		}
	}
	return maskOutcome{wire: wire, events: views}
}

// assertMaskEquivalent runs the ground-truth byte path (Vault.Mask) and the path
// under test (masker.Mask) on the same body with equally-seeded fresh vaults and
// asserts byte-for-byte identical wire output and identical replacement events.
// This is the correctness oracle for the hot-path IR work: the IR path must not
// change what leaves the machine relative to the proven byte path.
func assertMaskEquivalent(t *testing.T, name string, body []byte, masker *Masker) {
	t.Helper()
	seed := []byte("differential-corpus-seed-0123456789")

	wantVault := secretmask.NewVault(seed)
	wantWire, wantEvents := wantVault.Mask(body)
	want := outcomeOf(wantWire, wantEvents)

	gotVault := secretmask.NewVault(seed)
	gotMasker := NewMasker(gotVault, masker.rt, 0)
	gotWire, gotEvents := gotMasker.Mask(body)
	got := outcomeOf(gotWire, gotEvents)

	compareOutcomes(t, name, want, got)
}

func compareOutcomes(t *testing.T, name string, want, got maskOutcome) {
	t.Helper()
	if !bytes.Equal(got.wire, want.wire) {
		t.Fatalf("%s: wire bytes differ\n byte path: %s\n ir path:   %s", name, want.wire, got.wire)
	}
	if len(got.events) != len(want.events) {
		t.Fatalf("%s: event count differs: byte=%d ir=%d\n byte=%+v\n ir=%+v", name, len(want.events), len(got.events), want.events, got.events)
	}
	for i := range want.events {
		if got.events[i] != want.events[i] {
			t.Fatalf("%s: event %d differs\n byte: %+v\n ir:   %+v", name, i, want.events[i], got.events[i])
		}
	}
}

// TestMaskerIRParityWithVault is the hot-path correctness gate: the IR-scoped
// path (MaskWithProtocol) must produce byte-for-byte identical wire output and
// identical replacement events to the proven whole-body byte path (Vault.Mask)
// on the whole corpus, including the opaque cases where encrypted content must
// stay untouched.
func TestMaskerIRParityWithVault(t *testing.T) {
	rt, err := NewRuntime(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range maskCorpus() {
		t.Run(entry.name, func(t *testing.T) {
			seed := []byte("differential-corpus-seed-0123456789")
			body := []byte(entry.body)

			wantVault := secretmask.NewVault(seed)
			wantWire, wantEvents := wantVault.Mask(body)
			want := outcomeOf(wantWire, wantEvents)

			gotVault := secretmask.NewVault(seed)
			m := NewMasker(gotVault, rt, 0)
			gotWire, gotEvents := m.MaskWithProtocol(body, entry.protocol)
			got := outcomeOf(gotWire, gotEvents)

			compareOutcomes(t, entry.name, want, got)
		})
	}
}

// TestMaskerByteParityWithVault pins the starting point: the detector.Masker in
// its byte mode (no IR scoping) must be exactly equivalent to Vault.Mask on the
// whole corpus. Later phases plug the IR-scoped path into the same oracle.
func TestMaskerByteParityWithVault(t *testing.T) {
	rt, err := NewRuntime(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range maskCorpus() {
		t.Run(entry.name, func(t *testing.T) {
			assertMaskEquivalent(t, entry.name, []byte(entry.body), NewMasker(nil, rt, 0))
		})
	}
}
