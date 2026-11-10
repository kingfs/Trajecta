package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/pkg/protocolconvert"
)

func TestApplyConvertedHeaders(t *testing.T) {
	original := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	original.Header.Set("anthropic-beta", "tools-2024")
	messages := httptest.NewRequest("POST", "/v1/messages", nil)
	ApplyConvertedHeaders(messages, original, protocolconvert.ProtocolMessages)
	if messages.Header.Get("anthropic-version") != "2023-06-01" || messages.Header.Get("anthropic-beta") != "tools-2024" {
		t.Fatalf("messages headers = %v", messages.Header)
	}

	chat := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	chat.Header.Set("anthropic-version", "old")
	ApplyConvertedHeaders(chat, original, protocolconvert.ProtocolChatCompletions)
	if chat.Header.Get("anthropic-version") != "" || chat.Header.Get("anthropic-beta") != "" {
		t.Fatalf("anthropic headers leaked: %v", chat.Header)
	}

	geminiOriginal := httptest.NewRequest("POST", "/v1beta/models/gemini:generateContent?key=secret", nil)
	gemini := httptest.NewRequest("POST", "/v1beta/models/gemini:generateContent", nil)
	ApplyConvertedHeaders(gemini, geminiOriginal, protocolconvert.ProtocolGemini)
	if gemini.Header.Get("x-goog-api-key") != "secret" {
		t.Fatalf("gemini key = %q", gemini.Header.Get("x-goog-api-key"))
	}
	if gemini.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content type = %q", gemini.Header.Get("Content-Type"))
	}
}
