package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// Shared artwork helpers used by both the admin library_collections handler
// and the user collections handler. The S3 prefix differs so the two
// namespaces don't collide.
const (
	adminCollectionImagePrefix = "collection-images"
	userCollectionImagePrefix  = "user-collection-images"
	collectionTemplateImageDir = "/images/collection-templates/"

	collectionImageMaxBytes = 10 << 20 // 10 MB
)

// storeBundledCollectionPosterIfS3Configured stores a built-in collection
// template poster in S3 when public asset storage is configured. Non-S3
// installs and non-template paths keep the original persisted path.
func storeBundledCollectionPosterIfS3Configured(
	ctx context.Context,
	store blobstore.Store,
	frontendFS fs.FS,
	collectionID, prefix, posterPath string,
) (storedPath, thumbhashStr string, stored bool, err error) {
	posterPath = strings.TrimSpace(posterPath)
	if store == nil || !strings.HasPrefix(posterPath, collectionTemplateImageDir) {
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

	storedPath, thumbhashStr, err = uploadCollectionImageVariants(ctx, store, prefix, collectionID, "poster", data)
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

// invalidCollectionImage reports artwork the caller supplied that Silo cannot
// use. It is a 400 so both listeners answer with a validation error rather
// than a 500.
func invalidCollectionImage(message string, cause error) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: policyErrorBadRequest, Message: message, cause: cause}
}

// collectionArtworkError keeps a client-facing artwork error and hides any
// other failure behind a 500 with the given message.
func collectionArtworkError(err error, message string) error {
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Status < http.StatusInternalServerError {
		return apiErr
	}
	return apiError(http.StatusInternalServerError, "internal_error", message)
}

// downloadCollectionImageURL fetches an image from an http(s) URL with size
// limits.
func downloadCollectionImageURL(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, invalidCollectionImage("The image source URL is not valid.", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, invalidCollectionImage("The image source URL must use http or https.", nil)
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
		return nil, invalidCollectionImage(fmt.Sprintf("The image source returned status %d.", resp.StatusCode), nil)
	}
	if resp.ContentLength > collectionImageMaxBytes {
		return nil, invalidCollectionImage("The image exceeds the 10 MB limit.", nil)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, collectionImageMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading image response: %w", err)
	}
	if len(data) > collectionImageMaxBytes {
		return nil, invalidCollectionImage("The image exceeds the 10 MB limit.", nil)
	}
	return data, nil
}

// uploadCollectionImageVariants generates resized variants for the given
// image bytes, uploads them under "{prefix}/{collectionID}/{imageType}/", and
// returns the S3 path of the original variant plus a thumbhash computed from
// the w300 variant.
func uploadCollectionImageVariants(
	ctx context.Context,
	store blobstore.Store,
	prefix, collectionID, imageType string,
	fileData []byte,
) (s3Path, thumbhashStr string, err error) {
	if store == nil {
		return "", "", fmt.Errorf("image upload requires configured S3 storage")
	}
	result, err := generateCollectionImageVariants(imageType, fileData)
	if err != nil {
		return "", "", err
	}
	return storeCollectionImageVariants(ctx, store, prefix, collectionID, imageType, result)
}

// generateCollectionImageVariants decodes the image and renders its resized
// variants without touching storage. Bytes that are not a supported image
// fail with invalidCollectionImage.
func generateCollectionImageVariants(imageType string, fileData []byte) (*imageutil.VariantResult, error) {
	var widths []int
	switch imageType {
	case "poster":
		widths = []int{500, 300}
	case "backdrop":
		widths = []int{1280, 300}
	default:
		return nil, fmt.Errorf("invalid image type: %s", imageType)
	}

	result, err := imageutil.GenerateVariants(fileData, widths)
	if err != nil {
		return nil, invalidCollectionImage("The file is not a supported image.", err)
	}
	return result, nil
}

// storeCollectionImageVariants uploads generated variants under
// "{prefix}/{collectionID}/{imageType}/" and returns the path of the original
// variant plus a thumbhash computed from the w300 variant.
func storeCollectionImageVariants(
	ctx context.Context,
	store blobstore.Store,
	prefix, collectionID, imageType string,
	result *imageutil.VariantResult,
) (s3Path, thumbhashStr string, err error) {
	var w300Data []byte
	for _, v := range result.Variants {
		key := fmt.Sprintf("%s/%s/%s/%s%s", prefix, collectionID, imageType, v.Key, result.Ext)
		if err := store.Put(ctx, key, v.Data); err != nil {
			return "", "", fmt.Errorf("uploading %s: %w", v.Key, err)
		}
		if v.Key == "w300" {
			w300Data = v.Data
		}
		if v.Key == "original" {
			s3Path = key
		}
	}

	if len(w300Data) > 0 {
		thumbhashStr, err = imageutil.Thumbhash(w300Data)
		if err != nil {
			return "", "", fmt.Errorf("computing thumbhash: %w", err)
		}
	}
	return s3Path, thumbhashStr, nil
}

// removeCollectionImageVariants deletes every stored variant for the given
// collection / imageType under the supplied S3 prefix.
func removeCollectionImageVariants(
	ctx context.Context,
	store blobstore.Store,
	prefix, collectionID, imageType string,
) error {
	if store == nil {
		return nil
	}
	p := fmt.Sprintf("%s/%s/%s/", prefix, collectionID, imageType)
	items, _, err := store.List(ctx, p, "", 0)
	if err != nil {
		return fmt.Errorf("listing objects: %w", err)
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Key)
	}
	if _, err := store.Delete(ctx, keys); err != nil {
		return fmt.Errorf("deleting collection variants: %w", err)
	}
	return nil
}
