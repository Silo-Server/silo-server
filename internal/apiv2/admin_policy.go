package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/policy"
)

type AdminPolicyService interface {
	AdminPolicyReady(editor, storage, decisions bool) error
	ListAdminPolicyDocuments(context.Context, int64, int) ([]policy.Document, error)
	GetAdminPolicyDocument(context.Context, int64) (policy.DocumentSnapshot, error)
	CreateAdminPolicyDocument(context.Context, string, string) (policy.Document, error)
	ListAdminPolicyVersions(context.Context, int64, int, int) ([]policy.Version, error)
	GetAdminPolicyVersion(context.Context, int64, int64) (policy.Version, error)
	CreateAdminPolicyVersion(context.Context, int64, string, string) (policy.Version, error)
	ActivateAdminPolicyVersion(context.Context, int64, int64, int64) (policy.DocumentApplyResult, error)
	SetAdminPolicyEnabled(context.Context, int64, bool, int64) (policy.DocumentApplyResult, error)
	DeleteAdminPolicyDocument(context.Context, int64, int64) error
	SimulateAdminPolicy(context.Context, policy.SimulateRequest) (policy.SimulateResult, error)
	ListAdminPolicyDecisions(context.Context, policy.ListOptions) (policy.ListResult, error)
	GetAdminPolicyDecision(context.Context, int64) (policy.Entry, error)
}

