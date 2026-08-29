package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
	"github.com/Silo-Server/silo-server/internal/artworksource"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/artworkvariant"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

const (
	ArtworkCapabilityParam = "capability"
	ArtworkVariantParam    = "variant"

	artworkContentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; sandbox"
	artworkFallbackMediaType     = "application/octet-stream"
	webpContentType              = "image/webp"

	// Open decision 16: process-local, disposable, 64 MiB/256-entry LRU.
	emergencyArtworkCacheBytes   = 64 << 20
	emergencyArtworkCacheEntries = 256
	// Open decision 18: two seconds on the request path, then placeholder.
	artworkReconstructionWait      = 2 * time.Second
	artworkFallbackCacheTTL        = 60 * time.Second
	emergencyArtworkCacheTTL       = 5 * time.Minute
	repairSignalSuppression        = 30 * time.Second
	repairSignalQueueSize          = 64
	repairSignalWorkers            = 2
	repairSignalSuppressionEntries = 4096
	verifiedArtworkCacheBytes      = 64 << 20
	maxVerifiedStoredObjectBytes   = 32 << 20
	// The verified-digest validator is metadata only (key + ETag + size +
	// modTime), and the filesystem backend derives its ETag from that same
	// metadata, so an in-place byte replacement that preserves size and mtime is
	// invisible to it. Entries therefore expire and force a periodic re-hash
	// against the revision manifest instead of being trusted forever.
	verifiedArtworkCacheTTL = time.Hour
	// A backend that hands delivery a one-shot stream cannot seek, so a Range
	// request can only be answered by buffering the object. Stored artwork
	// variants are bounded well below this ceiling; anything larger is streamed
	// as a full 200 rather than held in memory.
	maxRangeBufferedObjectBytes    = maxVerifiedStoredObjectBytes
	artworkImagePoster             = artworkkey.ImageTypePoster
	artworkImageBackdrop           = artworkkey.ImageTypeBackdrop
	artworkImageLogo               = artworkkey.ImageTypeLogo
	artworkImageStill              = artworkkey.ImageTypeStill
	artworkImageProfile            = artworkkey.ImageTypeProfile
	artworkImageLibraryPoster      = artworkkey.ImageTypeLibraryPoster
	artworkImageCollectionPoster   = artworkkey.ImageTypeCollectionPoster
	artworkImageCollectionBackdrop = artworkkey.ImageTypeCollectionBackdrop
	artworkImageAvatar             = artworkkey.ImageTypeAvatar
)

type ArtworkObjectStore interface {
	Open(ctx context.Context, key string) (*artworkstore.Object, error)
}

type artworkStoreHealth interface {
	Health() (artworkstore.HealthState, time.Time)
}

type artworkTargetCoordinator interface {
	LoadTarget(ctx context.Context, target artworkurl.Target) (metadata.ArtworkTargetState, error)
	SignalMissing(ctx context.Context, state metadata.ArtworkTargetState) error
	ReadSidecar(ctx context.Context, state metadata.ArtworkTargetState) (metadata.ConfinedLocalArtwork, error)
}

type artworkSourceResolver interface {
	ResolveImageURL(ctx context.Context, path string, variant string) string
}

type ArtworkHandler struct {
	store             ArtworkObjectStore
	chapterStore      ArtworkObjectStore
	signer            *artworkurl.Signer
	targets           artworkTargetCoordinator
	sources           artworkSourceResolver
	emergency         *emergencyArtworkCache
	flight            singleflight.Group
	healthError       func(error)
	sourceRate        *rate.Limiter
	sourceBytes       *rate.Limiter
	sourceSlots       chan struct{}
	verified          *verifiedArtworkCache
	variants          *artworkvariant.Selector
	chapterVariants   *artworkvariant.Selector
	repairSignals     chan metadata.ArtworkTargetState
	repairSignalOnce  sync.Once
	repairSignalMu    sync.Mutex
	repairSignalUntil map[string]time.Time
}

