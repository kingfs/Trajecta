package trajectory

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamWritesOneJSONObjectPerLine(t *testing.T) {
	exchanges := []Exchange{
		exchange("t1", `{"input":"hello"}`, `{"id":"r1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"world"}]}]}`),
		exchange("t2", `{"input":"more"}`, `{"id":"r2","output":[{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]}`),
	}
	index := 0
	next := func() (Exchange, bool) {
		if index >= len(exchanges) {
			return Exchange{}, false
		}
		ex := exchanges[index]
		index++
		return ex, true
	}
	var buffer bytes.Buffer
	flushes := 0
	err := Stream(context.Background(), "session-1", "codex", StreamOptions{TraceCount: 2, IncludedTraces: 2}, next, &buffer, func() error {
		flushes++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("got %d records, want at least a header, a trace and a final record: %q", len(lines), buffer.String())
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d is not a JSON object: %v\n%s", i, err, line)
		}
		if record["type"] == nil {
			t.Fatalf("line %d has no type: %s", i, line)
		}
	}
	if flushes != len(lines) {
		t.Fatalf("flushes = %d, want one per record (%d)", flushes, len(lines))
	}

	var header StreamHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header.Type != StreamRecordHeader || header.SchemaVersion != SchemaVersion || header.SessionID != "session-1" || header.TraceCount != 2 || header.IncludedTraces != 2 {
		t.Fatalf("header = %+v", header)
	}

	traces, missing, final := 0, 0, false
	for _, line := range lines {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Fatal(err)
		}
		switch probe.Type {
		case StreamRecordTrace:
			traces++
		case StreamRecordMissing:
			missing++
		case StreamRecordFinal:
			final = true
		}
	}
	if traces != 2 || !final || missing != 1 {
		t.Fatalf("traces = %d, missing = %d, final = %v", traces, missing, final)
	}

	// The final record carries the same session extras the buffered response
	// exposes, including the truncation fields.
	var last StreamFinal
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatal(err)
	}
	if last.Type != StreamRecordFinal || last.FinalMetrics.TotalSteps == 0 || last.Extra["trace_count"] != float64(2) || last.Extra["truncated"] != false {
		t.Fatalf("final = %+v", last)
	}
}

func TestStreamCarriesLateToolResultAsItsOwnRecord(t *testing.T) {
	exchanges := []Exchange{
		exchange("t1", `{"input":"go"}`, `{"id":"r1","output":[{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]}`),
		exchange("t2", `{"input":[{"type":"function_call_output","call_id":"c1","output":"done"}],"previous_response_id":"r1"}`, `{"id":"r2","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`),
	}
	index := 0
	next := func() (Exchange, bool) {
		if index >= len(exchanges) {
			return Exchange{}, false
		}
		ex := exchanges[index]
		index++
		return ex, true
	}
	var buffer bytes.Buffer
	if err := Stream(context.Background(), "session-1", "codex", StreamOptions{TraceCount: 2, IncludedTraces: 2}, next, &buffer, nil); err != nil {
		t.Fatal(err)
	}
	var sawResult bool
	for _, line := range strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n") {
		var record StreamTrace
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Type != StreamRecordTrace {
			continue
		}
		for _, result := range record.Results {
			if result.SourceCallID == "c1" && result.Content == "done" && result.StepID > 0 {
				sawResult = true
			}
		}
	}
	if !sawResult {
		t.Fatalf("late tool result was not emitted as its own record: %s", buffer.String())
	}
}

func TestStreamStopsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reads := 0
	next := func() (Exchange, bool) {
		reads++
		return exchange("t1", `{"input":"hello"}`, `{}`), true
	}
	var buffer bytes.Buffer
	if err := Stream(ctx, "session-1", "", StreamOptions{}, next, &buffer, nil); err == nil {
		t.Fatal("Stream() error = nil, want context cancellation")
	}
	if reads != 0 {
		t.Fatalf("read %d exchanges after cancellation", reads)
	}
}
