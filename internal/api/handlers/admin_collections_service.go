package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collections/templates"
	"github.com/Silo-Server/silo-server/internal/models"
	"golang.org/x/sync/errgroup"
)

const (
	adminCollectionBackdrop = "backdrop"
	adminCollectionTrakt    = "trakt"
	adminCollectionVisible  = "visible"
)

type AdminCollection = libraryCollectionResponse
type AdminCollectionsList = libraryCollectionsListResponse
type AdminCollectionCreate = createLibraryCollectionRequest
type AdminCollectionUpdate = updateLibraryCollectionRequest
type AdminCollectionPreview = previewLibraryCollectionResponse
type AdminCollectionPreviewRequest = previewCollectionRequest
type AdminCollectionImportMDBList = importMDBListRequest
type AdminCollectionImportTMDB = importTMDBRequest
type AdminCollectionImportTrakt = importTraktRequest
type AdminCollectionImportResult = importCollectionResponse
type AdminCollectionTemplateApply = applyTemplateBundleRequest
type AdminCollectionTemplateResult = applyTemplateBundleResponse

type adminCollectionRevisionKey struct{}

func WithAdminCollectionExpectedRevision(ctx context.Context, revision int64) context.Context {
	return context.WithValue(ctx, adminCollectionRevisionKey{}, revision)
}
func adminCollectionExpectedRevision(ctx context.Context) *int64 {
	if v, ok := ctx.Value(adminCollectionRevisionKey{}).(int64); ok {
		return new(v)
	}
	return nil
}

type adminCollectionArtwork func(string, string, string) error

