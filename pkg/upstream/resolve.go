package upstream

import (
	"github.com/kingfs/Trajecta/pkg/xir"
)

// Resolved is an immutable upstream view: the merged endpoint/auth/header data
// a caller assembles from a catalog preset (or a custom upstream) before asking
// for request URLs and auth headers. It is a value: methods copy it and never
// mutate shared maps. internal/upstream constructs it directly and delegates
// BuildURL and ApplyAuth to this package.
type Resolved struct {
	// Preset is the catalog entry the configuration started from. For a custom
	// upstream it is a synthetic value with an empty ID.
	Preset Preset
	// Plan is the selected PlanVariant, or nil.
	Plan *PlanVariant
	// Endpoints are the merged, normalized protocol base URLs.
	Endpoints map[xir.Protocol]string
	// Protocols are the protocols this upstream serves. They are in canonical
	// order when derived from endpoints and in the requested order otherwise.
	Protocols []xir.Protocol
	// Auth is the merged authentication specification.
	Auth AuthSpec
	// Headers are the merged static headers.
	Headers map[string]string
	// APIVersion, Deployment, Project, Location and ModelResource carry the
	// provider-specific configuration values.
	APIVersion    string
	Deployment    string
	Project       string
	Location      string
	ModelResource string
	// ModelProtocols is the normalized per-model protocol override.
	ModelProtocols map[string][]xir.Protocol
	// Models is the preset model source.
	Models ModelSource
	// Credential is an optional attached secret. ApplyAuth takes the credential
	// explicitly; nothing here is persisted.
	Credential Credential
}
