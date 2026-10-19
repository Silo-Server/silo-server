package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const testLocalSeasonPoster = "file:///media/tv/Show/Season 01/poster.jpg?v=10-1"

func TestAccumulateSeasonResultsLocalPosterBeatsEarlierRemote(t *testing.T) {
	accumulator := make(map[int]*SeasonResult)
	accumulateSeasonResults(accumulator, []SeasonResult{{
		SeasonNumber: 1, Title: "Season 1", PosterPath: "tvdb://season-1.jpg", PosterThumbhash: "remote-thumb",
	}})
	accumulateSeasonResults(accumulator, []SeasonResult{{
		SeasonNumber: 1, Title: "Local Title", PosterPath: testLocalSeasonPoster,
	}})
	got := accumulator[1]
	if got.PosterPath != testLocalSeasonPoster || got.PosterThumbhash != "" {
		t.Fatalf("poster = %q thumbhash %q, want the local poster", got.PosterPath, got.PosterThumbhash)
	}
	if got.Title != "Season 1" {
		t.Fatalf("title = %q: only artwork is local-first, text keeps chain order", got.Title)
	}

	// A remote poster from a later provider does not displace it, and the
	// displaced remote poster stays available for localization rows.
	accumulateSeasonResults(accumulator, []SeasonResult{{
		SeasonNumber: 1, PosterPath: "tmdb://season-1.jpg", PosterThumbhash: "tmdb-thumb",
	}})
	if accumulator[1].PosterPath != testLocalSeasonPoster || accumulator[1].PosterThumbhash != "" {
		t.Fatalf("poster = %q thumbhash %q, want the local poster to stay without a remote thumbhash",
			accumulator[1].PosterPath, accumulator[1].PosterThumbhash)
	}
	if got := accumulator[1].remotePosterPath; got != "tvdb://season-1.jpg" {
		t.Fatalf("remote poster kept for localization = %q, want the first remote poster", got)
	}

	// Local first: the first later remote poster is kept beside it, and its
	// thumbhash stays with it rather than describing the local poster.
	localFirst := make(map[int]*SeasonResult)
	accumulateSeasonResults(localFirst, []SeasonResult{{SeasonNumber: 1, PosterPath: testLocalSeasonPoster}})
	accumulateSeasonResults(localFirst, []SeasonResult{{
		SeasonNumber: 1, PosterPath: "tvdb://season-1.jpg", PosterThumbhash: "tvdb-thumb",
	}})
	got = localFirst[1]
	if got.PosterPath != testLocalSeasonPoster || got.PosterThumbhash != "" ||
		got.remotePosterPath != "tvdb://season-1.jpg" || got.remotePosterThumbhash != "tvdb-thumb" {
		t.Fatalf("local-first season = %+v, want the local poster with the remote one kept beside it", got)
	}
}

func TestAccumulateEpisodeResultsLocalStillBeatsEarlierRemote(t *testing.T) {
	const localStill = "file:///media/tv/Show/Season 01/Show S01E01-thumb.jpg?v=10-1"
	accumulator := make(map[episodeResultKey]*EpisodeResult)
	accumulateEpisodeResults(accumulator, []EpisodeResult{{
		SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", StillPath: "tvdb://still.jpg", StillThumbhash: "remote-thumb",
	}})
	accumulateEpisodeResults(accumulator, []EpisodeResult{{
		SeasonNumber: 1, EpisodeNumber: 1, StillPath: localStill,
	}})
	accumulateEpisodeResults(accumulator, []EpisodeResult{{
		SeasonNumber: 1, EpisodeNumber: 1, StillPath: "tmdb://still.jpg", StillThumbhash: "tmdb-thumb",
	}})
	got := accumulator[episodeResultKey{seasonNumber: 1, episodeNumber: 1}]
	if got.StillPath != localStill || got.StillThumbhash != "" || got.Title != "Pilot" {
		t.Fatalf("episode = %+v, want the local still and the first provider's title", got)
	}

	// Local first: a later remote still's thumbhash does not fill the local
	// still's empty one.
	localFirst := make(map[episodeResultKey]*EpisodeResult)
	accumulateEpisodeResults(localFirst, []EpisodeResult{{SeasonNumber: 1, EpisodeNumber: 1, StillPath: localStill}})
	accumulateEpisodeResults(localFirst, []EpisodeResult{{
		SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", StillPath: "tvdb://still.jpg", StillThumbhash: "tvdb-thumb",
	}})
	got = localFirst[episodeResultKey{seasonNumber: 1, episodeNumber: 1}]
	if got.StillPath != localStill || got.StillThumbhash != "" || got.Title != "Pilot" {
		t.Fatalf("local-first episode = %+v, want the local still without a remote thumbhash", got)
	}
}

