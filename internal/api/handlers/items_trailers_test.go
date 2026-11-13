package handlers

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

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

// RequestTrailersRefresh authorizes against the series for season and episode
// IDs, answers not found for anything the viewer cannot see before it reveals
// the type, and only asks the metadata service (which spends the item's
// cooldown) once the request is authorized and supported.
func TestRequestTrailersRefresh(t *testing.T) {
	next := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	queued := metadata.TrailerRefreshOutcome{Status: metadata.TrailerRefreshStatusQueued}
	cases := []struct {
		name        string
		items       map[string]*models.MediaItem
		denied      string
		outcome     metadata.TrailerRefreshOutcome
		contentID   string
		wantStatus  int
		wantCode    string
		wantChecked string
		want        TrailerRefreshView
	}{
		{name: "movie queued", items: map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie"}},
			outcome: queued, contentID: "movie-1", wantChecked: "movie-1", want: TrailerRefreshView{Status: metadata.TrailerRefreshStatusQueued}},
		{name: "series cooldown carries next allowed", items: map[string]*models.MediaItem{"series-1": {ContentID: "series-1", Type: "series"}},
			outcome: metadata.TrailerRefreshOutcome{Status: metadata.TrailerRefreshStatusCooldown, NextAllowedAt: &next}, contentID: "series-1",
			wantChecked: "series-1", want: TrailerRefreshView{Status: metadata.TrailerRefreshStatusCooldown, NextAllowedAt: &next}},
		{name: "audiobook unsupported", items: map[string]*models.MediaItem{"item-1": {ContentID: "item-1", Type: "audiobook"}},
			contentID: "item-1", wantStatus: http.StatusBadRequest, wantCode: "unsupported_type", wantChecked: "item-1"},
		{name: "ebook unsupported", items: map[string]*models.MediaItem{"item-1": {ContentID: "item-1", Type: "ebook"}},
			contentID: "item-1", wantStatus: http.StatusBadRequest, wantCode: "unsupported_type", wantChecked: "item-1"},
		{name: "episode unsupported, authorized on its series", items: map[string]*models.MediaItem{"series-1": {ContentID: "series-1", Type: "series"}},
			contentID: "episode-1", wantStatus: http.StatusBadRequest, wantCode: "unsupported_type", wantChecked: "series-1"},
		{name: "season unsupported, authorized on its series", items: map[string]*models.MediaItem{"series-1": {ContentID: "series-1", Type: "series"}},
			contentID: "season-1", wantStatus: http.StatusBadRequest, wantCode: "unsupported_type", wantChecked: "series-1"},
		{name: "episode in hidden series is not found", items: map[string]*models.MediaItem{"series-1": {ContentID: "series-1", Type: "series"}},
			denied: "series-1", contentID: "episode-1", wantStatus: http.StatusNotFound, wantChecked: "series-1"},
		{name: "hidden movie is not found", items: map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie"}},
			denied: "movie-1", contentID: "movie-1", wantStatus: http.StatusNotFound, wantChecked: "movie-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			itemAccess := &fakeTrailerItemAccess{items: tc.items, ensureErr: map[string]error{}}
			if tc.denied != "" {
				itemAccess.ensureErr[tc.denied] = catalog.ErrItemNotFound
			}
			requester := &fakeTrailerRefreshRequester{outcome: tc.outcome}
			handler := newTrailerRefreshHandler(itemAccess, requester)
			handler.trailerSeasonLookup = fakeTrailerSeasonLookup{"season-1": {ContentID: "season-1", SeriesID: "series-1"}}
			handler.trailerEpisodeLookup = fakeTrailerEpisodeLookup{"episode-1": {ContentID: "episode-1", SeriesID: "series-1"}}

			got, err := handler.RequestTrailersRefresh(t.Context(), 7, tc.contentID, func() (catalog.AccessFilter, error) {
				return catalog.AccessFilter{UserID: 7, ProfileID: "profile-1"}, nil
			})
			if len(itemAccess.checked) != 1 || itemAccess.checked[0] != tc.wantChecked {
				t.Fatalf("access checks = %v, want [%s]", itemAccess.checked, tc.wantChecked)
			}
			if tc.wantStatus != 0 {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Status != tc.wantStatus || (tc.wantCode != "" && apiErr.Code != tc.wantCode) {
					t.Fatalf("error = %#v, want %d %s", err, tc.wantStatus, tc.wantCode)
				}
				if len(requester.requests) != 0 {
					t.Fatalf("refused request reached the service: %v", requester.requests)
				}
				return
			}
			if err != nil {
				t.Fatalf("RequestTrailersRefresh() error = %v", err)
			}
			if got.Status != tc.want.Status || !reflect.DeepEqual(got.NextAllowedAt, tc.want.NextAllowedAt) {
				t.Fatalf("view = %+v, want %+v", got, tc.want)
			}
			if len(requester.requests) != 1 || requester.requests[0] != tc.contentID {
				t.Fatalf("requests = %v, want [%s]", requester.requests, tc.contentID)
			}
		})
	}
}

