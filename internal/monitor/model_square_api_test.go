package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

func TestModelSquareListsEnabledModelsWithEveryChannel(t *testing.T) {
	t.Parallel()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	probeAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, config := range []store.ChannelConfigRecord{
		{ID: "alpha", Name: "Alpha", BaseURL: "https://alpha.invalid/v1", ProviderPreset: "openai", APIType: "chat_completions", ProtocolFamily: "openai_compatible", HeadersJSON: "{}", Enabled: true},
		{ID: "beta", Name: "Beta", BaseURL: "https://beta.invalid/v1", ProviderPreset: "openrouter", APIType: "chat_completions", ProtocolFamily: "openai_compatible", HeadersJSON: "{}", Enabled: true, LastProbeAt: probeAt},
		{ID: "off", Name: "Off", BaseURL: "https://off.invalid/v1", ProviderPreset: "openai", APIType: "chat_completions", ProtocolFamily: "openai_compatible", HeadersJSON: "{}", Enabled: false},
	} {
		if _, err := st.UpsertChannelConfig(config); err != nil {
			t.Fatalf("UpsertChannelConfig(%s) error = %v", config.ID, err)
		}
	}
	for channelID, models := range map[string][]store.ChannelModelRecord{
		"alpha": {
			{Model: "gpt-4o", DisplayName: "GPT-4o Alpha", Source: "manual", Enabled: true, UpstreamModel: "gpt-4o-2024-08-06", ContextWindow: intPtr(128000), MaxOutputTokens: intPtr(16384)},
			{Model: "qwen3-32b", DisplayName: "Qwen3 32B", Source: "manual", Enabled: false},
		},
		"beta": {
			{Model: "gpt-4o", DisplayName: "GPT-4o Beta", Source: "manual", Enabled: true},
			{Model: "claude-3-5-sonnet", DisplayName: "Claude 3.5 Sonnet", Source: "manual", Enabled: true},
			// Discovered by an older sync: the latest successful sync did not
			// return it, so the proxy never routes it even though the flag is on.
			{Model: "removed-model", DisplayName: "Removed Model", Source: "discovered", Enabled: true, LastProbeAt: probeAt.Add(-time.Hour)},
		},
		"off": {
			{Model: "gpt-4o", DisplayName: "GPT-4o Off", Source: "manual", Enabled: true},
		},
	} {
		if err := st.ReplaceChannelModels(channelID, models); err != nil {
			t.Fatalf("ReplaceChannelModels(%s) error = %v", channelID, err)
		}
	}

	enabled := getModelSquare(t, st, "/api/model-square")
	if enabled.Summary.Models != 2 || enabled.Summary.Available != 2 || enabled.Summary.Matched != 2 || enabled.Summary.Channels != 2 {
		t.Fatalf("enabled square summary = %+v", enabled.Summary)
	}
	if len(enabled.Items) != 2 {
		t.Fatalf("enabled square items = %+v", enabled.Items)
	}
	byModel := map[string]modelSquareItem{}
	for _, item := range enabled.Items {
		byModel[item.Model] = item
	}
	gpt := byModel["gpt-4o"]
	if !gpt.Available || gpt.ChannelCount != 3 || gpt.AvailableChannelCount != 2 {
		t.Fatalf("gpt-4o square item = %+v", gpt)
	}
	if gpt.Facts == nil || gpt.Facts.Provider == "" {
		t.Fatalf("gpt-4o facts = %+v", gpt.Facts)
	}
	// The display name comes from an available channel, never from the one
	// behind a disabled channel.
	if gpt.DisplayName != "GPT-4o Alpha" && gpt.DisplayName != "GPT-4o Beta" {
		t.Fatalf("gpt-4o display name = %q", gpt.DisplayName)
	}
	if len(gpt.Channels) != 3 {
		t.Fatalf("gpt-4o channels = %+v", gpt.Channels)
	}
	// Available channels sort first so the chips lead with a callable one.
	if !gpt.Channels[0].Available || gpt.Channels[0].ChannelName == "" {
		t.Fatalf("gpt-4o first channel = %+v", gpt.Channels[0])
	}
	if gpt.Channels[0].Interfaces.ChatCompletions == false && gpt.Channels[0].Interfaces.NativeResponses == false {
		t.Fatalf("gpt-4o first channel declares no interface: %+v", gpt.Channels[0].Interfaces)
	}
	for _, channel := range gpt.Channels {
		if channel.ChannelID == "off" {
			if channel.ChannelEnabled || channel.Available {
				t.Fatalf("disabled channel reported as available: %+v", channel)
			}
		}
	}
	claude := byModel["claude-3-5-sonnet"]
	if claude.Facts == nil || claude.Facts.ID != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("claude facts = %+v", claude.Facts)
	}
	if _, ok := byModel["qwen3-32b"]; ok {
		t.Fatalf("disabled model appeared in the enabled square: %+v", byModel)
	}

	all := getModelSquare(t, st, "/api/model-square?scope=all")
	if all.Summary.Models != 4 || all.Summary.Available != 2 {
		t.Fatalf("all square summary = %+v", all.Summary)
	}
	if all.Scope != "all" {
		t.Fatalf("all square scope = %q", all.Scope)
	}
	allByModel := map[string]modelSquareItem{}
	for _, item := range all.Items {
		allByModel[item.Model] = item
	}
	if item := allByModel["qwen3-32b"]; item.Available || item.AvailableChannelCount != 0 || item.ChannelCount != 1 {
		t.Fatalf("disabled model item = %+v", item)
	}
	if item, ok := allByModel["removed-model"]; !ok {
		t.Fatalf("removed model missing from scope=all: %+v", all.Items)
	} else {
		if item.Available || item.AvailableChannelCount != 0 || item.ChannelCount != 1 || len(item.Channels) != 1 {
			t.Fatalf("removed model item = %+v", item)
		}
		if item.Channels[0].Available || item.Channels[0].State != "removed" {
			t.Fatalf("removed model channel = %+v", item.Channels[0])
		}
	}

	searched := getModelSquare(t, st, "/api/model-square?q=claude")
	if len(searched.Items) != 1 || searched.Items[0].Model != "claude-3-5-sonnet" {
		t.Fatalf("search by model = %+v", searched.Items)
	}
	// Search also names channels and catalogue providers, which is how an
	// operator finds "everything on the OpenRouter channel".
	byChannel := getModelSquare(t, st, "/api/model-square?q=beta")
	for _, item := range byChannel.Items {
		if item.Model == "claude-3-5-sonnet" {
			return
		}
	}
	t.Fatalf("search by channel returned = %+v", byChannel.Items)
}

