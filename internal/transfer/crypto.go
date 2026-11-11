// Package transfer implements the trajecta export/import bundle used to move a
// deployment's data between two trajecta installations, or between a live
// deployment and an archive. It exists so that two environments of the same
// deployment can be merged without giving up replay history, analysis results
// or the audit trail.
//
// Design summary:
//
//   - Export walks one time window of logs (or --all), writes every table
//     that cannot be rebuilt from cassettes into a single streaming tar
//     (optionally gzip-compressed), then appends the referenced .http
//     cassettes with their modification times preserved.
//   - Secrets are decrypted at export with the source deployment's local
//     secret box; the bundle is expected to travel password-protected (a
//     random password is generated and shown once when --password is absent).
//   - Import is idempotent and rerunnable: rows and files that match the
//     record on disk are skipped, changed rows/files are updated, nothing is
//     ever deleted, and the whole merge happens inside one database
//     transaction followed by a session summaries rebuild.
//   - Channel identity is (base_url, plaintext api key): a source channel
//     whose url and key both match a target channel is merged into it, a
//     different key creates a new channel.

package transfer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/scrypt"
)

// BundleFormat is the manifest's format identifier. Bump on any
// incompatible layout change; import refuses other values.
const BundleFormat = "trajecta-export/v1"

const (
	envelopeMagic  = "TRAJECTA-EXPORT-1\n"
	headerPrefix   = "trajecta-export-v1:"
	defaultScryptN = 1 << 15
	defaultScryptR = 8
	defaultScryptP = 1
	scryptSaltSize = 32
	noncePrefixLen = 8
	chunkSize      = 64 * 1024
	sentinelChunk  = 0 // length marker signalling end of stream
)

type envelopeHeader struct {
	KDF       string `json:"kdf"`
	N         int    `json:"n"`
	R         int    `json:"r"`
	P         int    `json:"p"`
	Salt      string `json:"salt"`
	NonceSeed string `json:"nonce_seed"`
}

// encryptedWriter encrypts plaintext chunks with AES-256-GCM. The file layout
// is: magic line, header line (JSON, utf-8), then length-prefixed chunks where
// each chunk's nonce is the 8-byte seed from the header concatenated with a
// 4-byte big-endian counter. A final chunk with length 0 and a GCM tag
// authenticates the end of the stream so truncation is detected.
type encryptedWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	seed    []byte
	buf     []byte
	counter uint32
	closed  bool
}

func newEncryptedWriter(w io.Writer, password string) (*encryptedWriter, error) {
	if password == "" {
		return nil, errors.New("export password must not be empty")
	}
	salt := make([]byte, scryptSaltSize)
	seed := make([]byte, noncePrefixLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate export salt: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, seed); err != nil {
		return nil, fmt.Errorf("generate export nonce seed: %w", err)
	}
	key, err := scrypt.Key([]byte(password), salt, defaultScryptN, defaultScryptR, defaultScryptP, 32)
	if err != nil {
		return nil, fmt.Errorf("derive export key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("build export cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build export AEAD: %w", err)
	}
	header, err := json.Marshal(envelopeHeader{
		KDF: "scrypt", N: defaultScryptN, R: defaultScryptR, P: defaultScryptP,
		Salt: b64encode(salt), NonceSeed: b64encode(seed),
	})
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w, envelopeMagic); err != nil {
		return nil, err
	}
	n, err := fmt.Fprintf(w, "%s%s\n", headerPrefix, header)
	if err != nil || n == 0 {
		return nil, fmt.Errorf("write export envelope header: %w", err)
	}
	return &encryptedWriter{w: w, aead: aead, seed: seed, buf: make([]byte, 0, chunkSize)}, nil
}

func (e *encryptedWriter) Write(p []byte) (int, error) {
	if e.closed {
		return 0, errors.New("write on closed encrypted export writer")
	}
	total := len(p)
	for len(p) > 0 {
		room := chunkSize - len(e.buf)
		n := len(p)
		if n > room {
			n = room
		}
		e.buf = append(e.buf, p[:n]...)
		p = p[n:]
		if len(e.buf) == chunkSize {
			if err := e.flushChunk(); err != nil {
				return 0, err
			}
			e.buf = e.buf[:0]
		}
	}
	return total, nil
}

func (e *encryptedWriter) flushChunk() error {
	return e.writeChunk(e.buf)
}