// SetChapterThumbnailStore wires the public S3 bucket that owns chapter
// thumbnails. It may be the canonical store itself when canonical artwork also
// lives in that bucket.
func (h *ArtworkHandler) SetChapterThumbnailStore(store ArtworkObjectStore) {
	if h != nil {
		h.chapterStore = store
		h.chapterVariants = artworkvariant.New(store)
	}
}

func NewArtworkHandler(store ArtworkObjectStore, signer *artworkurl.Signer) *ArtworkHandler {
	if store == nil || signer == nil {
		return nil
	}
	return &ArtworkHandler{
		store: store, signer: signer,
		emergency:         newEmergencyArtworkCache(emergencyArtworkCacheBytes, emergencyArtworkCacheEntries),
		verified:          newVerifiedArtworkCache(verifiedArtworkCacheBytes),
		variants:          artworkvariant.New(store),
		repairSignals:     make(chan metadata.ArtworkTargetState, repairSignalQueueSize),
		repairSignalUntil: make(map[string]time.Time),
		// Request-path provider recovery is intentionally smaller than the
		// background worker pool: 16 starts/s, 8 concurrent, 32 MiB/s.
		sourceRate: rate.NewLimiter(16, 16), sourceBytes: rate.NewLimiter(32<<20, 64<<10), sourceSlots: make(chan struct{}, 8),
	}
}

func (h *ArtworkHandler) SetResilientDependencies(targets artworkTargetCoordinator, sources artworkSourceResolver, healthError func(error)) {
	if h != nil {
		h.targets, h.sources, h.healthError = targets, sources, healthError
		h.repairSignalOnce.Do(func() {
			for range repairSignalWorkers {
				go h.runRepairSignals()
			}
		})
	}
}

func (h *ArtworkHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, chi.URLParam(r, ArtworkCapabilityParam), chi.URLParam(r, ArtworkVariantParam))
}

func (h *ArtworkHandler) ServeArtworkURL(w http.ResponseWriter, r *http.Request, artworkURL string) bool {
	parsed, err := url.Parse(artworkURL)
	if err != nil || !strings.HasPrefix(parsed.Path, artworkurl.RoutePrefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, artworkurl.RoutePrefix), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	h.serve(w, r, parts[0], parts[1])
	return true
}

func (h *ArtworkHandler) serve(w http.ResponseWriter, r *http.Request, encodedCapability, variant string) {
	started := time.Now()
	if h == nil || h.store == nil || h.signer == nil || h.targets == nil {
		artworkNotFound(w)
		return
	}
	now := time.Now()
	target, expiresAt, err := h.signer.VerifyTarget(encodedCapability, variant, now)
	switch {
	case errors.Is(err, artworkurl.ErrExpired):
		artworkmetrics.Delivery("store", "expired_signature")
		writeError(w, http.StatusUnauthorized, "artwork_url_expired", "Artwork URL expired")
		return
	case err != nil:
		artworkmetrics.Delivery("store", "invalid_signature")
		artworkNotFound(w)
		return
	}
	state, err := h.targets.LoadTarget(r.Context(), target)
	if err != nil {
		artworkmetrics.Delivery("store", "miss")
		artworkNotFound(w)
		return
	}
	store, variants, usingChapterStore := h.readStoreForTarget(target)
	storedKey := state.StoredVariant(variant)
	if variants != nil && storedKey != "" {
		if selected, selectErr := variants.Select(r.Context(), state.SelectedPath, state.ImageType, variant); selectErr == nil {
			storedKey = selected
		}
	}
	backendReadable := true
	authoritativeMiss := false
	if health, ok := store.(artworkStoreHealth); ok {
		storeState, _ := health.Health()
		backendReadable = storeState != artworkstore.HealthUnavailable && storeState != artworkstore.HealthWrongMount
	}
	if key := storedKey; key != "" && store != nil && backendReadable {
		object, openErr := h.openVerifiedFrom(r.Context(), store, key)
		if openErr == nil {
			defer func() { _ = object.Close() }()
			w.Header().Set("X-Silo-Artwork", "stored")
			artworkmetrics.ObserveDeliveryLatency("store", started)
			h.writeObject(w, r, object, expiresAt, now)
			return
		}
		if errors.Is(openErr, artworkstore.ErrNotFound) || errors.Is(openErr, artworkstore.ErrContentMismatch) || errors.Is(openErr, artworkstore.ErrNotRegularFile) {
			authoritativeMiss = true
			if h.healthError != nil && !usingChapterStore {
				h.healthError(artworkstore.ErrRevisionMissing)
			}
		}
	}

	fallback, err := h.resolveFallback(r.Context(), state, variant)
	if err == nil && len(fallback.data) > 0 {
		if authoritativeMiss && !fallback.cacheHit {
			h.queueMissingSignal(state)
		}
		w.Header().Set("X-Silo-Artwork", "source_fallback")
		artworkmetrics.ObserveDeliveryLatency("source_fallback", started)
		h.writeFallback(w, r, fallback, expiresAt, now)
		return
	}
	if authoritativeMiss {
		h.queueMissingSignal(state)
	}
	artworkmetrics.ObserveDeliveryLatency("placeholder", started)
	h.writePlaceholder(w, r, state.ImageType)
}

