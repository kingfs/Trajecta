package observe

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

type ParseInput struct {
	TraceID          string
	CassettePath     string
	Header           recordfile.RecordHeader
	Events           []recordfile.RecordEvent
	RequestBody      []byte
	ResponseBody     []byte
	IsStream         bool
	ExchangeKind     string
	ExchangeRole     string
	ParentExchangeID string
	SequenceIndex    int
	RequestAuditID   string
	ResponseID       string
}

type Parser interface {
	Name() string
	Version() string
	CanParse(input ParseInput) bool
	Parse(ctx context.Context, input ParseInput) (TraceObservation, error)
}

type Registry struct {
	mu      sync.RWMutex
	parsers []Parser
}

func NewRegistry(parsers ...Parser) *Registry {
	r := &Registry{}
	for _, parser := range parsers {
		r.Register(parser)
	}
	return r
}

func NewDefaultRegistry() *Registry {
	return NewRegistry(
		NewEntryParser(),
		NewOpenAIParser(),
		NewAnthropicParser(),
		NewGeminiParser(),
	)
}

func (r *Registry) Register(parser Parser) {
	if parser == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parsers = append(r.parsers, parser)
}

func (r *Registry) Select(input ParseInput) (Parser, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, parser := range r.parsers {
		if parser.CanParse(input) {
			return parser, true
		}
	}
	return nil, false
}

// ErrNoParser reports that the registry holds no parser for an exchange's
// provider and operation.
//
// It is not a malformed payload. The exchange can be perfectly well-formed
// traffic for an operation this build does not parse, so callers must not record
// it as a parse failure; see ParseStatusUnsupported and UnsupportedObservation.
var ErrNoParser = errors.New("observe: no parser for the exchange")

// NoParserError names the exchange ErrNoParser was reported for.
type NoParserError struct {
	Provider  string
	Operation string
	Endpoint  string
}

func (e NoParserError) Error() string {
	return fmt.Sprintf("observe: no parser for provider=%q operation=%q endpoint=%q", e.Provider, e.Operation, e.Endpoint)
}

// Is makes errors.Is(err, ErrNoParser) report true.
func (e NoParserError) Is(target error) bool { return target == ErrNoParser }

func (r *Registry) Parse(ctx context.Context, input ParseInput) (TraceObservation, error) {
	parser, ok := r.Select(input)
	if !ok {
		return TraceObservation{}, NoParserError{
			Provider:  input.Header.Meta.Provider,
			Operation: input.Header.Meta.Operation,
			Endpoint:  input.Header.Meta.Endpoint,
		}
	}
	return parser.Parse(ctx, input)
}

// UnsupportedObservation is the observation recorded for an exchange this build
// has no parser for: a successful, non-failed outcome that keeps the exchange's
// metadata and explains itself through a warning.
func UnsupportedObservation(input ParseInput) TraceObservation {
	meta := input.Header.Meta
	obs := TraceObservation{
		TraceID:   input.TraceID,
		Provider:  meta.Provider,
		Operation: meta.Operation,
		Endpoint:  meta.Endpoint,
		Model:     meta.Model,
		Status:    ParseStatusUnsupported,
	}
	applyExchangeMetadata(input, &obs)
	obs.Warnings = append(obs.Warnings, ParseWarning{
		Code:    "no_parser",
		Message: fmt.Sprintf("no parser for provider=%q operation=%q endpoint=%q; the exchange is recorded but not parsed", meta.Provider, meta.Operation, meta.Endpoint),
	})
	return obs
}

func applyExchangeMetadata(input ParseInput, obs *TraceObservation) {
	obs.ExchangeKind = firstObservationNonEmpty(input.ExchangeKind, input.Header.Meta.ExchangeKind, obs.ExchangeKind)
	obs.ExchangeRole = firstObservationNonEmpty(input.ExchangeRole, input.Header.Meta.ExchangeRole, obs.ExchangeRole)
	obs.ParentExchangeID = firstObservationNonEmpty(input.ParentExchangeID, input.Header.Meta.ParentExchangeID, obs.ParentExchangeID)
	if input.SequenceIndex != 0 {
		obs.SequenceIndex = input.SequenceIndex
	} else if input.Header.Meta.SequenceIndex != 0 {
		obs.SequenceIndex = input.Header.Meta.SequenceIndex
	}
	obs.RequestAuditID = firstObservationNonEmpty(input.RequestAuditID, input.Header.Meta.RequestAuditID, obs.RequestAuditID)
	obs.ResponseID = firstObservationNonEmpty(input.ResponseID, input.Header.Meta.ResponseID, obs.ResponseID)
	if obs.ExchangeKind == "" {
		obs.ExchangeKind = "model"
	}
	if obs.ExchangeRole == "" {
		if obs.ExchangeKind == "entry" {
			obs.ExchangeRole = "client_request"
		} else {
			obs.ExchangeRole = "primary_model_call"
		}
	}
}

func firstObservationNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
