// Network proxy settings: the outbound policy the server applies to LLM
// upstream calls, provider probes and the GitHub release lookup.

package monitor

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/kingfs/Trajecta/internal/netproxy"
	"github.com/kingfs/Trajecta/internal/store"
)

type networkProxySettingsView struct {
	Enabled    bool   `json:"enabled"`
	HTTPProxy  string `json:"http_proxy"`
	HTTPSProxy string `json:"https_proxy"`
	NoProxy    string `json:"no_proxy"`
	// EffectiveHTTPSProxy is derived for the page: an empty https_proxy means
	// "same as http_proxy". It is reported rather than stored so the fallback
	// survives a later edit of http_proxy.
	EffectiveHTTPSProxy string `json:"effective_https_proxy,omitempty"`
}

func newNetworkProxySettingsView(settings netproxy.Settings) networkProxySettingsView {
	settings = settings.Normalize()
	return networkProxySettingsView{
		Enabled:             settings.Enabled,
		HTTPProxy:           settings.HTTPProxy,
		HTTPSProxy:          settings.HTTPSProxy,
		NoProxy:             settings.NoProxy,
		EffectiveHTTPSProxy: settings.EffectiveHTTPSProxy(),
	}
}

func loadNetworkProxySettings(ctx context.Context, st *store.Store) (netproxy.Settings, error) {
	settings := netproxy.DefaultSettings()
	if st == nil {
		return settings, nil
	}
	var persisted netproxy.Settings
	found, err := st.LoadAppSettingJSON(ctx, netproxy.SettingKey, &persisted)
	if err != nil {
		return netproxy.Settings{}, err
	}
	if found {
		settings = persisted.Normalize()
	}
	compiled, err := netproxy.Compile(settings)
	if err != nil {
		return netproxy.Settings{}, err
	}
	return compiled.Settings(), nil
}

// networkProxySettingsAPIHandler serves GET and PATCH
// /api/settings/network-proxy. The route is admin-only: the proxy URL may carry
// credentials and changing it changes where every outbound request goes.
func networkProxySettingsAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			settings, err := loadNetworkProxySettings(r.Context(), st)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, newNetworkProxySettingsView(settings))
		case http.MethodPatch:
			var req networkProxySettingsView
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid network proxy settings payload"})
				return
			}
			settings := netproxy.Settings{
				Enabled:    req.Enabled,
				HTTPProxy:  req.HTTPProxy,
				HTTPSProxy: req.HTTPSProxy,
				NoProxy:    req.NoProxy,
			}.Normalize()
			compiled, err := netproxy.Compile(settings)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			// Store the canonical form the transport parsed, not the raw form
			// that was submitted, so a bare host:port comes back with its
			// scheme and the next read needs no re-parsing to be meaningful.
			settings = compiled.Settings()
			if err := st.SaveAppSettingJSON(r.Context(), netproxy.SettingKey, settings); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if err := netproxy.Configure(settings); err != nil {
				// Compile already accepted the same value, so this is only
				// reachable if the compile rule and this path diverge.
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, newNetworkProxySettingsView(settings))
		default:
			http.NotFound(w, r)
		}
	}
}
