package handlers

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collage"
	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/netguard"
	"github.com/Silo-Server/silo-server/internal/usercollections"
)

// Shared artwork helpers used by both the admin library_collections handler
// and the user collections handler. The S3 prefix differs so the two
// namespaces don't collide.
const (
	adminCollectionImagePrefix = "collection-images"
	userCollectionImagePrefix  = "user-collection-images"
	collectionTemplateImageDir = "/images/collection-templates/"

	collectionImageMaxBytes = 10 << 20 // 10 MB

	collectionImageCleanupTimeout  = 30 * time.Second
	collectionImageDownloadTimeout = 30 * time.Second
)

// errCollectionImageSourceNotAllowed is the one answer for an image URL on
// the server's own network or in a blocked range. Every refused address gets
// it unchanged, so the answer says nothing about what listens there.
var errCollectionImageSourceNotAllowed = errors.New("the image URL must be a public internet address")

// newCollectionImageClient returns the client collection artwork downloads
// use. Its netguard transport checks every address it dials, redirect hops
// included: public addresses only, or the server's local network too when
// the request context carries netguard.WithPrivateAccess. See
// docs/architecture/outbound-address-guard.md.
func newCollectionImageClient() *http.Client {
	return netguard.NewClient(collectionImageDownloadTimeout)
}

// adminCollectionImageContext marks an admin collection artwork download as
// trusted with the server's local network. Only acting admins reach the
// library collection artwork routes; like an image an admin applies to an
// item, the URL may name a LAN host. netguard still refuses blocked addresses.
func adminCollectionImageContext(ctx context.Context) context.Context {
	return netguard.WithPrivateAccess(ctx)
}

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

// personalCollectionPosterURL resolves a personal collection's stored poster,
// or its collage, to a URL at card size. An image URL or a root-relative path
// such as a bundled template poster is returned as stored. A storage key is
// signed only when it is in the collection's own image directory
// (usercollections.OwnsPosterKey), where its uploaded poster and collages are
// written. Any other key reads as no poster, so a row that names one cannot
// make the server sign an object the collection does not own.
func personalCollectionPosterURL(ctx context.Context, resolver artworkurl.Resolver, collectionID, path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "/") {
		return catalog.ImageURLOrEmpty(path)
	}
	if resolver == nil || !usercollections.OwnsPosterKey(collectionID, path) {
		return ""
	}
	key := cardThumbnailPath(path)
	return resolver.ResolveURLs(ctx, []string{key})[key].URL
}

// invalidCollectionImage reports artwork the caller supplied that Silo cannot
// use. The v2 routes render the 400 as a validation problem instead of a 500.
func invalidCollectionImage(message string, cause error) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: policyErrorBadRequest, Message: message, cause: cause}
}

// collectionImageBusyRetryAfter is the Retry-After, in seconds, on a busy
// answer: the wait imageutil gives a source before it reports ErrBusy.
const collectionImageBusyRetryAfter = 30

// busyCollectionImage reports that image processing had no room for the
// artwork. The image may be fine, so the caller is told to try again and is
// not told the file is unsupported.
func busyCollectionImage(cause error) *APIError {
	return &APIError{
		Status:     http.StatusServiceUnavailable,
		Code:       policyErrorUnavailable,
		Message:    "Image processing is busy; try again shortly.",
		RetryAfter: collectionImageBusyRetryAfter,
		cause:      cause,
	}
}

// collectionArtworkError keeps a client-facing artwork error, including a
// busy answer, and hides any other failure behind a 500 with the given
// message.
func collectionArtworkError(err error, message string) error {
	if apiErr, ok := errors.AsType[*APIError](err); ok &&
		(apiErr.Status < http.StatusInternalServerError || apiErr.Status == http.StatusServiceUnavailable) {
		return apiErr
	}
	return apiError(http.StatusInternalServerError, "internal_error", message)
}

