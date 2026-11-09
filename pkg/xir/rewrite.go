package xir

import (
	"fmt"

	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// RewriteModel replaces the top-level model string in a JSON request body
// without re-encoding the body. It preserves member order, whitespace,
// duplicate keys and number literals; only the model string's bytes change.
//
// It returns the original slice and false when the body is not a JSON object,
// has no top-level model member, or the model value is not a string. A caller
// should then forward the body unchanged rather than guessing.
func RewriteModel(raw []byte, model string) ([]byte, bool, error) {
	if len(raw) == 0 || model == "" {
		return raw, false, nil
	}
	root, err := wire.Parse(raw)
	if err != nil {
		return raw, false, nil
	}
	if root.Kind != wire.Object {
		return raw, false, nil
	}
	member, ok := root.ObjectElem("model")
	if !ok || member.Value == nil || member.Value.Kind != wire.String {
		return raw, false, nil
	}
	out, err := wire.Apply(raw, []wire.Edit{{
		Span:   member.Value.Span,
		Bytes:  []byte(model),
		Mode:   wire.ReplaceString,
		Reason: "xir: rewrite model",
	}})
	if err != nil {
		return raw, false, fmt.Errorf("xir: rewrite model: %w", err)
	}
	return out, true, nil
}
