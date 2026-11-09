package gateway

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/kingfs/Trajecta/pkg/protocolconvert"
)

// ResponseConverter converts one buffered upstream response body to the
// client protocol. The proxy's protocolConversion implements it.
type ResponseConverter interface {
	ConvertResponse([]byte) ([]byte, []protocolconvert.Warning, error)
}

// ResponseTransform applies a ResponseConverter to a successful upstream
// response and rewrites the response envelope. It owns the buffering limit,
// the non-2xx passthrough and the JSON header rewrite; the converter only
// translates bytes.
type ResponseTransform struct {
	Converter ResponseConverter
	MaxBytes  int64
}

// Apply converts resp in place and returns it with the converted body and
// warnings. A nil/non-2xx response or an empty 2xx body passes through
// unchanged. The upstream body is closed whether conversion succeeds or fails.
func (t ResponseTransform) Apply(resp *http.Response) (*http.Response, []protocolconvert.Warning, error) {
	if resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, nil, nil
	}
	if t.Converter == nil {
		return resp, nil, nil
	}
	maxBytes := t.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		_ = resp.Body.Close()
		return nil, nil, fmt.Errorf("response transform: read upstream response: %w", err)
	}
	_ = resp.Body.Close()
	if int64(len(body)) > maxBytes {
		return nil, nil, fmt.Errorf("response transform: upstream response exceeds %d bytes", maxBytes)
	}
	if len(body) == 0 {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return resp, nil, nil
	}
	converted, warnings, err := t.Converter.ConvertResponse(body)
	if err != nil {
		return nil, nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(converted))
	resp.ContentLength = int64(len(converted))
	if resp.Header != nil {
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(converted)))
		resp.Header.Del("Content-Encoding")
	}
	return resp, warnings, nil
}