// personalCollectionPosterError answers a failed personal collection poster
// by its cause. A client-facing error, including a busy answer, passes
// through unchanged, and a source address the server will not fetch keeps its
// one fixed answer. Any other failure goes to the log, and the client gets a
// 500 that says nothing about the store or the source behind it. It takes no
// message, so no caller can put error text in the answer. collectionID is
// empty when the collection does not exist yet.
func personalCollectionPosterError(ctx context.Context, collectionID string, err error) error {
	if apiErr, ok := errors.AsType[*APIError](err); ok &&
		(apiErr.Status < http.StatusInternalServerError || apiErr.Status == http.StatusServiceUnavailable) {
		return apiErr
	}
	if errors.Is(err, errCollectionImageSourceNotAllowed) {
		return apiError(http.StatusInternalServerError, "internal_error", "poster source: "+errCollectionImageSourceNotAllowed.Error())
	}
	if download, ok := errors.AsType[*collectionImageDownloadError](err); ok {
		// The caller named the source, and any profile can name one that
		// fails, so this is a warning. The line carries the source's host and
		// the kind of failure, and nothing else of the URL.
		slog.WarnContext(ctx, "collection artwork: downloading a poster source failed", "component", "api",
			"collection_id", collectionID, "host", download.host, "error", download.failure)
	} else if !errors.Is(err, context.Canceled) {
		// The server's own failure; a request the client ended is not one. An
		// artwork store reached over HTTP reports a failed request with the
		// object's URL, which is logged without its userinfo, query and
		// fragment.
		slog.ErrorContext(ctx, "collection artwork: saving a poster failed", "component", "api",
			"collection_id", collectionID, "error", logredact.SanitizeText(logredact.SanitizeURLError(err).Error()))
	}
	return apiError(http.StatusInternalServerError, "internal_error", "Failed to store collection artwork")
}

// collectionImageDownloadError is an image source the server could not
// download. It keeps the host the request named and the kind of failure, and
// none of the transport error's text: that text quotes the requested URL and,
// after a redirect the client could not follow, the response's Location
// header, and a credential can sit anywhere in either.
type collectionImageDownloadError struct {
	host    string
	failure string
}

func (e *collectionImageDownloadError) Error() string {
	return "downloading image from " + e.host + ": " + e.failure
}

// collectionImageDownloadFailure reduces err, a failed download from host, to
// an error that is safe to log. A request the client ended stays
// context.Canceled.
func collectionImageDownloadFailure(host string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("downloading image: %w", context.Canceled)
	}
	failure := "request failed"
	if timeout, ok := errors.AsType[net.Error](err); ok && timeout.Timeout() {
		failure = "timed out"
	} else if _, ok := errors.AsType[*net.DNSError](err); ok {
		failure = "DNS lookup failed"
	} else if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		failure = "TLS certificate not accepted"
	} else if _, ok := errors.AsType[*net.OpError](err); ok {
		failure = "connection failed"
	}
	return &collectionImageDownloadError{host: host, failure: failure}
}

