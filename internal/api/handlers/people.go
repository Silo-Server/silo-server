package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type peopleRepository interface {
	Get(ctx context.Context, id int64) (*models.Person, error)
	Search(ctx context.Context, query string, limit int) ([]models.Person, error)
	Update(ctx context.Context, p models.Person) error
}

type PersonRefreshQueue interface {
	Enqueue(id int64)
}

type PersonRefresher interface {
	RefreshPerson(ctx context.Context, id int64) (*models.Person, error)
}

var personRefreshRate = ratelimit.Rate{
	RequestsPerSecond: 10,
	RequestsPerMinute: 10,
	Burst:             10,
}

const personMetadataStaleAfter = 90 * 24 * time.Hour

// PeopleHandler serves person-related API endpoints.
type PeopleHandler struct {
	personRepo      peopleRepository
	catalogResolver *catalog.CatalogResolver
	detailSvc       *catalog.DetailService
	itemsHandler    *ItemsHandler
	refreshQueue    PersonRefreshQueue
	refresher       PersonRefresher
	refreshLimiter  ratelimit.RateLimiter
}

// NewPeopleHandler creates a new people handler.
func NewPeopleHandler(
	personRepo peopleRepository,
	browseRepo *catalog.BrowseRepository,
	itemRepo *catalog.ItemRepository,
	detailSvc *catalog.DetailService,
) *PeopleHandler {
	return &PeopleHandler{
		personRepo:      personRepo,
		catalogResolver: catalog.NewCatalogResolver(browseRepo, itemRepo),
		detailSvc:       detailSvc,
		refreshLimiter:  ratelimit.NewMemoryLimiter(),
	}
}

// SetItemsHandler sets the items handler for browse response formatting.
func (h *PeopleHandler) SetItemsHandler(ih *ItemsHandler) {
	h.itemsHandler = ih
}

func (h *PeopleHandler) SetRefreshQueue(queue PersonRefreshQueue) {
	h.refreshQueue = queue
}

func (h *PeopleHandler) SetRefreshService(refresher PersonRefresher) {
	h.refresher = refresher
}

type personResponse struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Bio            string  `json:"bio,omitempty"`
	BirthDate      *string `json:"birth_date,omitempty"`
	DeathDate      *string `json:"death_date,omitempty"`
	Birthplace     string  `json:"birthplace,omitempty"`
	Homepage       string  `json:"homepage,omitempty"`
	PhotoURL       string  `json:"photo_url,omitempty"`
	PhotoThumbhash string  `json:"photo_thumbhash,omitempty"`
	TmdbID         string  `json:"tmdb_id,omitempty"`
	ImdbID         string  `json:"imdb_id,omitempty"`
	TvdbID         string  `json:"tvdb_id,omitempty"`
	PlexGUID       string  `json:"plex_guid,omitempty"`
}

// HandleSearch serves GET /api/people?q=&limit=
func (h *PeopleHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}

	people, err := h.personRepo.Search(r.Context(), query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search_failed", err.Error())
		return
	}

	resp := make([]personResponse, len(people))
	for i, p := range people {
		resp[i] = h.toResponse(r.Context(), p)
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleGetPerson serves GET /api/people/:id
func (h *PeopleHandler) HandleGetPerson(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid person ID")
		return
	}

	person, err := h.personRepo.Get(r.Context(), id)
	if err != nil {
		slog.Warn("people: get person failed", "id", id, "id_str", idStr, "error", err)
		writeError(w, http.StatusNotFound, "not_found", "person not found")
		return
	}

	h.enqueuePersonRefreshIfDue(*person)

	writeJSON(w, http.StatusOK, h.toResponse(r.Context(), *person))
}

// HandleRefreshPerson serves POST /api/v1/people/:id/refresh.
func (h *PeopleHandler) HandleRefreshPerson(w http.ResponseWriter, r *http.Request) {
	if h.refreshQueue == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Person refresh is not configured")
		return
	}

	id, ok := parsePersonID(w, r)
	if !ok {
		return
	}

	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	result := h.refreshLimiter.Allow(r.Context(), strconv.Itoa(userID), personRefreshRate)
	if !result.Allowed {
		if result.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(result.RetryAfter.Seconds()))))
		}
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many person refresh requests")
		return
	}

	person, err := h.personRepo.Get(r.Context(), id)
	if err != nil || person == nil {
		writeError(w, http.StatusNotFound, "not_found", "person not found")
		return
	}

	h.refreshQueue.Enqueue(id)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "queued",
		"person_id": id,
	})
}

// HandleAdminRefreshPerson serves POST /api/v1/admin/people/:id/refresh.
func (h *PeopleHandler) HandleAdminRefreshPerson(w http.ResponseWriter, r *http.Request) {
	if h.refresher == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Person refresh is not configured")
		return
	}

	id, ok := parsePersonID(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	person, err := h.refresher.RefreshPerson(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, metadata.ErrPersonNotFound):
			writeError(w, http.StatusNotFound, "not_found", "person not found")
		case errors.Is(err, metadata.ErrPersonMetadataNotFound):
			writeError(w, http.StatusBadGateway, "provider_error", "No person metadata found")
		default:
			slog.Warn("people: admin refresh failed", "id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to refresh person")
		}
		return
	}

	writeJSON(w, http.StatusOK, h.toResponse(r.Context(), *person))
}

