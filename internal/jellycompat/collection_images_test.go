package jellycompat

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestRenderCollectionPosterPNG(t *testing.T) {
	got, err := generatedCollectionPoster("My Favorite Films")
	if err != nil {
		t.Fatalf("render poster: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode poster PNG: %v", err)
	}
	if w := img.Bounds().Dx(); w != generatedPosterWidth {
		t.Fatalf("poster width = %d, want %d", w, generatedPosterWidth)
	}
	if h := img.Bounds().Dy(); h != generatedPosterHeight {
		t.Fatalf("poster height = %d, want %d", h, generatedPosterHeight)
	}

	cached, err := generatedCollectionPoster("My Favorite Films")
	if err != nil {
		t.Fatalf("render cached poster: %v", err)
	}
	if !bytes.Equal(got, cached) {
		t.Fatal("cached poster bytes differ from first render")
	}
}

func TestServeCollectionImageServesBundledTemplatePoster(t *testing.T) {
	const secret = "image-secret"
	codec := NewResourceIDCodec()
	collectionID := "129510738770395144"
	routeID := codec.EncodeStringID(EncodedIDCollection, collectionID)
	posterPath := "/images/collection-templates/tmdb_on_the_air.jpg"
	jpegBytes := []byte("\xff\xd8\xfffake-jpeg-bytes")

	collection := &models.LibraryCollection{
		ID:         collectionID,
		Title:      "On The Air",
		Visibility: "visible",
		PosterURL:  posterPath,
	}
	tag := newImageTagSigner(secret).Tag(
		imageTagSeed(routeID, "Primary", compatCardImageSize, posterPath, "", time.Time{}),
		"",
	)
	h := &ImagesHandler{
		codec:       codec,
		images:      NewImageCache(time.Hour, time.Now),
		imageTags:   newImageTagSigner(secret),
		collections: &fakeCollectionSource{collections: []*models.LibraryCollection{collection}},
		frontendFS: fstest.MapFS{
			"images/collection-templates/tmdb_on_the_air.jpg": {Data: jpegBytes},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?fillHeight=360&fillWidth=360&tag="+tag, nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), jpegBytes) {
		t.Fatal("served body does not match bundled asset bytes")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("Content-Type = %q, want image/jpeg", ct)
	}
}

func TestServeCollectionImageGeneratesFallbackWhenNoPoster(t *testing.T) {
	const secret = "image-secret"
	codec := NewResourceIDCodec()
	collectionID := "abc123"
	routeID := codec.EncodeStringID(EncodedIDCollection, collectionID)
	collection := &models.LibraryCollection{
		ID:         collectionID,
		Title:      "Hidden Gems",
		Visibility: "visible",
	}
	tag := newImageTagSigner(secret).Tag(
		imageTagSeed(routeID, "Primary", compatCardImageSize, generatedPosterSeed(collection.Title), "", time.Time{}),
		"",
	)
	h := &ImagesHandler{
		codec:       codec,
		images:      NewImageCache(time.Hour, time.Now),
		imageTags:   newImageTagSigner(secret),
		collections: &fakeCollectionSource{collections: []*models.LibraryCollection{collection}},
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?tag="+tag, nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	if _, err := png.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Fatalf("generated fallback is not a valid PNG: %v", err)
	}
}

func TestServeCollectionImageRejectsBadTagWithoutSession(t *testing.T) {
	const secret = "image-secret"
	codec := NewResourceIDCodec()
	collectionID := "abc123"
	routeID := codec.EncodeStringID(EncodedIDCollection, collectionID)
	collection := &models.LibraryCollection{ID: collectionID, Title: "X", Visibility: "visible"}
	h := &ImagesHandler{
		codec:       codec,
		images:      NewImageCache(time.Hour, time.Now),
		imageTags:   newImageTagSigner(secret),
		collections: &fakeCollectionSource{collections: []*models.LibraryCollection{collection}},
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?tag=deadbeef", nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestServeCollectionsViewImageGeneratesTile(t *testing.T) {
	const secret = "image-secret"
	codec := NewResourceIDCodec()
	tag := newImageTagSigner(secret).Tag(
		imageTagSeed(collectionsViewID, "Primary", compatCardImageSize, generatedPosterSeed(collectionsViewCaption), "", time.Time{}),
		"",
	)
	h := &ImagesHandler{
		codec:     codec,
		images:    NewImageCache(time.Hour, time.Now),
		imageTags: newImageTagSigner(secret),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+collectionsViewID+"/Images/Primary?tag="+tag, nil)
	req = withImageRouteParams(req, collectionsViewID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := png.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Fatalf("collections-view tile is not a valid PNG: %v", err)
	}
}
