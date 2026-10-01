package handlers

import (
	"context"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/policy"
)

// AdminPolicyReady preserves the editor gate independently of decision-log access.
func (h *PolicyHandler) AdminPolicyReady(editor, storage, decisions bool) error {
	if editor && !h.editorEnabled() {
		return apiError(http.StatusForbidden, "policy_editor_disabled", "Policy editor is disabled")
	}
	if h == nil || (storage && h.store == nil) || (decisions && h.decisions == nil) {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Policy storage is unavailable")
	}
	return nil
}
func (h *PolicyHandler) ListAdminPolicyDocuments(ctx context.Context, after int64, limit int) ([]policy.Document, error) {
	return h.store.ListDocumentsPage(ctx, after, limit)
}
func (h *PolicyHandler) GetAdminPolicyDocument(ctx context.Context, id int64) (policy.DocumentSnapshot, error) {
	return h.store.GetDocumentSnapshot(ctx, id)
}
func (h *PolicyHandler) CreateAdminPolicyDocument(ctx context.Context, domain, name string) (policy.Document, error) {
	return h.store.CreateDocument(ctx, domain, name)
}
func (h *PolicyHandler) ListAdminPolicyVersions(ctx context.Context, id int64, before, limit int) ([]policy.Version, error) {
	if _, err := h.store.GetDocument(ctx, id); err != nil {
		return nil, err
	}
	return h.store.ListVersionsPage(ctx, id, before, limit)
}
func (h *PolicyHandler) GetAdminPolicyVersion(ctx context.Context, id, version int64) (policy.Version, error) {
	return h.store.GetVersion(ctx, id, version)
}
func (h *PolicyHandler) CreateAdminPolicyVersion(ctx context.Context, id int64, source, comment string) (policy.Version, error) {
	doc, err := h.store.GetDocument(ctx, id)
	if err != nil {
		return policy.Version{}, err
	}
	compileErr := policy.CompileCheck(ctx, doc.Domain, source)
	var diagnostic *string
	if compileErr != nil {
		diagnostic = new(compileErr.Error())
	}
	var author *int
	if id := apimw.GetUserID(ctx); id > 0 {
		author = new(id)
	}
	return h.store.CreateVersion(ctx, id, source, sha256Hex(source), compileErr == nil, diagnostic, author, comment)
}
func (h *PolicyHandler) ActivateAdminPolicyVersion(ctx context.Context, id, version, expected int64) (policy.DocumentApplyResult, error) {
	return policy.NewDocumentService(h.store, h.system).Activate(ctx, id, version, expected)
}
func (h *PolicyHandler) SetAdminPolicyEnabled(ctx context.Context, id int64, enabled bool, expected int64) (policy.DocumentApplyResult, error) {
	return policy.NewDocumentService(h.store, h.system).SetEnabled(ctx, id, enabled, expected)
}
func (h *PolicyHandler) DeleteAdminPolicyDocument(ctx context.Context, id, expected int64) error {
	return h.store.DeleteDocumentIfRevision(ctx, id, expected)
}
func (h *PolicyHandler) SimulateAdminPolicy(ctx context.Context, in policy.SimulateRequest) (policy.SimulateResult, error) {
	return policy.Simulate(ctx, h.store, in)
}
func (h *PolicyHandler) ListAdminPolicyDecisions(ctx context.Context, in policy.ListOptions) (policy.ListResult, error) {
	return h.decisions.List(ctx, in)
}
func (h *PolicyHandler) GetAdminPolicyDecision(ctx context.Context, id int64) (policy.Entry, error) {
	return h.decisions.Get(ctx, id, nil)
}
