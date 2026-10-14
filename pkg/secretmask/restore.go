package secretmask

import "io"

// restoreReadChunk is how much the Restorer pulls from its source at a time.
const restoreReadChunk = 32 << 10

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
	v.restoreLocked(out, 0)
	return out
}

// RestoreInPlace rewrites every fake in buf back to its real value, starting
// the search at from, and reports how many values it replaced. Bytes before
// from are left alone, which lets a caller that appends to buf restore only
// what it just added.
func (v *Vault) RestoreInPlace(buf []byte, from int) int {
	if len(buf) == 0 {
		return 0
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.byFake) == 0 {
		return 0
	}
	return v.restoreLocked(buf, from)
}

// restoreLocked performs the replacement in place. Because a fake is exactly as
// long as the value it stands for, this never moves any bytes after it, which
// is what makes in-place restoration safe on a stream.
func (v *Vault) restoreLocked(buf []byte, from int) int {
	if from < 0 {
		from = 0
	}
	replaced := 0
	for i := from; i < len(buf); {
		if !v.firstFit[buf[i]] {
			i++
			continue
		}
		matched := false
		for _, e := range v.index[buf[i]] {
			n := len(e.raw)
			if i+n > len(buf) {
				continue
			}
			if !bytesEqual(buf[i:i+n], e.raw) {
				continue
			}
			copy(buf[i:i+n], v.byFake[e.text])
			i += n
			matched = true
			replaced++
			break
		}
		if !matched {
			i++
		}
	}
	return replaced
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Restorer rewrites fakes back to real values while a body is read.
//
// The body is streamed, so a fake can straddle two reads: an SSE event boundary
// and an io.Copy buffer are both arbitrary. The Restorer therefore holds back
// the last Holdback bytes of every read and only releases them once the next
// read has arrived, which guarantees that any fake whose first byte it emitted
// was complete. Holdback is zero for a vault that never masked anything, so an
// untouched stream is not delayed at all.
//
// A Restorer is not safe for concurrent use. It is not an io.Closer; wrap a
// body with NewRestorer and close the body underneath it.
type Restorer struct {
	src      io.Reader
	v        *Vault
	buf      []byte
	off      int
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
			r.off = 0
		}

		emit := len(r.buf) - r.off
		if hold := r.v.Holdback(); !r.eof && hold > 0 {
			if emit > hold {
				emit -= hold
			} else {
				emit = 0
			}
		}
		if emit > 0 {
			n := copy(p, r.buf[r.off:r.off+emit])
			r.off += n
			return n, nil
		}

		if r.eof {
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
			start := len(r.buf)
			r.buf = append(r.buf, r.chunk[:n]...)
			// A fake may also start inside the bytes withheld from the previous
			// read, so the scan restarts that far back.
			from := start - r.v.Holdback()
			if from < r.off {
				from = r.off
			}
			r.restored += r.v.RestoreInPlace(r.buf, from)
		}
		if err != nil {
			r.eof = true
			if err != io.EOF {
				r.err = err
			}
		}
	}
}
