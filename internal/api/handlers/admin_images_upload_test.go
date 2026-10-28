package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

const (
	uploadedPosterPath   = "local/series/series/ab12cd34/poster/original.webp"
	uploadedPosterSource = "upload://sha256:ab12cd34"
)

// recordingArtworkPublisher records publications and the revisions queued for
// cleanup, failing every publication when err is set.
type recordingArtworkPublisher struct {
	published []catalog.ArtworkSelection
	queued    []string
	queuedErr []error // the cleanup context's error at the call
	err       error
}

func (p *recordingArtworkPublisher) PublishArtworkSelection(_ context.Context, selection catalog.ArtworkSelection) error {
	p.published = append(p.published, selection)
	return p.err
}

func (p *recordingArtworkPublisher) QueueArtworkRevisionGC(ctx context.Context, originalPath, _ string, _ time.Time) error {
	p.queued = append(p.queued, originalPath)
	p.queuedErr = append(p.queuedErr, ctx.Err())
	return nil
}

// cardImageResolver resolves every stored path to a display URL that names
// the path and variant.
type cardImageResolver struct{}

func (cardImageResolver) ResolveImageURL(_ context.Context, path, variant string) string {
	return "https://cdn.example/" + path + "?variant=" + variant
}

func (r cardImageResolver) ResolveImageURLs(ctx context.Context, paths []string, variant string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = r.ResolveImageURL(ctx, path, variant)
	}
	return out
}

// newUploadImageHandlerForTest serves a movie, a series that shows an
// uploaded poster and a provider backdrop, an audiobook, the series' second
// season with an uploaded poster, and (through curationImageEpisodes) an
// episode of the series.
func newUploadImageHandlerForTest(svc *localImageServiceFake, publisher ...*recordingArtworkPublisher) *AdminImageHandler {
	items := imageItemLookupFake{
		"movie": {ContentID: "movie", Type: "movie", TmdbID: "7"},
		"series": {
			ContentID: "series", Type: "series", TmdbID: "42",
			PosterPath: uploadedPosterPath, PosterSourcePath: uploadedPosterSource, PosterThumbhash: "th-upload",
			BackdropPath: "tmdb/series/42/backdrop/original.webp", BackdropSourcePath: "https://image.example/backdrop.jpg",
		},
		"book": {ContentID: "book", Type: "audiobook"},
	}
	seasons := imageSeasonLookupFake{"season-2": {
		ContentID: "season-2", SeriesID: "series", SeasonNumber: 2,
		PosterPath: "local/series/series/seasons/2/ef56/poster/original.webp", PosterSourcePath: "upload://sha256:ef56",
	}}
	h := NewAdminImageHandler(items, seasons, curationImageEpisodes{}, nil, svc, cardImageResolver{}, nil)
	if len(publisher) > 0 {
		h.detailSvc = publisher[0]
	}
	return h
}

// An upload is cached under the movie or series, scoped to the season for a
// season poster, and published to the target with its upload:// source and
// the images lock. A season's lock goes on its series.
func TestAdminUploadItemImagePublishesToTheTarget(t *testing.T) {
	cases := []struct {
		name, id string
		season   int
		want     catalog.ArtworkSelection
	}{
		{"movie poster", "movie", -1, catalog.ArtworkSelection{TargetType: "movie", TargetContentID: "movie", ParentContentID: "movie"}},
		{"season poster", "season-2", 2, catalog.ArtworkSelection{TargetType: "season", TargetContentID: "season-2", ParentContentID: "series"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &localImageServiceFake{}
			publisher := &recordingArtworkPublisher{}
			out, err := newUploadImageHandlerForTest(svc, publisher).UploadAdminItemImage(context.Background(), tc.id, "poster", []byte("image-bytes"))
			if err != nil {
				t.Fatalf("UploadAdminItemImage: %v", err)
			}
			if len(svc.uploads) != 1 {
				t.Fatalf("uploads = %d, want 1", len(svc.uploads))
			}
			got := svc.uploads[0]
			if got.ContentID != tc.want.ParentContentID || (tc.season < 0) != (got.SeasonNumber == nil) || (got.SeasonNumber != nil && *got.SeasonNumber != tc.season) {
				t.Fatalf("upload request = %+v, want the cache under %q, season %d", got, tc.want.ParentContentID, tc.season)
			}
			want := tc.want
			want.ImageType = "poster"
			want.StoredPath = uploadedPosterPath
			want.SourcePath = uploadedPosterSource
			want.Thumbhash = "th-upload"
			want.LockField = int(metadata.FieldImages)
			if len(publisher.published) != 1 || publisher.published[0] != want {
				t.Fatalf("published %+v, want %+v", publisher.published, want)
			}
			if out.StoredPath != uploadedPosterPath || len(publisher.queued) != 0 {
				t.Fatalf("result %+v, queued %v", out, publisher.queued)
			}
		})
	}
}

