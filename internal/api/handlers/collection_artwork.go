package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collage"
	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/netguard"
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
// limits. The address policy lives in client's netguard transport
// (newCollectionImageClient), and ctx decides whether the local network is
// allowed. An address the policy refuses fails with
// errCollectionImageSourceNotAllowed.
func downloadCollectionImageURL(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	if client == nil {
		return nil, errors.New("collection image downloads have no HTTP client")
	}
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
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, netguard.ErrPrivateDestination) || errors.Is(err, netguard.ErrBlockedDestination) {
			return nil, errCollectionImageSourceNotAllowed
		}
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
		widths = []int{1280, 300}
	default:
		return "", "", fmt.Errorf("invalid image type: %s", imageType)
	}

	// Revision the key by content so replacement artwork lands on a new key,
	// and therefore a new public URL, rather than overwriting a fixed key that
	// stays cached by the CDN and browsers (issue #1258). Revisions stay in the
	// imageType directory, so removeCollectionImageVariants still clears every
	// one of them by that prefix.
	basePath := collectionImageDir(prefix, collectionID, imageType)
	return putCollectionImageVariants(ctx, store, basePath, collectionImageRevision(fileData), widths, fileData)
}

// collectionPosterWidths are the resized variants stored beside an original
// collection poster.
var collectionPosterWidths = catalog.CollectionPosterWidths

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
	result, err := imageutil.GenerateVariants(fileData, widths)
	if err != nil {
		return "", "", fmt.Errorf("generating image variants: %w", err)
	}

	var w300Data []byte
	for _, v := range result.Variants {
		key := artworkkey.Build(basePath, v.Key, revision, result.Ext)
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
// that a replacement superseded, identified from the previously stored path. It
// runs after the new revision is committed, and currentPath is the path the row
// holds on a fresh read at cleanup time.
//
// Only the superseded revision is removed, never "everything but the new
// revision", so a concurrent replacement that committed its own revision is
// never deleted. The revision is skipped when the row still points at it
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

// cleanUpReplacedCollectionImage runs after a replacement image is committed.
// It reads the path the row now holds and removes the revision oldPath named.
// Callers upload the replacement under its own revision and commit it before
// calling this, so a failed upload or update leaves the stored artwork intact.
// Failures here only log: the committed artwork is intact and the worst case
// is an orphaned revision. The replacement is already committed, so cleanup
// outlives a canceled request, bounded by collectionImageCleanupTimeout.
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

// isCollectionImageKeyIn reports whether key is an object directly inside dir
// (which carries a trailing slash).
func isCollectionImageKeyIn(dir, key string) bool {
	name, ok := strings.CutPrefix(key, dir)
	return ok && name != "" && !strings.Contains(name, "/")
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

// collectionCollageComposer composes collection collages from title posters
// and stores them under one artwork prefix (catalog.CollageGenerator). Each
// collage is stored in the collection's collage directory with its key as the
// revision, so every node that builds the same collage writes the same
// objects.
type collectionCollageComposer struct {
	prefix     string
	store      blobstore.Store
	posters    itemPosterSigner
	httpClient *http.Client
}

// NewPersonalCollectionCollageGenerator composes personal collection
// collages and stores them beside their uploaded posters. It returns nil
// when artwork storage or poster signing is not configured, which leaves
// personal collections without collages. A nil httpClient uses
// newCollectionImageClient.
func NewPersonalCollectionCollageGenerator(store blobstore.Store, posters itemPosterSigner, httpClient *http.Client) catalog.CollageGenerator {
	if store == nil || posters == nil {
		return nil
	}
	if httpClient == nil {
		httpClient = newCollectionImageClient()
	}
	return collectionCollageComposer{prefix: userCollectionImagePrefix, store: store, posters: posters, httpClient: httpClient}
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
// title's poster as the server resolved it, usually from its own artwork
// storage, which may be on the local network; no request supplies it. The
// fetch is therefore trusted with the local network, and netguard still
// refuses blocked addresses.
func (c collectionCollageComposer) fetchImage(ctx context.Context, imageURL string) ([]byte, error) {
	if c.httpClient == nil {
		return nil, errors.New("collage downloads have no HTTP client")
	}
	req, err := http.NewRequestWithContext(netguard.WithPrivateAccess(ctx), http.MethodGet, imageURL, nil)
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
