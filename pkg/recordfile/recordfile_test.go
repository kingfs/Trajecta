package recordfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalAndParsePreludeV3(t *testing.T) {
	header := RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: MetaData{
			RequestID:  "req-1",
			Time:       time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC),
			Model:      "gpt-4.1",
			URL:        "/v1/chat/completions",
			Method:     "POST",
			StatusCode: 200,
		},
		Layout: LayoutInfo{
			ReqHeaderLen: 100,
			ReqBodyLen:   50,
			ResHeaderLen: 80,
			ResBodyLen:   120,
			IsStream:     true,
		},
		Usage: UsageInfo{
			PromptTokens:     11,
			CompletionTokens: 7,
			TotalTokens:      18,
		},
	}

	prelude, err := MarshalPrelude(header, BuildEvents(header))
	require.NoError(t, err)

	content := append(prelude, []byte("REQUEST\nRESPONSE")...)
	parsed, err := ParsePrelude(content)
	require.NoError(t, err)

	assert.Equal(t, header, parsed.Header)
	assert.Len(t, parsed.Events, 2)
	assert.Equal(t, int64(len(prelude)), parsed.PayloadOffset)
}

func TestMarshalAndParsePreludeV3PreservesEventAttributes(t *testing.T) {
	header := RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: MetaData{
			RequestID: "req-2",
			Time:      time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC),
			Model:     "gpt-5",
			URL:       "/v1/responses",
			Method:    "POST",
		},
	}
	events := []RecordEvent{
		{
			Type:    "llm.usage",
			Time:    time.Date(2026, 3, 31, 10, 0, 1, 0, time.UTC),
			Message: "",
			Attributes: map[string]interface{}{
				"prompt_tokens":     float64(11),
				"completion_tokens": float64(7),
				"total_tokens":      float64(18),
			},
		},
	}

	prelude, err := MarshalPrelude(header, events)
	require.NoError(t, err)

	parsed, err := ParsePrelude(prelude)
	require.NoError(t, err)
	require.Len(t, parsed.Events, 1)
	assert.Equal(t, "llm.usage", parsed.Events[0].Type)
	assert.Equal(t, float64(18), parsed.Events[0].Attributes["total_tokens"])
}

func TestMarshalAndParsePreludeV3PreservesExchangeMetadata(t *testing.T) {
	header := RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: MetaData{
			RequestID:        "req-exchange-1",
			RequestAuditID:   "audit-exchange-1",
			ExchangeID:       "exchange-entry-1",
			ExchangeKind:     "entry",
			ExchangeRole:     "responses_entry",
			ParentExchangeID: "exchange-parent-1",
			SequenceIndex:    2,
			TraceID:          "trace-exchange-1",
			Time:             time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC),
			Model:            "gpt-5",
			URL:              "/v1/responses",
			Method:           "POST",
			StatusCode:       200,
		},
	}

	prelude, err := MarshalPrelude(header, BuildEvents(header))
	require.NoError(t, err)

	parsed, err := ParsePrelude(prelude)
	require.NoError(t, err)

	assert.Equal(t, "exchange-entry-1", parsed.Header.Meta.ExchangeID)
	assert.Equal(t, "entry", parsed.Header.Meta.ExchangeKind)
	assert.Equal(t, "responses_entry", parsed.Header.Meta.ExchangeRole)
	assert.Equal(t, "exchange-parent-1", parsed.Header.Meta.ParentExchangeID)
	assert.Equal(t, 2, parsed.Header.Meta.SequenceIndex)
	assert.Equal(t, "trace-exchange-1", parsed.Header.Meta.TraceID)
}

func TestSummarizeHTTPExchangePreservesCorrelationAndBoundsBodies(t *testing.T) {
	header := RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: MetaData{
			RequestID:       "trace-1",
			RequestAuditID:  "reqaudit-1",
			ResponseID:      "resp-1",
			ConversationID:  "thread-1",
			ClientRequestID: "client-1",
			Time:            time.Date(2026, 6, 22, 11, 0, 0, 0, time.UTC),
			Model:           "gpt-5",
			URL:             "/v1/responses",
			Method:          "POST",
			StatusCode:      200,
		},
		Layout: LayoutInfo{
			ReqHeaderLen: int64(len("POST /v1/responses HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n")),
			ReqBodyLen:   int64(len(`{"model":"gpt-5","input":"hello"}`)),
			ResHeaderLen: int64(len("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n")),
			ResBodyLen:   int64(len(`{"id":"resp-1","output_text":"hello world"}`)),
		},
	}
	prelude, err := MarshalPrelude(header, BuildEvents(header))
	require.NoError(t, err)
	content := append(prelude, []byte("POST /v1/responses HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n")...)
	content = append(content, []byte(`{"model":"gpt-5","input":"hello"}`)...)
	content = append(content, '\n')
	content = append(content, []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n")...)
	content = append(content, []byte(`{"id":"resp-1","output_text":"hello world"}`)...)

	summary, err := SummarizeHTTPExchange(content, CassetteSummaryOptions{BodyLimit: 12})
	require.NoError(t, err)

	assert.Equal(t, "reqaudit-1", summary.Header.Meta.RequestAuditID)
	assert.Equal(t, "client-1", summary.Header.Meta.ClientRequestID)
	assert.Equal(t, "POST", summary.Request.Method)
	assert.Equal(t, 200, summary.Response.StatusCode)
	assert.Equal(t, `{"model":"gp`, summary.Request.Body)
	assert.True(t, summary.Request.BodyTruncated)
	assert.True(t, summary.Response.BodyTruncated)
	assert.NotEmpty(t, summary.Response.BodySHA256)
	assert.Len(t, summary.Events, 2)
}

