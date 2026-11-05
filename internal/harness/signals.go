package harness

import (
	"regexp"
	"strings"
)

// userAgent is the parsed shape of a User-Agent product token. Every client
// worth detecting puts a product and often a version in it, and both are
// stable across releases in a way the rest of the header is not.
type userAgent struct {
	// product is the lower-cased token before the first "/", e.g. "claude-cli",
	// "openai", "codex_cli_rs".
	product string
	// tail is the token after the first "/" up to the first space, e.g.
	// "2.0.14" or "Python".
	tail string
	// next is the token after tail, used for OpenAI-style "OpenAI/Python 1.2.3"
	// user agents where the version is the second word.
	next string
	// version is tail or next when it looks like a version number.
	version string
	// raw is the original header value.
	raw string
}

var versionRe = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+)+([-+.][0-9A-Za-z.-]+)?$`)

// parseUserAgent extracts the product/version shape. User agents are not
// authoritative: a harness may use its SDK's default UA, which is exactly why
// the catalogue also has header and body rules.
func parseUserAgent(raw string) userAgent {
	agent := userAgent{raw: strings.TrimSpace(raw)}
	if agent.raw == "" {
		return agent
	}
	first, rest, _ := strings.Cut(agent.raw, "/")
	agent.product = strings.ToLower(strings.TrimSpace(first))
	fields := strings.Fields(rest)
	if len(fields) > 0 {
		agent.tail = strings.TrimRight(fields[0], ";,)")
	}
	if len(fields) > 1 {
		agent.next = strings.TrimRight(fields[1], ";,)")
	}
	switch {
	case versionRe.MatchString(agent.tail):
		agent.version = strings.TrimPrefix(agent.tail, "v")
	case versionRe.MatchString(agent.next):
		agent.version = strings.TrimPrefix(agent.next, "v")
	}
	return agent
}

// productPrefix reports whether the UA product starts with prefix, case
// insensitively. A prefix is safer than a substring because a product token is
// exactly the first token.
func (agent userAgent) productPrefix(prefix string) bool {
	return strings.HasPrefix(agent.product, strings.ToLower(prefix))
}

// versionOrEmpty returns the parsed version, never a guess.
func (agent userAgent) versionOrEmpty() string {
	return agent.version
}

// contains reports whether the raw UA contains needle, case insensitively.
func (agent userAgent) contains(needle string) bool {
	return strings.Contains(strings.ToLower(agent.raw), strings.ToLower(needle))
}

// sdkTag classifies the client SDK from the fingerprints the SDKs themselves
// add. Every Stainless-generated SDK (OpenAI, Anthropic, Google genai) sends
// the same `x-stainless-*` family, so the vendor comes from the User-Agent
// product token and the language from either the UA tail or
// `x-stainless-lang`, in that order.
func (view requestView) sdkTag() string {
	agent := parseUserAgent(view.headerValue("User-Agent"))
	vendor := ""
	language := ""

	switch {
	case agent.product == "openai" || strings.HasPrefix(agent.product, "openai-python") || strings.HasPrefix(agent.product, "openai-node"):
		vendor = "openai"
	case agent.productPrefix("anthropic"):
		vendor = "anthropic"
	case agent.productPrefix("google-genai"):
		vendor = "google-genai"
	case agent.productPrefix("google-gemini"):
		vendor = "google-genai"
	}

	switch strings.ToLower(agent.tail) {
	case "python":
		language = "python"
	case "js", "node", "typescript", "ts":
		language = "typescript"
	case "go":
		language = "go"
	}
	if language == "" {
		switch strings.ToLower(view.headerValue("x-stainless-lang")) {
		case "python":
			language = "python"
		case "js":
			language = "typescript"
		case "go":
			language = "go"
		}
	}
	if language == "" {
		// Google's generated clients are not Stainless; they put the language in
		// `x-goog-api-client` as gl-python / gl-node / gl-go.
		apiClient := strings.ToLower(view.headerValue("x-goog-api-client") + " " + view.headerValue("User-Agent"))
		switch {
		case strings.Contains(apiClient, "gl-python"):
			language = "python"
		case strings.Contains(apiClient, "gl-node"), strings.Contains(apiClient, "gl-js"):
			language = "typescript"
		case strings.Contains(apiClient, "gl-go"):
			language = "go"
		}
	}
	if vendor == "" && (view.headerValue("x-stainless-package-version") != "" || language != "") {
		// The product token only names the harness (claude-cli, codex_cli_rs);
		// the SDK vendor is visible in the protocol headers the SDK itself adds.
		switch {
		case view.headerValue("anthropic-version") != "" || strings.Contains(view.path, "/messages"):
			vendor = "anthropic"
		case strings.Contains(view.path, "/chat/completions"), strings.Contains(view.path, "/responses"), strings.Contains(view.path, "/embeddings"):
			vendor = "openai"
		}
	}
	if vendor == "" && strings.Contains(strings.ToLower(view.headerValue("User-Agent")), "ai-sdk-provider-utils/") {
		// The Vercel AI SDK appends `ai-sdk-provider-utils/<version>` to the UA
		// of the runtime it is embedded in; it is a framework tag, not a vendor.
		vendor = "vercel-ai-sdk"
	}
	if vendor == "" && view.headerValue("x-stainless-package-version") != "" {
		// A Stainless SDK with no vendor token is still a useful tag: it says the
		// traffic came from a generated SDK rather than a hand-written client.
		vendor = "stainless"
	}
	if vendor == "" {
		return ""
	}
	if language == "" {
		return vendor
	}
	return vendor + "-" + language
}

// capabilities infers the protocol capabilities a request visibly uses. The
// checks are marker searches over the same bounded body windows as the
// catalogue: they are advisory labels, not proof, and a capability missed on an
// oversized payload degrades to a missing tag rather than a wrong one. The
// markers are JSON keys/verbs that are unescaped inside JSON strings, so a
// match cannot be an artefact of the encoder.
func (view requestView) capabilities() []string {
	caps := make([]string, 0, 6)
	if view.bodyContainsAny(`"stream":true`, `"stream": true`) ||
		strings.Contains(strings.ToLower(view.headerValue("Accept")), "text/event-stream") ||
		strings.Contains(view.path, "streamGenerateContent") ||
		strings.Contains(view.query, "alt=sse") {
		caps = append(caps, "streaming")
	}
	if view.bodyContainsAny(`"tools":[`, `"tools": [`, `"functions":[`, `"functions": [`, `"tool_choice"`, `"tool_calls"`, `"tool_use"`) {
		caps = append(caps, "tool-use")
	}
	if view.bodyContainsAny(`"reasoning"`, `"thinking"`, `"reasoning_effort"`, `"include_reasoning"`) {
		caps = append(caps, "reasoning")
	}
	if view.bodyContainsAny(`"cache_control"`, `"prompt_cache_key"`, `"cached_content"`, `"cache_creation_input_tokens"`) ||
		view.headerContains("anthropic-beta", "prompt-caching") {
		caps = append(caps, "prompt-cache")
	}
	if view.bodyContainsAny(`"image_url"`, `"input_image"`, `"inline_data"`, `"inlineData"`, `"type":"image"`, `"type": "image"`) {
		caps = append(caps, "vision")
	}
	if strings.Contains(view.path, "/responses") ||
		view.bodyContainsAny(`"instructions"`, `"text":{"format"`, `"text": {"format"`) {
		caps = append(caps, "responses-api")
	}
	if view.bodyContainsAny(`"response_format"`, `"json_schema"`, `"responseSchema"`, `"response_schema"`, `"format":{"type":"json_schema"`) {
		caps = append(caps, "structured-output")
	}
	return caps
}

// harnessEvidence renders the evidence token for a prompt fingerprint. The
// value is the marker's short name, never the matched text, so a prompt phrase
// cannot leak into the index.
func promptEvidence(name string) string { return "prompt:" + name }
