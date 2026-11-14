// Package resolver implements the Stremio manifest stream resolver in Vio core.
//
// Scope: manifest fetch over HTTP, the candidate cache (positive entries with
// TTL, negative caching of empty answers, the fresh-serve floor, stale grace
// with background refresh, singleflight fetch dedup), candidate dedup with the
// maxVirtualCandidates cap, and connection validation. Deliberately NOT
// ported: Configure() (replaced by Config + New), runtimeServer/gRPC,
// TestConnection (monitor/prowlarr/altmount phases), and quality-profile
// selection (Resolve/SelectCandidates/GetVariants live with the quality
// package).
package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/virtuallibrary/quality"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/stream"
)

// ErrProviderUnavailable marks a provider-listing failure rather than a
// successful answer that merely lacks candidates: the provider request failed,
// timed out, or answered with a non-200 status. Callers that hold a
// session-bound candidate with persisted delivery evidence can distinguish this
// transient condition from a genuine "this release is gone" verdict and retry
// with backoff instead of rotating the release or returning a permanent error.
var ErrProviderUnavailable = errors.New("virtual playback provider unavailable")

// ErrTMDBExternalIDLookupFailed marks a TMDB external-id lookup that failed for
// an availability reason (HTTP 5xx, timeout, decode error) rather than because
// the title genuinely has no IMDb id. Callers must treat it as retryable and
// must never cache it as a negative: a title's IMDb id does not change because
// TMDB had a bad minute. It also wraps ErrProviderUnavailable so the serve layer
// answers the retryable provider_unavailable condition instead of a terminal
// "no playback id".
var ErrTMDBExternalIDLookupFailed = errors.New("TMDB external-id lookup failed")

const (
	virtualPathPrefix = "virtual://"

	// Log attribute keys shared by the wait and fetch traces.
	logKeyCacheKey = "cache_key"
	logKeyReason   = "reason"

	maxResponseBytes         = 4 << 20
	maxCandidateCacheEntries = 256
	maxCandidateCacheBytes   = 16 << 20
	// maxProviderCandidates is the parse-time bound, deliberately far above the
	// selectable cap. Deduplication and failure classification run at
	// ingestion, so a provider that returns one candidate per file of many
	// multi-file releases must be parsed in full before the per-release collapse
	// and the maxVirtualCandidates truncation. Without it, a flood of per-file
	// variants filled the 50-slot list before dedup could merge them.
	maxProviderCandidates = 500
	// maxVirtualCandidates is the selectable/version-list cap after dedup.
	maxVirtualCandidates     = 50
	maxManifestResponseBytes = 256 << 10

	// defaultCacheTTL is the positive-entry TTL when Config.CacheTTL is unset.
	// Bounds mirror the plugin (1 minute .. 10080 minutes / 7 days).
	defaultCacheTTL = 10 * time.Minute
	minCacheTTL     = 1 * time.Minute
	maxCacheTTL     = 10080 * time.Minute

	defaultHTTPTimeout = 45 * time.Second

	defaultTVMazeBaseURL = "https://api.tvmaze.com"
	defaultTMDBBaseURL   = "https://api.themoviedb.org/3"

	// negativeCacheTTL bounds how long an empty provider answer is reused.
	// Without it, a title the provider cannot serve re-paid a full 3-8s
	// round-trip on every resolve — four times inside one playback start in
	// production. Entries expire so newly added sources are noticed without
	// operator action.
	negativeCacheTTL = 2 * time.Minute
	// freshServeFloor caps how aggressively forced lookups re-fetch. One
	// playback start walks several candidate rounds; serving results younger
	// than this floor even to GetCandidatesFresh keeps an entire attempt at
	// one provider round-trip while bounding staleness well under the TTL.
	freshServeFloor = 30 * time.Second
	// candidateStaleGrace extends candidate-cache usefulness past its TTL:
	// an expired-but-non-empty entry is served immediately while one
	// background refresh repopulates it. Provider URLs are short-lived
	// tokens whose actual lifetime is unknown to the plugin, so the grace is
	// intentionally short — long enough to cover a typical playback start,
	// short enough that expired credentials are not served for long.
	candidateStaleGrace = 3 * time.Minute
	// backgroundRefreshTimeout bounds the stale-grace background repopulation
	// fetch so a hung provider cannot pile up goroutines.
	backgroundRefreshTimeout = 15 * time.Second
	// syncFetchTimeout bounds the singleflight blocking fetch used past stale
	// grace; the provider client timeout is the tighter bound in practice.
	syncFetchTimeout = 45 * time.Second

	// providerFailureBackoff bounds how long a failed provider listing is
	// remembered per cache key. Without it, a provider that times out at the
	// client's 45s bound re-pays the full timeout on every resolve: there is
	// no entry to negative-cache because the fetch never produced an answer.
	// Within the backoff window a resolve fails fast with the same
	// ErrProviderUnavailable a timeout would produce, preserving the outage
	// semantics callers branch on. A declared outage re-list
	// (GetCandidatesFreshUnbounded) deliberately bypasses it so recovery is
	// never blocked by the backoff the outage itself wrote.
	providerFailureBackoff = 30 * time.Second

	// tmdbExternalIDCacheTTL reuses a successful tmdb -> imdb translation for
	// this long. External IDs are stable, so a long TTL is safe.
	tmdbExternalIDCacheTTL = time.Hour
	// tmdbExternalIDNegativeTTL is the shorter reuse window for a translation
	// that failed or carried no IMDb id, so a transient TMDB outage or a newly
	// populated ID recovers quickly.
	tmdbExternalIDNegativeTTL = time.Minute

	// altmountCachedBadge is the display badge AltMount's Stremio addon puts
	// on imported, fresh releases; honored as a zero-config confirmed signal.
	altmountCachedBadge = "⚡ cached"
)

// StreamCandidate, BehaviorHints, QualityConfig, QualityProfile and
// CustomFormat are aliases of the stream and quality packages (see below).

// StreamCandidate is a single playable source offered by the provider.
// It aliases the stream package type so the resolver and stream parser share
// one definition.
type StreamCandidate = stream.StreamCandidate

// CustomFormat is a regex-scored quality custom format (quality package owns it).
type CustomFormat = quality.CustomFormat

// QualityProfile is a named quality selection profile (quality package owns it).
type QualityProfile = quality.QualityProfile

// QualityConfig carries the quality selection configuration. The resolver
// stores it for forward compatibility; profile-driven selection itself lives
// with the quality package.
type QualityConfig = quality.QualityConfig

// Config is the plain resolver configuration. It replaces the plugin SDK
// Configure() path: values are passed to New, never read from the SDK.
type Config struct {
	// ManifestURL is the provider manifest URL (must end in /manifest.json).
	ManifestURL string
	// AllowInsecure permits HTTP manifests, but only for private/local hosts
	// (see streamEndpointWithPolicy).
	AllowInsecure bool
	// CacheTTL is the positive candidate-cache TTL (clamped to 1m..7d,
	// default 10m).
	CacheTTL time.Duration
	// HTTPTimeout bounds provider round-trips (default 45s).
	HTTPTimeout time.Duration
	// TMDBAPIKey resolves tmdb: virtual IDs to IMDb provider IDs.
	TMDBAPIKey string
	// TVMazeBaseURL translates legacy tvdb: series IDs (default TVMaze API).
	TVMazeBaseURL string
	// TMDBBaseURL is the TMDB API base for external-ID lookups.
	TMDBBaseURL string
	// Quality is stored for forward compatibility with the quality port.
	Quality QualityConfig
}

// CandidateClassifier marks provider candidates with the authoritative
// completed/failed state of the configured source of truth. Implementations
// must be safe to call concurrently with their own refresh.
type CandidateClassifier interface {
	ClassifyCandidates(candidates []StreamCandidate)
}

// ReleaseGate reports whether tracked release metadata considers the
// requested movie or episode released. A nil gate disables the check.
type ReleaseGate interface {
	IsReleased(itemType string, imdbID string, season int, episode int) (bool, *time.Time)
}

// CandidateEnricher fills derived stream fields (resolution, codecs, sizes,
// languages) on an ingested candidate. Defaults to the stream package parser.
type CandidateEnricher func(*StreamCandidate)

// DefaultEnricher wires the stream package parser as the candidate enricher.
func DefaultEnricher(c *StreamCandidate) {
	stream.ParseStreamDetails(c)
}

// Resolver fetches Stremio stream candidates from a manifest provider with a
// bounded cache. Safe for concurrent use.
//
// requestIDReader extracts the edge request id from a fetch context for the
// provider-fetch log. It is a per-resolver field (not a package global) so
// concurrent resolvers in tests never share wiring; production installs the
// parent virtuallibrary reader via SetRequestIDReader before first use.
type Resolver struct {
	client *http.Client

	mu          sync.RWMutex
	config      Config
	generation  uint64
	enricher    CandidateEnricher
	classifier  CandidateClassifier
	releaseGate ReleaseGate
	logger      *slog.Logger

	requestIDReader func(context.Context) string

	cacheMu         sync.Mutex
	cache           map[string]candidateCacheEntry
	cacheGeneration uint64
	cacheBytes      int64
	refreshes       map[string]chan struct{}
	syncFlights     map[string]*candidateFlight
	// failures records the last provider failure per cache key so a provider
	// that is timing out or answering errors is not re-fetched, at full
	// timeout, once per resolve. Guarded by cacheMu.
	failures map[string]time.Time

	// tmdbCache memoizes the tmdb -> imdb translation, including negative
	// results, keyed by media type and tmdb id. Guarded by tmdbMu.
	tmdbMu    sync.Mutex
	tmdbCache map[string]tmdbCacheEntry
}

// tmdbCacheEntry is one cached TMDB external-ID translation. An empty imdbID
// is a negative entry: the lookup failed or the title has no IMDb id, so the
// short negative TTL applies.
type tmdbCacheEntry struct {
	imdbID    string
	expiresAt time.Time
}

// candidateFlight tracks one in-flight provider fetch for a cache key. done
// closes when the fetch returns; err carries the fetch's result and is set
// before done closes, so a waiter that observes done also observes err.
type candidateFlight struct {
	done chan struct{}
	err  error
}

// New builds a Resolver from a plain Config, clamping the cache TTL into its
// bounds and defaulting timeouts and metadata base URLs.
func New(cfg Config) *Resolver {
	return NewWithClient(cfg, nil)
}

// NewWithClient is New with an injectable HTTP client (nil selects a
// restricted-redirect client with the configured timeout). It exists for
// tests that stub the provider transport.
func NewWithClient(cfg Config, client *http.Client) *Resolver {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = defaultCacheTTL
	}
	if cfg.CacheTTL < minCacheTTL {
		cfg.CacheTTL = minCacheTTL
	}
	if cfg.CacheTTL > maxCacheTTL {
		cfg.CacheTTL = maxCacheTTL
	}
	if cfg.TVMazeBaseURL == "" {
		cfg.TVMazeBaseURL = defaultTVMazeBaseURL
	}
	if cfg.TMDBBaseURL == "" {
		cfg.TMDBBaseURL = defaultTMDBBaseURL
	}
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	if client == nil {
		client = newRestrictedRedirectHTTPClient(timeout)
	}
	return &Resolver{
		client: client,
		config: cfg,
	}
}

