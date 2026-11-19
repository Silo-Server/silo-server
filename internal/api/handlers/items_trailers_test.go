package handlers

import (
	"context"
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
