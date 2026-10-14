package secretmask

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// DefaultVaultCapacity bounds how many distinct real values one Vault
// remembers; the oldest is evicted first. A proxy sees few distinct
// credentials, so the cap only exists to keep a long-running process bounded.
const DefaultVaultCapacity = 4096

// keystreamDomain separates this use of the seed from any other.
const keystreamDomain = "secretmask/v1"

// fakeEntry holds one fake in the form Restore compares against, so matching
// does not allocate.
type fakeEntry struct {
	text string
	raw  []byte
}

// Vault renders the stand-in values that replace real credentials, and maps
// them back.
//
// A fake is a pure function of the seed, the rule name and the real value, so
// the same credential always renders to the same fake. That stability is not
// cosmetic: a model that sees one fake must see the same one in the next turn
// or the conversation stops making sense, and a provider that caches on the
// request prefix must see an unchanged prefix to get a cache hit.
//
// A fake is also exactly as long as the real value, and keeps the real value's
// prefix and every non-alphanumeric character. Length is load-bearing: the
// proxy copies the upstream Content-Length to the client verbatim and lets
// net/http derive the request length from the body it is handed, so a
// length-changing replacement would desynchronise both. Keeping the prefix and
// the punctuation keeps the value recognisable to the tools that only ever
// match on shape.
//
// A Vault is safe for concurrent use.
type Vault struct {
	mu       sync.RWMutex
	seed     []byte
	capacity int
	det      Detector

	byReal map[string]string
	byFake map[string]string
	// index groups fakes by their first byte. Restore skips a position with one
	// array lookup unless some fake starts there, and each bucket is ordered
	// longest first so the longest candidate wins.
	index    map[byte][]fakeEntry
	firstFit [256]bool
	fakeLen  int
	order    []string
}

// NewVault returns a Vault. A nil or empty seed is replaced with 32 bytes from
// crypto/rand, which is the recommended mode for a live process: the mapping
// exists only in memory, so a fake observed on the wire cannot be reversed
// without this process, and restarting the proxy invalidates every fake. Pass
// an explicit seed only for tests and for reproducing a session.
func NewVault(seed []byte) *Vault {
	if len(seed) == 0 {
		seed = make([]byte, 32)
		// crypto/rand.Read never returns an error and always fills the buffer.
		_, _ = rand.Read(seed)
	}
	cp := make([]byte, len(seed))
	copy(cp, seed)
	return &Vault{
		seed:     cp,
		capacity: DefaultVaultCapacity,
		byReal:   make(map[string]string),
		byFake:   make(map[string]string),
		index:    make(map[byte][]fakeEntry),
	}
}

// Len reports how many real values the vault currently maps.
func (v *Vault) Len() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.byReal)
}

// Holdback is the number of trailing bytes a streaming restore must withhold
// before it can be sure no fake starts in them. It is zero while the vault is
// empty, so a request that masked nothing pays nothing.
func (v *Vault) Holdback() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.fakeLen == 0 {
		return 0
	}
	return v.fakeLen - 1
}

// MaskEvent records one replacement. It deliberately carries no values: the
// audit trail names the rule and the offsets and leaves the bytes where they
// are, so a cassette prelude does not become a second copy of the secret.
type MaskEvent struct {
	Rule       RuleID
	Start, End int
	Score      int
	Confidence Confidence
	Reasons    Reason
}

// Attributes renders the event as JSON-ready values for a record-file event.
func (e MaskEvent) Attributes() map[string]interface{} {
	r := ruleTable[e.Rule]
	return map[string]interface{}{
		"rule":       r.Name,
		"vendor":     r.Vendor,
		"kind":       r.Kind.String(),
		"start":      e.Start,
		"end":        e.End,
		"length":     e.End - e.Start,
		"score":      e.Score,
		"confidence": e.Confidence.String(),
		"reasons":    e.Reasons.String(),
	}
}

