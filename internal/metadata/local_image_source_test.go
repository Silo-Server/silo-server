package metadata

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestIsLocalImageSourcePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"file:///media/movies/Film/poster.jpg", true},
		{"FILE:///media/movies/Film/poster.jpg", true},
		{"  file:///media/poster.jpg  ", true},
		{"https://image.tmdb.org/t/p/original/a.jpg", false},
		{"tvdb://banners/a.jpg", false},
		{"local/movies/abc/poster/original.webp", false},
		{"tmdb/movies/550/poster/original.webp", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isLocalImageSourcePath(tc.path); got != tc.want {
			t.Errorf("isLocalImageSourcePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsCachedImagePathExcludesLocalSources(t *testing.T) {
	if isCachedImagePath("file:///media/movies/Film/poster.jpg") {
		t.Fatal("file:// source must not be treated as a cached image path")
	}
	if !isCachedImagePath("local/movies/abc/deadbeef/poster/original.webp") {
		t.Fatal("local/ cached key must remain a cached image path")
	}
	if !isCachedImagePath("tmdb/movies/550/poster/original.webp") {
		t.Fatal("remote cached key must remain a cached image path")
	}
}

func TestPreserveCachedArtworkRoutesLocalSource(t *testing.T) {
	// No prior cached art: the file:// source must never land in *_path; the
	// pre-cache window keeps the path empty until the cache job completes.
	path, thumb, source := preserveCachedArtwork(
		"file:///media/movies/Film/poster.jpg", "", "", "", "",
	)
	if path != "" {
		t.Fatalf("path = %q, want empty during pre-cache window", path)
	}
	if thumb != "" {
		t.Fatalf("thumb = %q, want empty", thumb)
	}
	if source != "file:///media/movies/Film/poster.jpg" {
		t.Fatalf("source = %q", source)
	}

	// Prior cached art keeps serving while the local source waits to cache.
	path, thumb, source = preserveCachedArtwork(
		"file:///media/movies/Film/poster.jpg", "",
		"tmdb/movies/550/poster/original.webp", "https://image.tmdb.org/a.jpg", "th",
	)
	if path != "tmdb/movies/550/poster/original.webp" {
		t.Fatalf("path = %q, want prior cached key", path)
	}
	if thumb != "th" {
		t.Fatalf("thumb = %q", thumb)
	}
	if source != "file:///media/movies/Film/poster.jpg" {
		t.Fatalf("source = %q", source)
	}
}

func TestPrepareItemImagesForQueueRoutesLocalSource(t *testing.T) {
	item := &models.MediaItem{
		ContentID:  "movie-1",
		Type:       "movie",
		PosterPath: "file:///media/movies/Film/poster.jpg",
	}
	prepareItemImagesForQueue(item, nil)
	if item.PosterPath != "" {
		t.Fatalf("PosterPath = %q, want empty during pre-cache window", item.PosterPath)
	}
	if item.PosterSourcePath != "file:///media/movies/Film/poster.jpg" {
		t.Fatalf("PosterSourcePath = %q", item.PosterSourcePath)
	}
}

func TestApplyBestImagesLocalCandidateAlwaysApplies(t *testing.T) {
	item := &models.MediaItem{PosterPath: "tmdb/movies/550/poster/original.webp"}
	applyBestImages(item, []RemoteImage{
		{ProviderID: "nfo", URL: "file:///media/movies/Film/poster.jpg", Type: ImagePoster, Rating: 0},
		{ProviderID: "tmdb", URL: "https://image.tmdb.org/text.jpg", Type: ImagePoster, Language: "en", Rating: 10},
	}, MergeFillEmpty, "en")
	if item.PosterPath != "file:///media/movies/Film/poster.jpg" {
		t.Fatalf("PosterPath = %q, want local candidate to apply over existing art", item.PosterPath)
	}
}

func TestApplyBestImagesRemoteSelectionUnchanged(t *testing.T) {
	// Regression pins: a rating-0 remote candidate still cannot displace
	// existing art in fill mode, while a rated one still can.
	item := &models.MediaItem{PosterPath: "tmdb/movies/550/poster/original.webp"}
	applyBestImages(item, []RemoteImage{
		{ProviderID: "tmdb", URL: "https://image.tmdb.org/zero.jpg", Type: ImagePoster, Rating: 0},
	}, MergeFillEmpty, "en")
	if item.PosterPath != "tmdb/movies/550/poster/original.webp" {
		t.Fatalf("PosterPath = %q, rating-0 remote must not displace existing art", item.PosterPath)
	}

	applyBestImages(item, []RemoteImage{
		{ProviderID: "tmdb", URL: "https://image.tmdb.org/rated.jpg", Type: ImagePoster, Rating: 7.5},
	}, MergeFillEmpty, "en")
	if item.PosterPath != "https://image.tmdb.org/rated.jpg" {
		t.Fatalf("PosterPath = %q, rated remote candidate must still apply", item.PosterPath)
	}

	empty := &models.MediaItem{}
	applyBestImages(empty, []RemoteImage{
		{ProviderID: "tmdb", URL: "https://image.tmdb.org/zero.jpg", Type: ImagePoster, Rating: 0},
	}, MergeFillEmpty, "en")
	if empty.PosterPath != "https://image.tmdb.org/zero.jpg" {
		t.Fatalf("PosterPath = %q, rating-0 remote must still fill empty art", empty.PosterPath)
	}
}

func TestApplyBestImagesPrefersTextPostersByLanguage(t *testing.T) {
	includesText := true
	textless := false
	tests := []struct {
		name          string
		preferredLang string
		images        []RemoteImage
		wantPoster    string
	}{
		{
			name:          "explicit text flag beats a higher rated English-tagged textless poster",
			preferredLang: "en",
			images: []RemoteImage{
				{ProviderID: "tvdb", URL: "textless-english", Type: ImagePoster, Language: "en", Rating: 10, IncludesText: &textless},
				{ProviderID: "tvdb", URL: "english-with-text", Type: ImagePoster, Language: "en", Rating: 6, IncludesText: &includesText},
			},
			wantPoster: "english-with-text",
		},
		{
			name:          "explicit textless poster remains the final fallback",
			preferredLang: "en",
			images: []RemoteImage{
				{ProviderID: "tvdb", URL: "textless-english", Type: ImagePoster, Language: "en", Rating: 10, IncludesText: &textless},
			},
			wantPoster: "textless-english",
		},
		{
			name:          "preferred language beats higher rated textless poster",
			preferredLang: "en",
			images: []RemoteImage{
				{ProviderID: "tmdb", URL: "textless", Type: ImagePoster, Rating: 10},
				{ProviderID: "tmdb", URL: "english", Type: ImagePoster, Language: "en", Rating: 7},
			},
			wantPoster: "english",
		},
		{
			name:          "preferred language beats higher rated English poster",
			preferredLang: "fr",
			images: []RemoteImage{
				{ProviderID: "tmdb", URL: "french", Type: ImagePoster, Language: "fr", Rating: 6},
				{ProviderID: "tmdb", URL: "english", Type: ImagePoster, Language: "en", Rating: 9},
			},
			wantPoster: "french",
		},
		{
			name:          "English beats higher rated other language when preferred is unavailable",
			preferredLang: "fr",
			images: []RemoteImage{
				{ProviderID: "tmdb", URL: "english", Type: ImagePoster, Language: "en", Rating: 6},
				{ProviderID: "tmdb", URL: "german", Type: ImagePoster, Language: "de", Rating: 9},
			},
			wantPoster: "english",
		},
		{
			name:          "highest rated other language beats textless fallback",
			preferredLang: "fr",
			images: []RemoteImage{
				{ProviderID: "tmdb", URL: "textless", Type: ImagePoster, Rating: 10},
				{ProviderID: "tmdb", URL: "spanish", Type: ImagePoster, Language: "es", Rating: 6},
				{ProviderID: "tmdb", URL: "german", Type: ImagePoster, Language: "de", Rating: 8},
			},
			wantPoster: "german",
		},
		{
			name:          "textless remains the final fallback",
			preferredLang: "en",
			images: []RemoteImage{
				{ProviderID: "tmdb", URL: "textless-low", Type: ImagePoster, Rating: 4},
				{ProviderID: "tmdb", URL: "textless-high", Type: ImagePoster, Rating: 8},
			},
			wantPoster: "textless-high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := &models.MediaItem{}
			applyBestImages(item, tt.images, MergeFillEmpty, tt.preferredLang)
			if item.PosterPath != tt.wantPoster {
				t.Fatalf("PosterPath = %q, want %q", item.PosterPath, tt.wantPoster)
			}
		})
	}
}

func TestApplyBestImagesUsesLibraryLanguageForArtwork(t *testing.T) {
	item := &models.MediaItem{}
	applyBestImages(item, []RemoteImage{
		{ProviderID: "tmdb", URL: "poster-textless", Type: ImagePoster, Rating: 10},
		{ProviderID: "tmdb", URL: "poster-english", Type: ImagePoster, Language: "en", Rating: 7},
		{ProviderID: "tmdb", URL: "backdrop-textless", Type: ImageBackdrop, Rating: 10},
		{ProviderID: "tmdb", URL: "backdrop-english", Type: ImageBackdrop, Language: "en", Rating: 7},
		{ProviderID: "tmdb", URL: "logo-textless", Type: ImageLogo, Rating: 10},
		{ProviderID: "tmdb", URL: "logo-english", Type: ImageLogo, Language: "en", Rating: 7},
	}, MergeFillEmpty, "en")

	if item.PosterPath != "poster-english" {
		t.Fatalf("PosterPath = %q, want text poster", item.PosterPath)
	}
	if item.BackdropPath != "backdrop-textless" {
		t.Fatalf("BackdropPath = %q, want language-neutral background", item.BackdropPath)
	}
	if item.LogoPath != "logo-english" {
		t.Fatalf("LogoPath = %q, want English logo", item.LogoPath)
	}
}

func TestApplyBestImagesLogoLanguageFallbacks(t *testing.T) {
	tests := []struct {
		name          string
		preferredLang string
		images        []RemoteImage
		wantLogo      string
	}{
		{
			name:          "preferred language beats higher rated English logo",
			preferredLang: "fr",
			images: []RemoteImage{
				{ProviderID: "tmdb", URL: "french", Type: ImageLogo, Language: "fr", Rating: 6},
				{ProviderID: "tmdb", URL: "english", Type: ImageLogo, Language: "en", Rating: 9},
			},
			wantLogo: "french",
		},
		{
			name:          "English is the cross-language fallback",
			preferredLang: "fr",
			images: []RemoteImage{
				{ProviderID: "tmdb", URL: "english", Type: ImageLogo, Language: "en", Rating: 6},
				{ProviderID: "tmdb", URL: "neutral", Type: ImageLogo, Rating: 10},
			},
			wantLogo: "english",
		},
		{
			name:          "language-neutral beats unrelated language",
			preferredLang: "en",
			images: []RemoteImage{
				{ProviderID: "tmdb", URL: "german", Type: ImageLogo, Language: "de", Rating: 10},
				{ProviderID: "tmdb", URL: "neutral", Type: ImageLogo, Rating: 6},
			},
			wantLogo: "neutral",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := &models.MediaItem{}
			applyBestImages(item, tt.images, MergeFillEmpty, tt.preferredLang)
			if item.LogoPath != tt.wantLogo {
				t.Fatalf("LogoPath = %q, want %q", item.LogoPath, tt.wantLogo)
			}
		})
	}
}