func adminPolicyOperation(method, path, id, summary string, guarded bool) Operation {
	op := Operation{Operation: humaOp(method, Prefix+"/admin/policy"+path, id, "admin-policy", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: isMutatingMethod(method), Guarded: guarded}
	op.MaxBodyBytes = 1 << 20
	if method != http.MethodGet {
		op.RetrySafety = RetrySafetyNonRetryable
	}
	if method == http.MethodDelete {
		op.DefaultStatus = http.StatusNoContent
	}
	return op
}
func registerAdminPolicy(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, adminPolicyOperation(http.MethodGet, "/vendor", "listAdminPolicyVendor", "Read embedded vendor policy sources.", false), reg.listAdminPolicyVendor)
	Register(reg, adminPolicyOperation(http.MethodGet, "/documents", "listAdminPolicyDocuments", "List saved policy documents.", false), func(ctx context.Context, in *AdminPolicyDocumentsInput) (*AdminPolicyDocumentsOutput, error) {
		return reg.listAdminPolicyDocuments(ctx, cursors, in)
	})
	create := adminPolicyOperation(http.MethodPost, "/documents", "createAdminPolicyDocument", "Create a policy document without an active version.", false)
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, reg.createAdminPolicyDocument)
	Register(reg, adminPolicyOperation(http.MethodGet, "/documents/{id}", "getAdminPolicyDocument", "Read a canonical document, active source, and document validator.", false), reg.getAdminPolicyDocument)
	Register(reg, adminPolicyOperation(http.MethodDelete, "/documents/{id}", "deleteAdminPolicyDocument", "Delete an inactive document using its captured validator.", true), reg.deleteAdminPolicyDocument)
	Register(reg, adminPolicyOperation(http.MethodPatch, "/documents/{id}", "setAdminPolicyEnabled", "Set enabled state and report persisted and local application outcomes.", true), reg.setAdminPolicyEnabled)
	Register(reg, adminPolicyOperation(http.MethodGet, "/documents/{id}/versions", "listAdminPolicyVersions", "List immutable version metadata.", false), func(ctx context.Context, in *AdminPolicyVersionsInput) (*AdminPolicyVersionsOutput, error) {
		return reg.listAdminPolicyVersions(ctx, cursors, in)
	})
	version := adminPolicyOperation(http.MethodPost, "/documents/{id}/versions", "createAdminPolicyVersion", "Save an immutable draft, including drafts that fail compilation.", false)
	version.DefaultStatus = http.StatusCreated
	Register(reg, version, reg.createAdminPolicyVersion)
	Register(reg, adminPolicyOperation(http.MethodGet, "/documents/{id}/versions/{version}", "getAdminPolicyVersion", "Read an immutable version by its opaque ID, including source.", false), reg.getAdminPolicyVersion)
	Register(reg, adminPolicyOperation(http.MethodPut, "/documents/{id}/active-version", "activateAdminPolicyVersion", "Set the active version using the captured document validator.", true), reg.activateAdminPolicyVersion)
	Register(reg, adminPolicyOperation(http.MethodPost, "/validate", "validateAdminPolicy", "Compile policy source without persisting it.", false), reg.validateAdminPolicy)
	Register(reg, adminPolicyOperation(http.MethodPost, "/simulate", "simulateAdminPolicy", "Evaluate policy input without changing the running policy.", false), reg.simulateAdminPolicy)
	Register(reg, adminPolicyOperation(http.MethodGet, "/decisions", "listAdminPolicyDecisions", "List policy decisions with cursor pagination.", false), func(ctx context.Context, in *AdminPolicyDecisionsInput) (*AdminPolicyDecisionsOutput, error) {
		return reg.listAdminPolicyDecisions(ctx, cursors, in)
	})
	Register(reg, adminPolicyOperation(http.MethodGet, "/decisions/{id}", "getAdminPolicyDecision", "Read one policy decision and retained samples.", false), reg.getAdminPolicyDecision)
}
func (reg *Registry) adminPolicyReady(editor, storage, decisions bool) *Problem {
	if reg.deps.AdminPolicy == nil {
		return unavailable("admin policy")
	}
	if err := reg.deps.AdminPolicy.AdminPolicyReady(editor, storage, decisions); err != nil {
		return collectionProblem(err)
	}
	return nil
}
func adminPolicyID(id ID) (int64, *Problem) {
	n, err := strconv.ParseInt(string(id), 10, 64)
	if err != nil || n <= 0 || policyID(n) != id {
		return 0, NewProblem(TypeValidationFailed, "Expected a positive policy identifier.")
	}
	return n, nil
}
func adminPolicyError(err error) *Problem {
	switch {
	case errors.Is(err, policy.ErrDocumentNotFound), errors.Is(err, policy.ErrVersionNotFound), errors.Is(err, policy.ErrDecisionNotFound):
		return NewProblem(TypeNotFound, "Policy resource not found.")
	case errors.Is(err, policy.ErrDomainAlreadyEnabled):
		return NewProblem(TypeConflict, "Another document is enabled for this policy domain.")
	case errors.Is(err, policy.ErrDocumentHasActiveVersion):
		return NewProblem(TypeConflict, "A document with an active version cannot be deleted.")
	case errors.Is(err, policy.ErrVersionNotCompiled), errors.Is(err, policy.ErrCompileFailed), errors.Is(err, policy.ErrPolicySlowEval), errors.Is(err, policy.ErrPolicyEvalFailed), errors.Is(err, policy.ErrUnsupportedDomain), errors.Is(err, policy.ErrUnknownDecision):
		return NewProblem(TypeValidationFailed, "Policy source or evaluation failed validation.")
	}
	return collectionProblem(err)
}
func adminPolicyTag(ctx context.Context, id ID, revision int64) EntityTag {
	return collectionEditorTag(ctx, "admin-policy-document", string(id), revision)
}
func (reg *Registry) adminPolicyGuard(ctx context.Context, in AdminPolicyIDInput) (int64, int64, *Problem) {
	if p := reg.adminPolicyReady(true, true, false); p != nil {
		return 0, 0, p
	}
	id, p := adminPolicyID(in.ID)
	if p != nil {
		return 0, 0, p
	}
	snapshot, err := reg.deps.AdminPolicy.GetAdminPolicyDocument(ctx, id)
	if err != nil {
		return 0, 0, adminPolicyError(err)
	}
	revision := snapshot.Document.Revision
	if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, adminPolicyTag(ctx, in.ID, revision)); p != nil {
		return 0, 0, p
	}
	if strings.TrimSpace(in.IfMatch) == "*" {
		revision = policy.AnyDocumentRevision
	}
	return id, revision, nil
}
func adminPolicyMutationError(ctx context.Context, id ID, err error) *Problem {
	if mismatch, ok := errors.AsType[*policy.DocumentRevisionMismatchError](err); ok {
		return StaleVersionProblem(adminPolicyTag(ctx, id, mismatch.Actual))
	}
	return adminPolicyError(err)
}
func (reg *Registry) listAdminPolicyVendor(ctx context.Context, _ *struct{}) (*AdminPolicyVendorOutput, error) {
	if p := reg.adminPolicyReady(true, false, false); p != nil {
		return nil, p
	}
	out := NewCollection([]AdminPolicyVendor{})
	modules, err := policy.VendorModules()
	if err != nil {
		return nil, adminPolicyError(err)
	}
	for _, v := range modules {
		out.Items = append(out.Items, AdminPolicyVendor{Path: v.Path, Source: v.Source})
	}
	return &AdminPolicyVendorOutput{Body: out}, nil
}
func (reg *Registry) listAdminPolicyDocuments(ctx context.Context, cursors *Cursors, in *AdminPolicyDocumentsInput) (*AdminPolicyDocumentsOutput, error) {
	if p := reg.adminPolicyReady(true, true, false); p != nil {
		return nil, p
	}
	scope := adminPolicyListScope(ctx, "listAdminPolicyDocuments", "", "id", "id")
	var after int64
	if in.Cursor != "" {
		if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
			return nil, p
		}
		if after <= 0 {
			return nil, NewProblem(TypeInvalidCursor, "The cursor position is invalid.")
		}
	}
	values, err := reg.deps.AdminPolicy.ListAdminPolicyDocuments(ctx, after, in.Limit+1)
	if err != nil {
		return nil, adminPolicyError(err)
	}
	next := ""
	if len(values) > in.Limit {
		values = values[:in.Limit]
		next, err = cursors.Encode(scope, values[len(values)-1].ID)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
		}
	}
	items := make([]AdminPolicyDocument, 0, len(values))
	for _, v := range values {
		items = append(items, adminPolicyDocumentOf(v))
	}
	return &AdminPolicyDocumentsOutput{Body: Paginated(items, next)}, nil
}
func adminPolicyListScope(ctx context.Context, operation, filter, sort, tiebreaker string) CursorScope {
	return CursorScope{OperationID: operation, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: filter, Sort: sort, Tiebreaker: tiebreaker}
}
func (reg *Registry) getAdminPolicyDocument(ctx context.Context, in *AdminPolicyIDInput) (*AdminPolicyDocumentOutput, error) {
	if p := reg.adminPolicyReady(true, true, false); p != nil {
		return nil, p
	}
	id, p := adminPolicyID(in.ID)
	if p != nil {
		return nil, p
	}
	v, err := reg.deps.AdminPolicy.GetAdminPolicyDocument(ctx, id)
	if err != nil {
		return nil, adminPolicyError(err)
	}
	body := AdminPolicySnapshot{AdminPolicyDocument: adminPolicyDocumentOf(v.Document)}
	if v.ActiveVersion != nil {
		body.ActiveVersion = new(adminPolicyVersionOf(*v.ActiveVersion, true))
	}
	return &AdminPolicyDocumentOutput{ETag: adminPolicyTag(ctx, in.ID, v.Document.Revision).String(), Body: body}, nil
}
func (reg *Registry) createAdminPolicyDocument(ctx context.Context, in *AdminPolicyCreateInput) (*AdminPolicyCreatedOutput, error) {
	if p := reg.adminPolicyReady(true, true, false); p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	name := strings.TrimSpace(in.Body.Name)
	if name == "" {
		return nil, NewProblem(TypeValidationFailed, "Name is required.")
	}
	v, err := reg.deps.AdminPolicy.CreateAdminPolicyDocument(ctx, in.Body.Domain, name)
	if err != nil {
		return nil, adminPolicyError(err)
	}
	return &AdminPolicyCreatedOutput{ETag: adminPolicyTag(ctx, policyID(v.ID), v.Revision).String(), Location: Prefix + "/admin/policy/documents/" + string(policyID(v.ID)), Body: adminPolicyDocumentOf(v)}, nil
}
func (reg *Registry) deleteAdminPolicyDocument(ctx context.Context, in *AdminPolicyIDInput) (*struct{}, error) {
	id, revision, p := reg.adminPolicyGuard(ctx, *in)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.AdminPolicy.DeleteAdminPolicyDocument(ctx, id, revision); err != nil {
		return nil, adminPolicyMutationError(ctx, in.ID, err)
	}
	return nil, nil
}
func (reg *Registry) listAdminPolicyVersions(ctx context.Context, cursors *Cursors, in *AdminPolicyVersionsInput) (*AdminPolicyVersionsOutput, error) {
	if p := reg.adminPolicyReady(true, true, false); p != nil {
		return nil, p
	}
	id, p := adminPolicyID(in.ID)
	if p != nil {
		return nil, p
	}
	scope := adminPolicyListScope(ctx, "listAdminPolicyVersions", string(in.ID), "-version_number", "version_number")
	var before int
	if in.Cursor != "" {
		if p := cursors.Decode(scope, in.Cursor, &before); p != nil {
			return nil, p
		}
		if before <= 0 {
			return nil, NewProblem(TypeInvalidCursor, "The cursor position is invalid.")
		}
	}
	values, err := reg.deps.AdminPolicy.ListAdminPolicyVersions(ctx, id, before, in.Limit+1)
	if err != nil {
		return nil, adminPolicyError(err)
	}
	next := ""
	if len(values) > in.Limit {
		values = values[:in.Limit]
		next, err = cursors.Encode(scope, values[len(values)-1].VersionNumber)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
		}
	}
	items := make([]AdminPolicyVersion, 0, len(values))
	for _, v := range values {
		items = append(items, adminPolicyVersionOf(v, false))
	}
	return &AdminPolicyVersionsOutput{Body: Paginated(items, next)}, nil
}
func (reg *Registry) getAdminPolicyVersion(ctx context.Context, in *AdminPolicyVersionInput) (*AdminPolicyVersionOutput, error) {
	if p := reg.adminPolicyReady(true, true, false); p != nil {
		return nil, p
	}
	id, p := adminPolicyID(in.ID)
	if p != nil {
		return nil, p
	}
	version, p := adminPolicyID(in.Version)
	if p != nil {
		return nil, p
	}
	v, err := reg.deps.AdminPolicy.GetAdminPolicyVersion(ctx, id, version)
	if err != nil {
		return nil, adminPolicyError(err)
	}
	return &AdminPolicyVersionOutput{Body: adminPolicyVersionOf(v, true)}, nil
}
func (reg *Registry) createAdminPolicyVersion(ctx context.Context, in *AdminPolicyVersionCreateInput) (*AdminPolicyVersionCreatedOutput, error) {
	if p := reg.adminPolicyReady(true, true, false); p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	id, p := adminPolicyID(in.ID)
	if p != nil {
		return nil, p
	}
	v, err := reg.deps.AdminPolicy.CreateAdminPolicyVersion(ctx, id, in.Body.Source, in.Body.Comment)
	if err != nil {
		return nil, adminPolicyError(err)
	}
	return &AdminPolicyVersionCreatedOutput{Location: Prefix + "/admin/policy/documents/" + string(in.ID) + "/versions/" + string(policyID(v.ID)), Body: adminPolicyVersionOf(v, true)}, nil
}
func adminPolicyApplyOf(ctx context.Context, v policy.DocumentApplyResult) *AdminPolicyApplyOutput {
	return &AdminPolicyApplyOutput{ETag: adminPolicyTag(ctx, policyID(v.Document.ID), v.Document.Revision).String(), Body: AdminPolicyApplyResult{Persisted: v.Persisted, Document: adminPolicyDocumentOf(v.Document), PersistedGeneration: v.Generation, Application: AdminPolicyApplication{LocalApplied: v.Application.LocalReloadErr == nil, LoadedGeneration: v.Application.Generation, PublicationFailed: v.Application.PublishErr != nil}}}
}
func (reg *Registry) setAdminPolicyEnabled(ctx context.Context, in *AdminPolicyEnabledInput) (*AdminPolicyApplyOutput, error) {
	id, revision, p := reg.adminPolicyGuard(ctx, AdminPolicyIDInput{ID: in.ID, IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	v, err := reg.deps.AdminPolicy.SetAdminPolicyEnabled(ctx, id, in.Body.Enabled, revision)
	if err != nil {
		return nil, adminPolicyMutationError(ctx, in.ID, err)
	}
	return adminPolicyApplyOf(ctx, v), nil
}
func (reg *Registry) activateAdminPolicyVersion(ctx context.Context, in *AdminPolicyActivateInput) (*AdminPolicyApplyOutput, error) {
	id, revision, p := reg.adminPolicyGuard(ctx, AdminPolicyIDInput{ID: in.ID, IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	version, p := adminPolicyID(in.Body.VersionID)
	if p != nil {
		return nil, p
	}
	v, err := reg.deps.AdminPolicy.ActivateAdminPolicyVersion(ctx, id, version, revision)
	if err != nil {
		return nil, adminPolicyMutationError(ctx, in.ID, err)
	}
	return adminPolicyApplyOf(ctx, v), nil
}
func (reg *Registry) validateAdminPolicy(ctx context.Context, in *AdminPolicyValidateInput) (*AdminPolicyValidateOutput, error) {
	if p := reg.adminPolicyReady(true, false, false); p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	body := AdminPolicyValidation{CompiledOK: true, Errors: []policy.CompileIssue{}}
	if err := policy.CompileCheck(ctx, in.Body.Domain, in.Body.Source); err != nil {
		body.CompiledOK = false
		if ce, ok := errors.AsType[*policy.CompileError](err); ok {
			body.Errors = NonNil(ce.Issues)
		} else {
			body.Errors = []policy.CompileIssue{{Message: err.Error()}}
		}
	}
	return &AdminPolicyValidateOutput{Body: body}, nil
}
func (reg *Registry) simulateAdminPolicy(ctx context.Context, in *AdminPolicySimulateInput) (*AdminPolicySimulateOutput, error) {
	if p := reg.adminPolicyReady(true, false, false); p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, map[string]bool{"input": true}); p != nil {
		return nil, p
	}
	v, err := reg.deps.AdminPolicy.SimulateAdminPolicy(ctx, policy.SimulateRequest{Domain: in.Body.Domain, Source: in.Body.Source, Input: json.RawMessage(in.Body.Input)})
	if err != nil {
		return nil, adminPolicyError(err)
	}
	return &AdminPolicySimulateOutput{Body: AdminPolicySimulationResult{Decision: PolicyJSON(v.Decision), EvalTimeNS: v.EvalTimeNS, Generation: v.Generation}}, nil
}
func (reg *Registry) listAdminPolicyDecisions(ctx context.Context, cursors *Cursors, in *AdminPolicyDecisionsInput) (*AdminPolicyDecisionsOutput, error) {
	if p := reg.adminPolicyReady(false, false, true); p != nil {
		return nil, p
	}
	opts := policy.ListOptions{DecisionName: strings.TrimSpace(in.DecisionName), Limit: in.Limit}
	if in.UserID != "" {
		id, p := adminPolicyID(in.UserID)
		if p != nil {
			return nil, p
		}
		opts.UserID = new(int(id))
	}
	if in.Allowed != "" {
		allowed, err := strconv.ParseBool(in.Allowed)
		if err != nil {
			return nil, NewProblem(TypeValidationFailed, "allowed must be a boolean.")
		}
		opts.Allowed = &allowed
	}
	for _, v := range []struct {
		raw    string
		target **time.Time
	}{{in.From, &opts.From}, {in.To, &opts.To}} {
		if v.raw != "" {
			t, err := time.Parse(time.RFC3339Nano, v.raw)
			if err != nil || t.IsZero() {
				return nil, NewProblem(TypeValidationFailed, "Expected a nonzero RFC 3339 instant.")
			}
			*v.target = &t
		}
	}
	if opts.From != nil && opts.To != nil && opts.From.After(*opts.To) {
		return nil, NewProblem(TypeValidationFailed, "from must not be after to.")
	}
	filter, _ := json.Marshal(struct {
		Name     string
		User     *int
		Allowed  *bool
		From, To *time.Time
	}{opts.DecisionName, opts.UserID, opts.Allowed, opts.From, opts.To})
	scope := CursorScope{OperationID: "listAdminPolicyDecisions", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: string(filter), Sort: "-timestamp,-id", Tiebreaker: "id"}
	if in.Cursor != "" {
		if p := cursors.Decode(scope, in.Cursor, &opts.Cursor); p != nil {
			return nil, p
		}
		if opts.Cursor == "" {
			return nil, NewProblem(TypeInvalidCursor, "The cursor position is invalid.")
		}
	}
	result, err := reg.deps.AdminPolicy.ListAdminPolicyDecisions(ctx, opts)
	if err != nil {
		if strings.Contains(err.Error(), "cursor") {
			return nil, NewProblem(TypeInvalidCursor, "Invalid policy decision cursor.")
		}
		return nil, adminPolicyError(err)
	}
	items := make([]AdminPolicyDecision, 0, len(result.Entries))
	for _, v := range result.Entries {
		items = append(items, adminPolicyDecisionOf(v, false))
	}
	next := ""
	if result.NextCursor != "" {
		next, err = cursors.Encode(scope, result.NextCursor)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
		}
	}
	return &AdminPolicyDecisionsOutput{Body: Paginated(items, next)}, nil
}
func (reg *Registry) getAdminPolicyDecision(ctx context.Context, in *AdminPolicyIDInput) (*AdminPolicyDecisionOutput, error) {
	if p := reg.adminPolicyReady(false, false, true); p != nil {
		return nil, p
	}
	id, p := adminPolicyID(in.ID)
	if p != nil {
		return nil, p
	}
	v, err := reg.deps.AdminPolicy.GetAdminPolicyDecision(ctx, id)
	if err != nil {
		return nil, adminPolicyError(err)
	}
	return &AdminPolicyDecisionOutput{Body: adminPolicyDecisionOf(v, true)}, nil
}
