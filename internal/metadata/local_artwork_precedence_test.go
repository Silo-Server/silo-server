package metadata

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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

	// A remote poster from a later provider does not displace it.
	accumulateSeasonResults(accumulator, []SeasonResult{{SeasonNumber: 1, PosterPath: "tmdb://season-1.jpg"}})
	if accumulator[1].PosterPath != testLocalSeasonPoster {
		t.Fatalf("poster = %q, want the local poster to stay", accumulator[1].PosterPath)
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
		SeasonNumber: 1, EpisodeNumber: 1, StillPath: "tmdb://still.jpg",
	}})
	got := accumulator[episodeResultKey{seasonNumber: 1, episodeNumber: 1}]
	if got.StillPath != localStill || got.StillThumbhash != "" || got.Title != "Pilot" {
		t.Fatalf("episode = %+v, want the local still and the first provider's title", got)
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
				ctx := context.Background()
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

			service.persistSeasonsAndEpisodes(context.Background(),
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

// When the sidecar's own folder is gone too (a moved or deleted item), a
// library root that still lists entries shows the library is mounted.
func TestLocalArtworkRetentionReadsLibraryRoot(t *testing.T) {
	library := filepath.Join(t.TempDir(), "library")
	mkdirAll(t, library)
	source := "file://" + filepath.Join(library, "Moved Film", "poster.jpg") + "?v=1-1"
	retention := &localArtworkRetention{ctx: context.Background(), rootsLoaded: true, roots: []string{library}}

	if retention.sidecarGone(source) {
		t.Fatal("an empty library root looks unmounted; the local copy must stay")
	}
	writeTestBytes(t, filepath.Join(library, "Other Film", "Other Film.mkv"))
	if !retention.sidecarGone(source) {
		t.Fatal("a mounted library root without the folder confirms the sidecar is gone")
	}
	retention.roots = []string{filepath.Join(library, "Other Film")}
	if retention.sidecarGone(source) {
		t.Fatal("a root that does not hold the sidecar proves nothing about it")
	}
}
