package upstream

import (
	"sort"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// builtinPresets is the package-level catalog. Presets and LookupPreset return
// deep copies so callers cannot mutate it.
//
// Catalog organization (channel plugins):
//   - one visible preset = one plugin = one vendor site (vendor + region);
//   - billing plans (Coding Plan, Token Plan, membership) are PlanVariants of
//     their vendor preset, never separate presets;
//   - protocol surfaces (OpenAI-compatible, Anthropic Messages, ...) are
//     per-protocol Endpoints of one preset, served by one channel with one key;
//   - legacy plan/protocol presets stay resolvable for existing channels but
//     are Hidden and point at their canonical Replacement.
var builtinPresets = []Preset{
	{
		ID:     "alibaba",
		Name:   "Alibaba Bailian-China",
		NameZh: "阿里百炼-中国",
		Kind:   KindVendor,
		Region: "cn",
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://dashscope.aliyuncs.com/compatible-mode/v1",
			xir.ProtocolMessages:        "https://dashscope.aliyuncs.com/apps/anthropic",
		},
		Auth: bearerAuth(),
		Plans: []PlanVariant{
			{
				ID:            "coding",
				Name:          "Coding Plan",
				NameZh:        "Coding Plan",
				Description:   "Subscription coding plan with a dedicated sk-sp- key and isolated coding endpoints. Bundles Qwen, GLM, Kimi and MiniMax models; quota is consumed only on the coding endpoints.",
				DescriptionZh: "订阅制编程套餐：专用 sk-sp- key 与独立 coding 端点，整合千问、GLM、Kimi、MiniMax 等模型；仅 coding 端点消耗套餐额度。",
				Endpoints: map[xir.Protocol]string{
					xir.ProtocolChatCompletions: "https://coding.dashscope.aliyuncs.com/v1",
					xir.ProtocolMessages:        "https://coding.dashscope.aliyuncs.com/apps/anthropic",
				},
				KeyPrefix: "sk-sp-",
				Coding:    true,
				Models:    &ModelSource{NoList: true},
				DocsURL:   "https://help.aliyun.com/zh/model-studio/coding-plan",
				KeysURL:   "https://bailian.console.aliyun.com/cn-beijing/subscription/coding-plan",
			},
			{
				ID:            "token",
				Name:          "Token Plan",
				NameZh:        "Token Plan",
				Description:   "Token Plan subscription for interactive coding tools; dedicated sk-sp- key, separate from Coding Plan and pay-as-you-go.",
				DescriptionZh: "Token Plan 订阅套餐，面向交互式编程工具；专用 sk-sp- key，与 Coding Plan 和按量付费相互独立。",
				Endpoints: map[xir.Protocol]string{
					xir.ProtocolChatCompletions: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
					xir.ProtocolMessages:        "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic",
				},
				KeyPrefix: "sk-sp-",
				Models:    &ModelSource{NoList: true},
				DocsURL:   "https://help.aliyun.com/zh/model-studio/token-plan-team-quickstart",
				KeysURL:   "https://bailian.console.aliyun.com",
			},
		},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://help.aliyun.com/zh/model-studio/",
		KeysURL: "https://bailian.console.aliyun.com",
		Notes:   "Pay-as-you-go DashScope endpoints. Coding Plan and Token Plan are subscription variants with dedicated sk-sp- keys and isolated endpoints; never mix them with the pay-as-you-go key.",
		NotesZh: "按量付费 DashScope 端点。Coding Plan / Token Plan 为订阅套餐，使用专用 sk-sp- key 与独立端点，勿与按量 key 混用。",
	},
	{
		ID:     "alibaba_intl",
		Name:   "Alibaba Bailian-International",
		NameZh: "阿里百炼-国际",
		Kind:   KindVendor,
		Region: "intl",
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
			xir.ProtocolMessages:        "https://dashscope-intl.aliyuncs.com/apps/anthropic",
		},
		Auth: bearerAuth(),
		Plans: []PlanVariant{
			{
				ID:            "coding",
				Name:          "Coding Plan",
				NameZh:        "Coding Plan",
				Description:   "International subscription coding plan with a dedicated sk-sp- key and isolated coding endpoints.",
				DescriptionZh: "国际站订阅制编程套餐：专用 sk-sp- key 与独立 coding 端点。",
				Endpoints: map[xir.Protocol]string{
					xir.ProtocolChatCompletions: "https://coding-intl.dashscope.aliyuncs.com/v1",
					xir.ProtocolMessages:        "https://coding-intl.dashscope.aliyuncs.com/apps/anthropic",
				},
				KeyPrefix: "sk-sp-",
				Coding:    true,
				Models:    &ModelSource{NoList: true},
				DocsURL:   "https://www.alibabacloud.com/help/en/model-studio/coding-plan",
				KeysURL:   "https://bailian.console.alibabacloud.com",
			},
			{
				ID:            "token",
				Name:          "Token Plan",
				NameZh:        "Token Plan",
				Description:   "International Token Plan subscription (Singapore) for interactive coding tools; dedicated sk-sp- key.",
				DescriptionZh: "国际站 Token Plan 订阅套餐（新加坡），面向交互式编程工具；专用 sk-sp- key。",
				Endpoints: map[xir.Protocol]string{
					xir.ProtocolChatCompletions: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1",
					xir.ProtocolMessages:        "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic",
				},
				KeyPrefix: "sk-sp-",
				Models:    &ModelSource{NoList: true},
				DocsURL:   "https://www.alibabacloud.com/help/en/model-studio/token-plan-team-quickstart",
				KeysURL:   "https://bailian.console.alibabacloud.com",
			},
		},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://www.alibabacloud.com/help/en/model-studio/",
		KeysURL: "https://bailian.console.alibabacloud.com",
		Notes:   "International (Singapore) Model Studio endpoints. Coding Plan and Token Plan use dedicated sk-sp- keys and isolated endpoints; API keys are region-bound and cannot cross regions.",
		NotesZh: "国际站（新加坡）Model Studio 端点。Coding Plan / Token Plan 使用专用 sk-sp- key 与独立端点；各地域 API key 相互独立，不能跨地域使用。",
	},
	{
		ID:     "anthropic",
		Name:   "Anthropic",
		NameZh: "Anthropic",
		Kind:   KindVendor,
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
		NotesZh: "Messages API 位于 API 根路径，客户端追加 /v1/messages；请求缺少 anthropic-version 时按默认头补齐。",
	},
	{
		ID:     "azure",
		Name:   "Azure OpenAI",
		NameZh: "Azure OpenAI",
		Kind:   KindVendor,
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
		NotesZh: "需填写资源端点，例如 https://RESOURCE.openai.azure.com/openai/v1；设置 APIVersion 时以 api-version 查询参数附加。",
	},
	{
		ID:     "cerebras",
		Name:   "Cerebras",
		NameZh: "Cerebras",
		Kind:   KindVendor,
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
		NotesZh: "仅 OpenAI 兼容 Chat Completions；此处不声明 Responses。",
	},
	{
		ID:     "deepseek",
		Name:   "DeepSeek",
		NameZh: "DeepSeek",
		Kind:   KindVendor,
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
		NotesZh: "同一渠道同时提供 OpenAI Chat/Responses 与 Anthropic 兼容 Messages 端点；Anthropic base 由客户端追加 /v1/messages。",
	},
	{
		ID:     "fireworks",
		Name:   "Fireworks AI",
		NameZh: "Fireworks AI",
		Kind:   KindVendor,
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
		NotesZh: "Anthropic 兼容 base 止于 /inference，SDK 追加 /v1/messages。",
	},
	{
		ID:     "github",
		Name:   "GitHub Models",
		NameZh: "GitHub Models",
		Kind:   KindVendor,
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
		NotesZh: "OpenAI 兼容推理面。模型目录在 /inference 之外的独立端点，因此不声明 ListPath。",
	},
	{
		ID:     "google",
		Name:   "Google Gemini",
		NameZh: "Google Gemini",
		Kind:   KindVendor,
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
		NotesZh: "OpenAI 兼容面挂载在 /v1beta/openai；原生 Gemini 面使用 /v1beta/models/{model}:generateContent。",
	},
	{
		ID:     "groq",
		Name:   "Groq",
		NameZh: "Groq",
		Kind:   KindVendor,
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
		NotesZh: "Chat Completions 与 Responses 均挂载在 /openai/v1。",
	},
	{
		ID:     "kimi_coding",
		Name:   "Kimi Code",
		Kind:   KindVendor,
		Hidden: true,
		Replacement: &PresetReplacement{
			PresetID: "moonshot",
			Plan:     "kimi-code",
		},
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolMessages: "https://api.kimi.ai/coding",
		},
		Auth:    bearerAuth(),
		Models:  ModelSource{NoList: true},
		DocsURL: "https://platform.kimi.com/docs",
		KeysURL: "https://platform.kimi.com/console",
		Notes:   "Kimi Code membership uses the Anthropic-compatible coding endpoint. This is separate from the Moonshot open-platform API key.",
	},
	{
		ID:     "minimax",
		Name:   "MiniMax",
		NameZh: "MiniMax",
		Kind:   KindVendor,
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
		NotesZh: "OpenAI 兼容与 Anthropic 兼容双端点；不假定公开模型列表路径。",
	},
	{
		ID:     "mistral",
		Name:   "Mistral AI",
		NameZh: "Mistral AI",
		Kind:   KindVendor,
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
		NotesZh: "仅 OpenAI 兼容 Chat Completions。",
	},
	{
		ID:     "moonshot",
		Name:   "Moonshot / Kimi",
		NameZh: "月之暗面 Kimi",
		Kind:   KindVendor,
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.moonshot.cn/v1",
			xir.ProtocolMessages:        "https://api.moonshot.cn/anthropic",
		},
		Auth: bearerAuth(),
		Plans: []PlanVariant{
			{
				ID:            "kimi-code",
				Name:          "Kimi Code (membership)",
				NameZh:        "Kimi Code 会员",
				Description:   "Kimi Code membership key against the Anthropic-compatible coding endpoint; separate from the open-platform API key.",
				DescriptionZh: "Kimi Code 会员 key，使用 Anthropic 兼容的 coding 端点；与开放平台 API key 相互独立。",
				Endpoints: map[xir.Protocol]string{
					xir.ProtocolMessages: "https://api.kimi.ai/coding",
				},
				ModelFilter: "kimi-*",
				Coding:      true,
				Models:      &ModelSource{NoList: true},
			},
		},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://platform.kimi.com/docs",
		KeysURL: "https://platform.kimi.com/console/api-keys",
		Notes:   "The Kimi open platform uses api.moonshot.cn (overseas: api.moonshot.ai). The kimi-code plan uses a membership key against the Anthropic-compatible coding endpoint.",
		NotesZh: "Kimi 开放平台使用 api.moonshot.cn（海外：api.moonshot.ai）；kimi-code 套餐使用会员 key 访问 Anthropic 兼容 coding 端点。",
	},
	{
		ID:        "ollama",
		Name:      "Ollama",
		NameZh:    "Ollama",
		Kind:      KindLocal,
		Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "http://localhost:11434/v1"},
		Auth:      AuthSpec{Mode: AuthModeNone},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.ollama.com",
		Notes:   "Local default. Override the endpoint for a remote Ollama host.",
		NotesZh: "本地默认端点；远程 Ollama 主机请覆盖端点。",
	},
	{
		ID:     "openai",
		Name:   "OpenAI",
		NameZh: "OpenAI",
		Kind:   KindVendor,
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
		ID:     "openrouter",
		Name:   "OpenRouter",
		NameZh: "OpenRouter",
		Kind:   KindRelay,
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
		NotesZh: "Anthropic 兼容 base 为 https://openrouter.ai/api，SDK 追加 /v1/messages；预设默认不声明 Responses。",
	},
	{
		ID:     "together",
		Name:   "Together AI",
		NameZh: "Together AI",
		Kind:   KindVendor,
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
		NotesZh: "仅 OpenAI 兼容 Chat Completions。",
	},
	{
		ID:     "vertex",
		Name:   "Google Vertex AI",
		NameZh: "Google Vertex AI",
		Kind:   KindVendor,
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
		NotesZh: "预设端点为 Express Mode（全局主机、API key）。标准 Vertex 请覆盖 Gemini 端点为 https://LOCATION-aiplatform.googleapis.com/v1/projects/PROJECT/locations/LOCATION/publishers/google/models 并提供 OAuth access token；Project 与 Location 必须成对设置。",
	},
	{
		ID:        "vllm",
		Name:      "vLLM",
		NameZh:    "vLLM",
		Kind:      KindLocal,
		Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "http://localhost:8000/v1"},
		Auth:      AuthSpec{Mode: AuthModeNone},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.vllm.ai",
		Notes:   "Local default. vLLM's --api-key option, when enabled, is supplied through Config.Auth or a credential.",
		NotesZh: "本地默认端点；启用 vLLM --api-key 时通过 Config.Auth 或凭据提供。",
	},
	{
		ID:     "xai",
		Name:   "xAI",
		NameZh: "xAI",
		Kind:   KindVendor,
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
		ID:     "qwen_coding",
		Name:   "Qwen Coding Plan (China)",
		Kind:   KindVendor,
		Hidden: true,
		Replacement: &PresetReplacement{
			PresetID: "alibaba",
			Plan:     "coding",
		},
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://coding.dashscope.aliyuncs.com/v1",
			xir.ProtocolMessages:        "https://coding.dashscope.aliyuncs.com/apps/anthropic",
		},
		Auth:    bearerAuth(),
		Models:  ModelSource{NoList: true},
		DocsURL: "https://help.aliyun.com/zh/model-studio/coding-plan",
		KeysURL: "https://bailian.console.aliyun.com",
		Notes:   "Official Qwen Coding Plan uses a dedicated sk-sp- API key and coding endpoint. This is not a general DashScope key; do not mix it with dashscope.aliyuncs.com.",
	},
	{
		ID:     "qwen_coding_anthropic",
		Name:   "Qwen Coding Plan (China, Anthropic)",
		Kind:   KindVendor,
		Hidden: true,
		Replacement: &PresetReplacement{
			PresetID: "alibaba",
			Plan:     "coding",
		},
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolMessages: "https://coding.dashscope.aliyuncs.com/apps/anthropic",
		},
		Auth:    bearerAuth(),
		Models:  ModelSource{NoList: true},
		DocsURL: "https://help.aliyun.com/zh/model-studio/coding-plan",
		KeysURL: "https://bailian.console.aliyun.com",
		Notes:   "Anthropic Messages surface for Claude Code; uses the official Qwen Coding Plan sk-sp- key.",
	},
	{
		ID:     "qwen_coding_intl_anthropic",
		Name:   "Qwen Coding Plan (International, Anthropic)",
		Kind:   KindVendor,
		Hidden: true,
		Replacement: &PresetReplacement{
			PresetID: "alibaba_intl",
			Plan:     "coding",
		},
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolMessages: "https://coding-intl.dashscope.aliyuncs.com/apps/anthropic",
		},
		Auth:    bearerAuth(),
		Models:  ModelSource{NoList: true},
		DocsURL: "https://www.alibabacloud.com/help/en/model-studio/coding-plan",
		KeysURL: "https://bailian.console.alibabacloud.com",
		Notes:   "International Anthropic Messages surface for Claude Code; uses the official Qwen Coding Plan sk-sp- key.",
	},
	{
		ID:     "zai_coding_anthropic",
		Name:   "Z.ai GLM Coding Plan (Anthropic)",
		Kind:   KindVendor,
		Hidden: true,
		Replacement: &PresetReplacement{
			PresetID: "zai",
			Plan:     "coding",
		},
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolMessages: "https://api.z.ai/api/anthropic",
		},
		Auth:    bearerAuth(),
		Models:  ModelSource{NoList: true},
		DocsURL: "https://docs.z.ai",
		KeysURL: "https://z.ai/manage-apikey/apikey-list",
		Notes:   "Anthropic Messages surface for Claude Code; uses the GLM Coding Plan API key.",
	},
	{
		ID:     "zhipu_coding_anthropic",
		Name:   "Zhipu GLM Coding Plan (Anthropic)",
		Kind:   KindVendor,
		Hidden: true,
		Replacement: &PresetReplacement{
			PresetID: "zhipu",
			Plan:     "coding",
		},
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolMessages: "https://open.bigmodel.cn/api/anthropic",
		},
		Auth:    bearerAuth(),
		Models:  ModelSource{NoList: true},
		DocsURL: "https://docs.bigmodel.cn/cn/coding-plan/overview",
		KeysURL: "https://bigmodel.cn/usercenter/proj-mgmt/apikeys",
		Notes:   "Anthropic Messages surface for Claude Code; uses the GLM Coding Plan API key.",
	},
	{
		ID:     "qwen_token_plan",
		Name:   "Qwen Token Plan (China)",
		Kind:   KindVendor,
		Hidden: true,
		Replacement: &PresetReplacement{
			PresetID: "alibaba",
			Plan:     "token",
		},
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
			xir.ProtocolMessages:        "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic",
		},
		Auth:    bearerAuth(),
		Models:  ModelSource{NoList: true},
		DocsURL: "https://help.aliyun.com/zh/model-studio/token-plan-team-quickstart",
		KeysURL: "https://bailian.console.aliyun.com",
		Notes:   "Official Token Plan endpoint for interactive coding tools; uses a dedicated sk-sp- key and is separate from Coding Plan and pay-as-you-go.",
	},
	{
		ID:     "qwen_token_plan_intl",
		Name:   "Qwen Token Plan (International)",
		Kind:   KindVendor,
		Hidden: true,
		Replacement: &PresetReplacement{
			PresetID: "alibaba_intl",
			Plan:     "token",
		},
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1",
			xir.ProtocolMessages:        "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic",
		},
		Auth:    bearerAuth(),
		Models:  ModelSource{NoList: true},
		DocsURL: "https://www.alibabacloud.com/help/en/model-studio/token-plan-team-quickstart",
		KeysURL: "https://bailian.console.alibabacloud.com",
		Notes:   "Official international Token Plan endpoint for interactive coding tools; uses a dedicated sk-sp- key.",
	},
	{
		ID:     "qwen_coding_intl",
		Name:   "Qwen Coding Plan (International)",
		Kind:   KindVendor,
		Hidden: true,
		Replacement: &PresetReplacement{
			PresetID: "alibaba_intl",
			Plan:     "coding",
		},
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://coding-intl.dashscope.aliyuncs.com/v1",
			xir.ProtocolMessages:        "https://coding-intl.dashscope.aliyuncs.com/apps/anthropic",
		},
		Auth:    bearerAuth(),
		Models:  ModelSource{NoList: true},
		DocsURL: "https://www.alibabacloud.com/help/en/model-studio/coding-plan",
		KeysURL: "https://bailian.console.alibabacloud.com",
		Notes:   "Official international Qwen Coding Plan endpoint; uses a dedicated sk-sp- API key.",
	},
	{
		ID:     "zai_coding",
		Name:   "Z.ai GLM Coding Plan",
		Kind:   KindVendor,
		Hidden: true,
		Replacement: &PresetReplacement{
			PresetID: "zai",
			Plan:     "coding",
		},
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.z.ai/api/coding/paas/v4",
			xir.ProtocolResponses:       "https://api.z.ai/api/v1",
			xir.ProtocolMessages:        "https://api.z.ai/api/anthropic",
		},
		Auth:    bearerAuth(),
		Models:  ModelSource{ListPath: "/models", Protocol: xir.ProtocolChatCompletions},
		DocsURL: "https://docs.z.ai",
		KeysURL: "https://z.ai/manage-apikey/apikey-list",
		Notes:   "GLM Coding Plan on Z.ai; the coding endpoint consumes plan quota rather than API balance. Uses a Z.ai API key (id.secret).",
	},
	{
		ID:     "zhipu_coding",
		Name:   "Zhipu GLM Coding Plan",
		Kind:   KindVendor,
		Hidden: true,
		Replacement: &PresetReplacement{
			PresetID: "zhipu",
			Plan:     "coding",
		},
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://open.bigmodel.cn/api/coding/paas/v4",
			xir.ProtocolResponses:       "https://open.bigmodel.cn/api/v1",
			xir.ProtocolMessages:        "https://open.bigmodel.cn/api/anthropic",
		},
		Auth:    bearerAuth(),
		Models:  ModelSource{ListPath: "/models", Protocol: xir.ProtocolChatCompletions},
		DocsURL: "https://docs.bigmodel.cn/cn/coding-plan/overview",
		KeysURL: "https://bigmodel.cn/usercenter/proj-mgmt/apikeys",
		Notes:   "GLM Coding Plan on Zhipu BigModel; the coding endpoint consumes plan quota. Uses a BigModel API key.",
	},
	{
		ID:     "zai",
		Name:   "Z.ai",
		NameZh: "Z.ai",
		Kind:   KindVendor,
		Region: "intl",
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://api.z.ai/api/paas/v4",
			xir.ProtocolMessages:        "https://api.z.ai/api/anthropic",
		},
		Auth: bearerAuth(),
		Plans: []PlanVariant{
			{
				ID:            "coding",
				Name:          "GLM Coding Plan",
				NameZh:        "GLM Coding Plan",
				Description:   "GLM Coding Plan: the coding endpoint consumes plan quota rather than API balance.",
				DescriptionZh: "GLM Coding Plan：coding 端点消耗套餐额度而非 API 余额。",
				Endpoints: map[xir.Protocol]string{
					xir.ProtocolChatCompletions: "https://api.z.ai/api/coding/paas/v4",
					xir.ProtocolResponses:       "https://api.z.ai/api/v1",
					xir.ProtocolMessages:        "https://api.z.ai/api/anthropic",
				},
				ModelFilter: "glm-*",
				Coding:      true,
			},
		},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.z.ai",
		KeysURL: "https://z.ai/manage-apikey/apikey-list",
		Notes:   "The coding plan uses a dedicated Chat Completions base and exposes Responses at /api/v1.",
		NotesZh: "Coding Plan 使用专用 Chat Completions base，并在 /api/v1 提供 Responses。",
	},
	{
		ID:     "zhipu",
		Name:   "Zhipu BigModel",
		NameZh: "智谱 BigModel",
		Kind:   KindVendor,
		Region: "cn",
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://open.bigmodel.cn/api/paas/v4",
			xir.ProtocolMessages:        "https://open.bigmodel.cn/api/anthropic",
		},
		Auth: bearerAuth(),
		Plans: []PlanVariant{
			{
				ID:            "coding",
				Name:          "GLM Coding Plan",
				NameZh:        "GLM Coding Plan",
				Description:   "GLM Coding Plan: the coding endpoint consumes plan quota rather than API balance.",
				DescriptionZh: "GLM Coding Plan：coding 端点消耗套餐额度而非 API 余额。",
				Endpoints: map[xir.Protocol]string{
					xir.ProtocolChatCompletions: "https://open.bigmodel.cn/api/coding/paas/v4",
					xir.ProtocolResponses:       "https://open.bigmodel.cn/api/v1",
					xir.ProtocolMessages:        "https://open.bigmodel.cn/api/anthropic",
				},
				ModelFilter: "glm-*",
				Coding:      true,
			},
		},
		Models: ModelSource{
			ListPath: "/models",
			Protocol: xir.ProtocolChatCompletions,
		},
		DocsURL: "https://docs.bigmodel.cn",
		KeysURL: "https://bigmodel.cn/usercenter/proj-mgmt/apikeys",
		Notes:   "The coding plan uses a dedicated Chat Completions base and exposes Responses at /api/v1.",
		NotesZh: "Coding Plan 使用专用 Chat Completions base，并在 /api/v1 提供 Responses。",
	},
}

// presetAliases accepts the historical and marketing spellings used by the
// existing codebase without duplicating catalog entries.
var presetAliases = map[string]string{
	"azure_openai":     "azure",
	"alibabacloud":     "alibaba_intl",
	"bailian":          "alibaba",
	"bigmodel":         "zhipu",
	"dashscope":        "alibaba",
	"fireworks_ai":     "fireworks",
	"gemini":           "google",
	"github_models":    "github",
	"glm":              "zhipu",
	"google_ai_studio": "google",
	"google_genai":     "google",
	"kimi":             "moonshot",
	"mistralai":        "mistral",
	"model_studio":     "alibaba_intl",
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