func TestApplyBestImagesFallsBackToLanguageTaggedBackgrounds(t *testing.T) {
	// Language-neutral backgrounds are preferred, but an item whose backdrops
	// are all language-tagged must still get one rather than none.
	item := &models.MediaItem{}
	applyBestImages(item, []RemoteImage{
		{ProviderID: "tmdb", URL: "english", Type: ImageBackdrop, Language: "en", Rating: 10},
		{ProviderID: "tmdb", URL: "german", Type: ImageBackdrop, Language: "de", Rating: 9},
	}, MergeFillEmpty, "en")
	if item.BackdropPath != "english" {
		t.Fatalf("BackdropPath = %q, want a language-tagged fallback background", item.BackdropPath)
	}
}

func TestBuildItemLocalizationRecordSkipsLocalCandidates(t *testing.T) {
	// Language-neutral local files must not duplicate into per-language rows.
	loc := buildItemLocalizationRecord(
		nil,
		"movie-1",
		"de",
		"movie",
		&MetadataResult{},
		[]RemoteImage{
			{ProviderID: "nfo", URL: "file:///media/movies/Film/poster.jpg", Type: ImagePoster},
		},
		MergeFillEmpty,
		"de",
		false,
	)
	if loc.PosterPath != "" || loc.PosterSourcePath != "" {
		t.Fatalf("localization picked up local candidate: path=%q source=%q", loc.PosterPath, loc.PosterSourcePath)
	}
}

