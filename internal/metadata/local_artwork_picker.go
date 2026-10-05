package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// localArtworkPreviewWidth matches the "card" width the admin picker shows
// provider choices at.
const localArtworkPreviewWidth = 300

// ErrLocalImageNotOffered rejects a local image the item's own discovery did
// not return, so the apply endpoint cannot be used to read arbitrary files.
var ErrLocalImageNotOffered = errors.New("local image is not one of the item's local images")

// IsLocalImageSource reports whether an image URL names a local sidecar file.
func IsLocalImageSource(url string) bool {
	return isLocalImageSourcePath(url)
}

// FetchItemImagesWithLocal is FetchItemImages plus the item's local sidecar
// artwork. The provider request also carries the item's media files and
// sidecar directories, so the NFO provider returns the same poster, fanart and
// logo files a refresh would use. Remote providers ignore that context.
func (s *MetadataService) FetchItemImagesWithLocal(ctx context.Context, providerIDs map[string]string, contentType string, language string, folderID int, contentID string) ([]RemoteImage, map[string]string, error) {
	localCtx := s.localProviderContextForContent(ctx, contentID, folderID)
	return s.fetchItemImages(ctx, ImageRequest{
		ProviderIDs:               providerIDs,
		ContentType:               contentType,
		Language:                  language,
		RepresentativeFilePath:    localCtx.representativeFilePath,
		AllGroupFilePaths:         localCtx.allGroupFilePaths,
		PrimarySidecarSearchPaths: localCtx.primarySidecarSearchPaths,
	}, folderID)
}

// LocalImagePreview returns a small WebP data URI of a local sidecar image for
// the admin picker. The file is read under the same library-root confinement,
// symlink and size checks as background caching.
func (s *MetadataService) LocalImagePreview(ctx context.Context, contentID string, sourceURL string) (string, error) {
	data, err := readConfinedLocalImage(ctx, s.libraryRoots, contentID, sourceURL)
	if err != nil {
		return "", err
	}
	preview, err := imageutil.EncodeWebPWidth(data, localArtworkPreviewWidth)
	if err != nil {
		return "", err
	}
	return "data:image/webp;base64," + base64.StdEncoding.EncodeToString(preview), nil
}

// ApplyLocalItemImageRequest describes a local sidecar image an admin chose
// for a movie or series.
type ApplyLocalItemImageRequest struct {
	ContentID   string
	ContentType string // "movie" or "series"
	ProviderIDs map[string]string
	Language    string
	FolderID    int
	ImageType   ImageType
	SourceURL   string // a file:// choice returned by FetchItemImagesWithLocal
}

// ApplyLocalItemImage caches a local sidecar image the item's own discovery
// offers. It stores the image under the same local/ key a refresh would use,
// so a later refresh of the same file finds it already cached.
func (s *MetadataService) ApplyLocalItemImage(ctx context.Context, req ApplyLocalItemImageRequest) (*ApplyItemImageResult, error) {
	byteCacher, ok := s.imageCacher.(ImageByteCacher)
	if !ok {
		return nil, fmt.Errorf("image caching does not support local artwork")
	}
	if !isLocalImageSourcePath(req.SourceURL) {
		return nil, ErrLocalImageNotOffered
	}
	images, _, err := s.FetchItemImagesWithLocal(ctx, req.ProviderIDs, req.ContentType, req.Language, req.FolderID, req.ContentID)
	if err != nil {
		return nil, err
	}
	offered := false
	for _, image := range images {
		if image.URL == req.SourceURL && image.Type == req.ImageType {
			offered = true
			break
		}
	}
	if !offered {
		return nil, ErrLocalImageNotOffered
	}

	data, err := readConfinedLocalImage(ctx, s.libraryRoots, req.ContentID, req.SourceURL)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	result, err := byteCacher.CacheImageBytes(ctx, data, CacheImageRequest{
		SourceURL:        req.SourceURL,
		ProviderID:       imageCacheLocalProviderID,
		ContentType:      imageCacheContentType(req.ContentType),
		ContentID:        req.ContentID,
		ImageType:        req.ImageType,
		KeyDiscriminator: hex.EncodeToString(digest[:4]),
	})
	if err != nil {
		return nil, fmt.Errorf("caching image: %w", err)
	}
	if result == nil {
		return nil, errors.New("image cache returned no result")
	}
	storedPath := CachedImageOriginalPath(result)
	if storedPath == "" {
		return nil, errors.New("image cache returned empty stored path")
	}
	return &ApplyItemImageResult{
		StoredPath: storedPath,
		Revision:   result.Revision,
		Thumbhash:  result.Thumbhash,
	}, nil
}
