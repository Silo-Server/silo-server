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

func newTrailerRefreshHandler(
	access *fakeTrailerItemAccess,
	requester *fakeTrailerRefreshRequester,
) *ItemsHandler {
	return &ItemsHandler{
		trailerItemAccess:       access,
		trailerRefreshRequester: requester,
		trailerRefreshLimiter:   ratelimit.NewMemoryLimiter(),
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

// Episode (and any non movie/series) detail responses never carry videos, so
// asking for their trailers is a client bug rather than an empty result.
func TestTrailersRefreshRejectsNonMovieSeriesTypes(t *testing.T) {
	for _, itemType := range []string{"episode", "audiobook", "ebook"} {
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
