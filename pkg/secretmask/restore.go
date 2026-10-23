package secretmask

import "io"

// restoreReadChunk is how much the Restorer pulls from its source at a time.
const restoreReadChunk = 32 << 10

// A streamed provider response carries one message across many JSON deltas, so a
// stand-in the model echoes arrives cut into pieces with the envelope between
// them: "sk-proj-AAA" ends one delta and the rest begins the next. A matcher
// that only accepts contiguous bytes therefore restores nothing on a real
// stream, which is why a run that stops short of a whole stand-in opens a chain
// instead of being given up on.
//
// The chain is what the thresholds exist for. chainMinEntry is how much of a
// stand-in one run must cover before a chain may open, chainMinRun how much the
// next run must cover before it is accepted as the continuation, and chainMaxGap
// how many bytes may sit between two runs.
//
// chainMinEntry is deliberately low, and it is not the thing that makes a chain
// trustworthy: completing one requires every remaining byte of the stand-in,
// which is hundreds of bits of keystream, so a chain that merely resembles a
// stand-in cannot complete. What the entry threshold decides is only whether to
// hold output back while waiting, which is why it must stay at or below the
// shortest run a provider is likely to cut - a value dribbled out in pieces
// smaller than this is never recognised as one stand-in. chainMinRun is the
// point where a continuation stops being plausible by accident, and chainMaxGap
// bounds how long a chain that never resolves can hold output back, so a
// stand-in's literal prefix appearing in ordinary prose stalls a stream for a
// bounded distance instead of for the rest of the body.
const (
	chainMinEntry = 8
	chainMinRun   = 8
	chainMaxGap   = 512

	// chainLongFake and chainMinEntryLong are the other half of that policy: a
	// long fake needs a longer first run before a chain may open. The armored
	// private-key block shares its "-----BEGIN " prefix with every other PEM
	// block, so the eight-byte threshold let any certificate header in a
	// response open a chain that could never complete: the stream stalled for
	// the whole chainMaxGap and a PEM-dense body fell into the byte-by-byte
	// continuation walk. Sixteen bytes is past that shared prefix - a different
	// label diverges at its first letter, byte eleven - and still well inside
	// the block's own header, so a provider that cuts the real stand-in into
	// deltas opens the chain on its first run.
	chainLongFake     = 256
	chainMinEntryLong = 16
)

// run is one contiguous piece of a stand-in sitting in the buffer.
type run struct {
	off int // where the piece is
	log int // where it belongs in the stand-in
	len int
}

// fragment is a stand-in whose bytes the stream delivered in more than one run.
// Nothing is rewritten until the whole stand-in is accounted for, so a value is
// restored either completely or not at all: a run that merely resembles the
// start of a stand-in is held back while the chain is open and released
// unchanged if the rest never arrives.
type fragment struct {
	fake    []byte
	real    []byte
	start   int // buffer offset of the first byte of the first run
	matched int // how much of fake the runs account for
	gap     int // bytes passed over since the last run
	runs    []run
}

func (f *fragment) add(off, log, length int) {
	f.runs = append(f.runs, run{off: off, log: log, len: length})
	f.matched += length
}

func (f *fragment) rewrite(buf []byte) {
	for _, r := range f.runs {
		copy(buf[r.off:r.off+r.len], f.real[r.log:r.log+r.len])
	}
}

// streamState is what one stream's scan carries between reads. A scan resumes at
// cursor and never revisits a byte it has already judged, which is what keeps a
// replacement from being applied twice.
type streamState struct {
	frag   *fragment
	cursor int
}

// Restore rewrites every fake in src back to its real value and returns a new
// slice. It is the whole-body counterpart of Restorer, for callers that hold
// the body in memory.
func (v *Vault) Restore(src []byte) []byte {
	if len(src) == 0 {
		return src
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.byFake) == 0 {
		return src
	}
	out := make([]byte, len(src))
	copy(out, src)
	v.restoreScan(out, &streamState{})
	return out
}

// RestoreInPlace rewrites every fake in buf back to its real value, starting
// the search at from, and reports how many values it replaced. Bytes before
// from are left alone, which lets a caller that appends to buf restore only
// what it just added.
//
// A caller that streams should use a Restorer instead: this call drops the scan
// state, so a stand-in split across two calls is only restored when the second
// call happens to carry enough of it to match a whole value.
func (v *Vault) RestoreInPlace(buf []byte, from int) int {
	if len(buf) == 0 {
		return 0
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.byFake) == 0 {
		return 0
	}
	_, replaced := v.restoreScan(buf, &streamState{cursor: from})
	return replaced
}

