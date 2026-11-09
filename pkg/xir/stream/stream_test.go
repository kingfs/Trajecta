package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
)

func collectFrames(t *testing.T, input string, opts ...Option) []Frame {
	t.Helper()
	dec := NewDecoder(strings.NewReader(input), opts...)
	t.Cleanup(func() {
		if err := dec.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	var frames []Frame
	for {
		frame, err := dec.Next()
		if errors.Is(err, io.EOF) {
			return frames
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		frames = append(frames, frame)
	}
}

func assertFrames(t *testing.T, got, want []Frame) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("frame count = %d, want %d\ngot: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Event != want[i].Event {
			t.Errorf("frame %d Event = %q, want %q", i, got[i].Event, want[i].Event)
		}
		if got[i].ID != want[i].ID {
			t.Errorf("frame %d ID = %q, want %q", i, got[i].ID, want[i].ID)
		}
		if got[i].Retry != want[i].Retry {
			t.Errorf("frame %d Retry = %d, want %d", i, got[i].Retry, want[i].Retry)
		}
		if !bytes.Equal(got[i].Data, want[i].Data) {
			t.Errorf("frame %d Data = %q, want %q", i, got[i].Data, want[i].Data)
		}
	}
}

func TestDecoderFrames(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []Frame
	}{
		{
			name:  "lf",
			input: "data: hello\n\n",
			want:  []Frame{{Data: []byte("hello")}},
		},
		{
			name:  "crlf",
			input: "event: message\r\nid: 42\r\ndata: hello\r\n\r\n",
			want:  []Frame{{Event: "message", ID: "42", Data: []byte("hello")}},
		},
		{
			name:  "multiline data",
			input: "data: first\ndata: second\ndata: third\n\n",
			want:  []Frame{{Data: []byte("first\nsecond\nthird")}},
		},
		{
			name:  "multi frame crlf",
			input: "data: one\r\n\r\ndata: two\r\n\r\n",
			want:  []Frame{{Data: []byte("one")}, {Data: []byte("two")}},
		},
		{
			name:  "comments and empty lines",
			input: ": keep-alive\n\n\n: another comment\ndata: x\n\n",
			want:  []Frame{{Data: []byte("x")}},
		},
		{
			name:  "done is an ordinary payload",
			input: "data: [DONE]\n\n",
			want:  []Frame{{Data: []byte("[DONE]")}},
		},
		{
			name:  "no space after colon",
			input: "data:no-space\n\n",
			want:  []Frame{{Data: []byte("no-space")}},
		},
		{
			name:  "one optional space is stripped, the rest is data",
			input: "data:   padded\n\n",
			want:  []Frame{{Data: []byte("  padded")}},
		},
		{
			name:  "event id retry",
			input: "event: delta\nid: 7\nretry: 2500\ndata: x\n\n",
			want:  []Frame{{Event: "delta", ID: "7", Retry: 2500, Data: []byte("x")}},
		},
		{
			name:  "invalid retry is ignored",
			input: "retry: 12x\ndata: x\n\n",
			want:  []Frame{{Data: []byte("x")}},
		},
		{
			name:  "retry tolerates repeated spaces",
			input: "retry:  2500\ndata: x\n\n",
			want:  []Frame{{Retry: 2500, Data: []byte("x")}},
		},
		{
			name:  "eof terminates a final frame without a blank line",
			input: "data: tail",
			want:  []Frame{{Data: []byte("tail")}},
		},
		{
			name:  "eof after a final newline",
			input: "event: end\ndata: tail\n",
			want:  []Frame{{Event: "end", Data: []byte("tail")}},
		},
		{
			name:  "event without data",
			input: "event: ping\n\n",
			want:  []Frame{{Event: "ping"}},
		},
		{
			name:  "empty data field",
			input: "data:\n\n",
			want:  []Frame{{Data: []byte{}}},
		},
		{
			name:  "unknown fields are ignored",
			input: "foo: bar\ndata: x\n\n",
			want:  []Frame{{Data: []byte("x")}},
		},
		{
			name:  "colon inside data",
			input: "data: a:b:c\n\n",
			want:  []Frame{{Data: []byte("a:b:c")}},
		},
		{
			name:  "nul in id is ignored",
			input: "id: a\x00b\ndata: x\n\n",
			want:  []Frame{{Data: []byte("x")}},
		},
		{
			name:  "only comments",
			input: ": one\n: two\n\n",
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertFrames(t, collectFrames(t, tt.input), tt.want)
		})
	}
}

