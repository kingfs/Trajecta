// Code generated from rules.rl by ragel. DO NOT EDIT.
//
// Regenerate with:
//
//	task generate:secretmask
//
// Source of truth: pkg/secretmask/rules.rl
//
// The machine below and the rule metadata table in rule.go must agree on the
// RuleID set; TestRuleMetadataComplete enforces that.

package secretmask

%%{
machine secretmask;

# --- character classes -------------------------------------------------
# NOTE: inside a Ragel character class a literal '-' must be escaped.
b62    = [0-9A-Za-z];
b62u   = [0-9A-Za-z_];
b62d   = [0-9A-Za-z_\-];
b62dot = [0-9A-Za-z_.\-];
b64    = [0-9A-Za-z+/];
b64eq  = [0-9A-Za-z+/=];
b64u   = [0-9A-Za-z_\-];
hexl   = [0-9a-f];
letters = [A-Za-z];
lalnum = [a-z0-9];

# --- structural --------------------------------------------------------
jwt = 'eyJ' b64u+ '.' 'eyJ' b64u+ '.' b64u+;
pem = '-----BEGIN ' [A-Z ]* 'PRIVATE KEY-----';
# JWE is the five-segment compact serialization. It is listed before jwt
# because on the rare input that is both, the longer match has to win.
jwe = 'eyJ' b64u+ '.' b64u* '.' b64u* '.' b64u* '.' b64u*;
# RFC 6750 spells the scheme with a capital B, but "bearer" is common enough
# that both literals are listed rather than using a case-insensitive literal,
# which would also match a spelling no Prefixes entry could protect.
bearer = ('Bearer ' | 'bearer ') [0-9A-Za-z._~+/\-]+;

# --- personal data -----------------------------------------------------
# A maximal digit run, optionally closed by the X check character. This is the
# coarse half of the two-stage design: the machine finds the run in one pass,
# and scan.go decides which numeric class it is and whether it is real. Keeping
# the pattern coarse means the DFA gains one loop, not one state chain per
# numeric class, and adding a class needs no regeneration at all.
digit_run = [0-9]+ [Xx]?;

# --- OpenAI ------------------------------------------------------------
# T3BlbkFJ ("OpenAI" in base64) is what separates these from the sk- pool.
# Legacy segments allow no '-'/'_'; the project family does.
openai_legacy  = 'sk-' b62+ 'T3BlbkFJ' b62+;
openai_project = 'sk-proj-' b62d+ 'T3BlbkFJ' b62d+;
openai_svc     = ('sk-svcacct-' | 'sk-service-') b62d+ 'T3BlbkFJ' b62d+;
openai_admin   = 'sk-admin-' b62d+ 'T3BlbkFJ' b62d+;
whsec          = 'whsec_' b64+;
openai_ek      = 'ek_' b62u+;

# --- Anthropic ---------------------------------------------------------
anthropic_api   = 'sk-ant-api' [0-9]{2} '-' b62d+;
anthropic_admin = 'sk-ant-admin' [0-9]{2} '-' b62d+;
anthropic_oat   = 'sk-ant-oat' [0-9]{2} '-' b62d+;
anthropic_sid   = 'sk-ant-sid' [0-9]{2} '-' b62d+;

# --- AWS Bedrock -------------------------------------------------------
bedrock_short = 'bedrock-api-key-YmVkcm9jay5hbWF6b25hd3MuY29t' b64eq+;
bedrock_long  = 'ABSK' b64eq+;
anthropic_aws = 'aws-external-anthropic-api-key-' b64eq+;

# --- Google ------------------------------------------------------------
google_apikey  = 'AIza' b62d+;
google_aq      = 'AQ.' b62dot+;
google_gocspx  = 'GOCSPX-' b62d+;
google_ya29    = 'ya29.' b62dot+;
google_refresh = '1//' b64u+;

# --- xAI ---------------------------------------------------------------
xai_token  = 'xai-token-' b62u+;
xai_secret = 'xai-client-secret.' b62u+;
xai_api    = 'xai-' b62u+;

# --- other overseas model vendors --------------------------------------
groq         = 'gsk_' b62+;
replicate    = 'r8_' b62u+;
perplexity   = 'pplx-' b62+;
openrouter   = 'sk-or-v1-' hexl+;
hf_token     = 'hf_' letters+;
hf_org       = 'api_org_' letters+;
jina         = 'jina_' b62+;
voyage       = 'pa-' b62d+;
databricks   = 'dapi' hexl+ ('-' [0-9]+)?;
langfuse_pk  = 'pk-lf-' hexl+ '-' hexl+ '-' hexl+ '-' hexl+ '-' hexl+;
langfuse_sk  = 'sk-lf-' hexl+ '-' hexl+ '-' hexl+ '-' hexl+ '-' hexl+;
langsmith_pt = 'lsv2_pt_' hexl+ '_' hexl+;
langsmith_sk = 'lsv2_sk_' hexl+ '_' hexl+;
cerebras     = 'csk-' b62+;
anyscale     = 'esct_' b62+;
fireworks    = 'fw_' b62+;
fireworks_p  = 'fpk_' b62+;
modal_id     = 'ak-' b62+;
modal_secret = 'as-' b62+;
runpod       = 'rpa_' b62+;
baseten      = 'b10_' b62{8} '.' b62{32};
cartesia     = 'sk_car_' b62+;
openpipe     = 'opk_' b62+;
promptlayer  = 'pl_' hexl+;
nomic        = 'nk-' b62+;
runway       = 'key_' b62+;
traceloop    = 'tl_' b62+;
friendliai   = 'flp_' b62+;
helicone     = 'sk-helicone-' lalnum{7} '-' lalnum{7} '-' lalnum{7} '-' lalnum{7};
lobehub      = 'lobehub-' b62+;
langflow     = 'lf_' b62+;
dify         = ('app-' | 'dataset-') b62{32};
portkey      = 'pk-' b62+;
wandb        = 'wandb_v1_' b62u+ '_' b62u+;
together     = 'tgp_v1_' b62d+;

# --- domestic model vendors -------------------------------------------
# Zhipu is <32 lowercase hex> '.' <secret> with no alphabetic prefix; the
# second segment stays open and is length-checked natively.
zhipu          = hexl{32} '.' b62+;
dashscope_cp   = 'sk-sp-' b62+;
dashscope_temp = 'st-' b62+;
aliyun_akid    = 'LTAI' b62+;
baidu_qianfan  = 'bce-v3/ALTAK-' b62+ '/' b62+;
minimax_api    = 'sk-api-' b62+;
minimax_cp     = 'sk-cp-' b62+;

# --- cloud, VCS and tooling credentials --------------------------------
# Section 8.1 of the format reference: the shapes that can be decided offline.
# Each carries a published checksum, a self-describing field, or a literal that
# ordinary text does not produce, which is what lets them be masked on their own
# evidence instead of on context.
github_classic = 'gh' [pousr] '_' b62{30} b62{6};
# The body is matched as one run and the exact 93-character length is checked
# natively in collector.emit. Spelling out b62u{22} '_' b62u{59} instead is
# ambiguous - '_' is itself a body character - and that ambiguity is what makes
# the generated table explode.
github_fine    = 'github_pat_' b62u+;
aws_akid       = ('AKIA' | 'ASIA') [0-9A-Z]{16};
pypi_token     = 'pypi-AgEIcHlwaS5vcmc' b64u+;
age_key        = 'AGE-SECRET-KEY-1' [0-9A-Z]+;
powershell_ss  = '01000000d08c9ddf0115d1118c7a00c04fc297eb' hexl+;
# Generic pools. Every concrete rule above outranks these on longest match;
# sk_dash and sk_under are declared last on purpose.
sk_dash  = 'sk-' b62+;
sk_under = 'sk_' b62+;

# Scanner entry order is semantically significant: on an equal-length match
# the earliest listed pattern wins, so the T3BlbkFJ shapes must precede the
# sk_dash pool (they match the same span on an OpenAI legacy key).
main := |*
    pem             => { sink.emit(RulePEMPrivateKey, ts, te) };
    jwe             => { sink.emit(RuleJWE, ts, te) };
    jwt             => { sink.emit(RuleJWT, ts, te) };
    bearer          => { sink.emit(RuleBearerToken, ts, te) };
    openai_legacy   => { sink.emit(RuleOpenAILegacy, ts, te) };
    openai_project  => { sink.emit(RuleOpenAIProject, ts, te) };
    openai_svc      => { sink.emit(RuleOpenAIServiceAccount, ts, te) };
    openai_admin    => { sink.emit(RuleOpenAIAdmin, ts, te) };
    whsec           => { sink.emit(RuleWebhookSecret, ts, te) };
    openai_ek       => { sink.emit(RuleOpenAIEphemeralKey, ts, te) };
    anthropic_api   => { sink.emit(RuleAnthropicAPIKey, ts, te) };
    anthropic_admin => { sink.emit(RuleAnthropicAdminKey, ts, te) };
    anthropic_oat   => { sink.emit(RuleAnthropicOAuthToken, ts, te) };
    anthropic_sid   => { sink.emit(RuleAnthropicSessionKey, ts, te) };

    bedrock_short => { sink.emit(RuleBedrockShortTermKey, ts, te) };
    anthropic_aws => { sink.emit(RuleAnthropicAWSKey, ts, te) };
    bedrock_long  => { sink.emit(RuleBedrockLongTermKey, ts, te) };

    google_apikey  => { sink.emit(RuleGoogleAPIKey, ts, te) };
    google_aq      => { sink.emit(RuleGoogleAPIKeyAQ, ts, te) };
    google_gocspx  => { sink.emit(RuleGoogleOAuthClientSecret, ts, te) };
    google_ya29    => { sink.emit(RuleGoogleAccessToken, ts, te) };
    google_refresh => { sink.emit(RuleGoogleRefreshToken, ts, te) };

    xai_token  => { sink.emit(RuleXAIAPIToken, ts, te) };
    xai_secret => { sink.emit(RuleXAIClientSecret, ts, te) };
    xai_api    => { sink.emit(RuleXAIAPIKey, ts, te) };

    openrouter   => { sink.emit(RuleOpenRouterAPIKey, ts, te) };
    langfuse_pk  => { sink.emit(RuleLangfusePublicKey, ts, te) };
    langfuse_sk  => { sink.emit(RuleLangfuseSecretKey, ts, te) };
    langsmith_pt => { sink.emit(RuleLangSmithPersonalToken, ts, te) };
    langsmith_sk => { sink.emit(RuleLangSmithServiceKey, ts, te) };
    baseten      => { sink.emit(RuleBasetenAPIKey, ts, te) };
    helicone     => { sink.emit(RuleHeliconeAPIKey, ts, te) };
    wandb        => { sink.emit(RuleWeightsAndBiasesKey, ts, te) };
    together     => { sink.emit(RuleTogetherAPIKey, ts, te) };

    aliyun_akid    => { sink.emit(RuleAliyunAccessKeyID, ts, te) };
    baidu_qianfan  => { sink.emit(RuleBaiduQianfanAPIKey, ts, te) };
    dashscope_cp   => { sink.emit(RuleDashScopeCodingKey, ts, te) };
    dashscope_temp => { sink.emit(RuleDashScopeTemporaryKey, ts, te) };
    minimax_api    => { sink.emit(RuleMiniMaxAPIKey, ts, te) };
    minimax_cp     => { sink.emit(RuleMiniMaxCodingKey, ts, te) };
    zhipu          => { sink.emit(RuleZhipuAPIKey, ts, te) };

    databricks => { sink.emit(RuleDatabricksPAT, ts, te) };
    groq       => { sink.emit(RuleGroqAPIKey, ts, te) };
    replicate  => { sink.emit(RuleReplicateToken, ts, te) };
    perplexity => { sink.emit(RulePerplexityAPIKey, ts, te) };
    jina       => { sink.emit(RuleJinaAPIKey, ts, te) };
    hf_org     => { sink.emit(RuleHuggingFaceOrgToken, ts, te) };
    hf_token   => { sink.emit(RuleHuggingFaceToken, ts, te) };
    voyage     => { sink.emit(RuleVoyageAPIKey, ts, te) };

    cerebras     => { sink.emit(RuleCerebrasAPIKey, ts, te) };
    anyscale     => { sink.emit(RuleAnyscaleAPIKey, ts, te) };
    fireworks_p  => { sink.emit(RuleFireworksProjectKey, ts, te) };
    fireworks    => { sink.emit(RuleFireworksAPIKey, ts, te) };
    modal_id     => { sink.emit(RuleModalTokenID, ts, te) };
    modal_secret => { sink.emit(RuleModalTokenSecret, ts, te) };
    runpod       => { sink.emit(RuleRunPodAPIKey, ts, te) };
    cartesia     => { sink.emit(RuleCartesiaAPIKey, ts, te) };
    openpipe     => { sink.emit(RuleOpenPipeAPIKey, ts, te) };
    promptlayer  => { sink.emit(RulePromptLayerAPIKey, ts, te) };
    nomic        => { sink.emit(RuleNomicAPIKey, ts, te) };
    runway       => { sink.emit(RuleRunwayAPIKey, ts, te) };
    traceloop    => { sink.emit(RuleTraceloopAPIKey, ts, te) };
    friendliai   => { sink.emit(RuleFriendliAPIKey, ts, te) };
    lobehub      => { sink.emit(RuleLobeHubAPIKey, ts, te) };
    langflow     => { sink.emit(RuleLangflowAPIKey, ts, te) };
    dify         => { sink.emit(RuleDifyAPIKey, ts, te) };
    portkey      => { sink.emit(RulePortkeyAPIKey, ts, te) };

    github_fine   => { sink.emit(RuleGitHubFineGrainedPAT, ts, te) };
    github_classic => { sink.emit(RuleGitHubPAT, ts, te) };
    aws_akid      => { sink.emit(RuleAWSAccessKeyID, ts, te) };
    pypi_token    => { sink.emit(RulePyPIToken, ts, te) };
    age_key       => { sink.emit(RuleAgePrivateKey, ts, te) };
    powershell_ss => { sink.emit(RulePowerShellSecureString, ts, te) };

    sk_under => { sink.emit(RuleSkUnderscoreGeneric, ts, te) };
    sk_dash  => { sink.emit(RuleSkDashGeneric, ts, te) };

    digit_run => { sink.emitDigits(ts, te) };

    any;
*|;
}%%

%% write data;

// scanDFA runs the compiled credential DFA over data, appending every
// lexical candidate to sink. It performs no allocation and no validation:
// length and structure checks live in collector.emit.
func scanDFA(data []byte, sink *collector) {
	cs, p, pe, eof := 0, 0, len(data), len(data)
	ts, te, act := 0, 0, 0
	_ = act
	_ = te
	%% write exec;
	_ = cs
	_ = p
	_ = pe
	_ = eof
	_ = ts
}
