package apiv2

import "github.com/danielgtaylor/huma/v2"

// Capability states (docs/architecture/api-contract.md, "Discovery and
// capability detection"). Response-state additions are additive; a client
// treats an unknown state as unavailable.
const (
	StateAvailable     = "available"
	StateDisabled      = "disabled"
	StateNotConfigured = "not_configured"
	StateUnsupported   = "unsupported"
)

// Capability is the common head of every typed domain capability document.
// A domain embeds it and adds named typed fields; nothing goes in a generic
// features map.
type Capability struct {
	Revision string `json:"revision" doc:"Opaque revision of this document"`
	State    string `json:"state" enum:"available,disabled,not_configured,unsupported" doc:"Support and configuration state, not health"`
	// Allowed is the effective answer for an account/profile-scoped
	// document; omitted on server-wide documents.
	Allowed *bool `json:"allowed,omitempty" doc:"Whether the current principal may use the capability"`
}

// CapabilityProblem is the problem a mutation returns for a capability that
// is not available: route presence never changes with configuration, the
// handler reports state instead.
func CapabilityProblem(state, domain string) *Problem {
	switch state {
	case StateDisabled:
		return NewProblem(TypeCapabilityDisabled, "The "+domain+" capability is disabled by an administrator.")
	case StateNotConfigured:
		return NewProblem(TypeCapabilityNotConfigured, "The "+domain+" capability is not configured.")
	case StateUnsupported:
		return NewProblem(TypeCapabilityUnsupported, "This server build cannot provide the "+domain+" capability.")
	}
	return nil
}

// humaOp is the common operation head: method, path, id, one domain tag,
// summary, and the default success status for a read.
func humaOp(method, path, id, tag, summary string) huma.Operation {
	return huma.Operation{
		Method:      method,
		Path:        path,
		OperationID: id,
		Tags:        []string{tag},
		Summary:     summary,
	}
}
