package transfer

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// bundleCursor walks a bundle sequentially. Manifest is always the first tar
// entry; database tables follow in export order; cassettes come last.
type bundleCursor struct {
	file     *os.File
	envelope *encryptedReader
	gz       *gzip.Reader
	tr       *tar.Reader
	n        int64
}

func openBundle(path string, password string) (*bundleCursor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open bundle %s: %w", path, err)
	}
	cur := &bundleCursor{file: f}

	br := bufio.NewReader(f)
	peek, err := br.Peek(len(envelopeMagic))
	if err != nil && !errors.Is(err, io.EOF) {
		f.Close()
		return nil, fmt.Errorf("read bundle head: %w", err)
	}
	pipe := io.Reader(br)
	if string(peek) == envelopeMagic {
		if password == "" {
			f.Close()
			return nil, errors.New("bundle is password-protected; pass --password or set the TRAJECTA_IMPORT_PASSWORD environment variable")
		}
		enc, err := newEncryptedReader(br, password)
		if err != nil {
			f.Close()
			return nil, err
		}
		cur.envelope = enc
		pipe = enc
	}

	// Sniff the compression layer behind the envelope (or at the file head).
	gzbr := bufio.NewReader(pipe)
	head, err := gzbr.Peek(2)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("read bundle body head: %w", err)
	}
	if head[0] == 0x1f && head[1] == 0x8b {
		gz, err := gzip.NewReader(gzbr)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("open bundle gzip: %w", err)
		}
		cur.gz = gz
		cur.tr = tar.NewReader(gz)
		return cur, nil
	}
	cur.tr = tar.NewReader(gzbr)
	return cur, nil
}

func (c *bundleCursor) Close() error {
	if c.gz != nil {
		_ = c.gz.Close()
	}
	return c.file.Close()
}

// next returns the next regular-file entry, skipping directories.
func (c *bundleCursor) next() (*tar.Header, error) {
	for {
		hdr, err := c.tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		if err != nil {
			return nil, fmt.Errorf("read bundle tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		c.n++
		return hdr, nil
	}
}

func (c *bundleCursor) body() io.Reader { return c.tr }

// readManifest consumes the first entry, which must be the manifest.
func (c *bundleCursor) readManifest() (*Manifest, error) {
	hdr, err := c.next()
	if err != nil {
		return nil, fmt.Errorf("read bundle manifest: %w", err)
	}
	if hdr.Name != entryManifest {
		return nil, fmt.Errorf("first bundle entry is %q, want %q", hdr.Name, entryManifest)
	}
	if hdr.Size > 1<<20 {
		return nil, errors.New("bundle manifest exceeds 1 MB")
	}
	data, err := io.ReadAll(io.LimitReader(c.body(), hdr.Size+1))
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode bundle manifest: %w", err)
	}
	if manifest.Format != BundleFormat {
		return nil, fmt.Errorf("bundle format %q is not supported by this trajecta (want %q)", manifest.Format, BundleFormat)
	}
	return &manifest, nil
}