// downloadCollectionImageURL fetches an image from an http(s) URL with size
// limits. The address policy lives in client's netguard transport
// (newCollectionImageClient), and ctx decides whether the local network is
// allowed. An address the policy refuses fails with
// errCollectionImageSourceNotAllowed. A request or a body read that fails any
// other way fails with a collectionImageDownloadError, which keeps the host
// and drops the transport's own text.
func downloadCollectionImageURL(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	if client == nil {
		return nil, errors.New("collection image downloads have no HTTP client")
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, invalidCollectionImage("The image source URL is not valid.", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, invalidCollectionImage("The image source URL must use http or https.", nil)
	}
	if parsed.Hostname() == "" {
		return nil, invalidCollectionImage("The image source URL is not valid.", nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, invalidCollectionImage("The image source URL is not valid.", nil)
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, netguard.ErrPrivateDestination) || errors.Is(err, netguard.ErrBlockedDestination) {
			return nil, errCollectionImageSourceNotAllowed
		}
		return nil, collectionImageDownloadFailure(parsed.Host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The client message omits the upstream status so the route does not
		// report how an arbitrary URL answered; keep it for operators. The
		// host is the one that answered, after any redirects.
		slog.InfoContext(ctx, "collection artwork source did not return an image", "component", "api",
			"host", resp.Request.URL.Host, "status", resp.StatusCode)
		return nil, invalidCollectionImage("The image source did not return an image.", nil)
	}
	if resp.ContentLength > collectionImageMaxBytes {
		return nil, invalidCollectionImage("The image exceeds the 10 MB limit.", nil)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, collectionImageMaxBytes+1))
	if err != nil {
		return nil, collectionImageDownloadFailure(parsed.Host, err)
	}
	if len(data) > collectionImageMaxBytes {
		return nil, invalidCollectionImage("The image exceeds the 10 MB limit.", nil)
	}
	return data, nil
}

// uploadCollectionImageVariants generates resized variants for the given
// image bytes, uploads them as revisioned artwork keys under
// "{prefix}/{collectionID}/{imageType}/" (e.g. "original.{revision}.webp"), and
// returns the S3 path of the original variant plus a thumbhash computed from
// the w300 variant. The content revision gives replacement artwork a new URL
// (issue #1258).
func uploadCollectionImageVariants(
	ctx context.Context,
	store blobstore.Store,
	prefix, collectionID, imageType string,
	fileData []byte,
) (s3Path, thumbhashStr string, err error) {
	var widths []int
	switch imageType {
	case "poster":
		widths = collectionPosterWidths
	case "backdrop":
		widths = collectionBackdropWidths
	default:
		return "", "", fmt.Errorf("invalid image type: %s", imageType)
	}

	// Revision the key by content so replacement artwork lands on a new key,
	// and therefore a new public URL, rather than overwriting a fixed key that
	// stays cached by the CDN and browsers (issue #1258). Revisions stay in the
	// imageType directory, so deleting an admin collection still clears every
	// one of them by that prefix.
	basePath := collectionImageDir(prefix, collectionID, imageType)
	return putCollectionImageVariants(ctx, store, basePath, collectionImageRevision(fileData), widths, fileData)
}

// collectionPosterWidths are the resized variants stored beside an original
// collection poster.
var collectionPosterWidths = catalog.CollectionPosterWidths

// collectionBackdropWidths are the resized variants stored beside an original
// collection backdrop, widest first. A collection page shows the widest.
var collectionBackdropWidths = []int{1280, 300}

// putCollectionImageVariants generates the given resized variants of fileData
// and uploads them, with the original, as basePath/{variant}.{revision}.{ext}.
// It returns the original's key and a thumbhash of the w300 variant.
func putCollectionImageVariants(
	ctx context.Context,
	store blobstore.Store,
	basePath, revision string,
	widths []int,
	fileData []byte,
) (s3Path, thumbhashStr string, err error) {
	if store == nil {
		return "", "", fmt.Errorf("image upload requires configured S3 storage")
	}
	variants, err := generateCollectionImageVariants(fileData, widths)
	if err != nil {
		return "", "", err
	}
	s3Path, err = storeCollectionImageVariants(ctx, store, basePath, revision, variants)
	if err != nil {
		return "", "", err
	}
	return s3Path, variants.thumbhash, nil
}

// collectionImageVariants is collection artwork decoded into the variants to
// store, with a thumbhash of the w300 variant.
type collectionImageVariants struct {
	result    *imageutil.VariantResult
	thumbhash string
}

// generateCollectionImageVariants decodes fileData into the original and the
// given resized variants and computes the thumbhash. Every decode happens
// here and nothing is stored, so a caller can refuse an image before it
// writes anything.
func generateCollectionImageVariants(fileData []byte, widths []int) (*collectionImageVariants, error) {
	// Bytes libvips cannot read, and JPEG or PNG pixel data that does not
	// decode, are the caller's artwork problem; any other failure stays a
	// server error.
	result, err := imageutil.GenerateVariants(fileData, widths)
	if err != nil && imageutil.SourceUnusable(err, fileData) {
		return nil, invalidCollectionImage("The file is not a supported image.", err)
	}
	if errors.Is(err, imageutil.ErrBusy) {
		return nil, busyCollectionImage(err)
	}
	if err != nil {
		return nil, fmt.Errorf("generating image variants: %w", err)
	}
	variants := &collectionImageVariants{result: result}
	for _, v := range result.Variants {
		if v.Key == "w300" && len(v.Data) > 0 {
			variants.thumbhash, err = imageutil.Thumbhash(v.Data)
			if errors.Is(err, imageutil.ErrBusy) {
				return nil, busyCollectionImage(err)
			}
			if err != nil {
				return nil, fmt.Errorf("computing thumbhash: %w", err)
			}
		}
	}
	return variants, nil
}

// storeCollectionImageVariants uploads generated variants as
// basePath/{variant}.{revision}.{ext} and returns the original's key.
func storeCollectionImageVariants(
	ctx context.Context,
	store blobstore.Store,
	basePath, revision string,
	variants *collectionImageVariants,
) (s3Path string, err error) {
	for _, v := range variants.result.Variants {
		key := artworkkey.Build(basePath, v.Key, revision, variants.result.Ext)
		if err := store.Put(ctx, key, v.Data); err != nil {
			return "", fmt.Errorf("uploading %s: %w", v.Key, err)
		}
		if v.Key == artworkkey.OriginalVariant {
			s3Path = key
		}
	}
	return s3Path, nil
}

// collectionImageRevision derives a short content revision for artwork keys.
// Two uploads with the same bytes reuse the same revision (idempotent
// re-upload); different bytes produce a different revision, so a replacement is
// served from a new URL that no cache holds yet.
func collectionImageRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// collectionImageDir returns the directory holding every revision and variant
// of one collection image, without a trailing slash.
func collectionImageDir(prefix, collectionID, imageType string) string {
	return fmt.Sprintf("%s/%s/%s", prefix, collectionID, imageType)
}

// collectionCollageDir returns the directory holding a collection's generated
// collages, without a trailing slash. Each collage is stored with its key as
// the revision.
func collectionCollageDir(prefix, collectionID string) string {
	return collectionImageDir(prefix, collectionID, "collage")
}

// removeReplacedCollectionImageVersion deletes the variants of the revision
// that a replacement or clear superseded, identified from the previously
// stored path. It runs after the change is committed, and currentPath is the
// path the row holds on a fresh read at cleanup time.
//
// Only the superseded revision is removed, never "everything but the current
// revision", so a concurrent upload that committed its own revision is never
// deleted. The revision is skipped when the row still points at it
// (currentPath), which guards against a concurrent restore of the same content:
// re-uploading the old bytes reuses the old keys, so deleting them would strip
// the artwork the row now references. A legacy fixed key (no revision) selects
// the unrevisioned variants in the same directory. An oldPath outside this
// collection image's directory (a bundled-template path, or empty) is a no-op.
func removeReplacedCollectionImageVersion(
	ctx context.Context,
	store blobstore.Store,
	prefix, collectionID, imageType, oldPath, currentPath string,
) error {
	if store == nil {
		return nil
	}
	dir := collectionImageDir(prefix, collectionID, imageType) + "/"
	if !isCollectionImageKeyIn(dir, oldPath) {
		return nil
	}
	oldRevision := artworkkey.Revision(oldPath)
	if isCollectionImageKeyIn(dir, currentPath) && artworkkey.Revision(currentPath) == oldRevision {
		return nil
	}
	items, _, err := store.List(ctx, dir, "", 0)
	if err != nil {
		return fmt.Errorf("listing objects: %w", err)
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if isCollectionImageKeyIn(dir, item.Key) && artworkkey.Revision(item.Key) == oldRevision {
			keys = append(keys, item.Key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if _, err := store.Delete(ctx, keys); err != nil {
		return fmt.Errorf("deleting replaced collection variants: %w", err)
	}
	return nil
}

// cleanUpReplacedCollectionImage runs after a replacement image or a clear is
// committed. It reads the path the row now holds and removes the revision
// oldPath named. Callers commit the new reference (a new revision, or empty)
// before calling this, so a failed upload, update, or delete never leaves the
// row pointing at missing or partly deleted artwork (issue #2419). Failures
// here only log: the worst case is an orphaned revision. The change is already
// committed, so cleanup outlives a canceled request, bounded by
// collectionImageCleanupTimeout.
func cleanUpReplacedCollectionImage(
	ctx context.Context,
	store blobstore.Store,
	prefix, collectionID, imageType, oldPath string,
	readCurrent func(context.Context) (string, error),
) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), collectionImageCleanupTimeout)
	defer cancel()
	currentPath, err := readCurrent(ctx)
	if err != nil {
		slog.WarnContext(ctx, "collection artwork: skipping variant cleanup, re-read failed", "component", "api", "collection_id", collectionID, "kind", imageType, "error", err)
		return
	}
	if err := removeReplacedCollectionImageVersion(ctx, store, prefix, collectionID, imageType, oldPath, currentPath); err != nil {
		slog.WarnContext(ctx, "collection artwork: previous variant cleanup failed", "component", "api", "collection_id", collectionID, "kind", imageType, "error", err)
	}
}

// removeDeletedCollectionArtwork deletes deleted collections' whole artwork
// directories: every image revision and collage. The row deletes have
// committed, so the cleanup outlives a canceled request, bounded as a whole by
// collectionImageCleanupTimeout, and a failure only logs.
func removeDeletedCollectionArtwork(ctx context.Context, store blobstore.Store, prefix string, collectionIDs ...string) {
	if store == nil || len(collectionIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), collectionImageCleanupTimeout)
	defer cancel()
	for _, collectionID := range collectionIDs {
		if _, err := store.DeletePrefix(ctx, fmt.Sprintf("%s/%s/", prefix, collectionID)); err != nil {
			slog.WarnContext(ctx, "collection delete: removing artwork failed", "component", "api", "collection_id", collectionID, "prefix", prefix, "error", err)
		}
	}
}