func TestParsePreludeAcceptsLegacyFileMagic(t *testing.T) {
	header := RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: MetaData{
			RequestID:  "req-legacy",
			Time:       time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC),
			Model:      "gpt-4.1",
			URL:        "/v1/chat/completions",
			Method:     "POST",
			StatusCode: 200,
		},
	}

	current, err := MarshalPrelude(header, BuildEvents(header))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(current), FileMagic))

	// A cassette recorded before the rename carries the legacy magic.
	legacy := append([]byte(LegacyFileMagic), current[len(FileMagic):]...)
	legacy = append(legacy, []byte("REQUEST\nRESPONSE")...)

	parsed, err := ParsePrelude(legacy)
	require.NoError(t, err)

	assert.Equal(t, header, parsed.Header)
	assert.Len(t, parsed.Events, 2)
	assert.Equal(t, int64(len(legacy))-int64(len("REQUEST\nRESPONSE")), parsed.PayloadOffset)
}

func TestFileMagicHelpers(t *testing.T) {
	current := []byte(FileMagic + "\n# meta: {}\n\nREQUEST")
	legacy := []byte(LegacyFileMagic + "\n# meta: {}\n\nREQUEST")
	v2 := []byte(`{"version":"LLM_PROXY_V2"}` + "\n")

	assert.True(t, HasFileMagic(current))
	assert.True(t, HasFileMagic(legacy))
	assert.False(t, HasFileMagic(v2))

	assert.True(t, IsV3Prelude(current))
	assert.True(t, IsV3Prelude(legacy))
	assert.False(t, IsV3Prelude(v2))
	// Magic without the terminating newline is not yet a complete prelude.
	assert.False(t, IsV3Prelude([]byte(FileMagic)))
	assert.True(t, HasFileMagic([]byte(FileMagic)))
}

// TestReadPreludeFileReadsOnlyThePrelude is the point of the function: a caller that
// wants the metadata must not pay for the recording. The body here is far larger
// than the prelude, and the assertion is on the allocation, so a reader that went
// back to reading the whole file fails it.
func TestReadPreludeFileReadsOnlyThePrelude(t *testing.T) {
	const bodySize = 8 << 20

	header := RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: MetaData{
			RequestID:  "req-prelude-only",
			Time:       time.Date(2026, 6, 22, 11, 0, 0, 0, time.UTC),
			Model:      "gpt-5",
			URL:        "/v1/chat/completions",
			Method:     "POST",
			StatusCode: 200,
		},
		Layout: LayoutInfo{ReqHeaderLen: 10, ReqBodyLen: 20, ResHeaderLen: 10, ResBodyLen: bodySize},
	}
	prelude, err := MarshalPrelude(header, BuildEvents(header))
	require.NoError(t, err)

	dir := t.TempDir()
	path := dir + "/cassette.http"
	body := bytes.Repeat([]byte("r"), bodySize)
	require.NoError(t, os.WriteFile(path, append(append([]byte{}, prelude...), body...), 0o644))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	parsed, err := ReadPreludeFile(path)
	require.NoError(t, err)
	runtime.ReadMemStats(&after)

	want, err := ParsePrelude(prelude)
	require.NoError(t, err)
	assert.Equal(t, want.Header.Meta.RequestID, parsed.Header.Meta.RequestID)
	assert.Equal(t, want.PayloadOffset, parsed.PayloadOffset)
	assert.Equal(t, len(want.Events), len(parsed.Events))

	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > bodySize/8 {
		t.Fatalf("ReadPreludeFile allocated %.1f MiB for a %d MiB recording, want it to read only the prelude",
			float64(allocated)/(1<<20), int64(bodySize)>>20)
	}
}

// TestReadPreludeFileRejectsAnUnterminatedPrelude covers the reason the function
// looks for the terminator itself instead of relying on ParsePrelude succeeding: a
// file cut at a line boundary inside its prelude parses without error and reports a
// short payload offset, which would misplace every section read from it afterwards.
func TestReadPreludeFileRejectsAnUnterminatedPrelude(t *testing.T) {
	header := RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta:    MetaData{RequestID: "req-truncated", Time: time.Now().UTC(), Model: "gpt-5"},
	}
	prelude, err := MarshalPrelude(header, BuildEvents(header))
	require.NoError(t, err)

	// Cut at a line boundary: drop the terminating blank line and everything after.
	lines := strings.SplitAfter(string(prelude), "\n")
	require.Greater(t, len(lines), 2)
	truncated := strings.Join(lines[:len(lines)-2], "")
	require.False(t, strings.HasSuffix(truncated, "\n\n"))

	path := t.TempDir() + "/truncated.http"
	require.NoError(t, os.WriteFile(path, []byte(truncated), 0o644))

	// ParsePrelude accepts it, which is exactly the hazard.
	accepted, err := ParsePrelude([]byte(truncated))
	require.NoError(t, err, "ParsePrelude is expected to accept a line-aligned truncation")
	require.Less(t, accepted.PayloadOffset, int64(len(prelude)), "its payload offset is short, hence unusable")

	if _, err := ReadPreludeFile(path); err == nil {
		t.Fatal("ReadPreludeFile accepted a cassette whose prelude has no terminator")
	}
}