// SetCandidateClassifier installs the completed/failed classifier. It is safe
// to call at any time: the classifier is typically a long-lived client whose
// cache is refreshed out of band.
func (r *Resolver) SetCandidateClassifier(classifier CandidateClassifier) {
	r.mu.Lock()
	r.classifier = classifier
	r.mu.Unlock()
}

// SetReleaseGate installs the unreleased-media gate (nil disables it).
func (r *Resolver) SetReleaseGate(gate ReleaseGate) {
	r.mu.Lock()
	r.releaseGate = gate
	r.mu.Unlock()
}

// SetCandidateEnricher installs the derived-field enricher run at ingestion.
func (r *Resolver) SetCandidateEnricher(enricher CandidateEnricher) {
	r.mu.Lock()
	r.enricher = enricher
	r.mu.Unlock()
}

// SetLogger installs the logger (nil disables logging; provider URLs are
// never logged).
func (r *Resolver) SetLogger(logger *slog.Logger) {
	r.mu.Lock()
	r.logger = logger
	r.mu.Unlock()
}

// SetRequestIDReader installs the request-id reader for provider-fetch
// logging (the parent virtuallibrary helper in production). It must be
// called before the resolver serves traffic; until then fetches log without
// a request id rather than a wrong one. A nil reader is ignored.
func (r *Resolver) SetRequestIDReader(reader func(context.Context) string) {
	if r == nil || reader == nil {
		return
	}
	r.mu.Lock()
	r.requestIDReader = reader
	r.mu.Unlock()
}

// fetchRequestID resolves the edge request id for the provider-fetch log
// through the installed reader, or "" when none is installed.
func (r *Resolver) fetchRequestID(ctx context.Context) string {
	r.mu.RLock()
	reader := r.requestIDReader
	r.mu.RUnlock()
	if reader == nil {
		return ""
	}
	return reader(ctx)
}

// FetchReason names why a provider listing was performed, so a caller that
// attributes startup cost can separate a genuine discovery (a cold or expired
// cache miss) from an explicit user/forced refresh and from a background
// stale-grace refresh. It never changes fetch behavior; it is observation only.
type FetchReason string

const (
	// FetchReasonListing is a provider fetch on the ordinary read path: the
	// cache had no servable entry for the key. It is the cold discovery a
	// repeated start should be able to avoid once a resolution is reusable.
	FetchReasonListing FetchReason = "listing"
	// FetchReasonForced is a provider fetch that bypassed the fresh-serve floor,
	// either an explicit user refresh/retry or a declared provider-outage
	// re-list. It is a deliberate re-discovery, not a redundant one.
	FetchReasonForced FetchReason = "forced"
	// FetchReasonBackground is the stale-grace repopulation fetch. It runs off
	// the request path and is not part of any single start's critical path.
	FetchReasonBackground FetchReason = "background"
)

// ProviderFetchEvent reports one completed provider listing attempt. Duration
// covers the whole attempt, including a failed one; Count is the number of
// candidates the provider returned (0 on failure). CacheKey identifies the
// listing without exposing the provider URL. Err is non-nil for a failed
// attempt, so an observer can count failures separately from discovery.
type ProviderFetchEvent struct {
	CacheKey  string
	Count     int
	Reason    FetchReason
	Duration  time.Duration
	RequestID string
	Err       error
}

// ProviderFetchObserver receives one event per provider listing attempt the
// resolver performs under a context carrying it. It must be safe for
// concurrent use: a resolver can run several fetches for one request (a cache
// miss in listing plus a forced re-list in resolution) and background
// refreshes across requests.
type ProviderFetchObserver func(ProviderFetchEvent)

type providerFetchObserverContextKey struct{}

// WithProviderFetchObserver threads an observer into ctx for the provider
// listings the resolver performs while serving that context. It is the
// observability seam the startup path uses to attribute provider discovery
// separately from candidate resolution and probing, without the resolver
// knowing about HTTP or startup tracing. A nil observer leaves ctx untouched.
func WithProviderFetchObserver(ctx context.Context, observer ProviderFetchObserver) context.Context {
	if ctx == nil || observer == nil {
		return ctx
	}
	return context.WithValue(ctx, providerFetchObserverContextKey{}, observer)
}

// providerFetchObserverFromContext returns the observer threaded by the
// caller, or nil when none is present.
func providerFetchObserverFromContext(ctx context.Context) ProviderFetchObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(providerFetchObserverContextKey{}).(ProviderFetchObserver)
	return observer
}

// reportProviderFetch notifies a threaded observer of one completed listing
// attempt. It is a no-op without an observer, so the fetch path is unchanged
// for callers that do not opt in.
func reportProviderFetch(ctx context.Context, event ProviderFetchEvent) {
	observer := providerFetchObserverFromContext(ctx)
	if observer == nil {
		return
	}
	observer(event)
}

// unreleasedError reports that tracked release metadata places the requested
// movie or episode in the future. It is never surfaced as a playable
// candidate: the host selects candidates by rank without consulting
// availability flags, so any placeholder source would be handed to the
// player and fail at stream-open.
type unreleasedError struct {
	message string
}

func (e *unreleasedError) Error() string { return e.message }

func newUnreleasedError(imdbID string, airDate *time.Time, itemType string) *unreleasedError {
	formatted := "soon"
	if airDate != nil && !airDate.IsZero() {
		formatted = airDate.UTC().Format("2006-01-02 15:04 MST")
	}
	noun := "episode"
	if strings.EqualFold(itemType, "movie") {
		noun = "movie"
	}
	return &unreleasedError{message: fmt.Sprintf("This %s (%s) airs %s. Streams appear automatically once it is released.", noun, imdbID, formatted)}
}

type candidateCacheEntry struct {
	candidates []StreamCandidate
	expiresAt  time.Time
	// fetchedAt records when the provider actually answered, distinct from
	// lastAccess which moves on every serve. The forced-lookup floor is
	// judged against fetchedAt so repeated resolves inside one playback
	// start stay on one round-trip.
	fetchedAt  time.Time
	lastAccess time.Time
	sizeBytes  int64
}

type stremioResponse struct {
	Streams []StreamCandidate `json:"streams"`
}

type stremioManifest struct {
	ID        string            `json:"id"`
	Resources []json.RawMessage `json:"resources"`
	Types     []string          `json:"types"`
}

func cloneCandidates(candidates []StreamCandidate) []StreamCandidate {
	out := make([]StreamCandidate, len(candidates))
	for i, c := range candidates {
		out[i] = c
		out[i].AudioLanguages = append([]string(nil), c.AudioLanguages...)
		out[i].SubtitleLanguages = append([]string(nil), c.SubtitleLanguages...)
		out[i].VisualTags = append([]string(nil), c.VisualTags...)
		out[i].AudioTags = append([]string(nil), c.AudioTags...)
		out[i].RequestHeaders = maps.Clone(c.RequestHeaders)
		out[i].BehaviorHints.ProxyHeaders = maps.Clone(c.BehaviorHints.ProxyHeaders)
	}
	return out
}

// preferConfirmedCandidates applies the source-of-truth state to the candidate
// list on every serve: it honors the provider's cached badge, runs the
// classifier (AltMount's authoritative completed/failed state, then Prowlarr's
// confirmation), drops releases AltMount reports as failed, and stably moves
// confirmed releases ahead of unconfirmed ones. Order within each group is
// preserved, so the quality ranking still decides which confirmed release wins.
// The server-order preset skips promotion to preserve provider order before capping.
// Classification runs before dedup so a failed variant can never shadow a live
// duplicate of the same release.
//
// Deduplication collapses the per-file variants of one release to a single
// candidate; the confirmed variant wins its group when one exists. The dropped
// variants' ids are returned mapped to their release's surviving keeper id, so
// a pin that dedup collapsed can be translated instead of falling through to an
// unrelated release. The custom format verdict is applied later
// (rankCandidatesForVirtualPath), so reject never interacts with confirmation.
func (r *Resolver) preferConfirmedCandidates(ctx context.Context, candidates []StreamCandidate) ([]StreamCandidate, map[string]string) {
	if len(candidates) == 0 {
		return candidates, nil
	}
	// The AltMount Stremio addon marks releases it already imported with a
	// "⚡ Cached" badge. That is a free, zero-config signal even when the
	// AltMount API is not configured, so honor it before the classifier runs.
	markAltmountBadgeCandidates(candidates)
	r.mu.RLock()
	classifier := r.classifier
	r.mu.RUnlock()
	if classifier != nil {
		classifier.ClassifyCandidates(candidates)
	}
	// Capture failed identities before the drop mutates the slice, so an
	// all-failed answer is diagnosable rather than a silent empty result.
	beforeDrop := len(candidates)
	failedNames := failedCandidateIdentities(candidates, 8)
	candidates = dropFailedCandidates(candidates)
	if len(candidates) == 0 && beforeDrop > 0 {
		r.mu.RLock()
		logger := r.logger
		r.mu.RUnlock()
		if logger != nil {
			logger.WarnContext(ctx, "every provider candidate was dropped as failed by the classifier",
				"count", beforeDrop, "candidates", failedNames)
		}
	}
	candidates, dropped := dedupeCandidates(candidates)
	r.mu.RLock()
	trustServerOrder := r.config.Quality.TrustServerOrder()
	r.mu.RUnlock()
	if trustServerOrder {
		return candidates, dropped
	}
	return stablePartitionCandidates(candidates), dropped
}

// processCandidates runs the per-answer ingestion pipeline: apply AltMount's
// badge/classifier state and Prowlarr's confirmation, drop releases the source
// of truth reports failed, collapse per-file variants to one candidate per
// release, move confirmed releases ahead of unconfirmed ones, and truncate to
// the selectable cap. It is called on every serve so a classifier state change
// takes effect without waiting for the cache TTL, and it returns the
// dropped-variant -> keeper map for that answer.
//
// The keeper map is filtered to the surviving candidates after truncation: a
// dropped variant whose keeper ranked beyond the selectable cap has no surviving
// representative, so the entry is dropped and a pin on it is treated as a
// genuinely dead release (the existing dead-pin fallback) instead of being
// translated to a keeper that is not in the list.
func (r *Resolver) processCandidates(ctx context.Context, candidates []StreamCandidate) ([]StreamCandidate, map[string]string) {
	candidates, dropped := r.preferConfirmedCandidates(ctx, candidates)
	if len(candidates) > maxVirtualCandidates {
		candidates = candidates[:maxVirtualCandidates]
	}
	if len(dropped) > 0 {
		surviving := make(map[string]struct{}, len(candidates))
		for i := range candidates {
			surviving[stream.CandidateVariantID(candidates[i])] = struct{}{}
		}
		for droppedID, keeperID := range dropped {
			if _, ok := surviving[keeperID]; !ok {
				delete(dropped, droppedID)
			}
		}
	}
	return candidates, dropped
}

