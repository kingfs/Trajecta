package gateway

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

func reasoningDoneFrame(id, encrypted string) string {
	return "event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"` + id + `","type":"reasoning","encrypted_content":"` + encrypted + `","summary":[]}}` + "\n\n"
}

func reasoningCompletedFrame(items string) string {
	return "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"output":[` + items + `]}}` + "\n\n"
}

func textDeltaFrame(delta string) string {
	return "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"` + delta + `"}` + "\n\n"
}

func splitSSEFrames(t *testing.T, body string) []sseFrame {
	t.Helper()
	body = strings.ReplaceAll(body, "\r\n", "\n")
	parts := strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n")
	frames := make([]sseFrame, 0, len(parts))
	for _, part := range parts {
		frame, hasData := parseSSEFrame([]byte(part))
		if !hasData {
			t.Fatalf("frame has no data: %q", part)
		}
		frames = append(frames, frame)
	}
	return frames
}

func encryptedContentOf(t *testing.T, frame sseFrame) string {
	t.Helper()
	root, err := wire.Parse(frame.data)
	if err != nil {
		t.Fatalf("parse frame data: %v\n%s", err, frame.data)
	}
	item, ok := root.Get("item")
	if !ok {
		t.Fatalf("frame data has no item: %s", frame.data)
	}
	value, _ := item.StringField("encrypted_content")
	return value
}

func TestReasoningSignatureNormalizerUsesTerminalEncryptedContent(t *testing.T) {
	input := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n\n" +
		reasoningDoneFrame("rs_1", "DONE-SESSION-BOUND") +
		textDeltaFrame("hi") +
		reasoningCompletedFrame(`{"id":"rs_1","type":"reasoning","encrypted_content":"COMPLETED-CANONICAL","summary":[]}`)

	var events []recorder.RecordEvent
	normalizer := NewReasoningSignatureNormalizer(io.NopCloser(strings.NewReader(input)), &events, nil, "")
	body, err := io.ReadAll(normalizer)
	if err != nil {
		t.Fatal(err)
	}
	frames := splitSSEFrames(t, string(body))
	if len(frames) != 4 {
		t.Fatalf("frames = %d, want 4\n%s", len(frames), body)
	}
	if frames[0].event != "response.created" || frames[1].event != "response.output_text.delta" {
		t.Fatalf("unexpected leading frames: %+v", frames)
	}
	if frames[2].event != "response.output_item.done" || frames[3].event != "response.completed" {
		t.Fatalf("reasoning done must be emitted immediately before completed: %+v", frames)
	}
	if got := encryptedContentOf(t, frames[2]); got != "COMPLETED-CANONICAL" {
		t.Fatalf("done encrypted_content = %q, want the terminal value", got)
	}
	if len(events) != 1 || events[0].Type != "reasoning.signature_normalized" {
		t.Fatalf("events = %+v, want one reasoning.signature_normalized", events)
	}
	if events[0].Attributes["item_id"] != "rs_1" || events[0].Attributes["terminal_event"] != "response.completed" {
		t.Fatalf("event attributes = %+v", events[0].Attributes)
	}
}

func TestReasoningSignatureNormalizerLeavesIdenticalValueUnchanged(t *testing.T) {
	input := reasoningDoneFrame("rs_1", "SAME") +
		reasoningCompletedFrame(`{"id":"rs_1","type":"reasoning","encrypted_content":"SAME","summary":[]}`)

	var events []recorder.RecordEvent
	normalizer := NewReasoningSignatureNormalizer(io.NopCloser(strings.NewReader(input)), &events, nil, "")
	body, err := io.ReadAll(normalizer)
	if err != nil {
		t.Fatal(err)
	}
	frames := splitSSEFrames(t, string(body))
	if got := encryptedContentOf(t, frames[0]); got != "SAME" {
		t.Fatalf("encrypted_content = %q", got)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v, want none", events)
	}
}

