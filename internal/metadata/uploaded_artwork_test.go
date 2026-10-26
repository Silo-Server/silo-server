package metadata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"testing"

	"github.com/Silo-Server/silo-server/internal/imageutil"
)

func uploadedPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatalf("encoding png: %v", err)
	}
	return buf.Bytes()
}

// An upload is cached under the item's local/ keys with a digest
// discriminator, and its source records only the image's SHA-256.
func TestApplyUploadedItemImageCachesUnderLocalKeys(t *testing.T) {
	data := uploadedPNG(t)
	cacher := &fakeByteImageCacher{thumbhash: "th-upload"}
	service := &MetadataService{}
	service.SetImageCacher(cacher)

	result, err := service.ApplyUploadedItemImage(context.Background(), ApplyUploadedItemImageRequest{
		ContentID: "movie-1", ContentType: "movie", ImageType: ImageBackdrop, Data: data,
	})
	if err != nil {
		t.Fatalf("ApplyUploadedItemImage: %v", err)
	}
	digest := sha256.Sum256(data)
	wantSource := "upload://sha256:" + hex.EncodeToString(digest[:])
	if len(cacher.bytesReq) != 1 || !bytes.Equal(cacher.byteReqs[0], data) {
		t.Fatalf("cached %d images, want the upload once", len(cacher.bytesReq))
	}
	req := cacher.bytesReq[0]
	if req.ProviderID != LocalImageProviderID || req.ContentType != "movies" || req.ContentID != "movie-1" ||
		req.ImageType != ImageBackdrop || req.SeasonNumber != nil ||
		req.KeyDiscriminator != hex.EncodeToString(digest[:4]) || req.SourceURL != wantSource {
		t.Fatalf("cache request = %+v", req)
	}
	wantPath := "local/movies/movie-1/" + req.KeyDiscriminator + "/backdrop/original.webp"
	if result.StoredPath != wantPath || result.Thumbhash != "th-upload" || result.SourcePath != wantSource {
		t.Fatalf("result = %+v, want stored path %q and source %q", result, wantPath, wantSource)
	}
	if !IsUploadedImageSource(result.SourcePath) || IsLocalImageSource(result.SourcePath) {
		t.Fatalf("source %q should count as an upload and nothing else", result.SourcePath)
	}
}

// A season poster is cached under its series, scoped to the season.
func TestApplyUploadedItemImageScopesSeasonPosters(t *testing.T) {
	cacher := &fakeByteImageCacher{}
	service := &MetadataService{}
	service.SetImageCacher(cacher)
	season := 2
	if _, err := service.ApplyUploadedItemImage(context.Background(), ApplyUploadedItemImageRequest{
		ContentID: "series-1", ContentType: "series", ImageType: ImagePoster, SeasonNumber: &season, Data: uploadedPNG(t),
	}); err != nil {
		t.Fatalf("ApplyUploadedItemImage: %v", err)
	}
	req := cacher.bytesReq[0]
	if req.ContentID != "series-1" || req.ContentType != "series" || req.SeasonNumber == nil || *req.SeasonNumber != 2 {
		t.Fatalf("cache request = %+v, want the series scoped to season 2", req)
	}
}

// The bytes decide the format, not the type the client declared, so a file
// that is not a JPEG, PNG or WebP never reaches libvips.
func TestApplyUploadedItemImageRejectsOtherFormats(t *testing.T) {
	cacher := &fakeByteImageCacher{}
	service := &MetadataService{}
	service.SetImageCacher(cacher)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	_, err := service.ApplyUploadedItemImage(context.Background(), ApplyUploadedItemImageRequest{ContentID: "movie-1", ContentType: "movie", ImageType: ImagePoster, Data: svg})
	if !errors.Is(err, ErrUnsupportedUploadedImage) {
		t.Fatalf("err = %v, want ErrUnsupportedUploadedImage", err)
	}
	if len(cacher.bytesReq) != 0 {
		t.Fatalf("cached %d images, want none", len(cacher.bytesReq))
	}
}

func TestApplyUploadedItemImageReportsCacheFailures(t *testing.T) {
	t.Run("pixel data libvips cannot read is the upload's fault", func(t *testing.T) {
		cacher := &fakeByteImageCacher{}
		cacher.err = fmt.Errorf("imagecache: generate variants: %w", imageutil.ErrInvalidImage)
		service := &MetadataService{}
		service.SetImageCacher(cacher)
		_, err := service.ApplyUploadedItemImage(context.Background(), ApplyUploadedItemImageRequest{ContentID: "movie-1", ContentType: "movie", ImageType: ImagePoster, Data: uploadedPNG(t)})
		if !errors.Is(err, ErrUnsupportedUploadedImage) {
			t.Fatalf("err = %v, want ErrUnsupportedUploadedImage", err)
		}
	})

	t.Run("a storage failure is not", func(t *testing.T) {
		cacher := &fakeByteImageCacher{}
		cacher.err = errors.New("put: access denied")
		service := &MetadataService{}
		service.SetImageCacher(cacher)
		_, err := service.ApplyUploadedItemImage(context.Background(), ApplyUploadedItemImageRequest{ContentID: "movie-1", ContentType: "movie", ImageType: ImagePoster, Data: uploadedPNG(t)})
		if err == nil || errors.Is(err, ErrUnsupportedUploadedImage) {
			t.Fatalf("err = %v, want a server error", err)
		}
	})

	t.Run("a cacher that cannot store bytes", func(t *testing.T) {
		service := &MetadataService{}
		service.SetImageCacher(&fakeImageCacher{})
		_, err := service.ApplyUploadedItemImage(context.Background(), ApplyUploadedItemImageRequest{ContentID: "movie-1", ContentType: "movie", ImageType: ImagePoster, Data: uploadedPNG(t)})
		if !errors.Is(err, ErrImageUploadUnavailable) {
			t.Fatalf("err = %v, want ErrImageUploadUnavailable", err)
		}
	})
}
