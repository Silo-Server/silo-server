package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type fakeTrailerItemAccess struct {
	items     map[string]*models.MediaItem
	ensureErr map[string]error
	getErr    map[string]error
	checked   []string
}

func (f *fakeTrailerItemAccess) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	if err := f.getErr[contentID]; err != nil {
		return nil, err
	}
	if item := f.items[contentID]; item != nil {
		return item, nil
	}
	return nil, catalog.ErrItemNotFound
}

func (f *fakeTrailerItemAccess) EnsureAccessible(_ context.Context, contentID string, _ catalog.AccessFilter) error {
	f.checked = append(f.checked, contentID)
	return f.ensureErr[contentID]
}

type fakeTrailerRefreshRequester struct {
	outcome  metadata.TrailerRefreshOutcome
	err      error
	requests []string
}

func (f *fakeTrailerRefreshRequester) RequestTrailersRefresh(_ context.Context, contentID string) (metadata.TrailerRefreshOutcome, error) {
	f.requests = append(f.requests, contentID)
	if f.err != nil {
		return metadata.TrailerRefreshOutcome{}, f.err
	}
	return f.outcome, nil
}

// fakeTrailerSeasonLookup and fakeTrailerEpisodeLookup stand in for the season
// and episode tables. Their content IDs are real and resolvable, they are just
// not media_items rows — which is exactly why the route needs them.
type fakeTrailerSeasonLookup map[string]*models.Season

func (f fakeTrailerSeasonLookup) GetByID(_ context.Context, contentID string) (*models.Season, error) {
	if season := f[contentID]; season != nil {
		return season, nil
	}
	return nil, catalog.ErrSeasonNotFound
}

type fakeTrailerEpisodeLookup map[string]*models.Episode

func (f fakeTrailerEpisodeLookup) GetByID(_ context.Context, contentID string) (*models.Episode, error) {
	if episode := f[contentID]; episode != nil {
		return episode, nil
	}
	return nil, catalog.ErrEpisodeNotFound
}

func newTrailerRefreshHandler(
	access *fakeTrailerItemAccess,
	requester *fakeTrailerRefreshRequester,
) *ItemsHandler {
	return &ItemsHandler{
		trailerItemAccess:       access,
		trailerRefreshRequester: requester,
		trailerRefreshLimiter:   ratelimit.NewMemoryLimiter(),
		trailerSeasonLookup:     fakeTrailerSeasonLookup{},
		trailerEpisodeLookup:    fakeTrailerEpisodeLookup{},
	}
}

func newTrailerRefreshRequest(contentID string, userID int) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/items/"+contentID+"/trailers/refresh", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", contentID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: userID, Role: "user", TokenType: auth.TokenTypeAccess})
	ctx = apimw.SetProfileID(ctx, "profile-1")
	ctx = access.SetScope(ctx, access.Scope{UserID: userID, ProfileID: "profile-1"})
	return req.WithContext(ctx)
}

func decodeTrailerResponse(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rr.Body.String(), err)
	}
	return body
}

// The router discovers both seams by type assertion, so a signature drift
// would silently unregister the route rather than fail the build.
func TestTrailerRefreshWiringAssertionsHold(t *testing.T) {
	var svc any = (*metadata.MetadataService)(nil)
	if _, ok := svc.(TrailerRefreshRequester); !ok {
		t.Fatal("*metadata.MetadataService must satisfy handlers.TrailerRefreshRequester")
	}
	var repo any = (*catalog.ItemRepository)(nil)
	if _, ok := repo.(trailerItemAccess); !ok {
		t.Fatal("*catalog.ItemRepository must satisfy trailerItemAccess")
	}
	// SetTrailerRefreshRequester adopts these from the handler's own repos, so
	// drift here would silently downgrade every episode ID back to a 404.
	var seasons any = (*catalog.SeasonRepository)(nil)
	if _, ok := seasons.(trailerSeasonLookup); !ok {
		t.Fatal("*catalog.SeasonRepository must satisfy trailerSeasonLookup")
	}
	var episodes any = (*catalog.EpisodeRepository)(nil)
	if _, ok := episodes.(trailerEpisodeLookup); !ok {
		t.Fatal("*catalog.EpisodeRepository must satisfy trailerEpisodeLookup")
	}
}

func TestTrailersRefreshReturnsQueued(t *testing.T) {
	itemAccess := &fakeTrailerItemAccess{
		items:     map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie"}},
		ensureErr: map[string]error{},
	}
	requester := &fakeTrailerRefreshRequester{
		outcome: metadata.TrailerRefreshOutcome{Status: metadata.TrailerRefreshStatusQueued},
	}
	handler := newTrailerRefreshHandler(itemAccess, requester)

	rr := httptest.NewRecorder()
	handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest("movie-1", 7))

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusAccepted, rr.Body.String())
	}
	body := decodeTrailerResponse(t, rr)
	if body["status"] != "queued" {
		t.Fatalf("status field = %v, want queued", body["status"])
	}
	if _, ok := body["next_allowed_at"]; ok {
		t.Fatalf("queued response must omit next_allowed_at, got %v", body)
	}
	if len(requester.requests) != 1 || requester.requests[0] != "movie-1" {
		t.Fatalf("requests = %v, want [movie-1]", requester.requests)
	}
}