func (h *ArtworkHandler) readStoreForTarget(target artworkurl.Target) (ArtworkObjectStore, *artworkvariant.Selector, bool) {
	if target.Surface == artworkurl.SurfaceChapterThumbnails && h.chapterStore != nil {
		return h.chapterStore, h.chapterVariants, true
	}
	return h.store, h.variants, false
}

func (h *ArtworkHandler) queueMissingSignal(state metadata.ArtworkTargetState) {
	key := state.Target.CacheKey() + "\x00" + state.SelectedPath
	now := time.Now()
	h.repairSignalMu.Lock()
	if until := h.repairSignalUntil[key]; until.After(now) {
		h.repairSignalMu.Unlock()
		return
	}
	if len(h.repairSignalUntil) >= repairSignalSuppressionEntries {
		oldestKey := ""
		var oldest time.Time
		for candidate, until := range h.repairSignalUntil {
			if !until.After(now) {
				delete(h.repairSignalUntil, candidate)
				continue
			}
			if oldestKey == "" || until.Before(oldest) {
				oldestKey, oldest = candidate, until
			}
		}
		if len(h.repairSignalUntil) >= repairSignalSuppressionEntries && oldestKey != "" {
			delete(h.repairSignalUntil, oldestKey)
		}
	}
	h.repairSignalUntil[key] = now.Add(repairSignalSuppression)
	h.repairSignalMu.Unlock()
	select {
	case h.repairSignals <- state:
	default:
		h.repairSignalMu.Lock()
		delete(h.repairSignalUntil, key)
		h.repairSignalMu.Unlock()
	}
}

func (h *ArtworkHandler) runRepairSignals() {
	for state := range h.repairSignals {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := h.targets.SignalMissing(ctx, state)
		cancel()
		if err != nil {
			key := state.Target.CacheKey() + "\x00" + state.SelectedPath
			h.repairSignalMu.Lock()
			delete(h.repairSignalUntil, key)
			h.repairSignalMu.Unlock()
			slog.Warn("failed to signal missing artwork", "surface", state.Target.Surface, "error", err)
		}
	}
}

func (h *ArtworkHandler) openVerified(ctx context.Context, key string) (*artworkstore.Object, error) {
	return h.openVerifiedFrom(ctx, h.store, key)
}