// restoreScan walks buf from the state's cursor, rewriting complete fakes and
// opening a chain when a stand-in looks cut short. It returns the offset up to
// which buf is settled along with the number of values it replaced.
//
// Everything before the settled offset has either been rewritten or proven not
// to begin a fake, so a caller may emit it; the tail from there on needs more
// input before it can be judged. Withholding starts at the first run of an open
// chain, so the bytes between that run and the cursor are held too and are
// released untouched once the chain is abandoned.
//
// The caller must hold v.mu for reading. This function and its helpers read the
// vault's maps directly and take no lock of their own, because the whole-body
// entry points already hold one and an RWMutex is not reentrant; the streaming
// Restorer takes the read lock around each call instead. One process-wide Vault
// serves every request, so a response being restored while another request masks
// is the normal case, and an unguarded map read there is a fatal error rather
// than a stale value.
func (v *Vault) restoreScan(buf []byte, st *streamState) (int, int) {
	replaced := 0
	i := st.cursor
	if i < 0 {
		i = 0
	}
	if i > len(buf) {
		i = len(buf)
	}
	for i < len(buf) {
		if st.frag != nil {
			// A whole stand-in outranks an open chain, and this check has to come
			// first: the chain has no claim on bytes that already spell one.
			// Stepping over them as envelope is how a stand-in that the provider
			// repeated inside the gap used to reach the client unrestored, and
			// accepting them as the chain's own continuation - which is what
			// happens when a truncated copy is followed by a complete one, since
			// the run matches at the offset the chain expects next - is how two
			// occurrences used to be spliced into one chimera, half real value
			// and half stand-in. The chain is dropped rather than kept pending,
			// which releases its bytes exactly as they are, so the all-or-nothing
			// rule still holds: nothing of it was rewritten before it completed.
			if full, n := v.completeAt(buf, i); full != nil {
				copy(buf[i:i+n], v.byFake[full.text])
				st.frag = nil
				i += n
				replaced++
				continue
			}
			// The byte could still turn out to begin a stand-in, but the stream
			// has not delivered enough of it to tell. Consuming it as envelope
			// now would answer a question the stream has not answered, and the
			// scan never revisits a byte, so the stand-in would be lost. Wait for
			// more input instead; the chain stays open and its bytes stay held.
			if v.undecidedAt(buf, i) {
				st.cursor = i
				return st.frag.start, replaced
			}
			next, outcome := v.feedFragment(buf, i, st)
			switch outcome {
			case fragmentWaiting:
				st.cursor = i
				return st.frag.start, replaced
			case fragmentDone:
				// A stand-in reassembled from several runs is one replacement,
				// and the audit event has to count it as one.
				replaced++
				i = next
				continue
			case fragmentOpen:
				i = next
				continue
			}
			// The chain was abandoned: fall through and treat this byte as a
			// fresh start, which is the strongest reading of it available.
		}

		if v.firstFit[buf[i]] {
			// The candidate loop is inlined rather than kept in a helper: every
			// byte of every response passes through here, and a call per
			// first-fit byte costs more than the comparisons it would wrap.
			//
			// A candidate that matched every available byte without reaching its
			// own end is undecided rather than absent, because the rest of it may
			// be in the next read. A candidate that stopped short is a possible
			// first run of a stand-in the stream cut in two.
			entries := v.index[buf[i]]
			avail := len(buf) - i
			var (
				full      *fakeEntry
				fullLen   int
				undecided *fakeEntry
				prefixes  []chainCandidate
			)
			// Each bucket is ordered longest first, so the first complete match
			// is also the longest one.
			for idx := range entries {
				cand := &entries[idx]
				n := len(cand.raw)
				if n > avail {
					// Only the last bytes of the buffer can be undecided, so
					// this branch is rare: compare what is there and either
					// wait for the rest or record the run that did match.
					m := commonPrefix(buf[i:], cand.raw[:avail])
					if m < avail {
						if chainStart(m, cand.entry, buf[i+m]) {
							prefixes = append(prefixes, chainCandidate{entry: cand, first: m})
						}
						continue
					}
					if undecided == nil {
						undecided = cand
					}
					continue
				}
				if m := commonPrefix(buf[i:i+n], cand.raw); m < n {
					if chainStart(m, cand.entry, buf[i+m]) {
						prefixes = append(prefixes, chainCandidate{entry: cand, first: m})
					}
					continue
				}
				if full == nil {
					full, fullLen = cand, n
				}
			}
			switch {
			case full != nil:
				copy(buf[i:i+fullLen], v.byFake[full.text])
				i += fullLen
				replaced++
				continue
			case undecided != nil:
				st.cursor = i
				return i, replaced
			case len(prefixes) > 0:
				// Hold the run back instead of rewriting it now: only a complete
				// stand-in is rewritten, so a run that merely resembles one
				// cannot corrupt the text it sits in.
				chosen, decision := chooseChain(buf, i, prefixes)
				if decision == chainWait {
					// The variants share every byte the stream has delivered so
					// far and the next run will decide between them; waiting is
					// what turns that into a decision rather than a guess.
					st.cursor = i
					return i, replaced
				}
				if decision == chainNone {
					break
				}
				st.frag = &fragment{
					fake:  chosen.entry.raw,
					real:  []byte(v.byFake[chosen.entry.text]),
					start: i,
				}
				st.frag.add(i, 0, chosen.first)
				i += chosen.first
				continue
			}
		}
		i++
	}
	st.cursor = i
	if st.frag != nil {
		return st.frag.start, replaced
	}
	return len(buf), replaced
}

