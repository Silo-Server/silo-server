package apiv2

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/imagecache"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
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
	// itemPosterResolveMemo is how long a plugin's answer for a poster path
	// is reused, so a link costs one plugin call per poster per window, not
	// one per request. It stays well under the lifetime of a signed URL a
	// plugin returns.
	itemPosterResolveMemo = time.Minute
	// itemPosterResolveTimeout bounds a plugin lookup that outlives its caller.
	itemPosterResolveTimeout = 10 * time.Second
	// itemPosterResolveRetry is how long a path whose plugin ran out of time
	// answers busy before the plugin is asked again.
	itemPosterResolveRetry = 10 * time.Second
	// itemPosterResolveMax bounds how many plugin answers are kept.
	itemPosterResolveMax = 4096
)

// errPosterResolveBusy reports that a plugin poster path has no answer yet:
// the plugin-answer memo is full, so the path is refused instead of calling
// its plugin on every request, or the plugin did not answer in time and the
// path is waiting out itemPosterResolveRetry.
var errPosterResolveBusy = errors.New("poster resolution is busy")

// itemPosterHandler serves GET /api/v2/item-posters/{content_id}.
type itemPosterHandler struct {
	posters  *artworkurl.ItemPosters
	source   ItemPosterSource
	fetcher  RemotePosterFetcher
	enqueued posterEnqueueMemo
	resolved posterResolveMemo
	// resolving shares one plugin call among requests for one poster path.
	resolving singleflight.Group
}

// posterResolveMemo keeps what plugins answered for poster paths, by path.
// Like posterEnqueueMemo it starts no goroutine and never forgets a live
// entry to make room: a full memo refuses new paths until entries expire.
type posterResolveMemo struct {
	mu   sync.Mutex
	urls map[string]posterResolution
	// pending counts plugin calls whose answers are not stored yet; each
	// holds a place so the calls in flight cannot overfill the memo.
	pending     int
	firstExpiry time.Time
}

type posterResolution struct {
	url string
	// busy marks a path whose plugin ran out of time: no answer, and no new
	// plugin call until the entry expires.
	busy  bool
	until time.Time
}

// get returns the remembered answer for path. busy reports that there is
// none and the plugin must not be asked now: the path is waiting out a
// timeout, or there is no room to keep a new answer.
func (m *posterResolveMemo) get(path string, now time.Time) (url string, ok, busy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lookup(path, now)
}

// reserve is get for a caller about to ask the plugin: on a miss with room it
// also holds a place for the answer, which the caller gives back with release.
func (m *posterResolveMemo) reserve(path string, now time.Time) (url string, ok, busy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	url, ok, busy = m.lookup(path, now)
	if !ok && !busy {
		m.pending++
	}
	return url, ok, busy
}

func (m *posterResolveMemo) release() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending--
}

// lookup is get's body. The caller holds m.mu.
func (m *posterResolveMemo) lookup(path string, now time.Time) (url string, ok, busy bool) {
	if entry, found := m.urls[path]; found && now.Before(entry.until) {
		if entry.busy {
			return "", false, true
		}
		return entry.url, true, false
	}
	if len(m.urls)+m.pending < itemPosterResolveMax {
		return "", false, false
	}
	if now.Before(m.firstExpiry) {
		return "", false, true
	}
	m.firstExpiry = time.Time{}
	for key, entry := range m.urls {
		if !now.Before(entry.until) {
			delete(m.urls, key)
			continue
		}
		if m.firstExpiry.IsZero() || entry.until.Before(m.firstExpiry) {
			m.firstExpiry = entry.until
		}
	}
	return "", false, len(m.urls)+m.pending >= itemPosterResolveMax
}

func (m *posterResolveMemo) put(path, url string, now time.Time) {
	m.store(path, posterResolution{url: url, until: now.Add(itemPosterResolveMemo)})
}

// putBusy records that path's plugin ran out of time.
func (m *posterResolveMemo) putBusy(path string, now time.Time) {
	m.store(path, posterResolution{busy: true, until: now.Add(itemPosterResolveRetry)})
}

func (m *posterResolveMemo) store(path string, entry posterResolution) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.urls == nil {
		m.urls = make(map[string]posterResolution)
	}
	m.urls[path] = entry
	if len(m.urls) == 1 || m.firstExpiry.IsZero() || entry.until.Before(m.firstExpiry) {
		m.firstExpiry = entry.until
	}
}

// posterEnqueueMemo remembers which items' cache jobs this server queued
// recently. It checks and records under one lock, so concurrent requests for
// one item queue once, and it starts no goroutine: expired entries are
// dropped, at most once a minute, once it holds posterEnqueueMemoMax items,
// which is also the most it holds.
type posterEnqueueMemo struct {
	mu        sync.Mutex
	queued    map[string]time.Time
	nextSweep time.Time
}

