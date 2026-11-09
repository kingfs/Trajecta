package upstream

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// Config is the caller-supplied channel configuration that Resolve merges with
// a preset. Only PresetID (or explicit Endpoints/Auth for a custom upstream) is
// required; every other field is an override.
type Config struct {
	// PresetID selects a built-in preset. Empty means a custom upstream built
	// entirely from Endpoints and Auth.
	PresetID string
	// Plan selects a PlanVariant of the preset. Empty means the preset defaults.
	Plan string
	// Endpoints overrides preset endpoints per protocol.
	Endpoints map[xir.Protocol]string
	// Auth overrides the preset auth specification. A non-empty Mode replaces
	// the preset mode; a non-empty Header or Scheme replaces both header and
	// scheme; non-nil OAuth/Refresh replace the reserved specs; Headers are
	// appended after the preset templates.
	Auth AuthSpec
	// Headers override or add static request headers.
	Headers map[string]string
	// APIVersion is provider API version metadata. For Azure it becomes the
	// api-version query parameter when BuildURL does not already have one.
	APIVersion string
	// Deployment is the Azure OpenAI deployment name.
	Deployment string
	// Project is the Google Cloud project for Vertex.
	Project string
	// Location is the Google Cloud location for Vertex.
	Location string
	// ModelResource is the Vertex model resource path, for example
	// publishers/google/models.
	ModelResource string
	// Protocols restricts the resolved protocols. Empty means every protocol
	// with an endpoint.
	Protocols []xir.Protocol
	// ModelProtocols overrides Capabilities for specific models. Keys are
	// matched case-insensitively.
	ModelProtocols map[string][]xir.Protocol
	// Credential optionally attaches a secret for DiscoverModels. It is copied
	// into Resolved and never persisted.
	Credential *Credential
}

// Resolved is an immutable upstream view produced by Resolve. It is a value:
// methods copy it and never mutate shared maps.
type Resolved struct {
	// Preset is the catalog entry the configuration started from. For a custom
	// upstream it is a synthetic value with an empty ID.
	Preset Preset
	// Plan is the selected PlanVariant, or nil.
	Plan *PlanVariant
	// Endpoints are the merged, normalized protocol base URLs.
	Endpoints map[xir.Protocol]string
	// Protocols are the protocols this upstream serves. They are in canonical
	// order when derived from endpoints and in the requested order otherwise.
	Protocols []xir.Protocol
	// Auth is the merged authentication specification.
	Auth AuthSpec
	// Headers are the merged static headers.
	Headers map[string]string
	// APIVersion, Deployment, Project, Location and ModelResource carry the
	// provider-specific configuration values.
	APIVersion    string
	Deployment    string
	Project       string
	Location      string
	ModelResource string
	// ModelProtocols is the normalized per-model protocol override.
	ModelProtocols map[string][]xir.Protocol
	// Models is the preset model source.
	Models ModelSource
	// Credential is the optional secret used by DiscoverModels.
	Credential Credential
}

var canonicalProtocols = []xir.Protocol{
	xir.ProtocolChatCompletions,
	xir.ProtocolResponses,
	xir.ProtocolMessages,
	xir.ProtocolGemini,
}

// Resolve merges a Config with its preset into a Resolved value.
//
// It validates that every base URL is an absolute http(s) URL, that every
// declared protocol has an endpoint, and that the resolved auth mode is known.
// Azure endpoints remain caller-supplied because they are per-resource; Vertex
// Project and Location must be supplied together.
func Resolve(cfg Config) (Resolved, error) {
	var preset Preset
	if strings.TrimSpace(cfg.PresetID) != "" {
		found, ok := LookupPreset(cfg.PresetID)
		if !ok {
			return Resolved{}, fmt.Errorf("%w: %q", ErrUnknownPreset, cfg.PresetID)
		}
		preset = found
	} else {
		preset = Preset{Name: "Custom", Kind: KindVendor}
	}

	endpoints := cloneProtocolMap(preset.Endpoints)
	if endpoints == nil {
		endpoints = make(map[xir.Protocol]string)
	}
	var selectedPlan *PlanVariant
	if strings.TrimSpace(cfg.Plan) != "" {
		plan, ok := preset.Plan(cfg.Plan)
		if !ok {
			return Resolved{}, fmt.Errorf("%w: %q for preset %q", ErrUnknownPlan, cfg.Plan, preset.ID)
		}
		selectedPlan = &plan
		for protocol, raw := range plan.Endpoints {
			endpoints[protocol] = raw
		}
	}
	for protocol, raw := range cfg.Endpoints {
		endpoints[protocol] = raw
	}
	if len(endpoints) == 0 {
		return Resolved{}, fmt.Errorf("%w: preset %q", ErrNoEndpoints, preset.ID)
	}
	for protocol, raw := range endpoints {
		if !protocol.Valid() {
			return Resolved{}, fmt.Errorf("%w: %q", ErrUnsupportedProtocol, protocol)
		}
		normalized, err := normalizeEndpoint(raw)
		if err != nil {
			return Resolved{}, fmt.Errorf("%w: protocol %q: %v", ErrInvalidEndpoint, protocol, err)
		}
		endpoints[protocol] = normalized
	}

	project := strings.TrimSpace(cfg.Project)
	location := strings.TrimSpace(cfg.Location)
	if preset.ID == "vertex" && (project == "") != (location == "") {
		return Resolved{}, errors.New("upstream: vertex project and location must be set together")
	}

	auth, err := mergeAuth(preset.Auth, cfg.Auth, preset.ID)
	if err != nil {
		return Resolved{}, err
	}
	protocols, err := resolveProtocols(cfg.Protocols, endpoints)
	if err != nil {
		return Resolved{}, err
	}
	modelProtocols, err := normalizeModelProtocols(cfg.ModelProtocols, endpoints)
	if err != nil {
		return Resolved{}, err
	}

	headers := cloneStringMap(preset.Headers)
	for name, value := range cfg.Headers {
		if headers == nil {
			headers = make(map[string]string, len(cfg.Headers))
		}
		headers[name] = value
	}

	modelResource := strings.Trim(strings.TrimSpace(cfg.ModelResource), "/")
	if preset.ID == "vertex" && modelResource == "" {
		modelResource = "publishers/google/models"
	}

	resolved := Resolved{
		Preset:         preset,
		Plan:           selectedPlan,
		Endpoints:      endpoints,
		Protocols:      protocols,
		Auth:           auth,
		Headers:        headers,
		APIVersion:     strings.TrimSpace(cfg.APIVersion),
		Deployment:     strings.TrimSpace(cfg.Deployment),
		Project:        project,
		Location:       location,
		ModelResource:  modelResource,
		ModelProtocols: modelProtocols,
		Models:         preset.Models,
	}
	if cfg.Credential != nil {
		resolved.Credential = cloneCredential(*cfg.Credential)
	}
	return resolved, nil
}

