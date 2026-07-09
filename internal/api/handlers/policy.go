package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
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

// decodePolicyRequest decodes a size-capped JSON request body into dst,
// writing the error response and returning false when decoding fails.
func decodePolicyRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPolicyRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Request body must be under 1 MiB")
			return false
		}
		writeError(w, http.StatusBadRequest, policyErrorBadRequest, policyInvalidRequestBodyMessage)
		return false
	}
	return true
}

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

type policyCapabilityResponse struct {
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

// HandleCapability handles GET /policy/capability.
func (h *PolicyHandler) HandleCapability(w http.ResponseWriter, r *http.Request) {
	if apimw.GetUserID(r.Context()) == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.system == nil {
		writeError(w, http.StatusServiceUnavailable, policyErrorUnavailable, "Policy system is not configured")
		return
	}

	degraded := h.system.DegradedState()
	writeJSON(w, http.StatusOK, policyCapabilityResponse{
		Enabled:         true,
		EditorAvailable: h.editorEnabled(),
		DecisionTypes:   policy.DecisionTypes(),
		Generation:      h.system.Generation(),
		Degraded:        degraded.Degraded,
		DegradedReason:  degraded.Reason,
		DegradedDomains: degraded.Domains,
		EvalTimeouts:    h.system.EvalTimeouts(),
	})
}

// HandleListVendor handles GET /admin/policy/vendor.
func (h *PolicyHandler) HandleListVendor(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	modules, err := policy.VendorModules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, policyErrorInternal, "Failed to load vendor policy modules")
		return
	}
	response := make([]policyVendorModuleResponse, 0, len(modules))
	for _, module := range modules {
		response = append(response, policyVendorModuleResponse{
			Path:   module.Path,
			Source: module.Source,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

// HandleListDocuments handles GET /admin/policy/documents.
func (h *PolicyHandler) HandleListDocuments(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	if !h.requireStore(w) {
		return
	}
	documents, err := h.store.ListDocuments(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, policyErrorInternal, "Failed to list policy documents")
		return
	}
	response := make([]policyDocumentResponse, 0, len(documents))
	for _, document := range documents {
		response = append(response, toPolicyDocumentResponse(document, nil))
	}
	writeJSON(w, http.StatusOK, response)
}

// HandleCreateDocument handles POST /admin/policy/documents.
func (h *PolicyHandler) HandleCreateDocument(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	if !h.requireStore(w) {
		return
	}

	var req policyCreateDocumentRequest
	if !decodePolicyRequest(w, r, &req) {
		return
	}
	domain := strings.TrimSpace(req.Domain)
	name := strings.TrimSpace(req.Name)
	if !policy.ValidDomain(domain) {
		writeError(w, http.StatusBadRequest, policyErrorBadRequest, "Invalid policy domain")
		return
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, policyErrorBadRequest, "Name is required")
		return
	}

	document, err := h.store.CreateDocument(r.Context(), domain, name)
	if err != nil {
		h.writeStoreError(w, err, "Failed to create policy document")
		return
	}
	writeJSON(w, http.StatusCreated, toPolicyDocumentResponse(document, nil))
}

// HandleGetDocument handles GET /admin/policy/documents/{id}.
func (h *PolicyHandler) HandleGetDocument(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	if !h.requireStore(w) {
		return
	}
	documentID, ok := parsePolicyInt64Param(w, r, "id", "Invalid policy document ID")
	if !ok {
		return
	}
	document, err := h.store.GetDocument(r.Context(), documentID)
	if err != nil {
		h.writeStoreError(w, err, "Failed to load policy document")
		return
	}

	var active *policyVersionResponse
	if document.ActiveVersionID != nil {
		version, err := h.store.GetVersion(r.Context(), document.ID, *document.ActiveVersionID)
		if err != nil {
			h.writeStoreError(w, err, "Failed to load active policy version")
			return
		}
		versionResponse := toPolicyVersionResponse(version, true)
		active = &versionResponse
	}
	writeJSON(w, http.StatusOK, toPolicyDocumentResponse(document, active))
}

// HandleListVersions handles GET /admin/policy/documents/{id}/versions.
func (h *PolicyHandler) HandleListVersions(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	if !h.requireStore(w) {
		return
	}
	documentID, ok := parsePolicyInt64Param(w, r, "id", "Invalid policy document ID")
	if !ok {
		return
	}
	if _, err := h.store.GetDocument(r.Context(), documentID); err != nil {
		h.writeStoreError(w, err, "Failed to load policy document")
		return
	}
	versions, err := h.store.ListVersions(r.Context(), documentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, policyErrorInternal, "Failed to list policy versions")
		return
	}
	response := make([]policyVersionResponse, 0, len(versions))
	for _, version := range versions {
		response = append(response, toPolicyVersionResponse(version, false))
	}
	writeJSON(w, http.StatusOK, response)
}

