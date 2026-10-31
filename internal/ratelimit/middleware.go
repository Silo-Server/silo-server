package ratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

// Middleware manages rate limiting config, limiters, and the HTTP handler.
type Middleware struct {
	reloadMu sync.Mutex // serializes reading and applying reload snapshots
	mu       sync.RWMutex
	cfg      Config
	perKey   RateLimiter
	global   RateLimiter
	quota    QuotaLimiter
	store    SettingsStore
	isMemory bool
	// overQuotaLogged holds, per session, when the session quota last logged
	// it, so a session over the quota logs once per overQuotaLogEvery.
	overQuotaLogged sync.Map
}

// NewMiddleware creates the rate limit middleware. Session quotas share the
// per-key limiter's backend: Redis when it counts in Redis, else this process.
func NewMiddleware(perKey RateLimiter, global RateLimiter, store SettingsStore, isMemory bool) *Middleware {
	var quota QuotaLimiter
	if rl, ok := perKey.(*RedisLimiter); ok {
		quota = NewRedisQuotaLimiter(rl)
	} else {
		quota = NewMemoryQuotaLimiter()
	}
	return &Middleware{
		perKey:   perKey,
		global:   global,
		quota:    quota,
		store:    store,
		isMemory: isMemory,
	}
}

// ActiveBackend reports which limiter backend this process is actually
// running, as opposed to the configured backend, which only takes effect
// after a restart.
func (mw *Middleware) ActiveBackend() string {
	if mw.isMemory {
		return "memory"
	}
	return "redis"
}

// SharedLimiter returns the process's configured per-key limiter, so an action
// handler that enforces its own budget (person refresh, trailer refresh) counts
// against the same backend the middleware uses rather than a private in-memory
// one. That distinction only matters on Redis deployments, where a private
// limiter would give every instance an independent allowance for the same user
// and multiply the stated budget by the instance count.
//
// Handlers must tolerate nil: rate limiting is disabled outright when
// rate_limit.enabled is false or the database is unavailable, and no limiter
// exists then.
func (mw *Middleware) SharedLimiter() RateLimiter {
	if mw == nil {
		return nil
	}
	return mw.perKey
}

// Init loads config and seeds defaults. Call once at startup.
func (mw *Middleware) Init(ctx context.Context) error {
	if err := SeedDefaults(ctx, mw.store); err != nil {
		return err
	}
	return mw.Reload(ctx)
}

// Reload re-reads config from server_settings and clears the in-memory
// per-second and per-minute state. Session quota buckets are kept: they read
// their size and rate on every take, and clearing them on each settings change
// would hand every session a fresh hour.
func (mw *Middleware) Reload(ctx context.Context) error {
	mw.reloadMu.Lock()
	defer mw.reloadMu.Unlock()
	cfg, err := LoadConfig(ctx, mw.store)
	if err != nil {
		return err
	}
	mw.mu.Lock()
	mw.cfg = cfg
	mw.mu.Unlock()

	if mw.isMemory {
		if ml, ok := mw.perKey.(*MemoryLimiter); ok {
			ml.Clear()
		}
		if ml, ok := mw.global.(*MemoryLimiter); ok {
			ml.Clear()
		}
	}

	slog.InfoContext(ctx, "rate limit config reloaded", "component", "ratelimit", "enabled", cfg.Enabled, "global_rps", cfg.GlobalReqPerSecond)
	return nil
}

// RejectBodyWriter writes the status line and body of a rate-limited
// response. The middleware has already set Retry-After and the X-RateLimit-*
// headers; retryAfterSeconds is the Retry-After value. A secondary listener
// whose clients expect a different error envelope supplies its own writer so
// it can share this middleware's budgets and counters.
type RejectBodyWriter func(w http.ResponseWriter, retryAfterSeconds int)

// Handler returns the chi-compatible middleware handler.
func (mw *Middleware) Handler(next http.Handler) http.Handler {
	return mw.HandlerWithRejectBody(writeRateLimitBody)(next)
}

// HandlerWithRejectBody is Handler with a caller-supplied 429 body. Budgets,
// keys, and counters are this middleware's, so requests spend the same
// allowance whichever writer rejects them.
func (mw *Middleware) HandlerWithRejectBody(reject RejectBodyWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return mw.handler(next, reject)
	}
}