func TestReasoningSignatureNormalizerFlushesHeldFrameWithoutTerminal(t *testing.T) {
	done := reasoningDoneFrame("rs_1", "DONE")
	input := done + textDeltaFrame("hi")
	normalizer := NewReasoningSignatureNormalizer(io.NopCloser(strings.NewReader(input)), nil, nil, "")
	body, err := io.ReadAll(normalizer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"encrypted_content":"DONE"`) {
		t.Fatalf("held frame was lost:\n%s", body)
	}
	if strings.Index(string(body), "response.output_text.delta") > strings.Index(string(body), "response.output_item.done") {
		t.Fatalf("held frame must be flushed after the later frames at EOF:\n%s", body)
	}
}

func TestReasoningSignatureNormalizerFlushesBeforeFailedEvent(t *testing.T) {
	input := reasoningDoneFrame("rs_1", "DONE") +
		"event: response.failed\n" +
		`data: {"type":"response.failed","response":{"status":"failed"}}` + "\n\n"
	normalizer := NewReasoningSignatureNormalizer(io.NopCloser(strings.NewReader(input)), nil, nil, "")
	body, err := io.ReadAll(normalizer)
	if err != nil {
		t.Fatal(err)
	}
	done := strings.Index(string(body), "response.output_item.done")
	failed := strings.Index(string(body), "response.failed")
	if done < 0 || failed < 0 || done > failed {
		t.Fatalf("done must be flushed before the terminal failure:\n%s", body)
	}
	if !strings.Contains(string(body), `"encrypted_content":"DONE"`) {
		t.Fatalf("held frame was rewritten without a canonical value:\n%s", body)
	}
}

func TestReasoningSignatureNormalizerHandlesChunkedAndCRLFStreams(t *testing.T) {
	input := "event: response.created\r\n" +
		`data: {"type":"response.created"}` + "\r\n\r\n" +
		reasoningDoneFrame("rs_1", "DONE") +
		reasoningCompletedFrame(`{"id":"rs_1","type":"reasoning","encrypted_content":"COMPLETED","summary":[]}`)
	normalizer := NewReasoningSignatureNormalizer(io.NopCloser(iotest.OneByteReader(strings.NewReader(input))), nil, nil, "")
	body, err := io.ReadAll(normalizer)
	if err != nil {
		t.Fatal(err)
	}
	frames := splitSSEFrames(t, string(body))
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3\n%s", len(frames), body)
	}
	if got := encryptedContentOf(t, frames[1]); got != "COMPLETED" {
		t.Fatalf("encrypted_content = %q, want COMPLETED", got)
	}
}

func TestReasoningSignatureNormalizerNormalizesEveryHeldItem(t *testing.T) {
	input := reasoningDoneFrame("rs_1", "ONE") +
		reasoningDoneFrame("rs_2", "TWO") +
		reasoningCompletedFrame(`{"id":"rs_1","type":"reasoning","encrypted_content":"ONE-CANONICAL","summary":[]},{"id":"rs_2","type":"reasoning","encrypted_content":"TWO-CANONICAL","summary":[]}`)
	var events []recorder.RecordEvent
	normalizer := NewReasoningSignatureNormalizer(io.NopCloser(strings.NewReader(input)), &events, nil, "")
	body, err := io.ReadAll(normalizer)
	if err != nil {
		t.Fatal(err)
	}
	frames := splitSSEFrames(t, string(body))
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3", len(frames))
	}
	if got := encryptedContentOf(t, frames[0]); got != "ONE-CANONICAL" {
		t.Fatalf("first encrypted_content = %q", got)
	}
	if got := encryptedContentOf(t, frames[1]); got != "TWO-CANONICAL" {
		t.Fatalf("second encrypted_content = %q", got)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
}

func TestReasoningSignatureNormalizerDoesNotHoldNonReasoningDone(t *testing.T) {
	functionDone := "event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"}}` + "\n\n"
	input := functionDone + textDeltaFrame("after")
	normalizer := NewReasoningSignatureNormalizer(io.NopCloser(strings.NewReader(input)), nil, nil, "")
	body, err := io.ReadAll(normalizer)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(body), "function_call") > strings.Index(string(body), "output_text.delta") {
		t.Fatalf("non-reasoning done must not be delayed:\n%s", body)
	}
}

