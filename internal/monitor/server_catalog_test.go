package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProviderCatalogAPI(t *testing.T) {
	rr := httptest.NewRecorder()
	providerCatalogAPIHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/catalog/providers", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload providerCatalogResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]providerCatalogItem, len(payload.Items))
	for _, item := range payload.Items {
		byID[item.ID] = item
	}
	openai, ok := byID["openai"]
	if !ok {
		t.Fatalf("openai missing from %d presets", len(payload.Items))
	}
	if openai.Endpoints["chat_completions"] != "https://api.openai.com/v1" || openai.Endpoints["responses"] == "" {
		t.Fatalf("openai endpoints = %+v", openai.Endpoints)
	}
	if openai.Auth.Mode != "api_key" || openai.Auth.Scheme != "Bearer" {
		t.Fatalf("openai auth = %+v", openai.Auth)
	}
	if openai.Models.ListPath != "/models" {
		t.Fatalf("openai models = %+v", openai.Models)
	}
	deepseek, ok := byID["deepseek"]
	if !ok {
		t.Fatal("deepseek missing")
	}
	if deepseek.Endpoints["messages"] != "https://api.deepseek.com/anthropic" {
		t.Fatalf("deepseek endpoints = %+v", deepseek.Endpoints)
	}
	openrouter, ok := byID["openrouter"]
	if !ok {
		t.Fatal("openrouter missing")
	}
	if openrouter.Kind != "relay" || openrouter.Endpoints["messages"] != "https://openrouter.ai/api" {
		t.Fatalf("openrouter = %+v", openrouter)
	}
}