// sidecarLayout prepares one case's filesystem and returns the sidecar path a
// stored local source names.
type sidecarLayout func(t *testing.T, root string) string

var retentionCases = []struct {
	name   string
	layout sidecarLayout
	keep   bool
}{
	{
		name: "library unmounted",
		layout: func(_ *testing.T, root string) string {
			return filepath.Join(root, "missing-mount", "Film", "poster.jpg")
		},
		keep: true,
	},
	{
		name: "empty mount point",
		layout: func(t *testing.T, root string) string {
			mount := filepath.Join(root, "mnt")
			mkdirAll(t, mount)
			return filepath.Join(mount, "poster.jpg")
		},
		keep: true,
	},
	{
		name: "sidecar deleted from a mounted folder",
		layout: func(t *testing.T, root string) string {
			dir := filepath.Join(root, "Film")
			writeTestBytes(t, filepath.Join(dir, "Film.mkv"))
			return filepath.Join(dir, "poster.jpg")
		},
		keep: false,
	},
	{
		name: "sidecar on disk but not offered",
		layout: func(t *testing.T, root string) string {
			path := filepath.Join(root, "Film", "poster.jpg")
			writeTestBytes(t, path)
			return path
		},
		keep: false,
	},
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func writeTestBytes(t *testing.T, path string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("bytes"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// A refresh that does not rediscover cached local art (the provider found no
// sidecar) keeps it unless the sidecar is confirmed gone.
func TestMergeAndPersistKeepsCachedLocalArtworkUntilSidecarIsGone(t *testing.T) {
	const (
		contentID    = "movie:tmdb:7"
		cachedPoster = "local/movies/movie:tmdb:7/0badf00d/poster/original.webp"
		remotePoster = "tmdb://remote-poster.jpg"
	)
	for _, mode := range []RefreshMode{ModeScheduledRefresh, ModeManualRefresh} {
		for _, tc := range retentionCases {
			t.Run(fmt.Sprintf("mode %d/%s", mode, tc.name), func(t *testing.T) {
				localSource := "file://" + tc.layout(t, t.TempDir()) + "?v=12-1"
				h := newTestHarness()
				h.service.SetImageCacheJobEnqueuer(&recordingImageCacheJobEnqueuer{})
				h.service.SetAutoCacheImages(true)
				ctx := withLocalArtworkSearched(context.Background(), localArtworkItem)
				if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
					ContentID:        contentID,
					Type:             "movie",
					Title:            "Film",
					Status:           "matched",
					TmdbID:           "7",
					PosterPath:       cachedPoster,
					PosterSourcePath: localSource,
					PosterThumbhash:  "local-thumb",
					Studios:          []string{},
					Networks:         []string{},
					Countries:        []string{},
					Genres:           []string{},
				}); err != nil {
					t.Fatalf("upsert existing item: %v", err)
				}
				providerRepo := newFakeProviderIDRepo()
				providerRepo.set(contentID, &models.MediaItemProviderID{
					ContentID: contentID, ItemType: "movie", Provider: "tmdb", ProviderID: "7",
				})
				h.service.providerIDRepo = providerRepo

				if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{
					ContentID: contentID,
					Mode:      mode,
					Language:  "en",
				}, &MetadataResult{
					HasMetadata: true,
					Title:       "Film",
					ProviderIDs: map[string]string{"tmdb": "7"},
				}, []RemoteImage{
					{Type: ImagePoster, URL: remotePoster, ProviderID: "tmdb", Rating: 7.5, Language: "en"},
				}, nil, nil, "movie"); err != nil {
					t.Fatalf("mode %d: mergeAndPersist: %v", mode, err)
				}

				got, err := h.itemRepo.GetByID(ctx, contentID)
				if err != nil {
					t.Fatalf("load item: %v", err)
				}
				if tc.keep {
					if got.PosterPath != cachedPoster || got.PosterSourcePath != localSource || got.PosterThumbhash != "local-thumb" {
						t.Fatalf("mode %d: poster = path %q source %q thumb %q, want the cached local poster kept",
							mode, got.PosterPath, got.PosterSourcePath, got.PosterThumbhash)
					}
					return
				}
				if got.PosterSourcePath != remotePoster {
					t.Fatalf("mode %d: poster source = %q, want the remote poster to replace a confirmed-gone sidecar",
						mode, got.PosterSourcePath)
				}
			})
		}
	}
}

