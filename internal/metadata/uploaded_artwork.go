package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// uploadedImageSourcePrefix marks artwork an admin uploaded. Its bytes exist
// only in artwork storage, so it is one of the non-provider schemes image
// caching, reconcile and the ladder backfill never try to download.
const uploadedImageSourcePrefix = "upload://"

// MaxUploadedImageBytes caps an uploaded poster, backdrop or logo, like
// collection artwork.
const MaxUploadedImageBytes = 10 << 20

// ErrUnsupportedUploadedImage rejects an upload that is not a JPEG, PNG or
// WebP image, or whose pixel data does not decode.
var ErrUnsupportedUploadedImage = errors.New("uploaded file is not a JPEG, PNG or WebP image")

// ErrImageUploadUnavailable reports a server whose image caching cannot store
// uploaded bytes.
var ErrImageUploadUnavailable = errors.New("image caching does not support uploaded artwork")

// uploadableImageTypes are the formats an upload may be, by its bytes rather
// than the type the client declared.
var uploadableImageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

// IsUploadedImageSource reports whether an image source names artwork an
// admin uploaded (upload://).
func IsUploadedImageSource(source string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(source)), uploadedImageSourcePrefix)
}

// ApplyUploadedItemImageRequest describes an image an admin uploaded for a
// movie or series, or a season's poster.
type ApplyUploadedItemImageRequest struct {
	ContentID    string // the movie or series; a season's series
	ContentType  string // "movie" or "series"
	ImageType    ImageType
	SeasonNumber *int // set for a season poster
	Data         []byte
}

// ApplyUploadedItemImageResult is the cached upload and the source to publish
// with it.
type ApplyUploadedItemImageResult struct {
	ApplyItemImageResult
	SourcePath string // upload:// and the image's SHA-256
}

// ApplyUploadedItemImage caches an uploaded image under the item's local/
// keys, beside its local sidecar artwork, so the picker lists it as a local
// choice. The source records only the image's digest: nothing can download it
// again.
func (s *MetadataService) ApplyUploadedItemImage(ctx context.Context, req ApplyUploadedItemImageRequest) (*ApplyUploadedItemImageResult, error) {
	byteCacher, ok := s.imageCacher.(ImageByteCacher)
	if !ok {
		return nil, ErrImageUploadUnavailable
	}
	if len(req.Data) == 0 || !uploadableImageTypes[http.DetectContentType(req.Data)] {
		return nil, ErrUnsupportedUploadedImage
	}
	digest := sha256.Sum256(req.Data)
	source := uploadedImageSourcePrefix + "sha256:" + hex.EncodeToString(digest[:])
	result, err := byteCacher.CacheImageBytes(ctx, req.Data, CacheImageRequest{
		SourceURL:        source,
		ProviderID:       imageCacheLocalProviderID,
		ContentType:      imageCacheContentType(req.ContentType),
		ContentID:        req.ContentID,
		ImageType:        req.ImageType,
		SeasonNumber:     req.SeasonNumber,
		KeyDiscriminator: hex.EncodeToString(digest[:4]),
	})
	if err != nil {
		if errors.Is(err, imageutil.ErrInvalidImage) || imageutil.PixelDataUndecodable(req.Data) {
			return nil, fmt.Errorf("%w: %w", ErrUnsupportedUploadedImage, err)
		}
		return nil, fmt.Errorf("caching image: %w", err)
	}
	if result == nil {
		return nil, errors.New("image cache returned no result")
	}
	storedPath := CachedImageOriginalPath(result)
	if storedPath == "" {
		return nil, errors.New("image cache returned empty stored path")
	}
	return &ApplyUploadedItemImageResult{
		ApplyItemImageResult: ApplyItemImageResult{
			StoredPath: storedPath,
			Revision:   result.Revision,
			Thumbhash:  result.Thumbhash,
		},
		SourcePath: source,
	}, nil
}
