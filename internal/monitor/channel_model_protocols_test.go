package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
)

func TestChannelModelProtocolsRoundTrip(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID: "c", Name: "C", BaseURL: "https://example.invalid/v1", ProviderPreset: "openai",
		APIType: "chat_completions", ProtocolFamily: "openai_compatible", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertChannelModel("c", store.ChannelModelRecord{Model: "m", Source: "manual", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	body := `{"protocols":{"chat_completions":{"enabled":true,"wire_model":"wire-chat"},"messages":{"enabled":true,"wire_model":"wire-msg"}}}`
	rr := httptest.NewRecorder()
	channelDetailAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodPatch, "/api/channels/c/models/m", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var item channelModelItem
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.Protocols["chat_completions"].WireModel != "wire-chat" || item.Protocols["messages"].WireModel != "wire-msg" {
		t.Fatalf("protocols = %+v", item.Protocols)
	}

	records, err := st.ListChannelModels("c", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !strings.Contains(records[0].ProtocolsJSON, "wire-msg") {
		t.Fatalf("stored protocols = %+v", records)
	}
}

func TestChannelDetailIncludesModelProtocols(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID: "c", Name: "C", BaseURL: "https://example.invalid/v1", ProviderPreset: "openai",
		APIType: "chat_completions", ProtocolFamily: "openai_compatible", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertChannelModel("c", store.ChannelModelRecord{
		Model: "m", Source: "manual", Enabled: true,
		ProtocolsJSON: `{"messages":{"enabled":true,"wire_model":"wire-msg"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	channelDetailAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/channels/c", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var item channelItem
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if len(item.ModelsUsage) != 1 || item.ModelsUsage[0].Protocols["messages"].WireModel != "wire-msg" {
		t.Fatalf("models_usage = %+v", item.ModelsUsage)
	}
}