func TestPersistSeasonsAndEpisodesKeepsCachedLocalArtworkUntilSidecarIsGone(t *testing.T) {
	const (
		seriesID     = "series:tvdb:123"
		cachedSeason = "local/series/series:tvdb:123/0badf00d/poster/original.webp"
		cachedStill  = "local/series/series:tvdb:123/feedface/still/original.webp"
		remoteSeason = "tvdb://remote-season.jpg"
		remoteStill  = "tvdb://remote-still.jpg"
	)
	for _, tc := range retentionCases {
		t.Run(tc.name, func(t *testing.T) {
			seasonPoster := tc.layout(t, t.TempDir())
			seasonSource := "file://" + seasonPoster + "?v=12-1"
			stillSource := "file://" + filepath.Join(filepath.Dir(seasonPoster), "Show S01E01-thumb.jpg") + "?v=9-1"
			service, _, seasonRepo, episodeRepo := newSeasonEpisodeServiceForTest(seriesID)
			enqueuer := &recordingImageCacheJobEnqueuer{}
			service.SetAutoCacheImages(true)
			service.SetImageCacheJobEnqueuer(enqueuer)
			seasonRepo.seasons[seasonKey(seriesID, 1)] = &models.Season{
				ContentID: "season-1", SeriesID: seriesID, SeasonNumber: 1, Title: "Season 1",
				PosterPath: cachedSeason, PosterSourcePath: seasonSource, PosterThumbhash: "season-thumb",
			}
			episodeRepo.episodes[episodeKey(seriesID, 1, 1)] = &models.Episode{
				ContentID: "episode-1", SeriesID: seriesID, SeasonID: "season-1", SeasonNumber: 1, EpisodeNumber: 1,
				Title: "Pilot", StillPath: cachedStill, StillSourcePath: stillSource, StillThumbhash: "still-thumb",
			}

			service.persistSeasonsAndEpisodes(withLocalArtworkSearched(context.Background(), localArtworkSeason|localArtworkEpisode),
				&models.MediaItem{ContentID: seriesID, Type: "series", TvdbID: "123"},
				map[string]string{"tvdb": "123"}, "en", "en",
				[]SeasonResult{{SeasonNumber: 1, Title: "Season 1", PosterPath: remoteSeason}},
				[]EpisodeResult{{
					ProviderIDs: map[string]string{"tvdb": "ep-1"}, SeasonNumber: 1, EpisodeNumber: 1,
					Title: "Pilot", StillPath: remoteStill,
				}},
				MergeReplaceUnlocked,
			)

			season := seasonRepo.seasons[seasonKey(seriesID, 1)]
			episode := episodeRepo.episodes[episodeKey(seriesID, 1, 1)]
			if !tc.keep {
				if season.PosterSourcePath != remoteSeason || episode.StillSourcePath != remoteStill {
					t.Fatalf("sources = season %q episode %q, want the remote artwork",
						season.PosterSourcePath, episode.StillSourcePath)
				}
				return
			}
			if season.PosterPath != cachedSeason || season.PosterSourcePath != seasonSource || season.PosterThumbhash != "season-thumb" {
				t.Fatalf("season poster = path %q source %q thumb %q, want the cached local poster kept",
					season.PosterPath, season.PosterSourcePath, season.PosterThumbhash)
			}
			if episode.StillPath != cachedStill || episode.StillSourcePath != stillSource || episode.StillThumbhash != "still-thumb" {
				t.Fatalf("episode still = path %q source %q thumb %q, want the cached local still kept",
					episode.StillPath, episode.StillSourcePath, episode.StillThumbhash)
			}
			for _, job := range enqueuer.inputs {
				if job.SourcePath == remoteSeason || job.SourcePath == remoteStill {
					t.Fatalf("queued remote artwork over kept local artwork: %+v", job)
				}
			}
		})
	}
}