const posterEnqueueMemoMax = 4096

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
	if len(m.queued) >= posterEnqueueMemoMax && !now.Before(m.nextSweep) {
		for id, until := range m.queued {
			if !now.Before(until) {
				delete(m.queued, id)
			}
		}
		m.nextSweep = now.Add(time.Minute)
	}
	// A full memo queues nothing new: the poster is still served, and a
	// later request queues its job once entries expire. Forgetting a claim
	// instead would let cycling links repeat the queue write on each request.
	if _, ok := m.queued[contentID]; !ok && len(m.queued) >= posterEnqueueMemoMax {
		return false
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
	}
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/item-posters/{content_id}", "getItemPoster", "artwork",
		"Read a catalog item's current poster from a signed link that admin reads return in poster_url. Cached art redirects to its stored object; art not cached yet is fetched from its provider through the server and queued for caching."),
		Class: ClassPublic, ServiceBacked: true, RateLimitBucket: ratelimit.ItemPosterBucket}
	op.Parameters = []*huma.Param{
		{Name: "content_id", In: paramInPath, Required: true, Description: "Content ID of the movie or series whose poster the link names.", Schema: &huma.Schema{Type: huma.TypeString}},
		{Name: signedLinkExpiryParam, In: artworkParamQuery, Required: true, Schema: &huma.Schema{Type: artworkIntegerType, Format: playbackIntegerFormat}},
		{Name: signedLinkSigParam, In: artworkParamQuery, Required: true, Schema: &huma.Schema{Type: huma.TypeString}},
	}
	op.Responses = map[string]*huma.Response{
		"200": {Description: "Poster bytes of art not cached yet", Content: map[string]*huma.MediaType{artworkImageMediaType: {Schema: &huma.Schema{Type: huma.TypeString, Format: artworkBinaryFormat}}}},
		"302": {Description: "Redirect to the cached poster's signed URL", Headers: map[string]*huma.Header{"Location": {Schema: &huma.Schema{Type: huma.TypeString, Format: "uri-reference"}}}},
		"404": {Description: "Link invalid or expired, item gone, no poster, or the provider refused or failed"},
		"429": {
			Description: "Poster request rate limit exceeded",
			Headers:     map[string]*huma.Header{"Retry-After": {Description: "Seconds to wait before retrying.", Schema: &huma.Schema{Type: huma.TypeInteger}}},
			Content:     map[string]*huma.MediaType{problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")}},
		},
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
	target, proxy, ok, err := h.resolve(r.Context(), posterPath)
	if err != nil {
		writeProblem(w, r, NewProblem(TypeDependencyUnavailable, "Poster fetches are busy.").WithRetryAfter(5))
		return
	}
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
	// A signed poster link authorizes downloads from public addresses only.
	image, err := h.fetcher.Fetch(r.Context(), contentID, target)
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
// local source (file://, nfo+…) that only the image-cache job reads. err is
// errPosterResolveBusy when a plugin would have to be asked and the memo of
// plugin answers is full.
func (h *itemPosterHandler) resolve(ctx context.Context, posterPath string) (target string, proxy, ok bool, err error) {
	switch {
	case posterPath == "" || posterPath == "-":
		return "", false, false, nil
	case artworkurl.IsStoredArtworkPath(posterPath):
		return h.posters.Presign(ctx, artworkurl.StoredPosterVariant(posterPath)), false, true, nil
	case !metadata.IsRemoteImageSourcePath(posterPath):
		return "", false, false, nil
	case isHTTPURL(posterPath):
		return posterPath, true, true, nil
	}
	// A plugin path: its plugin answers with a stored artwork route or a URL.
	// The answer is reused for itemPosterResolveMemo, failures included, so a
	// link cannot make the plugin work on every request.
	resolved, known, _ := h.resolved.get(posterPath, time.Now())
	if !known {
		// Whether there is room is decided inside the shared call, so a
		// request for a path already being resolved joins that call even
		// when its reservation is the memo's last place.
		var busy bool
		if resolved, busy = h.askPlugin(ctx, posterPath); busy {
			return "", false, false, errPosterResolveBusy
		}
	}
	switch {
	// One leading slash is a path on this server; "//host/..." is another
	// host, which the tool this route serves may not reach.
	case strings.HasPrefix(resolved, "/") && !strings.HasPrefix(resolved, "//"):
		return resolved, false, true, nil
	case isHTTPURL(resolved):
		return resolved, true, true, nil
	default:
		return "", false, false, nil
	}
}

// askPlugin resolves a plugin poster path that has no remembered answer.
// Requests for one path share one call, and a call starts only while the memo
// has room for its answer, so neither the plugin calls in flight nor the
// answers kept can pass itemPosterResolveMax. The call runs apart from the
// request, so its answer is kept for the next request even when this client
// leaves.
func (h *itemPosterHandler) askPlugin(ctx context.Context, posterPath string) (resolved string, busy bool) {
	type answer struct {
		url  string
		busy bool
	}
	value, _, _ := h.resolving.Do(posterPath, func() (any, error) {
		// A request that missed the memo just before the previous call
		// stored its answer lands here after it.
		if url, known, busy := h.resolved.reserve(posterPath, time.Now()); known || busy {
			return answer{url: url, busy: busy}, nil
		}
		defer h.resolved.release()
		resolveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), itemPosterResolveTimeout)
		defer cancel()
		url := h.posters.Presign(resolveCtx, posterPath)
		// A plugin that ran out of time gave no answer. Keeping the empty
		// result for the whole memo would hide a poster the plugin can still
		// resolve, so the path answers busy for a short while instead.
		if resolveCtx.Err() != nil {
			h.resolved.putBusy(posterPath, time.Now())
			return answer{busy: true}, nil
		}
		h.resolved.put(posterPath, url, time.Now())
		return answer{url: url}, nil
	})
	got, _ := value.(answer)
	return got.url, got.busy
}

// redirect answers with the stored object's URL. The redirect itself is not
// cached: its target is a signed URL with its own lifetime and cache rules.
func (h *itemPosterHandler) redirect(w http.ResponseWriter, r *http.Request, target string) {
	if target == "" {
		writeProblem(w, r, unavailable("artwork storage"))
		return
	}
	// target is the signed URL or rooted path this server resolved for the
	// item's stored poster, so it needs none of http.Redirect's path handling.
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
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