// isCollectionImageKeyIn reports whether key is an object directly inside dir
// (which carries a trailing slash).
func isCollectionImageKeyIn(dir, key string) bool {
	name, ok := strings.CutPrefix(key, dir)
	return ok && name != "" && !strings.Contains(name, "/")
}

// collectionCollageComposer composes collection collages from title posters
// and stores them under one artwork prefix (catalog.CollageGenerator). Each
// collage is stored in the collection's collage directory with its key as the
// revision, so every node that builds the same collage writes the same
// objects.
type collectionCollageComposer struct {
	prefix     string
	store      blobstore.Store
	posters    itemPosterSigner
	signer     *artworkurl.Signer
	httpClient *http.Client
}

// NewPersonalCollectionCollageGenerator composes personal collection
// collages and stores them beside their uploaded posters. It returns nil
// when artwork storage or poster signing is not configured, which leaves
// personal collections without collages. signer is the artwork URL signer
// that local storage resolves posters with; nil when storage serves its own
// URLs. A nil httpClient uses newCollectionImageClient.
func NewPersonalCollectionCollageGenerator(store blobstore.Store, posters itemPosterSigner, signer *artworkurl.Signer, httpClient *http.Client) catalog.CollageGenerator {
	if store == nil || posters == nil {
		return nil
	}
	if httpClient == nil {
		httpClient = newCollectionImageClient()
	}
	return collectionCollageComposer{prefix: userCollectionImagePrefix, store: store, posters: posters, signer: signer, httpClient: httpClient}
}