func (mw *Middleware) handler(next http.Handler, reject RejectBodyWriter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mw.mu.RLock()
		cfg := mw.cfg
		mw.mu.RUnlock()

		if !cfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		// The narrower per-IP, per-key and account budgets run before the shared
		// global one, as in AuthEndpointHandler, so a client over its own
		// limit cannot spend server-wide capacity and get unrelated clients
		// rejected. A request the global check then rejects keeps the
		// narrower tokens it spent; that only charges the client that sent it.

		// Check per-IP limiter
		if clientIP := clientip.FromContext(r.Context()); clientIP != "" {
			ipRate := Rate{
				RequestsPerSecond: cfg.IPReqPerSecond,
				RequestsPerMinute: cfg.IPReqPerMinute,
				Burst:             cfg.IPBurst,
			}
			ipKey := "ip:" + SourceKey(clientIP)
			ipResult := mw.perKey.Allow(r.Context(), ipKey, ipRate)
			if !ipResult.Allowed {
				writeRateLimitResponse(w, ipResult, reject)
				return
			}
		}

		// Check per-key limiter (API key auth only)
		claims := apimw.GetClaims(r.Context())
		if claims != nil && claims.TokenType == auth.TokenTypeAPIKey && claims.APIKeyID != 0 {
			// RateTier is already on the claims — no DB lookup needed
			tier := claims.RateTier
			if tier == "" {
				tier = "standard"
			}
			tierCfg, ok := cfg.Tiers[tier]
			if !ok {
				tierCfg = cfg.Tiers["standard"]
			}

			keyRate := Rate{
				RequestsPerSecond: tierCfg.RequestsPerSecond,
				RequestsPerMinute: tierCfg.RequestsPerMinute,
				Burst:             tierCfg.Burst,
			}
			key := fmt.Sprintf("key:%d", claims.APIKeyID)
			result := mw.perKey.Allow(r.Context(), key, keyRate)

			// Set rate limit headers on every API-key-authenticated response
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(result.Limit))
			if result.Remaining >= 0 {
				w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))
			}
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(result.ResetAt.Unix(), 10))

			if !result.Allowed {
				writeRateLimitResponse(w, result, reject)
				return
			}
		}

		if !mw.allowAccount(w, r, cfg, reject) {
			return
		}
		if !mw.allowSession(w, r, cfg.Session, reject) {
			return
		}

		// Check global limiter (per-second only — spec says no per-minute global limit).
		// Setting RequestsPerMinute = RPS*60 makes the per-minute limiter a mathematical
		// no-op: it never triggers before the per-second limiter does.
		globalResult := mw.global.Allow(r.Context(), "global", globalRateFor(cfg))
		if !globalResult.Allowed {
			writeRateLimitResponse(w, globalResult, reject)
			return
		}

		next.ServeHTTP(w, r)
	})
}

type rateLimitError struct {
	Error      string `json:"error"`
	Message    string `json:"message"`
	RetryAfter int    `json:"retry_after"`
}

// AuthEndpointHandler returns middleware for public auth/webhook endpoints. It
// applies the shared per-IP and endpoint-specific budgets before consuming the
// process-wide budget, so one rejected client cannot drain capacity for others.
func (mw *Middleware) AuthEndpointHandler(endpoint string) func(http.Handler) http.Handler {
	return mw.AuthEndpointHandlerWithRejectBody(endpoint, writeRateLimitBody)
}

