package monitor

import (
	"net/http"
	"sort"

	"github.com/kingfs/Trajecta/pkg/upstream"
	"github.com/kingfs/Trajecta/pkg/xir"
)

// providerCatalogResponse is the read-only provider catalog used by the
// onboarding UI. It is derived entirely from pkg/upstream's compiled presets:
// no channel row, credential or operator-specific state is included.
type providerCatalogResponse struct {
	Items []providerCatalogItem `json:"items"`
}

type providerCatalogItem struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Kind      string                `json:"kind"`
	Endpoints map[string]string     `json:"endpoints"`
	Protocols []string              `json:"protocols"`
	Auth      providerCatalogAuth   `json:"auth"`
	Plans     []providerCatalogPlan `json:"plans,omitempty"`
	Models    providerCatalogModels `json:"models"`
	Headers   map[string]string     `json:"headers,omitempty"`
	DocsURL   string                `json:"docs_url,omitempty"`
	KeysURL   string                `json:"keys_url,omitempty"`
	Notes     string                `json:"notes,omitempty"`
}

type providerCatalogAuth struct {
	Mode        string                `json:"mode"`
	Header      string                `json:"header,omitempty"`
	Scheme      string                `json:"scheme,omitempty"`
	OAuth       *providerCatalogOAuth `json:"oauth,omitempty"`
	Refresh     bool                  `json:"refresh,omitempty"`
	HeaderRules []string              `json:"header_rules,omitempty"`
}

type providerCatalogOAuth struct {
	AuthorizeURL  string   `json:"authorize_url,omitempty"`
	TokenURL      string   `json:"token_url,omitempty"`
	DeviceCodeURL string   `json:"device_code_url,omitempty"`
	ClientID      string   `json:"client_id,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
	RedirectURI   string   `json:"redirect_uri,omitempty"`
	PKCE          bool     `json:"pkce,omitempty"`
}

type providerCatalogPlan struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Endpoints   map[string]string `json:"endpoints"`
	ModelFilter string            `json:"model_filter,omitempty"`
}

type providerCatalogModels struct {
	ListPath string   `json:"list_path,omitempty"`
	Static   []string `json:"static,omitempty"`
	NoList   bool     `json:"no_list,omitempty"`
	Protocol string   `json:"protocol,omitempty"`
}

func providerCatalogAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		presets := upstream.Presets()
		items := make([]providerCatalogItem, 0, len(presets))
		for _, preset := range presets {
			items = append(items, providerCatalogItemFrom(preset))
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		writeJSON(w, http.StatusOK, providerCatalogResponse{Items: items})
	}
}

func providerCatalogItemFrom(preset upstream.Preset) providerCatalogItem {
	item := providerCatalogItem{
		ID:        preset.ID,
		Name:      preset.Name,
		Kind:      string(preset.Kind),
		Endpoints: protocolEndpoints(preset.Endpoints),
		Protocols: protocolNames(preset.Endpoints),
		Auth:      catalogAuth(preset.Auth),
		Models: providerCatalogModels{
			ListPath: preset.Models.ListPath,
			Static:   append([]string(nil), preset.Models.Static...),
			NoList:   preset.Models.NoList,
		},
		Headers: cloneStringMap(preset.Headers),
		DocsURL: preset.DocsURL,
		KeysURL: preset.KeysURL,
		Notes:   preset.Notes,
	}
	if preset.Models.Protocol.Valid() {
		item.Models.Protocol = string(preset.Models.Protocol)
	}
	for _, plan := range preset.Plans {
		item.Plans = append(item.Plans, providerCatalogPlan{
			ID:          plan.ID,
			Name:        plan.Name,
			Endpoints:   protocolEndpoints(plan.Endpoints),
			ModelFilter: plan.ModelFilter,
		})
	}
	return item
}

func catalogAuth(spec upstream.AuthSpec) providerCatalogAuth {
	auth := providerCatalogAuth{
		Mode:   string(spec.Mode),
		Header: spec.Header,
		Scheme: spec.Scheme,
	}
	if spec.OAuth != nil {
		auth.OAuth = &providerCatalogOAuth{
			AuthorizeURL:  spec.OAuth.AuthorizeURL,
			TokenURL:      spec.OAuth.TokenURL,
			DeviceCodeURL: spec.OAuth.DeviceCodeURL,
			ClientID:      spec.OAuth.ClientID,
			Scopes:        append([]string(nil), spec.OAuth.Scopes...),
			RedirectURI:   spec.OAuth.RedirectURI,
			PKCE:          spec.OAuth.PKCE,
		}
	}
	auth.Refresh = spec.Refresh != nil
	for _, header := range spec.Headers {
		if header.Name != "" {
			auth.HeaderRules = append(auth.HeaderRules, header.Name)
		}
	}
	return auth
}

func protocolEndpoints(endpoints map[xir.Protocol]string) map[string]string {
	if len(endpoints) == 0 {
		return nil
	}
	out := make(map[string]string, len(endpoints))
	for protocol, endpoint := range endpoints {
		out[string(protocol)] = endpoint
	}
	return out
}

func protocolNames(endpoints map[xir.Protocol]string) []string {
	out := make([]string, 0, len(endpoints))
	for _, protocol := range []xir.Protocol{
		xir.ProtocolChatCompletions,
		xir.ProtocolResponses,
		xir.ProtocolMessages,
		xir.ProtocolGemini,
	} {
		if endpoints[protocol] != "" {
			out = append(out, string(protocol))
		}
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
