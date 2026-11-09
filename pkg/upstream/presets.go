package upstream

import (
	"sort"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// builtinPresets is the package-level catalog. Presets and LookupPreset return
// deep copies so callers cannot mutate it.
var builtinPresets = []Preset{
	{
		ID:   "anthropic",
		Name: "Anthropic",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolMessages: "https://api.anthropic.com",
		},
		Auth: anthropicAuth(),
		Models: ModelSource{
			ListPath: "/v1/models",
			Protocol: xir.ProtocolMessages,
		},
		DocsURL: "https://docs.anthropic.com",
		KeysURL: "https://console.anthropic.com/settings/keys",
		Notes:   "The Messages API lives at the API root; clients append /v1/messages. anthropic-version is sent as a default header when the request has none.",
	},
	{
		ID:   "azure",
		Name: "Azure OpenAI",
		Kind: KindVendor,
		// Azure endpoints are per-resource, so there is no universal base URL to
		// publish here. Config.Endpoints must supply one.
		Endpoints: nil,
		Auth:      AuthSpec{Mode: AuthModeAPIKey, Header: "api-key"},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://learn.microsoft.com/azure/ai-foundry/openai/",
		KeysURL: "https://portal.azure.com",
		Notes:   "Supply the resource endpoint, for example https://RESOURCE.openai.azure.com/openai/v1. APIVersion is added as the api-version query parameter when set.",
	},
	{
		ID:   "cerebras",
		Name: "Cerebras",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.cerebras.ai/v1",
		},
		Auth: bearerAuth(),
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://inference-docs.cerebras.ai",
		KeysURL: "https://cloud.cerebras.ai",
		Notes:   "OpenAI-compatible Chat Completions only; Responses is not advertised here.",
	},
	{
		ID:   "deepseek",
		Name: "DeepSeek",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.deepseek.com/v1",
			xir.ProtocolResponses:       "https://api.deepseek.com/v1",
			xir.ProtocolMessages:        "https://api.deepseek.com/anthropic",
		},
		Auth: bearerAuth(),
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://api-docs.deepseek.com",
		KeysURL: "https://platform.deepseek.com/api_keys",
		Notes:   "One preset serves OpenAI Chat/Responses and an Anthropic-compatible Messages endpoint; clients append /v1/messages to the Anthropic base.",
	},
	{
		ID:   "fireworks",
		Name: "Fireworks AI",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.fireworks.ai/inference/v1",
			xir.ProtocolResponses:       "https://api.fireworks.ai/inference/v1",
			xir.ProtocolMessages:        "https://api.fireworks.ai/inference",
		},
		Auth: bearerAuth(),
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.fireworks.ai",
		KeysURL: "https://fireworks.ai/account/api-keys",
		Notes:   "The Anthropic-compatible base ends at /inference; the SDK appends /v1/messages.",
	},
	{
		ID:   "github",
		Name: "GitHub Models",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://models.github.ai/inference",
		},
		Auth: bearerAuth(),
		Models: ModelSource{
			NoList: true,
		},
		DocsURL: "https://docs.github.com/en/github-models",
		KeysURL: "https://github.com/settings/tokens",
		Notes:   "OpenAI-compatible inference surface. The model catalog is a separate endpoint outside /inference, so no ListPath is declared.",
	},
	{
		ID:   "google",
		Name: "Google Gemini",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolGemini:          "https://generativelanguage.googleapis.com/v1beta",
			xir.ProtocolChatCompletions: "https://generativelanguage.googleapis.com/v1beta/openai",
		},
		Auth: AuthSpec{Mode: AuthModeAPIKey, Header: "x-goog-api-key"},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolGemini,
		},
		DocsURL: "https://ai.google.dev/gemini-api/docs",
		KeysURL: "https://aistudio.google.com/apikey",
		Notes:   "The OpenAI-compatible surface is mounted under /v1beta/openai; the native Gemini surface uses /v1beta/models/{model}:generateContent.",
	},
	{
		ID:   "groq",
		Name: "Groq",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.groq.com/openai/v1",
			xir.ProtocolResponses:       "https://api.groq.com/openai/v1",
		},
		Auth: bearerAuth(),
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://console.groq.com/docs",
		KeysURL: "https://console.groq.com/keys",
		Notes:   "Both Chat Completions and Responses are mounted under /openai/v1.",
	},
	{
		ID:   "minimax",
		Name: "MiniMax",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.minimax.io/v1",
			xir.ProtocolMessages:        "https://api.minimax.io/anthropic",
		},
		Auth: bearerAuth(),
		Models: ModelSource{
			NoList: true,
		},
		DocsURL: "https://platform.minimax.io/docs",
		KeysURL: "https://platform.minimax.io/user-center/basic-information/interface-key",
		Notes:   "OpenAI-compatible and Anthropic-compatible surfaces. No public model-list path is assumed.",
	},
	{
		ID:   "mistral",
		Name: "Mistral AI",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.mistral.ai/v1",
		},
		Auth: bearerAuth(),
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.mistral.ai",
		KeysURL: "https://console.mistral.ai/api-keys",
		Notes:   "OpenAI-compatible Chat Completions only.",
	},
	{
		ID:   "moonshot",
		Name: "Moonshot / Kimi",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.moonshot.cn/v1",
			xir.ProtocolMessages:        "https://api.moonshot.cn/anthropic",
		},
		Auth: bearerAuth(),
		Plans: []PlanVariant{
			{
				ID:   "kimi-code",
				Name: "Kimi Code (membership)",
				Endpoints: map[xir.Protocol]string{
					xir.ProtocolMessages: "https://api.kimi.ai/coding",
				},
				ModelFilter: "kimi-*",
			},
		},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://platform.kimi.com/docs",
		KeysURL: "https://platform.kimi.com/console/api-keys",
		Notes:   "The Kimi open platform uses api.moonshot.cn (overseas: api.moonshot.ai). The kimi-code plan uses a membership key against the Anthropic-compatible coding endpoint.",
	},
	{
		ID:        "ollama",
		Name:      "Ollama",
		Kind:      KindLocal,
		Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "http://localhost:11434/v1"},
		Auth:      AuthSpec{Mode: AuthModeNone},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.ollama.com",
		Notes:   "Local default. Override the endpoint for a remote Ollama host.",
	},
	{
		ID:   "openai",
		Name: "OpenAI",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.openai.com/v1",
			xir.ProtocolResponses:       "https://api.openai.com/v1",
		},
		Auth: bearerAuth(),
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://platform.openai.com/docs",
		KeysURL: "https://platform.openai.com/api-keys",
	},
	{
		ID:   "openrouter",
		Name: "OpenRouter",
		Kind: KindRelay,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://openrouter.ai/api/v1",
			xir.ProtocolMessages:        "https://openrouter.ai/api",
		},
		Auth: bearerAuth(),
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://openrouter.ai/docs",
		KeysURL: "https://openrouter.ai/settings/keys",
		Notes:   "The Anthropic-compatible base is https://openrouter.ai/api; the SDK appends /v1/messages. Responses is deliberately not declared as a preset default.",
	},
	{
		ID:   "together",
		Name: "Together AI",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.together.xyz/v1",
		},
		Auth: bearerAuth(),
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.together.ai",
		KeysURL: "https://api.together.xyz/settings/api-keys",
		Notes:   "OpenAI-compatible Chat Completions only.",
	},
	{
		ID:   "vertex",
		Name: "Google Vertex AI",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolGemini: "https://aiplatform.googleapis.com/v1/publishers/google/models",
		},
		Auth: AuthSpec{Mode: AuthModeAPIKey, Header: "x-goog-api-key"},
		Models: ModelSource{
			// A slash means the endpoint base itself is the model collection
			// (it already ends in /models).
			ListPath: "/",
			Protocol: xir.ProtocolGemini,
		},
		DocsURL: "https://cloud.google.com/vertex-ai/generative-ai/docs",
		KeysURL: "https://console.cloud.google.com/apis/credentials",
		Notes:   "The preset endpoint is Express Mode (global host, API key). For standard Vertex, override the Gemini endpoint with https://LOCATION-aiplatform.googleapis.com/v1/projects/PROJECT/locations/LOCATION/publishers/google/models and supply an OAuth access token; Project and Location must be set together.",
	},
	{
		ID:        "vllm",
		Name:      "vLLM",
		Kind:      KindLocal,
		Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "http://localhost:8000/v1"},
		Auth:      AuthSpec{Mode: AuthModeNone},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.vllm.ai",
		Notes:   "Local default. vLLM's --api-key option, when enabled, is supplied through Config.Auth or a credential.",
	},
	{
		ID:   "xai",
		Name: "xAI",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.x.ai/v1",
			xir.ProtocolResponses:       "https://api.x.ai/v1",
		},
		Auth: bearerAuth(),
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.x.ai",
		KeysURL: "https://console.x.ai",
	},
	{
		ID:   "zai",
		Name: "Z.ai",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.z.ai/api/paas/v4",
			xir.ProtocolMessages:        "https://api.z.ai/api/anthropic",
		},
		Auth: bearerAuth(),
		Plans: []PlanVariant{
			{
				ID:   "coding",
				Name: "GLM Coding Plan",
				Endpoints: map[xir.Protocol]string{
					xir.ProtocolChatCompletions: "https://api.z.ai/api/coding/paas/v4",
					xir.ProtocolResponses:       "https://api.z.ai/api/v1",
					xir.ProtocolMessages:        "https://api.z.ai/api/anthropic",
				},
				ModelFilter: "glm-*",
			},
		},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.z.ai",
		KeysURL: "https://z.ai/manage-apikey/apikey-list",
		Notes:   "The coding plan uses a dedicated Chat Completions base and exposes Responses at /api/v1.",
	},
	{
		ID:   "zhipu",
		Name: "Zhipu BigModel",
		Kind: KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://open.bigmodel.cn/api/paas/v4",
			xir.ProtocolMessages:        "https://open.bigmodel.cn/api/anthropic",
		},
		Auth: bearerAuth(),
		Plans: []PlanVariant{
			{
				ID:   "coding",
				Name: "GLM Coding Plan",
				Endpoints: map[xir.Protocol]string{
					xir.ProtocolChatCompletions: "https://open.bigmodel.cn/api/coding/paas/v4",
					xir.ProtocolResponses:       "https://open.bigmodel.cn/api/v1",
					xir.ProtocolMessages:        "https://open.bigmodel.cn/api/anthropic",
				},
				ModelFilter: "glm-*",
			},
		},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.bigmodel.cn",
		KeysURL: "https://bigmodel.cn/usercenter/proj-mgmt/apikeys",
		Notes:   "The coding plan uses a dedicated Chat Completions base and exposes Responses at /api/v1.",
	},
}