// AuthEndpointHandlerWithRejectBody is AuthEndpointHandler with a
// caller-supplied 429 body; the endpoint budget and its counters are shared.
func (mw *Middleware) AuthEndpointHandlerWithRejectBody(endpoint string, reject RejectBodyWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mw.mu.RLock()
			cfg := mw.cfg
			mw.mu.RUnlock()

			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			clientIP := clientip.FromContext(r.Context())
			if clientIP != "" {
				source := SourceKey(clientIP)
				// Check global per-IP limit (shared counter with authenticated routes)
				ipRate := Rate{
					RequestsPerSecond: cfg.IPReqPerSecond,
					RequestsPerMinute: cfg.IPReqPerMinute,
					Burst:             cfg.IPBurst,
				}
				ipResult := mw.perKey.Allow(r.Context(), "ip:"+source, ipRate)
				if !ipResult.Allowed {
					writeRateLimitResponse(w, ipResult, reject)
					return
				}

				// Check per-endpoint limit
				epCfg, ok := cfg.AuthEndpoints[endpoint]
				if ok {
					epRate := Rate{
						RequestsPerSecond: epCfg.RequestsPerMinute / 60,
						RequestsPerMinute: epCfg.RequestsPerMinute,
						Burst:             epCfg.Burst,
					}
					epKey := fmt.Sprintf("authip:%s:%s", source, endpoint)
					epResult := mw.perKey.Allow(r.Context(), epKey, epRate)
					if !epResult.Allowed {
						writeRateLimitResponse(w, epResult, reject)
						return
					}

					// A client IP taken from forwarding headers is a new
					// budget whenever the sender names a new client, and every
					// private address is a trusted proxy by default. The
					// connection's own address also spends a looser budget
					// that all clients behind it share. Network access ingress
					// writes the header itself and needs no such cap.
					if peer := clientip.PeerIP(r.Context()); peer != "" && peer != clientIP && netaccess.PathFromContext(r.Context()).IsDefault() {
						peerKey := fmt.Sprintf("authpeer:%s:%s", SourceKey(peer), endpoint)
						peerResult := mw.perKey.Allow(r.Context(), peerKey, authPeerRate(epRate))
						if !peerResult.Allowed {
							writeRateLimitResponse(w, peerResult, reject)
							return
						}
					}
				}
			}

			if !mw.allowAccount(w, r, cfg, reject) {
				return
			}

			globalRate := globalRateFor(cfg)
			globalResult := mw.global.Allow(r.Context(), "global", globalRate)
			if !globalResult.Allowed {
				writeRateLimitResponse(w, globalResult, reject)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// nat64Prefixes hold the IPv6 addresses that NAT64 and SIIT translators give
// IPv4 clients: the well-known prefix (RFC 6052) and the local-use range
// (RFC 8215). Each address there is a separate IPv4 client.
var nat64Prefixes = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// SourceKey is the part of a client address that the per-address budgets
// count. A public IPv6 client counts by its /64: one home line or server
// usually holds a whole /64, so counting each address would give it billions
// of budgets. Every other address counts on its own: IPv4, IPv4 written as
// IPv6, unique local and link-local IPv6, which only come from the local
// network the way private IPv4 addresses do, and anything that does not
// parse as an address.
func SourceKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is6() || addr.Is4In6() || !addr.IsGlobalUnicast() || addr.IsPrivate() || isNAT64(addr) {
		return ip
	}
	prefix, _ := addr.Prefix(64) // cannot fail: an IPv6 address has 128 bits
	return prefix.String()
}

func isNAT64(addr netip.Addr) bool {
	for _, prefix := range nat64Prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// allowAccount uses an authenticated account identity, independent of forwarding
// headers, profiles and credentials. Distinct accounts behind a proxy keep
// independent account budgets. With no authenticated identity, the existing
// source/global checks still run.
func (mw *Middleware) allowAccount(w http.ResponseWriter, r *http.Request, cfg Config, reject RejectBodyWriter) bool {
	accountID := accountFromContext(r.Context())
	if accountID <= 0 {
		return true
	}
	result := mw.perKey.Allow(r.Context(), fmt.Sprintf("account:%d", accountID), accountRate(cfg))
	if !result.Allowed {
		writeRateLimitResponse(w, result, reject)
	}
	return result.Allowed
}

// overQuotaLogEvery is how often a session over its quota is logged again.
const overQuotaLogEvery = 10 * time.Minute

// allowSession spends one request from the caller's sign-in session quota.
// It counts what a client asks of the API, not what it plays or downloads:
// playback and media transfer routes, API keys (which have their own tiers),
// admins and impersonating admins are not counted. In log mode a session over the quota
// is logged and served; in enforce mode it is refused.
func (mw *Middleware) allowSession(w http.ResponseWriter, r *http.Request, q SessionQuotaConfig, reject RejectBodyWriter) bool {
	if q.Mode != SessionQuotaLog && q.Mode != SessionQuotaEnforce {
		return true
	}
	claims := apimw.GetClaims(r.Context())
	if claims == nil || claims.SessionID == "" || claims.TokenType != auth.TokenTypeAccess ||
		claims.Role == models.RoleAdmin || claims.ImpersonatorUserID != nil || IsQuotaExemptPath(r.URL.Path) {
		return true
	}
	result := mw.quota.Take(r.Context(), "session:"+claims.SessionID, q)
	if result.Allowed {
		return true
	}
	mw.logOverQuota(r, claims, q)
	if q.Mode != SessionQuotaEnforce {
		return true
	}
	writeRateLimitResponse(w, result, reject)
	return false
}

func (mw *Middleware) logOverQuota(r *http.Request, claims *auth.Claims, q SessionQuotaConfig) {
	now := time.Now()
	if last, ok := mw.overQuotaLogged.Load(claims.SessionID); ok && now.Sub(last.(time.Time)) < overQuotaLogEvery { //nolint:errcheck,forcetypeassert // overQuotaLogged stores only time.Time
		return
	}
	mw.overQuotaLogged.Store(claims.SessionID, now)
	// Entries are dropped lazily: a session that stays over the quota keeps
	// one, and the rest age out on the next log.
	mw.overQuotaLogged.Range(func(key, value any) bool {
		if now.Sub(value.(time.Time)) >= overQuotaLogEvery { //nolint:errcheck,forcetypeassert // overQuotaLogged stores only time.Time
			mw.overQuotaLogged.Delete(key)
		}
		return true
	})
	slog.WarnContext(r.Context(), "sign-in session over its hourly request quota",
		"component", "ratelimit", "mode", q.Mode, "user_id", claims.UserID, "session_id", claims.SessionID,
		"requests_per_hour", q.RequestsPerHour, "burst", q.Burst, "user_agent", r.UserAgent(), "path", r.URL.Path)
}

// QuotaExemptPathPattern matches the routes session quotas do not count:
// everything under /stream and /playback, so a player keeps reporting
// progress, replanning and stopping after its session runs out, and the
// other native media transfer routes (internal/api's nativeMediaRoutes):
// downloads, direct downloads, theme audio and ebook reads. Playback is
// bounded by stream limits instead. Signing out (the provider sign-out lookup
// and the logout) is not counted either, so a session that spent its quota
// can still end itself. The pattern uses only syntax Go and PostgreSQL read
// alike, so the API usage summary leaves out the same requests in SQL.
const QuotaExemptPathPattern = `^/api/v[12]/(stream|playback)(/|$)` +
	`|^/api/v[12]/auth/(provider-)?logout$` +
	`|^/api/v[12]/direct-download(-proxy)?$` +
	`|^/api/v[12]/downloads/[^/]+/(file|file-proxy)$` +
	`|^/api/v[12]/downloads/[^/]+/subtitles/` +
	`|^/api/v2/catalog/items/[^/]+/themes/[^/]+/audio$` +
	`|^/api/v1/ebooks/[^/]+/files/[^/]+/read$`

var quotaExemptPath = regexp.MustCompile(QuotaExemptPathPattern)

// IsQuotaExemptPath reports whether session quotas leave path uncounted.
func IsQuotaExemptPath(path string) bool {
	// Every exempt route is under the native API; the prefix check keeps the
	// regexp off the other listeners' requests.
	return strings.HasPrefix(path, "/api/v") && quotaExemptPath.MatchString(path)
}

// authPeerBudgetMultiplier sizes the budget that every forwarded client behind
// one transport peer shares, relative to one client's endpoint budget. A
// reverse proxy fronting many users needs more than one client's allowance;
// a client rotating forwarding headers gets no more than this.
const authPeerBudgetMultiplier = 10

func authPeerRate(endpointRate Rate) Rate {
	return Rate{
		RequestsPerSecond: min(endpointRate.RequestsPerSecond*authPeerBudgetMultiplier, MaxRequestsPerWindow),
		RequestsPerMinute: min(endpointRate.RequestsPerMinute*authPeerBudgetMultiplier, MaxRequestsPerWindow),
		Burst:             min(endpointRate.Burst, MaxBurst/authPeerBudgetMultiplier) * authPeerBudgetMultiplier,
	}
}

// accountBudgetMultiplier sizes the allowance every device, profile and key of
// one account shares, in per-address budgets. One login commonly spans several
// networks (a home, a phone on cellular, family elsewhere), and every address is
// already capped at one per-address budget, so a single busy address or
// integration can spend at most a quarter of its account. A client rotating
// forwarding headers gets no more than this.
const accountBudgetMultiplier = 4

func accountRate(cfg Config) Rate {
	return Rate{
		RequestsPerSecond: min(cfg.IPReqPerSecond*accountBudgetMultiplier, MaxRequestsPerWindow),
		RequestsPerMinute: min(cfg.IPReqPerMinute*accountBudgetMultiplier, MaxRequestsPerWindow),
		Burst:             min(cfg.IPBurst, MaxBurst/accountBudgetMultiplier) * accountBudgetMultiplier,
	}
}

func globalRateFor(cfg Config) Rate {
	requestsPerSecond := cfg.GlobalReqPerSecond
	if math.IsNaN(requestsPerSecond) || math.IsInf(requestsPerSecond, 0) || requestsPerSecond <= 0 {
		requestsPerSecond = DefaultConfig().GlobalReqPerSecond
	} else if requestsPerSecond > MaxGlobalRequestsPerSecond {
		requestsPerSecond = MaxGlobalRequestsPerSecond
	}
	return Rate{
		RequestsPerSecond: requestsPerSecond,
		RequestsPerMinute: requestsPerSecond * 60,
		Burst:             max(1, int(math.Ceil(requestsPerSecond))),
	}
}

func writeRateLimitResponse(w http.ResponseWriter, result AllowResult, reject RejectBodyWriter) {
	retrySeconds := int(result.RetryAfter.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds))
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(result.Limit))
	w.Header().Set("X-RateLimit-Remaining", "0")
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(result.ResetAt.Unix(), 10))
	reject(w, retrySeconds)
}

// writeRateLimitBody is the native API's 429 body.
func writeRateLimitBody(w http.ResponseWriter, retrySeconds int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(rateLimitError{
		Error:      "rate_limit_exceeded",
		Message:    fmt.Sprintf("Too many requests. Please retry after %d seconds.", retrySeconds),
		RetryAfter: retrySeconds,
	})
}
