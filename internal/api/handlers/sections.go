package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SectionHandler handles section management and batch section endpoints.
type SectionHandler struct {
	repo           *sections.Repository
	fetcher        *sections.Fetcher
	previewFetcher sectionPreviewFetcher // set to fetcher at construction; separate for test injection
	episodeFetcher sectionEpisodeFetcher
	FolderRepo     *catalog.FolderRepository
	EpisodeRepo    *catalog.EpisodeRepository
	StoreProvider  userstore.UserStoreProvider
	UserRepo       *auth.UserRepository
	DetailSvc      *catalog.DetailService
	Settings       catalog.SettingsStore
	CollectionRepo *catalog.LibraryCollectionRepository
	EbookProgress  EbookReaderProgressLister
}

// NewSectionHandler creates a new SectionHandler.
func NewSectionHandler(repo *sections.Repository, fetcher *sections.Fetcher) *SectionHandler {
	return &SectionHandler{repo: repo, fetcher: fetcher, previewFetcher: fetcher, episodeFetcher: fetcher}
}

type sectionEpisodeFetcher interface {
	FetchEpisodesByContentIDs(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error)
}

func (h *SectionHandler) defaultHomeSections(ctx context.Context) ([]*sections.PageSection, error) {
	if h.FolderRepo == nil {
		return sections.DefaultHomeSections(nil), nil
	}

	libraries, err := h.FolderRepo.List(ctx)
	if err != nil {
		return nil, err
	}
	return sections.DefaultHomeSections(libraries), nil
}

func (h *SectionHandler) defaultLibrarySections(ctx context.Context, libraryID int) ([]*sections.PageSection, error) {
	if h.FolderRepo == nil {
		return libraryDefaultSections(nil, libraryID), nil
	}

	folder, err := h.FolderRepo.GetByID(ctx, libraryID)
	if err != nil {
		return nil, err
	}

	return libraryDefaultSections(folder, libraryID), nil
}

func libraryDefaultSections(folder *models.MediaFolder, libraryID int) []*sections.PageSection {
	if folder == nil {
		return sections.DefaultLibrarySections(&libraryID)
	}
	return sections.DefaultLibrarySectionsForType(&libraryID, folder.Type)
}

// --- Request/Response types ---

type createSectionRequest struct {
	Scope       string          `json:"scope"`
	LibraryID   *int            `json:"library_id"`
	Position    int             `json:"position"`
	SectionType string          `json:"section_type"`
	Title       string          `json:"title"`
	Featured    bool            `json:"featured"`
	ItemLimit   int             `json:"item_limit"`
	Config      json.RawMessage `json:"config"`
	Enabled     bool            `json:"enabled"`
}

type updateSectionRequest struct {
	Position    *int            `json:"position"`
	SectionType string          `json:"section_type,omitempty"`
	Title       string          `json:"title,omitempty"`
	Featured    *bool           `json:"featured"`
	ItemLimit   *int            `json:"item_limit"`
	Config      json.RawMessage `json:"config,omitempty"`
	Enabled     *bool           `json:"enabled"`
}

type reorderSectionsRequest struct {
	Entries []sections.ReorderEntry `json:"entries"`
}

type restoreDefaultsRequest struct {
	Scope         string `json:"scope"`
	LibraryID     *int   `json:"library_id"`
	ResetProfiles bool   `json:"reset_profiles"`
}

