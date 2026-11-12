package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
)

func TestProviderCatalogAPI(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// The catalog is plugin-gated: enable the plugins this test inspects.
	if err := saveChannelPluginSettings(context.Background(), st, channelPluginSettings{
		Enabled: []string{"openai", "deepseek", "openrouter"},
	}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	providerCatalogAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/catalog/providers", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload providerCatalogResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 3 {
		t.Fatalf("catalog served %d items, want only the 3 enabled plugins", len(payload.Items))
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

func TestProviderCatalogDefaultEmpty(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rr := httptest.NewRecorder()
	providerCatalogAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/catalog/providers", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload providerCatalogResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 0 {
		t.Fatalf("default catalog served %d items, want empty (all plugins disabled)", len(payload.Items))
	}
}

func TestProviderCatalogHidesLegacyPresets(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Even with every plugin enabled, hidden legacy presets never appear.
	enabled := pluginIDsForTest()
	if err := saveChannelPluginSettings(context.Background(), st, channelPluginSettings{Enabled: enabled}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	providerCatalogAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/catalog/providers", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload providerCatalogResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, item := range payload.Items {
		switch item.ID {
		case "qwen_coding", "qwen_coding_anthropic", "qwen_coding_intl", "qwen_coding_intl_anthropic",
			"qwen_token_plan", "qwen_token_plan_intl", "zai_coding", "zai_coding_anthropic",
			"zhipu_coding", "zhipu_coding_anthropic", "kimi_coding":
			t.Errorf("hidden legacy preset %q served by the catalog", item.ID)
		}
	}
	alibaba, ok := findCatalogItem(payload.Items, "alibaba")
	if !ok {
		t.Fatal("alibaba plugin missing with all plugins enabled")
	}
	if alibaba.NameZh != "阿里百炼-中国" || alibaba.Region != "cn" {
		t.Fatalf("alibaba bilingual facts = %+v", alibaba)
	}
	if len(alibaba.Plans) != 2 {
		t.Fatalf("alibaba plans = %d, want coding + token", len(alibaba.Plans))
	}
	coding := alibaba.Plans[0]
	if coding.ID != "coding" || coding.KeyPrefix != "sk-sp-" || !coding.Coding || !coding.NoList {
		t.Fatalf("coding plan = %+v", coding)
	}
	if coding.Endpoints["chat_completions"] != "https://coding.dashscope.aliyuncs.com/v1" ||
		coding.Endpoints["messages"] != "https://coding.dashscope.aliyuncs.com/apps/anthropic" {
		t.Fatalf("coding endpoints = %+v", coding.Endpoints)
	}
	if coding.Description == "" || coding.DescriptionZh == "" {
		t.Fatalf("coding plan descriptions = %+v", coding)
	}
}

func findCatalogItem(items []providerCatalogItem, id string) (providerCatalogItem, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return providerCatalogItem{}, false
}