func (h *LibraryCollectionHandler) adminArtworkSources(ctx context.Context) adminCollectionArtwork {
	return func(id, poster, backdrop string) error {
		for kind, url := range map[string]string{collectionImagePoster: poster, adminCollectionBackdrop: backdrop} {
			if strings.TrimSpace(url) != "" {
				if err := h.SetAdminCollectionArtworkSource(ctx, id, kind, url); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

func (h *LibraryCollectionHandler) ListAdminCollections(ctx context.Context, libraryID *int) (AdminCollectionsList, error) {
	var none AdminCollectionsList
	collectionsCh := make(chan []*models.LibraryCollection, 1)
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		collections, err := h.repo.ListAll(egCtx, libraryID, catalog.ListLibraryCollectionsOptions{IncludeHidden: true})
		if err != nil {
			return err
		}
		collectionsCh <- collections
		return nil
	})
	var groupsCh chan []models.LibraryCollectionGroup
	if libraryID != nil && h.GroupRepo != nil {
		scopedID := *libraryID
		groupsCh = make(chan []models.LibraryCollectionGroup, 1)
		eg.Go(func() error {
			groups, err := h.GroupRepo.ListByLibrary(egCtx, scopedID)
			if err != nil {
				return err
			}
			groupsCh <- groups
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to load collections")
	}
	collections := <-collectionsCh
	var groups []models.LibraryCollectionGroup
	if groupsCh != nil {
		groups = <-groupsCh
	}

	resp := libraryCollectionsListResponse{
		Collections: make([]libraryCollectionResponse, 0, len(collections)),
	}
	for _, collection := range collections {
		resp.Collections = append(resp.Collections, h.libraryCollectionResponseOf(ctx, collection))
	}
	if libraryID != nil {
		resp.Groups = toLibraryCollectionGroupResponses(groups)
	}
	return resp, nil
}

func (h *LibraryCollectionHandler) createAdminCollection(ctx context.Context, req AdminCollectionCreate, artwork adminCollectionArtwork) (AdminCollection, error) {
	var none AdminCollection
	if req.CollectionType == adminCollectionTrakt || isTraktCollectionSourceConfig(req.SourceConfig) {
		return none, apiError(http.StatusBadRequest, "unsupported_source", "new Trakt collections are not supported")
	}
	if !hasLibrarySelection(req.LibraryID, req.LibraryIDs) || strings.TrimSpace(req.Title) == "" {
		return none, apiError(http.StatusBadRequest, "bad_request", "library_id/library_ids and title are required")
	}
	if req.Slug == "" {
		req.Slug = slugifyCollectionName(req.Title)
	}
	if req.CollectionType == "" {
		req.CollectionType = collectionManagementModeManual
	}
	queryDefinition := defaultJSON(req.QueryDefinition)
	if req.CollectionType == collectionTypeSmart {
		var err error
		queryDefinition, err = normalizeSmartCollectionQueryDefinitionJSON(queryDefinition, false, false)
		if err != nil {
			return none, apiError(http.StatusBadRequest, "bad_request", "Invalid query_definition")
		}
	} else if len(req.QueryDefinition) > 0 {
		var err error
		queryDefinition, err = normalizeQueryDefinitionJSON(queryDefinition, false, false)
		if err != nil {
			return none, apiError(http.StatusBadRequest, "bad_request", "Invalid query_definition")
		}
	}

	// Library collections resolve with user scope stripped, so per-profile sort
	// fields are rejected here rather than failing at browse time.
	sortConfig, err := NormalizeCollectionSortConfig(req.SortConfig, false)
	if err != nil {
		return none, apiError(http.StatusBadRequest, "bad_request", err.Error())
	}

	var syncSchedule *string
	if s := strings.TrimSpace(req.SyncSchedule); s != "" {
		if err := catalog.ParseCronExpression(s); err != nil {
			return none, apiError(http.StatusBadRequest, "bad_request", err.Error())
		}
		syncSchedule = &s
	}
	managementMode, managementSource, managementKey, err := normalizeCollectionManagementFields(req.ManagementMode, req.ManagementSource, req.ManagementKey)
	if err != nil {
		return none, apiError(http.StatusBadRequest, "bad_request", err.Error())
	}

	collection, err := h.repo.Create(ctx, catalog.CreateLibraryCollectionInput{
		LibraryID:        req.LibraryID,
		LibraryIDs:       req.LibraryIDs,
		Slug:             req.Slug,
		Title:            req.Title,
		Description:      req.Description,
		CollectionType:   req.CollectionType,
		Visibility:       defaultCollectionVisibility(req.Visibility),
		SortOrder:        req.SortOrder,
		GroupID:          req.GroupID,
		Featured:         req.Featured,
		PosterURL:        req.PosterURL,
		BackdropURL:      req.BackdropURL,
		SourceURL:        req.SourceURL,
		QueryDefinition:  queryDefinition,
		SortConfig:       json.RawMessage(sortConfig),
		SourceConfig:     defaultCollectionSourceConfig(req.SourceConfig),
		ManagementMode:   managementMode,
		ManagementSource: managementSource,
		ManagementKey:    managementKey,
		SyncSchedule:     syncSchedule,
	})
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to create collection")
	}

	posterStored, _, _, err := h.storeBundledTemplatePoster(ctx, collection.ID, req.PosterURL, false)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to process template poster")
	}
	if err := artwork(collection.ID, req.PosterSourceURL, req.BackdropSourceURL); err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to process uploaded images")
	}
	if posterStored || artwork != nil || strings.TrimSpace(req.PosterSourceURL) != "" || strings.TrimSpace(req.BackdropSourceURL) != "" {
		collection, err = h.repo.GetByID(ctx, collection.ID)
		if err != nil {
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to load collection")
		}
	}
	h.refreshSmartCountAsync(collection.ID)

	return h.libraryCollectionResponseOf(ctx, collection), nil
}
func (h *LibraryCollectionHandler) CreateAdminCollection(ctx context.Context, req AdminCollectionCreate) (AdminCollection, error) {
	return h.createAdminCollection(ctx, req, h.adminArtworkSources(ctx))
}

func (h *LibraryCollectionHandler) updateAdminCollection(ctx context.Context, collectionID string, req AdminCollectionUpdate, artwork adminCollectionArtwork) (AdminCollection, error) {
	var none AdminCollection
	existing, err := h.repo.GetByID(ctx, collectionID)
	if err != nil {
		return none, adminCollectionLookupAPIError(err)
	}
	if err := validateAdminCollectionSourceUpdate(existing, req); err != nil {
		return none, err
	}
	queryDefinition := req.QueryDefinition
	if len(req.QueryDefinition) > 0 {
		var err error
		if req.CollectionType == nil || *req.CollectionType == collectionTypeSmart {
			queryDefinition, err = normalizeSmartCollectionQueryDefinitionJSON(req.QueryDefinition, false, false)
		} else {
			queryDefinition, err = normalizeQueryDefinitionJSON(req.QueryDefinition, false, false)
		}
		if err != nil {
			return none, apiError(http.StatusBadRequest, "bad_request", "Invalid query_definition")
		}
	}

	sortConfig := req.SortConfig
	if len(req.SortConfig) > 0 {
		normalized, err := NormalizeCollectionSortConfig(req.SortConfig, false)
		if err != nil {
			return none, apiError(http.StatusBadRequest, "bad_request", err.Error())
		}
		sortConfig = json.RawMessage(normalized)
	}

	// Validate sync_schedule if provided.
	if req.SyncSchedule != nil && *req.SyncSchedule != "" {
		if err := catalog.ParseCronExpression(*req.SyncSchedule); err != nil {
			return none, apiError(http.StatusBadRequest, "bad_request", err.Error())
		}
	}

	var setGroupID **string
	if req.GroupID.Set() {
		groupID := req.GroupID.Value()
		if groupID != nil && strings.TrimSpace(*groupID) == "" {
			groupID = nil
		}
		setGroupID = &groupID
	}

	if err := h.repo.Update(ctx, catalog.UpdateLibraryCollectionInput{
		ID:               collectionID,
		ExpectedRevision: adminCollectionExpectedRevision(ctx),
		LibraryIDs:       req.LibraryIDs,
		Slug:             req.Slug,
		Title:            req.Title,
		Description:      req.Description,
		CollectionType:   req.CollectionType,
		Visibility:       req.Visibility,
		SortOrder:        req.SortOrder,
		SetGroupID:       setGroupID,
		Featured:         req.Featured,
		PosterURL:        req.PosterURL,
		BackdropURL:      req.BackdropURL,
		SourceURL:        req.SourceURL,
		QueryDefinition:  queryDefinition,
		SortConfig:       sortConfig,
		SourceConfig:     req.SourceConfig,
		ManagementMode:   normalizeOptionalCollectionManagementMode(req.ManagementMode),
		ManagementSource: req.ManagementSource,
		ManagementKey:    req.ManagementKey,
		SyncSchedule:     req.SyncSchedule,
	}); err != nil {
		if errors.Is(err, catalog.ErrLibraryCollectionRevisionMismatch) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return none, err
		}
		if errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
			return none, apiError(http.StatusNotFound, "not_found", "Collection not found")
		}
		if errors.Is(err, catalog.ErrLibraryCollectionGroupNotFound) {
			return none, apiError(http.StatusNotFound, "not_found", "Collection group not found")
		}
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to update collection")
	}

	if err := artwork(collectionID, pointerStringValue(req.PosterSourceURL), pointerStringValue(req.BackdropSourceURL)); err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to process uploaded images")
	}

	updated, err := h.repo.GetByID(ctx, collectionID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to load collection")
	}
	if len(queryDefinition) > 0 || req.CollectionType != nil {
		h.refreshSmartCountAsync(collectionID)
	}
	return h.libraryCollectionResponseOf(ctx, updated), nil
}

