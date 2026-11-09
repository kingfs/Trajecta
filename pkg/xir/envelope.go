package xir

import (
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// Envelope is the small, routing-relevant view of a request body. It is
// parsed from the wire tree without building a full semantic Request, so the
// forwarding path can read the model, stream flag, tool/structured-output
// presence and output cap once and share them between routing and recording.
type Envelope struct {
	Model            string
	Stream           bool
	Tools            bool
	StructuredOutput bool
	MaxTokens        float64

	PreviousResponseID string
	PromptCacheKey     string
	ConversationID     string
	SessionID          string
}

// ParseEnvelope parses the routing envelope from a JSON request body. It is
// intentionally tolerant: a missing field is zero, and only an unparseable
// root or a non-object root is an error. Callers that treat an unparseable
// body as streaming (the historical fail-closed routing behaviour) can keep
// doing so on error.
func ParseEnvelope(raw []byte) (Envelope, error) {
	if len(raw) == 0 {
		return Envelope{}, nil
	}
	root, err := wire.Parse(raw)
	if err != nil {
		return Envelope{}, fmt.Errorf("xir: parse envelope: %w", err)
	}
	if root.Kind != wire.Object {
		return Envelope{}, fmt.Errorf("xir: envelope is not a JSON object")
	}
	var env Envelope
	if value, ok := root.StringField("model"); ok {
		env.Model = strings.TrimSpace(value)
	}
	env.Stream = boolField(root, "stream")
	env.Tools = nonEmptyArray(root, "tools") || nonEmptyArray(root, "functions")
	if _, ok := root.Get("response_format"); ok {
		env.StructuredOutput = true
	}
	for _, key := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		if value, ok := numberField(root, key); ok && value > 0 {
			env.MaxTokens = value
			break
		}
	}
	if env.MaxTokens == 0 {
		if generation, ok := root.Get("generationConfig"); ok && generation.Kind == wire.Object {
			if value, ok := numberField(generation, "maxOutputTokens"); ok && value > 0 {
				env.MaxTokens = value
			}
		}
	}
	env.PreviousResponseID, _ = root.StringField("previous_response_id")
	env.PromptCacheKey, _ = root.StringField("prompt_cache_key")
	env.ConversationID, _ = root.StringField("conversation_id")
	env.SessionID, _ = root.StringField("session_id")
	if env.SessionID == "" {
		if metadata, ok := root.Get("metadata"); ok && metadata.Kind == wire.Object {
			env.SessionID, _ = metadata.StringField("session_id")
		}
	}
	return env, nil
}

func boolField(node *wire.Node, key string) bool {
	value, ok := node.Get(key)
	if !ok {
		return false
	}
	if b, ok := value.Bool(); ok {
		return b
	}
	if s, ok := value.String(); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "1", "yes":
			return true
		}
	}
	if n, ok := value.Float(); ok {
		return n != 0
	}
	return false
}

func nonEmptyArray(node *wire.Node, key string) bool {
	value, ok := node.Get(key)
	return ok && value.Kind == wire.Array && len(value.Items) > 0
}

func numberField(node *wire.Node, key string) (float64, bool) {
	value, ok := node.Get(key)
	if !ok {
		return 0, false
	}
	return value.Float()
}