func TestItemImageCacheAttributionLocalSource(t *testing.T) {
	item := &models.MediaItem{ContentID: "movie-1", TmdbID: "550"}
	providerID, providerContentID := itemImageCacheAttribution(
		item,
		map[string]string{"tmdb": "550"},
		nil,
		"file:///media/movies/Film/poster.jpg",
	)
	if providerID != "local" {
		t.Fatalf("providerID = %q, want local", providerID)
	}
	if providerContentID != "movie-1" {
		t.Fatalf("providerContentID = %q, want the item content ID", providerContentID)
	}
}

func TestEnqueueItemImagesAcceptsLocalSource(t *testing.T) {
	service := &MetadataService{}
	enqueuer := &recordingImageCacheJobEnqueuer{}
	service.SetAutoCacheImages(true)
	service.SetImageCacheJobEnqueuer(enqueuer)

	item := &models.MediaItem{
		ContentID:        "movie-1",
		Type:             "movie",
		PosterSourcePath: "file:///media/movies/Film/poster.jpg",
	}
	service.enqueueItemImages(context.Background(), item, nil, nil)
	if len(enqueuer.inputs) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(enqueuer.inputs))
	}
	in := enqueuer.inputs[0]
	if in.SourcePath != "file:///media/movies/Film/poster.jpg" {
		t.Fatalf("SourcePath = %q", in.SourcePath)
	}
	if in.ProviderID != "local" {
		t.Fatalf("ProviderID = %q, want local", in.ProviderID)
	}
	if in.ProviderContentID != "movie-1" {
		t.Fatalf("ProviderContentID = %q", in.ProviderContentID)
	}
}