func adminCollectionLookupAPIError(err error) error {
	if errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
		return apiError(http.StatusNotFound, "not_found", "Collection not found")
	}
	return apiError(http.StatusInternalServerError, "internal_error", "Failed to load collection")
}

func validateAdminCollectionSourceUpdate(existing *models.LibraryCollection, req AdminCollectionUpdate) error {
	proposedType := existing.CollectionType
	if req.CollectionType != nil {
		proposedType = *req.CollectionType
	}
	proposedConfig := existing.SourceConfig
	if len(req.SourceConfig) > 0 {
		proposedConfig = req.SourceConfig
	}
	proposedURL := existing.SourceURL
	if req.SourceURL != nil {
		proposedURL = *req.SourceURL
	}
	wasTrakt := existing.CollectionType == adminCollectionTrakt || isTraktCollectionSourceConfig(existing.SourceConfig)
	willBeTrakt := proposedType == adminCollectionTrakt || isTraktCollectionSourceConfig(proposedConfig)
	if !wasTrakt && willBeTrakt {
		return apiError(http.StatusBadRequest, "unsupported_source", "new Trakt collections are not supported")
	}
	if wasTrakt {
		activatesSchedule := req.SyncSchedule != nil && strings.TrimSpace(*req.SyncSchedule) != "" && (existing.SyncSchedule == nil || strings.TrimSpace(*existing.SyncSchedule) == "")
		changesLibraries := req.LibraryIDs != nil && !samePositiveIntSet(existing.LibraryIDs, *req.LibraryIDs)
		if proposedType != existing.CollectionType || !jsonConfigEqual(existing.SourceConfig, proposedConfig) || proposedURL != existing.SourceURL || activatesSchedule || changesLibraries {
			return apiError(http.StatusBadRequest, "legacy_source_immutable", "legacy Trakt collection sources cannot be changed or reactivated")
		}
	}
	return nil
}

