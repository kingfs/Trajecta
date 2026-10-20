package providerprobe

import (
	"strings"

	"github.com/kingfs/Trajecta/internal/config"
)

// TargetFromConfig converts one configured upstream target into a probe target,
// folding in the first enabled credential.
//
// Both the provider CLI commands and the serve startup probe go through this, so
// a CLI probe and a startup probe always see the same base URL, credential and
// declared API surface.
func TargetFromConfig(target config.UpstreamTargetConfig) ProbeTarget {
	upstreamCfg := target.Upstream
	apiKey := strings.TrimSpace(upstreamCfg.ApiKey)
	headers := CloneHeaders(upstreamCfg.Headers)
	for _, credential := range target.EffectiveCredentials() {
		if credential.Enabled != nil && !*credential.Enabled {
			continue
		}
		if apiKey == "" {
			apiKey = strings.TrimSpace(credential.ApiKey)
		}
		for key, value := range credential.Headers {
			headers[key] = value
		}
		break
	}
	return ProbeTarget{
		ProviderID:              strings.TrimSpace(target.ID),
		BaseURL:                 strings.TrimSpace(upstreamCfg.BaseURL),
		APIKey:                  apiKey,
		Headers:                 headers,
		SpecifiedAPIType:        upstreamCfg.APIType,
		SpecifiedProtocolFamily: upstreamCfg.ProtocolFamily,
	}
}

// CloneHeaders copies a header map so a probe never mutates the configuration it
// was handed.
func CloneHeaders(in map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range in {
		out[key] = value
	}
	return out
}