func (e *encryptedWriter) writeChunk(plaintext []byte) error {
	if len(plaintext) == 0 {
		plaintext = nil
	}
	nonce := make([]byte, 0, 12)
	nonce = append(nonce, e.seed...)
	var counter [4]byte
	binary.BigEndian.PutUint32(counter[:], e.counter)
	nonce = append(nonce, counter[:]...)
	e.counter++
	ciphertext := e.aead.Seal(nil, nonce, plaintext, nil)
	length := make([]byte, 4)
	binary.LittleEndian.PutUint32(length, uint32(len(plaintext)))
	if _, err := e.w.Write(length); err != nil {
		return err
	}
	_, err := e.w.Write(ciphertext)
	return err
}

func (e *encryptedWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	// Any remaining partial chunk, then the sentinel. The sentinel proves to
	// the reader that the stream was not truncated by an attacker or a failed
	// disk.
	if len(e.buf) > 0 {
		if err := e.writeChunk(e.buf); err != nil {
			return err
		}
		e.buf = e.buf[:0]
	}
	return e.writeChunk(nil)
}

type encryptedReader struct {
	r       io.Reader
	aead    cipher.AEAD
	seed    []byte
	plain   []byte
	counter uint32
	done    bool
}

func newEncryptedReader(r io.Reader, password string) (*encryptedReader, error) {
	magic := make([]byte, len(envelopeMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, fmt.Errorf("read export magic: %w", err)
	}
	if string(magic) != envelopeMagic {
		return nil, errors.New("not a trajecta export bundle (magic mismatch)")
	}
	headerLine, err := readHeaderLine(r, 64*1024)
	if err != nil {
		return nil, fmt.Errorf("read export header: %w", err)
	}
	if len(headerLine) <= len(headerPrefix) || string(headerLine[:len(headerPrefix)]) != headerPrefix {
		return nil, errors.New("corrupt export envelope header line")
	}
	var header envelopeHeader
	if err := json.Unmarshal(headerLine[len(headerPrefix):], &header); err != nil {
		return nil, fmt.Errorf("decode export envelope header: %w", err)
	}
	if header.KDF != "scrypt" {
		return nil, fmt.Errorf("unsupported export kdf %q", header.KDF)
	}
	salt, err := b64decode(header.Salt)
	if err != nil {
		return nil, err
	}
	seed, err := b64decode(header.NonceSeed)
	if err != nil {
		return nil, err
	}
	if len(seed) != noncePrefixLen {
		return nil, errors.New("corrupt export envelope nonce seed")
	}
	n, rr, p := header.N, header.R, header.P
	if n < 1<<14 || n > 1<<20 || rr < 8 || rr > 32 || p < 1 || p > 8 {
		return nil, errors.New("export envelope scrypt parameters out of range")
	}
	key, err := scrypt.Key([]byte(password), salt, n, rr, p, 32)
	if err != nil {
		return nil, fmt.Errorf("derive import key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &encryptedReader{r: r, aead: aead, seed: seed}, nil
}

func readHeaderLine(r io.Reader, limit int64) ([]byte, error) {
	var line []byte
	buf := make([]byte, 1)
	for int64(len(line)) < limit {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return line, nil
			}
			line = append(line, buf[0])
		}
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("export envelope header line too long")
}

func (e *encryptedReader) Read(p []byte) (int, error) {
	for len(e.plain) == 0 {
		if e.done {
			return 0, io.EOF
		}
		if err := e.readChunk(); err != nil {
			return 0, err
		}
	}
	n := copy(p, e.plain)
	e.plain = e.plain[n:]
	return n, nil
}

func (e *encryptedReader) readChunk() error {
	length := make([]byte, 4)
	if _, err := io.ReadFull(e.r, length); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return errors.New("export bundle ended without the end-of-stream sentinel (truncated or tampered bundle)")
		}
		return err
	}
	n := binary.LittleEndian.Uint32(length)
	if n > chunkSize {
		return fmt.Errorf("export chunk length %d exceeds the maximum", n)
	}
	nonce := make([]byte, 0, 12)
	nonce = append(nonce, e.seed...)
	var counter [4]byte
	binary.BigEndian.PutUint32(counter[:], e.counter)
	nonce = append(nonce, counter[:]...)
	e.counter++
	ciphertext := make([]byte, n+16)
	if _, err := io.ReadFull(e.r, ciphertext); err != nil {
		return errors.New("export bundle ended mid-chunk (truncated or tampered bundle)")
	}
	plaintext, err := e.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return errors.New("export bundle failed authentication (wrong password or tampered bundle)")
	}
	if n == sentinelChunk && len(plaintext) == 0 {
		e.done = true
		return nil
	}
	e.plain = plaintext
	return nil
}

func b64encode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func b64decode(s string) ([]byte, error) {
	out, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode export envelope base64: %w", err)
	}
	return out, nil
}