func TestResponsePipelineNormalizesReasoningSignatures(t *testing.T) {
	input := reasoningDoneFrame("rs_1", "DONE") +
		reasoningCompletedFrame(`{"id":"rs_1","type":"reasoning","encrypted_content":"COMPLETED","summary":[]}`)
	var written bytes.Buffer
	var events []recorder.RecordEvent
	pipeline := NewResponsePipeline(ResponsePipelineOptions{
		Upstream:                     io.NopCloser(strings.NewReader(input)),
		File:                         &written,
		Events:                       &events,
		NormalizeReasoningSignatures: true,
	})
	body, err := io.ReadAll(pipeline.Reader())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"encrypted_content":"COMPLETED"`) {
		t.Fatalf("client stream was not normalized:\n%s", body)
	}
	if written.String() != string(body) {
		t.Fatalf("cassette bytes differ from the client stream")
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
}

func TestResponsePipelineLeavesReasoningSignaturesWhenDisabled(t *testing.T) {
	input := reasoningDoneFrame("rs_1", "DONE") +
		reasoningCompletedFrame(`{"id":"rs_1","type":"reasoning","encrypted_content":"COMPLETED","summary":[]}`)
	pipeline := NewResponsePipeline(ResponsePipelineOptions{
		Upstream: io.NopCloser(strings.NewReader(input)),
	})
	body, err := io.ReadAll(pipeline.Reader())
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != input {
		t.Fatalf("bytes changed with normalization disabled")
	}
}

func TestReasoningSignaturePolicyHoldsFirstResponseThenPassesConsistentUpstream(t *testing.T) {
	policy := NewReasoningSignaturePolicy()
	input := reasoningDoneFrame("rs_1", "SAME") +
		textDeltaFrame("hi") +
		reasoningCompletedFrame(`{"id":"rs_1","type":"reasoning","encrypted_content":"SAME","summary":[]}`)

	first := NewReasoningSignatureNormalizer(io.NopCloser(strings.NewReader(input)), nil, policy, "upstream-1")
	body, err := io.ReadAll(first)
	if err != nil {
		t.Fatal(err)
	}
	firstFrames := splitSSEFrames(t, string(body))
	if firstFrames[0].event != "response.output_text.delta" || firstFrames[1].event != "response.output_item.done" {
		t.Fatalf("first response from an unknown upstream must be held: %+v", firstFrames)
	}
	if policy.shouldHold("upstream-1") {
		t.Fatal("a clean first response must mark the upstream consistent")
	}

	second := NewReasoningSignatureNormalizer(io.NopCloser(strings.NewReader(input)), nil, policy, "upstream-1")
	body, err = io.ReadAll(second)
	if err != nil {
		t.Fatal(err)
	}
	secondFrames := splitSSEFrames(t, string(body))
	if secondFrames[0].event != "response.output_item.done" || secondFrames[1].event != "response.output_text.delta" {
		t.Fatalf("a consistent upstream must stream in order: %+v", secondFrames)
	}
}

func TestReasoningSignaturePolicyStaysHeldForInconsistentUpstream(t *testing.T) {
	policy := NewReasoningSignaturePolicy()
	input := reasoningDoneFrame("rs_1", "DONE") +
		reasoningCompletedFrame(`{"id":"rs_1","type":"reasoning","encrypted_content":"COMPLETED","summary":[]}`)

	first := NewReasoningSignatureNormalizer(io.NopCloser(strings.NewReader(input)), nil, policy, "upstream-2")
	if _, err := io.ReadAll(first); err != nil {
		t.Fatal(err)
	}
	if !policy.shouldHold("upstream-2") {
		t.Fatal("a mismatch must keep the upstream on the normalized path")
	}
	second := NewReasoningSignatureNormalizer(io.NopCloser(strings.NewReader(input)), nil, policy, "upstream-2")
	body, err := io.ReadAll(second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"encrypted_content":"COMPLETED"`) {
		t.Fatalf("the second response was not normalized:\n%s", body)
	}
}

func TestReasoningSignaturePolicyObservesMismatchWhenPassingThrough(t *testing.T) {
	policy := NewReasoningSignaturePolicy()
	policy.observe("upstream-3", true, false)
	if policy.shouldHold("upstream-3") {
		t.Fatal("test setup: upstream must start consistent")
	}
	input := reasoningDoneFrame("rs_1", "DONE") +
		reasoningCompletedFrame(`{"id":"rs_1","type":"reasoning","encrypted_content":"COMPLETED","summary":[]}`)
	var events []recorder.RecordEvent
	normalizer := NewReasoningSignatureNormalizer(io.NopCloser(strings.NewReader(input)), &events, policy, "upstream-3")
	body, err := io.ReadAll(normalizer)
	if err != nil {
		t.Fatal(err)
	}
	frames := splitSSEFrames(t, string(body))
	if got := encryptedContentOf(t, frames[0]); got != "DONE" {
		t.Fatalf("a consistent upstream must not be rewritten in this response: %q", got)
	}
	if len(events) != 1 || events[0].Type != "reasoning.signature_mismatch" {
		t.Fatalf("events = %+v, want one reasoning.signature_mismatch", events)
	}
	if !policy.shouldHold("upstream-3") {
		t.Fatal("an observed mismatch must flip the upstream to the normalized path")
	}
}
