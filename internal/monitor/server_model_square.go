package monitor

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/modelinfo"
	"github.com/kingfs/Trajecta/internal/store"
)

// The model square answers a different question from the usage list: it lists
// the models this installation can route right now, one entry per model name,
// with every channel that declares that name. Usage is a property of traffic
// and stays on /api/models; availability is a property of configuration and
// lives here.
//
// A model is available when at least one channel it is enabled on is itself
// enabled. A model enabled only on a disabled channel, or configured but not
// enabled, still has a row (scope=all) so an operator can see what is merely
// present, but it is not part of the default square.

type modelSquareResponse struct {
	Items       []modelSquareItem  `json:"items"`
	Summary     modelSquareSummary `json:"summary"`
	RefreshedAt time.Time          `json:"refreshed_at"`
	Scope       string             `json:"scope"`
}

type modelSquareSummary struct {
	Models    int `json:"models"`
	Available int `json:"available"`
	Matched   int `json:"matched"`
	Channels  int `json:"channels"`
}

type modelSquareItem struct {
	Model                 string `json:"model"`
	DisplayName           string `json:"display_name,omitempty"`
	Available             bool   `json:"available"`
	ChannelCount          int    `json:"channel_count"`
	AvailableChannelCount int    `json:"available_channel_count"`
	// Protocols is the union of the wire surfaces offered by the channels
	// that can serve this model now (or, when none can, by every declared
	// channel). The per-channel Interfaces remain the source facts.
	Protocols []string             `json:"protocols,omitempty"`
	Channels  []modelSquareChannel `json:"channels"`
	Facts     *modelinfo.Facts     `json:"facts,omitempty"`
}

type modelSquareChannel struct {
	ChannelID      string                  `json:"channel_id"`
	ChannelName    string                  `json:"channel_name,omitempty"`
	ChannelEnabled bool                    `json:"channel_enabled"`
	ModelEnabled   bool                    `json:"model_enabled"`
	Available      bool                    `json:"available"`
	State          string                  `json:"state,omitempty"`
	Source         string                  `json:"source,omitempty"`
	ProviderPreset string                  `json:"provider_preset,omitempty"`
	APIType        string                  `json:"api_type,omitempty"`
	ProtocolFamily string                  `json:"protocol_family,omitempty"`
	UpstreamModel  string                  `json:"upstream_model,omitempty"`
	Interfaces     channel.ModelInterfaces `json:"interfaces"`
	ContextWindow  *int                    `json:"context_window,omitempty"`
	MaxOutput      *int                    `json:"max_output_tokens,omitempty"`
}

// modelSquareProtocols folds the per-channel interface facts into the
// protocol badges shown on a model card. Available channels are preferred; a
// model with no available channel still reports what its configured channels
// declare, so scope=all is useful for planning.
func modelSquareProtocols(channels []modelSquareChannel) []string {
	add := func(set map[string]bool, interfaces channel.ModelInterfaces) {
		if interfaces.ChatCompletions || interfaces.ResponsesViaChat {
			set["chat_completions"] = true
		}
		if interfaces.NativeResponses {
			set["responses"] = true
		}
		if interfaces.Messages {
			set["messages"] = true
		}
		if interfaces.GenerateContent {
			set["gemini"] = true
		}
	}
	available := make(map[string]bool)
	all := make(map[string]bool)
	for _, entry := range channels {
		add(all, entry.Interfaces)
		if entry.Available {
			add(available, entry.Interfaces)
		}
	}
	set := available
	if len(set) == 0 {
		set = all
	}
	out := make([]string, 0, len(set))
	for _, protocol := range []string{"chat_completions", "responses", "messages", "gemini"} {
		if set[protocol] {
			out = append(out, protocol)
		}
	}
	return out
}

const (
	modelSquareScopeEnabled = "enabled"
	modelSquareScopeAll     = "all"
)

func modelSquareAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
		if scope != modelSquareScopeAll {
			scope = modelSquareScopeEnabled
		}
		query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

		// Metadata only: the square shows names and availability, never a
		// credential, and one channel with an unreadable secret must not take the
		// page down.
		configs, err := st.ListChannelConfigMetadata()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		byChannelID := make(map[string]store.ChannelConfigRecord, len(configs))
		for _, config := range configs {
			byChannelID[config.ID] = config
		}
		channelModels, err := st.ListChannelModels("", false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		items := make(map[string]*modelSquareItem, len(channelModels))
		// Prefer the display name declared by an available channel; a display
		// name on a disabled model is only a fallback.
		displayNameRank := make(map[string]int, len(channelModels))
		for _, record := range channelModels {
			model := strings.ToLower(strings.TrimSpace(record.Model))
			if model == "" || model == "list_models" {
				continue
			}
			item := items[model]
			if item == nil {
				item = &modelSquareItem{Model: model}
				items[model] = item
			}
			config, hasConfig := byChannelID[record.ChannelID]
			state := channel.ModelAvailability(config, record)
			routable := channel.ModelRoutable(config, record)
			entry := modelSquareChannel{
				ChannelID:      record.ChannelID,
				ChannelName:    strings.TrimSpace(config.Name),
				ChannelEnabled: hasConfig && config.Enabled,
				ModelEnabled:   record.Enabled,
				State:          string(state),
				Source:         record.Source,
				ProviderPreset: config.ProviderPreset,
				APIType:        config.APIType,
				ProtocolFamily: config.ProtocolFamily,
				UpstreamModel:  record.UpstreamModel,
				Interfaces:     channel.EffectiveModelInterfaces(config, record),
				ContextWindow:  record.ContextWindow,
				MaxOutput:      record.MaxOutputTokens,
			}
			// A model is available exactly when the proxy would route it: the
			// channel is enabled and the channel-model pair is routable (which
			// also excludes a discovered model the latest sync removed).
			entry.Available = entry.ChannelEnabled && routable
			item.Channels = append(item.Channels, entry)
			if record.DisplayName != "" {
				rank := 1
				if entry.Available {
					rank = 2
				}
				if rank > displayNameRank[model] {
					item.DisplayName = record.DisplayName
					displayNameRank[model] = rank
				}
			}
		}

		out := make([]modelSquareItem, 0, len(items))
		for _, item := range items {
			item.ChannelCount = len(item.Channels)
			for _, entry := range item.Channels {
				if entry.Available {
					item.AvailableChannelCount++
				}
			}
			item.Available = item.AvailableChannelCount > 0
			item.Protocols = modelSquareProtocols(item.Channels)
			sort.Slice(item.Channels, func(i, j int) bool {
				if item.Channels[i].Available != item.Channels[j].Available {
					return item.Channels[i].Available
				}
				if item.Channels[i].ChannelName != item.Channels[j].ChannelName {
					return item.Channels[i].ChannelName < item.Channels[j].ChannelName
				}
				return item.Channels[i].ChannelID < item.Channels[j].ChannelID
			})
			if facts, ok := modelinfo.Lookup(item.Model); ok {
				item.Facts = &facts
			}
			if scope == modelSquareScopeEnabled && !item.Available {
				continue
			}
			if query != "" && !modelSquareItemMatches(*item, query) {
				continue
			}
			out = append(out, *item)
		}

		sort.Slice(out, func(i, j int) bool {
			if out[i].Available != out[j].Available {
				return out[i].Available
			}
			if out[i].AvailableChannelCount != out[j].AvailableChannelCount {
				return out[i].AvailableChannelCount > out[j].AvailableChannelCount
			}
			if out[i].ChannelCount != out[j].ChannelCount {
				return out[i].ChannelCount > out[j].ChannelCount
			}
			return out[i].Model < out[j].Model
		})
		resp := modelSquareResponse{
			Items:       out,
			Summary:     summarizeModelSquare(out),
			RefreshedAt: time.Now().UTC(),
			Scope:       scope,
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func modelSquareItemMatches(item modelSquareItem, query string) bool {
	haystack := []string{item.Model, item.DisplayName}
	if item.Facts != nil {
		haystack = append(haystack,
			item.Facts.ID,
			item.Facts.Name,
			item.Facts.Provider,
			item.Facts.Developer,
			item.Facts.Family,
			item.Facts.Series,
			item.Facts.Summary,
			item.Facts.Description,
			item.Facts.DescriptionCN,
		)
	}
	for _, entry := range item.Channels {
		haystack = append(haystack, entry.ChannelID, entry.ChannelName)
	}
	for _, value := range haystack {
		if value != "" && strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func summarizeModelSquare(items []modelSquareItem) modelSquareSummary {
	summary := modelSquareSummary{Models: len(items)}
	channels := make(map[string]struct{}, 8)
	for _, item := range items {
		if item.Available {
			summary.Available++
		}
		if item.Facts != nil && item.Facts.Matched {
			summary.Matched++
		}
		// "Covered channels" means channels that actually serve one of the
		// listed models. A channel that merely declares a model but is disabled
		// (or a model disabled on it) is still shown on the card; it just does
		// not inflate the page's headline count.
		for _, entry := range item.Channels {
			if entry.Available && entry.ChannelID != "" {
				channels[entry.ChannelID] = struct{}{}
			}
		}
	}
	summary.Channels = len(channels)
	return summary
}
