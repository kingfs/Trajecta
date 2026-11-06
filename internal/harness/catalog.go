package harness

import "strings"

// catalog is ordered specific-to-generic. Detect stops at the first
// high-confidence match, so a product rule must come before any rule whose
// marker it would also satisfy (Qwen Code before Claude Code, because the
// former deliberately impersonates the latter; Codex (VS Code) before Codex CLI;
// every product before the gateway rules).
//
// A rule's confidence describes how uniquely its signals name the product:
//
//   - high   - a product-identifying User-Agent token or a vendor header only
//     that product sets.
//   - medium - a combination of header signals that corroborate each other
//     without naming the product alone (for example a session id plus an
//     editor version).
//   - low    - reserved for a future fallback class that many products
//     share; no catalogue rule returns it today. Gateway rules (LiteLLM,
//     Portkey, OpenRouter) return high, because the agent behind them is
//     invisible but the gateway's own UA token or private header names the
//     gateway unambiguously - the low-confidence SDK fallback is the one
//     that stays unlabelled (no ID) instead.
//
// Evidence always names the signal (`user-agent:claude-cli`,
// `header:originator:codex_exec`), never its value, so detection cannot copy
// header bytes into the trace index. No rule reads the body: identity lives
// in the headers, and a harness without a product header stays unrecognized.
//
// The markers below were read out of the harnesses' own published code; each
// rule names its source in a comment. A signal a vendor documents as
// user-overridable (ANTHROPIC_CUSTOM_HEADERS, GEMINI_CLI_CUSTOM_HEADERS,
// LITELLM_USER_AGENT, CODEX_INTERNAL_ORIGINATOR_OVERRIDE) is therefore evidence,
// not proof: the classification is an observation about the traffic, not a
// security boundary.
var catalog = []rule{
	// ---------------------------------------------------------------------
	// Anthropic-family agent CLIs
	// ---------------------------------------------------------------------
	{
		// Qwen Code is a Gemini CLI fork that deliberately impersonates Claude
		// Code when it talks to a non-anthropic.com base URL: it sends
		// `claude-cli/...` + `x-app: cli` (QwenLM/qwen-code,
		// anthropicContentGenerator.ts). With no body read to disambiguate,
		// that spoofed traffic is labelled claude-code; only its own product
		// UA names Qwen Code. The rule still runs first so a genuine
		// QwenCode UA is never stolen by a later rule.
		id:   "qwen-code",
		name: "Qwen Code",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("qwencode") || agent.productPrefix("qwen-code") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:qwencode")
			}
			return matchResult{}, false
		},
	},
	{
		id:   "claude-code",
		name: "Claude Code",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			// cli.js installs vx() = `claude-cli/${VERSION} (external,
			// ${CLAUDE_CODE_ENTRYPOINT})` and Ke() = `claude-code/${VERSION}`.
			if agent.productPrefix("claude-cli") || agent.productPrefix("claude-code") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:claude-cli")
			}
			if strings.EqualFold(view.headerValue("x-app"), "cli") {
				if view.headerContains("anthropic-beta", "claude-code") {
					return detected(ConfidenceHigh, "", "header:x-app=cli", "header:anthropic-beta=claude-code")
				}
			}
			if view.headerContains("anthropic-beta", "claude-code") {
				return detected(ConfidenceHigh, "", "header:anthropic-beta=claude-code")
			}
			return matchResult{}, false
		},
	},

	// ---------------------------------------------------------------------
	// OpenAI-family agent products
	// ---------------------------------------------------------------------
	{
		// openai/codex's is_first_party_originator lists codex_vscode, and the
		// extension is the same Responses agent as the CLI (same system prompt),
		// so its originator must be checked before the CLI rule.
		id:   "codex-vscode",
		name: "Codex (VS Code)",
		kind: KindIDE,
		match: func(view requestView) (matchResult, bool) {
			if strings.EqualFold(view.headerFirst("originator"), "codex_vscode") {
				return detected(ConfidenceHigh, "", "header:originator:codex_vscode")
			}
			return matchResult{}, false
		},
	},
	{
		// is_first_party_chat_originator lists codex_atlas and
		// codex_chatgpt_desktop for the desktop/chat surfaces.
		id:   "codex-app",
		name: "Codex App",
		kind: KindAgentApp,
		match: func(view requestView) (matchResult, bool) {
			originator := strings.ToLower(view.headerFirst("originator"))
			switch originator {
			case "codex_atlas", "codex_chatgpt_desktop":
				return detected(ConfidenceHigh, "", "header:originator:"+originator)
			}
			return matchResult{}, false
		},
	},
	{
		// codex-rs/core/src/default_client.rs: DEFAULT_ORIGINATOR is
		// codex_cli_rs, the UA is
		// `{originator}/{version} ({os} {os_version}; {arch}) {terminal_token}`,
		// and the client owns the private `x-codex-*` header namespace. The
		// non-interactive `codex exec` entry point switches the originator to
		// codex_exec before anything else reads it (set_default_originator),
		// so exec traffic arrives as `codex_exec/<version> (...) unknown
		// (codex_exec; <version>)` with `originator: codex_exec` - a mode of
		// the same CLI, not a different product.
		id:   "codex-cli",
		name: "Codex CLI",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			originator := strings.ToLower(view.headerFirst("originator"))
			// Cline's Codex-account passthrough sends originator: cline and a
			// Cline UA over the Codex wire; it belongs to the Cline rule, not to
			// the Codex header namespace or the Codex prompt fallback.
			if originator == "cline" || agent.productPrefix("cline") {
				return matchResult{}, false
			}
			if agent.productPrefix("codex_cli_rs") || agent.productPrefix("codex-cli") || agent.productPrefix("codex-tui") || agent.productPrefix("codex_exec") {
				// The evidence names the token that actually arrived, so
				// codex-tui and codex_exec traffic does not read as codex_cli_rs.
				evidence := []string{"user-agent:" + agent.product}
				if originator != "" {
					evidence = append(evidence, "header:originator:"+originator)
				}
				return detected(ConfidenceHigh, agent.versionOrEmpty(), evidence...)
			}
			switch originator {
			case "codex_cli_rs", "codex-tui", "codex-tui_rs", "codex_exec":
				return detected(ConfidenceHigh, "", "header:originator:"+originator)
			}
			if name := view.headerNameWithPrefix("x-codex-"); name != "" {
				return detected(ConfidenceHigh, "", "header:"+name)
			}
			return matchResult{}, false
		},
	},

	// ---------------------------------------------------------------------
	// Google-family agent products
	// ---------------------------------------------------------------------
	{
		// Gemini Code Assist for VS Code identifies its surface inside the UA
		// (`CloudCodeVSCode/... proxy_client=geminicli`), so it is a different
		// client from the terminal Gemini CLI even though the prompt is shared.
		id:   "gemini-code-assist",
		name: "Gemini Code Assist",
		kind: KindIDE,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("cloudcodevscode") || agent.contains("proxy_client=geminicli") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:cloudcode-vscode", "user-agent:proxy_client=geminicli")
			}
			return matchResult{}, false
		},
	},
	{
		// gemini-cli contentGenerator.ts: UA is
		// `GeminiCLI{,-<clientName>}/{version}/{model} ({platform}; {arch}; {surface})`.
		id:   "gemini-cli",
		name: "Gemini CLI",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("geminicli") || agent.productPrefix("gemini-cli") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:geminicli")
			}
			return matchResult{}, false
		},
	},

	// ---------------------------------------------------------------------
	// Terminal/IDE agent products
	// ---------------------------------------------------------------------
	{
		id:   "opencode",
		name: "OpenCode",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("opencode") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:opencode")
			}
			if name := view.headerNameWithPrefix("x-opencode-"); name != "" {
				return detected(ConfidenceHigh, "", "header:"+name)
			}
			return matchResult{}, false
		},
	},
	{
		// deepseek-ai/deepseek-harness packages/llm/llm/src/attribution.ts:
		// every provider request carries the static APP_IDENTITY as
		// `deepseek-harness/<version> (+https://github.com/deepseek-ai/deepseek-harness)`,
		// and attributionHeaders sends that one header and nothing else, so
		// the UA product token is the whole fingerprint. The version comes
		// from the package manifest, so it tracks releases on its own.
		id:   "deepseek-harness",
		name: "DeepSeek Harness",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("deepseek-harness") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:deepseek-harness")
			}
			return matchResult{}, false
		},
	},
	{
		// Factory Droid (the `droid` binary) sends
		// `factory-cli/<version>` as its User-Agent; droid2api's User-Agent
		// Updater tracks that exact version string, and Factory's own release
		// downloads live under `factory-cli/releases/`.
		id:   "factory-droid",
		name: "Factory Droid",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("factory-cli") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:factory-cli")
			}
			return matchResult{}, false
		},
	},
	{
		// Moonshot's Kimi family has two generations that share the X-Msh-*
		// device-fingerprint header namespace. The archived Python kimi-cli
		// sends `KimiCLI/<version>` (kimi_cli/constant.py: get_user_agent)
		// with `X-Msh-Platform: kimi_cli`; the current Kimi Code CLI sends
		// `kimi-code-cli/<version>` with `X-Msh-Platform: kimi_code_cli`
		// (moonshotai/kimi-code packages/oauth corrects the platform value
		// and sends the product User-Agent). Note that pi and other agents
		// spoof `KimiCLI/...` against api.kimi.com - appearance-based
		// labelling cannot tell a spoof from the product, by design.
		id:   "kimi-code",
		name: "Kimi Code",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("kimicli") || agent.productPrefix("kimi-code-cli") || agent.productPrefix("kimi-cli") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:"+agent.product)
			}
			if view.headerValue("x-msh-platform") != "" {
				return detected(ConfidenceHigh, "", "header:x-msh-platform")
			}
			if name := view.headerNameWithPrefix("x-msh-"); name != "" {
				return detected(ConfidenceHigh, "", "header:"+name)
			}
			return matchResult{}, false
		},
	},
	{
		// earendil-works/pi packages/ai/src/utils/identity.ts: the detailed
		// mode sends `pi/<version> (<platform>; <runtime>/<ver>; <arch>)` and
		// the short mode sends the bare token `pi`. The token is two letters,
		// so it is matched exactly - a prefix match would let any `pi*`
		// product claim the label.
		id:   "pi",
		name: "Pi",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.product == "pi" {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:pi")
			}
			return matchResult{}, false
		},
	},
	{
		id:   "cursor",
		name: "Cursor",
		kind: KindIDE,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("cursor") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:cursor")
			}
			for _, header := range []string{"x-cursor-client-version", "x-cursor-checksum", "x-cursor-config-version"} {
				if view.headerValue(header) != "" {
					return detected(ConfidenceHigh, "", "header:"+header)
				}
			}
			return matchResult{}, false
		},
	},
	{
		id:   "windsurf",
		name: "Windsurf",
		kind: KindIDE,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("windsurf") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:windsurf")
			}
			if agent.productPrefix("codeium") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:codeium")
			}
			for _, header := range []string{"x-codeium-request-id", "x-codeium-client-version"} {
				if view.headerValue(header) != "" {
					return detected(ConfidenceHigh, "", "header:"+header)
				}
			}
			return matchResult{}, false
		},
	},
	{
		// @vscode/copilot-api's header mixin: Copilot-Integration-Id,
		// Editor-Version, Editor-Plugin-Version: copilot-chat/<version>,
		// VScode-SessionId, VScode-MachineId, Editor-Device-Id.
		id:   "github-copilot",
		name: "GitHub Copilot Chat",
		kind: KindIDE,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("githubcopilotchat") || agent.productPrefix("github-copilot") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:github-copilot-chat")
			}
			if view.headerValue("copilot-integration-id") != "" {
				return detected(ConfidenceHigh, "", "header:copilot-integration-id")
			}
			if view.headerContains("editor-plugin-version", "copilot-chat") {
				return detected(ConfidenceHigh, "", "header:editor-plugin-version=copilot-chat")
			}
			if view.headerValue("vscode-sessionid") != "" && view.headerValue("editor-version") != "" {
				return detected(ConfidenceMedium, "", "header:vscode-sessionid", "header:editor-version")
			}
			return matchResult{}, false
		},
	},
	{
		id:   "continue",
		name: "Continue",
		kind: KindIDE,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("continue") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:continue")
			}
			if view.headerValue("x-continue-session-id") != "" {
				return detected(ConfidenceHigh, "", "header:x-continue-session-id")
			}
			return matchResult{}, false
		},
	},
	{
		// cline/cline sdk/packages/llms: UA Cline/<version>, HTTP-Referer
		// https://cline.bot, X-Title Cline, X-CLIENT-TYPE, X-TASK-ID, and for
		// Codex-account traffic originator: cline + session_id.
		id:   "cline",
		name: "Cline",
		kind: KindIDE,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("cline") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:cline")
			}
			if view.headerContains("http-referer", "cline.bot") ||
				(strings.EqualFold(view.headerFirst("x-title"), "cline") && view.headerValue("x-client-type") != "") ||
				(strings.EqualFold(view.headerFirst("originator"), "cline") && view.headerValue("session_id") != "") {
				return detected(ConfidenceHigh, "", "header:http-referer=cline.bot")
			}
			return matchResult{}, false
		},
	},
	{
		// RooCodeInc/Roo-Code src/api/providers/constants.ts: UA
		// RooCode/<version>, HTTP-Referer RooVetGit/Roo-Cline, X-Title Roo Code.
		id:   "roo-code",
		name: "Roo Code",
		kind: KindIDE,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("roo-code") || agent.productPrefix("roocode") || agent.productPrefix("roo code") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:roo-code")
			}
			if view.headerContains("http-referer", "RooVetGit") ||
				(strings.EqualFold(view.headerFirst("x-title"), "roo code") && view.headerValue("x-client-type") != "") {
				return detected(ConfidenceHigh, "", "header:http-referer=RooVetGit")
			}
			return matchResult{}, false
		},
	},
	{
		// Kilo Code's provider layer does not set a product UA (only the
		// anthropic headers). Some Kilo builds and UA presets do send
		// `Kilo-Code/<version>`; without it Kilo traffic is unrecognized by
		// design, because detection no longer reads the body where its mode
		// prompt used to be.
		id:   "kilo-code",
		name: "Kilo Code",
		kind: KindIDE,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("kilo-code") || agent.productPrefix("kilocode") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:kilo-code")
			}
			return matchResult{}, false
		},
	},
	{
		// aider has no User-Agent of its own: its HTTP layer is LiteLLM, which
		// forces `litellm/<version>`. The product rule is therefore a UA match
		// only, kept for builds that do set one; the gateway rule labels the
		// traffic that actually arrives through LiteLLM.
		id:   "aider",
		name: "Aider",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("aider") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:aider")
			}
			return matchResult{}, false
		},
	},
	{
		// charmbracelet/crush internal/agent/agent.go: UA
		// `Charm-Crush/<version> (https://charm.land/crush)`.
		id:   "crush",
		name: "Crush",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("charm-crush") || agent.productPrefix("crush") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:charm-crush")
			}
			return matchResult{}, false
		},
	},
	{
		// Block's goose providers do not set a product UA; the prompt was the
		// only signal, and with the body scan removed this build goes
		// unrecognized unless a deployment sets one.
		id:   "goose",
		name: "Goose",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("goose") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:goose")
			}
			return matchResult{}, false
		},
	},
	{
		id:   "amp",
		name: "Amp",
		kind: KindAgentCLI,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.product == "amp" || agent.productPrefix("amp-cli") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:amp")
			}
			return matchResult{}, false
		},
	},
	{
		id:   "openhands",
		name: "OpenHands",
		kind: KindAgentApp,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("openhands") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:openhands")
			}
			return matchResult{}, false
		},
	},
	{
		// Zed's provider clients set no product UA (crates/anthropic and
		// crates/openai), so only the X-Zed-* metadata headers can identify it.
		id:   "zed",
		name: "Zed",
		kind: KindIDE,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("zed") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:zed")
			}
			for _, header := range []string{"x-zed-version", "x-zed-client"} {
				if view.headerValue(header) != "" {
					return detected(ConfidenceHigh, "", "header:"+header)
				}
			}
			return matchResult{}, false
		},
	},

	// ---------------------------------------------------------------------
	// Gateways: the client behind them is invisible, but labelling the gateway
	// explains why a trace's UA does not look like the agent's.
	// ---------------------------------------------------------------------
	{
		// LiteLLM forces `litellm/<version>` (overridable with
		// LITELLM_USER_AGENT) and forwards the downstream SDK headers.
		id:   "litellm",
		name: "LiteLLM",
		kind: KindGateway,
		match: func(view requestView) (matchResult, bool) {
			agent := parseUserAgent(view.headerValue("User-Agent"))
			if agent.productPrefix("litellm") {
				return detected(ConfidenceHigh, agent.versionOrEmpty(), "user-agent:litellm")
			}
			if view.headerValue("x-litellm-call-id") != "" || view.headerValue("x-litellm-model-id") != "" {
				return detected(ConfidenceHigh, "", "header:x-litellm")
			}
			return matchResult{}, false
		},
	},
	{
		id:   "portkey",
		name: "Portkey",
		kind: KindGateway,
		match: func(view requestView) (matchResult, bool) {
			if view.headerValue("x-portkey-trace-id") != "" || view.headerValue("x-portkey-config") != "" {
				return detected(ConfidenceHigh, "", "header:x-portkey")
			}
			return matchResult{}, false
		},
	},
	{
		// openrouter.ai/docs/app-attribution: X-OpenRouter-Title (legacy
		// X-Title), X-OpenRouter-Categories, X-OpenRouter-App-Visibility.
		id:   "openrouter",
		name: "OpenRouter",
		kind: KindGateway,
		match: func(view requestView) (matchResult, bool) {
			if view.headerValue("x-openrouter-title") != "" || view.headerValue("x-openrouter-categories") != "" ||
				view.headerValue("x-openrouter-app-visibility") != "" {
				return detected(ConfidenceHigh, "", "header:x-openrouter")
			}
			return matchResult{}, false
		},
	},
}