func TestModelDetailCarriesCatalogueFactsAndChannelNames(t *testing.T) {
	t.Parallel()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID: "alpha", Name: "Alpha", BaseURL: "https://alpha.invalid/v1", ProviderPreset: "openai",
		APIType: "chat_completions", ProtocolFamily: "openai_compatible", HeadersJSON: "{}", Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if err := st.ReplaceChannelModels("alpha", []store.ChannelModelRecord{
		{Model: "gpt-4o", DisplayName: "GPT-4o", Source: "manual", Enabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID: "beta-off", Name: "Beta Off", BaseURL: "https://beta-off.invalid/v1", ProviderPreset: "openai",
		APIType: "chat_completions", ProtocolFamily: "openai_compatible", HeadersJSON: "{}", Enabled: false,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig(beta-off) error = %v", err)
	}
	if err := st.ReplaceChannelModels("beta-off", []store.ChannelModelRecord{
		{Model: "gpt-4o", DisplayName: "GPT-4o", Source: "manual", Enabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannelModels(beta-off) error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/models/gpt-4o", nil)
	rr := httptest.NewRecorder()
	modelDetailAPIHandler(st).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("model detail status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var detail modelDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("json.Unmarshal(detail) error = %v", err)
	}
	if detail.Facts == nil || detail.Facts.ID != "openai/gpt-4o" || detail.Facts.Matched != true {
		t.Fatalf("detail facts = %+v", detail.Facts)
	}
	if len(detail.Channels) != 2 || detail.AvailableChannelCount != 1 {
		t.Fatalf("detail channels = %+v, available = %d", detail.Channels, detail.AvailableChannelCount)
	}
	byChannel := map[string]modelChannelItem{}
	for _, channel := range detail.Channels {
		byChannel[channel.ChannelID] = channel
	}
	alpha := byChannel["alpha"]
	if alpha.ChannelName != "Alpha" || !alpha.ChannelEnabled || !alpha.Available || alpha.State != "unverified" || !alpha.Routable {
		t.Fatalf("alpha channel = %+v", alpha)
	}
	off := byChannel["beta-off"]
	if off.ChannelName != "Beta Off" || off.ChannelEnabled || off.Available {
		t.Fatalf("disabled channel = %+v", off)
	}
}

func getModelSquare(t *testing.T, st *store.Store, target string) modelSquareResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rr := httptest.NewRecorder()
	modelSquareAPIHandler(st).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("model square %s status = %d, body=%s", target, rr.Code, rr.Body.String())
	}
	var response modelSquareResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", target, err)
	}
	return response
}
