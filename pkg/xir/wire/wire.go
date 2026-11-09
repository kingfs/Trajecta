// Package wire owns the lossless JSON view of an Exchange IR document.
//
// A Document keeps the original bytes and a tree of nodes whose spans point
// back into them. Parsing never builds map[string]any: object member order,
// duplicate keys, whitespace and unknown fields survive, and a rewrite is a
// list of byte edits applied to the original buffer rather than a full
// re-encode.
package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// Kind is the JSON value kind of a Node.
type Kind uint8

const (
	Object Kind = iota
	Array
	String
	Number
	Bool
	Null
)

// Span is a half-open byte range in the document's raw buffer.
type Span struct {
	Start int
	End   int
}

// Len reports the span length.
func (s Span) Len() int { return s.End - s.Start }

// Valid reports whether the span is non-negative and ordered.
func (s Span) Valid() bool { return s.Start >= 0 && s.End >= s.Start }

// Node is one JSON value. Bytes shares the document's raw buffer and includes
// the value exactly as it appeared, including quotes for strings.
type Node struct {
	Kind    Kind
	Span    Span
	Bytes   []byte
	Members []Member
	Items   []*Node

	// Str is the decoded string value for String nodes. It is populated
	// during parsing so callers never need a second pass.
	Str string
}

// Member is one object member. KeySpan covers the quoted key literal.
type Member struct {
	Key     string
	KeySpan Span
	Value   *Node
}

// Raw returns the original bytes of the value.
func (n *Node) Raw() []byte {
	if n == nil {
		return nil
	}
	return n.Bytes
}

// String returns the decoded string value.
func (n *Node) String() (string, bool) {
	if n == nil || n.Kind != String {
		return "", false
	}
	return n.Str, true
}