// chainStart reports whether a run of m bytes may open a chain. Usually it has
// to reach the candidate's entry threshold, but a delta can end the run before
// that: the closing quote of the JSON string is where the envelope begins, and
// the variant lookahead can then confirm the block across that envelope. A
// short run that ends anywhere else is prose, not a delta boundary, so it is
// not allowed to stall the stream - that is what the certificate case taught.
func chainStart(m, entry int, next byte) bool {
	if m <= 0 {
		return false
	}
	if m >= entry {
		return true
	}
	// Only a long stand-in gets the delta-boundary exception: a short secret
	// cut into tiny runs must stay untouched, which is what its entry threshold
	// is for.
	return entry >= chainMinEntryLong && next == '"'
}

// chainCandidate is one escape-depth variant a run could begin, along with how
// many bytes of it the first run covers.
type chainCandidate struct {
	entry *fakeEntry
	first int
}

// chooseChain picks the variant whose continuation the buffered bytes actually
// carry. A stand-in made of newlines exists at several JSON escape depths, and
// the run that opens a chain ends at the first envelope between deltas, before
// any of them differs: the depth that matches the bytes after that envelope is
// the one the stream is carrying, so the lookahead is what makes the choice
// instead of bucket order. When the buffer cannot separate the variants yet it
// asks for more input rather than guessing, and it declines the run only when
// every variant exhausted the gap, which means the run was never a stand-in.
func chooseChain(buf []byte, at int, cands []chainCandidate) (chainCandidate, chainDecision) {
	bestReach := -1
	var best chainCandidate
	survivors := 0
	for _, cand := range cands {
		reach, status := chainReach(cand.entry.raw, buf, at, cand.first)
		if status == chainReachAbandoned {
			continue
		}
		survivors++
		if status == chainReachComplete {
			return cand, chainPick
		}
		if reach > bestReach {
			bestReach = reach
			best = cand
		}
	}
	switch survivors {
	case 0:
		return chainCandidate{}, chainNone
	case 1:
		return best, chainPick
	default:
		return chainCandidate{}, chainWait
	}
}

// chainDecision is what chooseChain concluded about a run that could open a
// chain.
type chainDecision int

const (
	chainPick chainDecision = iota // one variant is the only one that can match
	chainWait                      // the buffer cannot separate the variants yet
	chainNone                      // every variant exhausted the gap; not a chain
)

// chainReach walks the same continuation rules as feedFragment without touching
// the buffer and reports how much of fake the bytes at buf[at:] can account for.
// It stops when the stand-in is complete, when the gap limit is exceeded, or
// when the buffer ends inside something it cannot judge yet.
func chainReach(fake, buf []byte, at, matched int) (int, chainReachStatus) {
	pos := at + matched
	gap := 0
	for matched < len(fake) {
		need := chainMinRun
		if len(fake)-matched < need {
			need = len(fake) - matched
		}
		if len(buf)-pos < need {
			return matched, chainReachWaiting
		}
		if !bytesEqual(buf[pos:pos+need], fake[matched:matched+need]) {
			if gap == 0 {
				if n := shortDeltaTail(fake, buf, pos, matched, need); n > 0 {
					matched += n
					pos += n
					gap = 0
					continue
				}
			}
			gap++
			pos++
			if gap > chainMaxGap {
				return matched, chainReachAbandoned
			}
			continue
		}
		limit := len(fake) - matched
		if avail := len(buf) - pos; limit > avail {
			limit = avail
		}
		run := need
		for run < limit && buf[pos+run] == fake[matched+run] {
			run++
		}
		matched += run
		pos += run
		gap = 0
	}
	return matched, chainReachComplete
}