func samePositiveIntSet(left, right []int) bool {
	left = uniquePositiveInts(left)
	right = uniquePositiveInts(right)
	if len(left) != len(right) {
		return false
	}
	rightSet := make(map[int]struct{}, len(right))
	for _, id := range right {
		rightSet[id] = struct{}{}
	}
	for _, id := range left {
		if _, ok := rightSet[id]; !ok {
			return false
		}
	}
	return true
}
func (h *LibraryCollectionHandler) UpdateAdminCollection(ctx context.Context, collectionID string, req AdminCollectionUpdate) (AdminCollection, error) {
	if req.PosterSourceURL != nil || req.BackdropSourceURL != nil {
		return AdminCollection{}, apiError(400, "bad_request", "Use the artwork endpoint for artwork sources")
	}
	return h.updateAdminCollection(ctx, collectionID, req, func(string, string, string) error { return nil })
}

func (h *LibraryCollectionHandler) PreviewAdminCollection(ctx context.Context, req AdminCollectionPreviewRequest) (AdminCollectionPreview, error) {
	var none AdminCollectionPreview
	if h.Executor == nil {
		return none, apiError(503, "service_unavailable", "Preview is not configured")
	}
	var def catalog.QueryDefinition
	if len(req.QueryDefinition) > 0 {
		normalized, err := normalizeQueryDefinitionJSON(req.QueryDefinition, false, false)
		if err != nil {
			return none, apiError(http.StatusBadRequest, "bad_request", "Invalid query_definition")
		}
		if err := json.Unmarshal(normalized, &def); err != nil {
			return none, apiError(http.StatusBadRequest, "bad_request", "Invalid query_definition")
		}
	}

	items, total, err := h.Executor.Preview(ctx, def, catalog.AccessFilter{}, req.Limit)
	if err != nil {
		return none, apiError(http.StatusBadRequest, "bad_request", err.Error())
	}

	resp := previewLibraryCollectionResponse{Items: make([]itemListResponse, 0, len(items)), Total: total}
	for _, item := range items {
		resp.Items = append(resp.Items, h.itemListResponseOf(ctx, item))
	}
	return resp, nil
}