func TestDecoderLongLineAcrossFragments(t *testing.T) {
	payload := strings.Repeat("x", 1000)
	dec := NewDecoder(strings.NewReader("data: "+payload+"\n\n"),
		WithMaxLineBytes(4096), WithInitialBuffer(16))
	frame, err := dec.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(frame.Data) != payload {
		t.Fatalf("data length = %d, want %d", len(frame.Data), len(payload))
	}
	if _, err := dec.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after frame = %v, want EOF", err)
	}
}

func TestDecoderMaxLineBytes(t *testing.T) {
	dec := NewDecoder(strings.NewReader("data: 0123456789\n\n"), WithMaxLineBytes(8))
	if _, err := dec.Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("Next error = %v, want ErrLineTooLong", err)
	}
	// The framing error is terminal.
	if _, err := dec.Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("second Next error = %v, want ErrLineTooLong", err)
	}
}

func TestDecoderMaxLineBytesExcludesCRLF(t *testing.T) {
	const line = "data: x" // exactly the configured limit
	dec := NewDecoder(strings.NewReader(line+"\r\n\r\n"), WithMaxLineBytes(len(line)))
	frame, err := dec.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(frame.Data) != "x" {
		t.Fatalf("Data = %q, want x", frame.Data)
	}
}

func TestDecoderNilReader(t *testing.T) {
	dec := NewDecoder(nil)
	if _, err := dec.Next(); !errors.Is(err, ErrNilReader) {
		t.Fatalf("Next error = %v, want ErrNilReader", err)
	}
	if err := dec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

type trackingCloser struct {
	io.Reader
	closes int
	err    error
}

func (c *trackingCloser) Close() error {
	c.closes++
	return c.err
}

func TestDecoderCloseClosesReaderOnce(t *testing.T) {
	reader := &trackingCloser{Reader: strings.NewReader("data: x\n\n")}
	dec := NewDecoder(reader)
	if err := dec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := dec.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if reader.closes != 1 {
		t.Fatalf("closes = %d, want 1", reader.closes)
	}
	if _, err := dec.Next(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Next after Close = %v, want ErrClosed", err)
	}
}

func TestDecoderClosePropagatesError(t *testing.T) {
	sentinel := errors.New("close failed")
	dec := NewDecoder(&trackingCloser{Reader: strings.NewReader(""), err: sentinel})
	if err := dec.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("Close error = %v, want %v", err, sentinel)
	}
}

type flushBuffer struct {
	bytes.Buffer
	flushes  int
	flushErr error
}

func (w *flushBuffer) Flush() error {
	w.flushes++
	return w.flushErr
}

func TestEncoderWritesFrame(t *testing.T) {
	var buf flushBuffer
	enc := NewEncoder(&buf)
	frame := Frame{Event: "message", ID: "42", Retry: 2500, Data: []byte("first\nsecond")}
	if err := enc.WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	want := "event: message\nid: 42\nretry: 2500\ndata: first\ndata: second\n\n"
	if buf.String() != want {
		t.Fatalf("encoded:\n%q\nwant:\n%q", buf.String(), want)
	}
	if buf.flushes != 1 {
		t.Fatalf("flushes = %d, want 1", buf.flushes)
	}
	if err := enc.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if buf.flushes != 2 {
		t.Fatalf("flushes after explicit Flush = %d, want 2", buf.flushes)
	}
}

func TestEncoderRoundTrip(t *testing.T) {
	frames := []Frame{
		{Data: []byte("hello")},
		{Event: "message", ID: "42", Retry: 1000, Data: []byte("a\nb")},
		{Data: []byte(" leading space")},
		{Data: []byte("[DONE]")},
		{Data: []byte("a\n\nb")},
		{Event: "ping", Data: []byte{}},
		{Data: []byte("")},
	}
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	for _, frame := range frames {
		if err := enc.WriteFrame(frame); err != nil {
			t.Fatalf("WriteFrame(%#v): %v", frame, err)
		}
	}

	dec := NewDecoder(strings.NewReader(buf.String()))
	var got []Frame
	for {
		frame, err := dec.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, frame)
	}
	assertFrames(t, got, frames)
}