func TestReadPreludeFileReadsAPreludeLargerThanOneChunk(t *testing.T) {
	header := RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta:    MetaData{RequestID: "req-many-events", Time: time.Now().UTC(), Model: "gpt-5"},
	}
	events := make([]RecordEvent, 0, 4000)
	for i := 0; i < 4000; i++ {
		events = append(events, RecordEvent{
			Type:    "routing.candidates",
			Time:    header.Meta.Time,
			Message: strings.Repeat("x", 40),
		})
	}
	prelude, err := MarshalPrelude(header, events)
	require.NoError(t, err)
	require.Greater(t, len(prelude), preludeReadChunk, "the prelude must span several read chunks")

	body := bytes.Repeat([]byte("r"), 1024)
	path := t.TempDir() + "/multi-chunk.http"
	require.NoError(t, os.WriteFile(path, append(append([]byte{}, prelude...), body...), 0o644))

	parsed, err := ReadPreludeFile(path)
	require.NoError(t, err)
	assert.Equal(t, "req-many-events", parsed.Header.Meta.RequestID)
	assert.Len(t, parsed.Events, len(events))
	assert.Equal(t, int64(len(prelude)), parsed.PayloadOffset)
}

func TestReadPreludeFileReadsALegacyHeaderBlock(t *testing.T) {
	header := RecordHeader{
		Version: "LLM_PROXY_V2",
		Meta:    MetaData{RequestID: "req-legacy", Time: time.Now().UTC(), Model: "gpt-4"},
	}
	blob, err := json.Marshal(header)
	require.NoError(t, err)
	require.Less(t, len(blob), LegacyHeaderLen)

	record := []byte("POST /v1/chat/completions HTTP/1.1\r\n\r\n{}")
	headerBlock := make([]byte, LegacyHeaderLen)
	copy(headerBlock, blob)
	headerBlock[len(blob)] = '\n' // the legacy header block is the JSON line, padded
	path := t.TempDir() + "/legacy.http"
	require.NoError(t, os.WriteFile(path, append(headerBlock, record...), 0o644))

	parsed, err := ReadPreludeFile(path)
	require.NoError(t, err)
	assert.Equal(t, "req-legacy", parsed.Header.Meta.RequestID)
	assert.Equal(t, int64(LegacyHeaderLen), parsed.PayloadOffset)
}

// TestReadPreludeFileMarksUnusablePreludesTypesTheContractTheCallersRelyOn. The
// indexers skip a recording whose prelude is not usable yet instead of failing their
// walk, and they decide that with errors.Is rather than by matching error text. A
// malformed legacy header block is deliberately not marked: that is a corrupt
// recording, and the indexer must report it rather than skip it silently.
func TestReadPreludeFileMarksUnusablePreludes(t *testing.T) {
	header := RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta:    MetaData{RequestID: "req-unusable", Time: time.Now().UTC(), Model: "gpt-5"},
	}
	prelude, err := MarshalPrelude(header, BuildEvents(header))
	require.NoError(t, err)

	lines := strings.SplitAfter(string(prelude), "\n")
	require.Greater(t, len(lines), 3)
	unterminated := strings.Join(lines[:len(lines)-2], "")
	require.False(t, strings.HasSuffix(unterminated, "\n\n"))

	legacyBlock := make([]byte, LegacyHeaderLen)
	copy(legacyBlock, `{"version":"LLM_PROXY_V2"`)
	legacyBlock[len(`{"version":"LLM_PROXY_V2"`)] = '\n'

	cases := []struct {
		name     string
		content  []byte
		unusable bool
	}{
		{name: "empty file", content: nil, unusable: true},
		// A blank file carries no prelude magic at all, so it is not this sentinel's
		// business; the indexers recognise it as a fragment by inspecting the file.
		{name: "blank file", content: []byte("\n\n"), unusable: false},
		{name: "unterminated v3 prelude", content: []byte(unterminated), unusable: true},
		{name: "magic without a meta line", content: []byte(FileMagic + "\n"), unusable: true},
		{name: "legacy block cut short", content: []byte(`{"version":"LLM_PROXY_V2"}`), unusable: false},
		{name: "malformed legacy header block", content: legacyBlock, unusable: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cassette.http")
			require.NoError(t, os.WriteFile(path, tc.content, 0o644))

			_, err := ReadPreludeFile(path)
			require.Error(t, err)
			assert.Equal(t, tc.unusable, errors.Is(err, ErrUnusablePrelude), "error = %v", err)
		})
	}
}