// Mask rewrites every credential in src that the detector both recognises and
// considers safe to rewrite, and returns the rewritten bytes with one event per
// replacement. src is never modified; when there is nothing to rewrite the
// original slice is returned unchanged.
//
// Masking is idempotent: a value the vault already knows as a fake is left
// alone, so masking a body twice is the same as masking it once.
func (v *Vault) Mask(src []byte) ([]byte, []MaskEvent) {
	if len(src) == 0 {
		return src, nil
	}
	v.mu.RLock()
	alreadyFake := len(v.byFake) != 0
	v.mu.RUnlock()

	var usable []Finding
	for _, f := range v.det.Scan(src) {
		if !f.Maskable() {
			continue
		}
		if policySkipMask(&ruleTable[f.ID]) {
			continue
		}
		if alreadyFake && v.isFake(src[f.Start:f.End]) {
			continue
		}
		usable = append(usable, f)
	}
	if len(usable) == 0 {
		return src, nil
	}

	out := make([]byte, len(src))
	copy(out, src)
	events := make([]MaskEvent, 0, len(usable))
	for _, f := range usable {
		real := src[f.Start:f.End]
		fake := v.fakeFor(&ruleTable[f.ID], real)
		copy(out[f.Start:f.End], fake)
		events = append(events, MaskEvent{
			Rule:       f.ID,
			Start:      f.Start,
			End:        f.End,
			Score:      f.Score,
			Confidence: f.Confidence,
			Reasons:    f.Reasons,
		})
	}
	return out, events
}

// policySkipMask reports whether a rule's match must never be rewritten, even
// though it is reported.
//
// structural.pem_private_key matches only the "-----BEGIN ... PRIVATE KEY-----"
// line. That line carries no key material: the base64 body after it does, and
// no rule covers that body. Rewriting the header would corrupt the file without
// protecting anything, because the real key would still leave the machine.
func policySkipMask(r *Rule) bool { return r.Kind == KindPrivateKey }

func (v *Vault) isFake(tok []byte) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	_, ok := v.byFake[string(tok)]
	return ok
}

// fakeFor returns the fake for real, rendering and recording it on first use.
func (v *Vault) fakeFor(r *Rule, real []byte) string {
	key := string(real)

	v.mu.Lock()
	defer v.mu.Unlock()
	if f, ok := v.byReal[key]; ok {
		return f
	}
	// The loop is a safety net, not a search: the first attempt differs from
	// real with overwhelming probability, and a collision needs an existing
	// fake to be the same bytes.
	for attempt := 0; attempt < 64; attempt++ {
		f := v.render(r, real, attempt)
		if f == key {
			continue
		}
		if _, taken := v.byFake[f]; taken {
			continue
		}
		v.insertLocked(key, f)
		return f
	}
	// Unreachable outside of a deliberately hostile vault state.
	return key
}

// render builds a same-length fake.
//
// Everything up to protectedHead is copied verbatim, every non-alphanumeric
// byte is copied verbatim, a rule's declared literals are copied verbatim, and
// every remaining alphanumeric byte is replaced by one from its own class. The
// class mapping is what keeps an all-lowercase-hex value looking like hex, and
// what keeps a base64url value looking like base64url.
func (v *Vault) render(r *Rule, real []byte, attempt int) string {
	head := protectedHead(r, real)

	litStart, litEnd := -1, -1
	for _, lit := range r.Literals {
		if i := bytes.Index(real[head:], []byte(lit)); i >= 0 {
			litStart, litEnd = head+i, head+i+len(lit)
			break
		}
	}

	out := make([]byte, len(real))
	copy(out, real)
	ks := keystream(v.seed, r.Name, attempt, len(real))
	// The replacement alphabet is chosen per run of alphanumeric bytes rather
	// than for the whole value: a vendor that puts a hex segment next to a
	// free-form one (zhipu, the Langfuse UUIDs, a Databricks token) would
	// otherwise lose the hex shape as soon as any other segment was not hex.
	for i := head; i < len(real); {
		if i >= litStart && i < litEnd || !isAlnumByte(real[i]) {
			i++
			continue
		}
		j := i
		for j < len(real) && isAlnumByte(real[j]) {
			j++
		}
		hexMode := allLowerHex(real[i:j])
		for k := i; k < j; k++ {
			if k >= litStart && k < litEnd {
				continue
			}
			out[k] = substitute(real[k], ks[k], hexMode)
		}
		i = j
	}
	return string(out)
}

func isAlnumByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// protectedHead is how many leading bytes of a match stay verbatim. The longest
// declared prefix wins, so a rule with alternate prefixes keeps whichever one
// actually matched, and PrefixSkip covers a run the prefix cannot spell out
// (the two version digits in sk-ant-api03-).
func protectedHead(r *Rule, tok []byte) int {
	if r.Kind == KindJSONWebToken {
		// A JWT's header names the algorithm the signature length is checked
		// against, so it stays readable; the claims and the signature do not.
		if i := bytes.IndexByte(tok, '.'); i >= 0 {
			return clampHead(i+1, tok)
		}
		return 0
	}
	best := 0
	for _, p := range r.Prefixes {
		if len(p) > len(tok) || len(p) <= best {
			continue
		}
		if string(tok[:len(p)]) == p {
			best = len(p)
		}
	}
	if best == 0 {
		return 0
	}
	return clampHead(best+r.PrefixSkip, tok)
}

// clampHead keeps at least half of a match replaceable. Without it a rule whose
// declared prefix covers the whole match would render the value unchanged.
func clampHead(head int, tok []byte) int {
	if half := len(tok) / 2; head > half {
		return half
	}
	return head
}

// substitute maps one byte onto another of the same class. Non-alphanumeric
// bytes are returned unchanged so separators, dashes and dots keep their
// positions, which is most of what makes a fake look like the real thing.
func substitute(c, k byte, hexMode bool) byte {
	switch {
	case c >= '0' && c <= '9':
		return '0' + k%10
	case c >= 'a' && c <= 'z':
		if hexMode {
			return 'a' + k%6
		}
		return 'a' + k%26
	case c >= 'A' && c <= 'Z':
		return 'A' + k%26
	default:
		return c
	}
}

// allLowerHex reports whether a value is drawn from lowercase hex plus
// separators, which is how several vendors build keys. Restricting the
// replacement alphabet in that case keeps the fake in the same shape.
func allLowerHex(b []byte) bool {
	seen := false
	for _, c := range b {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
			seen = true
		case c == '-', c == '_', c == '.', c == '/', c == '+', c == '=', c == ':', c == '~':
			// Separators do not decide the alphabet.
		default:
			return false
		}
	}
	return seen
}

// keystream expands the seed into len(real) bytes that depend on the rule and
// on the real value, so two credentials of the same vendor get unrelated fakes
// while one credential keeps the same fake forever.
func keystream(seed []byte, label string, attempt, n int) []byte {
	if n <= 0 {
		return nil
	}
	out := make([]byte, 0, n)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(attempt))
	for i := 0; len(out) < n; i++ {
		mac := hmac.New(sha256.New, seed)
		mac.Write([]byte(keystreamDomain))
		mac.Write([]byte(label))
		mac.Write(counter[:])
		var idx [4]byte
		binary.BigEndian.PutUint32(idx[:], uint32(i))
		mac.Write(idx[:])
		out = mac.Sum(out)
	}
	return out[:n]
}

func (v *Vault) insertLocked(real, fake string) {
	if v.capacity > 0 && len(v.byReal) >= v.capacity {
		v.evictLocked()
	}
	v.byReal[real] = fake
	v.byFake[fake] = real
	b := fake[0]
	v.index[b] = insertLongestFirst(v.index[b], fakeEntry{text: fake, raw: []byte(fake)})
	v.firstFit[b] = true
	if len(fake) > v.fakeLen {
		v.fakeLen = len(fake)
	}
	v.order = append(v.order, real)
}

func (v *Vault) evictLocked() {
	if len(v.order) == 0 {
		return
	}
	oldest := v.order[0]
	v.order = v.order[1:]
	fake, ok := v.byReal[oldest]
	if !ok {
		return
	}
	delete(v.byReal, oldest)
	delete(v.byFake, fake)

	b := fake[0]
	bucket := v.index[b]
	for i := range bucket {
		if bucket[i].text == fake {
			bucket = append(bucket[:i], bucket[i+1:]...)
			break
		}
	}
	if len(bucket) == 0 {
		delete(v.index, b)
		v.firstFit[b] = false
	} else {
		v.index[b] = bucket
	}
	if len(fake) == v.fakeLen {
		v.fakeLen = 0
		for _, f := range v.byFake {
			if len(f) > v.fakeLen {
				v.fakeLen = len(f)
			}
		}
	}
}

// insertLongestFirst keeps a bucket ordered by descending length so Restore can
// take the first match at a position.
func insertLongestFirst(bucket []fakeEntry, e fakeEntry) []fakeEntry {
	i := 0
	for i < len(bucket) && len(bucket[i].text) >= len(e.text) {
		i++
	}
	bucket = append(bucket, fakeEntry{})
	copy(bucket[i+1:], bucket[i:])
	bucket[i] = e
	return bucket
}