func TestNormalizeImageCacheJobInputAcceptsLocalSource(t *testing.T) {
	in, ok := normalizeImageCacheJobInput(EnqueueImageCacheJobInput{
		TargetType:      ImageCacheTargetItem,
		TargetContentID: "movie-1",
		SourcePath:      "file:///media/movies/Film/poster.jpg",
		ContentType:     "movies",
	})
	if !ok {
		t.Fatal("local file:// source must be accepted")
	}
	if in.ProviderID != "local" {
		t.Fatalf("ProviderID = %q, want local", in.ProviderID)
	}
	// The other system schemes stay rejected.
	for _, rejected := range []string{
		"s3://bucket/key.jpg",
		"local://x.jpg",
		"upload://x.jpg",
		"generated://x.jpg",
		"tmdb/movies/550/poster/original.webp",
	} {
		if _, ok := normalizeImageCacheJobInput(EnqueueImageCacheJobInput{SourcePath: rejected}); ok {
			t.Errorf("source %q must stay rejected", rejected)
		}
	}
}

func TestIsStableProviderImageFailureLocalClasses(t *testing.T) {
	for _, text := range []string{
		"local image missing: /media/movies/Film/poster.jpg",
		"local image forbidden: /media/movies/Film/poster.jpg",
		"local image path outside library roots: /etc/passwd",
		"unexpected status 404",
	} {
		if !isStableProviderImageFailure(text) {
			t.Errorf("expected stable failure classification for %q", text)
		}
	}
	if isStableProviderImageFailure("local image read failed: io timeout") {
		t.Error("transient local read errors must keep the normal backoff")
	}
}
