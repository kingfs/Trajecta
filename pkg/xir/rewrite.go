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

// EnsureStreamIncludeUsage adds `stream_options.include_usage=true` to a Chat
// Completions request whose `stream` is true. It patches the JSON in place:
// existing members, whitespace, key order and number literals are preserved,
// and the inserted member is the only new bytes.
//
// It returns the original slice and false when the body is not a JSON object,
// is not a streaming Chat request, or already asks for usage. A boolean
// `include_usage: false` is replaced with true.
func EnsureStreamIncludeUsage(raw []byte) ([]byte, bool, error) {
	if len(raw) == 0 {
		return raw, false, nil
	}
	root, err := wire.Parse(raw)
	if err != nil {
		return raw, false, nil
	}
	if root.Kind != wire.Object {
		return raw, false, nil
	}
	stream, ok := root.BoolField("stream")
	if !ok || !stream {
		return raw, false, nil
	}
	opts, ok := root.Get("stream_options")
	if ok && opts.Kind == wire.Object {
		include, has := opts.Get("include_usage")
		if has {
			if value, ok := include.Bool(); ok && value {
				return raw, false, nil
			}
			out, err := wire.Apply(raw, []wire.Edit{{
				Span:   include.Span,
				Bytes:  []byte("true"),
				Mode:   wire.ReplaceRaw,
				Reason: "xir: include stream usage",
			}})
			if err != nil {
				return raw, false, err
			}
			return out, true, nil
		}
		insert := []byte(`"include_usage":true`)
		if len(opts.Members) > 0 {
			insert = append([]byte{','}, insert...)
		}
		out, err := insertMember(raw, opts, insert)
		if err != nil {
			return raw, false, err
		}
		return out, true, nil
	}
	if ok {
		out, err := wire.Apply(raw, []wire.Edit{{
			Span:   opts.Span,
			Bytes:  []byte(`{"include_usage":true}`),
			Mode:   wire.ReplaceRaw,
			Reason: "xir: replace stream options",
		}})
		if err != nil {
			return raw, false, err
		}
		return out, true, nil
	}
	insert := []byte(`"stream_options":{"include_usage":true}`)
	if len(root.Members) > 0 {
		insert = append([]byte{','}, insert...)
	}
	out, err := insertMember(raw, root, insert)
	if err != nil {
		return raw, false, err
	}
	return out, true, nil
}

// insertMember inserts bytes immediately before an object's closing brace.
// Whitespace before the brace is left in place, so the result stays readable.
func insertMember(raw []byte, object *wire.Node, member []byte) ([]byte, error) {
	if object == nil || object.Kind != wire.Object || object.Span.End <= object.Span.Start {
		return raw, fmt.Errorf("xir: insert member into non-object")
	}
	at := object.Span.End - 1
	return wire.Apply(raw, []wire.Edit{{
		Span:   wire.Span{Start: at, End: at},
		Bytes:  member,
		Mode:   wire.ReplaceRaw,
		Reason: "xir: insert member",
	}})
}
