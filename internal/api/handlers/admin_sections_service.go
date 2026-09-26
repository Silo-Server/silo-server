package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

const (
	adminSectionScopeHome    = "home"
	adminSectionScopeLibrary = "library"
	adminSectionPreviewTitle = "preview"
)

type AdminSection = sectionResponse
type AdminSectionCreate = createSectionRequest
type AdminSectionUpdate = updateSectionRequest
type AdminSectionRestore = restoreDefaultsRequest
type AdminSectionBulkCreate = bulkCreateSectionRequest
type AdminSectionBulkResult = bulkCreateSectionResponse
type AdminSectionPreviewRequest = previewRequest
type AdminSectionPreviewResult = previewResponse
type adminSectionRevisionKey struct{}

func WithAdminSectionExpectedRevision(ctx context.Context, revision int64) context.Context {
	return context.WithValue(ctx, adminSectionRevisionKey{}, revision)
}
func adminSectionExpectedRevision(ctx context.Context) *int64 {
	if v, ok := ctx.Value(adminSectionRevisionKey{}).(int64); ok {
		return new(v)
	}
	return nil
}

type AdminSectionOrderView struct {
	Scope      string
	LibraryID  *int
	OrderedIDs []string
	Revision   int64
}
type AdminSectionCapabilitiesView struct {
	ResetProfiles bool
	Preview       bool
}

func (h *SectionHandler) AdminSectionCapabilities(ctx context.Context) AdminSectionCapabilitiesView {
	return AdminSectionCapabilitiesView{ResetProfiles: h.repo != nil && h.canResetAllSectionProfileOverrides(), Preview: h.previewFetcher != nil || h.fetcher != nil}
}

func (h *SectionHandler) CreateAdminSection(ctx context.Context, req AdminSectionCreate) (AdminSection, error) {
	var none AdminSection
	if req.Title == "" || req.SectionType == "" {
		return none, apiError(http.StatusBadRequest, "bad_request", "Title and section_type are required")
	}

	if !sections.ValidSectionTypes[sections.SectionType(req.SectionType)] {
		return none, apiError(http.StatusBadRequest, "bad_request", "Invalid section_type")
	}
	if isTraktBackedSection(req.SectionType, req.Config) {
		return none, apiError(http.StatusBadRequest, "unsupported_source", "new Trakt-backed sections are not supported")
	}

	scope := req.Scope
	if scope == "" {
		scope = adminSectionScopeHome
	}

	if msg, ok := validateSectionScope(scope, req.LibraryID); !ok {
		return none, apiError(http.StatusBadRequest, "bad_request", msg)
	}

	if msg, ok := validateSectionConfig(sections.SectionType(req.SectionType), req.Config); !ok {
		return none, apiError(http.StatusBadRequest, "bad_request", msg)
	}

	sec := &sections.PageSection{
		Scope:       scope,
		LibraryID:   req.LibraryID,
		Position:    req.Position,
		SectionType: sections.SectionType(req.SectionType),
		Title:       req.Title,
		Featured:    req.Featured,
		ItemLimit:   req.ItemLimit,
		Config:      req.Config,
		Enabled:     req.Enabled,
	}
	if sec.Scope == "" {
		sec.Scope = adminSectionScopeHome
	}
	if sec.ItemLimit <= 0 {
		sec.ItemLimit = 20
	}

	created, err := h.repo.Create(ctx, sec)
	if err != nil {
		return none, err
	}

	return toSectionResponse(created), nil
}

