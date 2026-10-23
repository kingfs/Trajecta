package secretmask

import "bytes"

// An armored private-key block is the half of the PEM pair that carries key
// material: the header line alone tells an operator that a key is present, but
// only the base64 body is the key. rules.rl matches the header line, because
// that is what a DFA can decide in one pass; pemBlockAt extends the match to
// the footer natively, the same way scan.go's emitDigits classifies a digit run
// after the coarse pattern anchored it.
//
// The extension is native rather than another rules.rl pattern for three
// reasons. The footer has to repeat the header's label exactly, and a
// back-reference is the one thing a regular expression cannot express. A
// rules.rl block pattern would have to spell each label out and repeat the body
// loop per label, growing the committed table for no extra evidence. And the
// body arrives with its line breaks either literal or JSON-escaped, sometimes
// indented by a YAML block scalar, which native code can accept without
// touching the machine.
const (
	pemBeginMarker = "-----BEGIN "
	pemEndMarker   = "-----END "
	// pemKeyMarker is the tail every private-key label shares. It is what makes
	// the header rule private-key-specific: -----BEGIN CERTIFICATE----- and
	// -----BEGIN PUBLIC KEY----- do not contain it.
	pemKeyLabelSuffix = "PRIVATE KEY"
	pemKeyMarker      = " PRIVATE KEY" + pemArmorTail
	pemArmorTail      = "-----"

	// maxPEMBlockBytes bounds one block. A 16384-bit RSA key is about 12 KB of
	// armor, so 32 KB leaves room without letting a header that never gets an
	// END make the extender walk a whole document. The body scan stops at the
	// first byte outside the armor alphabet as well, so ordinary prose after a
	// header costs a few bytes rather than the cap.
	maxPEMBlockBytes = 32 << 10
	// minPEMBodyBytes is the shortest body that can be a key. It keeps a
	// degenerate "BEGIN immediately followed by END" from becoming a block
	// whose body has nothing to substitute, which would make the stand-in
	// identical to the value it is supposed to hide.
	minPEMBodyBytes = 32
)

// pemBlockAt returns the end offset of the armored private-key block whose
// header occupies src[start:end], and reports whether it found a complete one.
// The header match comes from the DFA; this function only decides where the
// block ends.
//
// A block is accepted only when the footer repeats the header's label exactly
// and every byte between them stays inside the armor alphabet (base64,
// whitespace, JSON escapes for the line breaks a string carries). That second
// condition is what stops a truncated block from spanning into whatever
// follows it: an unescaped quote, a space in the middle of a line, a second
// BEGIN line or any other punctuation ends the search and leaves the header
// reported but not rewritten.
func pemBlockAt(src []byte, start, end int) (int, bool) {
	header := src[start:end]
	if len(header) < len(pemBeginMarker)+len(pemKeyLabelSuffix)+len(pemArmorTail) {
		return 0, false
	}
	if !bytes.HasPrefix(header, []byte(pemBeginMarker)) || !bytes.HasSuffix(header, []byte(pemArmorTail)) {
		return 0, false
	}
	// label is everything between "-----BEGIN " and the trailing "-----"; for a
	// header the DFA accepted it always ends in " PRIVATE KEY", which is checked
	// again here so this function is safe to call on its own.
	label := header[len(pemBeginMarker) : len(header)-len(pemArmorTail)]
	if !bytes.HasSuffix(label, []byte(pemKeyLabelSuffix)) {
		return 0, false
	}

	footer := make([]byte, 0, len(pemEndMarker)+len(label)+len(pemArmorTail))
	footer = append(footer, pemEndMarker...)
	footer = append(footer, label...)
	footer = append(footer, pemArmorTail...)

	limit := end + maxPEMBlockBytes
	if limit > len(src) {
		limit = len(src)
	}
	// atLineStart allows the indentation a YAML block scalar puts in front of
	// every line, while a space in the middle of a line still ends the search:
	// RFC 7468 armor carries no spaces inside a line, and prose does.
	atLineStart := true
	for i := end; i < limit; {
		switch c := src[i]; {
		case c == '-':
			if bytes.HasPrefix(src[i:limit], footer) {
				if i-end < minPEMBodyBytes {
					return 0, false
				}
				return i + len(footer), true
			}
			// Base64 contains no dash, so a dash that does not begin this
			// block's footer means the block was truncated somewhere before it.
			return 0, false
		case c == '\\':
			if i+1 >= limit || !isJSONEscapeByte(src[i+1]) {
				return 0, false
			}
			atLineStart = src[i+1] == 'n'
			i += 2
		case c == '\n':
			atLineStart = true
			i++
		case c == '\r' || c == '\t':
			i++
		case c == ' ':
			if !atLineStart {
				return 0, false
			}
			i++
		case isBase64ArmorByte(c):
			atLineStart = false
			i++
		default:
			return 0, false
		}
	}
	return 0, false
}

// isBase64ArmorByte reports whether c belongs to the body of a PEM block. '+',
// '/', '=' and the base64 letters are the body; \r and \n are the line breaks
// RFC 7468 puts every 64 characters.
func isBase64ArmorByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '+', c == '/', c == '=', c == '\r', c == '\n':
		return true
	}
	return false
}

// isJSONEscapeByte reports whether b may follow a backslash inside a JSON
// string. This is how a block embedded in a request body arrives: the newlines
// between armor lines are the two bytes '\' 'n', not 0x0A.
func isJSONEscapeByte(b byte) bool {
	switch b {
	case 'n', 'r', 't', '"', '\\', '/', 'b', 'f', 'u':
		return true
	}
	return false
}
