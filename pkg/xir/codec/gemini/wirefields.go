package gemini

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// The helpers in this file keep decode errors tied to a JSON path. A missing
// member and an explicit null both read as "absent"; a present member of the
// wrong JSON kind is an error, because accepting it silently would produce a
// request whose meaning differs from the bytes on the wire.

func stringField(node *wire.Node, key, path string) (string, bool, error) {
	value, ok := node.Get(key)
	if !ok || value.Kind == wire.Null {
		return "", false, nil
	}
	if value.Kind != wire.String {
		return "", false, fmt.Errorf("gemini: %s.%s: expected string, got %s", path, key, kindName(value.Kind))
	}
	return value.Str, true, nil
}

func boolField(node *wire.Node, key, path string) (bool, bool, error) {
	value, ok := node.Get(key)
	if !ok || value.Kind == wire.Null {
		return false, false, nil
	}
	if value.Kind != wire.Bool {
		return false, false, fmt.Errorf("gemini: %s.%s: expected bool, got %s", path, key, kindName(value.Kind))
	}
	v, _ := value.Bool()
	return v, true, nil
}

func intField(node *wire.Node, key, path string) (int, bool, error) {
	value, ok := node.Get(key)
	if !ok || value.Kind == wire.Null {
		return 0, false, nil
	}
	if value.Kind != wire.Number {
		return 0, false, fmt.Errorf("gemini: %s.%s: expected integer, got %s", path, key, kindName(value.Kind))
	}
	v, ok := value.Int()
	if !ok {
		return 0, false, fmt.Errorf("gemini: %s.%s: expected integer, got %s", path, key, string(value.Raw()))
	}
	return int(v), true, nil
}

func int64Field(node *wire.Node, key, path string) (int64, bool, error) {
	value, ok := node.Get(key)
	if !ok || value.Kind == wire.Null {
		return 0, false, nil
	}
	if value.Kind != wire.Number {
		return 0, false, fmt.Errorf("gemini: %s.%s: expected integer, got %s", path, key, kindName(value.Kind))
	}
	v, ok := value.Int()
	if !ok {
		return 0, false, fmt.Errorf("gemini: %s.%s: expected integer, got %s", path, key, string(value.Raw()))
	}
	return v, true, nil
}

func floatField(node *wire.Node, key, path string) (float64, bool, error) {
	value, ok := node.Get(key)
	if !ok || value.Kind == wire.Null {
		return 0, false, nil
	}
	if value.Kind != wire.Number {
		return 0, false, fmt.Errorf("gemini: %s.%s: expected number, got %s", path, key, kindName(value.Kind))
	}
	v, ok := value.Float()
	if !ok {
		return 0, false, fmt.Errorf("gemini: %s.%s: expected number, got %s", path, key, string(value.Raw()))
	}
	return v, true, nil
}

func objectField(node *wire.Node, key, path string) (*wire.Node, bool, error) {
	value, ok := node.Get(key)
	if !ok || value.Kind == wire.Null {
		return nil, false, nil
	}
	if value.Kind != wire.Object {
		return nil, false, fmt.Errorf("gemini: %s.%s: expected object, got %s", path, key, kindName(value.Kind))
	}
	return value, true, nil
}

func arrayField(node *wire.Node, key, path string) ([]*wire.Node, bool, error) {
	value, ok := node.Get(key)
	if !ok || value.Kind == wire.Null {
		return nil, false, nil
	}
	if value.Kind != wire.Array {
		return nil, false, fmt.Errorf("gemini: %s.%s: expected array, got %s", path, key, kindName(value.Kind))
	}
	return value.Items, true, nil
}

func stringArrayField(node *wire.Node, key, path string) ([]string, bool, error) {
	items, ok, err := arrayField(node, key, path)
	if err != nil || !ok {
		return nil, ok, err
	}
	out := make([]string, 0, len(items))
	for i, item := range items {
		if item.Kind != wire.String {
			return nil, false, fmt.Errorf("gemini: %s.%s[%d]: expected string, got %s", path, key, i, kindName(item.Kind))
		}
		out = append(out, item.Str)
	}
	return out, true, nil
}

func compactJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}