type sectionResponse struct {
	ID          string          `json:"id"`
	Scope       string          `json:"scope"`
	LibraryID   *int            `json:"library_id"`
	Position    int             `json:"position"`
	SectionType string          `json:"section_type"`
	Title       string          `json:"title"`
	Featured    bool            `json:"featured"`
	ItemLimit   int             `json:"item_limit"`
	Config      json.RawMessage `json:"config"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type sectionListResponse struct {
	Sections []sectionResponse `json:"sections"`
}

func toSectionResponse(s *sections.PageSection) sectionResponse {
	return sectionResponse{
		ID:          s.ID,
		Scope:       s.Scope,
		LibraryID:   s.LibraryID,
		Position:    s.Position,
		SectionType: string(s.SectionType),
		Title:       s.Title,
		Featured:    s.Featured,
		ItemLimit:   s.ItemLimit,
		Config:      s.Config,
		Enabled:     s.Enabled,
		CreatedAt:   s.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   s.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

// validateSectionConfig checks config requirements for the given section type.
func validateSectionConfig(sectionType sections.SectionType, config json.RawMessage) (string, bool) {
	if sectionType == sections.SectionCollection {
		collectionConfig := sections.ParseCollectionConfig(config)
		if strings.TrimSpace(collectionConfig.LibraryCollectionID) == "" {
			return "library_collection_id is required for collection sections", false
		}
	}

	cfg := sections.ParseConfigFilters(config)
	if cfg.FilterType != "" && cfg.FilterType != "movie" && cfg.FilterType != "series" {
		return "filter_type must be 'movie' or 'series'", false
	}

	var rawLibraryConfig struct {
		FilterLibraryID  *int  `json:"filter_library_id"`
		FilterLibraryIDs []int `json:"filter_library_ids"`
	}
	if len(config) > 0 {
		_ = json.Unmarshal(config, &rawLibraryConfig)
	}
	if rawLibraryConfig.FilterLibraryID != nil && *rawLibraryConfig.FilterLibraryID <= 0 {
		return "filter_library_id must be a positive library ID", false
	}
	for _, id := range rawLibraryConfig.FilterLibraryIDs {
		if id <= 0 {
			return "filter_library_ids must contain positive library IDs", false
		}
	}
	if sectionType == sections.SectionCustomFilter || sectionType == sections.SectionGenre || sectionType == sections.SectionRandom || len(config) > 0 {
		if _, err := sections.ParseQueryDefinition(config); err != nil {
			return err.Error(), false
		}
	}
	return "", true
}

func validateSectionScope(scope string, libraryID *int) (string, bool) {
	switch scope {
	case "", "home":
		if libraryID != nil {
			return "library_id must not be set for home sections", false
		}
	case "library":
		if libraryID == nil {
			return "library_id is required for library sections", false
		}
	default:
		return "Invalid scope", false
	}

	return "", true
}

// --- Admin CRUD endpoints ---

// HandleListSections handles GET /admin/sections?scope=home&library_id=123
func (h *SectionHandler) HandleListSections(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "home"
	}

	var libraryID *int
	if lid := r.URL.Query().Get("library_id"); lid != "" {
		v, err := strconv.Atoi(lid)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid library_id")
			return
		}
		libraryID = &v
	}

	list, err := h.repo.ListByScopeAll(r.Context(), scope, libraryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list sections")
		return
	}

	resp := sectionListResponse{Sections: make([]sectionResponse, 0, len(list))}
	for _, s := range list {
		resp.Sections = append(resp.Sections, toSectionResponse(s))
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleCreateSection handles POST /admin/sections
func (h *SectionHandler) HandleCreateSection(w http.ResponseWriter, r *http.Request) {
	var req createSectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Title == "" || req.SectionType == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Title and section_type are required")
		return
	}

	if !sections.ValidSectionTypes[sections.SectionType(req.SectionType)] {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid section_type")
		return
	}

	scope := req.Scope
	if scope == "" {
		scope = "home"
	}

	if msg, ok := validateSectionScope(scope, req.LibraryID); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	if msg, ok := validateSectionConfig(sections.SectionType(req.SectionType), req.Config); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
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
		sec.Scope = "home"
	}
	if sec.ItemLimit <= 0 {
		sec.ItemLimit = 20
	}

	created, err := h.repo.Create(r.Context(), sec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create section")
		return
	}

	writeJSON(w, http.StatusCreated, toSectionResponse(created))
}

// HandleUpdateSection handles PUT /admin/sections/{id}
func (h *SectionHandler) HandleUpdateSection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Section ID is required")
		return
	}

	existing, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Section not found")
		return
	}

	var req updateSectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

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

	if msg, ok := validateSectionScope(existing.Scope, existing.LibraryID); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	if msg, ok := validateSectionConfig(existing.SectionType, existing.Config); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	if err := h.repo.Update(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update section")
		return
	}

	updated, _ := h.repo.GetByID(r.Context(), id)
	writeJSON(w, http.StatusOK, toSectionResponse(updated))
}

// HandleDeleteSection handles DELETE /admin/sections/{id}
func (h *SectionHandler) HandleDeleteSection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Section ID is required")
		return
	}

	existing, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Section not found")
		return
	}
	collectionID := strings.TrimSpace(sections.ParseCollectionConfig(existing.Config).LibraryCollectionID)

	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Section not found")
		return
	}
	h.deleteUnreferencedSectionManagedCollection(r.Context(), collectionID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *SectionHandler) deleteUnreferencedSectionManagedCollection(ctx context.Context, collectionID string) {
	if collectionID == "" || h.CollectionRepo == nil {
		return
	}
	collection, err := h.CollectionRepo.GetByID(ctx, collectionID)
	if err != nil {
		if !errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
			slog.Warn("failed to load section-managed collection during section delete", "collection_id", collectionID, "error", err)
		}
		return
	}
	if collection.ManagementMode != "section" {
		return
	}
	refs, err := h.repo.CountLibraryCollectionReferences(ctx, collectionID, "")
	if err != nil {
		slog.Warn("failed to count section-managed collection references", "collection_id", collectionID, "error", err)
		return
	}
	if refs > 0 {
		return
	}
	if err := h.CollectionRepo.Delete(ctx, collectionID); err != nil && !errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
		slog.Warn("failed to delete unreferenced section-managed collection", "collection_id", collectionID, "error", err)
	}
}

// HandleReorderSections handles PUT /admin/sections/reorder
func (h *SectionHandler) HandleReorderSections(w http.ResponseWriter, r *http.Request) {
	var req reorderSectionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if err := h.repo.Reorder(r.Context(), req.Entries); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to reorder sections")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Batch section endpoints ---

type upcomingEventResponse struct {
	Type          string   `json:"type"`
	AirDate       string   `json:"air_date"`
	AirTime       *string  `json:"air_time,omitempty"`
	EpisodeTitle  *string  `json:"episode_title,omitempty"`
	SeasonNumber  *int     `json:"season_number,omitempty"`
	EpisodeNumber *int     `json:"episode_number,omitempty"`
	Badges        []string `json:"badges"`
}

type sectionItemResponse struct {
	ContentID         string                 `json:"content_id"`
	Type              string                 `json:"type"`
	Title             string                 `json:"title"`
	SeriesID          string                 `json:"series_id,omitempty"`
	SeriesTitle       string                 `json:"series_title,omitempty"`
	SeasonNumber      *int                   `json:"season_number,omitempty"`
	EpisodeNumber     *int                   `json:"episode_number,omitempty"`
	Year              int                    `json:"year,omitempty"`
	Runtime           int                    `json:"runtime,omitempty"`
	Genres            []string               `json:"genres"`
	Keywords          []string               `json:"keywords"`
	Studios           []string               `json:"studios,omitempty"`
	Networks          []string               `json:"networks,omitempty"`
	ContentRating     string                 `json:"content_rating,omitempty"`
	Status            string                 `json:"status"`
	ShowStatus        string                 `json:"show_status,omitempty"`
	RatingIMDB        *float64               `json:"rating_imdb,omitempty"`
	RatingTMDB        *float64               `json:"rating_tmdb,omitempty"`
	RatingRTCritic    *int                   `json:"rating_rt_critic,omitempty"`
	RatingRTAudience  *int                   `json:"rating_rt_audience,omitempty"`
	OriginalLanguage  string                 `json:"original_language,omitempty"`
	Overview          string                 `json:"overview,omitempty"`
	PositionSeconds   *float64               `json:"position_seconds,omitempty"`
	DurationSeconds   *float64               `json:"duration_seconds,omitempty"`
	ProgressUpdatedAt *string                `json:"progress_updated_at,omitempty"`
	PosterURL         string                 `json:"poster_url,omitempty"`
	PosterThumbhash   string                 `json:"poster_thumbhash,omitempty"`
	BackdropURL       string                 `json:"backdrop_url,omitempty"`
	BackdropThumbhash string                 `json:"backdrop_thumbhash,omitempty"`
	LogoURL           string                 `json:"logo_url,omitempty"`
	OverlaySummary    *models.OverlaySummary `json:"overlay_summary,omitempty"`
	Badges            []string               `json:"badges,omitempty"`
	ItemSource        string                 `json:"item_source,omitempty"`
	UserState         *itemUserStateResponse `json:"user_state,omitempty"`
	UpcomingEvent     *upcomingEventResponse `json:"upcoming_event,omitempty"`
}

type resolvedSectionResponse struct {
	ID          string                `json:"id"`
	SectionType string                `json:"section_type"`
	Title       string                `json:"title"`
	Featured    bool                  `json:"featured"`
	ItemLimit   int                   `json:"item_limit"`
	TotalCount  int                   `json:"total_count"`
	IsCustom    bool                  `json:"is_custom"`
	Customized  bool                  `json:"customized"`
	Items       []sectionItemResponse `json:"items"`
}

type homeSectionsResponse struct {
	Sections []resolvedSectionResponse `json:"sections"`
}

type resolvedSectionLayoutResponse struct {
	ID          string `json:"id"`
	SectionType string `json:"section_type"`
	Title       string `json:"title"`
	Featured    bool   `json:"featured"`
	ItemLimit   int    `json:"item_limit"`
	IsCustom    bool   `json:"is_custom"`
	Customized  bool   `json:"customized"`
}

type homeLayoutResponse struct {
	Sections []resolvedSectionLayoutResponse `json:"sections"`
}

type homeSectionItemsResponse struct {
	Section resolvedSectionResponse `json:"section"`
}

// HandleHomeLayout handles GET /home/layout
func (h *SectionHandler) HandleHomeLayout(w http.ResponseWriter, r *http.Request) {
	resolved, _, _, _, err := h.loadResolvedHomeSections(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load sections")
		return
	}

	userID := apimw.GetUserID(r.Context())
	resolved = h.maybeInjectNextUp(r.Context(), resolved, userID)

	resp := homeLayoutResponse{
		Sections: make([]resolvedSectionLayoutResponse, 0, len(resolved)),
	}
	for _, s := range resolved {
		resp.Sections = append(resp.Sections, resolvedSectionLayoutResponse{
			ID:          s.ID,
			SectionType: string(s.SectionType),
			Title:       s.Title,
			Featured:    s.Featured,
			ItemLimit:   s.ItemLimit,
			IsCustom:    s.IsCustom,
			Customized:  s.Customized,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleLibraryLayout handles GET /library/{id}/layout
func (h *SectionHandler) HandleLibraryLayout(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	libraryID, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}

	resolved, _, _, err := h.loadResolvedLibrarySections(r, libraryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load sections")
		return
	}

	resp := homeLayoutResponse{
		Sections: make([]resolvedSectionLayoutResponse, 0, len(resolved)),
	}
	for _, s := range resolved {
		resp.Sections = append(resp.Sections, resolvedSectionLayoutResponse{
			ID:          s.ID,
			SectionType: string(s.SectionType),
			Title:       s.Title,
			Featured:    s.Featured,
			ItemLimit:   s.ItemLimit,
			IsCustom:    s.IsCustom,
			Customized:  s.Customized,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleHomeSections handles GET /home/sections
func (h *SectionHandler) HandleHomeSections(w http.ResponseWriter, r *http.Request) {
	resolved, libraryIDs, accessFilter, profileID, err := h.loadResolvedHomeSections(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load sections")
		return
	}

	userID := apimw.GetUserID(r.Context())
	resolved = h.maybeInjectNextUp(r.Context(), resolved, userID)
	withItems := h.fetcher.FetchAll(r.Context(), resolved, nil, libraryIDs, userID, profileID, accessFilter)
	withItems = applyDiversityFilter(withItems)
	withItems = dropEmptySeasonalSections(withItems)
	writeJSON(w, http.StatusOK, h.buildSectionsResponse(r, withItems))
}

// HandleHomeSectionItems handles GET /home/sections/{id}/items
func (h *SectionHandler) HandleHomeSectionItems(w http.ResponseWriter, r *http.Request) {
	sectionID := chi.URLParam(r, "id")
	if sectionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Section ID is required")
		return
	}

	resolved, libraryIDs, accessFilter, profileID, err := h.loadResolvedHomeSections(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load sections")
		return
	}
	userID := apimw.GetUserID(r.Context())
	resolved = h.maybeInjectNextUp(r.Context(), resolved, userID)

	for _, s := range resolved {
		if s.ID != sectionID {
			continue
		}

		withItems, fetchErr := h.fetcher.FetchOne(r.Context(), s, nil, libraryIDs, userID, profileID, accessFilter)
		if fetchErr != nil {
			slog.Error("fetching section items", "section_id", s.ID, "type", s.SectionType, "error", fetchErr)
			withItems = sections.SectionWithItems{
				ResolvedSection: s,
				Items:           []*models.MediaItem{},
			}
		}

		resp := h.buildSectionsResponse(r, []sections.SectionWithItems{withItems})
		if len(resp.Sections) == 0 {
			resp.Sections = append(resp.Sections, resolvedSectionResponse{
				ID:          withItems.ID,
				SectionType: string(withItems.SectionType),
				Title:       withItems.Title,
				Featured:    withItems.Featured,
				ItemLimit:   withItems.ItemLimit,
				TotalCount:  withItems.TotalCount,
				IsCustom:    withItems.IsCustom,
				Customized:  withItems.Customized,
				Items:       []sectionItemResponse{},
			})
		}

		writeJSON(w, http.StatusOK, homeSectionItemsResponse{
			Section: resp.Sections[0],
		})
		return
	}

	writeError(w, http.StatusNotFound, "not_found", "Section not found")
}

// HandleLibrarySections handles GET /library/{id}/sections
func (h *SectionHandler) HandleLibrarySections(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	libraryID, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}

	resolved, accessFilter, profileID, err := h.loadResolvedLibrarySections(r, libraryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load sections")
		return
	}

	userID := apimw.GetUserID(r.Context())
	withItems := h.fetcher.FetchAll(r.Context(), resolved, &libraryID, nil, userID, profileID, accessFilter)
	withItems = applyDiversityFilter(withItems)
	withItems = dropEmptySeasonalSections(withItems)
	writeJSON(w, http.StatusOK, h.buildSectionsResponse(r, withItems))
}

// HandleLibrarySectionItems handles GET /library/{id}/sections/{sectionId}/items
func (h *SectionHandler) HandleLibrarySectionItems(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	libraryID, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}

	sectionID := chi.URLParam(r, "sectionId")
	if sectionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Section ID is required")
		return
	}

	resolved, accessFilter, profileID, err := h.loadResolvedLibrarySections(r, libraryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load sections")
		return
	}

	userID := apimw.GetUserID(r.Context())
	for _, s := range resolved {
		if s.ID != sectionID {
			continue
		}

		withItems, fetchErr := h.fetcher.FetchOne(r.Context(), s, &libraryID, nil, userID, profileID, accessFilter)
		if fetchErr != nil {
			slog.Error("fetching section items", "section_id", s.ID, "type", s.SectionType, "error", fetchErr)
			withItems = sections.SectionWithItems{
				ResolvedSection: s,
				Items:           []*models.MediaItem{},
			}
		}

		resp := h.buildSectionsResponse(r, []sections.SectionWithItems{withItems})
		if len(resp.Sections) == 0 {
			resp.Sections = append(resp.Sections, resolvedSectionResponse{
				ID:          withItems.ID,
				SectionType: string(withItems.SectionType),
				Title:       withItems.Title,
				Featured:    withItems.Featured,
				ItemLimit:   withItems.ItemLimit,
				TotalCount:  withItems.TotalCount,
				IsCustom:    withItems.IsCustom,
				Customized:  withItems.Customized,
				Items:       []sectionItemResponse{},
			})
		}

		writeJSON(w, http.StatusOK, homeSectionItemsResponse{
			Section: resp.Sections[0],
		})
		return
	}

	writeError(w, http.StatusNotFound, "not_found", "Section not found")
}

func (h *SectionHandler) loadResolvedHomeSections(r *http.Request) ([]sections.ResolvedSection, []int, catalog.AccessFilter, string, error) {
	profileID := apimw.GetProfileID(r.Context())
	userID := apimw.GetUserID(r.Context())

	adminSections, err := h.repo.ListByScope(r.Context(), "home", nil)
	if err != nil {
		return nil, nil, catalog.AccessFilter{}, profileID, err
	}

	// Fall back to default sections when none are admin-configured.
	if len(adminSections) == 0 {
		adminSections, err = h.defaultHomeSections(r.Context())
		if err != nil {
			return nil, nil, catalog.AccessFilter{}, profileID, err
		}
	}

	var overrides []sections.ProfileSectionOverride
	if h.StoreProvider != nil && profileID != "" {
		store, storeErr := h.StoreProvider.ForUser(r.Context(), userID)
		if storeErr == nil {
			userOverrides, _ := store.ListSectionOverrides(r.Context(), profileID, "home", "")
			overrides = toSectionOverrides(userOverrides)
		}
	}

	resolved := sections.Resolve(adminSections, overrides)

	var libraryIDs []int
	accessFilter := catalog.AccessFilter{}
	if scope, ok := access.GetScope(r.Context()); ok {
		libraryIDs = scope.AllowedLibraryIDs
		accessFilter.AllowedLibraryIDs = scope.AllowedLibraryIDs
		accessFilter.DisabledLibraryIDs = scope.DisabledLibraryIDs
		accessFilter.MaxContentRating = scope.MaxContentRating
	} else if h.UserRepo != nil {
		user, _ := h.UserRepo.GetByID(r.Context(), userID)
		if user != nil && user.LibraryIDs != nil {
			libraryIDs = user.LibraryIDs
			accessFilter.AllowedLibraryIDs = user.LibraryIDs
		}
	}

	return resolved, libraryIDs, accessFilter, profileID, nil
}

func (h *SectionHandler) loadResolvedLibrarySections(r *http.Request, libraryID int) ([]sections.ResolvedSection, catalog.AccessFilter, string, error) {
	profileID := apimw.GetProfileID(r.Context())
	userID := apimw.GetUserID(r.Context())

	adminSections, err := h.repo.ListByScope(r.Context(), "library", &libraryID)
	if err != nil {
		return nil, catalog.AccessFilter{}, profileID, err
	}

	// Fall back to default sections when none are admin-configured.
	if len(adminSections) == 0 {
		defaults, defaultsErr := h.defaultLibrarySections(r.Context(), libraryID)
		if defaultsErr != nil {
			slog.Warn("loading typed library section defaults", "library_id", libraryID, "error", defaultsErr)
			adminSections = sections.DefaultLibrarySections(&libraryID)
		} else {
			adminSections = defaults
		}
	}

	var overrides []sections.ProfileSectionOverride
	if h.StoreProvider != nil && profileID != "" {
		store, storeErr := h.StoreProvider.ForUser(r.Context(), userID)
		if storeErr == nil {
			libStr := strconv.Itoa(libraryID)
			userOverrides, _ := store.ListSectionOverrides(r.Context(), profileID, "library", libStr)
			overrides = toSectionOverrides(userOverrides)
		}
	}

	resolved := sections.Resolve(adminSections, overrides)
	resolved = h.maybeInjectNextUp(r.Context(), resolved, userID)

	accessFilter := catalog.AccessFilter{}
	if scope, ok := access.GetScope(r.Context()); ok {
		accessFilter.AllowedLibraryIDs = scope.AllowedLibraryIDs
		accessFilter.DisabledLibraryIDs = scope.DisabledLibraryIDs
		accessFilter.MaxContentRating = scope.MaxContentRating
	}

	return resolved, accessFilter, profileID, nil
}

// --- Profile override endpoints ---

type saveOverridesRequest struct {
	Scope     string                   `json:"scope"`
	LibraryID string                   `json:"library_id"`
	Overrides []profileOverrideRequest `json:"overrides"`
}

type profileOverrideRequest struct {
	ID              string          `json:"id"`
	SectionID       string          `json:"section_id"`
	Position        *int            `json:"position"`
	Hidden          bool            `json:"hidden"`
	Removed         bool            `json:"removed"`
	SectionType     string          `json:"section_type"`
	Title           string          `json:"title"`
	Featured        *bool           `json:"featured"`
	ItemLimit       *int            `json:"item_limit"`
	Config          json.RawMessage `json:"config"`
	IsUserAdded     bool            `json:"is_user_added,omitempty"`
	UserSectionType string          `json:"user_section_type,omitempty"`
	UserConfig      json.RawMessage `json:"user_config,omitempty"`
	UserTitle       string          `json:"user_title,omitempty"`
}

// HandleGetProfileOverrides handles GET /profile/sections?scope=home
func (h *SectionHandler) HandleGetProfileOverrides(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())
	userID := apimw.GetUserID(r.Context())

	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "home"
	}
	libraryID := r.URL.Query().Get("library_id")

	if h.StoreProvider == nil {
		writeJSON(w, http.StatusOK, map[string][]userstore.SectionOverride{"overrides": {}})
		return
	}

	store, err := h.StoreProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	overrides, err := store.ListSectionOverrides(r.Context(), profileID, scope, libraryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load overrides")
		return
	}

	if overrides == nil {
		overrides = []userstore.SectionOverride{}
	}
	writeJSON(w, http.StatusOK, map[string][]userstore.SectionOverride{"overrides": overrides})
}

// HandleSaveProfileOverrides handles PUT /profile/sections
func (h *SectionHandler) HandleSaveProfileOverrides(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())
	userID := apimw.GetUserID(r.Context())

	var req saveOverridesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Scope == "" {
		req.Scope = "home"
	}

	// Gate: validate user-added overrides before touching the store.
	allowCustom := false
	if h.Settings != nil {
		v, _ := h.Settings.Get(r.Context(), SectionsAllowProfileCustomSettingKey)
		allowCustom = v == "true"
	}
	isAdmin := false
	if claims := apimw.GetClaims(r.Context()); claims != nil {
		isAdmin = claims.Role == "admin"
	}

	for _, o := range req.Overrides {
		// The resolver treats any override with empty SectionID as user-added,
		// regardless of the IsUserAdded flag. Match that here so a client cannot
		// bypass the recipe gate by omitting is_user_added and sending the legacy
		// shape (section_id:"", section_type:"admin_curated_list", config:{…}).
		isUserAdded := o.IsUserAdded || o.SectionID == ""
		if !isUserAdded {
			continue
		}

		// Prefer the explicit user_section_type; fall back to legacy section_type
		// for parity with resolveUserAdded.
		recipeType := o.UserSectionType
		if recipeType == "" {
			recipeType = o.SectionType
		}
		rec, ok := recipes.Get(recipeType)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown_recipe", "section_type not registered: "+recipeType)
			return
		}
		if rec.Definition().AdminOnly && !isAdmin && !allowCustom {
			writeError(w, http.StatusForbidden, "custom_disabled", "this server does not allow profiles to build custom sections")
			return
		}
		// Validate whichever config the resolver will actually use.
		cfg := o.UserConfig
		if len(cfg) == 0 {
			cfg = o.Config
		}
		if err := rec.Validate(cfg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
			return
		}
	}

	if h.StoreProvider == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "User store not available")
		return
	}

	store, err := h.StoreProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	overrides := make([]userstore.SectionOverride, len(req.Overrides))
	for i, o := range req.Overrides {
		var configStr string
		if len(o.Config) > 0 {
			configStr = string(o.Config)
		}
		overrides[i] = userstore.SectionOverride{
			ID:              o.ID,
			ProfileID:       profileID,
			Scope:           req.Scope,
			LibraryID:       req.LibraryID,
			SectionID:       o.SectionID,
			Position:        o.Position,
			Hidden:          o.Hidden,
			Removed:         o.Removed,
			SectionType:     o.SectionType,
			Title:           o.Title,
			Featured:        o.Featured,
			ItemLimit:       o.ItemLimit,
			Config:          configStr,
			IsUserAdded:     o.IsUserAdded,
			UserSectionType: o.UserSectionType,
			UserConfig:      string(o.UserConfig),
			UserTitle:       o.UserTitle,
		}
	}

	if err := store.SaveSectionOverrides(r.Context(), profileID, req.Scope, req.LibraryID, overrides); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save overrides")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleResetProfileOverrides handles DELETE /profile/sections/reset?scope=home
func (h *SectionHandler) HandleResetProfileOverrides(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())
	userID := apimw.GetUserID(r.Context())

	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "home"
	}
	libraryID := r.URL.Query().Get("library_id")

	if h.StoreProvider == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "User store not available")
		return
	}

	store, err := h.StoreProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.ResetSectionOverrides(r.Context(), profileID, scope, libraryID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to reset overrides")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleSectionSettings handles GET /profile/sections/settings?scope=home&library_id=123
func (h *SectionHandler) HandleSectionSettings(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())
	userID := apimw.GetUserID(r.Context())

	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "home"
	}

	var libIDPtr *int
	if lid := r.URL.Query().Get("library_id"); lid != "" {
		v, err := strconv.Atoi(lid)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid library_id")
			return
		}
		libIDPtr = &v
	}

	adminSections, err := h.repo.ListByScope(r.Context(), scope, libIDPtr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load sections")
		return
	}

	var overrides []sections.ProfileSectionOverride
	if h.StoreProvider != nil && profileID != "" {
		store, storeErr := h.StoreProvider.ForUser(r.Context(), userID)
		if storeErr == nil {
			libStr := ""
			if libIDPtr != nil {
				libStr = strconv.Itoa(*libIDPtr)
			}
			userOverrides, _ := store.ListSectionOverrides(r.Context(), profileID, scope, libStr)
			overrides = toSectionOverrides(userOverrides)
		}
	}

	resolved := sections.ResolveForSettings(adminSections, overrides)

	type settingsEntry struct {
		ID          string          `json:"id"`
		SectionType string          `json:"section_type"`
		Title       string          `json:"title"`
		Featured    bool            `json:"featured"`
		ItemLimit   int             `json:"item_limit"`
		Hidden      bool            `json:"hidden"`
		IsCustom    bool            `json:"is_custom"`
		Customized  bool            `json:"customized"`
		Position    int             `json:"position"`
		Config      json.RawMessage `json:"config,omitempty"`
	}

	entries := make([]settingsEntry, 0, len(resolved))
	for _, s := range resolved {
		entries = append(entries, settingsEntry{
			ID:          s.ID,
			SectionType: string(s.SectionType),
			Title:       s.Title,
			Featured:    s.Featured,
			ItemLimit:   s.ItemLimit,
			Hidden:      s.Hidden,
			IsCustom:    s.IsCustom,
			Customized:  s.Customized,
			Position:    s.Position,
			Config:      s.Config,
		})
	}

	writeJSON(w, http.StatusOK, map[string][]settingsEntry{"sections": entries})
}

// applyDiversityFilter removes items from sections whose recipe has
// AvoidDuplicates=true if the same content ID was already surfaced by an
// earlier section in the same render. Operates in-place — preserves order.
// Sections without an AvoidDuplicates recipe are seen-but-not-filtered:
// their items still mark content_ids as "seen" for downstream sections.
func applyDiversityFilter(withItems []sections.SectionWithItems) []sections.SectionWithItems {
	seen := map[string]struct{}{}
	for i := range withItems {
		sectionType := string(withItems[i].SectionType)
		rec, ok := recipes.Get(sectionType)
		avoid := ok && rec.Definition().AvoidDuplicates
		if avoid {
			kept := withItems[i].Items[:0]
			for _, item := range withItems[i].Items {
				if item == nil || item.ContentID == "" {
					kept = append(kept, item)
					continue
				}
				if _, dup := seen[item.ContentID]; dup {
					continue
				}
				kept = append(kept, item)
			}
			withItems[i].Items = kept
			withItems[i].TotalCount = len(kept)
		}
		for _, item := range withItems[i].Items {
			if item == nil || item.ContentID == "" {
				continue
			}
			seen[item.ContentID] = struct{}{}
		}
	}
	return withItems
}

// dropEmptySeasonalSections removes seasonal_themed sections that produced
// no items so the home page doesn't render an empty row off-season. Called
// from the aggregate handlers; per-section endpoints still return empty
// seasonal sections when explicitly requested by ID.
func dropEmptySeasonalSections(withItems []sections.SectionWithItems) []sections.SectionWithItems {
	out := withItems[:0]
	for _, w := range withItems {
		if w.SectionType == sections.SectionSeasonalThemed && len(w.Items) == 0 {
			continue
		}
		out = append(out, w)
	}
	return out
}

// --- Helper methods ---

type sectionItemImageKey struct {
	sectionID string
	contentID string
}

type sectionItemImageURLs struct {
	posterURL   string
	backdropURL string
	logoURL     string
}

func (h *SectionHandler) buildSectionsResponse(r *http.Request, withItems []sections.SectionWithItems) homeSectionsResponse {
	overlaySummaries := make(map[string]*models.OverlaySummary)
	contentIDs := make([]string, 0)
	seen := make(map[string]struct{})
	for _, section := range withItems {
		for _, item := range section.Items {
			if item == nil || item.ContentID == "" {
				continue
			}
			if _, ok := seen[item.ContentID]; ok {
				continue
			}
			seen[item.ContentID] = struct{}{}
			contentIDs = append(contentIDs, item.ContentID)
		}
	}
	if len(contentIDs) > 0 && h.fetcher != nil {
		summaries, err := h.fetcher.ListOverlaySummaries(r.Context(), contentIDs, requestAccessFilter(r))
		if err != nil {
			slog.Error("loading overlay summaries", "error", err)
		} else {
			overlaySummaries = summaries
		}
	}

	resp := homeSectionsResponse{
		Sections: make([]resolvedSectionResponse, 0, len(withItems)),
	}
	allItems := make([]*models.MediaItem, 0)
	for _, s := range withItems {
		allItems = append(allItems, s.Items...)
	}
	userStates := h.listSectionItemUserStates(r, allItems)
	imageURLs := h.resolveSectionItemImageURLs(r.Context(), withItems)
	episodeMeta := h.listSectionEpisodeItemMeta(r.Context(), withItems, requestAccessFilter(r))
	for _, s := range withItems {
		items := make([]sectionItemResponse, 0, len(s.Items))
		for _, item := range s.Items {
			var meta *sections.SectionItemMeta
			if s.ItemMeta != nil {
				if value, ok := s.ItemMeta[item.ContentID]; ok {
					meta = &value
				}
			}
			if meta == nil {
				if value, ok := episodeMeta[item.ContentID]; ok {
					meta = &value
				}
			}
			imageKey := sectionItemImageKey{sectionID: s.ID, contentID: item.ContentID}
			items = append(items, h.toSectionItemResponse(s.SectionType, item, meta, overlaySummaries[item.ContentID], userStates[item.ContentID], imageURLs[imageKey]))
		}
		resp.Sections = append(resp.Sections, resolvedSectionResponse{
			ID:          s.ID,
			SectionType: string(s.SectionType),
			Title:       s.Title,
			Featured:    s.Featured,
			ItemLimit:   s.ItemLimit,
			TotalCount:  s.TotalCount,
			IsCustom:    s.IsCustom,
			Customized:  s.Customized,
			Items:       items,
		})
	}
	return resp
}

func (h *SectionHandler) listSectionEpisodeItemMeta(ctx context.Context, withItems []sections.SectionWithItems, filter catalog.AccessFilter) map[string]sections.SectionItemMeta {
	if h == nil || h.episodeFetcher == nil {
		return map[string]sections.SectionItemMeta{}
	}

	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for _, section := range withItems {
		for _, item := range section.Items {
			if item == nil || item.Type != "episode" || strings.TrimSpace(item.ContentID) == "" {
				continue
			}
			if section.ItemMeta != nil {
				if _, ok := section.ItemMeta[item.ContentID]; ok {
					continue
				}
			}
			if _, ok := seen[item.ContentID]; ok {
				continue
			}
			seen[item.ContentID] = struct{}{}
			ids = append(ids, item.ContentID)
		}
	}
	if len(ids) == 0 {
		return map[string]sections.SectionItemMeta{}
	}

	_, meta, err := h.episodeFetcher.FetchEpisodesByContentIDs(ctx, ids, filter)
	if err != nil {
		slog.Warn("loading section episode metadata", "error", err)
		return map[string]sections.SectionItemMeta{}
	}
	return meta
}

func (h *SectionHandler) resolveSectionItemImageURLs(ctx context.Context, withItems []sections.SectionWithItems) map[sectionItemImageKey]sectionItemImageURLs {
	result := make(map[sectionItemImageKey]sectionItemImageURLs)
	if h.DetailSvc == nil {
		return result
	}

	type pendingImages struct {
		key          sectionItemImageKey
		posterPath   string
		backdropPath string
		logoPath     string
	}

	pending := make([]pendingImages, 0)
	paths := make([]string, 0)
	seenPaths := make(map[string]struct{})
	addPath := func(path string) {
		if path == "" || path == "-" {
			return
		}
		if _, ok := seenPaths[path]; ok {
			return
		}
		seenPaths[path] = struct{}{}
		paths = append(paths, path)
	}

	for _, section := range withItems {
		for _, item := range section.Items {
			if item == nil {
				continue
			}
			images := pendingImages{
				key: sectionItemImageKey{
					sectionID: section.ID,
					contentID: item.ContentID,
				},
				posterPath:   featuredPosterPath(item.PosterPath),
				backdropPath: sectionBackdropPath(section.SectionType, item.BackdropPath),
				logoPath:     item.LogoPath,
			}
			pending = append(pending, images)
			addPath(images.posterPath)
			addPath(images.backdropPath)
			addPath(images.logoPath)
		}
	}

	resolved := h.DetailSvc.PresignURLsWithExpiry(ctx, paths, "featured")
	for _, images := range pending {
		result[images.key] = sectionItemImageURLs{
			posterURL:   resolved[images.posterPath].URL,
			backdropURL: resolved[images.backdropPath].URL,
			logoURL:     resolved[images.logoPath].URL,
		}
	}
	return result
}

func (h *SectionHandler) toSectionItemResponse(sectionType sections.SectionType, item *models.MediaItem, meta *sections.SectionItemMeta, overlaySummary *models.OverlaySummary, userState *itemUserStateResponse, imageURLs sectionItemImageURLs) sectionItemResponse {
	resp := sectionItemResponse{
		ContentID:         item.ContentID,
		Type:              item.Type,
		Title:             item.Title,
		Year:              item.Year,
		Runtime:           item.Runtime,
		Genres:            item.Genres,
		Keywords:          item.Keywords,
		Studios:           item.Studios,
		Networks:          item.Networks,
		ContentRating:     item.ContentRating,
		Status:            item.Status,
		ShowStatus:        item.ShowStatus,
		RatingIMDB:        item.RatingIMDB,
		RatingTMDB:        item.RatingTMDB,
		RatingRTCritic:    item.RatingRTCritic,
		RatingRTAudience:  item.RatingRTAudience,
		OriginalLanguage:  item.OriginalLanguage,
		Overview:          item.Overview,
		PosterThumbhash:   item.PosterThumbhash,
		BackdropThumbhash: item.BackdropThumbhash,
		OverlaySummary:    overlaySummary,
		UserState:         userState,
	}
	if meta != nil {
		if meta.SeriesID != nil {
			resp.SeriesID = *meta.SeriesID
		}
		resp.SeriesTitle = meta.SeriesTitle
		resp.SeasonNumber = meta.SeasonNumber
		resp.EpisodeNumber = meta.EpisodeNumber
		resp.Badges = meta.Badges
		resp.PositionSeconds = meta.PositionSeconds
		resp.DurationSeconds = meta.DurationSeconds
		resp.ProgressUpdatedAt = meta.ProgressUpdatedAt
		resp.ItemSource = meta.ItemSource
	}

	if resp.Genres == nil {
		resp.Genres = []string{}
	}
	if resp.Keywords == nil {
		resp.Keywords = []string{}
	}

	resp.PosterURL = imageURLs.posterURL
	resp.BackdropURL = imageURLs.backdropURL
	resp.LogoURL = imageURLs.logoURL

	return resp
}

// sectionBackdropPath keeps featured-style backdrops for most sections, but
// uses the cached w1280 backdrop for Continue Watching / Next Up rows.
func sectionBackdropPath(sectionType sections.SectionType, path string) string {
	if sectionType == sections.SectionContinueWatching || sectionType == sections.SectionNextUp {
		return catalog.BackdropVariantPath(path, "w1280")
	}
	return featuredBackdropPath(path)
}

func (h *SectionHandler) listSectionItemUserStates(r *http.Request, items []*models.MediaItem) map[string]*itemUserStateResponse {
	if h.StoreProvider == nil {
		return map[string]*itemUserStateResponse{}
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 || profileID == "" {
		return map[string]*itemUserStateResponse{}
	}
	store, err := h.StoreProvider.ForUser(r.Context(), userID)
	if err != nil || store == nil {
		return map[string]*itemUserStateResponse{}
	}
	states, err := resolveItemUserStatesWithOptions(r.Context(), store, profileID, h.EpisodeRepo, items, itemUserStateOptions{
		UserID:             userID,
		EbookProgressStore: h.EbookProgress,
	})
	if err != nil {
		return map[string]*itemUserStateResponse{}
	}
	return states
}

func (h *SectionHandler) sectionPresignURL(r *http.Request, path string, variant string) string {
	if h.DetailSvc != nil {
		return h.DetailSvc.PresignURL(r.Context(), path, variant)
	}
	return ""
}

// maybeInjectNextUp injects a SectionNextUp entry after SectionContinueWatching
// if the user's next_up_mode setting is "separate".
func (h *SectionHandler) maybeInjectNextUp(ctx context.Context, resolved []sections.ResolvedSection, userID int) []sections.ResolvedSection {
	if h.StoreProvider == nil || userID <= 0 {
		return resolved
	}
	store, err := h.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		return resolved
	}
	mode, _ := store.GetSetting(ctx, "next_up_mode")
	if mode == "separate" {
		return injectNextUpSection(resolved)
	}
	return resolved
}

// injectNextUpSection inserts a synthetic SectionNextUp entry immediately after
// SectionContinueWatching in the resolved sections list.
func injectNextUpSection(resolved []sections.ResolvedSection) []sections.ResolvedSection {
	nextUp := sections.ResolvedSection{
		ID:          "system-next-up",
		SectionType: sections.SectionNextUp,
		Title:       "Next Up",
		ItemLimit:   20,
	}

	for i, s := range resolved {
		if s.SectionType == sections.SectionContinueWatching {
			result := make([]sections.ResolvedSection, 0, len(resolved)+1)
			result = append(result, resolved[:i+1]...)
			result = append(result, nextUp)
			result = append(result, resolved[i+1:]...)
			return result
		}
	}

	// If no continue_watching found, prepend
	return append([]sections.ResolvedSection{nextUp}, resolved...)
}

func toSectionOverrides(storeOverrides []userstore.SectionOverride) []sections.ProfileSectionOverride {
	result := make([]sections.ProfileSectionOverride, len(storeOverrides))
	for i, o := range storeOverrides {
		var cfg json.RawMessage
		if o.Config != "" {
			cfg = json.RawMessage(o.Config)
		}
		var userCfg json.RawMessage
		if o.UserConfig != "" {
			userCfg = json.RawMessage(o.UserConfig)
		}
		result[i] = sections.ProfileSectionOverride{
			ID:          o.ID,
			ProfileID:   o.ProfileID,
			Scope:       o.Scope,
			LibraryID:   o.LibraryID,
			SectionID:   o.SectionID,
			Position:    o.Position,
			Hidden:      o.Hidden,
			Removed:     o.Removed,
			SectionType: sections.SectionType(o.SectionType),
			Title:       o.Title,
			Featured:    o.Featured,
			ItemLimit:   o.ItemLimit,
			Config:      cfg,
			CreatedAt:   o.CreatedAt,
			UpdatedAt:   o.UpdatedAt,
			// User-added recipe fields. Without these the resolver sees
			// SectionType="" / IsUserAdded=false on every load and silently
			// drops profile-built sections.
			IsUserAdded:     o.IsUserAdded,
			UserSectionType: sections.SectionType(o.UserSectionType),
			UserConfig:      userCfg,
			UserTitle:       o.UserTitle,
		}
	}
	return result
}

// HandleRestoreDefaults handles POST /admin/sections/restore-defaults.
// It replaces all sections for a scope with the canonical defaults.
func (h *SectionHandler) HandleRestoreDefaults(w http.ResponseWriter, r *http.Request) {
	var req restoreDefaultsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Scope == "" {
		req.Scope = "home"
	}
	if req.Scope != "home" && req.Scope != "library" {
		writeError(w, http.StatusBadRequest, "bad_request", "Scope must be 'home' or 'library'")
		return
	}
	if req.Scope == "library" && req.LibraryID == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "library_id is required for library scope")
		return
	}
	if req.Scope == "home" && req.LibraryID != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "library_id must not be set for home scope")
		return
	}

	var defaults []*sections.PageSection
	var err error
	if req.Scope == "home" {
		defaults, err = h.defaultHomeSections(r.Context())
		if err != nil {
			slog.Error("loading default home sections", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load libraries")
			return
		}
	} else {
		defaults, err = h.defaultLibrarySections(r.Context(), *req.LibraryID)
		if err != nil {
			if errors.Is(err, catalog.ErrFolderNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "Library not found")
				return
			}
			slog.Error("loading default library sections", "library_id", *req.LibraryID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load library")
			return
		}
	}

	created, err := h.repo.RestoreDefaults(r.Context(), req.Scope, req.LibraryID, defaults)
	if err != nil {
		slog.Error("restoring default sections", "scope", req.Scope, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to restore defaults")
		return
	}

	// Optionally clear all profile overrides for this scope.
	if req.ResetProfiles {
		libraryIDStr := ""
		if req.LibraryID != nil {
			libraryIDStr = strconv.Itoa(*req.LibraryID)
		}
		if err := h.repo.ClearAllProfileOverrides(r.Context(), req.Scope, libraryIDStr); err != nil {
			slog.Error("clearing profile overrides", "scope", req.Scope, "error", err)
			// Don't fail the whole request — sections were already restored.
		}
	}

	resp := sectionListResponse{Sections: make([]sectionResponse, 0, len(created))}
	for _, s := range created {
		resp.Sections = append(resp.Sections, toSectionResponse(s))
	}
	writeJSON(w, http.StatusOK, resp)
}
