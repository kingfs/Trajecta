package router

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestRecordedFilterReasonsAreDocumented keeps the documented vocabulary of the recorded
// `routing.route_plan` event's `candidate_summary[].filter_reason` equal to the values this
// package actually emits.
//
// The documentation states the value set exhaustively, so a reason added here - or one the page
// never learned about, like `native_responses_required` - makes the page wrong about what an
// operator will find in a cassette. Both directions are checked, because a page that lists a
// value the router cannot emit is just as misleading as one that omits a value it can.
func TestRecordedFilterReasonsAreDocumented(t *testing.T) {
	code := recordedFilterReasonsFromPackage(t)
	documented := documentedFilterReasons(t)

	for _, reason := range sortedKeys(code) {
		if !documented[reason] {
			t.Errorf("docs/ROUTING_AND_CREDENTIALS.md does not document filter_reason %q, which internal/router emits", reason)
		}
	}
	for _, reason := range sortedKeys(documented) {
		if !code[reason] {
			t.Errorf("docs/ROUTING_AND_CREDENTIALS.md documents filter_reason %q, which internal/router never emits", reason)
		}
	}
}

var filterReasonAssignment = regexp.MustCompile(`\.FilterReason\s*=\s*"([^"]*)"`)

// recordedFilterReasonsFromPackage scans this package's non-test sources. An empty assignment is
// the "not filtered" value the event attributes skip, so it is not part of the vocabulary.
func recordedFilterReasonsFromPackage(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	out := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range filterReasonAssignment.FindAllStringSubmatch(string(source), -1) {
			if match[1] != "" {
				out[match[1]] = true
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no filter_reason assignments found in internal/router; the scan is looking in the wrong place")
	}
	return out
}

var documentedVocabularyStart = regexp.MustCompile("`candidate_summary\\[\\]\\.filter_reason` 取值为")

// documentedFilterReasons parses the value list out of the documentation sentence. The list runs
// from "取值为" to the clause that introduces the failure reason, and every entry is backticked.
func documentedFilterReasons(t *testing.T) map[string]bool {
	t.Helper()
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "ROUTING_AND_CREDENTIALS.md"))
	if err != nil {
		t.Fatalf("read documentation: %v", err)
	}
	location := documentedVocabularyStart.FindStringIndex(string(page))
	if location == nil {
		t.Fatal("docs/ROUTING_AND_CREDENTIALS.md no longer states the filter_reason vocabulary; update this gate with the new wording")
	}
	rest := string(page)[location[1]:]
	end := strings.Index(rest, "，失败原因")
	if end < 0 {
		t.Fatal("docs/ROUTING_AND_CREDENTIALS.md no longer ends the filter_reason vocabulary with the failure_reason clause; update this gate with the new wording")
	}
	out := map[string]bool{}
	for _, entry := range strings.Split(rest[:end], "、") {
		entry = strings.TrimSpace(entry)
		// Drop the parenthetical glosses, which are not part of the value list.
		if index := strings.Index(entry, "（"); index >= 0 {
			entry = entry[:index]
		}
		if !strings.HasPrefix(entry, "`") || !strings.HasSuffix(entry, "`") {
			continue
		}
		value := strings.Trim(entry, "`")
		if value == "" || strings.ContainsAny(value, " .[]") {
			continue
		}
		out[value] = true
	}
	if len(out) == 0 {
		t.Fatal("parsed no filter_reason values out of docs/ROUTING_AND_CREDENTIALS.md; update this gate with the new wording")
	}
	return out
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
