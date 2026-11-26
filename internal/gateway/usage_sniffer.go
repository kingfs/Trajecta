package gateway

import (
	"errors"
	"fmt"
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
type responseBodyRecorder interface {
	RecordResponseBody([]byte) (int, error)
}

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

	writeErr error
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
			var written int
			var wErr error
			if bounded, ok := s.File.(responseBodyRecorder); ok {
				// A bounded cassette writer may intentionally retain only a prefix.
				// It reports actual stored bytes; a short result with no error is not
				// a failed write and does not stop usage parsing/client delivery.
				written, wErr = bounded.RecordResponseBody(data)
			} else {
				written, wErr = s.File.Write(data)
				if wErr == nil && written != len(data) {
					wErr = io.ErrShortWrite
				}
			}
			if written > 0 && s.Count != nil {
				*s.Count += int64(written)
			}
			if wErr != nil && s.writeErr == nil {
				s.writeErr = fmt.Errorf("write response bytes to cassette: %w", wErr)
			}
		}
		if s.Pipeline != nil {
			s.Pipeline.Feed(data)
			s.setUsage()
		}
	}
	return
}

// RecordingError reports the first response write failure. It is separate from
// the upstream read/close error so callers can mark an incomplete cassette
// without falsely attributing local disk failures to an upstream.
func (s *UsageSniffer) RecordingError() error {
	if s == nil {
		return nil
	}
	return s.writeErr
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
	var closeErr error
	if s.Source != nil {
		closeErr = s.Source.Close()
	}
	return errors.Join(s.writeErr, closeErr)
}