// WithCredential returns a copy of r carrying cred. DiscoverModels uses the
// attached credential; nothing is persisted.
func (r Resolved) WithCredential(cred Credential) Resolved {
	r.Credential = cloneCredential(cred)
	return r
}

// Endpoint returns the normalized base URL for protocol.
func (r Resolved) Endpoint(protocol xir.Protocol) (string, bool) {
	endpoint, ok := r.Endpoints[protocol]
	if !ok || strings.TrimSpace(endpoint) == "" {
		return "", false
	}
	return endpoint, true
}

func normalizeEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return "", errors.New("URL must be absolute with a host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if parsed.User != nil {
		return "", errors.New("credentials in the endpoint URL are not allowed")
	}
	if parsed.Fragment != "" {
		return "", errors.New("fragment in the endpoint URL is not allowed")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}
	if parsed.Path == "/" {
		parsed.Path = ""
	}
	parsed.RawPath = ""
	return parsed.String(), nil
}

func mergeAuth(base AuthSpec, override AuthSpec, presetID string) (AuthSpec, error) {
	auth := cloneAuthSpec(base)
	if override.Mode != "" {
		auth.Mode = override.Mode
	}
	if override.Header != "" || override.Scheme != "" {
		auth.Header = override.Header
		auth.Scheme = override.Scheme
	}
	if override.OAuth != nil {
		auth.OAuth = cloneOAuthSpec(override.OAuth)
	}
	if override.Refresh != nil {
		auth.Refresh = cloneRefreshSpec(override.Refresh)
	}
	if len(override.Headers) > 0 {
		auth.Headers = append(auth.Headers, override.Headers...)
	}
	if auth.Mode == "" {
		if presetID == "" {
			return AuthSpec{}, fmt.Errorf("%w: auth.mode is required for a custom upstream", ErrInvalidAuthMode)
		}
		return AuthSpec{}, fmt.Errorf("%w: preset %q has no auth mode", ErrInvalidAuthMode, presetID)
	}
	if !auth.Mode.Valid() {
		return AuthSpec{}, fmt.Errorf("%w: %q", ErrInvalidAuthMode, auth.Mode)
	}
	return auth, nil
}

func resolveProtocols(requested []xir.Protocol, endpoints map[xir.Protocol]string) ([]xir.Protocol, error) {
	if len(requested) == 0 {
		out := make([]xir.Protocol, 0, len(canonicalProtocols))
		for _, protocol := range canonicalProtocols {
			if strings.TrimSpace(endpoints[protocol]) != "" {
				out = append(out, protocol)
			}
		}
		if len(out) == 0 {
			return nil, ErrNoEndpoints
		}
		return out, nil
	}
	seen := make(map[xir.Protocol]bool, len(requested))
	out := make([]xir.Protocol, 0, len(requested))
	for _, protocol := range requested {
		if !protocol.Valid() {
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedProtocol, protocol)
		}
		if strings.TrimSpace(endpoints[protocol]) == "" {
			return nil, fmt.Errorf("%w: %q", ErrNoEndpoint, protocol)
		}
		if seen[protocol] {
			continue
		}
		seen[protocol] = true
		out = append(out, protocol)
	}
	return out, nil
}

func normalizeModelProtocols(input map[string][]xir.Protocol, endpoints map[xir.Protocol]string) (map[string][]xir.Protocol, error) {
	if len(input) == 0 {
		return nil, nil
	}
	out := make(map[string][]xir.Protocol, len(input))
	for model, protocols := range input {
		key := strings.ToLower(strings.TrimSpace(model))
		if key == "" {
			return nil, errors.New("upstream: model protocol override has an empty model name")
		}
		seen := make(map[xir.Protocol]bool, len(protocols))
		normalized := make([]xir.Protocol, 0, len(protocols))
		for _, protocol := range protocols {
			if !protocol.Valid() {
				return nil, fmt.Errorf("%w: %q for model %q", ErrUnsupportedProtocol, protocol, key)
			}
			if strings.TrimSpace(endpoints[protocol]) == "" {
				return nil, fmt.Errorf("%w: %q for model %q", ErrNoEndpoint, protocol, key)
			}
			if seen[protocol] {
				continue
			}
			seen[protocol] = true
			normalized = append(normalized, protocol)
		}
		out[key] = normalized
	}
	return out, nil
}

func cloneCredential(cred Credential) Credential {
	out := cred
	out.Scopes = append([]string(nil), cred.Scopes...)
	return out
}