// The per-user limiter guards the action ahead of the per-item cooldown: a
// burst answers 429 with Retry-After and never reaches the service, and the
// budget is per user.
func TestRequestTrailersRefreshRateLimitsPerUser(t *testing.T) {
	allow := func() (catalog.AccessFilter, error) { return catalog.AccessFilter{}, nil }
	newHandler := func() (*ItemsHandler, *fakeTrailerRefreshRequester) {
		requester := &fakeTrailerRefreshRequester{outcome: metadata.TrailerRefreshOutcome{Status: metadata.TrailerRefreshStatusQueued}}
		return newTrailerRefreshHandler(&fakeTrailerItemAccess{
			items:     map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie"}},
			ensureErr: map[string]error{},
		}, requester), requester
	}
	assertLimited := func(t *testing.T, err error) {
		t.Helper()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || apiErr.RetryAfter <= 0 {
			t.Fatalf("error = %#v, want 429 with Retry-After", err)
		}
	}

	t.Run("default limiter is per user", func(t *testing.T) {
		handler, _ := newHandler()
		var err error
		for i := 0; i < int(trailerRefreshRate.RequestsPerMinute)+5 && err == nil; i++ {
			_, err = handler.RequestTrailersRefresh(t.Context(), 7, "movie-1", allow)
		}
		assertLimited(t, err)
		if _, err := handler.RequestTrailersRefresh(t.Context(), 8, "movie-1", allow); err != nil {
			t.Fatalf("another user was limited: %v", err)
		}
	})

	// A Redis deployment shares one budget across instances only if the
	// injected limiter decides, and its keyspace is shared with the rate-limit
	// middleware, so the key is namespaced.
	t.Run("injected shared limiter decides", func(t *testing.T) {
		handler, requester := newHandler()
		shared := &recordingLimiter{allowed: false}
		handler.SetTrailerRefreshLimiter(shared)
		handler.SetTrailerRefreshRequester(requester) // must keep the injected limiter
		_, err := handler.RequestTrailersRefresh(t.Context(), 7, "movie-1", allow)
		assertLimited(t, err)
		if len(shared.keys) != 1 || shared.keys[0] != trailerRefreshLimiterKey(7) || shared.keys[0] == "7" {
			t.Fatalf("limiter keys = %v, want one namespaced %q", shared.keys, trailerRefreshLimiterKey(7))
		}
		if len(requester.requests) != 0 {
			t.Fatalf("a rate-limited request reached the service: %v", requester.requests)
		}
	})
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

// recordingLimiter captures the keys an action limiter is called with.
type recordingLimiter struct {
	keys    []string
	allowed bool
}

func (l *recordingLimiter) Allow(_ context.Context, key string, _ ratelimit.Rate) ratelimit.AllowResult {
	l.keys = append(l.keys, key)
	return ratelimit.AllowResult{Allowed: l.allowed, RetryAfter: time.Second}
}

func (l *recordingLimiter) Close() {}

// Rate limiting can be disabled outright (or the database unavailable), in
// which case there is no shared limiter to inject. The action keeps its own
// in-memory guard rather than running unbounded.
func TestTrailersRefreshFallsBackToAPrivateLimiter(t *testing.T) {
	handler := &ItemsHandler{}
	handler.SetTrailerRefreshLimiter(nil)
	handler.SetTrailerRefreshRequester(&fakeTrailerRefreshRequester{
		outcome: metadata.TrailerRefreshOutcome{Status: metadata.TrailerRefreshStatusQueued},
	})

	if handler.trailerRefreshLimiter == nil {
		t.Fatal("the action must keep a limiter even when no shared one is configured")
	}
}