func (h *ArtworkHandler) openVerifiedFrom(ctx context.Context, store ArtworkObjectStore, key string) (*artworkstore.Object, error) {
	object, err := store.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	info, portable := artworkkey.ParsePortableKey(key)
	if !portable || info.IsManifest {
		return object, nil
	}
	validator := key + "\x00" + object.Info.ETag + "\x00" + strconv.FormatInt(object.Info.SizeBytes, 10) + "\x00" + strconv.FormatInt(object.Info.ModTime.UnixNano(), 10)
	if etag, ok := h.verified.get(validator); ok {
		object.Info.ETag = etag
		return object, nil
	}
	manifestObject, err := store.Open(ctx, info.Directory+"/"+artworkkey.ManifestName)
	if err != nil {
		_ = object.Close()
		return nil, err
	}
	manifestBytes, readErr := io.ReadAll(io.LimitReader(manifestObject.Body, 1<<20))
	_ = manifestObject.Close()
	if readErr != nil {
		_ = object.Close()
		return nil, readErr
	}
	manifest, err := artworkkey.ParseManifest(manifestBytes)
	if err != nil || manifest.Revision != info.Revision || manifest.ImageType != info.ImageType {
		_ = object.Close()
		return nil, artworkstore.ErrContentMismatch
	}
	var expectedDigest string
	var expectedSize int64 = -1
	wantedFilename := strings.TrimPrefix(key, info.Directory+"/")
	for _, candidate := range manifest.Variants {
		if candidate.Name == info.Variant && candidate.Filename == wantedFilename {
			expectedDigest, expectedSize = candidate.Digest, candidate.SizeBytes
			break
		}
	}
	if expectedDigest == "" || expectedSize < 0 || object.Info.SizeBytes != expectedSize || expectedSize > maxVerifiedStoredObjectBytes {
		_ = object.Close()
		return nil, artworkstore.ErrContentMismatch
	}
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(object.Body, maxVerifiedStoredObjectBytes+1))
	if closeErr := object.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if written != expectedSize || hex.EncodeToString(digest.Sum(nil)) != expectedDigest {
		return nil, artworkstore.ErrContentMismatch
	}
	object, err = store.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	reopenedValidator := key + "\x00" + object.Info.ETag + "\x00" + strconv.FormatInt(object.Info.SizeBytes, 10) + "\x00" + strconv.FormatInt(object.Info.ModTime.UnixNano(), 10)
	if reopenedValidator != validator {
		_ = object.Close()
		return nil, artworkstore.ErrContentMismatch
	}
	object.Info.ETag = `"` + expectedDigest + `"`
	h.verified.put(validator, object.Info.ETag, expectedSize)
	return object, nil
}

type fallbackArtwork struct {
	data      []byte
	mediaType string
	etag      string
	cacheHit  bool
}

func (h *ArtworkHandler) resolveFallback(ctx context.Context, state metadata.ArtworkTargetState, variant string) (fallbackArtwork, error) {
	if !state.Recoverable || state.SourcePath == "" {
		return fallbackArtwork{}, errors.New("artwork source is not recoverable")
	}
	cacheKey := emergencyCacheKey(state.Target.CacheKey(), state.SourcePath, variant, "")
	value, err, shared := h.flight.Do(cacheKey, func() (any, error) {
		requestCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), artworkReconstructionWait)
		defer cancel()
		var data []byte
		var mediaType string
		if strings.HasPrefix(strings.ToLower(state.SourcePath), "file://") {
			local, err := h.targets.ReadSidecar(requestCtx, state)
			if err != nil {
				return fallbackArtwork{}, err
			}
			data, mediaType = local.Data, local.MediaType
			if _, err := imageutil.ValidateImage(data); err != nil {
				return fallbackArtwork{}, err
			}
			cacheKey = emergencyCacheKey(state.Target.CacheKey(), state.SourcePath, variant,
				local.Path+"\x00"+strconv.FormatInt(local.ModTime.UnixNano(), 10)+"\x00"+strconv.FormatInt(local.Size, 10))
			if cached, ok := h.emergency.get(cacheKey); ok {
				artworkmetrics.Delivery("source_fallback", "emergency_cache_hit")
				return cached, nil
			}
		} else {
			// A direct http(s) source resolves to itself, so the key built above
			// is already the final cache key. Serving those bytes costs no
			// upstream request, and charging the recovery limiter for them would
			// let one page of distinct cached targets exhaust it and push later
			// requests onto placeholders. Plugin-resolved paths still have to
			// resolve first, so they keep the post-resolution check below.
			directSource := strings.HasPrefix(state.SourcePath, "http://") || strings.HasPrefix(state.SourcePath, "https://")
			if directSource {
				if cached, ok := h.emergency.get(cacheKey); ok {
					artworkmetrics.Delivery("source_fallback", "emergency_cache_hit")
					return cached, nil
				}
			}
			if err := h.sourceRate.Wait(requestCtx); err != nil {
				artworkmetrics.Repair("throttled")
				return fallbackArtwork{}, err
			}
			select {
			case h.sourceSlots <- struct{}{}:
				defer func() { <-h.sourceSlots }()
			case <-requestCtx.Done():
				artworkmetrics.Repair("throttled")
				return fallbackArtwork{}, requestCtx.Err()
			}
			resolved := state.SourcePath
			if !directSource {
				if h.sources == nil {
					return fallbackArtwork{}, errors.New("artwork source resolver is unavailable")
				}
				resolved = h.sources.ResolveImageURL(requestCtx, state.SourcePath, "original")
			}
			if resolved == "" {
				return fallbackArtwork{}, errors.New("artwork source resolved to an empty URL")
			}
			cacheKey = emergencyCacheKey(state.Target.CacheKey(), resolved, variant, "")
			if cached, ok := h.emergency.get(cacheKey); ok {
				artworkmetrics.Delivery("source_fallback", "emergency_cache_hit")
				return cached, nil
			}
			verified, err := artworksource.FetchVerifiedLimited(requestCtx, resolved, h.sourceBytes)
			if err != nil {
				return fallbackArtwork{}, err
			}
			data, mediaType = verified.Data, verified.MediaType
		}
		data, mediaType = fallbackVariant(data, mediaType, state.ImageType, variant)
		digest := sha256.Sum256(data)
		fallback := fallbackArtwork{data: data, mediaType: mediaType, etag: `"source-` + hex.EncodeToString(digest[:]) + `"`}
		h.emergency.put(cacheKey, fallback)
		return fallback, nil
	})
	if shared {
		artworkmetrics.Delivery("source_fallback", "singleflight_join")
	}
	if err != nil {
		return fallbackArtwork{}, err
	}
	fallback, ok := value.(fallbackArtwork)
	if !ok {
		return fallbackArtwork{}, errors.New("artwork fallback has an unexpected type")
	}
	return fallback, nil
}