// Int returns an integer value. JSON numbers with a fractional part or an
// exponent are not integers for this purpose.
func (n *Node) Int() (int64, bool) {
	if n == nil || n.Kind != Number {
		return 0, false
	}
	v, err := strconv.ParseInt(string(n.Bytes), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// Float returns a JSON number as float64.
func (n *Node) Float() (float64, bool) {
	if n == nil || n.Kind != Number {
		return 0, false
	}
	v, err := strconv.ParseFloat(string(n.Bytes), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// Bool returns a boolean value.
func (n *Node) Bool() (bool, bool) {
	if n == nil || n.Kind != Bool {
		return false, false
	}
	switch string(n.Bytes) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// Get returns the last member with key. Last wins, matching encoding/json's
// duplicate-key behaviour for a decoded object.
func (n *Node) Get(key string) (*Node, bool) {
	if n == nil || n.Kind != Object {
		return nil, false
	}
	for i := len(n.Members) - 1; i >= 0; i-- {
		if n.Members[i].Key == key {
			return n.Members[i].Value, true
		}
	}
	return nil, false
}

// Index returns the array element at i.
func (n *Node) Index(i int) (*Node, bool) {
	if n == nil || n.Kind != Array || i < 0 || i >= len(n.Items) {
		return nil, false
	}
	return n.Items[i], true
}

// Len returns the number of object members or array elements.
func (n *Node) Len() int {
	if n == nil {
		return 0
	}
	if n.Kind == Object {
		return len(n.Members)
	}
	if n.Kind == Array {
		return len(n.Items)
	}
	return 0
}

// StringField reads an object member as a string.
func (n *Node) StringField(key string) (string, bool) {
	v, ok := n.Get(key)
	if !ok {
		return "", false
	}
	return v.String()
}

// IntField reads an object member as an integer.
func (n *Node) IntField(key string) (int64, bool) {
	v, ok := n.Get(key)
	if !ok {
		return 0, false
	}
	return v.Int()
}

// BoolField reads an object member as a boolean.
func (n *Node) BoolField(key string) (bool, bool) {
	v, ok := n.Get(key)
	if !ok {
		return false, false
	}
	return v.Bool()
}

// ObjectElem returns a member value and its key span.
func (n *Node) ObjectElem(key string) (Member, bool) {
	if n == nil || n.Kind != Object {
		return Member{}, false
	}
	for i := len(n.Members) - 1; i >= 0; i-- {
		if n.Members[i].Key == key {
			return n.Members[i], true
		}
	}
	return Member{}, false
}

// Parse parses one JSON value and requires the whole buffer to be that value
// (leading/trailing JSON whitespace is allowed).
func Parse(raw []byte) (*Node, error) {
	p := parser{raw: raw}
	n, err := p.value()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos != len(raw) {
		return nil, fmt.Errorf("wire: trailing bytes at offset %d", p.pos)
	}
	return n, nil
}

type parser struct {
	raw []byte
	pos int
}

func (p *parser) skipSpace() {
	for p.pos < len(p.raw) {
		switch p.raw[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) value() (*Node, error) {
	p.skipSpace()
	if p.pos >= len(p.raw) {
		return nil, fmt.Errorf("wire: unexpected end of input")
	}
	switch p.raw[p.pos] {
	case '{':
		return p.object()
	case '[':
		return p.array()
	case '"':
		return p.stringNode()
	case 't', 'f':
		return p.boolNode()
	case 'n':
		return p.nullNode()
	default:
		return p.numberNode()
	}
}

func (p *parser) object() (*Node, error) {
	start := p.pos
	p.pos++ // {
	n := &Node{Kind: Object, Span: Span{Start: start}}
	p.skipSpace()
	if p.pos < len(p.raw) && p.raw[p.pos] == '}' {
		p.pos++
		n.Span.End = p.pos
		n.Bytes = p.raw[start:p.pos]
		return n, nil
	}
	for {
		p.skipSpace()
		keyStart := p.pos
		if p.pos >= len(p.raw) || p.raw[p.pos] != '"' {
			return nil, fmt.Errorf("wire: expected object key at offset %d", p.pos)
		}
		keyNode, err := p.stringNode()
		if err != nil {
			return nil, err
		}
		keyEnd := p.pos
		p.skipSpace()
		if p.pos >= len(p.raw) || p.raw[p.pos] != ':' {
			return nil, fmt.Errorf("wire: expected ':' at offset %d", p.pos)
		}
		p.pos++
		value, err := p.value()
		if err != nil {
			return nil, err
		}
		n.Members = append(n.Members, Member{
			Key:     keyNode.Str,
			KeySpan: Span{Start: keyStart, End: keyEnd},
			Value:   value,
		})
		p.skipSpace()
		if p.pos >= len(p.raw) {
			return nil, fmt.Errorf("wire: unexpected end of object")
		}
		switch p.raw[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			n.Span.End = p.pos
			n.Bytes = p.raw[start:p.pos]
			return n, nil
		default:
			return nil, fmt.Errorf("wire: expected ',' or '}' at offset %d", p.pos)
		}
	}
}

func (p *parser) array() (*Node, error) {
	start := p.pos
	p.pos++ // [
	n := &Node{Kind: Array, Span: Span{Start: start}}
	p.skipSpace()
	if p.pos < len(p.raw) && p.raw[p.pos] == ']' {
		p.pos++
		n.Span.End = p.pos
		n.Bytes = p.raw[start:p.pos]
		return n, nil
	}
	for {
		item, err := p.value()
		if err != nil {
			return nil, err
		}
		n.Items = append(n.Items, item)
		p.skipSpace()
		if p.pos >= len(p.raw) {
			return nil, fmt.Errorf("wire: unexpected end of array")
		}
		switch p.raw[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			n.Span.End = p.pos
			n.Bytes = p.raw[start:p.pos]
			return n, nil
		default:
			return nil, fmt.Errorf("wire: expected ',' or ']' at offset %d", p.pos)
		}
	}
}

func (p *parser) stringNode() (*Node, error) {
	start := p.pos
	p.pos++ // opening quote
	for p.pos < len(p.raw) {
		switch p.raw[p.pos] {
		case '\\':
			p.pos += 2
		case '"':
			p.pos++
			var s string
			if err := json.Unmarshal(p.raw[start:p.pos], &s); err != nil {
				return nil, fmt.Errorf("wire: invalid string at offset %d: %w", start, err)
			}
			return &Node{
				Kind:  String,
				Span:  Span{Start: start, End: p.pos},
				Bytes: p.raw[start:p.pos],
				Str:   s,
			}, nil
		default:
			p.pos++
		}
	}
	return nil, fmt.Errorf("wire: unterminated string at offset %d", start)
}

func (p *parser) boolNode() (*Node, error) {
	start := p.pos
	switch {
	case bytes.HasPrefix(p.raw[p.pos:], []byte("true")):
		p.pos += len("true")
	case bytes.HasPrefix(p.raw[p.pos:], []byte("false")):
		p.pos += len("false")
	default:
		return nil, fmt.Errorf("wire: invalid literal at offset %d", p.pos)
	}
	return &Node{Kind: Bool, Span: Span{Start: start, End: p.pos}, Bytes: p.raw[start:p.pos]}, nil
}

func (p *parser) nullNode() (*Node, error) {
	start := p.pos
	if !bytes.HasPrefix(p.raw[p.pos:], []byte("null")) {
		return nil, fmt.Errorf("wire: invalid literal at offset %d", p.pos)
	}
	p.pos += len("null")
	return &Node{Kind: Null, Span: Span{Start: start, End: p.pos}, Bytes: p.raw[start:p.pos]}, nil
}

func (p *parser) numberNode() (*Node, error) {
	start := p.pos
	for p.pos < len(p.raw) {
		c := p.raw[p.pos]
		if (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			p.pos++
			continue
		}
		break
	}
	if start == p.pos {
		return nil, fmt.Errorf("wire: expected value at offset %d", start)
	}
	if !json.Valid(p.raw[start:p.pos]) {
		return nil, fmt.Errorf("wire: invalid number at offset %d", start)
	}
	return &Node{Kind: Number, Span: Span{Start: start, End: p.pos}, Bytes: p.raw[start:p.pos]}, nil
}

// EditMode says how Edit.Bytes are interpreted.
type EditMode uint8

const (
	// ReplaceRaw replaces the span with Bytes verbatim. Bytes must form a
	// valid JSON value (or a valid fragment inside a string for callers that
	// know what they are doing).
	ReplaceRaw EditMode = iota
	// ReplaceString replaces a String node with Bytes as its decoded content,
	// re-quoting and escaping it as JSON.
	ReplaceString
	// Delete removes the span.
	Delete
)

// Edit is one byte-level change against a Document's raw buffer.
type Edit struct {
	Span   Span
	Bytes  []byte
	Mode   EditMode
	Reason string
	// PreserveLength rejects a replacement whose byte length differs from
	// the original span. Credential stand-ins rely on this.
	PreserveLength bool
}

// Apply applies edits to raw. It validates spans and overlaps, applies from
// the end so earlier offsets stay valid, and returns the new buffer.
func Apply(raw []byte, edits []Edit) ([]byte, error) {
	if len(edits) == 0 {
		return raw, nil
	}
	sorted := make([]Edit, len(edits))
	copy(sorted, edits)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Span.Start != sorted[j].Span.Start {
			return sorted[i].Span.Start < sorted[j].Span.Start
		}
		return sorted[i].Span.End < sorted[j].Span.End
	})
	for i, e := range sorted {
		if !e.Span.Valid() || e.Span.End > len(raw) {
			return nil, fmt.Errorf("wire: edit span %d..%d outside %d-byte document", e.Span.Start, e.Span.End, len(raw))
		}
		if i > 0 && e.Span.Start < sorted[i-1].Span.End {
			return nil, fmt.Errorf("wire: overlapping edits %d..%d and %d..%d",
				sorted[i-1].Span.Start, sorted[i-1].Span.End, e.Span.Start, e.Span.End)
		}
		if e.PreserveLength && e.Mode == ReplaceRaw && e.Span.Len() != len(e.Bytes) {
			return nil, fmt.Errorf("wire: edit at %d..%d must preserve length %d, got %d",
				e.Span.Start, e.Span.End, e.Span.Len(), len(e.Bytes))
		}
	}

	out := append([]byte(nil), raw...)
	for i := len(sorted) - 1; i >= 0; i-- {
		e := sorted[i]
		var repl []byte
		switch e.Mode {
		case Delete:
			repl = nil
		case ReplaceString:
			quoted, err := json.Marshal(string(e.Bytes))
			if err != nil {
				return nil, fmt.Errorf("wire: quote replacement: %w", err)
			}
			if e.PreserveLength && e.Span.Len() != len(quoted) {
				return nil, fmt.Errorf("wire: edit at %d..%d must preserve length %d, got %d",
					e.Span.Start, e.Span.End, e.Span.Len(), len(quoted))
			}
			repl = quoted
		case ReplaceRaw:
			repl = e.Bytes
		default:
			return nil, fmt.Errorf("wire: unknown edit mode %d", e.Mode)
		}
		out = append(out[:e.Span.Start], append(repl, out[e.Span.End:]...)...)
	}
	if !json.Valid(out) {
		return nil, fmt.Errorf("wire: edits produced invalid JSON")
	}
	return out, nil
}

// MustParse is Parse for tests and static fixtures.
func MustParse(raw []byte) *Node {
	n, err := Parse(raw)
	if err != nil {
		panic(err)
	}
	return n
}
