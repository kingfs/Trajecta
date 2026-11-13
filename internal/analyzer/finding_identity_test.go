package analyzer

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/observe"
)

// oneNodeObservation builds the smallest observation the detectors scan.
func oneNodeObservation(text string) observe.TraceObservation {
	node := observe.SemanticNode{
		ID:             "node-1",
		NormalizedType: observe.NodeText,
		Path:           "$.messages[0].content",
		Text:           text,
	}
	return observe.TraceObservation{
		TraceID: "trace-1",
		Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
}

// A finding's identity has to include the rule. Before this, two different
// credential rules on one node produced the same stable ID and the second
// finding was silently dropped by dedupeFindings.
func TestCredentialDetectorReportsEachRuleOnOneNode(t *testing.T) {
	const dsnPassword = "Zz9Qq2Ww3Ee4Rr5Tt6Yy7"
	obs := oneNodeObservation("postgres://user:" + dsnPassword + "@db.local/app aws AKIAIOSFODNN7EXAMPLE")

	findings, err := CredentialDetector{}.Detect(context.Background(), obs)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2 (dsn.password + aws.access_key_id): %+v", len(findings), findings)
	}
	titles := map[string]bool{}
	for _, finding := range findings {
		titles[finding.Title] = true
		if finding.ID == "" {
			t.Fatalf("finding %q has no stable ID", finding.Title)
		}
	}
	for _, want := range []string{"Credential: dsn.password", "Credential: aws.access_key_id"} {
		if !titles[want] {
			t.Errorf("missing finding %q; got %v", want, titles)
		}
	}
}

func TestPIIDetectorReportsEachKindOnOneNode(t *testing.T) {
	obs := oneNodeObservation("身份证 11010519491231002X 手机号 13800138000")

	findings, err := PIIDetector{}.Detect(context.Background(), obs)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2 (national_id + mobile): %+v", len(findings), findings)
	}
	titles := map[string]bool{}
	for _, finding := range findings {
		titles[finding.Title] = true
	}
	for _, want := range []string{"Personal data: national_id", "Personal data: mobile"} {
		if !titles[want] {
			t.Errorf("missing finding %q; got %v", want, titles)
		}
	}
}

// locateCredential must return the bytes of the rule the finding names, not
// the first credential in the node: an AWS key before a DSN must not be
// highlighted for the DSN finding.
func TestLocateCredentialUsesTheFindingRule(t *testing.T) {
	const dsnPassword = "Zz9Qq2Ww3Ee4Rr5Tt6Yy7"
	text := "aws AKIAIOSFODNN7EXAMPLE then postgres://user:" + dsnPassword + "@db.local/app"
	obs := oneNodeObservation(text)

	match, ok := LocateFinding(obs, observe.Finding{
		TraceID:  "trace-1",
		Category: "credential_leak",
		Title:    "Credential: dsn.password",
		NodeID:   "node-1",
		Detector: "credential",
	})
	if !ok {
		t.Fatal("LocateFinding returned false")
	}
	if match.Text != dsnPassword {
		t.Fatalf("located %q, want the dsn password", match.Text)
	}

	match, ok = LocateFinding(obs, observe.Finding{
		TraceID:  "trace-1",
		Category: "credential_leak",
		Title:    "Credential: aws.access_key_id",
		NodeID:   "node-1",
		Detector: "credential",
	})
	if !ok {
		t.Fatal("LocateFinding returned false for the AWS finding")
	}
	if match.Text != "AKIAIOSFODNN7EXAMPLE" {
		t.Fatalf("located %q, want the AWS key", match.Text)
	}
}

// The per-run cache must actually reuse one scan for the same text: the
// credential and PII detectors both call scanText with the node's bytes.
func TestScanTextReusesWithinOneRun(t *testing.T) {
	ctx := withScanCache(context.Background())
	const text = "aws AKIAIOSFODNN7EXAMPLE"
	first := scanText(ctx, text)
	second := scanText(ctx, text)
	if len(first) == 0 || len(second) == 0 {
		t.Fatal("expected a match")
	}
	if &first[0] != &second[0] {
		t.Fatal("scanText rescanned the same text within one run")
	}
	fresh := scanText(withScanCache(context.Background()), text)
	if len(fresh) == 0 {
		t.Fatal("expected a match in a fresh cache")
	}
	if &first[0] == &fresh[0] {
		t.Fatal("a fresh cache must not reuse the previous run's result")
	}
}

// The runner is the production entry point: both detectors must report their
// findings for one node and the shared scan must not change the result.
func TestRunnerReportsBothCredentialAndPIIOnOneNode(t *testing.T) {
	obs := oneNodeObservation("postgres://user:Zz9Qq2Ww3Ee4Rr5Tt6Yy7@db.local/app 身份证 11010519491231002X")
	findings, err := NewRunnerList(DefaultDetectors()).Analyze(context.Background(), obs)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	var credential, pii bool
	for _, finding := range findings {
		switch finding.Category {
		case "credential_leak":
			credential = true
		case "pii_leak":
			pii = true
		}
	}
	if !credential || !pii {
		t.Fatalf("credential=%v pii=%v, want both", credential, pii)
	}
}
