package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestBrowseResponseIncludesTotalExact(t *testing.T) {
	data, err := json.Marshal(browseResponse{
		Total:      3,
		TotalExact: true,
		HasMore:    false,
		Items:      []itemListResponse{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"total_exact":true`) {
		t.Fatalf("browse response missing total_exact: %s", data)
	}
}

type recordingEpisodeArtworkResolver struct {
	requests []artworkurl.TargetRequest
}

func (*recordingEpisodeArtworkResolver) ResolveImageURL(context.Context, string, string) string {
	return ""
}

func (*recordingEpisodeArtworkResolver) ResolveImageURLs(context.Context, []string, string) map[string]string {
	return nil
}

func (r *recordingEpisodeArtworkResolver) ResolveArtworkTargetsWithExpiry(
	ctx context.Context,
	targets []artworkurl.Target,
	variant string,
) map[string]catalog.ResolvedImageURL {
	requests := make([]artworkurl.TargetRequest, 0, len(targets))
	for _, target := range targets {
		requests = append(requests, artworkurl.TargetRequest{Target: target, Variant: variant})
	}
	resolved := r.ResolveArtworkTargetRequestsWithExpiry(ctx, requests)
	result := make(map[string]catalog.ResolvedImageURL, len(targets))
	for _, request := range requests {
		result[request.Target.CacheKey()] = resolved[request.CacheKey()]
	}
	return result
}

func (r *recordingEpisodeArtworkResolver) ResolveArtworkTargetRequestsWithExpiry(
	_ context.Context,
	requests []artworkurl.TargetRequest,
) map[string]catalog.ResolvedImageURL {
	r.requests = append(r.requests, requests...)
	result := make(map[string]catalog.ResolvedImageURL, len(requests))
	for _, request := range requests {
		result[request.CacheKey()] = catalog.ResolvedImageURL{URL: "resolved:" + request.Target.Surface}
	}
	return result
}

func TestEpisodeSeriesFallbackResolvesThroughSeriesArtworkTarget(t *testing.T) {
	for _, test := range []struct {
		name        string
		series      *models.MediaItem
		wantSurface string
		wantSlot    string
		wantPath    string
	}{
		{
			name: "backdrop",
			series: &models.MediaItem{
				ContentID:         "series-1",
				BackdropPath:      "provider://series-1/backdrop.jpg",
				BackdropThumbhash: "series-backdrop-thumbhash",
			},
			wantSurface: artworkurl.SurfaceItemBackdrops,
			wantSlot:    artworkkey.ImageTypeBackdrop,
			wantPath:    "provider://series-1/backdrop.jpg",
		},
		{
			name: "poster when backdrop is absent",
			series: &models.MediaItem{
				ContentID:       "series-1",
				PosterPath:      "provider://series-1/poster.jpg",
				PosterThumbhash: "series-poster-thumbhash",
			},
			wantSurface: artworkurl.SurfaceItemPosters,
			wantSlot:    artworkkey.ImageTypePoster,
			wantPath:    "provider://series-1/poster.jpg",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fallback, ok := episodeImageFallbackForSeries(test.series)
			if !ok {
				t.Fatal("series artwork did not produce an episode image fallback")
			}
			episode := &models.Episode{ContentID: "episode-1", SeriesID: test.series.ContentID}
			response, target := episodeResponseShell(episode, fallback)

			resolver := &recordingEpisodeArtworkResolver{}
			detailSvc := &catalog.DetailService{}
			detailSvc.SetImageResolver(resolver)
			request := artworkurl.TargetRequest{Target: target, Variant: artworkkey.VariantW300}
			resolved := detailSvc.PresignArtworkTargetRequestsWithExpiry(t.Context(), []artworkurl.TargetRequest{request})
			response.StillURL = resolved[request.CacheKey()].URL

			if response.StillURL != "resolved:"+test.wantSurface {
				t.Fatalf("fallback still URL = %q, want %s resolution", response.StillURL, test.wantSurface)
			}
			if len(resolver.requests) != 1 {
				t.Fatalf("resolved target requests = %d, want 1", len(resolver.requests))
			}
			got := resolver.requests[0].Target
			if got.Surface != test.wantSurface || len(got.Keys) != 1 || got.Keys[0] != test.series.ContentID || got.Slot != test.wantSlot {
				t.Fatalf("fallback target = %#v, want series-1 %s", got, test.wantSlot)
			}
			if got.Reference != test.wantPath {
				t.Fatalf("fallback reference = %q, want %q", got.Reference, test.wantPath)
			}
		})
	}
}

func TestEpisodeOwnStillKeepsEpisodeTarget(t *testing.T) {
	episode := &models.Episode{
		ContentID:      "episode-1",
		SeriesID:       "series-1",
		StillPath:      "provider://episode-1/still.jpg",
		StillThumbhash: "episode-thumbhash",
	}
	fallback, ok := episodeImageFallbackForSeries(&models.MediaItem{
		ContentID:    episode.SeriesID,
		BackdropPath: "provider://series-1/backdrop.jpg",
	})
	if !ok {
		t.Fatal("series backdrop did not produce a fallback")
	}
	response, target := episodeResponseShell(episode, fallback)

	if target.Surface != artworkurl.SurfaceEpisodeStills || len(target.Keys) != 1 || target.Keys[0] != episode.ContentID || target.Slot != artworkkey.ImageTypeStill {
		t.Fatalf("episode still target = %#v, want episode-owned still", target)
	}
	if target.Reference != episode.StillPath || response.StillThumbhash != episode.StillThumbhash {
		t.Fatalf("episode still selection = reference %q thumbhash %q", target.Reference, response.StillThumbhash)
	}
}

type countingItemListImageResolver struct {
	singleCalls int
	batchCalls  int
	batchPaths  []string
}

func (r *countingItemListImageResolver) ResolveImageURL(_ context.Context, path string, variant string) string {
	r.singleCalls++
	return "single:" + variant + ":" + path
}

func (r *countingItemListImageResolver) ResolveImageURLs(_ context.Context, paths []string, variant string) map[string]string {
	resolved := r.ResolveImageURLsWithExpiry(context.Background(), paths, variant)
	out := make(map[string]string, len(resolved))
	for path, value := range resolved {
		out[path] = value.URL
	}
	return out
}

func (r *countingItemListImageResolver) ResolveImageURLWithExpiry(_ context.Context, path string, variant string) catalog.ResolvedImageURL {
	r.singleCalls++
	return catalog.ResolvedImageURL{URL: "single:" + variant + ":" + path}
}

func (r *countingItemListImageResolver) ResolveImageURLsWithExpiry(_ context.Context, paths []string, variant string) map[string]catalog.ResolvedImageURL {
	r.batchCalls++
	r.batchPaths = append(r.batchPaths[:0], paths...)
	out := make(map[string]catalog.ResolvedImageURL, len(paths))
	for _, path := range paths {
		out[path] = catalog.ResolvedImageURL{URL: "batch:" + variant + ":" + path}
	}
	return out
}

func TestItemListCardImageURLsUsesBatchResolver(t *testing.T) {
	resolver := &countingItemListImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	handler := &ItemsHandler{detailSvc: detailSvc}

	items := []*models.MediaItem{
		{
			ContentID:    "movie-1",
			PosterPath:   "plugin://poster-1/original.jpg",
			BackdropPath: "plugin://backdrop-1/original.jpg",
		},
		{
			ContentID:    "movie-2",
			PosterPath:   "https://cdn.example/poster-2.jpg",
			BackdropPath: "plugin://backdrop-1/original.jpg",
		},
	}

	urls := handler.itemListCardImageURLs(context.Background(), items, items, catalog.AccessFilter{}, imagesize.Unset)

	if resolver.singleCalls != 0 {
		t.Fatalf("single resolver calls = %d, want 0", resolver.singleCalls)
	}
	if resolver.batchCalls != 1 {
		t.Fatalf("batch resolver calls = %d, want 1", resolver.batchCalls)
	}
	if got := urls["movie-1"].posterURL; got != "batch:card:plugin://poster-1/original.jpg" {
		t.Fatalf("movie-1 poster URL = %q", got)
	}
	if got := urls["movie-1"].backdropURL; got != "batch:card:plugin://backdrop-1/original.jpg" {
		t.Fatalf("movie-1 backdrop URL = %q", got)
	}
	if got := urls["movie-2"].posterURL; got != "https://cdn.example/poster-2.jpg" {
		t.Fatalf("movie-2 poster URL = %q", got)
	}
	if got := len(resolver.batchPaths); got != 2 {
		t.Fatalf("batch resolver path count = %d, want 2", got)
	}
}

func TestItemListArtworkTargetsBindOnlyOverriddenLocalizedFields(t *testing.T) {
	resolver := &recordingEpisodeArtworkResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	handler := &ItemsHandler{detailSvc: detailSvc}
	base := &models.MediaItem{
		ContentID:    "movie-1",
		PosterPath:   "provider://base/poster.jpg",
		BackdropPath: "provider://base/backdrop.jpg",
	}
	localized := *base
	localized.PosterPath = "provider://fr/poster.jpg"

	handler.itemListCardImageURLs(
		context.Background(),
		[]*models.MediaItem{base},
		[]*models.MediaItem{&localized},
		catalog.AccessFilter{PresentationLanguage: "fr"},
		imagesize.Unset,
	)

	if len(resolver.requests) != 2 {
		t.Fatalf("resolved requests = %d, want 2", len(resolver.requests))
	}
	poster := resolver.requests[0].Target
	if poster.Surface != artworkurl.SurfaceLocalizedItemPosters || len(poster.Keys) != 2 || poster.Keys[0] != "movie-1" || poster.Keys[1] != "fr" {
		t.Fatalf("poster target = %+v, want localized item poster [movie-1 fr]", poster)
	}
	backdrop := resolver.requests[1].Target
	if backdrop.Surface != artworkurl.SurfaceItemBackdrops || len(backdrop.Keys) != 1 || backdrop.Keys[0] != "movie-1" {
		t.Fatalf("backdrop target = %+v, want base item backdrop [movie-1]", backdrop)
	}
}

type overlayFastPathFileRepo struct {
	fullContentCalls    int
	overlayContentCalls int
	overlayEpisodeCalls int
}

func (r *overlayFastPathFileRepo) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (r *overlayFastPathFileRepo) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (r *overlayFastPathFileRepo) ListByContentIDs(context.Context, []string) (map[string][]*models.MediaFile, error) {
	r.fullContentCalls++
	return nil, nil
}

func (r *overlayFastPathFileRepo) ListOverlayFilesByContentIDs(context.Context, []string) (map[string][]*models.MediaFile, error) {
	r.overlayContentCalls++
	return map[string][]*models.MediaFile{
		"movie-1": {
			{
				ContentID:      "movie-1",
				Resolution:     "4k",
				CodecVideo:     "hevc",
				CodecAudio:     "eac3",
				AudioChannels:  6,
				HDR:            true,
				Container:      "mkv",
				MediaFolderID:  1,
				AudioTracks:    []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
				SubtitleTracks: []models.SubtitleTrack{{Language: "en"}},
			},
		},
	}, nil
}

func (r *overlayFastPathFileRepo) ListOverlayFilesByEpisodeIDs(context.Context, []string) (map[string][]*models.MediaFile, error) {
	r.overlayEpisodeCalls++
	return map[string][]*models.MediaFile{
		"episode-1": {
			{
				EpisodeID:     "episode-1",
				Resolution:    "1080p",
				CodecVideo:    "h264",
				Container:     "mp4",
				MediaFolderID: 1,
			},
		},
	}, nil
}

func TestListOverlaySummariesUsesOverlayFileProjection(t *testing.T) {
	repo := &overlayFastPathFileRepo{}
	handler := &ItemsHandler{fileRepo: repo}

	summaries := handler.listOverlaySummaries(context.Background(), []*models.MediaItem{
		{ContentID: "movie-1", Type: "movie"},
		{ContentID: "episode-1", Type: "episode"},
	}, catalog.AccessFilter{})

	if repo.fullContentCalls != 0 {
		t.Fatalf("full content file calls = %d, want 0", repo.fullContentCalls)
	}
	if repo.overlayContentCalls != 1 {
		t.Fatalf("overlay content calls = %d, want 1", repo.overlayContentCalls)
	}
	if repo.overlayEpisodeCalls != 1 {
		t.Fatalf("overlay episode calls = %d, want 1", repo.overlayEpisodeCalls)
	}
	if got := summaries["movie-1"].Resolution; got != "2160p" {
		t.Fatalf("movie overlay resolution = %q, want 2160p", got)
	}
	if got := summaries["movie-1"].VideoCodec; got != "H.265" {
		t.Fatalf("movie overlay video codec = %q, want H.265", got)
	}
	if got := summaries["episode-1"].VideoCodec; got != "H.264" {
		t.Fatalf("episode overlay video codec = %q, want H.264", got)
	}
}
