package apiv2

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/imagecache"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

// ItemPosterSource reads an item's current poster and queues its caching
// (*metadata.ImageCacheJobRepository).
type ItemPosterSource interface {
	ItemPosterPath(ctx context.Context, contentID string) (string, error)
	EnqueueItemPosterCache(ctx context.Context, contentID string) error
}

// RemotePosterFetcher downloads art that is not cached yet
// (*imagecache.RemoteFetcher).
type RemotePosterFetcher interface {
	Fetch(ctx context.Context, key, rawURL string) (imagecache.RemoteImage, error)
}

const (
	// itemPosterProxyCache lets a client keep proxied bytes briefly. The link
	// stays the same when the art is cached, and then redirects instead.
	itemPosterProxyCache = "private, max-age=300"
	// itemPosterEnqueueMemo is how long a server skips re-queueing the cache
	// job of an item it queued, so a link polled every few seconds costs one
	// queue write per item per window.
	itemPosterEnqueueMemo = 10 * time.Minute
	itemPosterMaxIDLength = 512
)

// itemPosterHandler serves GET /api/v2/item-posters/{content_id}.
type itemPosterHandler struct {
	posters  *artworkurl.ItemPosters
	source   ItemPosterSource
	fetcher  RemotePosterFetcher
	store    blobstore.Store
	enqueued *cache.TTLCache[bool]
}

func registerItemPosters(reg *Registry) {
	h := &itemPosterHandler{
		posters: reg.deps.ItemPosters, source: reg.deps.ItemPosterSource, fetcher: reg.deps.RemotePosters,
		store: reg.deps.ArtworkStore, enqueued: cache.NewTTLCache[bool](),
	}
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/item-posters/{content_id}", "getItemPoster", "artwork",
		"Read a catalog item's current poster from a signed link that admin reads return in poster_url. Cached art redirects to its stored object; art not cached yet is fetched from its provider through the server and queued for caching."),
		Class: ClassPublic, ServiceBacked: true}
	op.Parameters = []*huma.Param{
		{Name: "content_id", In: paramInPath, Required: true, Description: "Content ID of the movie or series whose poster the link names.", Schema: &huma.Schema{Type: huma.TypeString}},
		{Name: signedLinkExpiryParam, In: artworkParamQuery, Required: true, Schema: &huma.Schema{Type: artworkIntegerType, Format: playbackIntegerFormat}},
		{Name: signedLinkSigParam, In: artworkParamQuery, Required: true, Schema: &huma.Schema{Type: huma.TypeString}},
	}
	op.Responses = map[string]*huma.Response{
		"200": {Description: "Poster bytes of art not cached yet", Content: map[string]*huma.MediaType{artworkImageMediaType: {Schema: &huma.Schema{Type: huma.TypeString, Format: artworkBinaryFormat}}}},
		"302": {Description: "Redirect to the cached poster's signed URL", Headers: map[string]*huma.Header{"Location": {Schema: &huma.Schema{Type: huma.TypeString, Format: "uri-reference"}}}},
		"404": {Description: "Link invalid or expired, item gone, no poster, or the provider refused or failed"},
		"503": {Description: "Poster fetches are busy or not configured; retry after Retry-After"},
	}
	RegisterRaw(reg, RawOperation{Operation: op, Protocol: "artwork-image", Reason: "Poster bytes and redirects bypass JSON encoding."}, h)
}

