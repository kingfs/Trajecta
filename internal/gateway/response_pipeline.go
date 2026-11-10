package gateway

import (
	"io"
	"time"

	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/secretmask"
	"github.com/kingfs/Trajecta/pkg/usage"
)

// ResponsePipelineOptions configures one response-side pipeline.
type ResponsePipelineOptions struct {
	Upstream io.ReadCloser
	// Vault, when non-nil, restores masked stand-ins for the client. It is
	// installed underneath UsageSniffer so the cassette records what the
	// client read.
	Vault *secretmask.Vault

	File        io.Writer
	Count       *int64
	Usage       *usage.Usage
	RecordUsage *recorder.UsageInfo
	Pipeline    *llm.ResponsePipeline
	Events      *[]recorder.RecordEvent
	Start       time.Time
	TTFTMs      *int64
}

// ResponsePipeline is the fixed response-side order:
//
//	upstream -> [Restorer] -> UsageSniffer -> client
//
// The restorer is deliberately inside the sniffer: UsageSniffer writes the
// bytes it reads into the cassette, so a stand-in must already be restored
// before that point.
type ResponsePipeline struct {
	body     *ResponseBody
	sniffer  *UsageSniffer
	restorer *secretmask.Restorer
}

// NewResponsePipeline builds the response-side pipeline. It returns a valid
// pipeline even when Upstream is nil; Reader then returns a nil ReadCloser and
// Close is a no-op.
func NewResponsePipeline(opts ResponsePipelineOptions) *ResponsePipeline {
	body := NewResponseBody(opts.Upstream)
	var restorer *secretmask.Restorer
	if opts.Vault != nil && opts.Upstream != nil {
		restorer = secretmask.NewRestorer(opts.Upstream, opts.Vault)
		body.SetClient(restorer)
	}
	sniffer := &UsageSniffer{
		Source:      body.ClientReader(),
		File:        opts.File,
		Count:       opts.Count,
		Usage:       opts.Usage,
		RecordUsage: opts.RecordUsage,
		Pipeline:    opts.Pipeline,
		Events:      opts.Events,
		Start:       opts.Start,
		TTFTMs:      opts.TTFTMs,
	}
	return &ResponsePipeline{body: body, sniffer: sniffer, restorer: restorer}
}

// Reader returns the stream the HTTP handler must copy to the client. Closing
// it finalizes usage and closes the upstream chain.
func (p *ResponsePipeline) Reader() io.ReadCloser {
	if p == nil || p.sniffer.Source == nil {
		return nil
	}
	return p.sniffer
}

// Close finalizes the pipeline. It is safe to call more than once only if the
// underlying reader tolerates it; the HTTP path calls it once.
func (p *ResponsePipeline) Close() error {
	if p == nil || p.sniffer.Source == nil {
		return nil
	}
	return p.sniffer.Close()
}

// Restored reports how many masked stand-ins were restored for the client.
func (p *ResponsePipeline) Restored() int {
	if p == nil || p.restorer == nil {
		return 0
	}
	return p.restorer.Restored()
}
