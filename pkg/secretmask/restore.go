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
				prefix    *fakeEntry
				prefixLen int
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
						if m > prefixLen {
							prefix, prefixLen = cand, m
						}
						continue
					}
					if undecided == nil {
						undecided = cand
					}
					continue
				}
				if m := commonPrefix(buf[i:i+n], cand.raw); m < n {
					if m > prefixLen {
						prefix, prefixLen = cand, m
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
			case prefix != nil && prefixLen >= chainMinEntry:
				// Hold the run back instead of rewriting it now: only a complete
				// stand-in is rewritten, so a run that merely resembles one
				// cannot corrupt the text it sits in.
				st.frag = &fragment{
					fake:  prefix.raw,
					real:  []byte(v.byFake[prefix.text]),
					start: i,
				}
				st.frag.add(i, 0, prefixLen)
				i += prefixLen
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
// A Restorer is not safe for concurrent use. It is not an io.Closer; wrap a
// body with NewRestorer and close the body underneath it.
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
			settled, replaced := r.v.restoreScan(r.buf, &r.st)
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