// failedCandidateIdentities returns up to limit display identities of the
// candidates the classifier marked failed. Names are provider display text;
// provider URLs are deliberately never logged.
func failedCandidateIdentities(candidates []StreamCandidate, limit int) []string {
	names := make([]string, 0, limit)
	for _, candidate := range candidates {
		if !candidate.SourceFailed {
			continue
		}
		if len(names) >= limit {
			break
		}
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			name = strings.TrimSpace(candidate.Title)
		}
		if name == "" {
			name = "(unnamed)"
		}
		names = append(names, name)
	}
	return names
}

// dropFailedCandidates removes releases the source of truth reports as dead
// before deduplication, so a dead variant can never shadow a live duplicate of
// the same release.
func dropFailedCandidates(candidates []StreamCandidate) []StreamCandidate {
	kept := candidates[:0]
	for _, candidate := range candidates {
		if candidate.SourceFailed {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept
}

// candidateDedupKey returns the stable identity shared by provider candidates
// that describe the same playable release, in tiers from strongest to weakest.
//
// A stable external identity beats any name/size/profile heuristic: a non-empty
// VideoHash identifies the actual video content, and a SourceGUID identifies
// the indexed release the classifier matched. The maintainer's rule is that a
// shared GUID is enough to call two candidates one release, so the GUID tier
// deliberately ignores name, size, and quality profile differences — the same
// release re-offered with a different file list is still one release.
//
// Only when neither identity is available does it fall back to the release
// name plus exact file size. That tier collapses per-file torrent variants
// (one candidate per contained file, offered under different result IDs) while
// keeping genuinely distinct releases apart. An empty key means the candidate
// carries too little identity to collapse and is always kept.
func candidateDedupKey(candidate StreamCandidate) string {
	// Tier 1a: provider-supplied content hash. Accepts the Stremio
	// behaviorHints.videoHash and a torrent infoHash; both pin the bytes
	// across a re-listing's result renumbering.
	if hash := strings.ToLower(stream.CandidateVideoHash(candidate)); hash != "" {
		return "vidhash:" + hash
	}
	// Tier 1b: GUID of the indexed release the classifier tied us to.
	if guid := strings.TrimSpace(candidate.SourceGUID); guid != "" {
		return "guid:" + guid
	}
	// Tier 2: release name + exact size. The quality profile is deliberately
	// not part of the key: per-file variants of one release can parse
	// different resolution/codec/HDR metadata from their differing result
	// ids, and upstream treats a shared release name and size as sufficient
	// to call them one release.
	releaseKey := candidateDedupName(candidate)
	if releaseKey == "" {
		return ""
	}
	// True duplicates report identical byte sizes; distinct releases differ.
	// Unknown sizes collapse only with other unknown sizes of the same name.
	sizeKey := "0"
	if size := stream.CandidateDeclaredSize(candidate); size > 0 {
		sizeKey = strconv.FormatInt(size, 10)
	}
	return releaseKey + "\x00" + sizeKey
}

// candidateDedupName returns the release identity used for deduplication. It
// prefers the provider's release-title line because behaviorHints.filename and
// the URL name a single file inside a multi-file release: two files of one
// torrent yield different filenames/result IDs and would otherwise escape the
// name+size collapse. When only a per-file name is available, a trailing
// numeric file index (the `<hash>-43` form) is stripped.
func candidateDedupName(candidate StreamCandidate) string {
	for _, value := range []string{
		firstReleaseLine(candidate.Title),
		firstReleaseLine(candidate.Name),
	} {
		if key := releaseNameKey(value); key != "" {
			return key
		}
	}
	for _, value := range []string{
		candidate.BehaviorHints.Filename,
		urlPathBase(candidate.URL),
	} {
		if key := releaseNameKey(trimPerFileIndex(value)); key != "" {
			return key
		}
	}
	// Last resort: the first description line, where Stremio addons that
	// format Name/Title as display labels keep the real release name.
	// Guarded: a bare spec line ("1080p", a size, an emoji row) must never
	// become identity — it would collide across unrelated releases. Release
	// names all but always carry digits (season/episode/resolution/year),
	// so demand length plus a digit; anything else falls back to no
	// identity, exactly as before.
	if line := firstReleaseLine(candidate.Description); len(line) >= 12 && strings.IndexFunc(line, unicode.IsDigit) >= 0 {
		if key := releaseNameKey(line); key != "" {
			return key
		}
	}
	return ""
}

// CandidateReleaseName exposes the normalized release identity that
// candidateDedupKey uses in its name+size tier for a caller that persists the
// durable identity of a candidate. It is the same value the dedup collapse
// compares, so a persisted row can be re-matched to a fresh listing whose
// result id changed.
func CandidateReleaseName(candidate StreamCandidate) string {
	return candidateDedupName(candidate)
}

// PersistedIdentityTiers is a persisted candidate identity split into its three
// comparison tiers, each already normalized the way the deduplication chain
// normalizes it: the lowercase video hash, the source GUID, and the normalized
// release name. Size is kept separately because the name tier compares it with
// a tolerance rather than for equality. A tier is empty when the row never
// carried it.
type PersistedIdentityTiers struct {
	VideoHash   string
	GUID        string
	ReleaseName string
	ReleaseSize int64
}

// NewPersistedIdentityTiers normalizes an identity into its comparison tiers.
// The hash is lowercased like candidateDedupKey's hash tier, so a
// differently-cased stored hash still matches.
func NewPersistedIdentityTiers(videoHash, guid, releaseName string, releaseSize int64) PersistedIdentityTiers {
	return PersistedIdentityTiers{
		VideoHash:   strings.ToLower(strings.TrimSpace(videoHash)),
		GUID:        strings.TrimSpace(guid),
		ReleaseName: strings.TrimSpace(releaseName),
		ReleaseSize: releaseSize,
	}
}

// SharedTier returns the name of the strongest shared non-empty tier and
// whether any tier agreed. Tier names are "video_hash", "guid" and
// "release_name". It is used for logging so "identity mismatch" and "no shared
// tier" are distinguishable.
//
// The comparison is deliberately asymmetric, because the persisted row is the
// viewer's authoritative selection while the listed candidate is a fresh claim:
//
//   - A tier both sides carry that disagrees is a veto: two different hashes
//     (or GUIDs) are proof of different releases.
//   - A stored stronger tier the candidate does not carry is also a veto. The
//     row recorded a hash; a listing that cannot corroborate it is not proof of
//     the same release, so a coincidental release name must not re-identify it.
//   - A candidate stronger tier the stored row does not carry is neutral. That
//     asymmetry is exactly what lets the ~thousand name-only rows re-identify a
//     renumbered release whose fresh listing now carries a hash or GUID.
func (p PersistedIdentityTiers) SharedTier(other PersistedIdentityTiers) (string, bool) {
	// Disagreement on a tier both sides carry is decisive.
	if p.VideoHash != "" && other.VideoHash != "" && p.VideoHash != other.VideoHash {
		return "", false
	}
	if p.GUID != "" && other.GUID != "" && p.GUID != other.GUID && p.VideoHash != other.VideoHash {
		return "", false
	}
	// A stronger stored tier the candidate cannot corroborate constrains the
	// match: it may only be satisfied by the same tier, never by a weaker one.
	if p.VideoHash != "" && other.VideoHash != "" {
		return "video_hash", true
	}
	if p.VideoHash != "" {
		return "", false
	}
	if p.GUID != "" && other.GUID != "" {
		return "guid", true
	}
	if p.GUID != "" {
		return "", false
	}
	if p.ReleaseName != "" && other.ReleaseName != "" && p.ReleaseName == other.ReleaseName {
		if releaseSizesAgree(p.ReleaseSize, other.ReleaseSize) {
			return "release_name", true
		}
	}
	return "", false
}

// releaseSizesAgree reports whether two release sizes plausibly describe the
// same file for the name tier. Provider display sizes are rounded, and a
// re-list may report a slightly different byte count for one release, so a
// small relative drift is tolerated instead of requiring equality. An unknown
// size on either side is neutral: the names already agree, and refusing the
// match would turn a renumbered release into a dead pin.
func releaseSizesAgree(a, b int64) bool {
	if a <= 0 || b <= 0 {
		return true
	}
	if a == b {
		return true
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	largest := a
	if b > largest {
		largest = b
	}
	return float64(diff)/float64(largest) <= persistedNameSizeDrift
}

// PersistedIdentitiesMatch reports whether two persisted candidate identities
// name the same release, comparing compatible tiers symmetrically rather than
// by a single strongest-tier key.
//
// A strongest-tier key cannot answer this: a name-only row keys as name+size
// while a GUID- or hash-carrying row keys in that stronger tier, so two rows
// of one release that carry different tiers produce different keys and would
// be read as distinct releases. This per-tier comparison instead:
//
//   - Vetos a tier both sides carry that disagrees (two different hashes or
//     GUIDs are proof of different releases).
//   - Matches on any tier both sides carry that agrees (the strongest such
//     tier is returned for logging).
//   - Matches on the name tier only when both sides carry the same normalized
//     release name and their sizes plausibly agree, so a name coincidence does
//     not merge distinct releases.
//
// This is the symmetric sibling of SharedTier, which is deliberately
// asymmetric because it decides whether a fresh candidate may re-identify the
// viewer's persisted row. This predicate answers a different question — "are
// these two catalog rows the same release?" — so both sides are authoritative
// and a tier absent from one side is neutral in both directions. An identity
// with every tier empty never matches, so identity-less rows are never merged.
func PersistedIdentitiesMatch(a, b PersistedIdentityTiers) (string, bool) {
	if a.VideoHash != "" && b.VideoHash != "" && a.VideoHash != b.VideoHash {
		return "", false
	}
	if a.GUID != "" && b.GUID != "" && a.GUID != b.GUID {
		return "", false
	}
	if a.VideoHash != "" && b.VideoHash != "" {
		return "video_hash", true
	}
	if a.GUID != "" && b.GUID != "" {
		return "guid", true
	}
	if a.ReleaseName != "" && b.ReleaseName != "" && a.ReleaseName == b.ReleaseName &&
		releaseSizesAgree(a.ReleaseSize, b.ReleaseSize) {
		return "release_name", true
	}
	return "", false
}

// persistedNameSizeDrift is the tolerated relative size difference on the name
// tier, matching the ≤10% width the provider classifiers already use so a
// rounded display size does not miss a renumbered release.
const persistedNameSizeDrift = 0.10

// PersistedIdentityMatch reports why a persisted identity did or did not match a
// candidate, so a refusal can log identity presence instead of a bare miss.
type PersistedIdentityMatch struct {
	// Matched is true when the candidate shares a non-empty tier.
	Matched bool
	// Tier is the strongest shared tier ("video_hash", "guid",
	// "release_name") when Matched.
	Tier string
	// IdentityEmptyTiers lists the persisted identity's tiers that are empty,
	// in tier order. An identity with every tier empty can never match.
	IdentityEmptyTiers []string
	// CandidateEmptyTiers lists the matched candidate's empty tiers.
	CandidateEmptyTiers []string
}

// HasIdentity reports whether the persisted identity carried any usable tier at
// all. A false value explains a refusal as "no identity", distinct from a real
// tier mismatch.
func (m PersistedIdentityMatch) HasIdentity() bool {
	return len(m.IdentityEmptyTiers) < 3
}

// emptyTiers names the tiers absent from an identity, in tier order.
func (p PersistedIdentityTiers) emptyTiers() []string {
	var empty []string
	if p.VideoHash == "" {
		empty = append(empty, "video_hash")
	}
	if p.GUID == "" {
		empty = append(empty, "guid")
	}
	if p.ReleaseName == "" {
		empty = append(empty, "release_name")
	}
	return empty
}

// PersistedDedupKey builds the dedup key a persisted candidate identity
// represents, using the same tier precedence as candidateDedupKey: a non-empty
// video hash, then a source GUID, then the normalized release name plus exact
// size. It is exported so a re-match caller can compare a stored row against a
// fresh listing without duplicating the tier rules that decide whether two
// candidates are one release. An empty result means the identity carries no
// usable tier and can never be re-matched.
//
// MatchCandidateByPersistedIdentityReport is the per-tier alternative for a
// re-match: this key is deliberately strongest-tier-only, so a name-only row
// keys as name+size and can never be compared against a GUID/hash candidate.
// Use it only where an exact same-tier equality is the question (a transport
// replacement decision), not for re-identification.
func PersistedDedupKey(videoHash, guid, releaseName string, releaseSize int64) string {
	if hash := strings.ToLower(strings.TrimSpace(videoHash)); hash != "" {
		return "vidhash:" + hash
	}
	if g := strings.TrimSpace(guid); g != "" {
		return "guid:" + g
	}
	releaseKey := strings.TrimSpace(releaseName)
	if releaseKey == "" {
		return ""
	}
	sizeKey := "0"
	if releaseSize > 0 {
		sizeKey = strconv.FormatInt(releaseSize, 10)
	}
	return releaseKey + "\x00" + sizeKey
}

// MatchCandidateByPersistedIdentity returns the first listed candidate that
// shares any non-empty durable identity tier with the persisted identity.
//
// The comparison is per tier, not by a single precedence key: a name-only row
// matches a listed candidate that also carries a GUID or a hash for the same
// release, with a small size drift tolerated on the name tier. This is what
// lets the ~thousand name-only rows re-identify a renumbered release instead of
// being reported dead. An identity with every tier empty never matches, so a
// legacy row keeps today's behavior.
func MatchCandidateByPersistedIdentity(candidates []StreamCandidate, videoHash, guid, releaseName string, releaseSize int64) (StreamCandidate, bool) {
	matched, ok, _ := MatchCandidateByPersistedIdentityReport(candidates, videoHash, guid, releaseName, releaseSize)
	return matched, ok
}

// MatchCandidateByPersistedIdentityReport is MatchCandidateByPersistedIdentity
// plus the report that distinguishes "the row carries no identity" from "the
// row's identity disagreed with every listed candidate". Callers that log a
// refusal should use this so a renumber can be diagnosed from one line.
func MatchCandidateByPersistedIdentityReport(candidates []StreamCandidate, videoHash, guid, releaseName string, releaseSize int64) (StreamCandidate, bool, PersistedIdentityMatch) {
	identity := NewPersistedIdentityTiers(videoHash, guid, releaseName, releaseSize)
	report := PersistedIdentityMatch{IdentityEmptyTiers: identity.emptyTiers()}
	if !report.HasIdentity() {
		return StreamCandidate{}, false, report
	}
	for _, candidate := range candidates {
		candidateTiers := NewPersistedIdentityTiers(
			stream.CandidateVideoHash(candidate),
			candidate.SourceGUID,
			candidateDedupName(candidate),
			stream.CandidateDeclaredSize(candidate),
		)
		if tier, shared := identity.SharedTier(candidateTiers); shared {
			report.Matched = true
			report.Tier = tier
			return candidate, true, report
		}
	}
	// No candidate agreed. Report the first candidate's empty tiers so the
	// caller can tell "the provider stopped declaring identity" from a genuine
	// mismatch.
	if len(candidates) > 0 {
		report.CandidateEmptyTiers = NewPersistedIdentityTiers(
			stream.CandidateVideoHash(candidates[0]),
			candidates[0].SourceGUID,
			candidateDedupName(candidates[0]),
			stream.CandidateDeclaredSize(candidates[0]),
		).emptyTiers()
	}
	return StreamCandidate{}, false, report
}

// urlPathBase returns the last path segment of a stream URL, or "" when the
// URL cannot be parsed.
func urlPathBase(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return path.Base(parsed.Path)
}

// trimPerFileIndex drops a trailing `-<digits>` result/file index such as the
// `-43` AltMount appends to per-file result IDs and filenames.
func trimPerFileIndex(name string) string {
	if idx := strings.LastIndexByte(name, '-'); idx > 0 {
		suffix := name[idx+1:]
		for _, ext := range []string{".mkv", ".mp4", ".avi", ".ts", ".m2ts", ".mov", ".webm"} {
			suffix = strings.TrimSuffix(suffix, ext)
		}
		if suffix != "" {
			if _, err := strconv.Atoi(suffix); err == nil {
				return name[:idx]
			}
		}
	}
	return name
}

// dedupeCandidates collapses candidates that share a release identity, keeping
// the first-ranked variant of each group. When a group carries a confirmed
// variant, that variant is the keeper regardless of rank: confirmation is
// authoritative for the release, and the remaining variants differ only in
// which file the provider selects server-side at stream time.
//
// It also returns the dropped-variant -> keeper-variant map for the collapsed
// groups. A caller holding a pinned variant id that is no longer in the list
// (because this collapse removed it) can use the map to resolve to its release's
// surviving keeper instead of substituting an unrelated release.
func dedupeCandidates(candidates []StreamCandidate) ([]StreamCandidate, map[string]string) {
	if len(candidates) < 2 {
		return candidates, nil
	}
	keyKeeper := make(map[string]int, len(candidates))
	keep := make([]bool, len(candidates))
	for i, candidate := range candidates {
		key := candidateDedupKey(candidate)
		if key == "" {
			keep[i] = true
			continue
		}
		existing, seen := keyKeeper[key]
		if !seen {
			keyKeeper[key] = i
			keep[i] = true
			continue
		}
		if candidate.SourceConfirmed && !candidates[existing].SourceConfirmed {
			keep[existing] = false
			keyKeeper[key] = i
			keep[i] = true
		} else if !candidate.SourcePending && candidates[existing].SourcePending {
			// A pending duplicate never displaces a ready keeper, and a ready
			// duplicate displaces a pending keeper: the kept variant must be
			// the one playable now, not the one still fetching. Both-pending
			// keeps the first, preserving determinism.
			keep[existing] = false
			keyKeeper[key] = i
			keep[i] = true
		}
	}
	// Build the dropped -> keeper map before compacting: the compaction below
	// reuses the backing array, so the original indices must be read first.
	var dropped map[string]string
	for i := range candidates {
		if keep[i] {
			continue
		}
		key := candidateDedupKey(candidates[i])
		if key == "" {
			continue
		}
		keeperIdx, ok := keyKeeper[key]
		if !ok || keeperIdx < 0 || keeperIdx >= len(candidates) || !keep[keeperIdx] {
			continue
		}
		droppedID := stream.CandidateVariantID(candidates[i])
		keeperID := stream.CandidateVariantID(candidates[keeperIdx])
		if droppedID == "" || keeperID == "" || droppedID == keeperID {
			continue
		}
		if dropped == nil {
			dropped = make(map[string]string)
		}
		dropped[droppedID] = keeperID
	}
	kept := make([]StreamCandidate, 0, len(candidates))
	for i := range candidates {
		if keep[i] {
			kept = append(kept, candidates[i])
		}
	}
	return kept, dropped
}

// stablePartitionCandidates drops known-dead candidates and stably moves
// confirmed ones to the front, pending ones next, returning the
// possibly-shortened slice. Pending is never dropped: it is the waitable
// middle between ready and unknown.
func stablePartitionCandidates(candidates []StreamCandidate) []StreamCandidate {
	kept := candidates[:0]
	confirmed := 0
	pending := 0
	for _, candidate := range candidates {
		if candidate.SourceFailed {
			continue
		}
		if candidate.SourceConfirmed {
			confirmed++
		} else if candidate.SourcePending {
			pending++
		}
		kept = append(kept, candidate)
	}
	candidates = kept
	if confirmed == 0 && pending == 0 {
		return candidates
	}
	if confirmed == len(candidates) {
		return candidates
	}
	ordered := make([]StreamCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.SourceConfirmed {
			ordered = append(ordered, candidate)
		}
	}
	for _, candidate := range candidates {
		if !candidate.SourceConfirmed && candidate.SourcePending {
			ordered = append(ordered, candidate)
		}
	}
	for _, candidate := range candidates {
		if !candidate.SourceConfirmed && !candidate.SourcePending {
			ordered = append(ordered, candidate)
		}
	}
	copy(candidates, ordered)
	return candidates
}

// GetCandidates returns the ranked provider candidates for a virtual:// URI,
// serving the bounded candidate cache when possible.
func (r *Resolver) GetCandidates(ctx context.Context, virtualPath string) ([]StreamCandidate, string, string, error) {
	candidates, _, mediaType, mediaID, err := r.getCandidatesWithKeepers(ctx, virtualPath, false, false)
	return candidates, mediaType, mediaID, err
}

// GetCandidatesFresh bypasses the bounded candidate cache for an explicit
// user refresh/retry while retaining the normal cache behavior by default.
func (r *Resolver) GetCandidatesFresh(ctx context.Context, virtualPath string) ([]StreamCandidate, string, string, error) {
	candidates, _, mediaType, mediaID, err := r.getCandidatesWithKeepers(ctx, virtualPath, true, false)
	return candidates, mediaType, mediaID, err
}

// GetCandidatesFreshUnbounded re-lists candidates from the provider even when
// the cache entry is younger than freshServeFloor. The floor exists so the
// transport failover walk (which excludes failed candidate IDs) stays on one
// provider round-trip per playback start; a genuine re-list — the host asking
// for a fresh answer after the relay returned 502 — must not be served the
// same dead candidates it is trying to escape.
func (r *Resolver) GetCandidatesFreshUnbounded(ctx context.Context, virtualPath string) ([]StreamCandidate, string, string, error) {
	candidates, _, mediaType, mediaID, err := r.getCandidatesWithKeepers(ctx, virtualPath, true, true)
	return candidates, mediaType, mediaID, err
}

// GetCandidatesWithKeepers is GetCandidates plus the deduplicated-variant map
// for that answer: a candidate id that dedup collapsed maps to the keeper id of
// its release. ResolveDetailed uses it so a pin whose variant was collapsed
// resolves to its release's surviving keeper instead of falling through to a
// different release.
func (r *Resolver) GetCandidatesWithKeepers(ctx context.Context, virtualPath string) ([]StreamCandidate, map[string]string, string, string, error) {
	return r.getCandidatesWithKeepers(ctx, virtualPath, false, false)
}

// GetCandidatesFreshWithKeepers is GetCandidatesWithKeepers with forceRefresh.
func (r *Resolver) GetCandidatesFreshWithKeepers(ctx context.Context, virtualPath string) ([]StreamCandidate, map[string]string, string, string, error) {
	return r.getCandidatesWithKeepers(ctx, virtualPath, true, false)
}

// GetCandidatesFreshUnboundedWithKeepers is GetCandidatesFreshWithKeepers with
// the fresh-serve floor bypassed. A provider-outage retry needs a real re-list:
// the outage's empty answer was just negative-cached, so a floor-bounded forced
// lookup would serve the same empty entry back and the retry would be
// pointless. Only callers that declared a genuine outage re-list use it.
func (r *Resolver) GetCandidatesFreshUnboundedWithKeepers(ctx context.Context, virtualPath string) ([]StreamCandidate, map[string]string, string, string, error) {
	return r.getCandidatesWithKeepers(ctx, virtualPath, true, true)
}

// getCandidatesWithKeepers serves the candidate list and runs the ingestion
// pipeline on every answer. The classifier is a live source of truth, so a
// release that completed or failed since the cache entry was written is
// reflected immediately instead of after the TTL. The pipeline is applied here
// (not before the cache store) so it runs exactly once per answer; the cache
// holds the provider's raw, enriched candidates and the returned dropped ->
// keeper map is recomputed from them, which means it is current on every
// cache hit rather than stale relative to the classifier.
func (r *Resolver) getCandidatesWithKeepers(ctx context.Context, virtualPath string, forceRefresh bool, bypassFloor bool) ([]StreamCandidate, map[string]string, string, string, error) {
	candidates, mediaType, mediaID, err := r.getCandidatesRaw(ctx, virtualPath, forceRefresh, bypassFloor)
	if err != nil {
		return nil, nil, mediaType, mediaID, err
	}
	processed, dropped := r.processCandidates(ctx, candidates)
	return processed, dropped, mediaType, mediaID, nil
}

func (r *Resolver) getCandidatesRaw(ctx context.Context, virtualPath string, forceRefresh bool, bypassFloor bool) ([]StreamCandidate, string, string, error) {
	mediaType, mediaID, err := parseVirtualPath(virtualPath)
	if err != nil {
		return nil, mediaType, mediaID, err
	}
	r.mu.RLock()
	config := r.config
	generation := r.generation
	releaseGate := r.releaseGate
	r.mu.RUnlock()

	if strings.Contains(virtualPath, "refresh=1") || strings.Contains(virtualPath, "force=1") {
		forceRefresh = true
	}
	// strip query from mediaID
	if idx := strings.Index(mediaID, "?"); idx != -1 {
		mediaID = mediaID[:idx]
	}
	// Silo keeps TVDB-based catalog IDs for stable series identity, but the
	// Stremio stream protocol expects IMDb video IDs (tt...:season:episode).
	// Translate legacy TVDB virtual paths before contacting the provider.
	if mediaType == "series" {
		mediaID, err = r.normalizeSeriesProviderID(ctx, mediaID, config.TVMazeBaseURL)
		if err != nil {
			return nil, mediaType, mediaID, err
		}
	}
	if strings.HasPrefix(strings.ToLower(mediaID), "tmdb:") {
		mediaID, err = r.normalizeTMDBProviderID(ctx, mediaType, mediaID, config.TMDBAPIKey, config.TMDBBaseURL)
		if err != nil {
			return nil, mediaType, mediaID, err
		}
	}

	// Intercept unreleased media before contacting the streaming provider
	if releaseGate != nil {
		imdbID := mediaID
		season := 0
		episode := 0
		if mediaType == "series" {
			parts := strings.Split(mediaID, ":")
			if len(parts) >= 3 {
				imdbID = parts[0]
				season, _ = strconv.Atoi(parts[1])
				episode, _ = strconv.Atoi(parts[2])
			} else if len(parts) == 1 {
				imdbID = parts[0]
			}
		}
		if released, airDate := releaseGate.IsReleased(mediaType, imdbID, season, episode); !released {
			r.mu.RLock()
			logger := r.logger
			r.mu.RUnlock()
			if logger != nil {
				attrs := []any{"media_type", mediaType, "media_id", imdbID}
				if airDate != nil && !airDate.IsZero() {
					attrs = append(attrs, "air_date", airDate.UTC().Format(time.RFC3339))
				}
				logger.WarnContext(ctx, "virtual playback blocked by the release gate", attrs...)
			}
			return nil, mediaType, mediaID, newUnreleasedError(imdbID, airDate, mediaType)
		}
	}
	cacheKey := mediaType + "|" + mediaID

	if forceRefresh && !bypassFloor {
		// Forced lookups still serve very recent answers. One playback start
		// walks several resolve rounds; re-fetching within a single attempt
		// multiplies provider latency without producing new information, so
		// entries younger than the floor are served as-is regardless of
		// emptiness. Anything older takes the full fetch path below.
		now := time.Now()
		r.cacheMu.Lock()
		entry, ok := r.cache[cacheKey]
		if sameGeneration := r.cacheGeneration == generation; sameGeneration && ok && now.Before(entry.fetchedAt.Add(freshServeFloor)) {
			candidates := cloneCandidates(entry.candidates)
			entry.lastAccess = now
			r.cache[cacheKey] = entry
			r.cacheMu.Unlock()
			return candidates, mediaType, mediaID, nil
		}
		r.cacheMu.Unlock()
	}

	// Cache tiers: fresh serve → stale-in-grace serve + one background
	// refresh → singleflight blocking fetch past grace or on force_refresh.
	if !forceRefresh {
		now := time.Now()
		r.cacheMu.Lock()
		entry, ok := r.cache[cacheKey]
		sameGeneration := r.cacheGeneration == generation
		switch {
		case sameGeneration && ok && now.Before(entry.expiresAt):
			candidates := cloneCandidates(entry.candidates)
			entry.lastAccess = now
			r.cache[cacheKey] = entry
			r.cacheMu.Unlock()
			return candidates, mediaType, mediaID, nil
		case sameGeneration && ok && len(entry.candidates) > 0 &&
			!now.Before(entry.expiresAt) && now.Before(entry.expiresAt.Add(candidateStaleGrace)):
			candidates := cloneCandidates(entry.candidates)
			entry.lastAccess = now
			r.cache[cacheKey] = entry
			started := r.startRefreshLocked(cacheKey, generation, config, mediaType, mediaID)
			r.cacheMu.Unlock()
			if started {
				r.debugLog("stale candidates served; background refresh started", cacheKey, len(candidates))
			} else {
				r.debugLog("stale candidates served; refresh already running", cacheKey, len(candidates))
			}
			return candidates, mediaType, mediaID, nil
		}
		r.cacheMu.Unlock()
	}

	// A recent provider failure for this key is failed fast instead of
	// re-paying the provider timeout on every resolve. A still-servable
	// positive entry (fresh or inside stale grace) is preferred over the
	// error, so a provider flap keeps playback alive. A declared outage
	// re-list (bypassFloor) skips this: recovery must not be blocked by the
	// backoff the outage itself recorded.
	if !bypassFloor && r.providerFailureActive(cacheKey) {
		if cached, ok := r.servePositiveCachedCandidates(cacheKey, generation); ok {
			r.debugLog("recent provider failure; serving cached positive", cacheKey, len(cached))
			return cached, mediaType, mediaID, nil
		}
		return nil, mediaType, mediaID, fmt.Errorf("%w: provider listing failed recently", ErrProviderUnavailable)
	}

	// Forced lookups skip the cache tiers above, so without this they would
	// bypass the keyed flight and pay one provider round-trip per call. A
	// version liveness check fans out per file, but every version of one item
	// shares a cache key: 40 files of one listing would issue 40 identical
	// re-lists and starve a concurrent playback start of provider capacity.
	// Joining the same keyed flight the non-forced path uses collapses them to
	// one provider fetch per listing, with every caller served its result. The
	// join respects context cancellation, so a caller bounded by its own
	// per-file budget still returns when that budget fires.
	// A fetch on this path is a forced/refresh discovery when the caller asked
	// for one; otherwise it is an ordinary listing miss. The reason is
	// observation only: it travels with the shared flight and the direct
	// attempt below so a fetch observer can separate real discovery from a
	// deliberate re-list.
	fetchReason := FetchReasonListing
	if forceRefresh || bypassFloor {
		fetchReason = FetchReasonForced
	}
	if wait := r.joinFlight(ctx, cacheKey, config, generation, mediaType, mediaID, fetchReason); wait != nil {
		candidates, ok, err := r.awaitFlight(ctx, wait, cacheKey)
		if err != nil {
			// The joined flight failed. Surface its provider error instead of
			// starting a second full-timeout fetch on this request's context;
			// the failure backoff recorded by that flight makes the next
			// resolve fail fast.
			return nil, "", "", err
		}
		if ok {
			return candidates, mediaType, mediaID, nil
		}
		// Flight completed without a usable entry (expired, past grace, or
		// absent); fall through to our own attempt.
	}

	candidates, err := r.fetchProviderCandidates(ctx, config, generation, cacheKey, mediaType, mediaID, fetchReason)
	if err != nil {
		if ctx.Err() == nil {
			// A caller-canceled request is not the provider's state; recording
			// it would blackhole this key against a healthy provider.
			r.recordProviderFailure(cacheKey)
		}
		return candidates, mediaType, mediaID, err
	}
	if len(candidates) == 0 {
		// The provider flapped to an empty answer, but the store may have kept
		// a positive entry that is still servable (fresh, or within stale
		// grace). Serving it keeps playback alive; the empty answer is
		// returned only once no positive entry is servable, which also
		// preserves the escape-from-dead-candidates purpose of a forced
		// re-list.
		if cached, ok := r.servePositiveCachedCandidates(cacheKey, generation); ok {
			r.debugLog("empty provider answer served; cached positive retained", cacheKey, len(cached))
			return cached, mediaType, mediaID, nil
		}
	}
	return candidates, mediaType, mediaID, err
}

// servePositiveCachedCandidates returns the key's non-empty cache entry when it
// is still servable (fresh, or within candidateStaleGrace), and reports whether
// one was found. It honors the cache generation check and refreshes lastAccess
// the same way the direct read tiers do.
func (r *Resolver) servePositiveCachedCandidates(cacheKey string, generation uint64) ([]StreamCandidate, bool) {
	now := time.Now()
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.cacheGeneration != generation {
		return nil, false
	}
	entry, ok := r.cache[cacheKey]
	if !ok || len(entry.candidates) == 0 {
		return nil, false
	}
	if !now.Before(entry.expiresAt.Add(candidateStaleGrace)) {
		return nil, false
	}
	candidates := cloneCandidates(entry.candidates)
	entry.lastAccess = now
	r.cache[cacheKey] = entry
	return candidates, true
}

// resolveBoundLogAttrs reports the remaining time one resolve wait may spend,
// for logs. It never emits a negative bound: a deadline the caller's own budget
// already consumed is 0 ms left, and the explicit bound_exhausted flag keeps
// that state visible instead of logging a wait that can never happen (the
// production series bound_ms=-34/-65/-95/-126). The bound itself is derived by
// the caller from the request's remaining time (see
// handlers.virtualResolveAttemptBudget), which refuses to start the attempt at
// all once nothing is left.
func resolveBoundLogAttrs(remaining time.Duration) []any {
	ms := remaining.Milliseconds()
	if ms > 0 {
		return []any{"bound_ms", ms}
	}
	return []any{"bound_ms", int64(0), "bound_exhausted", true}
}

// awaitFlight waits for an in-flight provider fetch, respecting the caller's
// context, and serves the cache entry that flight wrote when it is usable: a
// positive answer still inside stale grace, or a fresh negative. A flight that
// failed reports its provider error before any cache inspection, so a waiter
// does not mistake a stale positive entry the failed flight never replaced for
// a successful result. It reports ok=false with a nil error when a successful
// flight left no usable entry, so the caller can run its own fetch; a canceled
// wait returns the context error so callers keep the bare cancellation they
// saw before. It does not hold cacheMu across the wait.
func (r *Resolver) awaitFlight(ctx context.Context, flight *candidateFlight, cacheKey string) ([]StreamCandidate, bool, error) {
	started := time.Now()
	r.mu.RLock()
	waitLogger := r.logger
	r.mu.RUnlock()
	if waitLogger != nil {
		attrs := []any{
			"component", "virtuallibrary",
			"wait_kind", "fetch",
			"wait_on", "provider_fetch_flight",
			logKeyCacheKey, cacheKey,
		}
		if deadline, ok := ctx.Deadline(); ok {
			attrs = append(attrs, resolveBoundLogAttrs(time.Until(deadline))...)
		}
		waitLogger.InfoContext(ctx, "virtual resolve wait started", attrs...)
	}
	select {
	case <-flight.done:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	if waitLogger != nil {
		elapsed := time.Since(started)
		attrs := []any{
			"component", "virtuallibrary",
			"wait_kind", "fetch",
			"wait_on", "provider_fetch_flight",
			logKeyCacheKey, cacheKey,
			"waited_ms", elapsed.Milliseconds(),
		}
		if elapsed >= backgroundRefreshTimeout {
			waitLogger.WarnContext(ctx, "virtual resolve wait exceeded threshold", attrs...)
		} else {
			waitLogger.DebugContext(ctx, "virtual resolve wait finished", attrs...)
		}
	}
	// The fetch result is published before done closes, so this read is
	// ordered after the write. A failed flight must surface its error rather
	// than let the cache inspection below return a stale positive entry as if
	// the fetch had succeeded.
	if flight.err != nil {
		return nil, false, flight.err
	}
	r.cacheMu.Lock()
	fresh, stillOK := r.cache[cacheKey]
	r.cacheMu.Unlock()
	if !stillOK {
		return nil, false, nil
	}
	now := time.Now()
	withinGrace := !now.After(fresh.expiresAt.Add(candidateStaleGrace))
	switch {
	case len(fresh.candidates) > 0 && withinGrace:
		// Positive results stay servable through the same stale grace the
		// direct tiers use. A successful empty answer deliberately preserves
		// this entry, so the waiters reuse the concurrent result rather than
		// forcing another fetch.
		return cloneCandidates(fresh.candidates), true, nil
	case len(fresh.candidates) == 0 && now.Before(fresh.expiresAt):
		// Negative-cache hit: the flight already proved the title is
		// unavailable, so waiting callers must not re-pay the round-trip.
		// Negatives never outlive their own short TTL.
		return cloneCandidates(fresh.candidates), true, nil
	}
	return nil, false, nil
}

// joinFlight registers this caller as a synchronous provider fetcher if no
// other flight is active for the key. It returns the flight to wait on. The
// fetch runs on a background goroutine and publishes its provider error on the
// flight before closing it, so every waiter can distinguish a failed fetch
// from a successful one. The detached fetch context preserves the caller's
// request values (including the edge request id for the fetch log) while
// carrying its own timeout, so a canceled caller does not kill the shared
// fetch its waiters still need. Callers must NOT hold cacheMu.
func (r *Resolver) joinFlight(ctx context.Context, cacheKey string, config Config, generation uint64, mediaType, mediaID string, reason FetchReason) *candidateFlight {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.refreshes == nil {
		r.refreshes = make(map[string]chan struct{})
	}
	if r.syncFlights == nil {
		r.syncFlights = make(map[string]*candidateFlight)
	}
	if _, inflight := r.refreshes[cacheKey]; inflight {
		flight, exists := r.syncFlights[cacheKey]
		if !exists {
			flight = &candidateFlight{done: make(chan struct{})}
			r.syncFlights[cacheKey] = flight
		}
		return flight
	}
	flight := &candidateFlight{done: make(chan struct{})}
	r.refreshes[cacheKey] = flight.done
	r.syncFlights[cacheKey] = flight
	go func() {
		defer func() {
			r.cacheMu.Lock()
			delete(r.refreshes, cacheKey)
			delete(r.syncFlights, cacheKey)
			r.cacheMu.Unlock()
			close(flight.done)
		}()
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), syncFetchTimeout)
		defer cancel()
		_, err := r.fetchProviderCandidates(fetchCtx, config, generation, cacheKey, mediaType, mediaID, reason)
		flight.err = err
		if err != nil {
			r.recordProviderFailure(cacheKey)
			r.debugLog("synchronous candidate fetch failed", cacheKey, 0)
			return
		}
		r.debugLog("synchronous candidate fetch complete", cacheKey, 0)
	}()
	return flight
}

// startRefreshLocked launches exactly one background provider fetch per
// cache key. Callers must hold cacheMu; the spawned goroutine replaces the
// cache entry through the normal generation-checked path.
func (r *Resolver) startRefreshLocked(cacheKey string, generation uint64, config Config, mediaType, mediaID string) bool {
	if _, inflight := r.refreshes[cacheKey]; inflight {
		return false
	}
	if r.refreshes == nil {
		r.refreshes = make(map[string]chan struct{})
	}
	if r.syncFlights == nil {
		r.syncFlights = make(map[string]*candidateFlight)
	}
	flight := &candidateFlight{done: make(chan struct{})}
	r.refreshes[cacheKey] = flight.done
	r.syncFlights[cacheKey] = flight
	go func() {
		defer func() {
			r.cacheMu.Lock()
			delete(r.refreshes, cacheKey)
			delete(r.syncFlights, cacheKey)
			r.cacheMu.Unlock()
			close(flight.done)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), backgroundRefreshTimeout)
		defer cancel()
		_, err := r.fetchProviderCandidates(ctx, config, generation, cacheKey, mediaType, mediaID, FetchReasonBackground)
		flight.err = err
		if err != nil {
			r.recordProviderFailure(cacheKey)
			r.debugLog("background candidate refresh failed", cacheKey, 0)
			return
		}
		r.debugLog("background candidate refresh complete", cacheKey, 0)
	}()
	return true
}

// fetchProviderCandidates performs a synchronous streaming-provider lookup
// and caches successful results. Provider URLs are never logged. reason names
// the path that triggered the fetch for the fetch log and any threaded
// ProviderFetchObserver; it does not change behavior.
func (r *Resolver) fetchProviderCandidates(ctx context.Context, config Config, generation uint64, cacheKey, mediaType, mediaID string, reason FetchReason) (candidates []StreamCandidate, err error) {
	started := time.Now()
	r.mu.RLock()
	fetchLogger := r.logger
	r.mu.RUnlock()
	// Report exactly one event per attempt, success or failure, so an observer
	// counts real provider discovery rather than cache serves. The request id
	// is read once here, before any early return.
	event := ProviderFetchEvent{CacheKey: cacheKey, Reason: reason, RequestID: r.fetchRequestID(ctx)}
	defer func() {
		event.Count = len(candidates)
		event.Duration = time.Since(started)
		event.Err = err
		reportProviderFetch(ctx, event)
		if fetchLogger != nil && event.Duration >= backgroundRefreshTimeout {
			attrs := []any{
				"media_type", mediaType, "media_id", mediaID,
				"count", event.Count,
				"duration_ms", event.Duration.Milliseconds(),
				logKeyCacheKey, cacheKey,
				logKeyReason, string(reason),
			}
			if event.RequestID != "" {
				attrs = append(attrs, "request_id", event.RequestID)
			}
			fetchLogger.WarnContext(ctx, "provider candidate fetch exceeded the background refresh budget", attrs...)
		}
	}()
	endpoint, err := streamEndpointWithPolicy(config.ManifestURL, mediaType, mediaID, config.AllowInsecure)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create streaming provider request: %w", err)
	}
	r.mu.RLock()
	enricher := r.enricher
	r.mu.RUnlock()
	client := r.client
	if client == nil {
		client = http.DefaultClient
	}
	// The provider round-trip is the silent leg of a listing: the completion
	// line below is the next thing logged. Announce the fetch once, with the
	// caller's remaining deadline, so an operator can tell a listing wait from
	// an ffprobe or an import hold while the start is still running.
	if fetchLogger != nil {
		attrs := []any{
			"media_type", mediaType, "media_id", mediaID,
			logKeyCacheKey, cacheKey,
			logKeyReason, string(reason),
		}
		if deadline, ok := ctx.Deadline(); ok {
			attrs = append(attrs, resolveBoundLogAttrs(time.Until(deadline))...)
		}
		if event.RequestID != "" {
			attrs = append(attrs, "request_id", event.RequestID)
		}
		fetchLogger.InfoContext(ctx, "provider candidate fetch started", attrs...)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: request streaming provider failed", ErrProviderUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: streaming provider returned status %d", ErrProviderUnavailable, resp.StatusCode)
	}
	var payload stremioResponse
	if err := decodeBoundedJSON(resp.Body, maxResponseBytes, &payload); err != nil {
		return nil, fmt.Errorf("decode streaming provider response: %w", err)
	}
	validCandidates := make([]StreamCandidate, 0, len(payload.Streams))
	for i, stream := range payload.Streams {
		parsed, parseErr := url.Parse(strings.TrimSpace(stream.URL))
		if parseErr == nil && parsed.IsAbs() && (parsed.Scheme == "https" || parsed.Scheme == "http") {
			// Some providers answer unavailable titles with a placeholder
			// entry instead of an empty list. Persisting or ranking it makes
			// every start pay a doomed probe and pollutes the catalog with
			// ghost variants — drop it at ingestion.
			if isProviderStubCandidate(stream) {
				continue
			}
			stream.OriginalIndex = i
			enrich := enricher
			if enrich == nil {
				enrich = DefaultEnricher
			}
			enrich(&stream)
			validCandidates = append(validCandidates, stream)
			if len(validCandidates) >= maxProviderCandidates {
				break
			}
		}
	}
	// Cache the provider's raw, enriched answer. The ingestion pipeline
	// (badge + classify -> drop SourceFailed -> dedupe -> confirmed-first
	// partition -> cap) runs exactly once per answer in
	// getCandidatesWithKeepers, so a classifier state change lands immediately
	// and the classifier is not run twice. Dedup still runs before the
	// selectable cap on every answer, so one multi-file release cannot flood
	// the list, and a non-empty all-failed answer is cached as data rather than
	// as an ordinary negative entry.
	now := time.Now()
	r.storeCandidateCache(cacheKey, validCandidates, now.Add(config.CacheTTL), now, generation)
	// The provider answered, so any recorded failure for this key is stale.
	r.clearProviderFailure(cacheKey)
	r.mu.RLock()
	logger := r.logger
	r.mu.RUnlock()
	if logger != nil {
		attrs := []any{
			"media_type", mediaType, "media_id", mediaID,
			"count", len(validCandidates),
			"duration_ms", time.Since(started).Milliseconds(),
			logKeyCacheKey, cacheKey,
			logKeyReason, string(reason),
		}
		if event.RequestID != "" {
			attrs = append(attrs, "request_id", event.RequestID)
		}
		logger.InfoContext(ctx, "provider candidates fetched", attrs...)
	}
	return validCandidates, nil
}

