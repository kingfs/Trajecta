package gateway

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

func TestResponsePipelineDoesNotHoldReasoningDone(t *testing.T) {
	done := "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"encrypted_content\":\"gAAA-done-n8I=\"}}\n\n"
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	var recorded bytes.Buffer
	pipeline := NewResponsePipeline(ResponsePipelineOptions{Upstream: reader, File: &recorded})
	defer pipeline.Reader().Close()
	go func() {
		_, _ = io.WriteString(writer, done)
	}()
	result := make(chan error, 1)
	got := make([]byte, len(done))
	go func() {
		_, err := io.ReadFull(pipeline.Reader(), got)
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		reader.Close()
		<-result
		t.Fatal("reasoning done was withheld while waiting for a terminal event")
	}
	if string(got) != done || recorded.String() != done {
		t.Fatal("reasoning done was modified")
	}
}

func TestRequestTransformPreservesOpaqueInputWithMasking(t *testing.T) {
	vault := secretmask.NewVault([]byte("reasoning-passthrough-test"))
	blob := "gAAA-sk-proj-" + strings.Repeat("A", 80) + "=="
	input := `{"model":"m","input":[{"id":"rs_1","type":"reasoning","encrypted_content":"` + blob + `"},{"type":"compaction","encrypted_content":"gAAA-compact-n8I="},{"type":"unknown","encrypted_content":"gAAA-other-Og=="},{"role":"user","content":"hi"}]}`
	for attempt := 0; attempt < 3; attempt++ {
		result, err := (RequestTransform{RequestedModel: "m", Masker: vault}).Apply([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if string(result.WireBody) != input || string(result.RecordBody) != input || result.Masked {
			t.Fatal("opaque input was rewritten")
		}
	}
}