func TestEncoderRejectsLineBreakInField(t *testing.T) {
	enc := NewEncoder(io.Discard)
	if err := enc.WriteFrame(Frame{Event: "a\nb", Data: []byte("x")}); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("event error = %v, want ErrInvalidField", err)
	}
	if err := enc.WriteFrame(Frame{ID: "a\rb", Data: []byte("x")}); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("id error = %v, want ErrInvalidField", err)
	}
}

func TestEncoderNilWriter(t *testing.T) {
	var enc *Encoder
	if err := enc.WriteFrame(Frame{Data: []byte("x")}); !errors.Is(err, ErrNilWriter) {
		t.Fatalf("WriteFrame error = %v, want ErrNilWriter", err)
	}
	if err := enc.Flush(); !errors.Is(err, ErrNilWriter) {
		t.Fatalf("Flush error = %v, want ErrNilWriter", err)
	}
}

func TestEncoderFlushError(t *testing.T) {
	sentinel := errors.New("flush failed")
	var buf flushBuffer
	buf.flushErr = sentinel
	if err := NewEncoder(&buf).WriteFrame(Frame{Data: []byte("x")}); !errors.Is(err, sentinel) {
		t.Fatalf("WriteFrame error = %v, want %v", err, sentinel)
	}
}

func TestAssemblerTextAndIdentity(t *testing.T) {
	asm := NewAssembler(xir.ProtocolChatCompletions)
	asm.SetIdentity("resp_1", "gpt-test")
	if err := asm.Apply(&xir.StreamEvent{Kind: xir.StreamStart}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := asm.Apply(&xir.StreamEvent{Kind: xir.StreamText, Text: "Hel"}); err != nil {
		t.Fatalf("text 1: %v", err)
	}
	if err := asm.Apply(&xir.StreamEvent{Kind: xir.StreamText, Text: "lo"}); err != nil {
		t.Fatalf("text 2: %v", err)
	}
	if err := asm.Apply(&xir.StreamEvent{Kind: xir.StreamStop, StopReason: xir.FinishStop}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	resp := asm.Response()
	if resp == nil {
		t.Fatal("Response() = nil")
	}
	if resp.ID != "resp_1" || resp.Model != "gpt-test" {
		t.Fatalf("identity = (%q, %q), want (resp_1, gpt-test)", resp.ID, resp.Model)
	}
	if resp.Status != "completed" {
		t.Fatalf("Status = %q, want completed", resp.Status)
	}
	if resp.StopReason != xir.FinishStop {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, xir.FinishStop)
	}
	if len(resp.Parts) != 1 || resp.Parts[0].Kind != xir.PartText || resp.Parts[0].Text != "Hello" {
		t.Fatalf("Parts = %#v, want one coalesced text part", resp.Parts)
	}
}

func TestAssemblerStartCarriesIdentity(t *testing.T) {
	asm := NewAssembler(xir.ProtocolResponses)
	if err := asm.Apply(&xir.StreamEvent{
		Kind:     xir.StreamStart,
		ToolID:   "resp_2",
		ToolName: "gpt-start",
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	resp := asm.Response()
	if resp.ID != "resp_2" || resp.Model != "gpt-start" {
		t.Fatalf("identity = (%q, %q), want (resp_2, gpt-start)", resp.ID, resp.Model)
	}
	if resp.Status != "in_progress" {
		t.Fatalf("Status = %q, want in_progress", resp.Status)
	}
}

func TestAssemblerReasoning(t *testing.T) {
	asm := NewAssembler(xir.ProtocolMessages)
	if err := asm.Apply(&xir.StreamEvent{Kind: xir.StreamReasoning, Text: "thin"}); err != nil {
		t.Fatalf("reasoning 1: %v", err)
	}
	if err := asm.Apply(&xir.StreamEvent{
		Kind: xir.StreamReasoning,
		Reasoning: &xir.Reasoning{
			Text:      "king",
			Signature: "sig",
			Summary:   []string{"summary"},
		},
	}); err != nil {
		t.Fatalf("reasoning 2: %v", err)
	}
	if err := asm.Apply(&xir.StreamEvent{Kind: xir.StreamText, Text: "answer"}); err != nil {
		t.Fatalf("text: %v", err)
	}

	parts := asm.Response().Parts
	if len(parts) != 2 {
		t.Fatalf("Parts = %#v, want reasoning + text", parts)
	}
	if parts[0].Kind != xir.PartReasoning || parts[0].Reasoning == nil {
		t.Fatalf("part 0 = %#v, want reasoning", parts[0])
	}
	if parts[0].Reasoning.Text != "thinking" {
		t.Fatalf("reasoning text = %q, want thinking", parts[0].Reasoning.Text)
	}
	if parts[0].Reasoning.Signature != "sig" {
		t.Fatalf("reasoning signature = %q", parts[0].Reasoning.Signature)
	}
	if len(parts[0].Reasoning.Summary) != 1 || parts[0].Reasoning.Summary[0] != "summary" {
		t.Fatalf("reasoning summary = %#v", parts[0].Reasoning.Summary)
	}
	if parts[1].Kind != xir.PartText || parts[1].Text != "answer" {
		t.Fatalf("part 1 = %#v, want text answer", parts[1])
	}
}

func TestAssemblerToolCall(t *testing.T) {
	asm := NewAssembler(xir.ProtocolChatCompletions)
	events := []*xir.StreamEvent{
		{Kind: xir.StreamToolStart, ToolID: "call_1", ToolName: "lookup"},
		{Kind: xir.StreamToolArgs, Text: `{"q":`},
		{Kind: xir.StreamToolArgs, Text: `"paris"}`},
		{Kind: xir.StreamToolEnd, ToolID: "call_1"},
	}
	for i, ev := range events {
		if err := asm.Apply(ev); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}

	parts := asm.Response().Parts
	if len(parts) != 1 || parts[0].Kind != xir.PartToolCall || parts[0].ToolCall == nil {
		t.Fatalf("Parts = %#v, want one tool call", parts)
	}
	call := parts[0].ToolCall
	if call.ID != "call_1" || call.Name != "lookup" || call.Type != "function" {
		t.Fatalf("call identity = %#v", call)
	}
	if call.ArgsText != `{"q":"paris"}` {
		t.Fatalf("ArgsText = %q", call.ArgsText)
	}
	if !bytes.Equal(call.Args, []byte(`{"q":"paris"}`)) {
		t.Fatalf("Args = %q", call.Args)
	}
	if !json.Valid(call.Args) {
		t.Fatalf("Args is not valid JSON: %q", call.Args)
	}
}

func TestAssemblerMultipleToolCalls(t *testing.T) {
	asm := NewAssembler(xir.ProtocolChatCompletions)
	for _, ev := range []*xir.StreamEvent{
		{Kind: xir.StreamToolStart, ToolID: "call_1", ToolName: "first"},
		{Kind: xir.StreamToolArgs, Text: `{"a":1}`},
		{Kind: xir.StreamToolEnd, ToolID: "call_1"},
		{Kind: xir.StreamToolStart, ToolID: "call_2", ToolName: "second"},
		{Kind: xir.StreamToolArgs, Text: `{"b":2}`},
		{Kind: xir.StreamToolEnd, ToolID: "call_2"},
	} {
		if err := asm.Apply(ev); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	parts := asm.Response().Parts
	if len(parts) != 2 {
		t.Fatalf("Parts = %#v, want two tool calls", parts)
	}
	if parts[0].ToolCall.ID != "call_1" || parts[0].ToolCall.ArgsText != `{"a":1}` {
		t.Fatalf("first call = %#v", parts[0].ToolCall)
	}
	if parts[1].ToolCall.ID != "call_2" || parts[1].ToolCall.ArgsText != `{"b":2}` {
		t.Fatalf("second call = %#v", parts[1].ToolCall)
	}
}

func TestAssemblerToolArgsWithoutStart(t *testing.T) {
	asm := NewAssembler(xir.ProtocolChatCompletions)
	if err := asm.Apply(&xir.StreamEvent{
		Kind:     xir.StreamToolArgs,
		ToolID:   "call_1",
		ToolName: "lookup",
		Text:     `{}`,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	parts := asm.Response().Parts
	if len(parts) != 1 || parts[0].ToolCall == nil {
		t.Fatalf("Parts = %#v, want one synthesized tool call", parts)
	}
	call := parts[0].ToolCall
	if call.ID != "call_1" || call.Name != "lookup" || call.ArgsText != `{}` {
		t.Fatalf("call = %#v", call)
	}
}

func TestAssemblerUsageMerge(t *testing.T) {
	asm := NewAssembler(xir.ProtocolChatCompletions)
	if err := asm.Apply(&xir.StreamEvent{
		Kind:  xir.StreamUsage,
		Usage: &usage.Usage{InputTokens: 10, OutputTokens: 1},
	}); err != nil {
		t.Fatalf("usage 1: %v", err)
	}
	if err := asm.Apply(&xir.StreamEvent{
		Kind: xir.StreamUsage,
		Usage: &usage.Usage{
			InputTokens:     10,
			OutputTokens:    5,
			TotalTokens:     99,
			CacheReadTokens: 3,
		},
	}); err != nil {
		t.Fatalf("usage 2: %v", err)
	}

	got := asm.Response().Usage
	if got == nil {
		t.Fatal("Usage = nil")
	}
	want := usage.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CacheReadTokens: 3}
	if *got != want {
		t.Fatalf("Usage = %#v, want %#v", *got, want)
	}
}

func TestAssemblerStop(t *testing.T) {
	asm := NewAssembler(xir.ProtocolChatCompletions)
	if err := asm.Apply(&xir.StreamEvent{Kind: xir.StreamStop, StopReason: xir.FinishLength}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	resp := asm.Response()
	if resp.StopReason != xir.FinishLength {
		t.Fatalf("StopReason = %q, want length", resp.StopReason)
	}
	if resp.Status != "completed" {
		t.Fatalf("Status = %q, want completed", resp.Status)
	}
}

func TestAssemblerError(t *testing.T) {
	asm := NewAssembler(xir.ProtocolChatCompletions)
	if err := asm.Apply(&xir.StreamEvent{
		Kind: xir.StreamError,
		Error: &xir.Error{
			Status:  429,
			Type:    "rate_limit_error",
			Code:    "rate_limit",
			Message: "slow down",
			Raw:     json.RawMessage(`{"error":"rate_limit"}`),
		},
	}); err != nil {
		t.Fatalf("error event: %v", err)
	}
	resp := asm.Response()
	if resp.Status != "failed" {
		t.Fatalf("Status = %q, want failed", resp.Status)
	}
	if resp.StopReason != xir.FinishError {
		t.Fatalf("StopReason = %q, want error", resp.StopReason)
	}
	if resp.Error == nil || resp.Error.Status != 429 || resp.Error.Message != "slow down" {
		t.Fatalf("Error = %#v", resp.Error)
	}
}

func TestAssemblerSealed(t *testing.T) {
	asm := NewAssembler(xir.ProtocolResponses)
	sealed := &xir.Sealed{
		Kind: "reasoning",
		Raw:  json.RawMessage(`{"type":"reasoning"}`),
	}
	if err := asm.Apply(&xir.StreamEvent{
		Kind:     xir.StreamSealed,
		Protocol: xir.ProtocolMessages,
		Sealed:   sealed,
	}); err != nil {
		t.Fatalf("sealed: %v", err)
	}
	parts := asm.Response().Parts
	if len(parts) != 1 || parts[0].Kind != xir.PartSealed || parts[0].Sealed == nil {
		t.Fatalf("Parts = %#v, want one sealed part", parts)
	}
	if parts[0].Sealed.Protocol != xir.ProtocolMessages {
		t.Fatalf("Sealed.Protocol = %q, want messages", parts[0].Sealed.Protocol)
	}
	if !bytes.Equal(parts[0].Sealed.Raw, sealed.Raw) {
		t.Fatalf("Sealed.Raw = %q", parts[0].Sealed.Raw)
	}
}

func TestAssemblerIgnoresNilAndUnknown(t *testing.T) {
	asm := NewAssembler(xir.ProtocolChatCompletions)
	// Response is valid before any event.
	if resp := asm.Response(); resp == nil || len(resp.Parts) != 0 {
		t.Fatalf("empty Response = %#v", resp)
	}
	for _, ev := range []*xir.StreamEvent{
		nil,
		{Kind: xir.StreamPing},
		{Kind: xir.StreamUnknown, Text: "unknown"},
		{},
	} {
		if err := asm.Apply(ev); err != nil {
			t.Fatalf("Apply(%#v): %v", ev, err)
		}
	}
	if parts := asm.Response().Parts; len(parts) != 0 {
		t.Fatalf("Parts after ignored events = %#v", parts)
	}
}

func TestAssemblerNilReceiver(t *testing.T) {
	var asm *Assembler
	if err := asm.Apply(&xir.StreamEvent{Kind: xir.StreamText, Text: "x"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if resp := asm.Response(); resp == nil || len(resp.Parts) != 0 {
		t.Fatalf("Response = %#v", resp)
	}
}