// Only the sidecar's own folder can confirm it is gone. A missing folder is
// what a vanished mount looks like, even while a sibling mount or the folder
// above it still lists entries, so the local copy stays.
func TestLocalArtworkRetentionTrustsOnlyTheSidecarFolder(t *testing.T) {
	gone := func(path string) bool {
		return newLocalArtworkRetention(context.Background(), "movie-1", localArtworkItem).sidecarGone("file://" + path + "?v=1-1")
	}
	media := filepath.Join(t.TempDir(), "media")
	writeTestBytes(t, filepath.Join(media, "disk1", "Film A", "Film A.mkv"))

	// A nested mount gone entirely, with its sibling still mounted.
	if gone(filepath.Join(media, "disk2", "Film B", "poster.jpg")) {
		t.Fatal("a vanished nested mount must keep its local copy")
	}
	// A sidecar directly in an unmounted library root (an empty mount point).
	mkdirAll(t, filepath.Join(media, "disk3"))
	if gone(filepath.Join(media, "disk3", "poster.jpg")) {
		t.Fatal("an empty mount point must keep its local copy")
	}
	// The mounted folder lists entries and the sidecar is missing.
	if !gone(filepath.Join(media, "disk1", "Film A", "poster.jpg")) {
		t.Fatal("a sidecar missing from a mounted folder is gone")
	}
}

// A sidecar folder that cannot be read proves nothing, so the local copy stays.
func TestLocalArtworkRetentionKeepsArtInUnreadableFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	dir := filepath.Join(t.TempDir(), "Film")
	writeTestBytes(t, filepath.Join(dir, "Film.mkv"))
	if err := os.Chmod(dir, 0o311); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	retention := newLocalArtworkRetention(context.Background(), "movie-1", localArtworkItem)
	if retention.sidecarGone("file://" + filepath.Join(dir, "poster.jpg") + "?v=1-1") {
		t.Fatal("an unreadable folder must keep the cached local copy")
	}
}

// seasonArtProvider is a series provider that answers with fixed seasons.
type seasonArtProvider struct {
	slug     string
	metadata *MetadataResult
	seasons  []SeasonResult
}

func (p *seasonArtProvider) Slug() string       { return p.slug }
func (p *seasonArtProvider) Name() string       { return p.slug }
func (p *seasonArtProvider) ForTypes() []string { return []string{"series"} }

func (p *seasonArtProvider) GetMetadata(context.Context, MetadataRequest) (*MetadataResult, error) {
	if p.metadata == nil {
		return &MetadataResult{}, nil
	}
	cp := *p.metadata
	return &cp, nil
}

func (p *seasonArtProvider) GetSeasons(context.Context, SeasonsRequest) ([]SeasonResult, error) {
	return append([]SeasonResult(nil), p.seasons...), nil
}

func (p *seasonArtProvider) GetEpisodes(context.Context, EpisodesRequest) ([]EpisodeResult, error) {
	return nil, nil
}

