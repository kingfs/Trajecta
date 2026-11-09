package convert

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

func minimalRequest(p xir.Protocol) []byte {
	switch p {
	case xir.ProtocolChatCompletions:
		return []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	case xir.ProtocolResponses:
		return []byte(`{"model":"m","input":"hi"}`)
	case xir.ProtocolMessages:
		return []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	case xir.ProtocolGemini:
		return []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	}
	return nil
}

func TestMinimalRequestMatrix(t *testing.T) {
	protocols := []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages, xir.ProtocolGemini}
	for _, from := range protocols {
		for _, to := range protocols {
			if from == to {
				continue
			}
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				got, err := ConvertRequest(context.Background(), minimalRequest(from), from, to, Options{Policy: Warn})
				if err != nil {
					t.Fatalf("convert: %v", err)
				}
				if len(got.Bytes) == 0 {
					t.Fatal("empty body")
				}
				if _, err := xir.ParseDocument(to, xir.OperationUnknown, got.Bytes); err != nil {
					t.Fatalf("target body is not JSON: %v (%s)", err, got.Bytes)
				}
			})
		}
	}
}
