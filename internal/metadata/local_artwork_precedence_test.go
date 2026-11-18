package metadata

import (
	"context"

	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

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

const testFilmContentID = "movie:tmdb:7"

// newTestFilmHarness returns a harness whose item repository holds existing
// as the matched movie "Film" (movie:tmdb:7) with TMDB ID 7 on record.
func newTestFilmHarness(ctx context.Context, t *testing.T, existing models.MediaItem) *testHarness {
	t.Helper()
	h := newTestHarness()
	existing.ContentID, existing.Type, existing.Title, existing.Status, existing.TmdbID = testFilmContentID, "movie", "Film", "matched", "7"
	existing.Studios, existing.Networks, existing.Countries, existing.Genres = []string{}, []string{}, []string{}, []string{}
	if err := h.itemRepo.Upsert(ctx, &existing); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}
	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(testFilmContentID, &models.MediaItemProviderID{ContentID: testFilmContentID, ItemType: "movie", Provider: "tmdb", ProviderID: "7"})
	h.service.providerIDRepo = providerRepo
	return h
}

// mergeTestFilm merges a TMDB result for the film offering images and
// returns the stored item.
func mergeTestFilm(ctx context.Context, t *testing.T, h *testHarness, mode RefreshMode, images []RemoteImage) *models.MediaItem {
	t.Helper()
	if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: testFilmContentID, Mode: mode, Language: "en"},
		&MetadataResult{HasMetadata: true, Title: "Film", ProviderIDs: map[string]string{"tmdb": "7"}},
		images, nil, nil, "movie"); err != nil {
		t.Fatalf("mergeAndPersist: %v", err)
	}
	got, err := h.itemRepo.GetByID(ctx, testFilmContentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	return got
}

// A refresh that does not rediscover cached local art (the provider found no
// sidecar) keeps it unless the sidecar is confirmed gone.
func TestMergeAndPersistKeepsCachedLocalArtworkUntilSidecarIsGone(t *testing.T) {
	const (
		cachedPoster = "local/movies/movie:tmdb:7/0badf00d/poster/original.webp"
		remotePoster = "tmdb://remote-poster.jpg"
	)
	for _, mode := range []RefreshMode{ModeScheduledRefresh, ModeManualRefresh} {
		for _, tc := range retentionCases {
			t.Run(fmt.Sprintf("mode %d/%s", mode, tc.name), func(t *testing.T) {
				localSource := "file://" + tc.layout(t, t.TempDir()) + "?v=12-1"
				ctx := withLocalArtworkSearched(context.Background(), localArtworkItem)
				h := newTestFilmHarness(ctx, t, models.MediaItem{
					PosterPath: cachedPoster, PosterSourcePath: localSource, PosterThumbhash: "local-thumb",
				})
				h.service.SetImageCacheJobEnqueuer(&recordingImageCacheJobEnqueuer{})
				h.service.SetAutoCacheImages(true)

				got := mergeTestFilm(ctx, t, h, mode,
					[]RemoteImage{{Type: ImagePoster, URL: remotePoster, ProviderID: "tmdb", Rating: 7.5, Language: "en"}})
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
