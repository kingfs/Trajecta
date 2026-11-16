package upstream

import (
	"github.com/kingfs/Trajecta/pkg/xir"
)

// ModelSource describes how a preset exposes its model list. It is catalog
// metadata: the runtime discovery call lives in internal/upstream, and this
// package never caches or persists a model list.
type ModelSource struct {
	// ListPath is the path appended to the selected endpoint base to list
	// models. An empty ListPath means no discovery.
	ListPath string
	// Static is a declarative model list carried by the catalog.
	Static []string
	// NoList disables model discovery explicitly.
	NoList bool
	// Protocol selects which endpoint ListPath is relative to. The zero value
	// falls back to Chat Completions, Responses, Messages and Gemini in that
	// order.
	Protocol xir.Protocol
}
