package metadata

import (
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Silo-Server/silo-server/internal/models"
)

// seedEditedMovie stores a matched movie the way Edit Metadata leaves it: an
// admin-typed tagline and overview under the overview lock, and the title
// under the name lock.
func seedEditedMovie(t *testing.T, h *testHarness, contentID string) {
	t.Helper()
	seedMovieIdentity(t, h, contentID, "100", "")
	item, err := h.itemRepo.GetByID(context.Background(), contentID)
	if err != nil {
		t.Fatalf("load seeded item: %v", err)
	}
	item.Title = "Typed Title"
	item.Overview = "Typed overview"
	item.Tagline = "Typed tagline"
	item.LockedFields = []int{int(FieldName), int(FieldOverview)}
	if err := h.itemRepo.Upsert(context.Background(), item); err != nil {
		t.Fatalf("seed edits: %v", err)
	}
}

// A reset can't tell a value a provider lacks from one it failed to send, so
// it only clears fields when the providers answered. A provider that fails, or
// a chain with no metadata at all, fails the reset, and the item keeps its
// values and locks.
func TestProcess_ResetToProviderKeepsTheItemWhenProvidersDoNotAnswer(t *testing.T) {
	answering := func() Provider {
		return &capturingMetadataProvider{response: &MetadataResult{
			HasMetadata: true, Title: "Provider Title", Year: 2006,
			ProviderIDs: map[string]string{"tmdb": "100"},
		}}
	}
	for _, tc := range []struct {
		name      string
		providers []Provider
	}{
		{name: "provider outage", providers: []Provider{answering(), transientRemote{slug: "tvdb"}}},
		{name: "no metadata", providers: []Provider{&capturingMetadataProvider{response: &MetadataResult{}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const contentID = "146000000000000100"
			h := newTestHarness()
			seedEditedMovie(t, h, contentID)

			result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
				ContentID: contentID, Language: "en", Mode: ModeManualRefresh, resetToProvider: true,
			}, tc.providers)
			if err == nil && result != nil && result.Updated {
				t.Fatalf("ProcessWithProviders = %#v, want a failed reset", result)
			}

			item, err := h.itemRepo.GetByID(context.Background(), contentID)
			if err != nil {
				t.Fatalf("load item: %v", err)
			}
			if item.Title != "Typed Title" || item.Overview != "Typed overview" || item.Tagline != "Typed tagline" {
				t.Errorf("title=%q overview=%q tagline=%q, want the admin's values kept", item.Title, item.Overview, item.Tagline)
			}
			if len(item.LockedFields) != 2 {
				t.Errorf("locked_fields = %v, want the stored locks kept", item.LockedFields)
			}
		})
	}
}