// shortDeltaTail returns the length of a run that stops short of chainMinRun
// only because the JSON string ends there. A delta can cut a run anywhere, and
// the bytes between one delta's content and the next are the envelope, so the
// closing quote is what tells a real delta boundary from prose that happens to
// share a few bytes. Refusing such a tail would drop the chain out of alignment
// for every later run; taking it is safe because completion still requires
// every remaining byte.
//
// The caller must only try it at gap zero, the direct continuation of the run
// that just ended. A quote appears all over an SSE envelope, and accepting one
// after a gap lets a coincidental few bytes inside the envelope advance the
// logical offset, which leaves the matching byte in front of the run
// unrewritten.
func shortDeltaTail(fake, buf []byte, at, matched, need int) int {
	n := commonPrefix(buf[at:], fake[matched:])
	if n > 0 && n < need && buf[at+n] == '"' {
		return n
	}
	return 0
}

// chainReachStatus is how far chainReach got with one variant.
type chainReachStatus int

const (
	chainReachWaiting   chainReachStatus = iota // the buffer ended inside a possible match
	chainReachComplete                          // the whole stand-in is present
	chainReachAbandoned                         // the gap limit was exceeded
)

// completeAt returns the longest whole stand-in that begins at buf[off], or nil,
// along with its length. Buckets are ordered longest first, so the first match
// is also the longest.
//
// This is the cold-path counterpart of the candidate loop in restoreScan, which
// is inlined there because it runs once per byte of every response; this one is
// reached only while a chain is open, and only for a byte that could begin a
// stand-in at all. The two must agree on what counts as a match, so a change to
// that rule belongs in both.
func (v *Vault) completeAt(buf []byte, off int) (*fakeEntry, int) {
	if off >= len(buf) || !v.firstFit[buf[off]] {
		return nil, 0
	}
	entries := v.index[buf[off]]
	avail := len(buf) - off
	for idx := range entries {
		cand := &entries[idx]
		n := len(cand.raw)
		if n > avail {
			continue
		}
		if bytesEqual(buf[off:off+n], cand.raw) {
			return cand, n
		}
	}
	return nil, 0
}

// undecidedAt reports whether buf[off:] could still turn out to be the head of a
// stand-in, because the buffer ends inside a candidate rather than before one.
//
// It is consulted only while a chain is open, where a byte that is not this
// chain's continuation has to be classified before it can be stepped over. A
// candidate that fits entirely in the buffer and did not match is decided - that
// is what completeAt just established - so only a candidate longer than what is
// left can still be undecided.
func (v *Vault) undecidedAt(buf []byte, off int) bool {
	if off >= len(buf) || !v.firstFit[buf[off]] {
		return false
	}
	avail := len(buf) - off
	if avail >= v.fakeLen {
		// Every stand-in in the vault fits inside what is left, and completeAt
		// has already ruled all of them out for this position, so no candidate
		// can still be waiting on more input. This is the whole-body case, where
		// the buffer holds the entire response.
		return false
	}
	for idx := range v.index[buf[off]] {
		cand := &v.index[buf[off]][idx]
		if len(cand.raw) <= avail {
			continue
		}
		if commonPrefix(buf[off:], cand.raw) == avail {
			return true
		}
	}
	return false
}

// fragmentOutcome is what feeding one position to an open chain produced.
type fragmentOutcome int

const (
	fragmentWaiting   fragmentOutcome = iota // not enough input to judge; withhold everything
	fragmentOpen                             // the run was accepted and the chain continues
	fragmentDone                             // the stand-in is complete and has been rewritten
	fragmentAbandoned                        // the rest never came; release the bytes untouched
)

