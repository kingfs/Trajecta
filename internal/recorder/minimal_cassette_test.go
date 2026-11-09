package recorder

import (
	"bytes"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestMinimalCassetteHeaderKeepsOnlyParseMetadata(t *testing.T) {
	when := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	header := RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: MetaData{
			RequestID:                      "req",
			Time:                           when,
			Model:                          "gpt-5",
			Provider:                       "openai",
			Operation:                      "chat_completions",
			Endpoint:                       "/v1/chat/completions",
			URL:                            "http://proxy.local/v1/chat/completions",
			Method:                         "POST",
			StatusCode:                     200,
			ContentLength:                  12,
			DurationMs:                     99,
			TTFTMs:                         10,
			ClientIP:                       "127.0.0.1",
			SelectedUpstreamID:             "channel:default",
			SelectedUpstreamBaseURL:        "https://api.openai.com/v1",
			SelectedUpstreamProviderPreset: "openai",
			RoutingPolicy:                  "p2c",
			RoutingScore:                   0.5,
			RoutingCandidateCount:          2,
			RoutingFailureReason:           "",
			RouteTargetID:                  "target",
			ChannelID:                      "channel",
			CredentialID:                   "default",
			StickyStatus:                   "hit",
			ClientHarness:                  "codex-cli",
			ClientHarnessName:              "Codex CLI",
			ClientHarnessTags:              []string{"streaming"},
		},
		Layout: LayoutInfo{ReqHeaderLen: 10, ReqBodyLen: 20, ResHeaderLen: 30, ResBodyLen: 40, IsStream: true},
		Usage:  UsageInfo{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	}
	got := minimalCassetteHeader(header)
	if got.Version != header.Version || got.Layout != header.Layout {
		t.Fatalf("version/layout lost: %+v", got)
	}
	if got.Meta.Time != when || got.Meta.Model != "gpt-5" || got.Meta.Endpoint != "/v1/chat/completions" {
		t.Fatalf("parse metadata lost: %+v", got.Meta)
	}
	if got.Meta.StatusCode != 200 || got.Meta.ContentLength != 12 {
		t.Fatalf("http metadata lost: %+v", got.Meta)
	}
	for name, value := range map[string]string{
		"client ip":      got.Meta.ClientIP,
		"upstream":       got.Meta.SelectedUpstreamID,
		"routing policy": got.Meta.RoutingPolicy,
		"route target":   got.Meta.RouteTargetID,
		"channel":        got.Meta.ChannelID,
		"credential":     got.Meta.CredentialID,
		"sticky":         got.Meta.StickyStatus,
		"harness":        got.Meta.ClientHarness,
		"harness name":   got.Meta.ClientHarnessName,
	} {
		if value != "" {
			t.Fatalf("%s not stripped: %q", name, value)
		}
	}
	if got.Meta.DurationMs != 0 || got.Meta.TTFTMs != 0 || got.Meta.RoutingScore != 0 {
		t.Fatalf("derived numbers not stripped: %+v", got.Meta)
	}
	if got.Usage.TotalTokens != 0 {
		t.Fatalf("usage not stripped: %+v", got.Usage)
	}
	if len(got.Meta.ClientHarnessTags) != 0 {
		t.Fatalf("harness tags not stripped: %v", got.Meta.ClientHarnessTags)
	}
}

func TestUpdateLogFileMinimalCassetteDropsDerivedPrelude(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, false, nil)

	req, err := http.NewRequest("POST", "http://proxy.local/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-5"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	info, err := rec.PrepareLogFileWithOptionsAndBody(req, PrepareOptions{
		SiteURL:            "https://api.openai.com/v1",
		SelectedUpstreamID: "channel:default",
		RoutingPolicy:      "p2c",
		RoutingScore:       0.5,
		MinimalCassette:    true,
	}, []byte(`{"model":"gpt-5"}`))
	if err != nil {
		t.Fatal(err)
	}
	info.Header.Meta.StatusCode = 200
	info.Header.Layout.ResHeaderLen = int64(len("HTTP/1.1 200 OK\r\n\r\n"))
	info.Header.Layout.ResBodyLen = int64(len(`{"ok":true}`))
	info.Events = []RecordEvent{{Type: "llm.usage", Time: info.Header.Meta.Time, Attributes: map[string]any{"total_tokens": 3}}}
	if _, err := info.File.Write([]byte("\nHTTP/1.1 200 OK\r\n\r\n{\"ok\":true}")); err != nil {
		t.Fatal(err)
	}
	if err := rec.UpdateLogFile(info); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(info.Path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := recordfile.ParsePrelude(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Events) != 0 {
		t.Fatalf("minimal cassette kept events: %+v", parsed.Events)
	}
	if parsed.Header.Meta.RoutingPolicy != "" || parsed.Header.Meta.SelectedUpstreamID != "" {
		t.Fatalf("routing metadata kept: %+v", parsed.Header.Meta)
	}
	if parsed.Header.Meta.Model != "gpt-5" || parsed.Header.Layout.ResBodyLen != int64(len(`{"ok":true}`)) {
		t.Fatalf("parse metadata lost: %+v", parsed.Header)
	}
	if parsed.Header.Usage.TotalTokens != 0 {
		t.Fatalf("usage kept: %+v", parsed.Header.Usage)
	}
}
