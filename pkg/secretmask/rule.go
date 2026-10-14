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
	RulePEMPrivateKey: {Name: "structural.pem_private_key", Kind: KindPrivateKey, Grade: GradeA, Strength: StrengthStrong, MinLen: 27},
	RuleJWT:           {Name: "structural.jwt", Kind: KindJSONWebToken, Grade: GradeA, Strength: StrengthAnchored, MinLen: 40, Validator: validatorJWT},

	RuleWebhookSecret: {Name: "webhook.signing_secret", Kind: KindWebhookSecret, Grade: GradeD, Strength: StrengthAnchored, MinLen: 40},

	RuleOpenAILegacy:         {Name: "openai.legacy_key", Vendor: "OpenAI", Kind: KindAPIKey, Grade: GradeA, Strength: StrengthStrong, MinLen: 51, MaxLen: 51},
	RuleOpenAIProject:        {Name: "openai.project_key", Vendor: "OpenAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 132, MaxLen: 164},
	RuleOpenAIServiceAccount: {Name: "openai.service_account_key", Vendor: "OpenAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 135, MaxLen: 167},
	RuleOpenAIAdmin:          {Name: "openai.admin_key", Vendor: "OpenAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 133, MaxLen: 133},
	// ek_ values are opaque and unpublished, and the two-letter anchor collides
	// with ordinary identifiers such as ek_from_official_host, so the floor
	// stays well above anything that is not a credential.
	RuleOpenAIEphemeralKey: {Name: "openai.ephemeral_key", Vendor: "OpenAI", Kind: KindEphemeralKey, Grade: GradeD, Strength: StrengthGeneric, MinLen: 30},

	RuleAnthropicAPIKey:     {Name: "anthropic.api_key", Vendor: "Anthropic", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 108, MaxLen: 108, Validator: validatorAnthropicKey, Keywords: []string{"anthropic", "claude"}},
	RuleAnthropicAdminKey:   {Name: "anthropic.admin_key", Vendor: "Anthropic", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 110, MaxLen: 110, Validator: validatorAnthropicKey, Keywords: []string{"anthropic", "claude"}},
	RuleAnthropicOAuthToken: {Name: "anthropic.oauth_token", Vendor: "Anthropic", Kind: KindOAuthToken, Grade: GradeD, Strength: StrengthAnchored, MinLen: 40, Keywords: []string{"anthropic", "claude", "oauth"}},
	RuleAnthropicSessionKey: {Name: "anthropic.session_key", Vendor: "Anthropic", Kind: KindSessionKey, Grade: GradeD, Strength: StrengthAnchored, MinLen: 60, Keywords: []string{"anthropic", "claude", "sessionkey"}},
	RuleAnthropicAWSKey:     {Name: "anthropic.aws_platform_key", Vendor: "Anthropic", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 100, Keywords: []string{"anthropic", "claude", "bedrock"}},

	RuleGoogleAPIKey:            {Name: "google.api_key", Vendor: "Google", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 39, MaxLen: 39, Keywords: []string{"google", "gemini", "generativelanguage"}},
	RuleGoogleAPIKeyAQ:          {Name: "google.api_key_aq", Vendor: "Google", Kind: KindAPIKey, Grade: GradeD, Strength: StrengthGeneric, MinLen: 30, Keywords: []string{"google", "gemini"}},
	RuleGoogleOAuthClientSecret: {Name: "google.oauth_client_secret", Vendor: "Google", Kind: KindOAuthClientSecret, Grade: GradeC, Strength: StrengthStrong, MinLen: 35, MaxLen: 35, Keywords: []string{"google", "oauth"}},
	RuleGoogleAccessToken:       {Name: "google.access_token", Vendor: "Google", Kind: KindAccessToken, Grade: GradeC, Strength: StrengthAnchored, MinLen: 50, Keywords: []string{"google", "oauth"}},
	RuleGoogleRefreshToken:      {Name: "google.refresh_token", Vendor: "Google", Kind: KindRefreshToken, Grade: GradeC, Strength: StrengthGeneric, MinLen: 40, Keywords: []string{"google", "oauth"}},

	RuleBedrockLongTermKey:  {Name: "bedrock.long_term_key", Vendor: "AWS", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 113, MaxLen: 273, Keywords: []string{"bedrock"}},
	RuleBedrockShortTermKey: {Name: "bedrock.short_term_key", Vendor: "AWS", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 100, Keywords: []string{"bedrock"}},

	RuleXAIAPIToken:     {Name: "xai.api_token", Vendor: "xAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 40, Keywords: []string{"xai", "grok"}},
	RuleXAIClientSecret: {Name: "xai.client_secret", Vendor: "xAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 38, Keywords: []string{"xai", "grok"}},
	RuleXAIAPIKey:       {Name: "xai.api_key", Vendor: "xAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 44, MaxLen: 104, Keywords: []string{"xai", "grok"}},

	RuleOpenRouterAPIKey:       {Name: "openrouter.api_key", Vendor: "OpenRouter", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 73, MaxLen: 73, Keywords: []string{"openrouter"}},
	RuleDatabricksPAT:          {Name: "databricks.pat", Vendor: "Databricks", Kind: KindPersonalAccessToken, Grade: GradeC, Strength: StrengthAnchored, MinLen: 36, MaxLen: 45, Keywords: []string{"databricks"}},
	RuleLangfusePublicKey:      {Name: "langfuse.public_key", Vendor: "Langfuse", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 42, MaxLen: 42, Keywords: []string{"langfuse"}},
	RuleLangfuseSecretKey:      {Name: "langfuse.secret_key", Vendor: "Langfuse", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 42, MaxLen: 42, Keywords: []string{"langfuse"}},
	RuleLangSmithPersonalToken: {Name: "langsmith.personal_token", Vendor: "LangSmith", Kind: KindPersonalAccessToken, Grade: GradeC, Strength: StrengthAnchored, MinLen: 30, MaxLen: 120, Keywords: []string{"langsmith", "langchain"}},
	RuleLangSmithServiceKey:    {Name: "langsmith.service_key", Vendor: "LangSmith", Kind: KindServiceAccountKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 30, MaxLen: 120, Keywords: []string{"langsmith", "langchain"}},
	RuleBasetenAPIKey:          {Name: "baseten.api_key", Vendor: "Baseten", Kind: KindAPIKey, Grade: GradeB, Strength: StrengthStrong, MinLen: 45, MaxLen: 45, Keywords: []string{"baseten"}},
	RuleHeliconeAPIKey:         {Name: "helicone.api_key", Vendor: "Helicone", Kind: KindAPIKey, Grade: GradeD, Strength: StrengthStrong, MinLen: 43, MaxLen: 43, Keywords: []string{"helicone"}},
	RuleWeightsAndBiasesKey:    {Name: "wandb.api_key", Vendor: "Weights & Biases", Kind: KindAPIKey, Grade: GradeD, Strength: StrengthStrong, MinLen: 40, Keywords: []string{"wandb"}},
	RuleTogetherAPIKey:         {Name: "together.api_key", Vendor: "Together AI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 40, MaxLen: 80, Keywords: []string{"together"}},

	RuleGroqAPIKey:          {Name: "groq.api_key", Vendor: "Groq", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 50, MaxLen: 60, Keywords: []string{"groq"}},
	RuleReplicateToken:      {Name: "replicate.api_token", Vendor: "Replicate", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 40, MaxLen: 40, Keywords: []string{"replicate"}},
	RulePerplexityAPIKey:    {Name: "perplexity.api_key", Vendor: "Perplexity", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 53, MaxLen: 53, Keywords: []string{"perplexity"}},
	RuleJinaAPIKey:          {Name: "jina.api_key", Vendor: "Jina AI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 65, MaxLen: 65, Keywords: []string{"jina"}},
	RuleHuggingFaceToken:    {Name: "huggingface.token", Vendor: "HuggingFace", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 37, MaxLen: 37, Keywords: []string{"huggingface", "hf_token", "hf_hub"}},
	RuleHuggingFaceOrgToken: {Name: "huggingface.org_token", Vendor: "HuggingFace", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 42, MaxLen: 42, Keywords: []string{"huggingface", "api_org"}},
	RuleVoyageAPIKey:        {Name: "voyage.api_key", Vendor: "Voyage AI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 27, Keywords: []string{"voyage"}},

	RuleCerebrasAPIKey:      {Name: "cerebras.api_key", Vendor: "Cerebras", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 27, Keywords: []string{"cerebras"}},
	RuleAnyscaleAPIKey:      {Name: "anyscale.api_key", Vendor: "Anyscale", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 23, Keywords: []string{"anyscale"}},
	RuleFireworksAPIKey:     {Name: "fireworks.api_key", Vendor: "Fireworks AI", Kind: KindAPIKey, Grade: GradeD, Strength: StrengthAnchored, MinLen: 23, Keywords: []string{"fireworks"}},
	RuleFireworksProjectKey: {Name: "fireworks.project_key", Vendor: "Fireworks AI", Kind: KindAPIKey, Grade: GradeD, Strength: StrengthAnchored, MinLen: 23, Keywords: []string{"fireworks"}},
	RuleModalTokenID:        {Name: "modal.token_id", Vendor: "Modal", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 23, Keywords: []string{"modal"}},
	RuleModalTokenSecret:    {Name: "modal.token_secret", Vendor: "Modal", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 23, Keywords: []string{"modal"}},
	RuleRunPodAPIKey:        {Name: "runpod.api_key", Vendor: "RunPod", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, Keywords: []string{"runpod"}},
	RuleCartesiaAPIKey:      {Name: "cartesia.api_key", Vendor: "Cartesia", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, Keywords: []string{"cartesia"}},
	RuleOpenPipeAPIKey:      {Name: "openpipe.api_key", Vendor: "OpenPipe", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 35, Keywords: []string{"openpipe"}},
	RulePromptLayerAPIKey:   {Name: "promptlayer.api_key", Vendor: "PromptLayer", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"promptlayer"}},
	RuleNomicAPIKey:         {Name: "nomic.api_key", Vendor: "Nomic", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 43, Keywords: []string{"nomic"}},
	RuleRunwayAPIKey:        {Name: "runway.api_key", Vendor: "Runway ML", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 51, Keywords: []string{"runway"}},
	RuleTraceloopAPIKey:     {Name: "traceloop.api_key", Vendor: "Traceloop", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"traceloop"}},
	RuleFriendliAPIKey:      {Name: "friendliai.api_key", Vendor: "FriendliAI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 35, Keywords: []string{"friendli"}},
	RuleLobeHubAPIKey:       {Name: "lobehub.api_key", Vendor: "LobeHub", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 40, Keywords: []string{"lobehub"}},
	RuleLangflowAPIKey:      {Name: "langflow.api_key", Vendor: "Langflow", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"langflow"}},
	RuleDifyAPIKey:          {Name: "dify.api_key", Vendor: "Dify", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 36, MaxLen: 40, Keywords: []string{"dify"}},
	RulePortkeyAPIKey:       {Name: "portkey.api_key", Vendor: "Portkey", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"portkey"}},

	RuleZhipuAPIKey:           {Name: "zhipu.api_key", Vendor: "Zhipu AI", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 49, Keywords: []string{"zhipu", "bigmodel", "glm", "open.bigmodel.cn", "api.z.ai"}},
	RuleDashScopeCodingKey:    {Name: "dashscope.coding_key", Vendor: "Alibaba Cloud", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, Keywords: []string{"dashscope", "qwen", "bailian", "aliyuncs"}},
	RuleDashScopeTemporaryKey: {Name: "dashscope.temporary_key", Vendor: "Alibaba Cloud", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthGeneric, MinLen: 20, Keywords: []string{"dashscope", "qwen", "bailian", "aliyuncs"}},
	RuleAliyunAccessKeyID:     {Name: "aliyun.access_key_id", Vendor: "Alibaba Cloud", Kind: KindAccessKeyID, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, MaxLen: 28, Keywords: []string{"aliyun", "alibaba", "accesskeyid", "ram"}},
	RuleBaiduQianfanAPIKey:    {Name: "baidu.qianfan_api_key", Vendor: "Baidu", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthStrong, MinLen: 35, Keywords: []string{"qianfan", "baidu", "bce"}},
	RuleMiniMaxAPIKey:         {Name: "minimax.api_key", Vendor: "MiniMax", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, Keywords: []string{"minimax", "minimaxi"}},
	RuleMiniMaxCodingKey:      {Name: "minimax.coding_key", Vendor: "MiniMax", Kind: KindAPIKey, Grade: GradeC, Strength: StrengthAnchored, MinLen: 20, Keywords: []string{"minimax", "minimaxi"}},

	RuleSkDashGeneric:       {Name: "generic.sk_dash", Kind: KindGenericKey, Grade: GradeD, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"deepseek", "dashscope", "moonshot", "kimi", "siliconflow"}},
	RuleSkUnderscoreGeneric: {Name: "generic.sk_underscore", Kind: KindGenericKey, Grade: GradeD, Strength: StrengthGeneric, MinLen: 35, Keywords: []string{"novita", "elevenlabs"}},
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
