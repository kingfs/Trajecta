package transfer

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestEncryptedEnvelopeRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w, err := newEncryptedWriter(&buf, "correct horse battery staple")
	if err != nil {
		t.Fatalf("newEncryptedWriter() error = %v", err)
	}
	payload := bytes.Repeat([]byte("trajecta-export-payload-"), 20_000) // ~ 500 KB: several chunks
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	r, err := newEncryptedReader(bytes.NewReader(buf.Bytes()), "correct horse battery staple")
	if err != nil {
		t.Fatalf("newEncryptedReader() error = %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestEncryptedEnvelopeRejectsWrongPassword(t *testing.T) {
	var buf bytes.Buffer
	w, _ := newEncryptedWriter(&buf, "right-password")
	if _, err := w.Write([]byte("secret data")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	r, err := newEncryptedReader(bytes.NewReader(buf.Bytes()), "wrong-password")
	if err != nil {
		t.Fatalf("newEncryptedReader() error = %v", err)
	}
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("ReadAll() with the wrong password succeeded")
	}
}

func TestEncryptedEnvelopeDetectsTruncation(t *testing.T) {
	var buf bytes.Buffer
	w, _ := newEncryptedWriter(&buf, "pw")
	payload := make([]byte, 200_000)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	truncated := buf.Bytes()[:buf.Len()-40]
	r, err := newEncryptedReader(bytes.NewReader(truncated), "pw")
	if err != nil {
		t.Fatalf("newEncryptedReader() error = %v", err)
	}
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("ReadAll() on a truncated bundle succeeded")
	}
}

func TestEncryptedEnvelopeRejectsForeignMagic(t *testing.T) {
	if _, err := newEncryptedReader(strings.NewReader("not a bundle at all\n"), "pw"); err == nil {
		t.Fatal("newEncryptedReader() accepted a foreign file")
	}
}

func TestEncryptedEnvelopeRejectsExpensiveScryptParameters(t *testing.T) {
	t.Parallel()

	for _, header := range []envelopeHeader{
		{KDF: "scrypt", N: maxScryptN * 2, R: defaultScryptR, P: defaultScryptP, Salt: b64encode(make([]byte, scryptSaltSize)), NonceSeed: b64encode(make([]byte, noncePrefixLen))},
		{KDF: "scrypt", N: defaultScryptN, R: maxScryptR + 1, P: defaultScryptP, Salt: b64encode(make([]byte, scryptSaltSize)), NonceSeed: b64encode(make([]byte, noncePrefixLen))},
		{KDF: "scrypt", N: defaultScryptN, R: defaultScryptR, P: maxScryptP + 1, Salt: b64encode(make([]byte, scryptSaltSize)), NonceSeed: b64encode(make([]byte, noncePrefixLen))},
	} {
		data, err := json.Marshal(header)
		if err != nil {
			t.Fatalf("Marshal(header) error = %v", err)
		}
		bundle := envelopeMagic + headerPrefix + string(data) + "\n"
		if _, err := newEncryptedReader(strings.NewReader(bundle), "pw"); err == nil || !strings.Contains(err.Error(), "scrypt parameters out of range") {
			t.Errorf("newEncryptedReader() error = %v, want bounded-parameters rejection", err)
		}
	}
}

func TestEncryptedEnvelopeRejectsInvalidSaltLength(t *testing.T) {
	t.Parallel()

	header := envelopeHeader{
		KDF:       "scrypt",
		N:         defaultScryptN,
		R:         defaultScryptR,
		P:         defaultScryptP,
		Salt:      b64encode([]byte("short")),
		NonceSeed: b64encode(make([]byte, noncePrefixLen)),
	}
	data, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("Marshal(header) error = %v", err)
	}
	bundle := envelopeMagic + headerPrefix + string(data) + "\n"
	if _, err := newEncryptedReader(strings.NewReader(bundle), "pw"); err == nil || !strings.Contains(err.Error(), "salt") {
		t.Fatalf("newEncryptedReader() error = %v, want invalid salt-length error", err)
	}
}

func TestGenerateExportPasswordIsLongAndGrouped(t *testing.T) {
	a := generateExportPassword()
	b := generateExportPassword()
	if a == b {
		t.Fatal("two generated passwords are identical")
	}
	if len(a) != 35 || strings.Count(a, "-") != 3 {
		t.Fatalf("password %q does not look like four 8-character groups", a)
	}
}
