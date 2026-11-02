package metadata

import (
	"context"
	"errors"
	"testing"
	"time"

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
	offer := func(path string) *string { return &path }
	episode := EpisodeResult{ProviderIDs: map[string]string{"tvdb": "ep-1"}, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"}
	for _, tc := range []struct {
		name      string
		authority seasonBackdropAuthority
		language  string
		fillEmpty bool
		locked    bool
		// episodeSeason is the season of the refresh's one episode; 0 means 1.
		episodeSeason int
		// failWrite makes every write of season 1 fail.
		failWrite bool
		// offered is nil when the refresh brought no season record, only
		// the season's episode.
		offered                *string
		wantPath, wantSource   string
		wantThumbhash, wantJob string
	}{
		{name: "same source keeps the cached copy", authority: seasonBackdropsAnswered, offered: offer(sourceA),
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA, wantJob: sourceA},
		{name: "new source replaces the copy at once", authority: seasonBackdropsAnswered, offered: offer(sourceB),
			wantPath: sourceB, wantSource: sourceB, wantJob: sourceB},
		{name: "no backdrop offered clears it", authority: seasonBackdropsAnswered, offered: offer("")},
		{name: "a refresh missing a provider keeps it", authority: seasonBackdropsKept, offered: offer(""),
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA},
		{name: "the images lock keeps it", authority: seasonBackdropsAnswered, locked: true, offered: offer(sourceB),
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA},
		{name: "a fill-empty refresh keeps it", authority: seasonBackdropsAnswered, fillEmpty: true, offered: offer(sourceB),
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA},
		{name: "a localization write keeps it", authority: seasonBackdropsAnswered, language: "de", offered: offer(""),
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA},
		{name: "a season known only by its episodes keeps it", authority: seasonBackdropsAnswered,
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA},
		{name: "identify clears a season known only by its episodes", authority: seasonBackdropsReidentified},
		{name: "a season the refresh names nowhere keeps it", authority: seasonBackdropsAnswered, episodeSeason: 2,
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA},
		{name: "identify clears a season the new match names nowhere", authority: seasonBackdropsReidentified, episodeSeason: 2},
		{name: "identify keeps a named season whose write failed", authority: seasonBackdropsReidentified, offered: offer(""), failWrite: true,
			wantPath: cachedA, wantSource: sourceA, wantThumbhash: thumbhashA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _, seasonRepo, _ := newSeasonEpisodeServiceForTest(seriesID)
			enqueuer := &recordingImageCacheJobEnqueuer{}
			service.SetAutoCacheImages(true)
			service.SetImageCacheJobEnqueuer(enqueuer)
			existing := stored
			seasonRepo.seasons[seasonKey(seriesID, 1)] = &existing
			if tc.failWrite {
				seasonRepo.bulkUpsertErr = errors.New("write failed")
				seasonRepo.upsertErrors[seasonKey(seriesID, 1)] = errors.New("write failed")
			}

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
			mergeMode := MergeReplaceUnlocked
			if tc.fillEmpty {
				mergeMode = MergeFillEmpty
			}
			refreshEpisode := episode
			if tc.episodeSeason != 0 {
				refreshEpisode.SeasonNumber = tc.episodeSeason
			}
			service.persistSeasonsAndEpisodes(withSeasonBackdropAuthority(context.Background(), tc.authority),
				series, map[string]string{"tvdb": "9"}, "en", language, seasons, []EpisodeResult{refreshEpisode}, mergeMode)

			got := seasonRepo.seasons[seasonKey(seriesID, 1)]
			if got.BackdropPath != tc.wantPath || got.BackdropSourcePath != tc.wantSource || got.BackdropThumbhash != tc.wantThumbhash {
				t.Errorf("backdrop = %q from %q (thumbhash %q), want %q from %q (thumbhash %q)",
					got.BackdropPath, got.BackdropSourcePath, got.BackdropThumbhash, tc.wantPath, tc.wantSource, tc.wantThumbhash)
			}
			job := backdropJob(enqueuer.inputs)
			switch {
			case tc.wantJob == "" && job != nil:
				t.Errorf("queued a backdrop job for %q, want none", job.SourcePath)
			case tc.wantJob != "" && (job == nil || job.SourcePath != tc.wantJob || job.TargetType != ImageCacheTargetSeason):
				t.Errorf("backdrop job = %+v, want a season job for %q", job, tc.wantJob)
			}
		})
	}
}

func backdropJob(inputs []EnqueueImageCacheJobInput) *EnqueueImageCacheJobInput {
	for i := range inputs {
		if inputs[i].ImageType == ImageCacheImageBackdrop {
			return &inputs[i]
		}
	}
	return nil
}

// An Identify whose corrected match has no seasons or episodes at all still
// clears the previous match's season backdrops.
func TestIdentifyWithoutChildMetadataClearsSeasonBackdrops(t *testing.T) {
	h := newTestHarness()
	seasonRepo := newFakeSeasonRepo()
	h.service.seasonRepo = seasonRepo
	ctx := withSeasonBackdropAuthority(context.Background(), seasonBackdropsReidentified)
	const contentID = "series:tvdb:200"
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: contentID, Type: "series", Title: "Corrected Show", Status: "matched", TvdbID: "200",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	seasonRepo.seasons[seasonKey(contentID, 1)] = &models.Season{
		ContentID: "season-1", SeriesID: contentID, SeasonNumber: 1,
		BackdropPath: "tvdb://previous-show.jpg", BackdropSourcePath: "tvdb://previous-show.jpg",
	}

	if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: contentID, Mode: ModeIdentify, Language: "en"},
		&MetadataResult{HasMetadata: true, Title: "Corrected Show", ProviderIDs: map[string]string{"tvdb": "200"}},
		nil, nil, nil, "series"); err != nil {
		t.Fatal(err)
	}
	if got := seasonRepo.seasons[seasonKey(contentID, 1)]; got.BackdropPath != "" || got.BackdropSourcePath != "" {
		t.Fatalf("season backdrop = %q from %q, want it cleared", got.BackdropPath, got.BackdropSourcePath)
	}
}

// Only a season fetch where every provider answered may decide backdrops: a
// scoped 404 is no answer either.
func TestTargetSeasonFetchIsCompleteOnlyWhenEveryProviderAnswered(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"provider answered", nil, true},
		{"provider returned 404", errors.New("tvdb: HTTP 404"), false},
		{"provider failed", errors.New("tvdb: timeout"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &localHintEpisodeProvider{seasonArtProvider: seasonArtProvider{slug: "tvdb"}, err: tc.err}
			service := &MetadataService{chainCache: map[string]chainCacheEntry{
				"0:season": {providers: []Provider{provider}, expiresAt: time.Now().Add(time.Hour)},
			}}
			_, _, complete, err := service.fetchTargetSeasonResults(context.Background(), nil, 0, "en", 1, seriesChildLocalContext{}, false)
			if err != nil {
				t.Fatalf("fetch seasons: %v", err)
			}
			if complete != tc.want {
				t.Fatalf("complete = %v, want %v", complete, tc.want)
			}
		})
	}
}