// isProviderStubCandidate reports whether a provider stream entry is the
// conventional "nothing found" placeholder rather than a playable source.
// Addons signal this via the entry's display text; the URL itself usually
// still looks plausible, so it must be checked before ingestion.
func isProviderStubCandidate(s StreamCandidate) bool {
	hay := strings.ToLower(strings.Join([]string{s.Name, s.Title, s.Description}, " \n "))
	for _, marker := range []string{
		"no streams available",
		"no streams found",
		"nothing found",
		"no results",
	} {
		if strings.Contains(hay, marker) {
			return true
		}
	}
	return false
}

func (r *Resolver) debugLog(msg, cacheKey string, count int) {
	r.mu.RLock()
	logger := r.logger
	r.mu.RUnlock()
	if logger == nil {
		return
	}
	logger.Debug(msg, "cache_key", cacheKey, "count", count)
}

func candidateCacheSize(candidates []StreamCandidate) int64 {
	var size int64
	for _, candidate := range candidates {
		size += 256
		for _, value := range []string{
			candidate.URL, candidate.Name, candidate.Title, candidate.Description,
			candidate.Resolution, candidate.CodecVideo, candidate.CodecAudio,
			candidate.HDR, candidate.SourceType, candidate.Container, candidate.BehaviorHints.VideoHash,
		} {
			size += int64(len(value))
		}
		for _, language := range candidate.AudioLanguages {
			size += int64(len(language))
		}
		for _, language := range candidate.SubtitleLanguages {
			size += int64(len(language))
		}
	}
	return size
}

