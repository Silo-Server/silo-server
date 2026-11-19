package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/Silo-Server/silo-server/internal/policy"
)

const (
	policyErrorBadRequest    = "bad_request"
	policyErrorConflict      = "conflict"
	policyErrorInternal      = "internal_error"
	policyErrorNotFound      = "not_found"
	policyErrorUnavailable   = "unavailable"
	policyErrorUnprocessable = "unprocessable_entity"
)

// PolicyHandler handles policy management and simulation endpoints.
type PolicyHandler struct {
	system          *policy.System
	store           *policy.PolicyStore
	decisions       *policy.DecisionRepository
	editorAvailable func() bool
}

// NewPolicyHandler creates a PolicyHandler.
func NewPolicyHandler(
	system *policy.System,
	store *policy.PolicyStore,
	decisions *policy.DecisionRepository,
	editorAvailable ...func() bool,
) *PolicyHandler {
	var available func() bool
	if len(editorAvailable) > 0 {
		available = editorAvailable[0]
	}
	return &PolicyHandler{system: system, store: store, decisions: decisions, editorAvailable: available}
}

type PolicyCapabilityView struct {
	Enabled         bool     `json:"enabled"`
	EditorAvailable bool     `json:"editor_available"`
	DecisionTypes   []string `json:"decision_types"`
	Generation      int64    `json:"generation"`
	Degraded        bool     `json:"degraded"`
	DegradedReason  string   `json:"degraded_reason,omitempty"`
	DegradedDomains []string `json:"degraded_domains,omitempty"`
	EvalTimeouts    int64    `json:"eval_timeouts"`
}

// PolicyCapability reports runtime health independently of editor authorization.
func (h *PolicyHandler) PolicyCapability(_ context.Context) PolicyCapabilityView {
	if h == nil || h.system == nil {
		return PolicyCapabilityView{DecisionTypes: policy.DecisionTypes()}
	}
	degraded := h.system.DegradedState()
	return PolicyCapabilityView{
		Enabled:         true,
		EditorAvailable: h.editorEnabled(),
		DecisionTypes:   policy.DecisionTypes(),
		Generation:      h.system.Generation(),
		Degraded:        degraded.Degraded,
		DegradedReason:  degraded.Reason,
		DegradedDomains: degraded.Domains,
		EvalTimeouts:    h.system.EvalTimeouts(),
	}
}

func (h *PolicyHandler) editorEnabled() bool {
	return h != nil && h.editorAvailable != nil && h.editorAvailable()
}

func sha256Hex(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}
