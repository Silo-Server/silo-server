package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/logredact"
)

// localArtworkPreviewWidth matches the "card" width the admin picker shows
// provider choices at.
const localArtworkPreviewWidth = 300

// localArtworkCapabilityID is the built-in provider that discovers sidecar
// artwork next to an item's files.
const localArtworkCapabilityID = "nfo"

// ErrLocalImageNotOffered rejects a local image the item's own discovery did
// not return, so the apply endpoint cannot be used to read arbitrary files.
var ErrLocalImageNotOffered = errors.New("local image is not one of the item's local images")

// IsLocalImageSource reports whether an image URL names a local sidecar file.
func IsLocalImageSource(url string) bool {
	return isLocalImageSourcePath(url)
}

// FetchItemImagesWithLocal is FetchItemImages plus the item's local sidecar
// artwork. Local choices come only from Silo's own sidecar discovery, run
// whether or not the library's metadata chain uses the NFO provider: a
// file:// URL from any provider in the chain is dropped, so a provider cannot
// get an arbitrary library file offered as this item's artwork.
func (s *MetadataService) FetchItemImagesWithLocal(ctx context.Context, providerIDs map[string]string, contentType string, language string, folderID int, contentID string) ([]RemoteImage, map[string]string, error) {
	images, providerErrors, err := s.FetchItemImages(ctx, providerIDs, contentType, language, folderID)
	if err != nil {
		// Sidecar discovery does not use the chain, so a chain failure still
		// lists the item's local artwork and reports the failure.
		slog.WarnContext(ctx, "metadata: image provider chain unavailable for picker", "component", "metadata", "folder_id", folderID, "error", logredact.SanitizeText(err.Error()))
		images, providerErrors = nil, map[string]string{"chain": "Image provider chain unavailable"}
	}
	images = slices.DeleteFunc(images, func(image RemoteImage) bool {
		return isLocalImageSourcePath(image.URL)
	})
	local, err := s.localItemImages(ctx, contentType, folderID, contentID)
	if err != nil {
		// The error can carry paths and provider text, so the listing only says
		// which source failed, and the log keeps the text with any credential
		// assignments masked.
		slog.WarnContext(ctx, "metadata: local artwork discovery failed for picker", "component", "metadata", "folder_id", folderID, "content_id", contentID, "error", logredact.SanitizeText(err.Error()))
		if providerErrors == nil {
			providerErrors = map[string]string{}
		}
		providerErrors[imageCacheLocalProviderID] = "Local artwork discovery failed"
	}
	return append(images, local...), providerErrors, nil
}

// localItemImages runs sidecar discovery for an item with its media files and
// sidecar directories, finding the same poster, fanart and logo files a
// refresh would use.
func (s *MetadataService) localItemImages(ctx context.Context, contentType string, folderID int, contentID string) ([]RemoteImage, error) {
	discovery := s.localImageProvider
	if discovery == nil {
		provider, ok := builtinProvider(localArtworkCapabilityID)
		if !ok {
			return nil, nil
		}
		if discovery, ok = provider.(ImageProvider); !ok {
			return nil, nil
		}
	}
	localCtx := s.localProviderContextForContent(ctx, contentID, folderID)
	if localCtx.representativeFilePath == "" && len(localCtx.primarySidecarSearchPaths) == 0 {
		return nil, nil
	}
	images, err := discovery.GetImages(ctx, ImageRequest{
		ContentType:               contentType,
		RepresentativeFilePath:    localCtx.representativeFilePath,
		AllGroupFilePaths:         localCtx.allGroupFilePaths,
		PrimarySidecarSearchPaths: localCtx.primarySidecarSearchPaths,
	})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(images, func(image RemoteImage) bool {
		return !isLocalImageSourcePath(image.URL)
	}), nil
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
	images, err := s.localItemImages(ctx, req.ContentType, req.FolderID, req.ContentID)
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
