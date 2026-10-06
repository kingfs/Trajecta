package store

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// TestFindingFiltersNormalizeSeverityAndCategory pins that the findings filters speak the same
// vocabulary as the events filters next to them.
//
// The audit page sends lowercase values, so the UI never noticed that `?severity=High` and
// `?severity=all` matched nothing: ListAllFindings and ListFindings compared the raw query value
// against the stored lowercase severity, while the events API normalizes its enum-like filters
// (case-insensitively, with `all` meaning "no filter"). An agent calling the MCP tool with `High`,
// or an operator reusing `all` from the events API, got an empty finding list - a false "no findings"
// answer. Both read paths are asserted, because they agree with each other either way.
func TestFindingFiltersNormalizeSeverityAndCategory(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "findings.http")
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	if err := st.UpsertLog(path, recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:  "findings.http",
			Time:       time.Now().UTC(),
			Model:      "gpt-5",
			Provider:   "openai_compatible",
			Endpoint:   "/v1/chat/completions",
			URL:        "/v1/chat/completions",
			Method:     http.MethodPost,
			StatusCode: http.StatusOK,
		},
	}); err != nil {
		t.Fatalf("UpsertLog error = %v", err)
	}
	entry, err := st.GetByRequestID("findings.http")
	if err != nil {
		t.Fatalf("GetByRequestID error = %v", err)
	}

	now := time.Now().UTC()
	if err := st.SaveFindings(entry.ID, []observe.Finding{
		{
			ID:        "finding-secret",
			TraceID:   entry.ID,
			Category:  "credential_leak",
			Severity:  observe.SeverityHigh,
			Title:     "Credential-like value",
			CreatedAt: now,
		},
		{
			ID:        "finding-refusal",
			TraceID:   entry.ID,
			Category:  "model_refusal",
			Severity:  observe.SeverityLow,
			Title:     "Model refusal",
			CreatedAt: now,
		},
	}); err != nil {
		t.Fatalf("SaveFindings error = %v", err)
	}

	cases := []struct {
		name   string
		filter FindingFilter
		want   []string
	}{
		{name: "no filter", filter: FindingFilter{}, want: []string{"finding-refusal", "finding-secret"}},
		{name: "severity all", filter: FindingFilter{Severity: "all"}, want: []string{"finding-refusal", "finding-secret"}},
		{name: "severity lower", filter: FindingFilter{Severity: "high"}, want: []string{"finding-secret"}},
		{name: "severity title case", filter: FindingFilter{Severity: "High"}, want: []string{"finding-secret"}},
		{name: "severity upper", filter: FindingFilter{Severity: "HIGH"}, want: []string{"finding-secret"}},
		{name: "severity padded", filter: FindingFilter{Severity: "  High  "}, want: []string{"finding-secret"}},
		{name: "category title case", filter: FindingFilter{Category: "Credential_Leak"}, want: []string{"finding-secret"}},
		{name: "category all", filter: FindingFilter{Category: "all"}, want: []string{"finding-refusal", "finding-secret"}},
		{name: "severity and category", filter: FindingFilter{Severity: "LOW", Category: "MODEL_REFUSAL"}, want: []string{"finding-refusal"}},
		{name: "unrecognised severity matches nothing", filter: FindingFilter{Severity: "nonsense"}, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			all, err := st.ListAllFindings(tc.filter, 50)
			if err != nil {
				t.Fatalf("ListAllFindings(%+v) error = %v", tc.filter, err)
			}
			perTrace, err := st.ListFindings(entry.ID, tc.filter)
			if err != nil {
				t.Fatalf("ListFindings(%+v) error = %v", tc.filter, err)
			}
			global := findingIDsOf(all)
			if !equalStringSlices(global, tc.want) {
				t.Fatalf("ListAllFindings(%+v) = %v, want %v", tc.filter, global, tc.want)
			}
			if trace := findingIDsOf(perTrace); !equalStringSlices(trace, tc.want) {
				t.Fatalf("ListFindings(%+v) = %v, want %v: the global and per-trace finding lists must apply the same filter", tc.filter, trace, tc.want)
			}
		})
	}
}

func findingIDsOf(findings []observe.Finding) []string {
	ids := make([]string, 0, len(findings))
	for _, finding := range findings {
		ids = append(ids, finding.ID)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil
	}
	return ids
}
