package secretmask

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"log/slog"
	"strings"
	"sync"
)

// DefaultVaultCapacity is the headroom added above a positive mapping load
// window. Load-all (the default) disables eviction instead, because the point of
// loading every pair is that every recorded fake stays recognizable; memory then
// grows with the number of distinct sensitive values, not with request volume.
// With a positive limit the oldest is evicted first, and an evicted pair is
// looked up in the durable store and reused if its real value appears again.
const DefaultVaultCapacity = 4096

// keystreamDomain separates this use of the seed from any other.
const keystreamDomain = "secretmask/v1"

// fakeEntry holds one fake in the form Restore compares against, so matching
// does not allocate.
type fakeEntry struct {
	text string
	raw  []byte
	// entry is how many leading bytes a run must cover before this fake may
	// open a chain. It is chainMinEntry for ordinary values and
	// chainMinEntryLong for a long fake such as an armored private-key block;
	// see chainLongFake.
	entry int
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

	// store is the durable side of the mapping, nil when the vault is purely
	// in-memory. It is consulted only when a real value is not already in
	// memory.
	store MappingStore
	// createMu serializes the slow path of fakeFor - the store lookup and the
	// render - so two requests that carry the same new value cannot race to
	// create and persist two mappings. Masking an already-known value never
	// takes it.
	createMu sync.Mutex
	// closeOnce makes Close idempotent; closeErr is the result of the one close.
	closeOnce sync.Once
	closeErr  error
}

// NewVault returns an in-memory Vault. A nil or empty seed is replaced with 32
// bytes from crypto/rand, which is the recommended mode for a live process: the
// mapping exists only in memory, so a fake observed on the wire cannot be
// reversed without this process, and restarting the proxy invalidates every
// fake. Pass an explicit seed only for tests and for reproducing a session.
//
// Use NewVaultWithStore when the mapping should survive a restart.
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

// NewVaultWithStore returns a Vault that loads its mappings from store and
// records every new one through it. A limit of zero or less loads every pair and
// keeps it resident; a positive limit loads the newest limit and leaves
// DefaultVaultCapacity of headroom before evicting the oldest.
//
// Persisting the pair is what makes a stand-in stable across a restart. Without
// it a fake rendered before the restart is just an unknown value to the new
// process: the detector sees a well-formed identity number or mobile number and
// renders a second fake over it, so the upstream conversation, which still holds
// the first fake, and the local side, which needs the real value, disagree. A
// loaded fake is in byFake, so Mask leaves it alone, and a loaded real is in
// byReal, so it renders to the recorded fake rather than a new one.
//
// The vault owns store and closes it from Close.
func NewVaultWithStore(seed []byte, store MappingStore, limit int) (*Vault, error) {
	v := NewVault(seed)
	if store == nil {
		return v, nil
	}
	if limit < 0 {
		limit = 0
	}
	mappings, err := store.LoadMappings(limit)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	v.store = store
	if limit == 0 {
		// Load-all is also no-evict: evicting a loaded pair after
		// DefaultVaultCapacity new values would silently reopen the window the
		// default exists to close. Memory grows with the distinct values the
		// mapping file already grows with.
		v.capacity = 0
	} else if need := len(mappings) + DefaultVaultCapacity; need > v.capacity {
		v.capacity = need
	}
	// LoadMappings returns newest first, and eviction is oldest first, so insert
	// in reverse: the slice then reads newest first while order reads oldest
	// first.
	for i := len(mappings) - 1; i >= 0; i-- {
		v.loadMapping(mappings[i])
	}
	return v, nil
}

// Close flushes any pending mapping appends and closes the mapping store. It is
// idempotent, and a vault without a store has nothing to close. Requests must
// have stopped before Close: a mask that starts after the store closes cannot
// persist its pair, though the in-memory mapping still works for this process.
func (v *Vault) Close() error {
	if v == nil {
		return nil
	}
	v.closeOnce.Do(func() {
		if v.store != nil {
			v.closeErr = v.store.Close()
		}
	})
	return v.closeErr
}

