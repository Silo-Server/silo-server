package apiv2

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/imagecache"
	"github.com/Silo-Server/silo-server/internal/logredact"
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
	// itemPosterEnqueueTimeout bounds the queue write a poster request makes
	// before it fetches.
	itemPosterEnqueueTimeout = 3 * time.Second
	// itemPosterEnqueueRetry is how soon a failed queue write is tried again.
	itemPosterEnqueueRetry = 30 * time.Second
)

// itemPosterHandler serves GET /api/v2/item-posters/{content_id}.
type itemPosterHandler struct {
	posters  *artworkurl.ItemPosters
	source   ItemPosterSource
	fetcher  RemotePosterFetcher
	store    blobstore.Store
	enqueued posterEnqueueMemo
}

// posterEnqueueMemo remembers which items' cache jobs this server queued
// recently. It checks and records under one lock, so concurrent requests for
// one item queue once, and it starts no goroutine: expired entries are
// dropped, at most once a minute, once it grows past posterEnqueueMemoSweepAt.
type posterEnqueueMemo struct {
	mu        sync.Mutex
	queued    map[string]time.Time
	nextSweep time.Time
}

const posterEnqueueMemoSweepAt = 4096

// claim reports whether the caller should queue contentID's job now, and
// records that it did.
func (m *posterEnqueueMemo) claim(contentID string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if until, ok := m.queued[contentID]; ok && now.Before(until) {
		return false
	}
	if m.queued == nil {
		m.queued = make(map[string]time.Time)
	}
	// At most one sweep a minute, so a burst of distinct items past the
	// threshold does not rescan the map on every request.
	if len(m.queued) >= posterEnqueueMemoSweepAt && !now.Before(m.nextSweep) {
		for id, until := range m.queued {
			if !now.Before(until) {
				delete(m.queued, id)
			}
		}
		m.nextSweep = now.Add(time.Minute)
	}
	m.queued[contentID] = now.Add(itemPosterEnqueueMemo)
	return true
}

// retryAt shortens a claim whose queue write failed, so the item is tried
// again at when.
func (m *posterEnqueueMemo) retryAt(contentID string, when time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.queued[contentID]; ok {
		m.queued[contentID] = when
	}
}

// posterFetchLogError is a fetch failure as it is logged: without the URL a
// transport error quotes, which for a plugin's or a store's link can carry a
// signature or a key in its query.
func posterFetchLogError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	return logredact.SanitizeText(err.Error())
}

func registerItemPosters(reg *Registry) {
	h := &itemPosterHandler{
		posters: reg.deps.ItemPosters, source: reg.deps.ItemPosterSource, fetcher: reg.deps.RemotePosters,
		store: reg.deps.ArtworkStore,
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
			slog.InfoContext(r.Context(), "item poster fetch failed", "component", "apiv2", "content_id", contentID, "error", posterFetchLogError(err))
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
	http.Redirect(w, r, target, http.StatusFound)
}

// queueCache makes sure the item's poster has a cache job, at most once per
// item per itemPosterEnqueueMemo on this server.
func (h *itemPosterHandler) queueCache(ctx context.Context, contentID string) {
	if !h.enqueued.claim(contentID, time.Now()) {
		return
	}
	// Detached from the request so a client that leaves does not cancel the
	// queue write, and bounded so a stalled database cannot hold the poster.
	queueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), itemPosterEnqueueTimeout)
	defer cancel()
	if err := h.source.EnqueueItemPosterCache(queueCtx, contentID); err != nil {
		slog.WarnContext(ctx, "queueing an item poster cache job failed", "component", "apiv2", "content_id", contentID, "error", err)
		// Nothing was queued, so do not hold the item for the whole memo:
		// try again soon, but not on every request while the database is slow.
		h.enqueued.retryAt(contentID, time.Now().Add(itemPosterEnqueueRetry))
	}
}

func isHTTPURL(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}