func (h *SectionHandler) RestoreAdminSections(ctx context.Context, req AdminSectionRestore) ([]AdminSection, error) {
	var none []AdminSection
	if req.Scope == "" {
		req.Scope = adminSectionScopeHome
	}
	if req.Scope != adminSectionScopeHome && req.Scope != adminSectionScopeLibrary {
		return none, apiError(http.StatusBadRequest, "bad_request", "Scope must be 'home' or 'library'")
	}
	if req.Scope == adminSectionScopeLibrary && req.LibraryID == nil {
		return none, apiError(http.StatusBadRequest, "bad_request", "library_id is required for library scope")
	}
	if req.Scope == adminSectionScopeHome && req.LibraryID != nil {
		return none, apiError(http.StatusBadRequest, "bad_request", "library_id must not be set for home scope")
	}

	if req.ResetProfiles && !h.canResetAllSectionProfileOverrides() {
		return none, apiError(http.StatusNotImplemented, "capability_unsupported", "Resetting all profile section overrides is unavailable for this user store")
	}

	var defaults []*sections.PageSection
	var err error
	if req.Scope == adminSectionScopeHome {
		defaults, err = h.defaultHomeSections(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "loading default home sections", "component", "api", "error", err)
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to load libraries")
		}
	} else {
		defaults, err = h.defaultLibrarySections(ctx, *req.LibraryID)
		if err != nil {
			if errors.Is(err, catalog.ErrFolderNotFound) {
				return none, apiError(http.StatusNotFound, "not_found", "Library not found")
			}
			slog.ErrorContext(ctx, "loading default library sections", "component", "api", "library_id", *req.LibraryID, "error", err)
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to load library")
		}
	}

	var created []*sections.PageSection
	if expected := adminSectionExpectedRevision(ctx); expected != nil {
		created, err = h.repo.RestoreDefaultsIfRevision(ctx, req.Scope, req.LibraryID, defaults, *expected, req.ResetProfiles)
	} else {
		created, err = h.repo.RestoreDefaultsWithProfileReset(ctx, req.Scope, req.LibraryID, defaults, req.ResetProfiles)
	}
	if err != nil {
		slog.ErrorContext(ctx, "restoring default sections", "component", "api", "scope", req.Scope, "error", err)
		return none, err
	}

	resp := sectionListResponse{Sections: make([]sectionResponse, 0, len(created))}
	for _, s := range created {
		resp.Sections = append(resp.Sections, toSectionResponse(s))
	}
	return resp.Sections, nil
}

func (h *SectionBulkHandler) BulkCreateAdminSections(ctx context.Context, req AdminSectionBulkCreate) (AdminSectionBulkResult, error) {
	var none AdminSectionBulkResult
	scope := req.Scope
	if scope == "" {
		scope = adminSectionScopeHome
	}

	// Validate scope.
	switch scope {
	case adminSectionScopeHome, adminSectionScopeLibrary:
	default:
		return none, apiError(http.StatusBadRequest, "bad_request", "scope must be 'home' or 'library'")
	}

	// Library scope requires at least one library_id.
	if scope == adminSectionScopeLibrary && len(req.LibraryIDs) == 0 {
		return none, apiError(http.StatusBadRequest, "bad_request", "library_ids must not be empty for library scope")
	}

	// Validate section_type via recipe registry (catches unknown types and invalid config).
	if req.SectionType == "" {
		return none, apiError(http.StatusBadRequest, "bad_request", "section_type is required")
	}
	rec, ok := recipes.Get(req.SectionType)
	if !ok {
		return none, apiError(http.StatusBadRequest, "bad_request", "unknown section_type")
	}
	if isTraktBackedSection(req.SectionType, req.Config) {
		return none, apiError(http.StatusBadRequest, "unsupported_source", "new Trakt-backed sections are not supported")
	}
	if err := rec.Validate(req.Config); err != nil {
		return none, apiError(http.StatusBadRequest, "bad_request", err.Error())
	}

	itemLimit := req.ItemLimit
	if itemLimit <= 0 {
		itemLimit = 20
	}

	// Build the rows to insert.
	var rows []*sections.PageSection

	switch scope {
	case adminSectionScopeHome:
		rows = append(rows, &sections.PageSection{
			Scope:       adminSectionScopeHome,
			LibraryID:   nil,
			SectionType: sections.SectionType(req.SectionType),
			Title:       req.Title,
			Featured:    req.Featured,
			ItemLimit:   itemLimit,
			Config:      req.Config,
			Enabled:     req.Enabled,
		})
	case adminSectionScopeLibrary:
		for _, libID := range req.LibraryIDs {
			id := libID
			rows = append(rows, &sections.PageSection{
				Scope:       adminSectionScopeLibrary,
				LibraryID:   &id,
				SectionType: sections.SectionType(req.SectionType),
				Title:       req.Title,
				Featured:    req.Featured,
				ItemLimit:   itemLimit,
				Config:      req.Config,
				Enabled:     req.Enabled,
			})
		}
	}

	if err := h.Repo.CreateMany(ctx, rows); err != nil {
		return none, err
	}

	return bulkCreateSectionResponse{Created: len(rows)}, nil
}

