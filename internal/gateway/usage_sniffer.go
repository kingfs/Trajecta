package gateway

import (
	"io"
	"time"

	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/pkg/llm"
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
	Source   io.ReadCloser
	File     io.Writer
	Count    *int64
	Usage    *recorder.UsageInfo
	Pipeline *llm.ResponsePipeline
	Events   *[]recorder.RecordEvent
	Start    time.Time
	TTFTMs   *int64
}

func (s *UsageSniffer) Read(p []byte) (n int, err error) {
	n, err = s.Source.Read(p)
	if n > 0 {
		if s.TTFTMs != nil && *s.TTFTMs <= 0 && !s.Start.IsZero() {
			*s.TTFTMs = time.Since(s.Start).Milliseconds()
		}
		data := p[:n]
		if s.File != nil {
			if written, wErr := s.File.Write(data); wErr == nil {
				*s.Count += int64(written)
			}
		}
		if s.Pipeline != nil {
			s.Pipeline.Feed(data)
			if usage, ok := s.Pipeline.Usage(); ok && s.Usage != nil {
				*s.Usage = recorder.UsageInfo(usage)
			}
		}
	}
	return
}

func (s *UsageSniffer) Close() error {
	if s.Pipeline != nil {
		s.Pipeline.Finalize()
		if usage, ok := s.Pipeline.Usage(); ok && s.Usage != nil {
			*s.Usage = recorder.UsageInfo(usage)
		}
		if s.Events != nil {
			events := s.Pipeline.Events()
			*s.Events = append(*s.Events, events...)
		}
	}
	return s.Source.Close()
}