func (h *LibraryCollectionHandler) importAdminMDBList(ctx context.Context, req AdminCollectionImportMDBList, artwork adminCollectionArtwork) (AdminCollectionImportResult, error) {
	var none AdminCollectionImportResult
	if !hasLibrarySelection(req.LibraryID, req.LibraryIDs) || strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.URL) == "" {
		return none, apiError(http.StatusBadRequest, "bad_request", "library_id/library_ids, title, and url are required")
	}
	if req.Limit != nil && *req.Limit <= 0 {
		return none, apiError(http.StatusBadRequest, "bad_request", "limit must be greater than 0")
	}

	collection, err := h.createMDBListCollection(ctx, req)
	if err != nil {
		if validationErr, ok := errors.AsType[requestValidationError](err); ok {
			return none, apiError(http.StatusBadRequest, "bad_request", validationErr.Error())
		}
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to create collection")
	}

	// Process admin artwork before sync so maybeGenerateCollage sees the
	// uploaded poster and skips collage generation.
	if err := artwork(collection.ID, req.PosterSourceURL, req.BackdropSourceURL); err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to process uploaded images")
	}

	run, err := h.service.SyncCollection(ctx, collection.ID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to import MDBList collection")
	}

	refreshed, err := h.repo.GetByID(ctx, collection.ID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to load collection")
	}

	return importCollectionResponse{
		Collection: h.libraryCollectionResponseOf(ctx, refreshed),
		SyncRun:    run,
	}, nil
}
func (h *LibraryCollectionHandler) ImportAdminMDBList(ctx context.Context, req AdminCollectionImportMDBList) (AdminCollectionImportResult, error) {
	return h.importAdminMDBList(ctx, req, h.adminArtworkSources(ctx))
}

func (h *LibraryCollectionHandler) importAdminTMDB(ctx context.Context, req AdminCollectionImportTMDB, artwork adminCollectionArtwork) (AdminCollectionImportResult, error) {
	var none AdminCollectionImportResult
	if !hasLibrarySelection(req.LibraryID, req.LibraryIDs) || strings.TrimSpace(req.Title) == "" {
		return none, apiError(http.StatusBadRequest, "bad_request", "library_id/library_ids and title are required")
	}
	if _, _, _, err := normalizeTMDBPresetRequest(req.Preset, req.MediaType, req.TimeWindow); err != nil {
		return none, apiError(http.StatusBadRequest, "bad_request", err.Error())
	}
	if req.Limit != nil && *req.Limit <= 0 {
		return none, apiError(http.StatusBadRequest, "bad_request", "limit must be greater than 0")
	}

	collection, err := h.createTMDBCollection(ctx, req)
	if err != nil {
		if validationErr, ok := errors.AsType[requestValidationError](err); ok {
			return none, apiError(http.StatusBadRequest, "bad_request", validationErr.Error())
		}
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to create collection")
	}

	// Process admin artwork before sync so maybeGenerateCollage sees the
	// uploaded poster and skips collage generation.
	if err := artwork(collection.ID, req.PosterSourceURL, req.BackdropSourceURL); err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to process uploaded images")
	}

	run, err := h.service.SyncCollection(ctx, collection.ID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to sync TMDB collection")
	}

	refreshed, err := h.repo.GetByID(ctx, collection.ID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to load collection")
	}

	return importCollectionResponse{
		Collection: h.libraryCollectionResponseOf(ctx, refreshed),
		SyncRun:    run,
	}, nil
}
func (h *LibraryCollectionHandler) ImportAdminTMDB(ctx context.Context, req AdminCollectionImportTMDB) (AdminCollectionImportResult, error) {
	return h.importAdminTMDB(ctx, req, h.adminArtworkSources(ctx))
}

func (h *LibraryCollectionHandler) importAdminTrakt(ctx context.Context, req AdminCollectionImportTrakt, artwork adminCollectionArtwork) (AdminCollectionImportResult, error) {
	var none AdminCollectionImportResult
	return none, apiError(http.StatusGone, "unsupported_source", "new Trakt collections are not supported")
}
func (h *LibraryCollectionHandler) ImportAdminTrakt(ctx context.Context, req AdminCollectionImportTrakt) (AdminCollectionImportResult, error) {
	return h.importAdminTrakt(ctx, req, h.adminArtworkSources(ctx))
}