// HandleGetVersion handles GET /admin/policy/documents/{id}/versions/{version}.
func (h *PolicyHandler) HandleGetVersion(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	if !h.requireStore(w) {
		return
	}
	documentID, versionID, ok := parsePolicyVersionRoute(w, r)
	if !ok {
		return
	}
	version, err := h.store.GetVersion(r.Context(), documentID, versionID)
	if err != nil {
		h.writeStoreError(w, err, "Failed to load policy version")
		return
	}
	writeJSON(w, http.StatusOK, toPolicyVersionResponse(version, true))
}

// HandleCreateVersion handles POST /admin/policy/documents/{id}/versions.
func (h *PolicyHandler) HandleCreateVersion(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	if !h.requireStore(w) {
		return
	}
	documentID, ok := parsePolicyInt64Param(w, r, "id", "Invalid policy document ID")
	if !ok {
		return
	}
	document, err := h.store.GetDocument(r.Context(), documentID)
	if err != nil {
		h.writeStoreError(w, err, "Failed to load policy document")
		return
	}

	var req policyCreateVersionRequest
	if !decodePolicyRequest(w, r, &req) {
		return
	}

	compileErr := policy.CompileCheck(r.Context(), document.Domain, req.Source)
	compiledOK := compileErr == nil
	var compileErrorText *string
	if compileErr != nil {
		text := compileErr.Error()
		compileErrorText = &text
	}
	comment := ""
	if req.Comment != nil {
		comment = *req.Comment
	}
	version, err := h.store.CreateVersion(
		r.Context(),
		document.ID,
		req.Source,
		sha256Hex(req.Source),
		compiledOK,
		compileErrorText,
		createdByUserID(r),
		comment,
	)
	if err != nil {
		h.writeStoreError(w, err, "Failed to create policy version")
		return
	}
	if compileErr != nil {
		h.writeCompileError(w, compileErr)
		return
	}
	writeJSON(w, http.StatusCreated, policyCreateVersionResponse{
		ID:            version.ID,
		VersionNumber: version.VersionNumber,
		CompiledOK:    version.CompiledOK,
	})
}

// HandleActivateVersion handles POST /admin/policy/documents/{id}/versions/{version}/activate.
func (h *PolicyHandler) HandleActivateVersion(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	if !h.requireStore(w) {
		return
	}
	documentID, versionID, ok := parsePolicyVersionRoute(w, r)
	if !ok {
		return
	}
	document, err := h.store.GetDocument(r.Context(), documentID)
	if err != nil {
		h.writeStoreError(w, err, "Failed to load policy document")
		return
	}
	version, err := h.store.GetVersion(r.Context(), documentID, versionID)
	if err != nil {
		h.writeStoreError(w, err, "Failed to load policy version")
		return
	}
	if err := policy.GuardEvalCost(r.Context(), document.Domain, version.RegoSource, h.evalBudget()); err != nil {
		h.writeEvalCostError(w, err)
		return
	}
	generation, err := h.store.Activate(r.Context(), documentID, versionID)
	if err != nil {
		h.writeStoreError(w, err, "Failed to activate policy version")
		return
	}
	status := h.notifyChanged(r)
	writeJSON(w, applyStatusCode(status), policyActivateVersionResponse{
		ActiveVersionID:   versionID,
		Generation:        generation,
		policyApplyStatus: status,
	})
}