func TestAdminUploadItemImageRejectsBeforeCaching(t *testing.T) {
	cases := []struct {
		name, id, imageType string
		data                []byte
		status              int
	}{
		{"empty file", "movie", "poster", nil, http.StatusBadRequest},
		{"oversized file", "movie", "poster", make([]byte, metadata.MaxUploadedImageBytes+1), http.StatusBadRequest},
		{"unknown type read as a poster elsewhere", "movie", "cover", []byte("x"), http.StatusBadRequest},
		{"type the picker does not offer", "movie", "still", []byte("x"), http.StatusBadRequest},
		{"season logo", "season-2", "logo", []byte("x"), http.StatusBadRequest},
		{"episode", "episode", "poster", []byte("x"), http.StatusBadRequest},
		{"missing item", "missing", "poster", []byte("x"), http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &localImageServiceFake{}
			h := newUploadImageHandlerForTest(svc)
			if tc.id == "missing" {
				h.episodes = imageEpisodeLookupFake{}
			}
			_, err := h.UploadAdminItemImage(context.Background(), tc.id, tc.imageType, tc.data)
			requireAPIStatus(t, err, tc.status)
			if len(svc.uploads) != 0 {
				t.Fatalf("uploads = %d, want none", len(svc.uploads))
			}
		})
	}
}

func TestAdminUploadItemImageMapsCacheFailures(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"not an image", metadata.ErrUnsupportedUploadedImage, http.StatusBadRequest},
		{"no artwork storage", metadata.ErrImageUploadUnavailable, http.StatusServiceUnavailable},
		{"storage failure", errors.New("put local/series/series/poster: access denied"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &localImageServiceFake{uploadErr: tc.err}
			_, err := newUploadImageHandlerForTest(svc).UploadAdminItemImage(context.Background(), "series", "poster", []byte("x"))
			apiErr := requireAPIStatus(t, err, tc.status)
			if strings.Contains(apiErr.Message, "access denied") {
				t.Fatalf("message %q leaks the storage error", apiErr.Message)
			}
		})
	}
}

// The uploaded image an item shows is listed as a local choice, with a display
// URL for its stored path. Frozen v1 lists provider choices only.
func TestAdminItemImagesListsTheShownUpload(t *testing.T) {
	svc := &localImageServiceFake{}
	h := newUploadImageHandlerForTest(svc)

	out, err := h.GetAdminItemImages(context.Background(), "series")
	if err != nil {
		t.Fatalf("GetAdminItemImages: %v", err)
	}
	var uploads []itemImageEntry
	for _, entry := range out.Images {
		if metadata.IsUploadedImageSource(entry.OriginalURL) {
			uploads = append(uploads, entry)
		}
	}
	// The backdrop came from a provider, so only the poster is an upload.
	if len(uploads) != 1 {
		t.Fatalf("upload choices = %+v, want the poster", uploads)
	}
	want := itemImageEntry{ProviderID: "local", URL: "https://cdn.example/" + uploadedPosterPath + "?variant=card", OriginalURL: uploadedPosterSource, Type: "poster"}
	if uploads[0] != want {
		t.Fatalf("upload choice = %+v, want %+v", uploads[0], want)
	}

	season, err := h.GetAdminItemImages(context.Background(), "season-2")
	if err != nil {
		t.Fatalf("GetAdminItemImages season: %v", err)
	}
	if len(season.Images) != 1 || season.Images[0].OriginalURL != "upload://sha256:ef56" || season.Images[0].ProviderID != "local" {
		t.Fatalf("season choices = %+v, want its uploaded poster", season.Images)
	}

	router := chi.NewRouter()
	router.Get("/{id}/images", h.HandleGetItemImages)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/series/images", nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "upload://") {
		t.Fatalf("v1 list %d %s, want no upload choice", rec.Code, rec.Body)
	}
}

