package gemini

import (
	"encoding/json"
	"fmt"

	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// extensionMap collects object members outside known as one JSON object under
// the protocol extension key. The result is nil when nothing is left over.
func extensionMap(node *wire.Node, known map[string]struct{}) map[string]json.RawMessage {
	var extras map[string]json.RawMessage
	for _, member := range node.Members {
		if _, skip := known[member.Key]; skip {
			continue
		}
		if extras == nil {
			extras = make(map[string]json.RawMessage, len(node.Members))
		}
		extras[member.Key] = cloneRaw(member.Value.Raw())
	}
	if len(extras) == 0 {
		return nil
	}
	raw, err := json.Marshal(extras)
	if err != nil {
		// Raw values came from a parsed document, so this is unreachable.
		return nil
	}
	return map[string]json.RawMessage{ExtensionKey: raw}
}

// extensionRaw returns the Gemini extension object of a request or response.
func extensionRaw(ext map[string]json.RawMessage) json.RawMessage {
	if len(ext) == 0 {
		return nil
	}
	return ext[ExtensionKey]
}

// extensionObject decodes the Gemini extension object. A missing extension is
// nil; a non-object extension is a caller error.
func extensionObject(ext map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	raw := extensionRaw(ext)
	if len(raw) == 0 {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("gemini: %s extension is not a JSON object: %w", ExtensionKey, err)
	}
	return object, nil
}

// marshalWithExtensions renders v and restores unmodeled extension members. A
// key the encoder owns is never taken from the extension (the modeled value
// wins, even when it is omitted), and a key v already produced wins as well.
func marshalWithExtensions(v any, ext json.RawMessage, owned map[string]struct{}) ([]byte, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("gemini: encode: %w", err)
	}
	if len(ext) == 0 {
		return encoded, nil
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, fmt.Errorf("gemini: encode: %w", err)
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(ext, &extra); err != nil {
		return nil, fmt.Errorf("gemini: %s extension is not a JSON object: %w", ExtensionKey, err)
	}
	for key, value := range extra {
		if _, skip := owned[key]; skip {
			continue
		}
		if _, exists := fields[key]; exists {
			continue
		}
		fields[key] = value
	}
	return json.Marshal(fields)
}
