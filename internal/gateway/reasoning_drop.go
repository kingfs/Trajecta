package gateway

import (
	"bytes"
	"crypto/sha256"

	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// ReasoningQuarantine answers whether an upstream has already rejected the
// given opaque reasoning blob. The proxy keeps the durable (process-local) map;
// gateway only consumes the read side so the request transform can be tested
// without the proxy.
type ReasoningQuarantine interface {
	// Empty reports whether any live entry exists. The transform uses it to
	// keep the normal fast path free of JSON parsing until an upstream has
	// actually rejected a blob.
	Empty() bool
	Quarantined(upstreamID string, blobHash [32]byte) bool
}

// DroppedReasoning identifies one reasoning item the proxy removed from the
// wire body because its encrypted_content had already been rejected by this
// upstream. The client body and the cassette keep the item; only the wire copy
// is stripped.
type DroppedReasoning struct {
	ID   string
	Type string
	Hash [32]byte
}

// dropQuarantinedReasoning removes top-level Responses input items whose
// encrypted_content is quarantined for this upstream. It returns the dropped
// items and the rewritten body; ok is false when nothing changed or the body is
// not a Responses-shaped JSON document.
//
// Rebuilding the input array from the kept items' original raw bytes preserves
// every retained item byte for byte (the array's own whitespace is compacted).
// The rest of the request document is untouched. The request transform keeps
// the client's original body as RecordBody; only WireBody sees this rewrite.
func dropQuarantinedReasoning(body []byte, upstreamID string, quarantine ReasoningQuarantine) (dropped []DroppedReasoning, rewritten []byte, ok bool) {
	if quarantine == nil || len(body) == 0 || !bytes.Contains(body, []byte(`"encrypted_content"`)) {
		return nil, body, false
	}
	root, err := wire.Parse(body)
	if err != nil {
		return nil, body, false
	}
	input, ok := root.Get("input")
	if !ok || input.Kind != wire.Array {
		return nil, body, false
	}
	kept := make([][]byte, 0, input.Len())
	for i := 0; i < input.Len(); i++ {
		item, ok := input.Index(i)
		if !ok {
			continue
		}
		if item.Kind != wire.Object {
			kept = append(kept, item.Raw())
			continue
		}
		blob, ok := item.StringField("encrypted_content")
		if !ok || blob == "" {
			kept = append(kept, item.Raw())
			continue
		}
		hash := sha256.Sum256([]byte(blob))
		if !quarantine.Quarantined(upstreamID, hash) {
			kept = append(kept, item.Raw())
			continue
		}
		id, _ := item.StringField("id")
		kind, _ := item.StringField("type")
		dropped = append(dropped, DroppedReasoning{ID: id, Type: kind, Hash: hash})
	}
	if len(dropped) == 0 {
		return nil, body, false
	}
	var array bytes.Buffer
	array.WriteByte('[')
	for i, raw := range kept {
		if i > 0 {
			array.WriteByte(',')
		}
		array.Write(raw)
	}
	array.WriteByte(']')
	rewritten, err = wire.Apply(body, []wire.Edit{{
		Span:  input.Span,
		Bytes: array.Bytes(),
		Mode:  wire.ReplaceRaw,
	}})
	if err != nil {
		return nil, body, false
	}
	return dropped, rewritten, true
}