func (r *Resolver) storeCandidateCache(key string, candidates []StreamCandidate, expiresAt, now time.Time, generation uint64) {
	size := int64(0)
	if len(candidates) == 0 {
		// Negative caching: a genuinely EMPTY provider answer is stored
		// briefly so repeated resolves of an unavailable title fail in
		// microseconds instead of paying another 3-8s round-trip each. The
		// short TTL keeps newly added sources discoverable without operator
		// action. A non-empty answer whose releases the classifier all rejects
		// is deliberately never treated this way: it reaches here non-empty and
		// is cached as data, so classification re-runs on every serve and the
		// title recovers the moment the classifier changes rather than after a
		// two-minute blackhole.
		expiresAt = now.Add(negativeCacheTTL)
	} else {
		size = candidateCacheSize(candidates)
		if size > maxCandidateCacheBytes {
			return
		}
	}
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.cacheGeneration != generation {
		return
	}
	if len(candidates) == 0 {
		// The upstream provider flaps between a full list and an empty/stub
		// answer. Letting the empty answer replace a still-servable positive
		// entry starves playback for the whole negative TTL, so keep the
		// positive entry (and its lastAccess) until it leaves stale grace, and
		// only then install the negative. No usable positive means the
		// negative is installed as before.
		if previous, exists := r.cache[key]; exists && len(previous.candidates) > 0 &&
			now.Before(previous.expiresAt.Add(candidateStaleGrace)) {
			return
		}
	}
	if r.cache == nil {
		r.cache = make(map[string]candidateCacheEntry)
	}
	if previous, exists := r.cache[key]; exists {
		r.cacheBytes -= previous.sizeBytes
		delete(r.cache, key)
	}
	for candidateKey, entry := range r.cache {
		if !now.Before(entry.expiresAt) {
			r.cacheBytes -= entry.sizeBytes
			delete(r.cache, candidateKey)
		}
	}
	for len(r.cache) >= maxCandidateCacheEntries || r.cacheBytes+size > maxCandidateCacheBytes {
		oldestKey := ""
		var oldest time.Time
		for candidateKey, entry := range r.cache {
			if oldestKey == "" || entry.lastAccess.Before(oldest) {
				oldestKey, oldest = candidateKey, entry.lastAccess
			}
		}
		if oldestKey == "" {
			break
		}
		r.cacheBytes -= r.cache[oldestKey].sizeBytes
		delete(r.cache, oldestKey)
	}
	r.cache[key] = candidateCacheEntry{
		candidates: cloneCandidates(candidates),
		expiresAt:  expiresAt,
		fetchedAt:  now,
		lastAccess: now,
		sizeBytes:  size,
	}
	r.cacheBytes += size
}

