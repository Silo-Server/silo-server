package handlers

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type peopleRepository interface {
	Get(ctx context.Context, id int64) (*models.Person, error)
	GetVisible(ctx context.Context, id int64, filter catalog.AccessFilter) (*models.Person, error)
	SearchAlphabetical(ctx context.Context, query string, limit int, filter catalog.AccessFilter) ([]models.Person, error)
	SearchScoped(ctx context.Context, query string, limit int, mediaScope string, filter catalog.AccessFilter) ([]models.Person, error)
	Update(ctx context.Context, p models.Person) error
}

type PersonRefreshQueue interface {
	// Enqueue asks for a lookup of person id. lastAttempt is the person's
	// MetadataRefreshAttemptedAt as read; the request is dropped if a lookup
	// started after it.
	Enqueue(id int64, lastAttempt *time.Time)
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
		h.refreshQueue.Enqueue(person.ID, person.MetadataRefreshAttemptedAt)
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