// ComposeCollectionCollage fetches the source posters, composes them and
// stores the result.
func (c collectionCollageComposer) ComposeCollectionCollage(ctx context.Context, collectionID, key string, sources []string) (string, string, error) {
	if len(sources) == 0 {
		return "", "", collage.ErrNotEnoughImages
	}

	slog.InfoContext(ctx, "collage: generating poster", "component", "api", "collection_id", collectionID, "item_poster_count", len(sources))

	// Resolve poster paths to fetchable URLs. The collage is stored under the
	// key of all its sources, so a source that doesn't resolve or download
	// fails the build rather than being left out. A later read retries it.
	resolved := c.posters.PresignImageURLs(ctx, sources, "poster", "small")
	imageData := make([][]byte, 0, len(sources))
	for _, path := range sources {
		imageURL := resolved[path]
		if imageURL == "" {
			return "", "", fmt.Errorf("collage source %q did not resolve", path)
		}
		data, err := c.fetchImage(ctx, imageURL)
		if err != nil {
			// A transport error names the presigned URL; keep it out of logs.
			if urlErr, ok := errors.AsType[*url.Error](err); ok {
				err = urlErr.Err
			}
			return "", "", fmt.Errorf("fetching collage source %q: %w", path, err)
		}
		imageData = append(imageData, data)
	}

	composited, err := collage.ComposePoster(imageData)
	if err != nil {
		return "", "", err
	}

	// Process through the standard image pipeline (generates WebP variants + thumbhash).
	s3Path, thumbhash, err := putCollectionImageVariants(ctx, c.store, collectionCollageDir(c.prefix, collectionID), key, collectionPosterWidths, composited)
	if err != nil {
		return "", "", fmt.Errorf("processing collage image: %w", err)
	}
	if want := c.CollectionCollagePath(collectionID, key); s3Path != want {
		return "", "", fmt.Errorf("collage stored at %q, want %q", s3Path, want)
	}

	slog.InfoContext(ctx, "collage: poster generated successfully", "component", "api", "collection_id", collectionID, "s3_path", s3Path)
	return s3Path, thumbhash, nil
}