// loadMapping inserts one persisted pair. A pair that is malformed - an empty
// side, unequal lengths, or a fake already owned by a different real value - is
// skipped rather than installed, because installing it would either break the
// length invariant or make Restore return the wrong real value.
func (v *Vault) loadMapping(m Mapping) {
	if m.Real == "" || m.Fake == "" || m.Real == m.Fake || len(m.Real) != len(m.Fake) {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.byReal[m.Real]; ok {
		return
	}
	if owner, taken := v.byFake[m.Fake]; taken && owner != m.Real {
		return
	}
	v.insertLocked(m.Real, m.Fake)
}

// Len reports how many real values the vault currently maps.
func (v *Vault) Len() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.byReal)
}

// Holdback is the worst case number of trailing bytes a caretaker of this vault
// has to keep before it can be sure no fake starts in them, which is one byte
// short of the longest fake. It bounds a buffer a caller sizes up front; the
// Restorer itself withholds only the runs and tails its scan cannot yet judge,
// which is normally no more than the stand-in it is waiting on. It is zero
// while the vault is empty, so a request that masked nothing pays nothing.
func (v *Vault) Holdback() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.fakeLen == 0 {
		return 0
	}
	return v.fakeLen - 1
}

// MaskEvent records one replacement, and carries both sides of it so an audit
// written at mask time can show what stood in for what.
//
// Original and StandIn must never reach a cassette prelude: Attributes is the
// only rendering a record file may use, and it names the rule and the offsets
// and leaves the bytes where they are. A prelude is copied into every trace
// index, exported and read by the monitor, so writing the pair there would turn
// the audit trail into a second, less protected copy of the secret. The pair is
// persisted separately and encrypted; see the mask audit table in
// internal/store.
type MaskEvent struct {
	Rule       RuleID
	Start, End int
	Score      int
	Confidence Confidence
	Reasons    Reason

	// Original is the real credential and StandIn is the format-preserving
	// replacement that left the machine. Neither is nil for an event Mask
	// returned, and Attributes and AuditValues are the two supported ways to read
	// them.
	//
	// Original is a view into the buffer Mask was handed, which Mask overwrites in
	// place, so a caller that holds the slice across another pass over that buffer
	// reads back the stand-in instead of the credential. StandIn is the event's own
	// copy. AuditValues returns copies of both, which is why the audit write path
	// goes through it.
	Original []byte
	StandIn  []byte
}

// Attributes renders the event as JSON-ready values for a record-file event.
//
// It deliberately omits Original and StandIn, which is the whole point of the
// split between the record file and the audit table: the cassette says a
// credential was rewritten and which rule decided that, and the audit table is
// where an operator goes to see the value.
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

// AuditValues returns the pair a credential audit row stores, as strings so the
// caller does not have to keep the buffer Mask worked on alive.
func (e MaskEvent) AuditValues() (original, standIn string) {
	return string(e.Original), string(e.StandIn)
}

// RuleName is the stable name of the rule that made the decision, for a row or a
// system event that has to name the rule.
func (e MaskEvent) RuleName() string {
	return ruleTable[e.Rule].Name
}

// Vendor is the rule's vendor label, empty for the structural rules that have
// none. It lets a row say "OpenAI" without the reader holding the rule table.
func (e MaskEvent) Vendor() string {
	return ruleTable[e.Rule].Vendor
}

// Kind is the rule's credential kind, such as "api_key".
func (e MaskEvent) Kind() string {
	return ruleTable[e.Rule].Kind.String()
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
		if policySkipMask(f.ID) {
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
		// Own StandIn rather than pointing it into out. out is what the caller
		// hands the wire, so a caller that keeps an event and lets the send
		// overwrite its buffer would otherwise read back whatever replaced the
		// stand-in. Original may stay a view of src: nothing in this package
		// writes to src.
		fake := v.fakeFor(&ruleTable[f.ID], src[f.Start:f.End])
		copy(out[f.Start:f.End], fake)
		events = append(events, MaskEvent{
			Rule:       f.ID,
			Start:      f.Start,
			End:        f.End,
			Score:      f.Score,
			Confidence: f.Confidence,
			Reasons:    f.Reasons,
			Original:   src[f.Start:f.End],
			StandIn:    []byte(fake),
		})
	}
	return out, events
}

// policySkipMask reports whether a rule's match must never be rewritten, even
// though it is reported.
//
// Only structural.pem_private_key is skipped, and it is named rather than
// selected by Kind. That rule matches the "-----BEGIN ... PRIVATE KEY-----"
// line alone: the line carries no key material, the base64 body after it does,
// and no rule covers that body, so rewriting the header would corrupt the file
// without protecting anything - the real key would still leave the machine.
// Selecting by Kind used to sweep in age.private_key as well, which is a
// complete, self-contained, checksummed secret whose whole value is the match,
// so the BIP-173 validator was gating a rewrite that never happened.
func policySkipMask(id RuleID) bool { return id == RulePEMPrivateKey }