// An upload is stored only in artwork storage, so apply takes back only the
// upload the target shows, and never downloads an upload:// source.
func TestAdminApplyUploadedImageOnlyReappliesTheShownUpload(t *testing.T) {
	t.Run("another upload is refused", func(t *testing.T) {
		svc := &localImageServiceFake{}
		publisher := &recordingArtworkPublisher{}
		_, err := newUploadImageHandlerForTest(svc, publisher).ApplyAdminItemImage(context.Background(), "series", AdminItemImageRequest{OriginalURL: "upload://sha256:other", Type: "poster"})
		requireAPIStatus(t, err, http.StatusBadRequest)
		if svc.providerCalls != 0 || len(svc.applyLocal) != 0 || len(publisher.published) != 0 {
			t.Fatalf("provider %d, local %d calls, published %v, want none", svc.providerCalls, len(svc.applyLocal), publisher.published)
		}
	})

	t.Run("the shown upload is published again", func(t *testing.T) {
		svc := &localImageServiceFake{}
		publisher := &recordingArtworkPublisher{}
		if _, err := newUploadImageHandlerForTest(svc, publisher).ApplyAdminItemImage(context.Background(), "series", AdminItemImageRequest{OriginalURL: uploadedPosterSource, Type: "poster", ProviderID: "local"}); err != nil {
			t.Fatalf("ApplyAdminItemImage: %v", err)
		}
		if svc.providerCalls != 0 || len(svc.applyLocal) != 0 || len(svc.uploads) != 0 {
			t.Fatalf("provider %d, local %d, upload %d calls, want none", svc.providerCalls, len(svc.applyLocal), len(svc.uploads))
		}
		if len(publisher.published) != 1 {
			t.Fatalf("published %v, want one", publisher.published)
		}
		if got := publisher.published[0]; got.StoredPath != uploadedPosterPath || got.SourcePath != uploadedPosterSource || got.Thumbhash != "th-upload" {
			t.Fatalf("published %+v, want the shown upload", got)
		}
	})
}

// When publication fails, only a revision the target does not already show is
// queued for cleanup, even when the request has ended.
func TestAdminImagePublishFailureKeepsTheShownRevision(t *testing.T) {
	cases := []struct {
		name, id string
		apply    bool
		queued   []string
	}{
		{"re-applying the shown upload", "series", true, nil},
		{"a new upload", "movie", false, []string{uploadedPosterPath}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &recordingArtworkPublisher{err: errors.New("database unavailable")}
			h := newUploadImageHandlerForTest(&localImageServiceFake{}, publisher)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var err error
			if tc.apply {
				_, err = h.ApplyAdminItemImage(ctx, tc.id, AdminItemImageRequest{OriginalURL: uploadedPosterSource, Type: "poster"})
			} else {
				_, err = h.UploadAdminItemImage(ctx, tc.id, "poster", []byte("image-bytes"))
			}
			requireAPIStatus(t, err, http.StatusInternalServerError)
			if strings.Join(publisher.queued, ",") != strings.Join(tc.queued, ",") {
				t.Fatalf("queued %v, want %v", publisher.queued, tc.queued)
			}
			for _, queueErr := range publisher.queuedErr {
				if queueErr != nil {
					t.Fatalf("cleanup ran with a done context: %v", queueErr)
				}
			}
		})
	}
}
