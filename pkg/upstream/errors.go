package upstream

import "errors"

// Sentinel errors returned by this package. Callers can use errors.Is to
// classify failures without matching error strings.
var (
	// ErrUnknownPreset reports a PresetID that is not in the catalog.
	ErrUnknownPreset = errors.New("upstream: unknown preset")
	// ErrUnknownPlan reports a plan that the selected preset does not define.
	ErrUnknownPlan = errors.New("upstream: unknown plan")
	// ErrNoEndpoints reports a resolution with no endpoint at all.
	ErrNoEndpoints = errors.New("upstream: no endpoints configured")
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
	// ErrNoModelList reports that a preset has no model list source or that
	// discovery is disabled for it.
	ErrNoModelList = errors.New("upstream: no model list endpoint")
	// ErrModelListUnauthorized reports a 401 or 403 model-list response.
	ErrModelListUnauthorized = errors.New("upstream: model list request unauthorized")
	// ErrModelListNotFound reports a 404 model-list response.
	ErrModelListNotFound = errors.New("upstream: model list endpoint not found")
	// ErrModelListUnavailable reports a 429 or 5xx model-list response.
	ErrModelListUnavailable = errors.New("upstream: model list endpoint unavailable")
)
