package gateway

import "io"

// ResponseBody is the response-side counterpart of Body. It keeps the
// upstream stream and the stream the client reads as two explicit fields.
//
// The current forwarding path wraps the client stream in a secret Restorer and
// then in UsageSniffer; the cassette records what the sniffer reads (the
// restored/client stream), matching the existing masking contract. Upstream is
// retained so a future response transform can name the raw side without
// reconstructing it from a comment.
type ResponseBody struct {
	Upstream io.ReadCloser
	Client   io.ReadCloser
}

// NewResponseBody starts in passthrough: the client reads the upstream stream.
func NewResponseBody(upstream io.ReadCloser) *ResponseBody {
	return &ResponseBody{Upstream: upstream, Client: upstream}
}

// SetClient replaces the client-visible stream. A nil client restores
// passthrough.
func (b *ResponseBody) SetClient(client io.ReadCloser) {
	if b == nil {
		return
	}
	if client == nil {
		b.Client = b.Upstream
		return
	}
	b.Client = client
}

// ClientReader returns the stream the client and recorder must consume. It is
// never nil when the ResponseBody was constructed with a non-nil upstream.
func (b *ResponseBody) ClientReader() io.ReadCloser {
	if b == nil {
		return nil
	}
	if b.Client == nil {
		return b.Upstream
	}
	return b.Client
}

// Close closes the client stream (and only closes Upstream separately when the
// two differ and the client stream does not already own it). The forwarding
// path normally closes through UsageSniffer, which owns the client chain.
func (b *ResponseBody) Close() error {
	if b == nil {
		return nil
	}
	if b.Client != nil && b.Client != b.Upstream {
		return b.Client.Close()
	}
	if b.Upstream != nil {
		return b.Upstream.Close()
	}
	return nil
}
