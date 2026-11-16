package upstream

import "errors"

// Sentinel errors returned by this package. Callers can use errors.Is to
// classify failures without matching error strings.
var (
	// ErrInvalidEndpoint reports a base URL that is not an absolute http(s) URL.
	ErrInvalidEndpoint = errors.New("upstream: invalid endpoint")
	// ErrInvalidAuthMode reports an AuthMode outside the supported set.
	ErrInvalidAuthMode = errors.New("upstream: invalid auth mode")
	// ErrMissingCredential reports an auth mode that needs a secret the
	// credential did not carry.
	ErrMissingCredential = errors.New("upstream: missing credential")
	// ErrUnsupportedProtocol reports a protocol outside the xir protocol set.
	ErrUnsupportedProtocol = errors.New("upstream: unsupported protocol")
	// ErrNoEndpoint reports that a resolved upstream has no endpoint for a
	// requested protocol.
	ErrNoEndpoint = errors.New("upstream: no endpoint for protocol")
)
