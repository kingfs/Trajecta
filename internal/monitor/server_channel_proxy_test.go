package monitor

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/netproxy"
)

func TestChannelProxySettingsPersistAndPublish(t *testing.T) {
	st, rtr, svc := configurationFixture(t)

	response := configurationRequest(channelDetailAPIHandler(st, rtr, svc), http.MethodPatch, "/api/channels/primary",
		`{"proxy":{"enabled":true,"http_proxy":"127.0.0.1:7890","https_proxy":"","no_proxy":" .internal.example.com,10.0.0.0/8 "}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body=%s", response.Code, response.Body.String())
	}
	var item channelItem
	if err := json.Unmarshal(response.Body.Bytes(), &item); err != nil {
		t.Fatalf("json.Unmarshal(channel) error = %v", err)
	}
	if !item.Proxy.Enabled || item.Proxy.HTTPProxy != "http://127.0.0.1:7890" {
		t.Fatalf("returned proxy = %+v, want enabled and canonical http proxy", item.Proxy)
	}
	if item.Proxy.NoProxy != ".internal.example.com,10.0.0.0/8" {
		t.Fatalf("returned no_proxy = %q", item.Proxy.NoProxy)
	}

	record, err := st.GetChannelConfig("primary")
	if err != nil {
		t.Fatalf("GetChannelConfig() error = %v", err)
	}
	var stored netproxy.Settings
	if err := json.Unmarshal([]byte(record.ProxyJSON), &stored); err != nil {
		t.Fatalf("stored proxy JSON = %q: %v", record.ProxyJSON, err)
	}
	if !stored.Enabled || stored.HTTPProxy != "http://127.0.0.1:7890" {
		t.Fatalf("stored proxy = %+v, want the canonical enabled policy", stored)
	}

	var published *netproxy.Resolver
	for _, target := range rtr.Targets() {
		if target.ChannelID == "primary" {
			published = target.Upstream.Proxy
			break
		}
	}
	if published == nil || !published.Enabled() {
		t.Fatalf("router target proxy = %+v, want an enabled resolver", published)
	}

	// Leaving the field out on a later patch keeps the stored policy.
	response = configurationRequest(channelDetailAPIHandler(st, rtr, svc), http.MethodPatch, "/api/channels/primary", `{"priority":7}`)
	if response.Code != http.StatusOK {
		t.Fatalf("second PATCH status = %d, body=%s", response.Code, response.Body.String())
	}
	record, err = st.GetChannelConfig("primary")
	if err != nil {
		t.Fatalf("GetChannelConfig() after second patch error = %v", err)
	}
	if err := json.Unmarshal([]byte(record.ProxyJSON), &stored); err != nil {
		t.Fatalf("stored proxy JSON after second patch = %q: %v", record.ProxyJSON, err)
	}
	if !stored.Enabled || stored.HTTPProxy != "http://127.0.0.1:7890" {
		t.Fatalf("stored proxy after omitted field = %+v, want the previous policy", stored)
	}
}

func TestChannelProxySettingsRejectInvalidPolicy(t *testing.T) {
	st, rtr, svc := configurationFixture(t)

	response := configurationRequest(channelDetailAPIHandler(st, rtr, svc), http.MethodPatch, "/api/channels/primary",
		`{"proxy":{"enabled":true,"https_proxy":"ftp://proxy.internal:21"}}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("PATCH status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
	record, err := st.GetChannelConfig("primary")
	if err != nil {
		t.Fatalf("GetChannelConfig() error = %v", err)
	}
	if record.ProxyJSON != "{}" {
		t.Fatalf("stored proxy = %q, want the default {} after a rejected write", record.ProxyJSON)
	}
}

func TestChannelProxySettingsRedactPasswordAndKeepItOnSave(t *testing.T) {
	st, rtr, svc := configurationFixture(t)

	response := configurationRequest(channelDetailAPIHandler(st, rtr, svc), http.MethodPatch, "/api/channels/primary",
		`{"proxy":{"enabled":true,"http_proxy":"http://alice:s3cret@127.0.0.1:7890"}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body=%s", response.Code, response.Body.String())
	}
	var item channelItem
	if err := json.Unmarshal(response.Body.Bytes(), &item); err != nil {
		t.Fatalf("json.Unmarshal(channel) error = %v", err)
	}
	if item.Proxy.HTTPProxy != "http://alice:xxxxx@127.0.0.1:7890" {
		t.Fatalf("returned http_proxy = %q, want the password redacted", item.Proxy.HTTPProxy)
	}
	if strings.Contains(response.Body.String(), "s3cret") {
		t.Fatalf("channel response leaked the proxy password: %s", response.Body.String())
	}

	// The UI sends the redacted value back on the next save. The stored password
	// must survive that round trip.
	response = configurationRequest(channelDetailAPIHandler(st, rtr, svc), http.MethodPatch, "/api/channels/primary",
		`{"priority":9,"proxy":{"enabled":true,"http_proxy":"http://alice:xxxxx@127.0.0.1:7890"}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("second PATCH status = %d, body=%s", response.Code, response.Body.String())
	}
	record, err := st.GetChannelConfig("primary")
	if err != nil {
		t.Fatalf("GetChannelConfig() error = %v", err)
	}
	var stored netproxy.Settings
	if err := json.Unmarshal([]byte(record.ProxyJSON), &stored); err != nil {
		t.Fatalf("stored proxy JSON = %q: %v", record.ProxyJSON, err)
	}
	if stored.HTTPProxy != "http://alice:s3cret@127.0.0.1:7890" {
		t.Fatalf("stored http_proxy = %q, want the original password kept", stored.HTTPProxy)
	}
}
