package jellycompat

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

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
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("generated fallback is not a valid PNG: %v", err)
	}
	if img.Bounds().Dx() != generatedPosterWidth || img.Bounds().Dy() != generatedPosterHeight {
		t.Fatalf("poster dimensions = %v, want %dx%d", img.Bounds(), generatedPosterWidth, generatedPosterHeight)
	}
}