func fallbackVariant(data []byte, mediaType, imageType, variant string) ([]byte, string) {
	if variant == artworkkey.OriginalVariant {
		return data, mediaType
	}
	width, ok := imagesize.VariantWidthPx(variant)
	if !ok {
		return data, mediaType
	}
	generate := imageutil.GenerateVariants
	if imageType == artworkkey.ImageTypeAvatar {
		generate = imageutil.GenerateSquareVariants
	}
	generated, err := generate(data, []int{width})
	if err != nil {
		// Request-time recovery must remain resilient to images the background
		// pipeline cannot resize. The already validated/capped original is safer
		// than replacing usable artwork with a placeholder.
		return data, mediaType
	}
	for _, candidate := range generated.Variants {
		if candidate.Key == variant {
			return candidate.Data, webpContentType
		}
	}
	return data, mediaType
}

func (h *ArtworkHandler) writeFallback(w http.ResponseWriter, r *http.Request, fallback fallbackArtwork, expiresAt, now time.Time) {
	header := w.Header()
	header.Set("Content-Type", fallback.mediaType)
	header.Set("Content-Length", strconv.Itoa(len(fallback.data)))
	header.Set("ETag", fallback.etag)
	header.Set("Cache-Control", fallbackCacheControl(expiresAt, now))
	secureArtworkHeaders(header)
	if artworkETagMatches(r.Header.Get("If-None-Match"), fallback.etag) {
		artworkmetrics.Delivery("source_fallback", "conditional_hit")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	artworkmetrics.Delivery("source_fallback", "served")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(fallback.data)
	artworkmetrics.DeliveryBytes("source_fallback", int64(len(fallback.data)))
}

func (h *ArtworkHandler) writePlaceholder(w http.ResponseWriter, r *http.Request, imageType string) {
	data := artworkPlaceholder(imageType)
	header := w.Header()
	header.Set("Content-Type", "image/png")
	header.Set("Content-Length", strconv.Itoa(len(data)))
	header.Set("Cache-Control", "no-store")
	header.Set("X-Silo-Artwork", "placeholder")
	secureArtworkHeaders(header)
	artworkmetrics.Delivery("placeholder", "served")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

type artworkCountingResponseWriter struct {
	http.ResponseWriter
	bytes int64
}

func (w *artworkCountingResponseWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.bytes += int64(n)
	return n, err
}

func (h *ArtworkHandler) writeObject(w http.ResponseWriter, r *http.Request, object *artworkstore.Object, expiresAt, now time.Time) {
	info := object.Info
	mediaType := info.MediaType
	if mediaType == "" {
		mediaType = artworkFallbackMediaType
	}
	header := w.Header()
	header.Set("Content-Type", mediaType)
	secureArtworkHeaders(header)
	if info.ETag != "" {
		header.Set("ETag", info.ETag)
	}
	if header.Get("Cache-Control") == "" {
		header.Set("Cache-Control", artworkCacheControl(expiresAt, now))
	}
	if seeker, ok := object.ReadSeeker(); ok {
		serveSeekableArtwork(w, r, info, seeker)
		return
	}
	// Range support is part of the delivery contract on every backend, not just
	// the seekable ones. A remote body has to be buffered to answer one, so pay
	// that cost only when the client actually asked for a range and the object
	// is small enough to hold; everything else keeps streaming untouched.
	if r.Method != http.MethodHead && r.Header.Get("Range") != "" &&
		info.SizeBytes > 0 && info.SizeBytes <= maxRangeBufferedObjectBytes {
		buffered, err := io.ReadAll(io.LimitReader(object.Body, info.SizeBytes))
		if err != nil {
			// The backend stream broke mid-object. Writing the bytes that did
			// arrive matches what the streaming path below does on the same
			// failure rather than inventing a second truncation behavior.
			slog.DebugContext(r.Context(), "artwork range buffering interrupted", "component", "api", "error", err)
			artworkmetrics.Delivery("store", "served")
			w.WriteHeader(http.StatusOK)
			written, _ := w.Write(buffered)
			artworkmetrics.DeliveryBytes("store", int64(written))
			return
		}
		serveSeekableArtwork(w, r, info, bytes.NewReader(buffered))
		return
	}
	if artworkETagMatches(r.Header.Get("If-None-Match"), info.ETag) {
		artworkmetrics.Delivery("store", "conditional_hit")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	artworkmetrics.Delivery("store", "served")
	if info.SizeBytes > 0 {
		header.Set("Content-Length", strconv.FormatInt(info.SizeBytes, 10))
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	written, err := io.Copy(w, object.Body)
	artworkmetrics.DeliveryBytes("store", written)
	if err != nil {
		slog.DebugContext(r.Context(), "artwork response interrupted", "component", "api", "error", err)
	}
}

// serveSeekableArtwork answers with http.ServeContent, which owns conditional
// and range handling once delivery can seek the bytes.
func serveSeekableArtwork(w http.ResponseWriter, r *http.Request, info artworkstore.ObjectInfo, seeker io.ReadSeeker) {
	if artworkETagMatches(r.Header.Get("If-None-Match"), info.ETag) {
		artworkmetrics.Delivery("store", "conditional_hit")
	} else {
		artworkmetrics.Delivery("store", "served")
	}
	counting := &artworkCountingResponseWriter{ResponseWriter: w}
	http.ServeContent(counting, r, info.Key, info.ModTime, seeker)
	artworkmetrics.DeliveryBytes("store", counting.bytes)
}

func secureArtworkHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", artworkContentSecurityPolicy)
}

func artworkNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not_found", "Artwork not found")
}

func artworkCacheControl(expiresAt, now time.Time) string {
	maxAge := int64(expiresAt.Sub(now) / time.Second)
	if maxAge < 0 {
		maxAge = 0
	}
	return "private, max-age=" + strconv.FormatInt(maxAge, 10) + ", immutable"
}

func fallbackCacheControl(expiresAt, now time.Time) string {
	maxAge := int64(artworkFallbackCacheTTL / time.Second)
	if remaining := int64(expiresAt.Sub(now) / time.Second); remaining < maxAge {
		maxAge = remaining
	}
	if maxAge < 0 {
		maxAge = 0
	}
	return "private, max-age=" + strconv.FormatInt(maxAge, 10)
}

func artworkETagMatches(ifNoneMatch, etag string) bool {
	if etag == "" || ifNoneMatch == "" {
		return false
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

type emergencyArtworkEntry struct {
	value     fallbackArtwork
	usedAt    time.Time
	expiresAt time.Time
}

type emergencyArtworkCache struct {
	mu         sync.Mutex
	entries    map[string]emergencyArtworkEntry
	bytes      int64
	maxBytes   int64
	maxEntries int
}

type verifiedArtworkCache struct {
	mu       sync.Mutex
	entries  map[string]verifiedArtworkEntry
	bytes    int64
	maxBytes int64
}

type verifiedArtworkEntry struct {
	usedAt    time.Time
	expiresAt time.Time
	etag      string
	size      int64
}

func newVerifiedArtworkCache(maxBytes int64) *verifiedArtworkCache {
	return &verifiedArtworkCache{entries: make(map[string]verifiedArtworkEntry), maxBytes: maxBytes}
}

func (c *verifiedArtworkCache) get(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	now := time.Now()
	// An expired entry is dropped rather than refreshed: the validator alone
	// cannot prove the stored bytes are still the verified ones, so the caller
	// has to re-hash them against the manifest.
	if now.After(entry.expiresAt) {
		delete(c.entries, key)
		c.bytes -= entry.size
		return "", false
	}
	entry.usedAt = now
	c.entries[key] = entry
	return entry.etag, true
}

func (c *verifiedArtworkCache) put(key, etag string, size int64) {
	if c == nil || c.maxBytes <= 0 || size < 0 || size > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		c.bytes -= old.size
	}
	now := time.Now()
	c.entries[key] = verifiedArtworkEntry{usedAt: now, expiresAt: now.Add(verifiedArtworkCacheTTL), etag: etag, size: size}
	c.bytes += size
	for c.bytes > c.maxBytes {
		oldestKey := ""
		var oldest time.Time
		for candidate, entry := range c.entries {
			if oldestKey == "" || entry.usedAt.Before(oldest) {
				oldestKey, oldest = candidate, entry.usedAt
			}
		}
		old := c.entries[oldestKey]
		delete(c.entries, oldestKey)
		c.bytes -= old.size
	}
}

func newEmergencyArtworkCache(maxBytes int64, maxEntries int) *emergencyArtworkCache {
	return &emergencyArtworkCache{entries: make(map[string]emergencyArtworkEntry), maxBytes: maxBytes, maxEntries: maxEntries}
}

func emergencyCacheKey(target, source, variant, fileIdentity string) string {
	digest := sha256.Sum256([]byte(target + "\x00" + source + "\x00" + variant + "\x00" + fileIdentity))
	return hex.EncodeToString(digest[:])
}

func (c *emergencyArtworkCache) get(key string) (fallbackArtwork, bool) {
	if c == nil {
		return fallbackArtwork{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return fallbackArtwork{}, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		c.bytes -= int64(len(entry.value.data))
		return fallbackArtwork{}, false
	}
	entry.usedAt = time.Now()
	c.entries[key] = entry
	value := entry.value
	value.cacheHit = true
	return value, true
}

func (c *emergencyArtworkCache) put(key string, value fallbackArtwork) {
	if c == nil || int64(len(value.data)) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		c.bytes -= int64(len(old.value.data))
	}
	now := time.Now()
	c.entries[key] = emergencyArtworkEntry{value: value, usedAt: now, expiresAt: now.Add(emergencyArtworkCacheTTL)}
	c.bytes += int64(len(value.data))
	for c.bytes > c.maxBytes || len(c.entries) > c.maxEntries {
		oldestKey := ""
		var oldest time.Time
		for candidate, entry := range c.entries {
			if oldestKey == "" || entry.usedAt.Before(oldest) {
				oldestKey, oldest = candidate, entry.usedAt
			}
		}
		entry := c.entries[oldestKey]
		delete(c.entries, oldestKey)
		c.bytes -= int64(len(entry.value.data))
	}
}
