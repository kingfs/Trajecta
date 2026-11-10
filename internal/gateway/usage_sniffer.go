package gateway

import (
	"io"
	"time"

	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/usage"
)

// UsageSniffer is the response-body tee that records the client-visible
// bytes, counts them and feeds the protocol usage pipeline. It lives in
// gateway because it is part of the response side's byte/usage/record
// contract, not HTTP routing.
//
// The Source must be the client-visible stream (a Restorer when masking is
// active), not the raw upstream stream: the cassette records what the client
// read, matching the existing masking contract.
type UsageSniffer struct {
	Source io.ReadCloser
	File   io.Writer
	Count  *int64
	// Usage is the canonical usage snapshot. RecordUsage is the legacy
	// cassette/DB projection of the same counters; keeping both at this
	// boundary lets the core depend on pkg/usage without changing the file
	// format.
	Usage       *usage.Usage
	RecordUsage *recorder.UsageInfo
	Pipeline    *llm.ResponsePipeline
	Events      *[]recorder.RecordEvent
	Start       time.Time
	TTFTMs      *int64
}

func (s *UsageSniffer) Read(p []byte) (n int, err error) {
	if s == nil || s.Source == nil {
		return 0, io.ErrClosedPipe
	}
	n, err = s.Source.Read(p)
	if n > 0 {
		if s.TTFTMs != nil && *s.TTFTMs <= 0 && !s.Start.IsZero() {
			*s.TTFTMs = time.Since(s.Start).Milliseconds()
		}
		data := p[:n]
		if s.File != nil {
			if written, wErr := s.File.Write(data); wErr == nil && s.Count != nil {
				*s.Count += int64(written)
			}
		}
		if s.Pipeline != nil {
			s.Pipeline.Feed(data)
			s.setUsage()
		}
	}
	return
}

func (s *UsageSniffer) setUsage() {
	if s.Pipeline == nil {
		return
	}
	snapshot, ok := s.Pipeline.Usage()
	if !ok {
		return
	}
	if s.Usage != nil {
		*s.Usage = usage.Usage{
			InputTokens:     snapshot.PromptTokens,
			OutputTokens:    snapshot.CompletionTokens,
			TotalTokens:     snapshot.TotalTokens,
			CacheReadTokens: cachedTokens(snapshot),
		}
	}
	if s.RecordUsage != nil {
		*s.RecordUsage = recorder.UsageInfo(snapshot)
	}
}

func cachedTokens(snapshot llm.UsageSummary) int {
	if snapshot.PromptTokenDetails == nil {
		return 0
	}
	return snapshot.PromptTokenDetails.CachedTokens
}

func (s *UsageSniffer) Close() error {
	if s == nil {
		return nil
	}
	if s.Pipeline != nil {
		s.Pipeline.Finalize()
		s.setUsage()
		if s.Events != nil {
			events := s.Pipeline.Events()
			*s.Events = append(*s.Events, events...)
		}
	}
	if s.Source == nil {
		return nil
	}
	return s.Source.Close()
}
