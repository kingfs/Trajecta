package secretmask

// RuleID identifies one credential shape recognized by the compiled DFA.
// RuleUnknown is the zero value and never matches.
type RuleID uint16

// Kind is the credential category a caller usually branches on. Vendor
// disambiguation happens outside this package, from surrounding context.
type Kind uint8

const (
	KindUnknown Kind = iota
	KindPrivateKey
	KindJSONWebToken
	KindAPIKey
	KindOAuthToken
	KindOAuthClientSecret
	KindAccessToken
	KindRefreshToken
	KindAccessKeyID
	KindWebhookSecret
	KindPersonalAccessToken
	KindServiceAccountKey
	KindSessionKey
	KindEphemeralKey
	KindGenericKey
)

// kindNames renders a Kind for logs, audit records and test failures.
var kindNames = [...]string{
	KindUnknown:             "unknown",
	KindPrivateKey:          "private_key",
	KindJSONWebToken:        "jwt",
	KindAPIKey:              "api_key",
	KindOAuthToken:          "oauth_token",
	KindOAuthClientSecret:   "oauth_client_secret",
	KindAccessToken:         "access_token",
	KindRefreshToken:        "refresh_token",
	KindAccessKeyID:         "access_key_id",
	KindWebhookSecret:       "webhook_secret",
	KindPersonalAccessToken: "personal_access_token",
	KindServiceAccountKey:   "service_account_key",
	KindSessionKey:          "session_key",
	KindEphemeralKey:        "ephemeral_key",
	KindGenericKey:          "generic_key",
}

// String names the kind.
func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// Grade records how strong the format evidence is, following the grading used
// by the format reference: A means an official document states the shape, D
// means the shape rests on community observation. Where that reference grades
// a prefix and a length separately, this field carries the weaker of the two.
//
// Grade is evidence about how well the format is documented. Strength below is
// a different axis: how much a match proves on its own.
type Grade uint8

const (
	GradeUnknown Grade = iota
	GradeA
	GradeB
	GradeC
	GradeD
)

// Strength says how much a lexical match proves by itself, which is what
// decides whether a caller may act on the result automatically.
type Strength uint8

const (
	// StrengthGeneric means the anchor is a token that occurs in ordinary text
	// and code, so a match alone proves very little: the sk- and sk_ pools,
	// ek_, ak-, as-, st-, pa-, nk-, pk-, pl_, tl_, lf_, key-, AQ., 1//,
	// app-/dataset-, and similar. These are reported, but only acted on when
	// an offline validator proves them or the surrounding window names a
	// credential.
	StrengthGeneric Strength = iota
	// StrengthAnchored means the anchor is vendor-specific but the body is a
	// free-form run, so an accident is unlikely yet not impossible.
	StrengthAnchored
	// StrengthStrong means either the shape embeds a vendor watermark or its
	// exact length plus prefix is decisive, so a match is conclusive.
	StrengthStrong
)

// Rule is the static metadata attached to a RuleID.
type Rule struct {
	// Name is a stable identifier such as "anthropic.api_key". It is part of
	// the package API: callers persist and match on it.
	Name string
	// Vendor is the issuing organization, empty for vendor-neutral shapes.
	Vendor string
	Kind   Kind
	Grade  Grade
	// Strength is how conclusive this rule's lexical shape is on its own.
	Strength Strength
	// MinLen and MaxLen bound the total match length in bytes, inclusive.
	// MaxLen 0 means unbounded.
	MinLen int
	MaxLen int
	// Validator names an offline structural check applied to the matched
	// bytes. A rule with a validator only reports matches that pass it, and a
	// pass is by itself enough to treat the match as conclusive.
	Validator string
	// Keywords are vendor-specific strings whose presence near the match
	// counts as context evidence. The global credential vocabulary in
	// context.go applies to every rule as well.
	Keywords []string

	// Prefixes are the literal heads this rule can match, used when rendering
	// a fake: the longest one the value actually starts with is kept verbatim
	// so the stand-in still looks like the vendor's credential to a tool that
	// only matches on shape. A rule that matches no literal head leaves this
	// empty and is rendered from its first byte.
	Prefixes []string
	// PrefixSkip keeps this many bytes verbatim immediately after the matched
	// prefix, for heads a plain literal cannot spell out, such as the two
	// version digits in sk-ant-api03-.
	PrefixSkip int
	// Literals are vendor watermarks inside a match that a fake must preserve,
	// because the shape check downstream looks for them.
	Literals []string
}