// recordProviderFailure remembers that the provider listing for cacheKey
// failed. Within providerFailureBackoff, getCandidatesRaw fails fast instead of
// re-paying the provider timeout. The map is pruned on every write and bounded
// like the candidate cache.
func (r *Resolver) recordProviderFailure(cacheKey string) {
	now := time.Now()
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.failures == nil {
		r.failures = make(map[string]time.Time)
	}
	for key, at := range r.failures {
		if now.Sub(at) >= providerFailureBackoff {
			delete(r.failures, key)
		}
	}
	for len(r.failures) >= maxCandidateCacheEntries {
		oldestKey := ""
		var oldest time.Time
		for key, at := range r.failures {
			if oldestKey == "" || at.Before(oldest) {
				oldestKey, oldest = key, at
			}
		}
		if oldestKey == "" {
			break
		}
		delete(r.failures, oldestKey)
	}
	r.failures[cacheKey] = now
}

// clearProviderFailure forgets a key's provider failure, so a later resolve is
// free to fetch again. Called whenever a fetch produces an answer, including a
// genuine empty one.
func (r *Resolver) clearProviderFailure(cacheKey string) {
	r.cacheMu.Lock()
	delete(r.failures, cacheKey)
	r.cacheMu.Unlock()
}

// providerFailureActive reports whether cacheKey failed recently enough that
// another full-timeout fetch should be skipped. An entry older than the
// backoff is pruned and reported inactive.
func (r *Resolver) providerFailureActive(cacheKey string) bool {
	now := time.Now()
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	at, ok := r.failures[cacheKey]
	if !ok {
		return false
	}
	if now.Sub(at) >= providerFailureBackoff {
		delete(r.failures, cacheKey)
		return false
	}
	return true
}

