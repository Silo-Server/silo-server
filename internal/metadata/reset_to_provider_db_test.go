package metadata

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Reset to Provider on a season or episode (#3581) clears that row's locks and
// gives every lockable field the provider's value, empty included, so an edit
// the provider has nothing for doesn't stay behind. The rest of the show keeps
// its edits, and a provider that fails to answer fails the reset.
func TestResetToProvider_SeasonAndEpisodeRows(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	nonce := time.Now().UnixNano() % 100_000_000

	for i, tc := range []struct {
		name        string
		target      string
		episodesErr error
		wantErr     bool
	}{
		{name: "episode", target: RefreshTargetEpisode},
		{name: "season", target: RefreshTargetSeason},
		{name: "provider outage", target: RefreshTargetEpisode, episodesErr: errors.New("tvdb: HTTP 503: service unavailable"), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentifyLockFixture(pool)
			seriesID := f.seed(t, strconv.FormatInt(810_000_000+nonce+int64(i), 10))
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, seriesID)
			})
			provider := &targetRefreshProvider{
				slug:             "tvdb",
				seasons:          []SeasonResult{{SeasonNumber: 1, Title: "Provider season"}},
				episodesBySeason: map[int][]EpisodeResult{1: {{SeasonNumber: 1, EpisodeNumber: 1, Title: "Provider episode"}}},
				episodesErr:      tc.episodesErr,
			}
			expiresAt := time.Now().Add(time.Hour)
			f.service.chainCache = map[string]chainCacheEntry{
				"0:series":  {providers: []Provider{provider}, expiresAt: expiresAt},
				"0:season":  {providers: []Provider{provider}, expiresAt: expiresAt},
				"0:episode": {providers: []Provider{provider}, expiresAt: expiresAt},
			}
			season, episode := f.read(t, seriesID)
			targetID := episode.ContentID
			if tc.target == RefreshTargetSeason {
				targetID = season.ContentID
			}

			err := f.service.ResetTargetForLibrary(t.Context(), tc.target, targetID, 0)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ResetTargetForLibrary error = %v, want error %v", err, tc.wantErr)
			}

			season, episode = f.read(t, seriesID)
			assertChildRow(t, "season", tc.target == RefreshTargetSeason && !tc.wantErr, "Provider season",
				childRow{season.Title, season.Overview, season.AirDate, 0, season.LockedFields}, identifySeasonLocks)
			assertChildRow(t, "episode", tc.target == RefreshTargetEpisode && !tc.wantErr, "Provider episode",
				childRow{episode.Title, episode.Overview, episode.AirDate, episode.Runtime, episode.LockedFields}, identifyEpisodeLocks)
		})
	}
}

// childRow is a season's or episode's lockable fields and locks.
type childRow struct {
	title, overview string
	airDate         *time.Time
	runtime         int
	locks           []int
}

// assertChildRow checks a season or episode row: reset, it holds the
// provider's title and nothing the provider lacks, with no locks; otherwise it
// keeps the seeded edits and locks.
func assertChildRow(t *testing.T, name string, reset bool, providerTitle string, row childRow, seededLocks []int) {
	t.Helper()
	if reset {
		if row.title != providerTitle || row.overview != "" || row.airDate != nil || row.runtime != 0 || len(row.locks) != 0 {
			t.Errorf("%s = %+v, want the provider's title, nothing else and no locks", name, row)
		}
		return
	}
	if row.title != identifyEditTitle || row.overview != identifyEditOverview || row.airDate == nil || !slices.Equal(row.locks, seededLocks) {
		t.Errorf("%s = %+v, want the edits and locks kept", name, row)
	}
}

// A movie's or series' locks are kept by the stored row, which a refresh's item
// write leaves alone, so Reset to Provider (#3581) has to clear them in that
// write's transaction: otherwise the edits' locks, a locked poster's among
// them, outlive the reset.
func TestResetToProvider_ItemRowClearsStoredLocks(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	f := newIdentifyLockFixture(pool)
	seriesID := f.seed(t, strconv.FormatInt(820_000_000+time.Now().UnixNano()%100_000_000, 10))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, seriesID)
	})
	tagline := "Typed tagline"
	locks := []int{int(FieldOverview), int(FieldImages)}
	if err := f.items.UpdateMetadata(t.Context(), seriesID, &catalog.MetadataUpdate{Tagline: &tagline, LockedFields: &locks}); err != nil {
		t.Fatal(err)
	}
	remote := &remoteStubProvider{
		slug:     "tvdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Show", Overview: "Provider overview"},
	}

	if _, err := f.service.ProcessWithProviders(t.Context(), ProcessRequest{
		ContentID: seriesID, Language: "en", Mode: ModeManualRefresh, resetToProvider: true,
	}, []Provider{remote}); err != nil {
		t.Fatal(err)
	}

	item, err := f.items.GetByID(t.Context(), seriesID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Tagline != "" || item.Overview != "Provider overview" || len(item.LockedFields) != 0 {
		t.Errorf("tagline=%q overview=%q locked_fields=%v, want no tagline, the provider's overview and no locks", item.Tagline, item.Overview, item.LockedFields)
	}
	// The series' own reset leaves its seasons' and episodes' edits alone.
	f.assertLocksKept(t, seriesID)
}