const (
	RuleUnknown RuleID = iota

	// Structural, vendor-neutral shapes.
	RulePEMPrivateKey
	RuleJWT
	RuleWebhookSecret

	// OpenAI.
	RuleOpenAILegacy
	RuleOpenAIProject
	RuleOpenAIServiceAccount
	RuleOpenAIAdmin
	RuleOpenAIEphemeralKey

	// Anthropic.
	RuleAnthropicAPIKey
	RuleAnthropicAdminKey
	RuleAnthropicOAuthToken
	RuleAnthropicSessionKey
	RuleAnthropicAWSKey

	// Google.
	RuleGoogleAPIKey
	RuleGoogleAPIKeyAQ
	RuleGoogleOAuthClientSecret
	RuleGoogleAccessToken
	RuleGoogleRefreshToken

	// AWS Bedrock.
	RuleBedrockLongTermKey
	RuleBedrockShortTermKey

	// xAI.
	RuleXAIAPIToken
	RuleXAIClientSecret
	RuleXAIAPIKey

	// Other overseas model vendors.
	RuleOpenRouterAPIKey
	RuleDatabricksPAT
	RuleLangfusePublicKey
	RuleLangfuseSecretKey
	RuleLangSmithPersonalToken
	RuleLangSmithServiceKey
	RuleBasetenAPIKey
	RuleHeliconeAPIKey
	RuleWeightsAndBiasesKey
	RuleTogetherAPIKey
	RuleGroqAPIKey
	RuleReplicateToken
	RulePerplexityAPIKey
	RuleJinaAPIKey
	RuleHuggingFaceToken
	RuleHuggingFaceOrgToken
	RuleVoyageAPIKey
	RuleCerebrasAPIKey
	RuleAnyscaleAPIKey
	RuleFireworksAPIKey
	RuleFireworksProjectKey
	RuleModalTokenID
	RuleModalTokenSecret
	RuleRunPodAPIKey
	RuleCartesiaAPIKey
	RuleOpenPipeAPIKey
	RulePromptLayerAPIKey
	RuleNomicAPIKey
	RuleRunwayAPIKey
	RuleTraceloopAPIKey
	RuleFriendliAPIKey
	RuleLobeHubAPIKey
	RuleLangflowAPIKey
	RuleDifyAPIKey
	RulePortkeyAPIKey

	// Domestic model vendors.
	RuleZhipuAPIKey
	RuleDashScopeCodingKey
	RuleDashScopeTemporaryKey
	RuleAliyunAccessKeyID
	RuleBaiduQianfanAPIKey
	RuleMiniMaxAPIKey
	RuleMiniMaxCodingKey

	// Generic pools. These match a prefix shared by many vendors plus a
	// charset run; the owning vendor can only be resolved from context.
	RuleSkDashGeneric
	RuleSkUnderscoreGeneric

	ruleIDCount
)