func (r *Resolver) normalizeTMDBProviderID(ctx context.Context, mediaType, mediaID, apiKey, baseURL string) (string, error) {
	parts := strings.Split(mediaID, ":")
	if len(parts) < 2 || !strings.EqualFold(parts[0], "tmdb") {
		return mediaID, nil
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return "", errors.New("TMDB ID requires a configured TMDB API token to resolve IMDb playback ID")
	}
	cacheKey := strings.ToLower(mediaType) + "|" + parts[1]
	if imdbID, ok := r.cachedTMDBExternalID(cacheKey); ok {
		if imdbID == "" {
			return "", fmt.Errorf("TMDB ID %s has no IMDb playback ID", parts[1])
		}
		return tmdbProviderID(imdbID, parts), nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, tmdbExternalIDTimeout)
	defer cancel()
	externals, err := fetchTMDBExternalIDs(lookupCtx, mediaType, parts[1], key, baseURL)
	if err != nil {
		// A failed lookup is not evidence that the title has no IMDb id: TMDB
		// may be down, rate-limiting, or slow. Do not cache it as a negative
		// (that would make the title unplayable for the negative TTL with a
		// misleading message), and report it as the retryable provider failure
		// it is.
		return "", fmt.Errorf("TMDB ID %s external-id lookup failed: %w", parts[1],
			errors.Join(ErrTMDBExternalIDLookupFailed, ErrProviderUnavailable))
	}
	imdbID := strings.TrimSpace(externals.IMDbID)
	if imdbID == "" {
		r.storeTMDBExternalID(cacheKey, "", tmdbExternalIDNegativeTTL)
		return "", fmt.Errorf("TMDB ID %s has no IMDb playback ID", parts[1])
	}
	r.storeTMDBExternalID(cacheKey, imdbID, tmdbExternalIDCacheTTL)
	return tmdbProviderID(imdbID, parts), nil
}

// tmdbProviderID rejoins a translated IMDb id with any season/episode suffix
// the original tmdb virtual id carried, so a series path keeps its episode
// coordinates.
func tmdbProviderID(imdbID string, parts []string) string {
	if len(parts) > 2 {
		return imdbID + ":" + strings.Join(parts[2:], ":")
	}
	return imdbID
}

// cachedTMDBExternalID returns the cached translation for key, or false when
// none is live. An empty imdbID with true is a live negative entry.
func (r *Resolver) cachedTMDBExternalID(key string) (string, bool) {
	now := time.Now()
	r.tmdbMu.Lock()
	defer r.tmdbMu.Unlock()
	entry, ok := r.tmdbCache[key]
	if !ok {
		return "", false
	}
	if !now.Before(entry.expiresAt) {
		delete(r.tmdbCache, key)
		return "", false
	}
	return entry.imdbID, true
}