func TestTrailersRefreshReturnsCooldownWithNextAllowedAt(t *testing.T) {
	next := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	itemAccess := &fakeTrailerItemAccess{
		items:     map[string]*models.MediaItem{"series-1": {ContentID: "series-1", Type: "series"}},
		ensureErr: map[string]error{},
	}
	requester := &fakeTrailerRefreshRequester{
		outcome: metadata.TrailerRefreshOutcome{
			Status:        metadata.TrailerRefreshStatusCooldown,
			NextAllowedAt: &next,
		},
	}
	handler := newTrailerRefreshHandler(itemAccess, requester)

	rr := httptest.NewRecorder()
	handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest("series-1", 7))

	// Cooldown is an expected client-rendered state, not an error: 200, and
	// 429 stays reserved for the per-user limiter.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := decodeTrailerResponse(t, rr)
	if body["status"] != "cooldown" {
		t.Fatalf("status field = %v, want cooldown", body["status"])
	}
	if got := body["next_allowed_at"]; got != next.Format(time.RFC3339) {
		t.Fatalf("next_allowed_at = %v, want %s", got, next.Format(time.RFC3339))
	}
}

func TestTrailersRefreshReturnsDisabled(t *testing.T) {
	itemAccess := &fakeTrailerItemAccess{
		items:     map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie"}},
		ensureErr: map[string]error{},
	}
	requester := &fakeTrailerRefreshRequester{
		outcome: metadata.TrailerRefreshOutcome{Status: metadata.TrailerRefreshStatusDisabled},
	}
	handler := newTrailerRefreshHandler(itemAccess, requester)

	rr := httptest.NewRecorder()
	handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest("movie-1", 7))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := decodeTrailerResponse(t, rr)
	if body["status"] != "disabled" {
		t.Fatalf("status field = %v, want disabled", body["status"])
	}
	if _, ok := body["next_allowed_at"]; ok {
		t.Fatalf("disabled response must omit next_allowed_at, got %v", body)
	}
}

// Only movie and series detail responses carry videos, so any other
// media_items type is a client bug rather than an empty result. These are the
// types that actually exist as media_items rows; episodes and seasons live in
// their own tables and are covered separately below.
func TestTrailersRefreshRejectsNonMovieSeriesTypes(t *testing.T) {
	for _, itemType := range []string{"audiobook", "ebook", "manga"} {
		t.Run(itemType, func(t *testing.T) {
			itemAccess := &fakeTrailerItemAccess{
				items:     map[string]*models.MediaItem{"item-1": {ContentID: "item-1", Type: itemType}},
				ensureErr: map[string]error{},
			}
			requester := &fakeTrailerRefreshRequester{}
			handler := newTrailerRefreshHandler(itemAccess, requester)

			rr := httptest.NewRecorder()
			handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest("item-1", 7))

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusBadRequest, rr.Body.String())
			}
			if len(requester.requests) != 0 {
				t.Fatalf("unsupported type must not reach the service, got %v", requester.requests)
			}
		})
	}
}

// Episodes and seasons are not media_items rows, so the item lookup misses on
// their real content IDs. Without the fallbacks the route would answer 404
// "Item not found" for content that plainly exists; the contract is 400
// unsupported-type. Authorization runs against the parent series, as on the
// on-view translation route.
func TestTrailersRefreshRejectsEpisodeAndSeasonIDsWith400(t *testing.T) {
	tests := []struct {
		name       string
		contentID  string
		wantAccess string
	}{
		{name: "episode", contentID: "episode-1", wantAccess: "series-1"},
		{name: "season", contentID: "season-1", wantAccess: "series-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			itemAccess := &fakeTrailerItemAccess{
				items:     map[string]*models.MediaItem{"series-1": {ContentID: "series-1", Type: "series"}},
				ensureErr: map[string]error{},
			}
			requester := &fakeTrailerRefreshRequester{}
			handler := newTrailerRefreshHandler(itemAccess, requester)
			handler.trailerSeasonLookup = fakeTrailerSeasonLookup{
				"season-1": {ContentID: "season-1", SeriesID: "series-1"},
			}
			handler.trailerEpisodeLookup = fakeTrailerEpisodeLookup{
				"episode-1": {ContentID: "episode-1", SeriesID: "series-1"},
			}

			rr := httptest.NewRecorder()
			handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest(tc.contentID, 7))

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusBadRequest, rr.Body.String())
			}
			body := decodeTrailerResponse(t, rr)
			if code, _ := body["error"].(string); code != "unsupported_type" {
				t.Fatalf("error code = %v, want unsupported_type (%s)", body["error"], rr.Body.String())
			}
			if len(itemAccess.checked) != 1 || itemAccess.checked[0] != tc.wantAccess {
				t.Fatalf("access checks = %v, want [%s]", itemAccess.checked, tc.wantAccess)
			}
			if len(requester.requests) != 0 {
				t.Fatalf("unsupported type must not reach the service, got %v", requester.requests)
			}
		})
	}
}

