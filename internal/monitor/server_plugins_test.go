package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
	pkgupstream "github.com/kingfs/Trajecta/pkg/upstream"
)

func pluginIDsForTest() []string {
	return pkgupstream.PluginIDs()
}

func newPluginTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestPluginsAPIDefaultAllDisabled(t *testing.T) {
	st := newPluginTestStore(t)
	rr := httptest.NewRecorder()
	pluginsAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/plugins", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload pluginsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != len(pluginIDsForTest()) {
		t.Fatalf("plugins = %d, want %d", len(payload.Items), len(pluginIDsForTest()))
	}
	var alibaba *pluginItem
	for i := range payload.Items {
		if payload.Items[i].Enabled {
			t.Errorf("plugin %q enabled by default", payload.Items[i].ID)
		}
		if payload.Items[i].ID == "alibaba" {
			alibaba = &payload.Items[i]
		}
	}
	if alibaba == nil {
		t.Fatal("alibaba plugin missing")
	}
	if alibaba.Name != "Alibaba Bailian-China" || alibaba.NameZh != "阿里百炼-中国" || alibaba.Region != "cn" {
		t.Fatalf("alibaba = %+v", alibaba)
	}
	if alibaba.Preset.ID != "alibaba" || len(alibaba.Preset.Plans) != 2 {
		t.Fatalf("alibaba preset payload = %+v", alibaba.Preset)
	}
	if alibaba.ChannelCount != 0 || alibaba.EnabledChannelCount != 0 {
		t.Fatalf("alibaba channel stats = %+v", alibaba)
	}
}