// presetAliases accepts the historical and marketing spellings used by the
// existing codebase without duplicating catalog entries.
var presetAliases = map[string]string{
	"azure_openai":     "azure",
	"bigmodel":         "zhipu",
	"fireworks_ai":     "fireworks",
	"gemini":           "google",
	"github_models":    "github",
	"glm":              "zhipu",
	"google_ai_studio": "google",
	"google_genai":     "google",
	"kimi":             "moonshot",
	"mistralai":        "mistral",
	"together_ai":      "together",
	"x_ai":             "xai",
	"z.ai":             "zai",
	"z_ai":             "zai",
	"zhipu_ai":         "zhipu",
}

var presetIndex = func() map[string]int {
	index := make(map[string]int, len(builtinPresets))
	for i, preset := range builtinPresets {
		index[normalizeKey(preset.ID)] = i
	}
	return index
}()

// Presets returns a deep copy of the built-in catalog.
func Presets() []Preset {
	out := make([]Preset, len(builtinPresets))
	for i := range builtinPresets {
		out[i] = clonePreset(builtinPresets[i])
	}
	return out
}

// PresetIDs returns the canonical preset IDs in sorted order.
func PresetIDs() []string {
	ids := make([]string, 0, len(builtinPresets))
	for _, preset := range builtinPresets {
		ids = append(ids, preset.ID)
	}
	sort.Strings(ids)
	return ids
}

// LookupPreset returns the preset with the given canonical ID or alias. The
// returned value is a deep copy; the second result is false when the ID is not
// known.
func LookupPreset(id string) (Preset, bool) {
	key := normalizeKey(id)
	if key == "" {
		return Preset{}, false
	}
	if index, ok := presetIndex[key]; ok {
		return clonePreset(builtinPresets[index]), true
	}
	if canonical, ok := presetAliases[key]; ok {
		if index, ok := presetIndex[canonical]; ok {
			return clonePreset(builtinPresets[index]), true
		}
	}
	return Preset{}, false
}

func normalizeKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func bearerAuth() AuthSpec {
	return AuthSpec{Mode: AuthModeAPIKey, Header: "Authorization", Scheme: "Bearer"}
}

func anthropicAuth() AuthSpec {
	return AuthSpec{
		Mode:   AuthModeAPIKey,
		Header: "x-api-key",
		Headers: []HeaderTemplate{
			{Name: "anthropic-version", Value: "2023-06-01", IfAbsent: true},
		},
	}
}