// HandleSetDocumentEnabled handles POST /admin/policy/documents/{id}/enabled.
func (h *PolicyHandler) HandleSetDocumentEnabled(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	if !h.requireStore(w) {
		return
	}
	documentID, ok := parsePolicyInt64Param(w, r, "id", "Invalid policy document ID")
	if !ok {
		return
	}

	var req policySetEnabledRequest
	if !decodePolicyRequest(w, r, &req) {
		return
	}
	if req.Enabled {
		// Enabling puts the document's active version into the live bundle, so
		// it gets the same eval-cost guard as activation.
		document, err := h.store.GetDocument(r.Context(), documentID)
		if err != nil {
			h.writeStoreError(w, err, "Failed to load policy document")
			return
		}
		if document.ActiveVersionID != nil {
			version, err := h.store.GetVersion(r.Context(), documentID, *document.ActiveVersionID)
			if err != nil {
				h.writeStoreError(w, err, "Failed to load policy version")
				return
			}
			if err := policy.GuardEvalCost(r.Context(), document.Domain, version.RegoSource, h.evalBudget()); err != nil {
				h.writeEvalCostError(w, err)
				return
			}
		}
	}
	generation, err := h.store.SetEnabled(r.Context(), documentID, req.Enabled)
	if err != nil {
		h.writeStoreError(w, err, "Failed to update policy document")
		return
	}
	status := h.notifyChanged(r)
	writeJSON(w, applyStatusCode(status), policySetEnabledResponse{
		ID:                documentID,
		Enabled:           req.Enabled,
		Generation:        generation,
		policyApplyStatus: status,
	})
}