type UpdatePersonRequest struct {
	Name       *string `json:"name"`
	Bio        *string `json:"bio"`
	BirthDate  *string `json:"birth_date"`
	DeathDate  *string `json:"death_date"`
	Birthplace *string `json:"birthplace"`
	Homepage   *string `json:"homepage"`
	TmdbID     *string `json:"tmdb_id"`
	ImdbID     *string `json:"imdb_id"`
	TvdbID     *string `json:"tvdb_id"`
}

// HandleAdminUpdatePerson serves PATCH /api/v1/admin/people/:id.
func (h *PeopleHandler) HandleAdminUpdatePerson(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePersonID(w, r)
	if !ok {
		return
	}

	var req UpdatePersonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	person, err := h.personRepo.Get(r.Context(), id)
	if err != nil || person == nil {
		writeError(w, http.StatusNotFound, "not_found", "person not found")
		return
	}

	if req.Name != nil {
		person.Name = *req.Name
		person.SortName = *req.Name
	}
	if req.Bio != nil {
		person.Bio = *req.Bio
	}
	if req.BirthDate != nil {
		parsed, err := parseOptionalPersonDate(*req.BirthDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid birth_date")
			return
		}
		person.BirthDate = parsed
	}
	if req.DeathDate != nil {
		parsed, err := parseOptionalPersonDate(*req.DeathDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid death_date")
			return
		}
		person.DeathDate = parsed
	}
	if req.Birthplace != nil {
		person.Birthplace = *req.Birthplace
	}
	if req.Homepage != nil {
		person.Homepage = *req.Homepage
	}
	if req.TmdbID != nil {
		person.TmdbID = *req.TmdbID
	}
	if req.ImdbID != nil {
		person.ImdbID = *req.ImdbID
	}
	if req.TvdbID != nil {
		person.TvdbID = *req.TvdbID
	}

	if err := h.personRepo.Update(r.Context(), *person); err != nil {
		slog.Error("people: admin update failed", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update person")
		return
	}

	writeJSON(w, http.StatusOK, h.toResponse(r.Context(), *person))
}

// HandleGetPersonItems serves GET /api/people/:id/items?type=&limit=&offset=
func (h *PeopleHandler) HandleGetPersonItems(w http.ResponseWriter, r *http.Request) {
	writeDeprecatedReadHeaders(w, "/api/v1/catalog?source=person")
	idStr := chi.URLParam(r, "id")
	if _, err := strconv.ParseInt(idStr, 10, 64); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid person ID")
		return
	}

	values := url.Values{}
	for key, rawValues := range r.URL.Query() {
		for _, value := range rawValues {
			values.Add(key, value)
		}
	}
	values.Set("source", "person")
	values.Set("person_id", idStr)
	if values.Get("limit") == "" {
		values.Set("limit", "24")
	}

	req, err := catalog.ParseCatalogRequest(values)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	result, err := h.catalogResolver.Resolve(r.Context(), req, h.itemsHandler.accessFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "browse_failed", err.Error())
		return
	}

	items := make([]itemListResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, h.itemsHandler.toItemListResponse(r, item))
	}

	writeJSON(w, http.StatusOK, browseResponse{
		Total:   result.Total,
		HasMore: req.Offset+len(items) < result.Total,
		Items:   items,
	})
}

func (h *PeopleHandler) toResponse(ctx context.Context, p models.Person) personResponse {
	resp := personResponse{
		ID:         p.ID,
		Name:       p.Name,
		Bio:        p.Bio,
		Birthplace: p.Birthplace,
		Homepage:   p.Homepage,
		TmdbID:     p.TmdbID,
		ImdbID:     p.ImdbID,
		TvdbID:     p.TvdbID,
		PlexGUID:   p.PlexGUID,
	}
	if p.BirthDate != nil {
		s := p.BirthDate.Format("2006-01-02")
		resp.BirthDate = &s
	}
	if p.DeathDate != nil {
		s := p.DeathDate.Format("2006-01-02")
		resp.DeathDate = &s
	}
	if p.PhotoPath != "" && p.PhotoPath != "-" && h.detailSvc != nil {
		resp.PhotoURL = h.detailSvc.PresignURL(ctx, featuredPosterPath(p.PhotoPath), "featured")
	}
	if p.PhotoThumbhash != "" && p.PhotoThumbhash != "-" {
		resp.PhotoThumbhash = p.PhotoThumbhash
	}
	return resp
}

func (h *PeopleHandler) enqueuePersonRefreshIfDue(person models.Person) {
	if h.refreshQueue == nil || !personHasRefreshableProviderID(person) {
		return
	}

	if personMetadataIncomplete(person) {
		h.refreshQueue.Enqueue(person.ID)
		return
	}

	if person.UpdatedAt.Before(time.Now().Add(-personMetadataStaleAfter)) {
		h.refreshQueue.Enqueue(person.ID)
	}
}

func personHasRefreshableProviderID(person models.Person) bool {
	return person.TmdbID != "" || person.ImdbID != "" || person.TvdbID != ""
}

func personMetadataIncomplete(person models.Person) bool {
	return person.Bio == "" || person.PhotoPath == "" || person.PhotoPath == "-" || person.BirthDate == nil
}

func parseOptionalPersonDate(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}

	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, err
	}

	return &parsed, nil
}

func parsePersonID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid person ID")
		return 0, false
	}
	return id, true
}
