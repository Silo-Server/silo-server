package metadata

import (
	"context"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/models"
)

const nfoPosterSource = "nfo+https://img.example.org/poster.jpg"

// An image URL an NFO names is local metadata: it never lands in a served
// column, it is not a provider URL any sweep re-downloads, and it outranks
// remote artwork like a sidecar file.
func TestLocalMetadataURLSourceRouting(t *testing.T) {
	t.Parallel()

	if got := LocalMetadataURLSource("https://img.example.org/poster.jpg"); got != nfoPosterSource {
		t.Fatalf("LocalMetadataURLSource = %q", got)
	}
	for _, source := range []string{nfoPosterSource, "NFO+HTTP://img.example.org/poster.jpg", " nfo+https://x.org/a.jpg "} {
		if !isLocalImageSourcePath(source) || isRemoteImageSourcePath(source) || isProviderImagePath(source) ||
			isCachedImagePath(source) || !isCacheableImageSourcePath(source) {
			t.Errorf("%q: local=%v remote=%v provider=%v cached=%v cacheable=%v", source,
				isLocalImageSourcePath(source), isRemoteImageSourcePath(source), isProviderImagePath(source),
				isCachedImagePath(source), isCacheableImageSourcePath(source))
		}
	}
	for _, source := range []string{"nfo+smb://nas/a.jpg", "nfo+file:///a.jpg", "nfo:https://x.org/a.jpg"} {
		if isLocalImageSourcePath(source) {
			t.Errorf("%q counted as a local source", source)
		}
	}
	if url, ok := localMetadataImageURL(" " + nfoPosterSource); !ok || url != "https://img.example.org/poster.jpg" {
		t.Errorf("localMetadataImageURL = %q, %v", url, ok)
	}
	if _, ok := localMetadataImageURL("file:///media/a.jpg"); ok {
		t.Error("a file source is not a URL source")
	}

	served, source := splitProviderImagePath(nfoPosterSource)
	if served != "" || source != nfoPosterSource {
		t.Errorf("splitProviderImagePath = %q, %q; want only the source", served, source)
	}
	path, _, source := preserveCachedArtwork(nfoPosterSource, "", "tmdb/movies/1/poster/original.webp", "https://image.tmdb.org/p.jpg", "th", true)
	if path != "tmdb/movies/1/poster/original.webp" || source != nfoPosterSource {
		t.Errorf("preserveCachedArtwork = %q from %q; want the cached copy kept until the NFO's lands", path, source)
	}
	if kept := withoutLocalImages([]RemoteImage{{URL: nfoPosterSource}, {URL: "https://image.tmdb.org/p.jpg"}}); len(kept) != 1 {
		t.Errorf("withoutLocalImages kept %v; NFO URLs need the image cache like sidecars", kept)
	}
	// The copy is downloaded and keyed like a provider's, under nfo/, apart
	// from the content-hashed local/ copies of sidecar files.
	if got := imageCacheProviderIDFromSource(nfoPosterSource, "tmdb"); got != "nfo" {
		t.Errorf("provider = %q, want nfo", got)
	}
	if got := imageCacheProviderIDFromSource("file:///media/a.jpg", "tmdb"); got != imageCacheLocalProviderID {
		t.Errorf("sidecar provider = %q, want local", got)
	}

	item := &models.MediaItem{PosterPath: "tmdb/movies/1/poster/original.webp"}
	applyBestImages(item, []RemoteImage{
		{ProviderID: "tmdb", URL: "https://image.tmdb.org/rated.jpg", Type: ImagePoster, Language: "en", Rating: 9},
		{ProviderID: "nfo", URL: nfoPosterSource, Type: ImagePoster},
	}, MergeFillEmpty, "en")
	if item.PosterPath != nfoPosterSource {
		t.Errorf("PosterPath = %q, want the NFO poster over a rated remote one", item.PosterPath)
	}
}

// The processor downloads an NFO-named URL through the image cacher (and its
// public-address guard) rather than reading it as a file or resolving it as
// a plugin path.
func TestImageCacheProcessorDownloadsLocalMetadataURL(t *testing.T) {
	jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{{
		ID:                31,
		TargetType:        ImageCacheTargetPerson,
		TargetContentID:   "287",
		SeriesID:          "movie-1",
		SourcePath:        "nfo+https://img.example.org/lead.jpg",
		ProviderID:        imageCacheLocalProviderID,
		ProviderContentID: "287",
		ContentType:       "people",
		ImageType:         ImageCacheImageProfile,
	}}}
	cacher := &fakeImageCacher{result: &CacheImageResult{BasePath: "local/people/287/profile", Ext: ".webp", Thumbhash: "th"}}
	resolver := &fakeImageResolver{url: "https://wrong.example.org/resolved.jpg"}
	people := &fakePersonPhotoUpdater{updated: true}

	processor := NewImageCacheProcessorWithTargets(jobs, cacher, resolver, ImageCacheProcessorTargets{People: people})
	stats, err := processor.RunOnce(context.Background(), "test-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Succeeded != 1 || len(cacher.reqs) != 1 {
		t.Fatalf("stats = %+v, cache requests = %d", stats, len(cacher.reqs))
	}
	if got := cacher.reqs[0].SourceURL; got != "https://img.example.org/lead.jpg" {
		t.Errorf("downloaded %q, want the NFO's URL", got)
	}
	if people.cachedPath != "local/people/287/profile/original.webp" {
		t.Errorf("cachedPath = %q", people.cachedPath)
	}
}

// A title's interactive artwork refresh covers its own jobs and its seasons'
// and episodes', not the photos of the people its NFO credits, whose jobs
// carry the title's content ID only to confine a local read.
func TestImageCacheTargetScopeLeavesOutPersonJobs(t *testing.T) {
	t.Parallel()

	scope := imageCacheTargetScope("$1")
	if !strings.Contains(scope, "target_content_id = $1") || !strings.Contains(scope, "target_type <> 'person'") {
		t.Fatalf("scope = %q", scope)
	}
}

// Copies of NFO-named URLs are keyed under the provider id they are credited
// to, which artworkkey must count as library artwork.
func TestLocalMetadataURLCopiesCountAsLibraryArtwork(t *testing.T) {
	t.Parallel()

	if key := localMetadataURLProviderID + "/movies/1/poster/original.webp"; !artworkkey.IsLocal(key) {
		t.Fatalf("artworkkey.IsLocal(%q) = false", key)
	}
}