func (h *LibraryCollectionHandler) GetAdminCollection(ctx context.Context, id string) (AdminCollection, int64, error) {
	var none AdminCollection
	before, err := h.repo.CollectionRevision(ctx, id)
	if err != nil {
		return none, 0, err
	}
	c, err := h.repo.GetByID(ctx, id)
	if err != nil {
		return none, 0, err
	}
	after, err := h.repo.CollectionRevision(ctx, id)
	if err != nil {
		return none, 0, err
	}
	if before != after {
		return none, 0, catalog.ErrLibraryCollectionRevisionMismatch
	}
	// Canonical editor bodies must not contain expiring artwork signatures.
	copy := *c
	copy.PosterURL = ""
	copy.BackdropURL = ""
	return h.libraryCollectionResponseOf(ctx, &copy), after, nil
}
func (h *LibraryCollectionHandler) SyncAdminCollection(ctx context.Context, id string) (*models.LibraryCollectionSyncRun, error) {
	if h.service == nil {
		return nil, apiError(503, "service_unavailable", "Collection sync is not configured")
	}
	return h.service.SyncCollection(ctx, id)
}
func (h *LibraryCollectionHandler) ListAdminCollectionTemplates(ctx context.Context) (templates.BundleCatalog, error) {
	return h.templateRegistry().BundleCatalog(), nil
}
func (h *LibraryCollectionHandler) ApplyAdminCollectionTemplate(ctx context.Context, id string, req AdminCollectionTemplateApply) (AdminCollectionTemplateResult, error) {
	result, err := h.applyTemplateBundle(ctx, id, req, nil)
	if e, ok := errors.AsType[templateBundleApplyError](err); ok {
		return result, apiError(e.status, e.code, e.message)
	}
	return result, err
}
func (h *LibraryCollectionHandler) QueueAdminCollectionTemplate(ctx context.Context, id string, req AdminCollectionTemplateApply, actingUserID int) (*models.AdminJob, error) {
	if h.JobRepo == nil {
		return nil, apiError(503, "service_unavailable", "Admin job queue is not configured")
	}
	if req.DryRun {
		return nil, apiError(400, "bad_request", "dry_run is not supported for background apply jobs")
	}
	if _, ok := h.templateRegistry().GetBundle(id); !ok {
		return nil, apiError(404, "not_found", "Template bundle not found")
	}
	ids := uniquePositiveInts(req.LibraryIDs)
	if len(ids) == 0 {
		return nil, apiError(400, "bad_request", "library_ids is required")
	}
	job, err := h.JobRepo.Create(ctx, adminjob.CreateJobInput{JobType: adminjob.JobTypeTemplateBundleApply, CreatedByUserID: actingUserID, RequestPayload: adminjob.TemplateBundleApplyRequest{BundleID: id, LibraryIDs: ids, DeleteExisting: req.DeleteExisting, Featured: toAdminJobTemplateBundleFeatured(req.Featured)}, Message: "Queued collection defaults apply"})
	if err != nil {
		return nil, err
	}
	publishEventJob(ctx, h.EventsHub, "job.created", job)
	return job, nil
}
func (h *LibraryCollectionHandler) UploadAdminCollectionArtwork(ctx context.Context, id, kind string, data []byte) error {
	if kind != collectionImagePoster && kind != adminCollectionBackdrop {
		return apiError(400, "bad_request", "Artwork kind must be poster or backdrop")
	}
	if len(data) == 0 || len(data) > collectionImageMaxBytes {
		return apiError(400, "bad_request", "Artwork must be nonempty and at most 10 MiB")
	}
	if _, err := h.repo.GetByID(ctx, id); err != nil {
		return err
	}
	path, hash, err := h.processCollectionImage(ctx, id, kind, data)
	if err != nil {
		return err
	}
	input := catalog.UpdateLibraryCollectionInput{ID: id}
	if kind == collectionImagePoster {
		input.PosterURL = &path
		input.PosterThumbhash = &hash
		input.PosterAutoGenerated = new(false)
		input.PosterFromTemplate = new(false)
	} else {
		input.BackdropURL = &path
		input.BackdropThumbhash = &hash
	}
	return h.repo.Update(ctx, input)
}
func (h *LibraryCollectionHandler) SetAdminCollectionArtworkSource(ctx context.Context, id, kind, url string) error {
	if _, err := h.repo.GetByID(ctx, id); err != nil {
		return err
	}
	data, err := downloadCollectionImageURL(ctx, h.httpClient, url)
	if err != nil {
		return err
	}
	return h.UploadAdminCollectionArtwork(ctx, id, kind, data)
}
func (h *LibraryCollectionHandler) DeleteAdminCollectionArtwork(ctx context.Context, id, kind string) error {
	if kind != collectionImagePoster && kind != adminCollectionBackdrop {
		return apiError(400, "bad_request", "Artwork kind must be poster or backdrop")
	}
	if _, err := h.repo.GetByID(ctx, id); err != nil {
		return err
	}
	if err := h.deleteCollectionImages(ctx, id, kind); err != nil {
		return err
	}
	input := catalog.UpdateLibraryCollectionInput{ID: id}
	if kind == collectionImagePoster {
		input.PosterURL = new("")
		input.PosterThumbhash = new("")
		input.PosterAutoGenerated = new(false)
		input.PosterFromTemplate = new(false)
	} else {
		input.BackdropURL = new("")
		input.BackdropThumbhash = new("")
	}
	if err := h.repo.Update(ctx, input); err != nil {
		return err
	}
	if kind == collectionImagePoster && h.service != nil && h.service.CollageGen != nil {
		if err := h.GenerateCollectionPoster(ctx, id); err != nil {
			slog.DebugContext(ctx, "Collection poster regeneration unavailable", "error", err)
		}
	}
	return nil
}
func (h *LibraryCollectionHandler) adminManualCollection(ctx context.Context, id string) (*models.LibraryCollection, error) {
	c, err := h.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.CollectionType != collectionManagementModeManual {
		return nil, apiError(409, "collection_not_manual", "Only manual collections support manual items")
	}
	return c, nil
}
func (h *LibraryCollectionHandler) AddAdminCollectionItem(ctx context.Context, id, itemID string, position int) error {
	c, err := h.adminManualCollection(ctx, id)
	if err != nil {
		return err
	}
	ids := c.LibraryIDs
	if len(ids) == 0 {
		ids = []int{c.LibraryID}
	}
	items, err := h.itemRepo.GetByIDsWithAccess(ctx, []string{itemID}, catalog.AccessFilter{AllowedLibraryIDs: ids})
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return apiError(404, "not_found", "Item not found in collection libraries")
	}
	return h.repo.AddItemIfAbsent(ctx, id, itemID, position)
}
func (h *LibraryCollectionHandler) RemoveAdminCollectionItem(ctx context.Context, id, itemID string) error {
	if _, err := h.adminManualCollection(ctx, id); err != nil {
		return err
	}
	return h.repo.RemoveManualItem(ctx, id, itemID)
}
func (h *LibraryCollectionHandler) ReorderAdminCollectionItems(ctx context.Context, id string, ids []string) error {
	if _, err := h.adminManualCollection(ctx, id); err != nil {
		return err
	}
	if revision := adminCollectionExpectedRevision(ctx); revision != nil {
		return h.repo.ReorderItemsIfRevision(ctx, id, ids, *revision)
	}
	return h.repo.ReorderItems(ctx, id, ids)
}
func (h *LibraryCollectionHandler) DeleteAdminCollection(ctx context.Context, id string) error {
	revision := adminCollectionExpectedRevision(ctx)
	if revision == nil {
		return h.deleteServerCollection(ctx, id)
	}
	if err := h.repo.DeleteIfRevision(ctx, id, *revision); err != nil {
		return err
	}
	h.cleanupDeletedCollection(ctx, id)
	return nil
}
