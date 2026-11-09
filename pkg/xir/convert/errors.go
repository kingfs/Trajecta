package convert

import (
	"errors"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// Sentinel errors reported by the conversion hub. Use errors.Is for a stable
// check and errors.As for the structured detail.
var (
	// ErrStreamingConversion reports a request to convert a streaming exchange.
	// Stream conversion is not implemented; same-protocol streaming bodies are
	// passed through unchanged instead.
	ErrStreamingConversion = errors.New("xir/convert: streaming conversion is not supported")
	// ErrFatalLoss reports a conversion rejected by the loss policy because a
	// fatal semantic loss would change the answer.
	ErrFatalLoss = errors.New("xir/convert: fatal conversion loss")
	// ErrCodecMissing reports a protocol with no registered codec.
	ErrCodecMissing = errors.New("xir/convert: codec is not registered")
)

// StreamingConversionError describes a rejected streaming conversion. It
// unwraps to ErrStreamingConversion.
type StreamingConversionError struct {
	From xir.Protocol
	To   xir.Protocol
}

func (e *StreamingConversionError) Error() string {
	return fmt.Sprintf("%s: %s -> %s", ErrStreamingConversion, e.From, e.To)
}

// Unwrap makes errors.Is(err, ErrStreamingConversion) true.
func (e *StreamingConversionError) Unwrap() error { return ErrStreamingConversion }

// CodecMissingError describes a protocol with no codec in the selected
// registry. It unwraps to ErrCodecMissing.
type CodecMissingError struct {
	Protocol  xir.Protocol
	Direction string // "source" or "target"
}

func (e *CodecMissingError) Error() string {
	return fmt.Sprintf("%s: %s codec for %q", ErrCodecMissing, e.Direction, e.Protocol)
}

// Unwrap makes errors.Is(err, ErrCodecMissing) true.
func (e *CodecMissingError) Unwrap() error { return ErrCodecMissing }

// FatalLossError carries the fatal losses that rejected a conversion. It
// unwraps to ErrFatalLoss.
type FatalLossError struct {
	Losses []xir.Loss
}

func (e *FatalLossError) Error() string {
	if len(e.Losses) == 0 {
		return ErrFatalLoss.Error()
	}
	fields := make([]string, 0, len(e.Losses))
	for _, loss := range e.Losses {
		fields = append(fields, loss.Field)
	}
	return fmt.Sprintf("%s: %s", ErrFatalLoss, strings.Join(fields, ", "))
}

// Unwrap makes errors.Is(err, ErrFatalLoss) true.
func (e *FatalLossError) Unwrap() error { return ErrFatalLoss }