// ruleTable is indexed by RuleID.
var ruleTable = [ruleIDCount]Rule{
	RulePEMPrivateKey: {Name: "structural.pem_private_key", Kind: KindPrivateKey, Grade: GradeA, Strength: StrengthStrong, MinLen: 27, Prefixes: []string{"-----BEGIN "}},
	RuleJWT:           {Name: "structural.jwt", Kind: KindJSONWebToken, Grade: GradeA, Strength: StrengthAnchored, MinLen: 40, Validator: validatorJWT},

	RuleWebhookSecret: {Name: "webhook.signing_secret", Kind: KindWebhookSecret, Grade: GradeD, Strength: StrengthAnchored, MinLen: 40, Prefixes: []string{"whsec_"}},

	RuleOpenAILegacy:         {Name: "openai.legacy_key", Vendor: "OpenAI", Kind: KindAPIKey, Grade: GradeA, Strength: StrengthStrong, MinLen: 51, MaxLen: 51, Prefixes: []string{"sk-"}, Literals: []string{"T3BlbkFJ"}},
	RuleOpenAIProject:        {Name: "openai.project_key", Vendor: "OpenAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 132, MaxLen: 164, Prefixes: []string{"sk-proj-"}, Literals: []string{"T3BlbkFJ"}},
	RuleOpenAIServiceAccount: {Name: "openai.service_account_key", Vendor: "OpenAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 135, MaxLen: 167, Prefixes: []string{"sk-svcacct-", "sk-service-"}, Literals: []string{"T3BlbkFJ"}},
	RuleOpenAIAdmin:          {Name: "openai.admin_key", Vendor: "OpenAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 133, MaxLen: 133, Prefixes: []string{"sk-admin-"}, Literals: []string{"T3BlbkFJ"}},
	// ek_ values are opaque and unpublished, and the two-letter anchor collides
	// with ordinary identifiers such as ek_from_official_host, so the floor
	// stays well above anything that is not a credential.
	RuleOpenAIEphemeralKey: {Name: "openai.ephemeral_key", Vendor: "OpenAI", Kind: KindEphemeralKey, Grade: GradeD, Strength: StrengthGeneric, MinLen: 30, Prefixes: []string{"ek_"}},

	RuleAnthropicAPIKey:     {Name: "anthropic.api_key", Vendor: "Anthropic", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 108, MaxLen: 108, Validator: validatorAnthropicKey, Keywords: []string{"anthropic", "claude"}, Prefixes: []string{"sk-ant-api"}, PrefixSkip: 3},
	RuleAnthropicAdminKey:   {Name: "anthropic.admin_key", Vendor: "Anthropic", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 110, MaxLen: 110, Validator: validatorAnthropicKey, Keywords: []string{"anthropic", "claude"}, Prefixes: []string{"sk-ant-admin"}, PrefixSkip: 3},
	RuleAnthropicOAuthToken: {Name: "anthropic.oauth_token", Vendor: "Anthropic", Kind: KindOAuthToken, Grade: GradeD, Strength: StrengthAnchored, MinLen: 40, Keywords: []string{"anthropic", "claude", "oauth"}, Prefixes: []string{"sk-ant-oat"}, PrefixSkip: 3},
	RuleAnthropicSessionKey: {Name: "anthropic.session_key", Vendor: "Anthropic", Kind: KindSessionKey, Grade: GradeD, Strength: StrengthAnchored, MinLen: 60, Keywords: []string{"anthropic", "claude", "sessionkey"}, Prefixes: []string{"sk-ant-sid"}, PrefixSkip: 3},
	RuleAnthropicAWSKey:     {Name: "anthropic.aws_platform_key", Vendor: "Anthropic", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 100, Keywords: []string{"anthropic", "claude", "bedrock"}, Prefixes: []string{"aws-external-anthropic-api-key-"}},

	RuleGoogleAPIKey:            {Name: "google.api_key", Vendor: "Google", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 39, MaxLen: 39, Keywords: []string{"google", "gemini", "generativelanguage"}, Prefixes: []string{"AIza"}},
	RuleGoogleAPIKeyAQ:          {Name: "google.api_key_aq", Vendor: "Google", Kind: KindAPIKey, Grade: GradeD, Strength: StrengthGeneric, MinLen: 30, Keywords: []string{"google", "gemini"}, Prefixes: []string{"AQ."}},
	RuleGoogleOAuthClientSecret: {Name: "google.oauth_client_secret", Vendor: "Google", Kind: KindOAuthClientSecret, Grade: GradeC, Strength: StrengthStrong, MinLen: 35, MaxLen: 35, Keywords: []string{"google", "oauth"}, Prefixes: []string{"GOCSPX-"}},
	RuleGoogleAccessToken:       {Name: "google.access_token", Vendor: "Google", Kind: KindAccessToken, Grade: GradeC, Strength: StrengthAnchored, MinLen: 50, Keywords: []string{"google", "oauth"}, Prefixes: []string{"ya29."}},
	RuleGoogleRefreshToken:      {Name: "google.refresh_token", Vendor: "Google", Kind: KindRefreshToken, Grade: GradeC, Strength: StrengthGeneric, MinLen: 40, Keywords: []string{"google", "oauth"}, Prefixes: []string{"1//"}},

	RuleBedrockLongTermKey:  {Name: "bedrock.long_term_key", Vendor: "AWS", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 113, MaxLen: 273, Keywords: []string{"bedrock"}, Prefixes: []string{"ABSK"}},
	RuleBedrockShortTermKey: {Name: "bedrock.short_term_key", Vendor: "AWS", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 100, Keywords: []string{"bedrock"}, Prefixes: []string{"bedrock-api-key-YmVkcm9jay5hbWF6b25hd3MuY29t"}},

	RuleXAIAPIToken:     {Name: "xai.api_token", Vendor: "xAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 40, Keywords: []string{"xai", "grok"}, Prefixes: []string{"xai-token-"}},
	RuleXAIClientSecret: {Name: "xai.client_secret", Vendor: "xAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 38, Keywords: []string{"xai", "grok"}, Prefixes: []string{"xai-client-secret."}},
	RuleXAIAPIKey:       {Name: "xai.api_key", Vendor: "xAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 44, MaxLen: 104, Keywords: []string{"xai", "grok"}, Prefixes: []string{"xai-"}},

	RuleOpenRouterAPIKey:       {Name: "openrouter.api_key", Vendor: "OpenRouter", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 73, MaxLen: 73, Keywords: []string{"openrouter"}, Prefixes: []string{"sk-or-v1-"}},
	RuleDatabricksPAT:          {Name: "databricks.pat", Vendor: "Databricks", Kind: KindPersonalAccessToken, Grade: GradeC, Strength: StrengthAnchored, MinLen: 36, MaxLen: 45, Keywords: []string{"databricks"}, Prefixes: []string{"dapi"}},
	RuleLangfusePublicKey:      {Name: "langfuse.public_key", Vendor: "Langfuse", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 42, MaxLen: 42, Keywords: []string{"langfuse"}, Prefixes: []string{"pk-lf-"}},
	RuleLangfuseSecretKey:      {Name: "langfuse.secret_key", Vendor: "Langfuse", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 42, MaxLen: 42, Keywords: []string{"langfuse"}, Prefixes: []string{"sk-lf-"}},
	RuleLangSmithPersonalToken: {Name: "langsmith.personal_token", Vendor: "LangSmith", Kind: KindPersonalAccessToken, Grade: GradeC, Strength: StrengthAnchored, MinLen: 30, MaxLen: 120, Keywords: []string{"langsmith", "langchain"}, Prefixes: []string{"lsv2_pt_"}},
	RuleLangSmithServiceKey:    {Name: "langsmith.service_key", Vendor: "LangSmith", Kind: KindServiceAccountKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 30, MaxLen: 120, Keywords: []string{"langsmith", "langchain"}, Prefixes: []string{"lsv2_sk_"}},
	RuleBasetenAPIKey:          {Name: "baseten.api_key", Vendor: "Baseten", Kind: KindAPIKey, Grade: GradeB, Strength: StrengthStrong, MinLen: 45, MaxLen: 45, Keywords: []string{"baseten"}, Prefixes: []string{"b10_"}},
	RuleHeliconeAPIKey:         {Name: "helicone.api_key", Vendor: "Helicone", Kind: KindAPIKey, Grade: GradeD, Strength: StrengthStrong, MinLen: 43, MaxLen: 43, Keywords: []string{"helicone"}, Prefixes: []string{"sk-helicone-"}},
	RuleWeightsAndBiasesKey:    {Name: "wandb.api_key", Vendor: "Weights & Biases", Kind: KindAPIKey, Grade: GradeD, Strength: StrengthStrong, MinLen: 40, Keywords: []string{"wandb"}, Prefixes: []string{"wandb_v1_"}},
	RuleTogetherAPIKey:         {Name: "together.api_key", Vendor: "Together AI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 40, MaxLen: 80, Keywords: []string{"together"}, Prefixes: []string{"tgp_v1_"}},

	RuleGroqAPIKey:          {Name: "groq.api_key", Vendor: "Groq", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 50, MaxLen: 60, Keywords: []string{"groq"}, Prefixes: []string{"gsk_"}},
	RuleReplicateToken:      {Name: "replicate.api_token", Vendor: "Replicate", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 40, MaxLen: 40, Keywords: []string{"replicate"}, Prefixes: []string{"r8_"}},
	RulePerplexityAPIKey:    {Name: "perplexity.api_key", Vendor: "Perplexity", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 53, MaxLen: 53, Keywords: []string{"perplexity"}, Prefixes: []string{"pplx-"}},
	RuleJinaAPIKey:          {Name: "jina.api_key", Vendor: "Jina AI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 65, MaxLen: 65, Keywords: []string{"jina"}, Prefixes: []string{"jina_"}},
	RuleHuggingFaceToken:    {Name: "huggingface.token", Vendor: "HuggingFace", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 37, MaxLen: 37, Keywords: []string{"huggingface", "hf_token", "hf_hub"}, Prefixes: []string{"hf_"}},
	RuleHuggingFaceOrgToken: {Name: "huggingface.org_token", Vendor: "HuggingFace", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 42, MaxLen: 42, Keywords: []string{"huggingface", "api_org"}, Prefixes: []string{"api_org_"}},
	RuleVoyageAPIKey:        {Name: "voyage.api_key", Vendor: "Voyage AI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 27, Keywords: []string{"voyage"}, Prefixes: []string{"pa-"}},

	RuleCerebrasAPIKey:      {Name: "cerebras.api_key", Vendor: "Cerebras", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 27, Keywords: []string{"cerebras"}, Prefixes: []string{"csk-"}},
	RuleAnyscaleAPIKey:      {Name: "anyscale.api_key", Vendor: "Anyscale", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 23, Keywords: []string{"anyscale"}, Prefixes: []string{"esct_"}},
	RuleFireworksAPIKey:     {Name: "fireworks.api_key", Vendor: "Fireworks AI", Kind: KindAPIKey, Grade: GradeD, Strength: StrengthAnchored, MinLen: 23, Keywords: []string{"fireworks"}, Prefixes: []string{"fw_"}},
	RuleFireworksProjectKey: {Name: "fireworks.project_key", Vendor: "Fireworks AI", Kind: KindAPIKey, Grade: GradeD, Strength: StrengthAnchored, MinLen: 23, Keywords: []string{"fireworks"}, Prefixes: []string{"fpk_"}},
	RuleModalTokenID:        {Name: "modal.token_id", Vendor: "Modal", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 23, Keywords: []string{"modal"}, Prefixes: []string{"ak-"}},
	RuleModalTokenSecret:    {Name: "modal.token_secret", Vendor: "Modal", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 23, Keywords: []string{"modal"}, Prefixes: []string{"as-"}},
	RuleRunPodAPIKey:        {Name: "runpod.api_key", Vendor: "RunPod", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, Keywords: []string{"runpod"}, Prefixes: []string{"rpa_"}},
	RuleCartesiaAPIKey:      {Name: "cartesia.api_key", Vendor: "Cartesia", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, Keywords: []string{"cartesia"}, Prefixes: []string{"sk_car_"}},
	RuleOpenPipeAPIKey:      {Name: "openpipe.api_key", Vendor: "OpenPipe", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 35, Keywords: []string{"openpipe"}, Prefixes: []string{"opk_"}},
	RulePromptLayerAPIKey:   {Name: "promptlayer.api_key", Vendor: "PromptLayer", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"promptlayer"}, Prefixes: []string{"pl_"}},
	RuleNomicAPIKey:         {Name: "nomic.api_key", Vendor: "Nomic", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 43, Keywords: []string{"nomic"}, Prefixes: []string{"nk-"}},
	RuleRunwayAPIKey:        {Name: "runway.api_key", Vendor: "Runway ML", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 51, Keywords: []string{"runway"}, Prefixes: []string{"key_"}},
	RuleTraceloopAPIKey:     {Name: "traceloop.api_key", Vendor: "Traceloop", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"traceloop"}, Prefixes: []string{"tl_"}},
	RuleFriendliAPIKey:      {Name: "friendliai.api_key", Vendor: "FriendliAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 35, Keywords: []string{"friendli"}, Prefixes: []string{"flp_"}},
	RuleLobeHubAPIKey:       {Name: "lobehub.api_key", Vendor: "LobeHub", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 40, Keywords: []string{"lobehub"}, Prefixes: []string{"lobehub-"}},
	RuleLangflowAPIKey:      {Name: "langflow.api_key", Vendor: "Langflow", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"langflow"}, Prefixes: []string{"lf_"}},
	RuleDifyAPIKey:          {Name: "dify.api_key", Vendor: "Dify", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 36, MaxLen: 40, Keywords: []string{"dify"}, Prefixes: []string{"dataset-", "app-"}},
	RulePortkeyAPIKey:       {Name: "portkey.api_key", Vendor: "Portkey", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"portkey"}, Prefixes: []string{"pk-"}},

	RuleZhipuAPIKey:           {Name: "zhipu.api_key", Vendor: "Zhipu AI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 49, Keywords: []string{"zhipu", "bigmodel", "glm", "open.bigmodel.cn", "api.z.ai"}},
	RuleDashScopeCodingKey:    {Name: "dashscope.coding_key", Vendor: "Alibaba Cloud", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, Keywords: []string{"dashscope", "qwen", "bailian", "aliyuncs"}, Prefixes: []string{"sk-sp-"}},
	RuleDashScopeTemporaryKey: {Name: "dashscope.temporary_key", Vendor: "Alibaba Cloud", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 20, Keywords: []string{"dashscope", "qwen", "bailian", "aliyuncs"}, Prefixes: []string{"st-"}},
	RuleAliyunAccessKeyID:     {Name: "aliyun.access_key_id", Vendor: "Alibaba Cloud", Kind: KindAccessKeyID, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, MaxLen: 28, Keywords: []string{"aliyun", "alibaba", "accesskeyid", "ram"}, Prefixes: []string{"LTAI"}},
	RuleBaiduQianfanAPIKey:    {Name: "baidu.qianfan_api_key", Vendor: "Baidu", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 35, Keywords: []string{"qianfan", "baidu", "bce"}, Prefixes: []string{"bce-v3/ALTAK-"}},
	RuleMiniMaxAPIKey:         {Name: "minimax.api_key", Vendor: "MiniMax", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, Keywords: []string{"minimax", "minimaxi"}, Prefixes: []string{"sk-api-"}},
	RuleMiniMaxCodingKey:      {Name: "minimax.coding_key", Vendor: "MiniMax", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, Keywords: []string{"minimax", "minimaxi"}, Prefixes: []string{"sk-cp-"}},

	RuleSkDashGeneric:       {Name: "generic.sk_dash", Kind: KindGenericKey, Grade: GradeD, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"deepseek", "dashscope", "moonshot", "kimi", "siliconflow"}, Prefixes: []string{"sk-"}},
	RuleSkUnderscoreGeneric: {Name: "generic.sk_underscore", Kind: KindGenericKey, Grade: GradeD, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"novita", "elevenlabs"}, Prefixes: []string{"sk_"}},
}

// RuleFor returns the metadata for id. It reports false for RuleUnknown and
// for out-of-range ids.
func RuleFor(id RuleID) (Rule, bool) {
	if id == RuleUnknown || id >= ruleIDCount {
		return Rule{}, false
	}
	return ruleTable[id], true
}

// Rules returns the metadata of every rule known to the package, in RuleID
// order. Callers use it to build documentation, coverage reports and
// vendor-level filters.
func Rules() []Rule {
	out := make([]Rule, 0, ruleIDCount-1)
	for id := RuleUnknown + 1; id < ruleIDCount; id++ {
		out = append(out, ruleTable[id])
	}
	return out
}
