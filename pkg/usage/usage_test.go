package usage

import "testing"

func TestNormalizeFillsTotal(t *testing.T) {
	got := Usage{InputTokens: 10, OutputTokens: 3}.Normalize()
	if got.TotalTokens != 13 {
		t.Fatalf("TotalTokens = %d, want 13", got.TotalTokens)
	}
}

func TestNormalizeKeepsReportedTotal(t *testing.T) {
	got := Usage{InputTokens: 10, OutputTokens: 3, TotalTokens: 99}.Normalize()
	if got.TotalTokens != 99 {
		t.Fatalf("TotalTokens = %d, want 99", got.TotalTokens)
	}
}

func TestMergeNonZeroWins(t *testing.T) {
	a := Usage{InputTokens: 10, OutputTokens: 3, CacheReadTokens: 4, Source: "chat"}
	b := Usage{OutputTokens: 8, ReasoningTokens: 2, Source: "responses"}
	got := Merge(a, b)
	if got.InputTokens != 10 || got.OutputTokens != 8 {
		t.Fatalf("input/output = %d/%d, want 10/8", got.InputTokens, got.OutputTokens)
	}
	if got.CacheReadTokens != 4 {
		t.Fatalf("CacheReadTokens = %d, want 4", got.CacheReadTokens)
	}
	if got.ReasoningTokens != 2 {
		t.Fatalf("ReasoningTokens = %d, want 2", got.ReasoningTokens)
	}
	if got.TotalTokens != 18 {
		t.Fatalf("TotalTokens = %d, want 18", got.TotalTokens)
	}
	if got.Source != "responses" {
		t.Fatalf("Source = %q, want responses", got.Source)
	}
}

func TestMergeZeroDoesNotErase(t *testing.T) {
	a := Usage{InputTokens: 10, OutputTokens: 3, CacheCreation5mTokens: 7}
	got := Merge(a, Usage{})
	if got.InputTokens != 10 || got.OutputTokens != 3 || got.CacheCreation5mTokens != 7 {
		t.Fatalf("merge zero erased counters: %+v", got)
	}
	if !got.Normalize().Empty() && got.TotalTokens != 13 {
		t.Fatalf("TotalTokens = %d, want 13", got.TotalTokens)
	}
}

func TestMergeKeepsReportedTotalWithoutDetail(t *testing.T) {
	got := Merge(Usage{}, Usage{TotalTokens: 42})
	if got.TotalTokens != 42 {
		t.Fatalf("TotalTokens = %d, want 42", got.TotalTokens)
	}
}

func TestMergeIdempotent(t *testing.T) {
	a := Usage{InputTokens: 10, OutputTokens: 3, CacheReadTokens: 4}
	once := Merge(a, a)
	twice := Merge(once, a)
	if once != twice {
		t.Fatalf("not idempotent: %+v vs %+v", once, twice)
	}
}

func TestPromptTokens(t *testing.T) {
	u := Usage{InputTokens: 10, CacheReadTokens: 4, CacheCreationTokens: 2}
	if got := u.PromptTokens(); got != 16 {
		t.Fatalf("PromptTokens = %d, want 16", got)
	}
}

func TestEmpty(t *testing.T) {
	if !(Usage{}).Empty() {
		t.Fatal("zero usage should be empty")
	}
	if (Usage{TotalTokens: 1}).Empty() {
		t.Fatal("reported total should not be empty")
	}
}