// An episode inside a series the caller cannot see must not be distinguishable
// from content that does not exist, so the access check runs before the type
// answer.
func TestTrailersRefreshEpisodeInInaccessibleSeriesReturns404(t *testing.T) {
	itemAccess := &fakeTrailerItemAccess{
		items:     map[string]*models.MediaItem{"series-1": {ContentID: "series-1", Type: "series"}},
		ensureErr: map[string]error{"series-1": catalog.ErrItemNotFound},
	}
	requester := &fakeTrailerRefreshRequester{}
	handler := newTrailerRefreshHandler(itemAccess, requester)
	handler.trailerEpisodeLookup = fakeTrailerEpisodeLookup{
		"episode-1": {ContentID: "episode-1", SeriesID: "series-1"},
	}

	rr := httptest.NewRecorder()
	handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest("episode-1", 7))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}
	if len(requester.requests) != 0 {
		t.Fatalf("denied request must not reach the service, got %v", requester.requests)
	}
}

// An unauthorized caller must be turned away before the metadata service is
// asked, so it can never burn the item's cooldown slot.
func TestTrailersRefreshDeniedAccessReturns404WithoutConsumingCooldown(t *testing.T) {
	itemAccess := &fakeTrailerItemAccess{
		items:     map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie"}},
		ensureErr: map[string]error{"movie-1": catalog.ErrItemNotFound},
	}
	requester := &fakeTrailerRefreshRequester{}
	handler := newTrailerRefreshHandler(itemAccess, requester)

	rr := httptest.NewRecorder()
	handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest("movie-1", 7))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}
	if len(itemAccess.checked) != 1 {
		t.Fatalf("access checks = %v, want one check", itemAccess.checked)
	}
	if len(requester.requests) != 0 {
		t.Fatalf("denied request must not reach the service, got %v", requester.requests)
	}
}

func TestTrailersRefreshMissingItemReturns404(t *testing.T) {
	itemAccess := &fakeTrailerItemAccess{
		items:     map[string]*models.MediaItem{},
		ensureErr: map[string]error{},
	}
	requester := &fakeTrailerRefreshRequester{}
	handler := newTrailerRefreshHandler(itemAccess, requester)

	rr := httptest.NewRecorder()
	handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest("missing", 7))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}
	if len(requester.requests) != 0 {
		t.Fatalf("missing item must not reach the service, got %v", requester.requests)
	}
}

func TestTrailersRefreshRequiresAuthentication(t *testing.T) {
	itemAccess := &fakeTrailerItemAccess{
		items:     map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie"}},
		ensureErr: map[string]error{},
	}
	requester := &fakeTrailerRefreshRequester{}
	handler := newTrailerRefreshHandler(itemAccess, requester)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/items/movie-1/trailers/refresh", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "movie-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rr := httptest.NewRecorder()
	handler.HandleRequestTrailersRefresh(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
	if len(requester.requests) != 0 {
		t.Fatalf("unauthenticated request must not reach the service, got %v", requester.requests)
	}
}

// The per-user limiter is the abuse guard in front of the per-item cooldown:
// once a user exhausts the burst it answers 429 with Retry-After.
func TestTrailersRefreshRateLimitsPerUser(t *testing.T) {
	itemAccess := &fakeTrailerItemAccess{
		items:     map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie"}},
		ensureErr: map[string]error{},
	}
	requester := &fakeTrailerRefreshRequester{
		outcome: metadata.TrailerRefreshOutcome{Status: metadata.TrailerRefreshStatusQueued},
	}
	handler := newTrailerRefreshHandler(itemAccess, requester)

	limited := false
	for i := 0; i < int(trailerRefreshRate.RequestsPerMinute)+5; i++ {
		rr := httptest.NewRecorder()
		handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest("movie-1", 7))
		if rr.Code == http.StatusTooManyRequests {
			limited = true
			if rr.Header().Get("Retry-After") == "" {
				t.Fatal("429 response must carry Retry-After")
			}
			break
		}
	}
	if !limited {
		t.Fatal("expected the per-user limiter to reject a burst of requests")
	}

	// A different user is unaffected — the limiter keys on the user id.
	rr := httptest.NewRecorder()
	handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest("movie-1", 8))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("second user status = %d, want %d (%s)", rr.Code, http.StatusAccepted, rr.Body.String())
	}
}

func TestTrailersRefreshUnconfiguredReturns503(t *testing.T) {
	handler := &ItemsHandler{}

	rr := httptest.NewRecorder()
	handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest("movie-1", 7))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
}

func TestTrailersRefreshServiceErrorReturns500(t *testing.T) {
	itemAccess := &fakeTrailerItemAccess{
		items:     map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie"}},
		ensureErr: map[string]error{},
	}
	requester := &fakeTrailerRefreshRequester{err: errors.New("database is down")}
	handler := newTrailerRefreshHandler(itemAccess, requester)

	rr := httptest.NewRecorder()
	handler.HandleRequestTrailersRefresh(rr, newTrailerRefreshRequest("movie-1", 7))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusInternalServerError, rr.Body.String())
	}
}