// HandleDeleteDocument handles DELETE /admin/policy/documents/{id}.
func (h *PolicyHandler) HandleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	if !h.requireStore(w) {
		return
	}
	documentID, ok := parsePolicyInt64Param(w, r, "id", "Invalid policy document ID")
	if !ok {
		return
	}
	if err := h.store.DeleteDocument(r.Context(), documentID); err != nil {
		h.writeStoreError(w, err, "Failed to delete policy document")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleValidate handles POST /admin/policy/validate.
func (h *PolicyHandler) HandleValidate(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	var req policyValidateRequest
	if !decodePolicyRequest(w, r, &req) {
		return
	}
	response := policyValidateResponse{CompiledOK: true, Errors: []policy.CompileIssue{}}
	if err := policy.CompileCheck(r.Context(), strings.TrimSpace(req.Domain), req.Source); err != nil {
		response.CompiledOK = false
		response.Errors = compileIssues(err)
	}
	writeJSON(w, http.StatusOK, response)
}

// HandleSimulate handles POST /admin/policy/simulate.
func (h *PolicyHandler) HandleSimulate(w http.ResponseWriter, r *http.Request) {
	if !h.requireEditor(w) {
		return
	}
	var req policy.SimulateRequest
	if !decodePolicyRequest(w, r, &req) {
		return
	}
	result, err := policy.Simulate(r.Context(), h.store, req)
	if err != nil {
		h.writeSimulateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// HandleListDecisions handles GET /admin/policy/decisions.
func (h *PolicyHandler) HandleListDecisions(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.decisions == nil {
		writeError(w, http.StatusServiceUnavailable, policyErrorUnavailable, "Policy decision repository is not configured")
		return
	}

	opts := policy.ListOptions{
		DecisionName: strings.TrimSpace(r.URL.Query().Get("decision_name")),
		Cursor:       strings.TrimSpace(r.URL.Query().Get("cursor")),
		Limit:        parseLimit(r, 100),
	}
	userID, err := parseOptionalIntQuery(r, "user_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, policyErrorBadRequest, err.Error())
		return
	}
	opts.UserID = userID
	allowed, err := parseOptionalBoolQuery(r, "allowed")
	if err != nil {
		writeError(w, http.StatusBadRequest, policyErrorBadRequest, err.Error())
		return
	}
	opts.Allowed = allowed
	if from, err := parseTimeQuery(r, "from"); err != nil {
		writeError(w, http.StatusBadRequest, policyErrorBadRequest, err.Error())
		return
	} else {
		opts.From = from
	}
	if to, err := parseTimeQuery(r, "to"); err != nil {
		writeError(w, http.StatusBadRequest, policyErrorBadRequest, err.Error())
		return
	} else {
		opts.To = to
	}

	result, err := h.decisions.List(r.Context(), opts)
	if err != nil {
		if strings.Contains(err.Error(), "cursor") {
			writeError(w, http.StatusBadRequest, policyErrorBadRequest, "Invalid cursor")
			return
		}
		writeError(w, http.StatusInternalServerError, policyErrorInternal, "Failed to list policy decisions")
		return
	}
	entries := make([]policyDecisionResponse, 0, len(result.Entries))
	for _, entry := range result.Entries {
		entries = append(entries, toPolicyDecisionResponse(entry, false))
	}
	writeJSON(w, http.StatusOK, policyDecisionListResponse{
		Entries:    entries,
		NextCursor: result.NextCursor,
	})
}

// HandleGetDecision handles GET /admin/policy/decisions/{id}.
func (h *PolicyHandler) HandleGetDecision(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.decisions == nil {
		writeError(w, http.StatusServiceUnavailable, policyErrorUnavailable, "Policy decision repository is not configured")
		return
	}
	id, ok := parsePolicyInt64Param(w, r, "id", "Invalid policy decision ID")
	if !ok {
		return
	}
	entry, err := h.decisions.Get(r.Context(), id, nil)
	if err != nil {
		if errors.Is(err, policy.ErrDecisionNotFound) {
			writeError(w, http.StatusNotFound, policyErrorNotFound, "Policy decision not found")
			return
		}
		writeError(w, http.StatusInternalServerError, policyErrorInternal, "Failed to load policy decision")
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDecisionResponse(entry, true))
}

func (h *PolicyHandler) requireStore(w http.ResponseWriter) bool {
	if h == nil || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, policyErrorUnavailable, policyStoreUnavailableMessage)
		return false
	}
	return true
}

func (h *PolicyHandler) requireEditor(w http.ResponseWriter) bool {
	if h == nil || !h.editorEnabled() {
		writeError(w, http.StatusForbidden, "policy_editor_disabled", "Policy editor is disabled")
		return false
	}
	return true
}

func (h *PolicyHandler) editorEnabled() bool {
	return h != nil && h.editorAvailable != nil && h.editorAvailable()
}

// notifyChanged applies the persisted change to the live engine and reports
// the outcome. Store success and live-apply success are distinct: callers must
// surface Applied=false instead of implying the policy is already enforced.
func (h *PolicyHandler) notifyChanged(r *http.Request) policyApplyStatus {
	if h == nil || h.system == nil {
		return policyApplyStatus{Applied: false, FailedStep: "local_reload"}
	}
	status := h.system.ApplyChanged(r.Context())
	if err := status.Err(); err != nil {
		slog.ErrorContext(r.Context(), "policy notify changed failed", "component", "api", "error", err, "failed_step", status.FailedStep())
	}
	return policyApplyStatus{
		Applied:          status.Applied(),
		FailedStep:       status.FailedStep(),
		LoadedGeneration: status.Generation,
	}
}

// applyStatusCode maps an apply outcome to the response status: 200 when the
// mutation is live on this node, 202 when it persisted but has not applied.
func applyStatusCode(status policyApplyStatus) int {
	if status.Applied {
		return http.StatusOK
	}
	return http.StatusAccepted
}

func (h *PolicyHandler) writeStoreError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, policy.ErrDocumentNotFound):
		writeError(w, http.StatusNotFound, policyErrorNotFound, policyDocumentNotFoundMessage)
	case errors.Is(err, policy.ErrVersionNotFound):
		writeError(w, http.StatusNotFound, policyErrorNotFound, policyVersionNotFoundMessage)
	case errors.Is(err, policy.ErrVersionNotCompiled):
		writeError(w, http.StatusUnprocessableEntity, policyErrorUnprocessable, "Policy version did not compile")
	case errors.Is(err, policy.ErrDomainAlreadyEnabled):
		writeError(w, http.StatusConflict, policyErrorConflict, "Policy domain already has an enabled document")
	case errors.Is(err, policy.ErrDocumentHasActiveVersion):
		writeError(w, http.StatusConflict, policyErrorConflict, "Policy document has an active version")
	default:
		writeError(w, http.StatusInternalServerError, policyErrorInternal, fallback)
	}
}

// evalBudget returns the live per-decision evaluation budget for the
// activation-time cost guard.
func (h *PolicyHandler) evalBudget() time.Duration {
	if h == nil || h.system == nil {
		return 0 // GuardEvalCost falls back to the default budget
	}
	return h.system.EvalTimeout()
}

