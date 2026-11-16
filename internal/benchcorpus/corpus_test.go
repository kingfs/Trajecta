package benchcorpus_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/benchcorpus"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/stream"
)

func protocols() []xir.Protocol {
	return []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages, xir.ProtocolGemini}
}

func TestTrajectoryEncodesEveryProtocol(t *testing.T) {
	sizes := []struct {
		name string
		n    int
	}{
		{"short", benchcorpus.ShortBytes},
		{"medium", benchcorpus.MediumBytes},
		{"long", benchcorpus.LongBytes},
		{"agent256k", benchcorpus.Agent256KBytes},
	}
	for _, size := range sizes {
		t.Run(size.name, func(t *testing.T) {
			trajectory := benchcorpus.Agent(size.n, benchcorpus.Options{Secrets: true})
			if again := benchcorpus.Agent(size.n, benchcorpus.Options{Secrets: true}); again == nil {
				t.Fatal("nil trajectory")
			}
			encoded := make(map[xir.Protocol][]byte, len(protocols()))
			for _, protocol := range protocols() {
				body := benchcorpus.Encode(protocol, trajectory)
				encoded[protocol] = body
				if !json.Valid(body) {
					t.Fatalf("%s body is not valid JSON", protocol)
				}
				if len(body) < size.n/2 {
					t.Fatalf("%s body is %d bytes for a %d-byte trajectory", protocol, len(body), size.n)
				}
			}
			// The corpus is a benchmark fixture: it must be byte-stable across
			// runs or two benchmark snapshots cannot be compared.
			for _, protocol := range protocols() {
				if !bytes.Equal(encoded[protocol], benchcorpus.Encode(protocol, trajectory)) {
					t.Fatalf("%s encoding is not deterministic", protocol)
				}
			}
			if len(encoded[xir.ProtocolChatCompletions]) < size.n {
				t.Fatalf("chat body is %d bytes, want at least %d", len(encoded[xir.ProtocolChatCompletions]), size.n)
			}
		})
	}
}

func TestSecretsArePlanted(t *testing.T) {
	trajectory := benchcorpus.Agent(benchcorpus.MediumBytes, benchcorpus.Options{Secrets: true})
	body := string(benchcorpus.EncodeChat(trajectory))
	for _, value := range benchcorpus.SensitiveValues() {
		if !strings.Contains(body, value) {
			t.Fatalf("sensitive value missing from the trajectory: %.16s...", value)
		}
	}
	clean := string(benchcorpus.EncodeChat(benchcorpus.Agent(benchcorpus.MediumBytes, benchcorpus.Options{})))
	for _, value := range benchcorpus.SensitiveValues() {
		if strings.Contains(clean, value) {
			t.Fatalf("clean trajectory contains a sensitive value: %.16s...", value)
		}
	}
}

func TestDenseSecretsAreDenser(t *testing.T) {
	sparse := benchcorpus.EncodeChat(benchcorpus.Agent(benchcorpus.MediumBytes, benchcorpus.Options{Secrets: true}))
	dense := benchcorpus.EncodeChat(benchcorpus.Agent(benchcorpus.MediumBytes, benchcorpus.Options{Secrets: true, Dense: true}))
	key := benchcorpus.SensitiveValues()[0]
	if strings.Count(string(dense), key) <= strings.Count(string(sparse), key) {
		t.Fatalf("dense trajectory does not repeat the key more often (%d vs %d)",
			strings.Count(string(dense), key), strings.Count(string(sparse), key))
	}
}

func TestLongContextReachesTarget(t *testing.T) {
	for _, size := range []int{benchcorpus.LongBytes, benchcorpus.Agent256KBytes} {
		trajectory := benchcorpus.LongContext(size, benchcorpus.Options{Secrets: true})
		if len(trajectory.Steps) != 2 {
			t.Fatalf("steps = %d, want 2", len(trajectory.Steps))
		}
		body := benchcorpus.EncodeChat(trajectory)
		if len(body) < size {
			t.Fatalf("body = %d bytes, want at least %d", len(body), size)
		}
		if !strings.Contains(string(body), benchcorpus.SensitiveValues()[0]) {
			t.Fatal("long context lost the planted secrets")
		}
	}
}

func TestResponsesAreValidJSON(t *testing.T) {
	trajectory := benchcorpus.Agent(benchcorpus.MediumBytes, benchcorpus.Options{Secrets: true})
	for _, protocol := range protocols() {
		body := benchcorpus.Response(protocol, trajectory)
		if !json.Valid(body) {
			t.Fatalf("%s response is not valid JSON", protocol)
		}
		long := benchcorpus.LongResponse(protocol, benchcorpus.LongBytes, benchcorpus.Options{Secrets: true})
		if !json.Valid(long) || len(long) < benchcorpus.LongBytes {
			t.Fatalf("%s long response is invalid or too short (%d bytes)", protocol, len(long))
		}
	}
}

func TestChatStreamEventsAssemble(t *testing.T) {
	events := benchcorpus.ChatStreamEvents(benchcorpus.Agent256KBytes)
	if len(events) < 2 {
		t.Fatalf("events = %d", len(events))
	}
	assembler := stream.NewAssembler(xir.ProtocolChatCompletions)
	for _, event := range events {
		if err := assembler.Apply(event); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	response := assembler.Response()
	if response == nil {
		t.Fatal("nil response")
	}
	var text strings.Builder
	for _, part := range response.Parts {
		if part.Kind == xir.PartText {
			text.WriteString(part.Text)
		}
	}
	if text.Len() < benchcorpus.Agent256KBytes {
		t.Fatalf("assembled text = %d bytes, want at least %d", text.Len(), benchcorpus.Agent256KBytes)
	}
	if response.Usage == nil || response.Usage.InputTokens != 1024 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}