func (h *SectionHandler) PreviewAdminSection(ctx context.Context, req AdminSectionPreviewRequest) (AdminSectionPreviewResult, error) {
	return h.previewAdminSection(ctx, req, AccessFilterFromContext(ctx, ""))
}
func (h *SectionHandler) previewAdminSection(ctx context.Context, req AdminSectionPreviewRequest, filter catalog.AccessFilter) (AdminSectionPreviewResult, error) {
	var none AdminSectionPreviewResult
	rec, ok := recipes.Get(req.SectionType)
	if !ok {
		return none, apiError(http.StatusBadRequest, "unknown_type", "section_type not registered")
	}
	if err := rec.Validate(req.Config); err != nil {
		return none, apiError(http.StatusBadRequest, "invalid_config", err.Error())
	}

	limit := req.ItemLimit
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	resolved := sections.ResolvedSection{
		SectionType: sections.SectionType(req.SectionType),
		Title:       adminSectionPreviewTitle,
		ItemLimit:   limit,
		Config:      req.Config,
	}

	fetcher := h.previewFetcher
	if fetcher == nil {
		fetcher = h.fetcher
	}
	if fetcher == nil {
		return none, apiError(http.StatusInternalServerError, "preview_unavailable", "section fetcher not configured")
	}

	userID := apimw.GetUserID(ctx)
	profileID := apimw.GetProfileID(ctx)

	result, err := fetcher.FetchOne(ctx, resolved, req.LibraryID, req.LibraryIDs, userID, profileID, filter)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "preview_failed", err.Error())
	}

	items := result.Items
	if items == nil {
		items = []*models.MediaItem{}
	}
	return previewResponse{Items: items, TotalCount: result.TotalCount}, nil
}