// A refresh in a language other than the series' canonical one writes only
// localization rows. Local season art belongs to the base row, so the
// localization row takes the provider's localized season poster that the local
// one displaced; the canonical refresh still puts the local poster first.
func TestLocalizationRefreshKeepsProviderSeasonPoster(t *testing.T) {
	const (
		seriesID    = "series:tvdb:1"
		seasonID    = "season-1"
		localPoster = "file:///media/Show/season01-poster.jpg?v=1-1"
	)
	h := newTestHarness()
	ctx := context.Background()
	seasonRepo := newFakeSeasonRepo()
	localizations := newFakeSeasonLocalizationRepo()
	h.service.seasonRepo = seasonRepo
	h.service.episodeRepo = newFakeEpisodeRepo()
	h.service.seasonLocalizationRepo = localizations
	h.service.SetAutoCacheImages(true)
	h.service.SetImageCacheJobEnqueuer(&recordingImageCacheJobEnqueuer{})
	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(seriesID, &models.MediaItemProviderID{ContentID: seriesID, ItemType: "series", Provider: "tvdb", ProviderID: "1"})
	h.service.providerIDRepo = providerRepo
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: seriesID, Type: "series", Title: "Show", Status: "matched", TvdbID: "1",
		DefaultMetadataLanguage: "en",
		Studios:                 []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	seasonRepo.seasons[seasonKey(seriesID, 1)] = &models.Season{
		ContentID: seasonID, SeriesID: seriesID, SeasonNumber: 1, Title: "Season 1",
		PosterPath: "tvdb/series/1/seasons/1/poster/original.webp", PosterSourcePath: "tvdb://en-season-1.jpg",
	}

	refresh := func(language, remotePoster string) {
		t.Helper()
		remote := &seasonArtProvider{
			slug:     "tvdb",
			metadata: &MetadataResult{HasMetadata: true, Title: "Show"},
			seasons:  []SeasonResult{{SeasonNumber: 1, Title: "Season 1", PosterPath: remotePoster}},
		}
		local := &seasonArtProvider{slug: "local-art", seasons: []SeasonResult{{SeasonNumber: 1, PosterPath: localPoster}}}
		if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
			ContentID: seriesID, Mode: ModeManualRefresh, Language: language,
		}, []Provider{remote, local}); err != nil {
			t.Fatalf("%s refresh: %v", language, err)
		}
	}

	// No provider poster at all: the local poster must not land on the
	// localization row (it belongs to the base row).
	refresh("fr", "")
	if loc := localizations.localizations[seasonLocalizationKey(seasonID, "fr")]; loc != nil && (loc.PosterPath != "" || loc.PosterSourcePath != "") {
		t.Fatalf("fr season localization took local art: path %q source %q", loc.PosterPath, loc.PosterSourcePath)
	}

	refresh("fr", "tvdb://fr-season-1.jpg")
	loc := localizations.localizations[seasonLocalizationKey(seasonID, "fr")]
	if loc == nil || loc.PosterSourcePath != "tvdb://fr-season-1.jpg" {
		t.Fatalf("fr season localization = %+v, want the provider's French poster", loc)
	}
	if got := seasonRepo.seasons[seasonKey(seriesID, 1)].PosterSourcePath; got != "tvdb://en-season-1.jpg" {
		t.Fatalf("a localization refresh changed the base season poster source to %q", got)
	}

	refresh("en", "tvdb://en-season-1.jpg")
	if got := seasonRepo.seasons[seasonKey(seriesID, 1)].PosterSourcePath; got != localPoster {
		t.Fatalf("canonical refresh: base season poster source = %q, want the local poster", got)
	}
}

// A refresh that never asked the local provider for artwork (local metadata
// off, an Identify) treats cached local art like any stored artwork: a rated
// remote image replaces it, and no library folder is consulted.
func TestCachedLocalArtworkNotKeptWithoutLocalSearch(t *testing.T) {
	const (
		contentID    = "movie:tmdb:7"
		cachedPoster = "local/movies/movie:tmdb:7/0badf00d/poster/original.webp"
		remotePoster = "tmdb://remote-poster.jpg"
	)
	unmounted := "file://" + filepath.Join(t.TempDir(), "missing-mount", "Film", "poster.jpg") + "?v=12-1"
	h := newTestHarness()
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: contentID, Type: "movie", Title: "Film", Status: "matched", TmdbID: "7",
		PosterPath: cachedPoster, PosterSourcePath: unmounted, PosterThumbhash: "local-thumb",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}
	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(contentID, &models.MediaItemProviderID{ContentID: contentID, ItemType: "movie", Provider: "tmdb", ProviderID: "7"})
	h.service.providerIDRepo = providerRepo

	if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: contentID, Mode: ModeManualRefresh, Language: "en"},
		&MetadataResult{HasMetadata: true, Title: "Film", ProviderIDs: map[string]string{"tmdb": "7"}},
		[]RemoteImage{{Type: ImagePoster, URL: remotePoster, ProviderID: "tmdb", Rating: 7.5, Language: "en"}},
		nil, nil, "movie"); err != nil {
		t.Fatalf("mergeAndPersist: %v", err)
	}
	got, err := h.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if got.PosterSourcePath != remotePoster {
		t.Fatalf("poster source = %q, want the remote poster when the refresh did not search local art", got.PosterSourcePath)
	}
	retention := newLocalArtworkRetention(ctx, contentID, localArtworkItem)
	if retention.keep(ImagePoster, remotePoster, unmounted) || len(retention.probes) != 0 {
		t.Fatalf("an unmarked refresh kept local art or probed a folder (%d probes)", len(retention.probes))
	}
}

