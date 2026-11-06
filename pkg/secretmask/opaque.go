package secretmask

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// opaqueJSONField describes a JSON object member whose value is an opaque,
// integrity-protected protocol payload. Detection is skipped inside the value:
// the bytes are not a user secret, they must reach the provider exactly as they
// arrived, and the credential DFA has no way to know that a token-shaped run in
// ciphertext or base64 was not typed by a human.
//
// The field is usually the whole contract. dataURIOnly exists for generic member
// names such as url and image_url: those are also used for ordinary remote
// links, where a credential in the URL still has to be masked. Only a base64
// data URI is skipped in that case, because a data URI carries a binary payload
// and no user-typed text.
type opaqueJSONField struct {
	name        string
	dataURIOnly bool
}

// opaqueJSONFields are the object members whose values are skipped wherever
// they appear. Every entry is backed by a provider contract that says the value
// is opaque or must be sent back unmodified:
//
//   - encrypted_content: an OpenAI Responses reasoning item's encrypted reasoning
//     state. The provider hands the ciphertext to the client and expects the same
//     bytes back on the next turn; the value is base64url, so its alphabet
//     overlaps every free-form credential body. A run that happens to spell
//     `xai-` followed by enough token characters matched xai.api_key by chance,
//     the masker rewrote a byte of the ciphertext, and the provider rejected the
//     request as modified.
//   - thoughtSignature / thought_signature: a Gemini and Vertex Part's "opaque
//     signature for the thought so it can be reused in subsequent requests"
//     (Google's own schema wording). It is byte-format base64 and is sent back
//     on later turns.
//   - reasoningContent / reasoningText / redactedContent: AWS Bedrock Converse's
//     ReasoningContentBlock. reasoningText carries the reasoning text plus a
//     signature that verifies the text; the API reference says to "include the
//     text and its signature unmodified". redactedContent is the encrypted
//     reasoning as base64 bytes. Skipping the union wrapper keeps both intact.
//   - inlineData / inline_data: a Gemini/Vertex Part's inline binary media
//     (base64-encoded bytes), not text.
//   - file_data: an OpenAI Responses input_file's base64 file body.
//   - input_audio: an OpenAI chat/responses audio part (base64 data + format).
//   - image_url / url: only when the value is a base64 data URI (an image
//     payload embedded in the body); an ordinary remote URL is still scanned.
//
// There is deliberately no entry for a bare `signature` or `data` member: those
// names are generic, and skipping every object that happens to use one would let
// arbitrary JSON hide a real credential. The Anthropic thinking blocks they
// belong to are covered by opaqueJSONObjectTypes instead.
var opaqueJSONFields = [...]opaqueJSONField{
	{name: "encrypted_content"},
	{name: "thoughtSignature"},
	{name: "thought_signature"},
	{name: "reasoningContent"},
	{name: "reasoningText"},
	{name: "redactedContent"},
	{name: "inlineData"},
	{name: "inline_data"},
	{name: "file_data"},
	{name: "input_audio"},
	{name: "image_url", dataURIOnly: true},
	{name: "url", dataURIOnly: true},
}

// opaqueJSONObjectTypes are values of an object's "type" member that make the
// whole object opaque, so every string inside it is skipped together. This is
// how member names that are too generic to list on their own are covered:
//
//   - thinking: an Anthropic Messages extended-thinking block
//     (`{"type":"thinking","thinking":"…","signature":"…"}`). The signature
//     verifies the text; the API requires the complete block back unmodified,
//     so masking one byte of `thinking` is as fatal as masking `signature`.
//   - redacted_thinking: the encrypted-reasoning block
//     (`{"type":"redacted_thinking","data":"…"}`); `data` is base64 ciphertext.
//   - reasoning and the OpenRouter reasoning-detail types: a provider reasoning
//     item (`{"type":"reasoning","summary":…,"signature":…,"encrypted_content":…}`)
//     and its Chat Completions form in `reasoning_details`
//     (`reasoning.text`, `reasoning.encrypted`, `reasoning.summary`). OpenRouter
//     documents that the reasoning item's content, summary, signature and
//     encrypted_content must be echoed back unchanged, and that consecutive
//     reasoning blocks must match what the model produced.
//
// The match is structural, not provider-scoped: any JSON object using one of
// these type values is skipped. The generic names inside them are exactly why
// the rule is written this way; see the known-boundary note in
// docs/SECRET_MASKING.md.
var opaqueJSONObjectTypes = [...]string{
	"thinking",
	"redacted_thinking",
	"reasoning",
	"reasoning.text",
	"reasoning.encrypted",
	"reasoning.summary",
}