func (h *itemPosterHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.posters == nil || h.source == nil {
		writeProblem(w, r, unavailable("poster"))
		return
	}
	notFound := func() { writeProblem(w, r, NewProblem(TypeNotFound, "Poster not found.")) }
	contentID := strings.TrimPrefix(r.URL.Path, Prefix+"/item-posters/")
	if contentID == "" || len(contentID) > itemPosterMaxIDLength || strings.Contains(contentID, "/") {
		notFound()
		return
	}
	exp, err := strconv.ParseInt(r.URL.Query().Get(signedLinkExpiryParam), 10, 64)
	if err != nil || h.posters.Verify(contentID, exp, r.URL.Query().Get(signedLinkSigParam), time.Now()) != nil {
		notFound()
		return
	}
	posterPath, err := h.source.ItemPosterPath(r.Context(), contentID)
	switch {
	case errors.Is(err, metadata.ErrItemNotFound):
		notFound()
		return
	case err != nil:
		writeProblem(w, r, unavailable("poster"))
		return
	}
	target, proxy, ok := h.resolve(r.Context(), posterPath)
	if !ok {
		notFound()
		return
	}
	if !proxy {
		h.redirect(w, r, target)
		return
	}
	h.queueCache(r.Context(), contentID)
	if h.fetcher == nil {
		writeProblem(w, r, unavailable("poster"))
		return
	}
	// The artwork store's own direct URLs may sit on the local network;
	// every other URL reaches public addresses only.
	ctx := artworkurl.FetchContext(r.Context(), h.store, target)
	image, err := h.fetcher.Fetch(ctx, contentID, target)
	switch {
	case errors.Is(err, imagecache.ErrRemoteBusy):
		writeProblem(w, r, NewProblem(TypeDependencyUnavailable, "Poster fetches are busy.").WithRetryAfter(5))
		return
	case err != nil:
		// A refused address, an upstream failure and a body that is not an
		// image all read the same, so the answer cannot map the network.
		if r.Context().Err() == nil {
			slog.InfoContext(r.Context(), "item poster fetch failed", "component", "apiv2", "content_id", contentID, "error", err)
		}
		notFound()
		return
	}
	header := w.Header()
	header.Set("Content-Type", image.ContentType)
	header.Set("Content-Length", strconv.Itoa(len(image.Data)))
	header.Set("Cache-Control", itemPosterProxyCache)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(image.Data)
}

// resolve maps an item's stored poster path to what the route answers with:
// a stored object's URL to redirect to, or a provider URL to fetch and proxy.
// ok is false when the path names nothing the route serves: no poster, or a
// local source (file://, nfo+…) that only the image-cache job reads.
func (h *itemPosterHandler) resolve(ctx context.Context, posterPath string) (target string, proxy, ok bool) {
	switch {
	case posterPath == "" || posterPath == "-":
		return "", false, false
	case artworkurl.IsStoredArtworkPath(posterPath):
		return h.posters.Presign(ctx, artworkurl.StoredPosterVariant(posterPath)), false, true
	case !metadata.IsRemoteImageSourcePath(posterPath):
		return "", false, false
	case isHTTPURL(posterPath):
		return posterPath, true, true
	}
	// A plugin path: its plugin answers with a stored artwork route or a URL.
	resolved := h.posters.Presign(ctx, posterPath)
	switch {
	case strings.HasPrefix(resolved, "/"):
		return resolved, false, true
	case isHTTPURL(resolved):
		return resolved, true, true
	default:
		return "", false, false
	}
}

// redirect answers with the stored object's URL. The redirect itself is not
// cached: its target is a signed URL with its own lifetime and cache rules.
func (h *itemPosterHandler) redirect(w http.ResponseWriter, r *http.Request, target string) {
	if target == "" {
		writeProblem(w, r, unavailable("artwork storage"))
		return
	}
	//nolint:gosec // G710: target is the signed URL this server resolved for the item's stored poster, not request input
	http.Redirect(w, r, target, http.StatusFound)
}

// queueCache makes sure the item's poster has a cache job, at most once per
// item per itemPosterEnqueueMemo on this server.
func (h *itemPosterHandler) queueCache(ctx context.Context, contentID string) {
	if _, ok := h.enqueued.Get(contentID); ok {
		return
	}
	h.enqueued.Set(contentID, true, itemPosterEnqueueMemo)
	if err := h.source.EnqueueItemPosterCache(context.WithoutCancel(ctx), contentID); err != nil {
		slog.WarnContext(ctx, "queueing an item poster cache job failed", "component", "apiv2", "content_id", contentID, "error", err)
	}
}

func isHTTPURL(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}
