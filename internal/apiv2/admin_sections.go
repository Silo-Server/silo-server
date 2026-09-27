package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

type AdminSectionService interface {
	ListAdminSections(context.Context, string, *int) ([]handlers.AdminSection, error)
	GetAdminSection(context.Context, string) (handlers.AdminSection, int64, error)
	AdminSectionOrder(context.Context, string, *int) (handlers.AdminSectionOrderView, error)
	CreateAdminSection(context.Context, handlers.AdminSectionCreate) (handlers.AdminSection, error)
	UpdateAdminSection(context.Context, string, handlers.AdminSectionUpdate) (handlers.AdminSection, error)
	DeleteAdminSection(context.Context, string) error
	ReorderAdminSections(context.Context, string, *int, []string) error
	RestoreAdminSections(context.Context, handlers.AdminSectionRestore) ([]handlers.AdminSection, error)
	BulkCreateAdminSections(context.Context, handlers.AdminSectionBulkCreate) (handlers.AdminSectionBulkResult, error)
	PreviewAdminSection(context.Context, handlers.AdminSectionPreviewRequest) (handlers.AdminSectionPreviewResult, error)
	AdminSectionCapabilities(context.Context) handlers.AdminSectionCapabilitiesView
}

func adminSectionOperation(method, path, id, summary string, guarded bool) Operation {
	op := Operation{Operation: humaOp(method, Prefix+path, id, "admin-sections", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: isMutatingMethod(method), Guarded: guarded}
	if method != http.MethodGet {
		op.RetrySafety = RetrySafetyNonRetryable
	}
	if method == http.MethodDelete {
		op.DefaultStatus = http.StatusNoContent
	}
	return op
}
func registerAdminSections(reg *Registry) {
	Register(reg, adminSectionOperation(http.MethodGet, "/admin/sections", "listAdminSections", "List all saved sections in one scope, including disabled definitions.", false), reg.listAdminSections)
	create := adminSectionOperation(http.MethodPost, "/admin/sections", "createAdminSection", "Create a section definition.", false)
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, reg.createAdminSection)
	Register(reg, adminSectionOperation(http.MethodGet, "/admin/sections/{id}", "getAdminSection", "Read a canonical section definition and its validator.", false), reg.getAdminSection)
	Register(reg, adminSectionOperation(http.MethodPatch, "/admin/sections/{id}", "updateAdminSection", "Update a section using its captured canonical validator.", true), reg.updateAdminSection)
	Register(reg, adminSectionOperation(http.MethodDelete, "/admin/sections/{id}", "deleteAdminSection", "Delete a section using its captured canonical validator.", true), reg.deleteAdminSection)
	Register(reg, adminSectionOperation(http.MethodGet, "/admin/sections/order", "getAdminSectionOrder", "Read the full saved section order for one scope.", false), reg.getAdminSectionOrder)
	Register(reg, adminSectionOperation(http.MethodPut, "/admin/sections/order", "reorderAdminSections", "Replace a scope's full order using the observed order validator.", true), reg.reorderAdminSections)
	Register(reg, adminSectionOperation(http.MethodPut, "/admin/sections/defaults", "restoreAdminSections", "Replace a scope with defaults using its captured order validator; optionally reset all profile overrides.", true), reg.restoreAdminSections)
	Register(reg, adminSectionOperation(http.MethodPost, "/admin/sections/bulk", "bulkCreateAdminSections", "Create one section in each selected library in one transaction.", false), reg.bulkCreateAdminSections)
	Register(reg, adminSectionOperation(http.MethodPost, "/admin/sections/preview", "previewAdminSection", "Preview a recipe without saving it.", false), reg.previewAdminSection)
	Register(reg, adminSectionOperation(http.MethodGet, "/admin/sections/capabilities", "getAdminSectionCapabilities", "Report preview and whole-provider profile reset support.", false), reg.getAdminSectionCapabilities)
}
func adminSectionTag(ctx context.Context, kind, id string, revision int64) EntityTag {
	return collectionEditorTag(ctx, "admin-section-"+kind, id, revision)
}
func adminSectionScopeKey(scope string, library *int) string {
	if library == nil {
		return scope
	}
	return scope + ":" + strconv.Itoa(*library)
}
func adminSectionError(err error) *Problem {
	switch {
	case errors.Is(err, sections.ErrSectionNotFound), errors.Is(err, catalogsvc.ErrLibraryCollectionNotFound):
		return NewProblem(TypeNotFound, "Section, library, or referenced collection not found.")
	case errors.Is(err, sections.ErrSectionOrderMismatch), errors.Is(err, sections.ErrInvalidSectionConfig):
		return NewProblem(TypeValidationFailed, "The section configuration or full scope order is invalid.")
	case errors.Is(err, sections.ErrSectionRevisionMismatch):
		return NewProblem(TypeConflict, "The section changed during the read; fetch its canonical state again.")
	}
	return collectionProblem(err)
}
func applyAdminSectionGuard(ctx context.Context, kind, id string, revision int64, match, none string) (context.Context, *Problem) {
	tag := adminSectionTag(ctx, kind, id, revision)
	if p := EvaluateGuardedPreconditions(match, none, tag); p != nil {
		return ctx, p
	}
	if match == "*" {
		revision = -1
	}
	return handlers.WithAdminSectionExpectedRevision(ctx, revision), nil
}
func (reg *Registry) listAdminSections(ctx context.Context, in *AdminSectionScopeInput) (*AdminSectionListOutput, error) {
	if reg.deps.AdminSections == nil {
		return nil, unavailable("admin sections")
	}
	library, p := adminSectionLibrary(in.Scope, in.LibraryID)
	if p != nil {
		return nil, p
	}
	values, err := reg.deps.AdminSections.ListAdminSections(ctx, in.Scope, library)
	if err != nil {
		return nil, adminSectionError(err)
	}
	body, p := adminSectionListOf(values)
	if p != nil {
		return nil, p
	}
	return &AdminSectionListOutput{Body: body}, nil
}
func (reg *Registry) getAdminSection(ctx context.Context, in *AdminSectionIDInput) (*AdminSectionOutput, error) {
	if reg.deps.AdminSections == nil {
		return nil, unavailable("admin sections")
	}
	value, revision, err := reg.deps.AdminSections.GetAdminSection(ctx, string(in.ID))
	if err != nil {
		return nil, adminSectionError(err)
	}
	body, p := adminSectionOf(value)
	if p != nil {
		return nil, p
	}
	return &AdminSectionOutput{ETag: adminSectionTag(ctx, "row", string(in.ID), revision).String(), Body: body}, nil
}
func (reg *Registry) createAdminSection(ctx context.Context, in *AdminSectionCreateInput) (*AdminSectionCreatedOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	if reg.deps.AdminSections == nil {
		return nil, unavailable("admin sections")
	}
	id := ID("")
	if in.Body.LibraryID != nil {
		id = *in.Body.LibraryID
	}
	library, p := adminSectionLibrary(in.Body.Scope, id)
	if p != nil {
		return nil, p
	}
	v, err := reg.deps.AdminSections.CreateAdminSection(ctx, handlers.AdminSectionCreate{Scope: in.Body.Scope, LibraryID: library, Position: in.Body.Position, SectionType: in.Body.SectionType, Title: in.Body.Title, Featured: in.Body.Featured, ItemLimit: in.Body.ItemLimit, Config: adminSectionConfig(in.Body.Config), Enabled: in.Body.Enabled})
	if err != nil {
		return nil, adminSectionError(err)
	}
	body, p := adminSectionOf(v)
	if p != nil {
		return nil, p
	}
	return &AdminSectionCreatedOutput{Location: Prefix + "/admin/sections/" + v.ID, Body: body}, nil
}
func (reg *Registry) adminSectionRowGuard(ctx context.Context, id, match, none string) (context.Context, *Problem) {
	if reg.deps.AdminSections == nil {
		return ctx, unavailable("admin sections")
	}
	_, revision, err := reg.deps.AdminSections.GetAdminSection(ctx, id)
	if err != nil {
		return ctx, adminSectionError(err)
	}
	return applyAdminSectionGuard(ctx, "row", id, revision, match, none)
}
func (reg *Registry) adminSectionRowMutationError(ctx context.Context, id ID, err error) error {
	if errors.Is(err, sections.ErrSectionRevisionMismatch) {
		current, readErr := reg.getAdminSection(ctx, &AdminSectionIDInput{ID: id})
		if readErr != nil {
			return readErr
		}
		tag, _ := ParseEntityTag(current.ETag)
		return StaleVersionProblem(tag)
	}
	return adminSectionError(err)
}
func (reg *Registry) updateAdminSection(ctx context.Context, in *AdminSectionUpdateInput) (*AdminSectionOutput, error) {
	guarded, p := reg.adminSectionRowGuard(ctx, string(in.ID), in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	cmd := handlers.AdminSectionUpdate{Position: in.Body.Position, Featured: in.Body.Featured, ItemLimit: in.Body.ItemLimit, Config: adminSectionConfig(in.Body.Config), Enabled: in.Body.Enabled}
	if in.Body.Title != nil {
		cmd.Title = *in.Body.Title
	}
	if in.Body.SectionType != nil {
		cmd.SectionType = *in.Body.SectionType
	}
	if _, err := reg.deps.AdminSections.UpdateAdminSection(guarded, string(in.ID), cmd); err != nil {
		return nil, reg.adminSectionRowMutationError(ctx, in.ID, err)
	}
	return reg.getAdminSection(ctx, &AdminSectionIDInput{ID: in.ID})
}
func (reg *Registry) deleteAdminSection(ctx context.Context, in *AdminSectionIDInput) (*struct{}, error) {
	guarded, p := reg.adminSectionRowGuard(ctx, string(in.ID), in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.AdminSections.DeleteAdminSection(guarded, string(in.ID)); err != nil {
		return nil, reg.adminSectionRowMutationError(ctx, in.ID, err)
	}
	return nil, nil
}
func (reg *Registry) getAdminSectionOrder(ctx context.Context, in *AdminSectionScopeInput) (*AdminSectionOrderOutput, error) {
	if reg.deps.AdminSections == nil {
		return nil, unavailable("admin sections")
	}
	library, p := adminSectionLibrary(in.Scope, in.LibraryID)
	if p != nil {
		return nil, p
	}
	view, err := reg.deps.AdminSections.AdminSectionOrder(ctx, in.Scope, library)
	if err != nil {
		return nil, adminSectionError(err)
	}
	return &AdminSectionOrderOutput{ETag: adminSectionTag(ctx, "order", adminSectionScopeKey(in.Scope, library), view.Revision).String(), Body: adminSectionOrderOf(view)}, nil
}
func (reg *Registry) adminSectionOrderGuard(ctx context.Context, in AdminSectionScopeInput, match, none string) (context.Context, *int, *Problem) {
	if reg.deps.AdminSections == nil {
		return ctx, nil, unavailable("admin sections")
	}
	library, p := adminSectionLibrary(in.Scope, in.LibraryID)
	if p != nil {
		return ctx, nil, p
	}
	view, err := reg.deps.AdminSections.AdminSectionOrder(ctx, in.Scope, library)
	if err != nil {
		return ctx, nil, adminSectionError(err)
	}
	guarded, p := applyAdminSectionGuard(ctx, "order", adminSectionScopeKey(in.Scope, library), view.Revision, match, none)
	return guarded, library, p
}
func (reg *Registry) adminSectionOrderMutationError(ctx context.Context, in AdminSectionScopeInput, err error) error {
	if errors.Is(err, sections.ErrSectionRevisionMismatch) {
		current, readErr := reg.getAdminSectionOrder(ctx, &in)
		if readErr != nil {
			return readErr
		}
		tag, _ := ParseEntityTag(current.ETag)
		return StaleVersionProblem(tag)
	}
	return adminSectionError(err)
}
func (reg *Registry) reorderAdminSections(ctx context.Context, in *AdminSectionOrderInput) (*AdminSectionOrderOutput, error) {
	guarded, library, p := reg.adminSectionOrderGuard(ctx, in.AdminSectionScopeInput, in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	if err := reg.deps.AdminSections.ReorderAdminSections(guarded, in.Scope, library, idsToStrings(in.Body.OrderedIDs)); err != nil {
		return nil, reg.adminSectionOrderMutationError(ctx, in.AdminSectionScopeInput, err)
	}
	return reg.getAdminSectionOrder(ctx, &in.AdminSectionScopeInput)
}
func (reg *Registry) restoreAdminSections(ctx context.Context, in *AdminSectionDefaultsInput) (*AdminSectionOrderOutput, error) {
	guarded, library, p := reg.adminSectionOrderGuard(ctx, in.AdminSectionScopeInput, in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	if in.Body.ResetProfiles && !reg.deps.AdminSections.AdminSectionCapabilities(ctx).ResetProfiles {
		return nil, NewProblem(TypeCapabilityUnsupported, "This store provider cannot reset all profile overrides atomically.")
	}
	_, err := reg.deps.AdminSections.RestoreAdminSections(guarded, handlers.AdminSectionRestore{Scope: in.Scope, LibraryID: library, ResetProfiles: in.Body.ResetProfiles})
	if err != nil {
		return nil, reg.adminSectionOrderMutationError(ctx, in.AdminSectionScopeInput, err)
	}
	return reg.getAdminSectionOrder(ctx, &in.AdminSectionScopeInput)
}