// opaqueJSONScopedField marks a member that is opaque only inside an object of
// one type. The member name is generic (data), so it must not be skipped on its
// own, and the object may hold other members that still have to be scanned.
type opaqueJSONScopedField struct {
	objectType string
	member     string
}

// opaqueJSONScopedFields cover the payload of an Anthropic image/document
// `source` object (`{"type":"base64","media_type":"image/png","data":"…"}`).
// Only `data` is the binary payload; siblings keep their normal scanning.
var opaqueJSONScopedFields = [...]opaqueJSONScopedField{
	{objectType: "base64", member: "data"},
}

// opaqueJSONScopedMembers indexes the scoped member names for the walker.
var opaqueJSONScopedMembers = func() map[string]struct{} {
	members := make(map[string]struct{}, len(opaqueJSONScopedFields))
	for _, scoped := range opaqueJSONScopedFields {
		members[scoped.member] = struct{}{}
	}
	return members
}()

// isOpaqueJSONScopedField reports whether member is opaque inside an object of
// objectType.
func isOpaqueJSONScopedField(objectType, member string) bool {
	for _, scoped := range opaqueJSONScopedFields {
		if scoped.objectType == objectType && scoped.member == member {
			return true
		}
	}
	return false
}

// opaqueJSONFieldFor returns the specification for a member name.
func opaqueJSONFieldFor(name string) (opaqueJSONField, bool) {
	for _, field := range opaqueJSONFields {
		if field.name == name {
			return field, true
		}
	}
	return opaqueJSONField{}, false
}

// isOpaqueJSONObjectType reports whether an object's decoded "type" value makes
// the whole object opaque.
func isOpaqueJSONObjectType(objectType string) bool {
	for _, candidate := range opaqueJSONObjectTypes {
		if objectType == candidate {
			return true
		}
	}
	return false
}

// base64DataURIMarker is the part of a base64 data URI that distinguishes it
// from a data URI carrying text or a percent-encoded payload.
const base64DataURIMarker = ";base64,"

// opaqueJSONPrefilterTokens are the quoted literals mayContainOpaqueJSONValue
// searches for: the non-data-URI member names and the whole-object type values.
// They are built once so the per-request prefilter does not allocate.
var opaqueJSONPrefilterTokens = func() [][]byte {
	tokens := make([][]byte, 0, len(opaqueJSONFields)+len(opaqueJSONObjectTypes)+len(opaqueJSONScopedFields))
	for _, field := range opaqueJSONFields {
		if field.dataURIOnly {
			continue
		}
		tokens = append(tokens, quotedJSONString(field.name))
	}
	for _, objectType := range opaqueJSONObjectTypes {
		tokens = append(tokens, quotedJSONString(objectType))
	}
	for _, scoped := range opaqueJSONScopedFields {
		tokens = append(tokens, quotedJSONString(scoped.objectType))
	}
	return tokens
}()

// mayContainOpaqueJSONValue is the cheap prefilter: most request bodies carry no
// opaque member at all, and those must not pay for a JSON tokenizer pass.
//
// The search strings are quoted so the common case (a member name or type value
// spelled as a JSON string) matches, while prose that merely mentions a word
// does not. A data URI is detected by its own marker instead of the generic
// member name, because url/image_url appear in almost every body.
func mayContainOpaqueJSONValue(src []byte) bool {
	for _, token := range opaqueJSONPrefilterTokens {
		if bytes.Contains(src, token) {
			return true
		}
	}
	return bytes.Contains(src, []byte(base64DataURIMarker))
}

// quotedJSONString renders a name as a JSON string literal. Only used to build
// the prefilter tokens at init.
func quotedJSONString(name string) []byte {
	out := make([]byte, 0, len(name)+2)
	out = append(out, '"')
	out = append(out, name...)
	out = append(out, '"')
	return out
}

// opaqueValueSpan is the half-open byte range of one opaque JSON value inside
// the buffer it was found in.
type opaqueValueSpan struct {
	start int
	end   int
}

// scopedOpaqueValue is a member value whose opacity still depends on the
// containing object's type, which may be declared after it.
type scopedOpaqueValue struct {
	member string
	span   opaqueValueSpan
}