// imageArtProvider is a movie provider with fixed metadata and images.
type imageArtProvider struct {
	slug     string
	metadata *MetadataResult
	images   []RemoteImage
}

func (p *imageArtProvider) Slug() string       { return p.slug }
func (p *imageArtProvider) Name() string       { return p.slug }
func (p *imageArtProvider) ForTypes() []string { return []string{"movie"} }

func (p *imageArtProvider) GetMetadata(context.Context, MetadataRequest) (*MetadataResult, error) {
	if p.metadata == nil {
		return &MetadataResult{}, nil
	}
	cp := *p.metadata
	return &cp, nil
}

func (p *imageArtProvider) GetImages(context.Context, ImageRequest) ([]RemoteImage, error) {
	return append([]RemoteImage(nil), p.images...), nil
}

// localHintArtProvider stands in for the local (NFO) provider, which the
// pipeline recognizes by IdentityHintProvider.
type localHintArtProvider struct{ imageArtProvider }

func (p *localHintArtProvider) IdentityHints(context.Context, SearchQuery) map[string]string {
	return nil
}

func TestRefreshMarksLocalArtworkSearchOnlyWhenTheLocalProviderRan(t *testing.T) {
	const (
		contentID    = "movie:tmdb:7"
		cachedPoster = "local/movies/movie:tmdb:7/0badf00d/poster/original.webp"
		remotePoster = "tmdb://remote-poster.jpg"
	)
	unmounted := "file://" + filepath.Join(t.TempDir(), "missing-mount", "Film", "poster.jpg") + "?v=12-1"
	remote := &imageArtProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Film", ProviderIDs: map[string]string{"tmdb": "7"}},
		images:   []RemoteImage{{Type: ImagePoster, URL: remotePoster, ProviderID: "tmdb", Rating: 7.5, Language: "en"}},
	}
	// The local provider ran but found nothing: the library is unmounted.
	local := &localHintArtProvider{imageArtProvider{slug: "nfo"}}

	for _, tc := range []struct {
		name  string
		chain []Provider
		want  string
	}{
		{"local provider searched", []Provider{remote, local}, unmounted},
		{"local provider not in the chain", []Provider{remote}, remotePoster},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			ctx := context.Background()
			h.service.SetAutoCacheImages(true)
			h.service.SetImageCacheJobEnqueuer(&recordingImageCacheJobEnqueuer{})
			providerRepo := newFakeProviderIDRepo()
			providerRepo.set(contentID, &models.MediaItemProviderID{ContentID: contentID, ItemType: "movie", Provider: "tmdb", ProviderID: "7"})
			h.service.providerIDRepo = providerRepo
			if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
				ContentID: contentID, Type: "movie", Title: "Film", Status: "matched", TmdbID: "7",
				DefaultMetadataLanguage: "en",
				PosterPath:              cachedPoster, PosterSourcePath: unmounted, PosterThumbhash: "local-thumb",
				Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
			}); err != nil {
				t.Fatalf("seed item: %v", err)
			}
			if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
				ContentID: contentID, Mode: ModeManualRefresh, Language: "en",
			}, tc.chain); err != nil {
				t.Fatalf("refresh: %v", err)
			}
			got, err := h.itemRepo.GetByID(ctx, contentID)
			if err != nil {
				t.Fatalf("load item: %v", err)
			}
			if got.PosterSourcePath != tc.want {
				t.Fatalf("poster source = %q, want %q", got.PosterSourcePath, tc.want)
			}
		})
	}
}