// childResetHarness seeds the target-refresh show with an edited, locked S05
// and S05E03, served by provider for every level, in a series whose own
// language is Swedish.
func childResetHarness(t *testing.T, provider Provider) (h *testHarness, seriesID, seasonID, episodeID string) {
	t.Helper()
	ctx := context.Background()
	h, seriesID, seasonID, episodeID = seedTargetRefreshHarness(&targetRefreshProvider{})
	h.itemRepo.items[seriesID].DefaultMetadataLanguage = "sv"
	expiresAt := time.Now().Add(time.Hour)
	for _, level := range []string{"series", "season", "episode"} {
		h.service.chainCache["0:"+level] = chainCacheEntry{providers: []Provider{provider}, expiresAt: expiresAt}
	}
	if err := h.service.seasonRepo.Upsert(ctx, &models.Season{
		ContentID: seasonID, SeriesID: seriesID, SeasonNumber: 5,
		Title: "Typed season", Overview: "Typed season overview", LockedFields: slices.Clone(childResetSeasonLocks),
		MetadataSource: "provider",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.service.episodeRepo.Upsert(ctx, &models.Episode{
		ContentID: episodeID, SeriesID: seriesID, SeasonID: seasonID, SeasonNumber: 5, EpisodeNumber: 3,
		Title: "Typed title", Overview: "Typed overview", LockedFields: slices.Clone(childResetEpisodeLocks),
		MetadataSource: "provider",
	}); err != nil {
		t.Fatal(err)
	}
	return h, seriesID, seasonID, episodeID
}

var (
	childResetSeasonLocks  = []int{int(FieldName), int(FieldOverview)}
	childResetEpisodeLocks = []int{int(FieldName), int(FieldOverview)}
)

// A season or episode reset fetches its fallback text before it empties the
// row, so a fallback lookup that fails leaves the row's edits and locks alone.
func TestResetTargetForLibrary_ChildFallbackFailureKeepsTheRow(t *testing.T) {
	for _, targetType := range []string{RefreshTargetSeason, RefreshTargetEpisode} {
		t.Run(targetType, func(t *testing.T) {
			ctx := context.Background()
			provider := &languageTextProvider{
				slug: "tmdb", types: []string{"series"}, failIn: "en",
				seasons:  map[string][]SeasonResult{"sv": {{SeasonNumber: 5}}},
				episodes: map[string][]EpisodeResult{"sv": {{SeasonNumber: 5, EpisodeNumber: 3}}},
			}
			h, seriesID, seasonID, episodeID := childResetHarness(t, provider)
			h.service.SetFallbackLanguage("en")
			target := seasonID
			if targetType == RefreshTargetEpisode {
				target = episodeID
			}

			if err := h.service.ResetTargetForLibrary(ctx, targetType, target, 0); err == nil {
				t.Fatal("ResetTargetForLibrary succeeded, want the failed fallback lookup to fail it")
			}
			season, err := h.service.seasonRepo.GetBySeriesAndNumber(ctx, seriesID, 5)
			if err != nil {
				t.Fatal(err)
			}
			if season.Title != "Typed season" || season.Overview != "Typed season overview" || !slices.Equal(season.LockedFields, childResetSeasonLocks) {
				t.Errorf("season = %q / %q locks %v, want the admin's edits and locks %v", season.Title, season.Overview, season.LockedFields, childResetSeasonLocks)
			}
			episode, err := h.service.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, 5, 3)
			if err != nil {
				t.Fatal(err)
			}
			if episode.Title != "Typed title" || episode.Overview != "Typed overview" || !slices.Equal(episode.LockedFields, childResetEpisodeLocks) {
				t.Errorf("episode = %q / %q locks %v, want the admin's edits and locks %v", episode.Title, episode.Overview, episode.LockedFields, childResetEpisodeLocks)
			}
		})
	}
}

// An item an admin pinned unmatched has no provider to reset to: the reset
// fails rather than report success with every edit and lock in place, while
// an ordinary refresh of it still settles as a success.
func TestRefreshItemTarget_ResetOfAPinnedItemFails(t *testing.T) {
	const contentID = "146000000000000100"
	h := newTestHarness()
	h.service.hooks.process = func(context.Context, ProcessRequest) (*ProcessResult, error) {
		return &ProcessResult{ContentID: contentID, Pinned: true}, nil
	}
	if err := h.service.RefreshTargetForLibrary(context.Background(), RefreshTargetItem, contentID, 0); err != nil {
		t.Errorf("refresh of a pinned item: %v, want success", err)
	}
	if err := h.service.ResetTargetForLibrary(context.Background(), RefreshTargetItem, contentID, 0); err == nil {
		t.Error("reset of a pinned item succeeded, want an error")
	}
}

// The refresh that ends a Complete Refresh empties the values its providers
// don't send, so it can't do that when one of them fails to answer. It runs
// as an ordinary refresh instead: the job succeeds, what the other providers
// sent applies, and the values they left empty stay.
func TestRebuildItemForLibrary_ProviderOutageKeepsStoredValues(t *testing.T) {
	const contentID = "146000000000000100"
	h := newTestHarness()
	seedEditedMovie(t, h, contentID)
	// The overview lock also holds the tagline, so the content rating is the
	// stored value here that no lock holds and no provider sends.
	seeded, err := h.itemRepo.GetByID(context.Background(), contentID)
	if err != nil {
		t.Fatalf("load seeded item: %v", err)
	}
	seeded.ContentRating = "PG-13"
	if err := h.itemRepo.Upsert(context.Background(), seeded); err != nil {
		t.Fatalf("seed content rating: %v", err)
	}
	providers := []Provider{
		&capturingMetadataProvider{response: &MetadataResult{
			HasMetadata: true, Title: "Provider Title", Year: 2006,
			ProviderIDs: map[string]string{"tmdb": "100"},
		}},
		transientRemote{slug: "tvdb"},
	}
	h.service.hooks.process = func(ctx context.Context, req ProcessRequest) (*ProcessResult, error) {
		req.Language = "en"
		return h.service.ProcessWithProviders(ctx, req, providers)
	}

	if err := h.service.RebuildItemForLibrary(context.Background(), contentID, 0); err != nil {
		t.Fatalf("RebuildItemForLibrary: %v, want an ordinary refresh to succeed", err)
	}
	item, err := h.itemRepo.GetByID(context.Background(), contentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.ContentRating != "PG-13" || item.Year != 2006 {
		t.Errorf("content rating=%q year=%d, want the unlocked content rating kept and the answering provider's year", item.ContentRating, item.Year)
	}
	if len(item.LockedFields) != 2 {
		t.Errorf("locked_fields = %v, want the stored locks kept", item.LockedFields)
	}
}

// An enrichment provider whose key was rejected once supplied the item's
// stored ratings, so a rebuild treats it as not answering and keeps them. A
// reset still lets it stand, as it does a key that was never set.
func TestUnansweredLookup_RebuildKeepsARejectedKeysValues(t *testing.T) {
	enrichment := &PluginProvider{lookupProviderIDs: []string{"imdb"}}
	rejected := status.Error(codes.Unauthenticated, "invalid api key")
	missing := status.Error(codes.FailedPrecondition, "no api key")
	for _, tc := range []struct {
		name       string
		req        ProcessRequest
		err        error
		unanswered bool
	}{
		{"rebuild, rejected key", ProcessRequest{rebuildMatch: true}, rejected, true},
		{"rebuild, missing key", ProcessRequest{rebuildMatch: true}, missing, false},
		{"reset, rejected key", ProcessRequest{resetToProvider: true}, rejected, false},
		{"refresh, rejected key", ProcessRequest{}, rejected, false},
	} {
		got := tc.req.unansweredLookup(enrichment, "metadata", tc.err) != nil
		if got != tc.unanswered {
			t.Errorf("%s: unanswered = %v, want %v", tc.name, got, tc.unanswered)
		}
	}
}
