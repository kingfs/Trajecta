package secretmask

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// opaqueJSONFieldNames lists the JSON object members whose value is an opaque,
// integrity-protected protocol blob. Detection is skipped inside those values:
// the bytes are not a user secret, they must reach the provider exactly as they
// arrived, and the credential DFA has no way to know that a token-shaped run in
// ciphertext was not typed by a human.
//
// The case that motivated the list is encrypted_content in the OpenAI Responses
// API. The provider encrypts its reasoning state, gives the ciphertext to the
// client, and expects the same bytes back on the next turn; the value is
// base64url, so its alphabet overlaps every free-form credential body. A run
// that happens to spell `xai-` followed by enough token characters matches
// xai.api_key by chance, the masker rewrites a byte of the ciphertext, and the
// provider rejects the request as modified. There is nothing to protect by
// masking ciphertext, so the whole value is excluded from detection.
//
// The list is a closed set of exact member names rather than a heuristic on
// "looks random". A shape heuristic would also skip the base64-wrapped secrets
// some rules exist to protect (a PEM body, a JWT), and every skipped region is
// somewhere a real credential could hide.
var opaqueJSONFieldNames = [...]string{"encrypted_content"}

// isOpaqueJSONField reports whether a decoded object member name is in
// opaqueJSONFieldNames.
func isOpaqueJSONField(name string) bool {
	for _, candidate := range opaqueJSONFieldNames {
		if name == candidate {
			return true
		}
	}
	return false
}

// mayContainOpaqueJSONField is the cheap prefilter: most request bodies carry no
// opaque member at all, and those must not pay for a JSON tokenizer pass.
func mayContainOpaqueJSONField(src []byte) bool {
	for _, name := range opaqueJSONFieldNames {
		if bytes.Contains(src, []byte(name)) {
			return true
		}
	}
	return false
}

// opaqueValueSpan is the half-open byte range of one opaque JSON member value
// inside the buffer it was found in.
type opaqueValueSpan struct {
	start int
	end   int
}

// opaqueJSONValueSpans locates the values of every opaque JSON member in src.
//
// The body is walked with encoding/json rather than searched for the member
// name: a message is free to contain the literal bytes `"encrypted_content"` in
// its text, and skipping a region chosen by the prompt would let the prompt
// hide a real credential from the masker. The decoder only reports a key when
// the bytes occupy an object-key position, so a lookalike inside a string is
// not a field.
//
// A body that is not valid JSON yields no spans and Mask keeps its historical
// behaviour. Malformed JSON is not a reason to skip bytes: the masker errs on
// the side of protecting values it cannot prove are protocol blobs. That
// includes a body whose first value is complete but which has trailing bytes:
// only one whole JSON document is trusted, so a second value or trailing debris
// disables skipping. The prefilter above matches the literal member name, so a
// body that spells the name with JSON \u escapes is not recognised as carrying
// one; no JSON encoder produces that spelling for an ASCII key.
func opaqueJSONValueSpans(src []byte) []opaqueValueSpan {
	if !mayContainOpaqueJSONField(src) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	var spans []opaqueValueSpan
	if err := walkJSONValue(dec, src, &spans); err != nil {
		return nil
	}
	// The decoder stops after the first value, so a trailing value or debris
	// would otherwise leave the spans of an invalid body in place. Require EOF
	// (the decoder skips trailing JSON whitespace) before trusting them.
	if _, err := dec.Token(); err != io.EOF {
		return nil
	}
	return spans
}

// walkJSONValue consumes one JSON value from dec, descending into its members
// and elements.
func walkJSONValue(dec *json.Decoder, src []byte, spans *[]opaqueValueSpan) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	return walkJSONToken(dec, src, spans, token)
}

// walkJSONToken is walkJSONValue once the value's first token is known. Only
// the object branch has to look at keys: an array's elements are values, and a
// scalar has nothing below it.
func walkJSONToken(dec *json.Decoder, src []byte, spans *[]opaqueValueSpan, token json.Token) error {
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errJSONKeyNotString
			}
			if isOpaqueJSONField(key) {
				span, err := opaqueValueSpanFor(dec, src)
				if err != nil {
					return err
				}
				*spans = append(*spans, span)
				continue
			}
			if err := walkJSONValue(dec, src, spans); err != nil {
				return err
			}
		}
		_, err := dec.Token() // closing '}'
		return err
	case '[':
		for dec.More() {
			if err := walkJSONValue(dec, src, spans); err != nil {
				return err
			}
		}
		_, err := dec.Token() // closing ']'
		return err
	}
	return nil
}

// opaqueValueSpanFor consumes the value that follows an opaque member name and
// reports its byte range.
//
// The range is derived from the decoder's own position rather than by carving
// bytes out of the buffer: after Decode the offset is the end of the raw value,
// so subtracting the RawMessage length gives its start. The equality check
// catches an implementation that consumed trailing whitespace; if the range
// cannot be proven, the whole scan fails closed and nothing is skipped.
func opaqueValueSpanFor(dec *json.Decoder, src []byte) (opaqueValueSpan, error) {
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return opaqueValueSpan{}, err
	}
	end := int(dec.InputOffset())
	for end > 0 && isJSONSpaceByte(src[end-1]) {
		end--
	}
	start := end - len(raw)
	if start < 0 || end > len(src) || !bytes.Equal(src[start:end], raw) {
		return opaqueValueSpan{}, errJSONValueSpan
	}
	return opaqueValueSpan{start: start, end: end}, nil
}

// isJSONSpaceByte reports whether c is one of the four bytes JSON whitespace
// may consist of.
func isJSONSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// findingInOpaqueValue reports whether a finding overlaps an opaque value.
//
// Overlap rather than containment: the DFA cannot cross the quote that opens
// the value, so a real finding and an opaque value can only meet at the
// boundary, and a finding that touches the blob is treated as part of it.
func findingInOpaqueValue(f Finding, spans []opaqueValueSpan) bool {
	for _, span := range spans {
		if f.Start < span.end && f.End > span.start {
			return true
		}
	}
	return false
}

// The walker is fed bytes encoding/json already accepted, so these errors mean a
// decoder invariant changed rather than that the body is untrusted; they exist
// to make the failure path explicit and to disable skipping instead of guessing
// a byte range.
var (
	errJSONKeyNotString = errors.New("secretmask: JSON object key is not a string")
	errJSONValueSpan    = errors.New("secretmask: cannot locate JSON value span")
)