// Provider chains are configured per level: a refresh whose season chain ran
// the local provider but whose episode chain did not keeps undiscovered local
// season art and lets remote stills replace cached local ones.
func TestLocalArtworkRetentionFollowsTheLevelThatSearched(t *testing.T) {
	const (
		seriesID     = "series:tvdb:123"
		cachedSeason = "local/series/series:tvdb:123/0badf00d/poster/original.webp"
		cachedStill  = "local/series/series:tvdb:123/feedface/still/original.webp"
		remoteSeason = "tvdb://remote-season.jpg"
		remoteStill  = "tvdb://remote-still.jpg"
	)
	unmounted := filepath.Join(t.TempDir(), "missing-mount", "Show", "Season 01")
	seasonSource := "file://" + filepath.Join(unmounted, "poster.jpg") + "?v=12-1"
	stillSource := "file://" + filepath.Join(unmounted, "Show S01E01-thumb.jpg") + "?v=9-1"
	service, _, seasonRepo, episodeRepo := newSeasonEpisodeServiceForTest(seriesID)
	seasonRepo.seasons[seasonKey(seriesID, 1)] = &models.Season{
		ContentID: "season-1", SeriesID: seriesID, SeasonNumber: 1, Title: "Season 1",
		PosterPath: cachedSeason, PosterSourcePath: seasonSource,
	}
	episodeRepo.episodes[episodeKey(seriesID, 1, 1)] = &models.Episode{
		ContentID: "episode-1", SeriesID: seriesID, SeasonID: "season-1", SeasonNumber: 1, EpisodeNumber: 1,
		Title: "Pilot", StillPath: cachedStill, StillSourcePath: stillSource,
	}

	service.persistSeasonsAndEpisodes(withLocalArtworkSearched(context.Background(), localArtworkSeason),
		&models.MediaItem{ContentID: seriesID, Type: "series", TvdbID: "123"},
		map[string]string{"tvdb": "123"}, "en", "en",
		[]SeasonResult{{SeasonNumber: 1, Title: "Season 1", PosterPath: remoteSeason}},
		[]EpisodeResult{{
			ProviderIDs: map[string]string{"tvdb": "ep-1"}, SeasonNumber: 1, EpisodeNumber: 1,
			Title: "Pilot", StillPath: remoteStill,
		}},
		MergeReplaceUnlocked,
	)

	if got := seasonRepo.seasons[seasonKey(seriesID, 1)].PosterSourcePath; got != seasonSource {
		t.Fatalf("season poster source = %q, want the cached local poster kept", got)
	}
	if got := episodeRepo.episodes[episodeKey(seriesID, 1, 1)].StillSourcePath; got != remoteStill {
		t.Fatalf("episode still source = %q, want the remote still: the episode chain did not search", got)
	}
}

// localHintEpisodeProvider stands in for the local (NFO) provider at the
// season and episode levels; a non-nil err makes both calls fail.
type localHintEpisodeProvider struct {
	seasonArtProvider
	err error
}

func (p *localHintEpisodeProvider) IdentityHints(context.Context, SearchQuery) map[string]string {
	return nil
}

func (p *localHintEpisodeProvider) GetSeasons(ctx context.Context, req SeasonsRequest) ([]SeasonResult, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.seasonArtProvider.GetSeasons(ctx, req)
}

func (p *localHintEpisodeProvider) GetEpisodes(ctx context.Context, req EpisodesRequest) ([]EpisodeResult, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.seasonArtProvider.GetEpisodes(ctx, req)
}

// A season or episode refresh, like a full refresh, counts a level as searched
// only when the local provider answered: a failed call never looked at the
// sidecars, so it cannot confirm cached local art is gone.
func TestTargetRefreshMarksLocalArtworkSearchOnlyWhenTheLocalProviderAnswered(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want localArtworkLevel
	}{
		{"local provider answered", nil, localArtworkSeason | localArtworkEpisode},
		{"local provider failed", errors.New("season.nfo unreadable"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := &localHintEpisodeProvider{seasonArtProvider: seasonArtProvider{slug: "nfo"}, err: tc.err}
			service := &MetadataService{chainCache: map[string]chainCacheEntry{
				"0:season":  {providers: []Provider{local}, expiresAt: time.Now().Add(time.Hour)},
				"0:episode": {providers: []Provider{local}, expiresAt: time.Now().Add(time.Hour)},
			}}
			ctx := context.Background()
			_, seasonSearched, err := service.fetchTargetSeasonResults(ctx, nil, 0, "en", 1, seriesChildLocalContext{}, false)
			if err != nil {
				t.Fatalf("fetch seasons: %v", err)
			}
			_, episodeSearched, err := service.fetchTargetEpisodeResults(ctx, nil, 0, "en", 1, 1, seriesChildLocalContext{}, false)
			if err != nil {
				t.Fatalf("fetch episodes: %v", err)
			}
			if got := seasonSearched | episodeSearched; got != tc.want {
				t.Fatalf("searched levels = %b, want %b", got, tc.want)
			}
		})
	}
}
