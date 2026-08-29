package handlers

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkupload"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

// Shared artwork helpers used by both the admin library_collections handler and
// the user collections handler.
//
// New collection artwork is materialized through the canonical artwork store as
// an immutable, content-addressed artwork/v1 revision, so admin and user
// collections no longer need separate key namespaces to avoid collisions —
// identical bytes are the same revision, and the reference-aware collector is
// what decides when one stops being needed. The two legacy prefixes below are
// retained only so pre-existing mutable objects stay readable and sweepable
// until their owning row is next replaced.
const (
	adminCollectionImagePrefix = "collection-images"
	userCollectionImagePrefix  = "user-collection-images"
	collectionTemplateImageDir = "/images/collection-templates/"

	collectionImageMaxBytes = 10 << 20 // 10 MB
)

func userCollectionPosterTarget(userID int, collectionID string) artworkurl.Target {
	return artworkurl.Target{
		Surface: artworkurl.SurfaceUserCollectionPosters,
		Keys:    []string{strconv.Itoa(userID), collectionID},
		Slot:    artworkkey.ImageTypeCollectionPoster,
	}
}

// storeBundledCollectionPoster materializes a built-in collection template
// poster into the artwork store, so the collection keeps its artwork if the
// bundled asset is later renamed or dropped. Non-template paths and installs
// without artwork storage keep the original app-relative path, which the API
// serves straight from the frontend bundle.
//
// track must be true only when the owning row lands in a catalog table the
// artwork revision GC can see; see materializeCollectionImage.
func storeBundledCollectionPoster(
	ctx context.Context,
	uploads *artworkupload.Materializer,
	frontendFS fs.FS,
	posterPath string,
	track bool,
) (storedPath, thumbhashStr string, stored bool, err error) {
	posterPath = strings.TrimSpace(posterPath)
	if !uploads.Available() || !strings.HasPrefix(posterPath, collectionTemplateImageDir) {
		return posterPath, "", false, nil
	}
	if frontendFS == nil {
		return "", "", false, fmt.Errorf("frontend assets are not available")
	}

	assetPath := strings.TrimPrefix(posterPath, "/")
	data, err := fs.ReadFile(frontendFS, assetPath)
	if err != nil {
		return "", "", false, fmt.Errorf("reading bundled poster %q: %w", posterPath, err)
	}

	storedPath, thumbhashStr, err = materializeCollectionImage(ctx, uploads, artworkkey.ImageTypePoster, data, track)
	if err != nil {
		return "", "", false, err
	}
	return storedPath, thumbhashStr, true, nil
}

// readCollectionImageMultipart reads a single image file from a multipart
// request, validating MIME type and size.
func readCollectionImageMultipart(r *http.Request, fieldName string) ([]byte, error) {
	file, header, err := r.FormFile(fieldName)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	switch header.Header.Get("Content-Type") {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return nil, fmt.Errorf("unsupported image type: %s", header.Header.Get("Content-Type"))
	}
	if header.Size > collectionImageMaxBytes {
		return nil, fmt.Errorf("file exceeds 10 MB limit")
	}

	data := make([]byte, header.Size)
	if _, err := io.ReadFull(file, data); err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}
	return data, nil
}

// downloadCollectionImageURL fetches an image from an http(s) URL with size
// limits.
func downloadCollectionImageURL(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("image source URL must use http or https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image source returned status %d", resp.StatusCode)
	}
	if resp.ContentLength > collectionImageMaxBytes {
		return nil, fmt.Errorf("image exceeds 10 MB limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, collectionImageMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading image response: %w", err)
	}
	if len(data) > collectionImageMaxBytes {
		return nil, fmt.Errorf("image exceeds 10 MB limit")
	}
	return data, nil
}

// materializeCollectionImage stores collection artwork as an immutable
// content-addressed revision and returns the logical key of its original
// variant plus a thumbhash placeholder.
//
// slot is the collection's artwork slot ("poster" or "backdrop"); it selects
// the upload image type, and with it the variant ladder. The returned key is
// what the collection row is pointed at.
//
// track registers the revision for garbage collection before the first byte
// is written — so an upload that fails between here and the row update cleans
// itself up, and the revision this one displaces is collected once nothing
// references it. It must be true only when the owning row lands in a catalog
// table the collector's reference union can see: admin collections
// (library_collections) always qualify, user personal collections only when
// the user store is Postgres-backed, because rows in per-user SQLite files
// are invisible to the union and a tracked-but-invisible revision would be a
// live image scheduled for deletion.
func materializeCollectionImage(
	ctx context.Context,
	uploads *artworkupload.Materializer,
	slot string,
	fileData []byte,
	track bool,
) (storedPath, thumbhashStr string, err error) {
	if !uploads.Available() {
		return "", "", fmt.Errorf("image upload requires configured artwork storage")
	}
	imageType, ok := artworkkey.CollectionImageType(slot)
	if !ok {
		return "", "", fmt.Errorf("invalid image type: %s", slot)
	}
	result, err := uploads.Materialize(ctx, artworkupload.Request{
		ImageType: imageType,
		Data:      fileData,
		Track:     track,
	})
	if err != nil {
		return "", "", err
	}
	return result.OriginalKey, result.Thumbhash, nil
}

// removeLegacyCollectionImageVariants deletes the mutable per-collection
// objects written before collection artwork became content-addressed.
//
// It exists only for that migration window. New revisions must never be swept
// by prefix: a content-addressed revision directory can back several rows at
// once, and its lifetime belongs to the reference-aware collector. Installs
// without a legacy S3 bucket have nothing to sweep and skip it entirely.
func removeLegacyCollectionImageVariants(
	ctx context.Context,
	s3GP *s3client.Client,
	prefix, collectionID, imageType string,
) error {
	if s3GP == nil {
		return nil
	}
	p := fmt.Sprintf("%s/%s/%s/", prefix, collectionID, imageType)
	keys, err := s3GP.ListObjects(ctx, s3GP.Bucket(), p)
	if err != nil {
		return fmt.Errorf("listing objects: %w", err)
	}
	for _, key := range keys {
		if err := s3GP.DeleteObject(ctx, s3GP.Bucket(), key); err != nil {
			return fmt.Errorf("deleting %s: %w", key, err)
		}
	}
	return nil
}
