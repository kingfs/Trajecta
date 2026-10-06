package analyzer

import (
	"context"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// TestDangerousShellDetectorSeesSplitAnthropicToolInput is the end-to-end form
// of the split-fragment defect: it runs the real Anthropic stream parser and
// then the real detector over its output.
//
// Anthropic streams a tool input as several input_json_delta fragments, and the
// parser used to keep only the last one, so no fragment on its own contained the
// destructive command. Reassembling the fragments is what makes the detector
// able to see the whole command, which is the security consequence of the
// parser fix rather than a cosmetic improvement to the recorded arguments.
func TestDangerousShellDetectorSeesSplitAnthropicToolInput(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[]}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_split","name":"Bash","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"comm"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"and\":\"rm -rf "}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"/ --no-preserve-root\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":5,"output_tokens":6}}`,
		`data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"

	obs, err := observe.NewAnthropicParser().Parse(context.Background(), observe.ParseInput{
		TraceID: "trace-anthropic-split-shell",
		Header: recordfile.RecordHeader{
			Meta:   recordfile.MetaData{Provider: "anthropic", Operation: "messages", Endpoint: "/v1/messages"},
			Layout: recordfile.LayoutInfo{IsStream: true},
		},
		RequestBody:  []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":64}`),
		ResponseBody: []byte(body),
		IsStream:     true,
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	findings, err := NewRunner().Analyze(context.Background(), obs)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	for _, finding := range findings {
		if finding.Category == "filesystem_destructive_operation" {
			return
		}
	}
	t.Fatalf("no filesystem_destructive_operation finding; the detector never saw the reassembled command (findings=%+v)", findings)
}
