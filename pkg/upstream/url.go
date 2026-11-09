package upstream

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// BuildURL joins the resolved endpoint for protocol with clientPath.
//
// Preset endpoints are complete API base prefixes, so a client "/v1" segment is
// folded into the base rather than duplicated: /v1/chat/completions against
// https://api.openai.com/v1 becomes https://api.openai.com/v1/chat/completions.
// Any non-root base is treated as the complete prefix, so a custom base must
// already include its version segment when the upstream needs one; only an
// origin-only base keeps the client /v1. A Gemini model path such as
// /v1beta/models/gemini-2.5-flash:generateContent is preserved, and a base that
// already ends in /models (Vertex Express and project-scoped Vertex endpoints)
// only receives the model/action suffix. The client query is preserved and
// merged onto any base query; Azure api-version is added when APIVersion is set
// and the request does not already carry one.
func (r Resolved) BuildURL(protocol xir.Protocol, clientPath string) (string, error) {
	if !protocol.Valid() {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedProtocol, protocol)
	}
	endpoint, ok := r.Endpoints[protocol]
	if !ok || strings.TrimSpace(endpoint) == "" {
		return "", fmt.Errorf("%w: %q", ErrNoEndpoint, protocol)
	}
	baseURL, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidEndpoint, err)
	}
	if !baseURL.IsAbs() || baseURL.Host == "" {
		return "", fmt.Errorf("%w: %q", ErrInvalidEndpoint, endpoint)
	}
	clientURL, err := url.Parse(clientPath)
	if err != nil {
		return "", fmt.Errorf("upstream: invalid client path %q: %w", clientPath, err)
	}
	if clientURL.IsAbs() || clientURL.Host != "" {
		return "", fmt.Errorf("upstream: client path must be relative: %q", clientPath)
	}
	joinedPath := joinEndpointPath(protocol, baseURL.Path, clientURL.Path)
	target := *baseURL
	target.Path = joinedPath
	target.RawPath = ""
	target.Fragment = ""

	query := baseURL.Query()
	for key, values := range clientURL.Query() {
		query[key] = append([]string(nil), values...)
	}
	if r.isAzure() && r.APIVersion != "" && query.Get("api-version") == "" {
		query.Set("api-version", r.APIVersion)
	}
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}
	return target.String(), nil
}

func (r Resolved) isAzure() bool {
	if r.Preset.ID == "azure" {
		return true
	}
	for _, endpoint := range r.Endpoints {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(parsed.Host), "openai.azure.com") {
			return true
		}
	}
	return false
}

func joinEndpointPath(protocol xir.Protocol, basePath, clientPath string) string {
	basePath = strings.TrimRight(basePath, "/")
	if clientPath == "" {
		clientPath = "/"
	}
	if !strings.HasPrefix(clientPath, "/") {
		clientPath = "/" + clientPath
	}
	if clientPath == "/" {
		if basePath == "" {
			return "/"
		}
		return basePath
	}
	switch protocol {
	case xir.ProtocolGemini:
		// A base ending in /models is itself the model collection: Vertex
		// Express and project-scoped Vertex endpoints. Keep only the model and
		// action suffix instead of repeating the resource path.
		if strings.HasSuffix(basePath, "/models") {
			if index := strings.Index(clientPath, "/models/"); index >= 0 {
				return basePath + clientPath[index+len("/models"):]
			}
			if index := strings.Index(clientPath, "/models:"); index >= 0 {
				return basePath + clientPath[index+len("/models"):]
			}
			if strings.HasSuffix(clientPath, "/models") {
				return basePath
			}
		}
		clientSegments := splitURLPath(clientPath)
		if len(clientSegments) > 0 && isVersionSegment(clientSegments[0]) {
			baseSegments := splitURLPath(basePath)
			baseLast := ""
			if len(baseSegments) > 0 {
				baseLast = baseSegments[len(baseSegments)-1]
			}
			if baseLast == clientSegments[0] || hasVersionSegment(baseSegments) {
				clientPath = "/" + strings.Join(clientSegments[1:], "/")
				if clientPath == "/" {
					clientPath = ""
				}
			}
		}
	case xir.ProtocolChatCompletions, xir.ProtocolResponses:
		// OpenAI-compatible client paths always start with /v1, while the
		// endpoint base is the complete SDK base prefix. Fold the client /v1
		// into the base even when the base has no version segment (GitHub
		// Models' /inference, for example). A base of the origin keeps /v1.
		if basePath != "" && (clientPath == "/v1" || strings.HasPrefix(clientPath, "/v1/")) {
			clientPath = strings.TrimPrefix(clientPath, "/v1")
			if clientPath == "" {
				clientPath = "/"
			}
		}
	case xir.ProtocolMessages:
		// Anthropic-style clients append /v1/messages to the base. Only fold it
		// when the base itself ends in /v1; otherwise the base may already sit
		// below the version (for example /anthropic or /inference).
		if basePath != "" && (clientPath == "/v1" || strings.HasPrefix(clientPath, "/v1/")) && lastURLSegment(basePath) == "v1" {
			clientPath = strings.TrimPrefix(clientPath, "/v1")
			if clientPath == "" {
				clientPath = "/"
			}
		}
	}
	return joinURLPath(basePath, clientPath)
}

func joinURLPath(basePath, clientPath string) string {
	if basePath == "" {
		if clientPath == "" {
			return "/"
		}
		if !strings.HasPrefix(clientPath, "/") {
			return "/" + clientPath
		}
		return clientPath
	}
	joined := path.Join(basePath, clientPath)
	if joined == "." || joined == "" {
		return "/"
	}
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return joined
}

func splitURLPath(value string) []string {
	value = strings.Trim(value, "/")
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

func lastURLSegment(value string) string {
	segments := splitURLPath(value)
	if len(segments) == 0 {
		return ""
	}
	return segments[len(segments)-1]
}

func hasVersionSegment(segments []string) bool {
	for _, segment := range segments {
		if isVersionSegment(segment) {
			return true
		}
	}
	return false
}

func isVersionSegment(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' || segment[1] < '0' || segment[1] > '9' {
		return false
	}
	for _, char := range segment[2:] {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') {
			continue
		}
		return false
	}
	return true
}
