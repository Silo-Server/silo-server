package metadata

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A season backdrop is provider-owned: a refresh that heard from every season
// provider replaces or clears it, and anything less keeps what is stored.
func TestPersistSeasonBackdrops(t *testing.T) {
	const (
		seriesID   = "series-tvdb-9"
		cachedA    = "tvdb/series/9/seasons/1/backdrop/a/original.webp"
		sourceA    = "tvdb://season-1-a.jpg"
		sourceB    = "tvdb://season-1-b.jpg"
		thumbhashA = "thumb-a"
	)
	stored := models.Season{
		ContentID: "season-1", SeriesID: seriesID, SeasonNumber: 1, Title: "Season 1",
		BackdropPath: cachedA, BackdropSourcePath: sourceA, BackdropThumbhash: thumbhashA,
	}
	episode := EpisodeResult{ProviderIDs: map[string]string{"tvdb": "ep-1"}, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"}
	for _, tc := range []struct {
		name      string
		authority seasonBackdropAuthority
		language  string
		locked    bool
		// offered is nil when the refresh brought no season record, only
		// the season's episode.
		offered                *string
		wantPath, wantSource   string
		wantThumbhash, wantJob string
	}{
		{name: "same source keeps the cached copy", authority: seasonBackdropsAnswered, offered: ptr(sourceA),
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA, wantJob: sourceA},
		{name: "new source replaces the copy at once", authority: seasonBackdropsAnswered, offered: ptr(sourceB),
			wantPath: sourceB, wantSource: sourceB, wantJob: sourceB},
		{name: "no backdrop offered clears it", authority: seasonBackdropsAnswered, offered: ptr("")},
		{name: "a refresh missing a provider keeps it", authority: seasonBackdropsKept, offered: ptr(""),
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA},
		{name: "the images lock keeps it", authority: seasonBackdropsAnswered, locked: true, offered: ptr(sourceB),
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA},
		{name: "a localization write keeps it", authority: seasonBackdropsAnswered, language: "de", offered: ptr(""),
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA},
		{name: "a season known only by its episodes keeps it", authority: seasonBackdropsAnswered,
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA},
		{name: "identify clears a season known only by its episodes", authority: seasonBackdropsReidentified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _, seasonRepo, _ := newSeasonEpisodeServiceForTest(seriesID)
			enqueuer := &recordingImageCacheJobEnqueuer{}
			service.SetAutoCacheImages(true)
			service.SetImageCacheJobEnqueuer(enqueuer)
			existing := stored
			seasonRepo.seasons[seasonKey(seriesID, 1)] = &existing

			series := &models.MediaItem{ContentID: seriesID, Type: "series", TvdbID: "9"}
			if tc.locked {
				series.LockedFields = []int{int(FieldImages)}
			}
			var seasons []SeasonResult
			if tc.offered != nil {
				seasons = []SeasonResult{{SeasonNumber: 1, Title: "Season 1", BackdropPath: *tc.offered}}
			}
			language := tc.language
			if language == "" {
				language = "en"
			}
			service.persistSeasonsAndEpisodes(withSeasonBackdropAuthority(context.Background(), tc.authority),
				series, map[string]string{"tvdb": "9"}, "en", language, seasons, []EpisodeResult{episode}, MergeReplaceUnlocked)

			got := seasonRepo.seasons[seasonKey(seriesID, 1)]
			if got.BackdropPath != tc.wantPath || got.BackdropSourcePath != tc.wantSource || got.BackdropThumbhash != tc.wantThumbhash {
				t.Errorf("backdrop = %q from %q (thumbhash %q), want %q from %q (thumbhash %q)",
					got.BackdropPath, got.BackdropSourcePath, got.BackdropThumbhash, tc.wantPath, tc.wantSource, tc.wantThumbhash)
			}
			var job *EnqueueImageCacheJobInput
			for i := range enqueuer.inputs {
				if enqueuer.inputs[i].ImageType == ImageCacheImageBackdrop {
					job = &enqueuer.inputs[i]
				}
			}
			switch {
			case tc.wantJob == "" && job != nil:
				t.Errorf("queued a backdrop job for %q, want none", job.SourcePath)
			case tc.wantJob != "" && (job == nil || job.SourcePath != tc.wantJob || job.TargetType != ImageCacheTargetSeason):
				t.Errorf("backdrop job = %+v, want a season job for %q", job, tc.wantJob)
			}
		})
	}
}

func ptr(s string) *string { return &s }