func (h *SectionHandler) BulkCreateAdminSections(ctx context.Context, req AdminSectionBulkCreate) (AdminSectionBulkResult, error) {
	return (&SectionBulkHandler{Repo: h.repo}).BulkCreateAdminSections(ctx, req)
}
func (h *SectionHandler) ListAdminSections(ctx context.Context, scope string, library *int) ([]AdminSection, error) {
	if scope == "" {
		scope = adminSectionScopeHome
	}
	if msg, ok := validateSectionScope(scope, library); !ok {
		return nil, apiError(400, "bad_request", msg)
	}
	rows, err := h.repo.ListByScopeAll(ctx, scope, library)
	if err != nil {
		return nil, err
	}
	out := make([]AdminSection, 0, len(rows))
	for _, row := range rows {
		out = append(out, toSectionResponse(row))
	}
	return out, nil
}
func (h *SectionHandler) adminSectionSnapshot(ctx context.Context, id string) (*sections.PageSection, int64, error) {
	before, err := h.repo.SectionRevision(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	row, err := h.repo.GetByID(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	after, err := h.repo.SectionRevision(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	if before != after {
		return nil, 0, sections.ErrSectionRevisionMismatch
	}
	return row, after, nil
}
func (h *SectionHandler) GetAdminSection(ctx context.Context, id string) (AdminSection, int64, error) {
	row, revision, err := h.adminSectionSnapshot(ctx, id)
	if err != nil {
		return AdminSection{}, 0, err
	}
	return toSectionResponse(row), revision, nil
}

func (h *SectionHandler) AdminSectionOrder(ctx context.Context, scope string, library *int) (AdminSectionOrderView, error) {
	var none AdminSectionOrderView
	if scope == "" {
		scope = adminSectionScopeHome
	}
	if msg, ok := validateSectionScope(scope, library); !ok {
		return none, apiError(400, "bad_request", msg)
	}
	before, err := h.repo.ScopeRevision(ctx, scope, library)
	if err != nil {
		return none, err
	}
	rows, err := h.repo.ListByScopeAll(ctx, scope, library)
	if err != nil {
		return none, err
	}
	after, err := h.repo.ScopeRevision(ctx, scope, library)
	if err != nil {
		return none, err
	}
	if before != after {
		return none, sections.ErrSectionRevisionMismatch
	}
	out := AdminSectionOrderView{Scope: scope, LibraryID: library, Revision: after, OrderedIDs: make([]string, 0, len(rows))}
	for _, row := range rows {
		out.OrderedIDs = append(out.OrderedIDs, row.ID)
	}
	return out, nil
}
func (h *SectionHandler) UpdateAdminSection(ctx context.Context, id string, req AdminSectionUpdate) (AdminSection, error) {
	var none AdminSection
	expected := adminSectionExpectedRevision(ctx)
	exact := expected != nil && *expected != -1
	for range 3 {
		existing, revision, err := h.adminSectionSnapshot(ctx, id)
		if errors.Is(err, sections.ErrSectionRevisionMismatch) && !exact {
			continue
		}
		if err != nil {
			return none, err
		}
		if exact && revision != *expected {
			return none, sections.ErrSectionRevisionMismatch
		}
		originalConfig := append(json.RawMessage(nil), existing.Config...)
		originalType := existing.SectionType
		originalEnabled := existing.Enabled
		wasTraktBacked := isTraktBackedSection(string(existing.SectionType), existing.Config)
		if req.Position != nil {
			existing.Position = *req.Position
		}
		if req.SectionType != "" {
			existing.SectionType = sections.SectionType(req.SectionType)
		}
		if req.Title != "" {
			existing.Title = req.Title
		}
		if req.Featured != nil {
			existing.Featured = *req.Featured
		}
		if req.ItemLimit != nil {
			existing.ItemLimit = *req.ItemLimit
		}
		if len(req.Config) > 0 {
			existing.Config = req.Config
		}
		if req.Enabled != nil {
			existing.Enabled = *req.Enabled
		}
		if !sections.ValidSectionTypes[existing.SectionType] {
			return none, apiError(400, "bad_request", "Invalid section_type")
		}
		if msg, ok := validateSectionScope(existing.Scope, existing.LibraryID); !ok {
			return none, apiError(400, "bad_request", msg)
		}
		if msg, ok := validateSectionConfig(existing.SectionType, existing.Config); !ok {
			return none, apiError(400, "bad_request", msg)
		}
		willBeTraktBacked := isTraktBackedSection(string(existing.SectionType), existing.Config)
		if !wasTraktBacked && willBeTraktBacked {
			return none, apiError(http.StatusBadRequest, "unsupported_source", "new Trakt-backed sections are not supported")
		}
		if wasTraktBacked && (existing.SectionType != originalType || !jsonConfigEqual(originalConfig, existing.Config) || (!originalEnabled && existing.Enabled)) {
			return none, apiError(http.StatusBadRequest, "legacy_source_immutable", "legacy Trakt section sources cannot be changed or reactivated")
		}

		// PATCH merges into the same version passed to SQL, including wildcard
		// requests. A wildcard may reread and remerge, never replay a stale full row.
		err = h.repo.UpdateIfRevision(ctx, existing, revision)
		if errors.Is(err, sections.ErrSectionRevisionMismatch) && !exact {
			continue
		}
		if err != nil {
			return none, err
		}
		updated, err := h.repo.GetByID(ctx, id)
		if err != nil {
			return none, err
		}
		return toSectionResponse(updated), nil
	}
	return none, apiError(http.StatusConflict, "conflict", "The section changed repeatedly; reload it before retrying")
}

func (h *SectionHandler) DeleteAdminSection(ctx context.Context, id string) error {
	existing, err := h.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	collectionID := strings.TrimSpace(sections.ParseCollectionConfig(existing.Config).LibraryCollectionID)
	if expected := adminSectionExpectedRevision(ctx); expected != nil {
		err = h.repo.DeleteIfRevision(ctx, id, *expected)
	} else {
		err = h.repo.Delete(ctx, id)
	}
	if err != nil {
		return err
	}
	h.deleteUnreferencedSectionManagedCollection(ctx, collectionID)
	return nil
}
func (h *SectionHandler) ReorderAdminSections(ctx context.Context, scope string, library *int, ids []string) error {
	if scope == "" {
		scope = adminSectionScopeHome
	}
	if msg, ok := validateSectionScope(scope, library); !ok {
		return apiError(400, "bad_request", msg)
	}
	expected := int64(-1)
	if v := adminSectionExpectedRevision(ctx); v != nil {
		expected = *v
	}
	return h.repo.ReorderScopeIfRevision(ctx, scope, library, ids, expected)
}
func (h *SectionHandler) reorderLegacySections(ctx context.Context, entries []sections.ReorderEntry) error {
	return h.repo.Reorder(ctx, entries)
}

func adminSectionServiceError(err error) error {
	if errors.Is(err, sections.ErrSectionNotFound) {
		return apiError(404, "not_found", "Section not found")
	}
	return err
}