func (h *PolicyHandler) writeEvalCostError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, policy.ErrPolicySlowEval):
		writeError(w, http.StatusUnprocessableEntity, policyErrorUnprocessable, err.Error())
	case errors.Is(err, policy.ErrCompileFailed):
		h.writeCompileError(w, err)
	default:
		writeError(w, http.StatusInternalServerError, policyErrorInternal, "Failed to verify policy evaluation cost")
	}
}

func (h *PolicyHandler) writeCompileError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusUnprocessableEntity, policyCompileErrorsResponse{
		Errors: compileIssues(err),
	})
}

func (h *PolicyHandler) writeSimulateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, policy.ErrUnsupportedDomain), errors.Is(err, policy.ErrUnknownDecision):
		writeError(w, http.StatusUnprocessableEntity, policyErrorUnprocessable, err.Error())
	case errors.Is(err, policy.ErrCompileFailed):
		h.writeCompileError(w, err)
	case errors.Is(err, policy.ErrPolicyEvalFailed):
		writeError(w, http.StatusUnprocessableEntity, policyErrorUnprocessable, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, policyErrorInternal, "Failed to simulate policy")
	}
}

func compileIssues(err error) []policy.CompileIssue {
	var compileErr *policy.CompileError
	if errors.As(err, &compileErr) {
		return compileErr.Issues
	}
	return []policy.CompileIssue{{Message: err.Error()}}
}

func toPolicyDocumentResponse(document policy.Document, active *policyVersionResponse) policyDocumentResponse {
	return policyDocumentResponse{
		ID:              document.ID,
		Domain:          document.Domain,
		Name:            document.Name,
		Enabled:         document.Enabled,
		ActiveVersionID: document.ActiveVersionID,
		ActiveVersion:   active,
		CreatedAt:       formatPolicyTime(document.CreatedAt),
		UpdatedAt:       formatPolicyTime(document.UpdatedAt),
	}
}

func toPolicyVersionResponse(version policy.Version, includeSource bool) policyVersionResponse {
	response := policyVersionResponse{
		ID:              version.ID,
		DocumentID:      version.DocumentID,
		VersionNumber:   version.VersionNumber,
		SourceSHA256:    version.SourceSHA256,
		CompiledOK:      version.CompiledOK,
		CompileError:    version.CompileError,
		CreatedByUserID: version.CreatedByUserID,
		Comment:         version.Comment,
		CreatedAt:       formatPolicyTime(version.CreatedAt),
	}
	if includeSource {
		response.Source = version.RegoSource
	}
	return response
}

func toPolicyDecisionResponse(entry policy.Entry, includeSamples bool) policyDecisionResponse {
	response := policyDecisionResponse{
		ID:               entry.ID,
		Timestamp:        formatPolicyTime(entry.Timestamp),
		DecisionName:     string(entry.DecisionName),
		PolicyGeneration: entry.PolicyGeneration,
		UserID:           entry.UserID,
		ProfileID:        entry.ProfileID,
		SessionID:        entry.SessionID,
		RequestID:        entry.RequestID,
		NodeID:           entry.NodeID,
		Allowed:          entry.Allowed,
		EvalTimeNS:       entry.EvalTimeNS,
		InputDigest:      entry.InputDigest,
		Error:            entry.Error,
	}
	if includeSamples {
		response.InputSample = entry.InputSample
		response.ResultSample = entry.ResultSample
	}
	return response
}

func parsePolicyVersionRoute(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	documentID, ok := parsePolicyInt64Param(w, r, "id", "Invalid policy document ID")
	if !ok {
		return 0, 0, false
	}
	versionID, ok := parsePolicyInt64Param(w, r, "version", "Invalid policy version ID")
	if !ok {
		return 0, 0, false
	}
	return documentID, versionID, true
}

func parsePolicyInt64Param(w http.ResponseWriter, r *http.Request, key, message string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, key), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, policyErrorBadRequest, message)
		return 0, false
	}
	return id, true
}

func parseOptionalBoolQuery(r *http.Request, key string) (*bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, invalidQueryError(key)
	}
	return &value, nil
}

func createdByUserID(r *http.Request) *int {
	if userID := apimw.GetUserID(r.Context()); userID > 0 {
		return &userID
	}
	return nil
}

func sha256Hex(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func formatPolicyTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}
