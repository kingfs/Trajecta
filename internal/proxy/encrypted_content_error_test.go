package proxy

import (
	"encoding/json"
	"testing"
)

func TestInvalidEncryptedContentClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want bool
	}{
		{"native", `{"error":{"code":"invalid_encrypted_content","message":"rejected"}}`, true},
		{"padded", `{"error":{"code":"invalid_encrypted_content","message":"gAAA...n8I="}}`, true},
		{"double_padding", `{"error":{"code":"invalid_encrypted_content","message":"gAAA...Og=="}}`, true},
		{"no_fingerprint", `{"code":"invalid_encrypted_content"}`, true},
		{"unrelated_code", `{"error":{"code":"rate_limit_exceeded"}}`, false},
		{"mention_only", `{"error":{"message":"not invalid_encrypted_content"}}`, false},
		{"malformed", `{"error":{"code":"invalid_encrypted_content"`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			for depth := 0; depth < 3; depth++ {
				if got := (upstreamFailure{rawBody: test.body}).invalidEncryptedContent(); got != test.want {
					t.Fatalf("depth %d: classification = %v, want %v", depth, got, test.want)
				}
				wrapped, err := json.Marshal(map[string]any{"error": map[string]string{"message": test.body, "type": "api_error"}})
				if err != nil {
					t.Fatal(err)
				}
				test.body = string(wrapped)
			}
		})
	}
}