func TestPluginEnableDisableRoundTrip(t *testing.T) {
	st := newPluginTestStore(t)
	patch := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		pluginDetailAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodPatch, "/api/plugins/alibaba", strings.NewReader(body)))
		return rr
	}
	rr := patch(`{"enabled":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable status = %d body=%s", rr.Code, rr.Body.String())
	}
	var item pluginItem
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if !item.Enabled || item.ID != "alibaba" {
		t.Fatalf("enabled item = %+v", item)
	}
	// The state persists and gates the catalog.
	settings, err := loadChannelPluginSettings(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Enabled) != 1 || settings.Enabled[0] != "alibaba" {
		t.Fatalf("settings = %+v", settings)
	}
	catalogRR := httptest.NewRecorder()
	providerCatalogAPIHandler(st).ServeHTTP(catalogRR, httptest.NewRequest(http.MethodGet, "/api/catalog/providers", nil))
	var catalog providerCatalogResponse
	if err := json.Unmarshal(catalogRR.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if _, ok := findCatalogItem(catalog.Items, "alibaba"); !ok {
		t.Fatal("enabled plugin missing from catalog")
	}

	// No channels: disabling needs no force.
	rr = patch(`{"enabled":false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status = %d body=%s", rr.Code, rr.Body.String())
	}
	settings, _ = loadChannelPluginSettings(context.Background(), st)
	if len(settings.Enabled) != 0 {
		t.Fatalf("settings after disable = %+v", settings)
	}

	// Unknown plugin and invalid payload.
	rr = httptest.NewRecorder()
	pluginDetailAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodPatch, "/api/plugins/nope", strings.NewReader(`{"enabled":true}`)))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown plugin status = %d", rr.Code)
	}
	rr = patch(`{}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing enabled status = %d", rr.Code)
	}
	// Hidden legacy presets are not plugins.
	rr = httptest.NewRecorder()
	pluginDetailAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodPatch, "/api/plugins/qwen_coding", strings.NewReader(`{"enabled":true}`)))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("legacy preset as plugin status = %d", rr.Code)
	}
}

func TestPluginDisableChecksEnabledChannels(t *testing.T) {
	st := newPluginTestStore(t)
	// Enable the plugin and create a channel from its coding plan.
	if err := saveChannelPluginSettings(context.Background(), st, channelPluginSettings{Enabled: []string{"alibaba"}}); err != nil {
		t.Fatal(err)
	}
	create := `{"name":"Bailian Coding","provider_preset":"alibaba","plan":"coding","api_key":"sk-sp-test","models":["qwen3.7-plus"]}`
	rr := httptest.NewRecorder()
	channelListCreateAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(create)))
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created channelItem
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// Disabling with an enabled channel is refused with the affected channels.
	rr = httptest.NewRecorder()
	pluginDetailAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodPatch, "/api/plugins/alibaba", strings.NewReader(`{"enabled":false}`)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("disable status = %d body=%s", rr.Code, rr.Body.String())
	}
	var conflict struct {
		Error               string             `json:"error"`
		EnabledChannelCount int                `json:"enabled_channel_count"`
		EnabledModelCount   int                `json:"enabled_model_count"`
		Channels            []pluginChannelRef `json:"channels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Error != "plugin_in_use" || conflict.EnabledChannelCount != 1 || conflict.EnabledModelCount != 1 {
		t.Fatalf("conflict = %+v", conflict)
	}
	if len(conflict.Channels) != 1 || conflict.Channels[0].ID != created.ID || !conflict.Channels[0].Enabled {
		t.Fatalf("conflict channels = %+v", conflict.Channels)
	}

	// Force acknowledges the check; the channel itself keeps routing (enabled).
	rr = httptest.NewRecorder()
	pluginDetailAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodPatch, "/api/plugins/alibaba", strings.NewReader(`{"enabled":false,"force":true}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("forced disable status = %d body=%s", rr.Code, rr.Body.String())
	}
	record, err := st.GetChannelConfig(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Enabled {
		t.Fatal("plugin disable must not touch the channel's routing switch")
	}
	// The channel list badges the disabled plugin.
	rr = httptest.NewRecorder()
	channelListCreateAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/channels", nil))
	var list channelListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].PluginEnabled {
		t.Fatalf("channel list plugin badge = %+v", list.Items)
	}

	// Disabling the channel first makes the plain disable succeed.
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{ID: created.ID, Name: record.Name, BaseURL: record.BaseURL, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	pluginDetailAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodPatch, "/api/plugins/alibaba", strings.NewReader(`{"enabled":false}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("disable after channel off status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestChannelCreateFromPlanFillsEndpointsAndCapabilities(t *testing.T) {
	st := newPluginTestStore(t)
	create := `{"name":"Bailian Coding","provider_preset":"alibaba","plan":"coding","api_key":"sk-sp-test"}`
	rr := httptest.NewRecorder()
	channelListCreateAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(create)))
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created channelItem
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.BaseURL != "https://coding.dashscope.aliyuncs.com/v1" {
		t.Fatalf("base URL = %q", created.BaseURL)
	}
	if created.Endpoints["messages"] != "https://coding.dashscope.aliyuncs.com/apps/anthropic" {
		t.Fatalf("messages endpoint = %+v", created.Endpoints)
	}
	if created.Endpoints["chat_completions"] != "https://coding.dashscope.aliyuncs.com/v1" {
		t.Fatalf("chat endpoint = %+v", created.Endpoints)
	}
	if created.Capabilities.Messages == nil || !*created.Capabilities.Messages {
		t.Fatalf("messages capability = %+v", created.Capabilities)
	}
	if created.Capabilities.ChatCompletions == nil || !*created.Capabilities.ChatCompletions {
		t.Fatalf("chat capability = %+v", created.Capabilities)
	}
	if !created.CodingPlan || created.Plan != "coding" || created.PlanName != "Coding Plan" {
		t.Fatalf("plan facts = %+v", created)
	}
	if created.ModelDiscovery != "disabled" {
		t.Fatalf("model discovery = %q, want disabled for a NoList plan", created.ModelDiscovery)
	}
	// The stored record carries the plan and resolves both URL surfaces.
	record, err := st.GetChannelConfig(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Plan != "coding" {
		t.Fatalf("stored plan = %q", record.Plan)
	}

	// Wrong key prefix is rejected for the plan.
	rr = httptest.NewRecorder()
	channelListCreateAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(`{"name":"Bad","provider_preset":"alibaba","plan":"coding","api_key":"sk-standard"}`)))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "sk-sp-") {
		t.Fatalf("bad key status = %d body=%s", rr.Code, rr.Body.String())
	}

	// Pay-as-you-go default: no prefix rule, model list stays enabled.
	rr = httptest.NewRecorder()
	channelListCreateAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(`{"name":"Bailian PAYG","provider_preset":"alibaba","api_key":"sk-standard"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("payg create status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payg channelItem
	if err := json.Unmarshal(rr.Body.Bytes(), &payg); err != nil {
		t.Fatal(err)
	}
	if payg.BaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" {
		t.Fatalf("payg base URL = %q", payg.BaseURL)
	}
	if payg.Endpoints["messages"] != "https://dashscope.aliyuncs.com/apps/anthropic" {
		t.Fatalf("payg messages endpoint = %+v", payg.Endpoints)
	}
	if payg.CodingPlan || payg.PlanName != "" {
		t.Fatalf("payg plan facts = %+v", payg)
	}
	if payg.ModelDiscovery == "disabled" {
		t.Fatal("payg channel should keep model discovery")
	}
}

func TestPluginSettingsNormalizeUnknownIDs(t *testing.T) {
	st := newPluginTestStore(t)
	if err := st.SaveAppSettingJSON(context.Background(), pluginsSettingsKey, map[string]any{
		"enabled": []string{"alibaba", "no-such-plugin", "alibaba", "qwen_coding"},
	}); err != nil {
		t.Fatal(err)
	}
	settings, err := loadChannelPluginSettings(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Enabled) != 1 || settings.Enabled[0] != "alibaba" {
		t.Fatalf("normalized settings = %+v", settings)
	}
}

func TestLegacyPresetChannelCountsTowardPlugin(t *testing.T) {
	st := newPluginTestStore(t)
	// A channel created from a hidden legacy preset still belongs to its plugin.
	create := `{"name":"Legacy Qwen","provider_preset":"qwen_coding_anthropic","api_type":"messages","protocol_family":"anthropic_messages","api_key":"sk-sp-test"}`
	rr := httptest.NewRecorder()
	channelListCreateAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(create)))
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	pluginsAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/plugins", nil))
	var payload pluginsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, item := range payload.Items {
		if item.ID == "alibaba" {
			if item.ChannelCount != 1 || item.EnabledChannelCount != 1 {
				t.Fatalf("legacy channel not counted: %+v", item)
			}
			return
		}
	}
	t.Fatal("alibaba plugin missing")
}
