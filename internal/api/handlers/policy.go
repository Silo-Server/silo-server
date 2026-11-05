package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/Silo-Server/silo-server/internal/policy"
)

const (
	policyErrorBadRequest    = "bad_request"
	policyErrorConflict      = "conflict"
	policyErrorInternal      = "internal_error"
	policyErrorNotFound      = "not_found"
	policyErrorUnavailable   = "unavailable"
	policyErrorUnprocessable = "unprocessable_entity"

	policyInvalidRequestBodyMessage = "Invalid request body"
	policyStoreUnavailableMessage   = "Policy store is not configured"
	policyDocumentNotFoundMessage   = "Policy document not found"
	policyVersionNotFoundMessage    = "Policy version not found"
)

// maxPolicyRequestBodyBytes bounds JSON bodies on policy write endpoints.
// CompileCheck already caps Rego sources at 256 KiB, but only after the body
// has been decoded; 1 MiB leaves room for JSON encoding overhead and simulate
// input documents while keeping oversized payloads from buffering in memory.
const maxPolicyRequestBodyBytes = 1 << 20

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

// policyApplyStatus reports whether a persisted policy mutation is live on
// this node. Applied=false means the store change succeeded but the local
// engine still serves the previous generation (the poll loop keeps retrying).
type policyApplyStatus struct {
	Applied          bool   `json:"applied"`
	FailedStep       string `json:"failed_step,omitempty"`
	LoadedGeneration int64  `json:"loaded_generation"`
}

type policyVendorModuleResponse struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

type policyCreateDocumentRequest struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
}

type policyDocumentResponse struct {
	ID              int64                  `json:"id"`
	Domain          string                 `json:"domain"`
	Name            string                 `json:"name"`
	Enabled         bool                   `json:"enabled"`
	ActiveVersionID *int64                 `json:"active_version_id,omitempty"`
	ActiveVersion   *policyVersionResponse `json:"active_version,omitempty"`
	CreatedAt       string                 `json:"created_at"`
	UpdatedAt       string                 `json:"updated_at"`
}

type policyCreateVersionRequest struct {
	Source  string  `json:"source"`
	Comment *string `json:"comment,omitempty"`
}

type policyVersionResponse struct {
	ID              int64   `json:"id"`
	DocumentID      int64   `json:"document_id"`
	VersionNumber   int     `json:"version_number"`
	SourceSHA256    string  `json:"source_sha256"`
	CompiledOK      bool    `json:"compiled_ok"`
	CompileError    *string `json:"compile_error,omitempty"`
	CreatedByUserID *int    `json:"created_by_user_id,omitempty"`
	Comment         *string `json:"comment,omitempty"`
	CreatedAt       string  `json:"created_at"`
	Source          string  `json:"source,omitempty"`
}

type policyCreateVersionResponse struct {
	ID            int64 `json:"id"`
	VersionNumber int   `json:"version_number"`
	CompiledOK    bool  `json:"compiled_ok"`
}

type policyActivateVersionResponse struct {
	ActiveVersionID int64 `json:"active_version_id"`
	Generation      int64 `json:"generation"`
	policyApplyStatus
}

type policySetEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

type policySetEnabledResponse struct {
	ID         int64 `json:"id"`
	Enabled    bool  `json:"enabled"`
	Generation int64 `json:"generation"`
	policyApplyStatus
}

type policyValidateRequest struct {
	Domain string `json:"domain"`
	Source string `json:"source"`
}

type policyValidateResponse struct {
	CompiledOK bool                  `json:"compiled_ok"`
	Errors     []policy.CompileIssue `json:"errors"`
}

type policyCompileErrorsResponse struct {
	Errors []policy.CompileIssue `json:"errors"`
}

type policyDecisionListResponse struct {
	Entries    []policyDecisionResponse `json:"entries"`
	NextCursor string                   `json:"next_cursor,omitempty"`
}

type policyDecisionResponse struct {
	ID               int64           `json:"id"`
	Timestamp        string          `json:"timestamp"`
	DecisionName     string          `json:"decision_name"`
	PolicyGeneration int64           `json:"policy_generation"`
	UserID           *int            `json:"user_id,omitempty"`
	ProfileID        string          `json:"profile_id,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	RequestID        string          `json:"request_id,omitempty"`
	NodeID           string          `json:"node_id,omitempty"`
	Allowed          *bool           `json:"allowed"`
	EvalTimeNS       int64           `json:"eval_time_ns"`
	InputDigest      string          `json:"input_digest"`
	InputSample      json.RawMessage `json:"input_sample,omitempty"`
	ResultSample     json.RawMessage `json:"result_sample,omitempty"`
	Error            string          `json:"error,omitempty"`
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