// feedFragment advances an open chain at buf[i]. A byte that is not the
// continuation is passed over as envelope rather than ending the chain
// outright, which is the whole point: the JSON between two deltas is what
// separates the runs of one stand-in.
func (v *Vault) feedFragment(buf []byte, i int, st *streamState) (int, fragmentOutcome) {
	f := st.frag
	remaining := len(f.fake) - f.matched
	need := chainMinRun
	if remaining < need {
		need = remaining
	}
	if len(buf)-i < need {
		return i, fragmentWaiting
	}
	if !bytesEqual(buf[i:i+need], f.fake[f.matched:f.matched+need]) {
		if f.gap == 0 {
			if n := shortDeltaTail(f.fake, buf, i, f.matched, need); n > 0 {
				f.add(i, f.matched, n)
				f.gap = 0
				if f.matched == len(f.fake) {
					f.rewrite(buf)
					st.frag = nil
					return i + n, fragmentDone
				}
				return i + n, fragmentOpen
			}
		}
		f.gap++
		if f.gap > chainMaxGap {
			st.frag = nil
			return i, fragmentAbandoned
		}
		return i + 1, fragmentOpen
	}
	// Past the trust threshold, take the longest run that keeps matching.
	limit := remaining
	if avail := len(buf) - i; limit > avail {
		limit = avail
	}
	run := need
	for run < limit && buf[i+run] == f.fake[f.matched+run] {
		run++
	}
	f.add(i, f.matched, run)
	if f.matched == len(f.fake) {
		f.rewrite(buf)
		st.frag = nil
		return i + run, fragmentDone
	}
	f.gap = 0
	return i + run, fragmentOpen
}

// commonPrefix is the length of the leading run the two slices share.
func commonPrefix(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func bytesEqual(a, b []byte) bool {
	return len(a) == len(b) && commonPrefix(a, b) == len(a)
}

// Restorer rewrites fakes back to real values while a body is read.
//
// It emits only the bytes its scan has settled, and withholds whatever tail it
// cannot judge yet: a stand-in split across two reads, and the runs of a
// stand-in that a streamed response carried across separate JSON deltas. A vault
// that never masked anything settles every byte as it arrives, so an untouched
// stream is not delayed at all.
//
// A Restorer is not safe for concurrent use, but the Vault it reads from is: the
// restorer takes the vault's read lock around each scan, so several responses
// can be restored while other requests render and record new stand-ins. It is
// not an io.Closer; wrap a body with NewRestorer and close the body underneath
// it.
type Restorer struct {
	src      io.Reader
	v        *Vault
	buf      []byte
	off      int
	settled  int
	st       streamState
	eof      bool
	err      error
	chunk    []byte
	restored int
}

// NewRestorer wraps src so that reads return restored bytes.
func NewRestorer(src io.Reader, v *Vault) *Restorer {
	return &Restorer{src: src, v: v}
}

// Restored reports how many values have been rewritten so far. A caller uses it
// to record that a response carried stand-ins, which is otherwise invisible:
// the restored body is byte-identical to what a session without masking would
// have produced.
func (r *Restorer) Restored() int { return r.restored }

// Close forwards to the source when it is an io.Closer, so wrapping a response
// body does not leak the connection.
func (r *Restorer) Close() error {
	if c, ok := r.src.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Read returns restored bytes. It returns io.EOF only after everything withheld
// has been released.
func (r *Restorer) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if r.off > 0 && r.off == len(r.buf) {
			r.buf = r.buf[:0]
			r.off, r.settled, r.st = 0, 0, streamState{}
		}

		if r.settled > r.off {
			n := copy(p, r.buf[r.off:r.settled])
			r.off += n
			return n, nil
		}

		if r.eof {
			// Nothing more will arrive, so a chain that is still open can never
			// resolve and the bytes it held are released exactly as they are.
			if r.settled < len(r.buf) {
				r.settled = len(r.buf)
				continue
			}
			if r.err != nil {
				err := r.err
				r.err = nil
				return 0, err
			}
			return 0, io.EOF
		}

		if r.chunk == nil {
			r.chunk = make([]byte, restoreReadChunk)
		}
		n, err := r.src.Read(r.chunk)
		if n > 0 {
			r.buf = append(r.buf, r.chunk[:n]...)
			// The lock is taken per scan rather than held across the read: the
			// scan touches the vault's maps, the read above does not, and holding
			// it over the network would stall every request that wants to mask.
			r.v.mu.RLock()
			settled, replaced := r.v.restoreScan(r.buf, &r.st)
			r.v.mu.RUnlock()
			r.settled = settled
			r.restored += replaced
		}
		if err != nil {
			r.eof = true
			if err != io.EOF {
				r.err = err
			}
		}
	}
}