// opaqueJSONValueSpans locates every opaque JSON value in src.
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
	if !mayContainOpaqueJSONValue(src) {
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
// the object branch has state: an array's elements are values, and a scalar has
// nothing below it.
func walkJSONToken(dec *json.Decoder, src []byte, spans *[]opaqueValueSpan, token json.Token) error {
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		// InputOffset is just past the consumed '{', so the object starts one
		// byte earlier. That position is what whole-object spans are built from.
		return walkJSONObject(dec, src, spans, int(dec.InputOffset())-1)
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

// walkJSONObject walks one object. Candidate opaque values are collected in
// pending and only published after the closing brace, because an object can
// carry its "type" member after the value that the type makes opaque.
func walkJSONObject(dec *json.Decoder, src []byte, spans *[]opaqueValueSpan, start int) error {
	var objectType string
	var pending []opaqueValueSpan
	var scoped []scopedOpaqueValue
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errJSONKeyNotString
		}
		// The type member decides whether this whole object is opaque. Its value
		// is a scalar string in every protocol here; if it is not, fall through
		// and walk it like any other value.
		if key == "type" && nextJSONValueIsString(src, dec) {
			span, err := opaqueValueSpanFor(dec, src)
			if err != nil {
				return err
			}
			if decoded, ok := decodeJSONString(src[span.start:span.end]); ok {
				objectType = decoded
			}
			continue
		}
		if _, ok := opaqueJSONScopedMembers[key]; ok {
			// A scoped payload is a scalar (the base64 string), but a member
			// with the same generic name elsewhere may hold a container. Walk
			// the container so opaque fields inside it are still found instead
			// of being consumed unread.
			if !nextJSONValueIsString(src, dec) {
				if err := walkJSONValue(dec, src, spans); err != nil {
					return err
				}
				continue
			}
			span, err := opaqueValueSpanFor(dec, src)
			if err != nil {
				return err
			}
			scoped = append(scoped, scopedOpaqueValue{member: key, span: span})
			continue
		}
		field, opaque := opaqueJSONFieldFor(key)
		if opaque {
			if field.dataURIOnly {
				// url/image_url also carry ordinary remote links, so only a
				// string that proves it is a base64 data URI is skipped. A
				// non-string value (chat's image_url object) must still be
				// walked so its own url member is checked.
				if !nextJSONValueIsString(src, dec) {
					if err := walkJSONValue(dec, src, spans); err != nil {
						return err
					}
					continue
				}
				span, err := opaqueValueSpanFor(dec, src)
				if err != nil {
					return err
				}
				if isBase64DataURI(src[span.start:span.end]) {
					pending = append(pending, span)
				}
				continue
			}
			span, err := opaqueValueSpanFor(dec, src)
			if err != nil {
				return err
			}
			pending = append(pending, span)
			continue
		}
		if err := walkJSONValue(dec, src, spans); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return err
	}
	if isOpaqueJSONObjectType(objectType) {
		end := int(dec.InputOffset())
		if start < 0 || end > len(src) || end <= start || src[start] != '{' || src[end-1] != '}' {
			return errJSONValueSpan
		}
		*spans = append(*spans, opaqueValueSpan{start: start, end: end})
		return nil
	}
	*spans = append(*spans, pending...)
	for _, value := range scoped {
		if isOpaqueJSONScopedField(objectType, value.member) {
			*spans = append(*spans, value.span)
		}
	}
	return nil
}

// opaqueValueSpanFor consumes the value that follows a member name and reports
// its byte range.
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

// nextJSONValueIsString reports whether the next value the decoder will read is
// a JSON string. It only peeks at the buffer, so the caller can choose between
// consuming the scalar as RawMessage and walking a container normally.
func nextJSONValueIsString(src []byte, dec *json.Decoder) bool {
	start := jsonValueStart(src, int(dec.InputOffset()))
	return start >= 0 && start < len(src) && src[start] == '"'
}

// jsonValueStart returns the first byte of the value following an object key.
// In the current decoder InputOffset is at the colon; a decoder version that
// consumes the colon leaves it on the value, so the no-colon path still returns
// the position after any whitespace rather than failing the peek.
func jsonValueStart(src []byte, offset int) int {
	if offset < 0 || offset > len(src) {
		return -1
	}
	i := offset
	for i < len(src) && isJSONSpaceByte(src[i]) {
		i++
	}
	if i < len(src) && src[i] == ':' {
		i++
		for i < len(src) && isJSONSpaceByte(src[i]) {
			i++
		}
	}
	return i
}

// decodeJSONString decodes a raw JSON string literal.
func decodeJSONString(raw []byte) (string, bool) {
	if len(raw) < 2 || raw[0] != '"' {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// isBase64DataURI reports whether raw is a JSON string literal whose decoded
// content is a base64 data URI. The marker is required so that a data URI
// carrying text (where a credential could sit in the text part) is not skipped.
func isBase64DataURI(raw []byte) bool {
	if len(raw) < 2 || raw[0] != '"' || !bytes.HasPrefix(raw, []byte(`"data:`)) {
		return false
	}
	comma := bytes.IndexByte(raw, ',')
	if comma < 0 || comma > 128 {
		return false
	}
	return bytes.Contains(raw[:comma], []byte(";base64"))
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
