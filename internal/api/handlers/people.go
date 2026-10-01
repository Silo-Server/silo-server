package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type peopleRepository interface {
	Get(ctx context.Context, id int64) (*models.Person, error)
	Search(ctx context.Context, query string, limit int) ([]models.Person, error)
	SearchScoped(ctx context.Context, query string, limit int, mediaScope string, filter catalog.AccessFilter) ([]models.Person, error)
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

type PersonView struct {
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
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	resp, err := h.SearchPeople(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleGetPerson serves GET /api/people/:id
func (h *PeopleHandler) HandleGetPerson(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePersonID(w, r)
	if !ok {
		return
	}
	resp, err := h.Person(r.Context(), id, true)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
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
	if err := h.RefreshPerson(r.Context(), apimw.GetUserID(r.Context()), id); err != nil {
		writeAPIError(w, err)
		return
	}
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

	person, err := h.RefreshAdminPerson(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, person)
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

	person, err := h.UpdateAdminPerson(r.Context(), id, req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, person)
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

	filter, ok := h.itemsHandler.accessFilterOrError(w, r)
	if !ok {
		return
	}

	result, err := h.catalogResolver.Resolve(r.Context(), req, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "browse_failed", err.Error())
		return
	}

	items := make([]itemListResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, h.itemsHandler.toItemListResponse(r.Context(), viewerFromRequest(r, filter), item, filter.ImageSize))
	}

	writeJSON(w, http.StatusOK, browseResponse{
		Total:   result.Total,
		HasMore: req.Offset+len(items) < result.Total,
		Items:   items,
	})
}

func (h *PeopleHandler) toResponse(ctx context.Context, p models.Person) PersonView {
	resp := PersonView{
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

// enqueuePersonRefreshIfDue queues an on-demand provider lookup for a person
// whose detail page was just viewed. It shares catalog.PersonRefreshDue with
// the background sweep, so a person who was looked up recently is not sent to
// the provider again on every page view.
func (h *PeopleHandler) enqueuePersonRefreshIfDue(person models.Person) {
	if h.refreshQueue == nil {
		return
	}

	if catalog.PersonRefreshDue(person, time.Now()) {
		h.refreshQueue.Enqueue(person.ID)
	}
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