// CollectionCollagePath returns the path ComposeCollectionCollage stores the
// collection's collage key under.
func (c collectionCollageComposer) CollectionCollagePath(collectionID, key string) string {
	return artworkkey.Original(collectionCollageDir(c.prefix, collectionID), key, ".webp")
}

// fetchImage downloads an image from a resolved URL. The URL is a catalog
// title's poster as the server resolved it. Local artwork storage resolves
// posters to this server's own signed artwork route, a root-relative URL that
// is read from the store directly, as offline downloads do. Otherwise it is a
// direct URL to the artwork store, which may be on the local network, or a
// metadata provider's URL that was not cached, which is untrusted.
// artworkurl.FetchContext tells those apart, and netguard refuses blocked
// addresses either way.
func (c collectionCollageComposer) fetchImage(ctx context.Context, imageURL string) ([]byte, error) {
	if c.store != nil && c.signer != nil {
		if key, ok := c.signer.SignedKey(imageURL, time.Now()); ok {
			body, _, err := c.store.Get(ctx, key)
			if err != nil {
				return nil, fmt.Errorf("reading stored poster: %w", err)
			}
			defer func() { _ = body.Close() }()
			return io.ReadAll(io.LimitReader(body, collectionImageMaxBytes))
		}
	}
	if c.httpClient == nil {
		return nil, errors.New("collage downloads have no HTTP client")
	}
	req, err := http.NewRequestWithContext(artworkurl.FetchContext(ctx, c.store, imageURL), http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image fetch returned status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, collectionImageMaxBytes))
}
