package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
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

	urls := handler.itemListCardImageURLs(context.Background(), items)

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
