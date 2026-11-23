package monitor

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// `trace.minimal_cassette` clears timing and usage from the prelude on purpose,
// so the header the detail page used to report was 0 ms / 0 tokens on exactly
// the deployments that record the most traffic - while the 统计 tab beside it
// read the index row and showed the real duration. The overlay keeps the two
// from disagreeing.

func TestFillHeaderFromIndexFillsWhatThePreludeDropped(t *testing.T) {
	header := recordHeaderView{
		Meta: recordfile.MetaData{
			Model:    "gpt-test",
			Endpoint: "/v1/chat/completions",
			// What minimalCassetteHeader leaves behind.
			DurationMs: 0,
			TTFTMs:     0,
		},
		Layout: recordfile.LayoutInfo{IsStream: false, ReqBodyLen: 120},
		Usage:  recordfile.UsageInfo{},
	}
	index := recordfile.RecordHeader{
		Meta:   recordfile.MetaData{DurationMs: 7966, TTFTMs: 6275},
		Layout: recordfile.LayoutInfo{IsStream: true},
		Usage: recordfile.UsageInfo{
			PromptTokens: 350000, CompletionTokens: 900, TotalTokens: 350900,
			PromptTokenDetails: &recordfile.PromptTokenDetails{CachedTokens: 349000},
		},
	}
	fillHeaderFromIndex(&header, index)
	meta := header.Meta.(recordfile.MetaData)
	if meta.DurationMs != 7966 || meta.TTFTMs != 6275 {
		t.Fatalf("timing = %+v, want the index row", meta)
	}
	if meta.Model != "gpt-test" || meta.Endpoint != "/v1/chat/completions" {
		t.Fatalf("meta = %+v, want the prelude's own identity untouched", meta)
	}
	if !header.Layout.(recordfile.LayoutInfo).IsStream {
		t.Fatal("layout.is_stream = false, want the index row's stream flag")
	}
	if header.Layout.(recordfile.LayoutInfo).ReqBodyLen != 120 {
		t.Fatal("layout lost the byte lengths the prelude did carry")
	}
	usage := header.Usage.(recordfile.UsageInfo)
	if usage.PromptTokens != 350000 || usage.TotalTokens != 350900 {
		t.Fatalf("usage = %+v, want the index row", usage)
	}
}

func TestFillHeaderFromIndexKeepsTheCassettesOwnValues(t *testing.T) {
	header := recordHeaderView{
		Meta:   recordfile.MetaData{DurationMs: 1200, TTFTMs: 300},
		Layout: recordfile.LayoutInfo{IsStream: true},
		Usage:  recordfile.UsageInfo{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}
	index := recordfile.RecordHeader{
		Meta:   recordfile.MetaData{DurationMs: 9999, TTFTMs: 8888},
		Layout: recordfile.LayoutInfo{IsStream: false},
		Usage:  recordfile.UsageInfo{PromptTokens: 1000, CompletionTokens: 1, TotalTokens: 1001},
	}
	fillHeaderFromIndex(&header, index)
	meta := header.Meta.(recordfile.MetaData)
	if meta.DurationMs != 1200 || meta.TTFTMs != 300 {
		t.Fatalf("timing = %+v, want the file's own values", meta)
	}
	if !header.Layout.(recordfile.LayoutInfo).IsStream {
		t.Fatal("layout.is_stream was overwritten by the index row")
	}
	if header.Usage.(recordfile.UsageInfo).PromptTokens != 10 {
		t.Fatal("usage was overwritten by the index row")
	}
}