// storeTMDBExternalID records a translation under key. imdbID is empty for a
// negative result. The map is bounded like the candidate cache so an
// adversarial id stream cannot grow it without limit.
func (r *Resolver) storeTMDBExternalID(key, imdbID string, ttl time.Duration) {
	now := time.Now()
	r.tmdbMu.Lock()
	defer r.tmdbMu.Unlock()
	if r.tmdbCache == nil {
		r.tmdbCache = make(map[string]tmdbCacheEntry)
	}
	for existingKey, entry := range r.tmdbCache {
		if !now.Before(entry.expiresAt) {
			delete(r.tmdbCache, existingKey)
		}
	}
	for len(r.tmdbCache) >= maxCandidateCacheEntries {
		oldestKey := ""
		var oldest time.Time
		for existingKey, entry := range r.tmdbCache {
			if oldestKey == "" || entry.expiresAt.Before(oldest) {
				oldestKey, oldest = existingKey, entry.expiresAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(r.tmdbCache, oldestKey)
	}
	r.tmdbCache[key] = tmdbCacheEntry{imdbID: imdbID, expiresAt: now.Add(ttl)}
}

// ValidateConnection checks the manifest URL policy and fetches the provider
// manifest, verifying it advertises the Stremio stream resource for movies or
// series. It mirrors the provider phase of the plugin's TestConnection; the
// Prowlarr/AltMount phases are host concerns and are not ported.
func (r *Resolver) ValidateConnection(ctx context.Context) error {
	r.mu.RLock()
	manifestURL := r.config.ManifestURL
	allowInsecure := r.config.AllowInsecure
	r.mu.RUnlock()
	if _, err := streamEndpointWithPolicy(manifestURL, "movie", "tt0000001", allowInsecure); err != nil {
		return err
	}
	// Use a short-timeout clone of the provider client so validation can't
	// exhaust the caller's deadline. Copy the transport (so mocks and
	// redirect policies still work) but cap the round-trip at 5s.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return fmt.Errorf("create manifest validation request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	var client *http.Client
	if r.client != nil {
		client = &http.Client{Timeout: 5 * time.Second, Transport: r.client.Transport, CheckRedirect: r.client.CheckRedirect}
	} else {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("request streaming provider manifest failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("streaming provider manifest returned status %d", resp.StatusCode)
	}
	var manifest stremioManifest
	if err := decodeBoundedJSON(resp.Body, maxManifestResponseBytes, &manifest); err != nil {
		return fmt.Errorf("decode streaming provider manifest: %w", err)
	}
	return validateStremioManifest(manifest)
}

func validateStremioManifest(manifest stremioManifest) error {
	if strings.TrimSpace(manifest.ID) == "" {
		return errors.New("streaming provider manifest is missing id")
	}
	hasStreamResource := false
	for _, raw := range manifest.Resources {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			var descriptor struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &descriptor); err == nil {
				name = descriptor.Name
			}
		}
		if strings.EqualFold(strings.TrimSpace(name), "stream") {
			hasStreamResource = true
			break
		}
	}
	if !hasStreamResource {
		return errors.New("streaming provider manifest does not advertise the stream resource")
	}
	for _, mediaType := range manifest.Types {
		if strings.EqualFold(strings.TrimSpace(mediaType), "movie") || strings.EqualFold(strings.TrimSpace(mediaType), "series") {
			return nil
		}
	}
	return errors.New("streaming provider manifest does not advertise movie or series support")
}

func decodeBoundedJSON(body io.Reader, limit int64, destination any) error {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("response exceeds %d bytes", limit)
	}
	return json.Unmarshal(data, destination)
}

func newProviderHTTPClient() *http.Client {
	return newRestrictedRedirectHTTPClient(defaultHTTPTimeout)
}

// sameParentDomain reports whether host a and host b share at least two
// rightmost domain labels (e.g. "v3-cinemeta.strem.io" and
// "cinemeta-live.strem.io" both end with ".strem.io").
func sameParentDomain(a, b string) bool {
	aParts := strings.Split(strings.TrimSuffix(a, "."), ".")
	bParts := strings.Split(strings.TrimSuffix(b, "."), ".")
	if len(aParts) < 3 || len(bParts) < 3 {
		return false
	}
	aLast := strings.ToLower(strings.Join(aParts[len(aParts)-2:], "."))
	bLast := strings.ToLower(strings.Join(bParts[len(bParts)-2:], "."))
	return aLast == bLast
}

func newRestrictedRedirectHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if len(via) == 0 {
				return nil
			}
			origin := via[0].URL
			target := request.URL
			if target.User != nil {
				return errors.New("redirect with userinfo is not allowed")
			}
			if !strings.EqualFold(origin.Scheme, target.Scheme) {
				return errors.New("cross-scheme redirects are not allowed")
			}
			if strings.EqualFold(origin.Host, target.Host) {
				return nil
			}
			// Allow same-registered-domain redirects so well-known
			// metadata providers redirect within their own domain.
			if sameParentDomain(origin.Host, target.Host) {
				return nil
			}
			return errors.New("cross-origin redirects are not allowed")
		},
	}
}

func (r *Resolver) normalizeSeriesProviderID(ctx context.Context, mediaID, baseURL string) (string, error) {
	parts := strings.Split(mediaID, ":")
	if len(parts) < 2 || !strings.EqualFold(parts[0], "tvdb") {
		return mediaID, nil
	}
	tvdbID := strings.TrimSpace(parts[1])
	if tvdbID == "" {
		return "", errors.New("TVDB series ID is empty")
	}
	lookupURL := strings.TrimRight(baseURL, "/") + "/lookup/shows?thetvdb=" + url.QueryEscape(tvdbID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		return "", fmt.Errorf("create TVDB series lookup: %w", err)
	}
	client := r.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("lookup TVDB series ID %s: %w", tvdbID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("TVMaze returned status %d for TVDB series ID %s", resp.StatusCode, tvdbID)
	}
	var payload struct {
		Externals struct {
			IMDb string `json:"imdb"`
		} `json:"externals"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode TVDB series lookup: %w", err)
	}
	imdbID := strings.TrimSpace(payload.Externals.IMDb)
	if imdbID == "" {
		return "", fmt.Errorf("TVDB series ID %s has no IMDb ID for Stremio playback", tvdbID)
	}
	if len(parts) == 2 {
		return imdbID, nil
	}
	return imdbID + ":" + strings.Join(parts[2:], ":"), nil
}

// prowlarrCleanPattern reduces a release name to a comparable identity:
// lowercase, extension stripped, all non-alphanumerics removed. Two postings
// of the same scene release normalize to the same key even when one uses dots
// and the other spaces.
var prowlarrCleanPattern = regexp.MustCompile(`[^a-z0-9]+`)

// releaseNameKey reduces a release or provider filename to a comparable
// identity: lowercase, extension stripped, all non-alphanumerics removed. Two
// postings of the same scene release normalize to the same key even when one
// uses dots and the other spaces.
func releaseNameKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if idx := strings.IndexAny(value, "?#"); idx != -1 {
		value = value[:idx]
	}
	for _, ext := range []string{".mkv", ".mp4", ".avi", ".ts", ".m2ts", ".mov", ".webm"} {
		value = strings.TrimSuffix(value, ext)
	}
	return prowlarrCleanPattern.ReplaceAllString(value, "")
}

// firstReleaseLine returns the first line of a provider display field.
// AltMount-style providers put the release name on the first line of the
// stream title (the remaining lines carry size and indexer badges), so only
// the first line is considered for release identity.
func firstReleaseLine(value string) string {
	if idx := strings.IndexAny(value, "\r\n"); idx >= 0 {
		return value[:idx]
	}
	return value
}

// markAltmountBadgeCandidates honors the completion badge AltMount's Stremio
// addon already puts on imported, fresh releases. This needs no API
// configuration and lets the resolver respect AltMount's cached-first ordering
// instead of re-sorting it away.
func markAltmountBadgeCandidates(candidates []StreamCandidate) {
	for i := range candidates {
		name := strings.ToLower(candidates[i].Name)
		if strings.Contains(name, altmountCachedBadge) {
			candidates[i].SourceConfirmed = true
		}
	}
}

func parseVirtualPath(virtualPath string) (string, string, error) {
	if !strings.HasPrefix(virtualPath, virtualPathPrefix) {
		return "", "", errors.New("path is not an virtual URI")
	}
	cleanPath := virtualPath
	if idx := strings.Index(cleanPath, "?"); idx != -1 {
		cleanPath = cleanPath[:idx]
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(cleanPath, virtualPathPrefix), "/"), "/")
	if len(parts) < 2 {
		return "", "", errors.New("virtual URI must contain a media type and identifier")
	}
	mediaType := strings.ToLower(parts[0])
	if mediaType != "movie" && mediaType != "series" && mediaType != "anime" {
		return "", "", fmt.Errorf("unsupported virtual media type %q", mediaType)
	}
	mediaID := strings.Join(parts[1:], ":")
	if strings.ContainsAny(mediaID, "?#") || strings.Contains(mediaID, "..") {
		return "", "", errors.New("virtual URI contains an invalid identifier")
	}
	return mediaType, mediaID, nil
}

func streamEndpoint(manifestURL, mediaType, mediaID string) (string, error) {
	return streamEndpointWithPolicy(manifestURL, mediaType, mediaID, false)
}

func streamEndpointWithPolicy(manifestURL, mediaType, mediaID string, allowInsecure bool) (string, error) {
	manifest, err := url.Parse(strings.TrimSpace(manifestURL))
	if err != nil || manifest.Host == "" || (manifest.Scheme != "https" && manifest.Scheme != "http") || (manifest.Scheme != "https" && !allowInsecure) {
		return "", errors.New("a valid streaming provider manifest URL is required (HTTPS, or HTTP with Allow HTTP for local manifests enabled for private/local hosts)")
	}
	if manifest.Scheme == "http" && !isPrivateHost(manifest.Hostname()) {
		return "", errors.New("insecure HTTP is allowed only for private/local streaming provider hosts")
	}
	if !strings.HasSuffix(manifest.Path, "/manifest.json") {
		return "", errors.New("streaming provider URL must end in /manifest.json")
	}
	manifest.Path = strings.TrimSuffix(manifest.Path, "/manifest.json") + "/stream/" + url.PathEscape(mediaType) + "/" + url.PathEscape(mediaID) + ".json"
	manifest.RawQuery = ""
	manifest.Fragment = ""
	return manifest.String(), nil
}

func isPrivateHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	// Single-label names are normally Docker/Kubernetes service names (for
	// example "virtual" or "altmount") and are not public DNS names.
	if host == "localhost" || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	return false
}

// metadataHTTPClient serves the TMDB external-ID lookups (mirrors the shared
// metadata client in the plugin's routing.go).
var metadataHTTPClient = newRestrictedRedirectHTTPClient(20 * time.Second)

// tmdbExternalIDTimeout bounds one TMDB external-ID translation. The shared
// metadata client allows 20s and this lookup runs before the candidate-cache
// singleflight, so nothing downstream bounds it; a slow TMDB would hold the
// whole resolve. It is a var so tests can shorten it without waiting seconds.
var tmdbExternalIDTimeout = 5 * time.Second

type tmdbExternalIDs struct {
	IMDbID string `json:"imdb_id"`
	TVDBID int    `json:"tvdb_id"`
}

func fetchTMDBExternalIDs(ctx context.Context, mediaType, tmdbID, key, baseURL string) (tmdbExternalIDs, error) {
	kind := "tv"
	if mediaType == "movie" {
		kind = "movie"
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/" + kind + "/" + url.PathEscape(tmdbID) + "/external_ids"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if strings.Count(key, ".") == 2 {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		q := req.URL.Query()
		q.Set("api_key", key)
		req.URL.RawQuery = q.Encode()
	}
	resp, err := metadataHTTPClient.Do(req)
	if err != nil {
		return tmdbExternalIDs{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return tmdbExternalIDs{}, fmt.Errorf("TMDB external_ids HTTP %d", resp.StatusCode)
	}
	var out tmdbExternalIDs
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return tmdbExternalIDs{}, err
	}
	return out, nil
}