func (v *Vault) isFake(tok []byte) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	_, ok := v.byFake[string(tok)]
	return ok
}

// fakeFor returns the fake for real, reusing a recorded mapping when one exists
// and rendering and recording a new one otherwise.
func (v *Vault) fakeFor(r *Rule, real []byte) string {
	key := string(real)

	v.mu.RLock()
	f, ok := v.byReal[key]
	v.mu.RUnlock()
	if ok {
		return f
	}

	// The store lookup and the render both run outside v.mu, so creation is
	// serialized separately: two requests carrying the same new value must not
	// each render and persist a pair. The fast path above never takes this lock.
	v.createMu.Lock()
	defer v.createMu.Unlock()

	v.mu.RLock()
	f, ok = v.byReal[key]
	v.mu.RUnlock()
	if ok {
		return f
	}
	if f, ok := v.recordedFake(key); ok {
		return f
	}

	f = v.renderAndInsert(r, key, real)
	v.record(key, f)
	return f
}

// recordedFake reuses a mapping the durable store already holds. It is what
// keeps a fake stable when its pair has fallen out of the loaded window, or when
// this process restarted after recording it: the value is rendered again only
// when no pair exists.
func (v *Vault) recordedFake(real string) (string, bool) {
	if v.store == nil {
		return "", false
	}
	m, found, err := v.store.LookupMapping(real)
	if err != nil || !found {
		return "", false
	}
	if m.Real != real || m.Fake == "" || m.Real == m.Fake || len(m.Real) != len(m.Fake) {
		return "", false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if f, ok := v.byReal[real]; ok {
		return f, true
	}
	if owner, taken := v.byFake[m.Fake]; taken && owner != real {
		return "", false
	}
	v.insertLocked(real, m.Fake)
	return m.Fake, true
}

// renderAndInsert renders a fresh fake and installs it under v.mu. The render
// loop is a safety net, not a search: the first attempt differs from real with
// overwhelming probability, and a collision needs an existing fake to be the
// same bytes.
func (v *Vault) renderAndInsert(r *Rule, key string, real []byte) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if f, ok := v.byReal[key]; ok {
		return f
	}
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

// record hands a new pair to the durable store. It is deliberately not called
// for a pair read back from the store, and it never runs while v.mu is held: the
// store may fall back to a synchronous write when its queue is full.
func (v *Vault) record(real, fake string) {
	if v.store == nil || real == fake {
		return
	}
	if err := v.store.AppendMapping(Mapping{Real: real, Fake: fake}); err != nil {
		slog.Warn("Failed to persist secret mask mapping", "error", err)
	}
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
	if r.Kind == KindNationalID {
		if fake := renderChineseID(len(real), ks); fake != nil {
			return string(fake)
		}
	}
	if r.Kind == KindMobile {
		if fake := renderCNMobile(real, ks); fake != nil {
			return string(fake)
		}
	}
	// An armored private-key block keeps its header and footer verbatim and is
	// rewritten only between them. The two labels have to agree for the armor to
	// stay well formed, the body's line breaks have to keep their positions, and
	// a YAML block scalar indents every line: copying everything outside the
	// body and substituting inside it gives all three for free. The fallback
	// below still runs when the value is not a complete block, which is how the
	// shortest-match prefix test can render a synthetic header on its own.
	if r.ArmoredBlock {
		if from, to, ok := armoredBodySpan(real); ok {
			substituteRuns(out, real, ks, from, to)
			return string(out)
		}
	}
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

// armoredBodySpan returns the half-open body range of an RFC 7468 private-key
// block, or ok=false when real is not one. The body starts right after the
// header's closing dashes and ends where the footer's opening dashes begin, so
// both delimiter lines - and with them both labels - stay outside the range and
// are copied verbatim by the caller.
//
// The first " PRIVATE KEY-----" in a block is the header's: base64 contains no
// dash, so the only occurrences of that marker are the header and the footer,
// and LastIndex finds the footer.
func armoredBodySpan(real []byte) (int, int, bool) {
	i := bytes.Index(real, []byte(pemKeyMarker))
	if i < 0 {
		return 0, 0, false
	}
	start := i + len(pemKeyMarker)
	end := bytes.LastIndex(real, []byte(pemEndMarker))
	if end < start {
		return 0, 0, false
	}
	return start, end, true
}

// jsonEscapeLen is how many bytes a JSON escape at the start of b occupies: two
// for the simple escapes, six for \uXXXX. A backslash with nothing valid behind
// it counts as one so the loop always advances.
func jsonEscapeLen(b []byte) int {
	if len(b) < 2 {
		return 1
	}
	if b[1] != 'u' {
		return 2
	}
	if len(b) < 6 {
		return len(b)
	}
	return 6
}

// substituteRuns rewrites every alphanumeric run in real[from:to] in place in
// out. It is the armored-block half of the generic loop in render: no literal
// protection and no header, because the caller has already narrowed the range to
// the body and the block's constants live outside it. The keystream index stays
// absolute, so a byte's substitute still depends on its position in the value.
func substituteRuns(out, real, ks []byte, from, to int) {
	for i := from; i < to; {
		if real[i] == '\\' {
			// A block inside a JSON string carries its line breaks as the two
			// bytes '\' 'n'. Substituting the 'n' would turn a valid escape into
			// an arbitrary letter and corrupt the request, so the escape
			// sequence is copied whole; a real armor body never contains a
			// backslash, so this only ever fires on the escaped form.
			i += jsonEscapeLen(real[i:to])
			continue
		}
		if !isAlnumByte(real[i]) {
			i++
			continue
		}
		j := i
		for j < to && isAlnumByte(real[j]) {
			j++
		}
		hexMode := allLowerHex(real[i:j])
		for k := i; k < j; k++ {
			out[k] = substitute(real[k], ks[k], hexMode)
		}
		i = j
	}
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
			return clampHead(i+1, 0, tok)
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
	return clampHead(best+r.PrefixSkip, best, tok)
}

// clampHead keeps at least half of a match replaceable, so a stand-in never
// hands the provider more than half of the value it stands in for, but it never
// truncates a declared prefix. Those bytes are format constants - the pypi
// macaroon header, the ConvertTo-SecureString magic - which a downstream shape
// check matches on and which disclose nothing, since they are the same in every
// real value. literal is the length of the declared prefix that matched, or 0
// for a head that was inferred rather than declared.
func clampHead(head, literal int, tok []byte) int {
	limit := len(tok) / 2
	if literal > limit {
		limit = literal
	}
	if head > limit {
		return limit
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
	for _, variant := range wireVariants(real, fake) {
		v.addWireLocked(variant.real, variant.fake)
	}
	v.order = append(v.order, real)
}

// addWireLocked indexes one byte form of a stand-in. A value with no bytes JSON
// escapes has exactly one form, so the ordinary rules add one entry; an armored
// block adds up to three, one for each nesting depth it can appear at.
func (v *Vault) addWireLocked(real, fake string) {
	if _, ok := v.byFake[fake]; ok {
		return
	}
	v.byFake[fake] = real
	b := fake[0]
	v.index[b] = insertLongestFirst(v.index[b], fakeEntry{text: fake, raw: []byte(fake), entry: chainEntry(len(fake))})
	v.firstFit[b] = true
	if len(fake) > v.fakeLen {
		v.fakeLen = len(fake)
	}
}

// removeWireLocked drops one byte form of a stand-in from the restore index.
func (v *Vault) removeWireLocked(fake string) {
	if _, ok := v.byFake[fake]; !ok {
		return
	}
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
	for _, variant := range wireVariants(oldest, fake) {
		v.removeWireLocked(variant.fake)
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

// wireVariant is one byte form of a stand-in and the real bytes it restores to.
type wireVariant struct {
	real string
	fake string
}

// wireVariants returns the byte forms a stand-in can take on the wire, paired
// with the matching form of the real value.
//
// A credential with no byte JSON escapes has one form and this returns one
// entry, so every rule but the armored block is unaffected. A private-key block
// is different: it is full of newlines, and JSON carries a newline as the two
// bytes '\' 'n'. The form the request used depends on how many JSON strings
// enclosed the key, and the echo comes back at the depth of the field that
// carries it - plain text at one level, a tool call's arguments at two - so the
// depth immediately above and below the stored form are indexed as well. Each
// variant maps to the real bytes at the same depth, which is what keeps the
// client's JSON valid after a restore.
func wireVariants(real, fake string) []wireVariant {
	out := []wireVariant{{real: real, fake: fake}}
	if up := jsonEscapeValue(fake); up != fake {
		out = append(out, wireVariant{real: jsonEscapeValue(real), fake: up})
	}
	if down := jsonUnescapeValue(fake); down != fake {
		out = append(out, wireVariant{real: jsonUnescapeValue(real), fake: down})
	}
	return out
}

// jsonEscapeValue escapes a value the way encoding/json writes a string.
func jsonEscapeValue(s string) string {
	if !strings.ContainsAny(s, "\\\"\n\r\t") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// jsonUnescapeValue reverses jsonEscapeValue for the escapes it can emit. An
// unknown escape is copied through unchanged rather than dropped, so a real
// block whose body happens to contain a backslash is never mangled.
func jsonUnescapeValue(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
			i++
		case 'r':
			b.WriteByte('\r')
			i++
		case 't':
			b.WriteByte('\t')
			i++
		case '"':
			b.WriteByte('"')
			i++
		case '\\':
			b.WriteByte('\\')
			i++
		case '/':
			b.WriteByte('/')
			i++
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// chainEntry is the prefix a run must cover before a chain may open for a fake
// of length n. A long value is almost certainly an armored block, whose literal
// prefix is shared with every other PEM block; see chainLongFake.
func chainEntry(n int) int {
	if n > chainLongFake {
		return chainMinEntryLong
	}
	return chainMinEntry
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

// renderChineseID builds a stand-in that is itself a well-formed identity
// number: a real province code, a calendar birthdate and the ISO 7064 check
// character recomputed over the substituted digits. Keeping the shape valid is
// the same reason every other rule keeps its prefix - a downstream validator
// has to accept the value the provider was given. A shape-only stand-in would
// be rejected by every tool that checks an identity number.
//
// The birthdate is deliberately not preserved: it is the part of the value that
// identifies a person most directly, and nothing downstream needs that
// particular date to be true. The stand-in only has to be a number the
// validator admits.
//
// The keystream is the same one the byte-wise renderer uses, so the stand-in is
// still a pure function of (seed, rule, real value) and a retry with a
// different attempt produces a different number.
func renderChineseID(n int, ks []byte) []byte {
	if n != 18 || len(ks) < 12 {
		return nil
	}
	out := make([]byte, 18)
	province := chineseIDProvinceList[int(ks[0])%len(chineseIDProvinceList)]
	out[0], out[1] = province[0], province[1]
	for i := 2; i <= 5; i++ {
		out[i] = '0' + ks[i]%10
	}
	year := 1950 + int(ks[6])%60
	month := 1 + int(ks[7])%12
	day := 1 + int(ks[8])%28
	putFixedDigits(out[6:10], year)
	putFixedDigits(out[10:12], month)
	putFixedDigits(out[12:14], day)
	for i := 14; i <= 16; i++ {
		out[i] = '0' + ks[9+i-14]%10
	}
	out[17] = chineseIDCheckChar(out[:17])
	return out
}

// putFixedDigits writes v into dst as zero-padded decimal digits.
func putFixedDigits(dst []byte, v int) {
	for i := len(dst) - 1; i >= 0; i-- {
		dst[i] = byte('0' + v%10)
		v /= 10
	}
}

// renderCNMobile builds a stand-in that is itself a well-formed mainland mobile
// number: the real 1[3-9]x head when it is an allocated one (otherwise another
// allocated head) and eight subscriber digits from the keystream. Keeping the
// head is the point - carrier allocation is public, so a tool that checks the
// head still accepts the value the provider was given, and there is no check
// digit to recompute.
//
// The subscriber digits are not preserved: they are the part that identifies a
// person. The result is still a pure function of (seed, rule, real value), so
// the same number renders the same stand-in across retries and sessions.
func renderCNMobile(real, ks []byte) []byte {
	if len(real) != 11 || len(ks) < 11 {
		return nil
	}
	out := make([]byte, 11)
	putFixedDigits(out[:3], cnMobileHead(real, ks))
	for i := 3; i < 11; i++ {
		out[i] = '0' + ks[i]%10
	}
	// A keystream can reproduce the real value; hand back a different number
	// rather than the one this stand-in exists to hide.
	if string(out) == string(real) {
		out[10] = '0' + (out[10]-'0'+1)%10
	}
	return out
}
