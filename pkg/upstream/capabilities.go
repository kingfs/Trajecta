package upstream

import (
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// ProtocolsForModel returns the protocols this upstream serves for model. A
// model-specific entry in Resolved.ModelProtocols overrides the upstream-wide
// protocol list; protocols without an endpoint are always excluded. The result
// is in canonical order.
func (r Resolved) ProtocolsForModel(model string) []xir.Protocol {
	selected := r.Protocols
	if key := strings.ToLower(strings.TrimSpace(model)); key != "" && len(r.ModelProtocols) > 0 {
		if override, ok := r.ModelProtocols[key]; ok {
			selected = override
		}
	}
	served := make(map[xir.Protocol]bool, len(selected))
	for _, protocol := range selected {
		served[protocol] = true
	}
	out := make([]xir.Protocol, 0, len(selected))
	for _, protocol := range canonicalProtocols {
		if !served[protocol] {
			continue
		}
		if strings.TrimSpace(r.Endpoints[protocol]) == "" {
			continue
		}
		out = append(out, protocol)
	}
	return out
}

// Capabilities reports the protocols this upstream serves natively for model.
// It is derived from Resolved.Protocols, unless Resolved.ModelProtocols has a
// case-insensitive entry for model in which case that entry is the override.
func (r Resolved) Capabilities(model string) xir.Capabilities {
	var capabilities xir.Capabilities
	for _, protocol := range r.ProtocolsForModel(model) {
		switch protocol {
		case xir.ProtocolChatCompletions:
			capabilities.ChatCompletions = true
		case xir.ProtocolResponses:
			capabilities.Responses = true
		case xir.ProtocolMessages:
			capabilities.Messages = true
		case xir.ProtocolGemini:
			capabilities.Gemini = true
		}
	}
	return capabilities
}
