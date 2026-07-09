package jellycompat

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/go-chi/chi/v5"
)

type sessionContextKey string

const compatSessionKey sessionContextKey = "jellycompat_session"

var mediaBrowserTokenPattern = regexp.MustCompile(`(?i)token="?([^",\s]+)"?`)

const (
	tokenRefreshBuffer  = 5 * time.Minute
	tokenRefreshTimeout = 30 * time.Second
)

// Authenticator extracts Jellyfin-style auth tokens and resolves compat sessions.
type Authenticator struct {
	sessions    *SessionStore
	authService *auth.Service
	now         func() time.Time
}

// NewAuthenticator creates a new compat authenticator.
func NewAuthenticator(sessions *SessionStore, authService *auth.Service) *Authenticator {
	return &Authenticator{sessions: sessions, authService: authService, now: time.Now}
}

// ExtractToken extracts a compat token from Jellyfin-style request auth.
// Checks Authorization, X-Emby-Authorization, X-Emby-Token,
// X-Mediabrowser-Token headers and api_key query parameter.
func ExtractToken(r *http.Request) (string, bool) {
	// Check Authorization and X-Emby-Authorization headers for Bearer or
	// MediaBrowser Token="..." formats.  The Jellyfin Kotlin SDK (used by
	// Findroid and other Android clients) sends X-Emby-Authorization.
	for _, headerName := range []string{"Authorization", "X-Emby-Authorization"} {
		if header := strings.TrimSpace(r.Header.Get(headerName)); header != "" {
			parts := strings.SplitN(header, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
				token := strings.TrimSpace(parts[1])
				if token != "" {
					return token, true
				}
			}
			if match := mediaBrowserTokenPattern.FindStringSubmatch(header); len(match) == 2 && match[1] != "" {
				return match[1], true
			}
		}
	}

	if token := strings.TrimSpace(r.Header.Get("X-Emby-Token")); token != "" {
		return token, true
	}
	if token := strings.TrimSpace(r.Header.Get("X-Mediabrowser-Token")); token != "" {
		return token, true
	}
	// Query-param token. Jellyfin's current spelling is "ApiKey" (PascalCase,
	// always enabled); "api_key" is the legacy spelling (gated behind
	// EnableLegacyAuthorization on a real server). Clients vary the casing
	// further (Api_Key / API_KEY), so match both keys case-insensitively.
	// Ref: jellyfin Jellyfin.Server.Implementations/Security/AuthorizationContext.cs.
	query := newCaseInsensitiveQuery(r.URL.Query())
	for _, key := range []string{"ApiKey", "api_key"} {
		if token := strings.TrimSpace(query.Get(key)); token != "" {
			return token, true
		}
	}

	return "", false
}

// RequireSession enforces a valid compat session for a route.
func (a *Authenticator) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := ExtractToken(r)
		if !ok {
			slog.WarnContext(r.Context(), "jellycompat auth: no token in request", "component", "jellycompat",
				"path", r.URL.Path,
				"auth_header_present", r.Header.Get("Authorization") != "",
				"x_emby_auth_present", r.Header.Get("X-Emby-Authorization") != "",
			)
			writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
			return
		}

		session, ok := a.sessions.Get(token)
		if !ok {
			slog.WarnContext(r.Context(), "jellycompat auth: session not found", "component", "jellycompat",
				"path", r.URL.Path,
				"token_prefix", safeTokenPrefix(token),
			)
			writeError(w, http.StatusUnauthorized, "Unauthorized", "Invalid or expired authentication token")
			return
		}

		// Refresh underlying Silo tokens if they're about to expire.
		// Use a detached context so a client aborting the request mid-refresh
		// (common on flaky mobile networks) doesn't revoke the compat session.
		if a.authService != nil && !session.StreamAppTokenExpiry.IsZero() &&
			session.StreamAppTokenExpiry.Before(a.now().Add(tokenRefreshBuffer)) {
			refreshCtx, cancel := context.WithTimeout(context.Background(), tokenRefreshTimeout)
			newPair, err := a.authService.Refresh(refreshCtx, session.StreamAppRefreshToken)
			cancel()
			if err != nil {
				slog.WarnContext(r.Context(), "jellycompat auth: token refresh failed, revoking session", "component", "jellycompat",
					"path", r.URL.Path,
					"token_prefix", safeTokenPrefix(token),
					"error", err,
				)
				a.sessions.Delete(token)
				writeError(w, http.StatusUnauthorized, "Unauthorized", "Session expired")
				return
			}
			updateErr := a.sessions.Update(token, func(s *Session) error {
				s.StreamAppAccessToken = newPair.AccessToken
				s.StreamAppRefreshToken = newPair.RefreshToken
				s.StreamAppTokenExpiry = a.now().Add(time.Duration(newPair.ExpiresIn) * time.Second)
				return nil
			})
			if updateErr != nil {
				slog.WarnContext(r.Context(), "jellycompat auth: session update after refresh failed", "component", "jellycompat",
					"token_prefix", safeTokenPrefix(token),
					"error", updateErr,
				)
			} else {
				// Re-read the session to get the updated tokens.
				session, _ = a.sessions.Get(token)
			}
		}

		serveWithSession(next, w, r, session)
	})
}

// safeTokenPrefix returns the first 8 characters of a token for logging.
func safeTokenPrefix(token string) string {
	if len(token) <= 8 {
		return token
	}
	return token[:8] + "..."
}

// serveWithSession injects the resolved compat session into the request context
// and continues the handler chain.
func serveWithSession(next http.Handler, w http.ResponseWriter, r *http.Request, session *Session) {
	ctx := context.WithValue(r.Context(), compatSessionKey, session)
	ctx = playback.WithClientInfo(ctx, compatPlaybackClientInfo(r))
	next.ServeHTTP(w, r.WithContext(ctx))
}

func compatPlaybackClientInfo(r *http.Request) playback.ClientInfo {
	if r == nil {
		return playback.ClientInfo{}
	}
	return playback.ClientInfo{
		Name:      firstMediaBrowserAuthorizationValue(r, "Client"),
		Version:   firstMediaBrowserAuthorizationValue(r, "Version"),
		UserAgent: r.UserAgent(),
	}
}

func firstMediaBrowserAuthorizationValue(r *http.Request, key string) string {
	for _, headerName := range []string{"X-Emby-Authorization", "Authorization"} {
		if value := mediaBrowserAuthorizationValue(r.Header.Get(headerName), key); value != "" {
			return value
		}
	}
	return ""
}

func mediaBrowserAuthorizationValue(header, key string) string {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(header), "mediabrowser ") {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "mediabrowser ") {
			part = strings.TrimSpace(part[len("MediaBrowser "):])
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), key) {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}

// resolveCompatToken resolves a token to a compat session: a session-store token
// (normal login) or, matching Jellyfin, an sa_ admin API key (synthesized
// session bound to the key user's primary profile). Returns false when the token
// matches neither. keyAuth may be nil (resolveSession handles a nil receiver).
func resolveCompatToken(ctx context.Context, sessions *SessionStore, keyAuth *AdminAPIKeyAuthenticator, token string) (*Session, bool) {
	if token == "" {
		return nil, false
	}
	if session, ok := sessions.Get(token); ok {
		return session, true
	}
	if strings.HasPrefix(token, "sa_") {
		if session, _, _ := keyAuth.resolveSession(ctx, token); session != nil {
			return session, true
		}
	}
	return nil, false
}

// PlaybackSessionAuth creates middleware that falls back to playback session
// authentication for media stream endpoints where external players (e.g. libmpv)
// don't forward auth headers or query parameters.
func PlaybackSessionAuth(sessions *SessionStore, playbackStore CompatPlaybackStore, keyAuth *AdminAPIKeyAuthenticator) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try standard token auth first — a compat session token or an sa_
			// admin key (synthesized session).
			if token, ok := ExtractToken(r); ok {
				if session, ok := resolveCompatToken(r.Context(), sessions, keyAuth, token); ok {
					serveWithSession(next, w, r, session)
					return
				}
			}

			// Follow-up HLS requests (master/segment) carry only PlaySessionId,
			// no auth header or api_key. Resolve the negotiated session's
			// CompatToken — which for an API-key stream is itself the sa_ key,
			// so it must go through the same session-or-API-key resolution.
			//
			// The lookup must be case-insensitive: Wholphin's jellyfin-sdk-kotlin
			// builds its own direct-play URL with a lowercase "playSessionId"
			// (and no api_key / auth header), so a case-sensitive match would
			// miss it and 401 the stream — forcing a needless transcode fallback.
			if playSessionID := newCaseInsensitiveQuery(r.URL.Query()).Get("PlaySessionId"); playSessionID != "" {
				if playSession, found := playbackStore.Get(playSessionID); found {
					if session, ok := resolveCompatToken(r.Context(), sessions, keyAuth, playSession.CompatToken); ok {
						serveWithSession(next, w, r, session)
						return
					}
					writeError(w, http.StatusUnauthorized, "Unauthorized", "Session expired")
					return
				}
			}

			// Stock Jellyfin Android TV ignores the api_key-bearing DirectStreamUrl
			// we return from PlaybackInfo and builds its own direct-play URL with no
			// auth header, no api_key/ApiKey, and no PlaySessionId. Anchor auth on
			// the PlaybackSession negotiated for this item: a successful PlaybackInfo
			// already authenticated the user and registered a session holding the
			// CompatToken. Scope this strictly to the direct-play video stream routes
			// (NOT /Items/{id}/Download) via the chi route pattern, prefer matching
			// on mediaSourceId when present, and require the matched session's
			// RouteItemID to equal the requested item so a source id can't
			// authorize a stream for a different item.
			if playbackStore != nil {
				switch chi.RouteContext(r.Context()).RoutePattern() {
				case "/Videos/{id}/stream", "/Videos/{id}/stream.{container}":
					routeItemID := chi.URLParam(r, "id")
					if routeItemID != "" {
						mediaSourceID := newCaseInsensitiveQuery(r.URL.Query()).Get("mediaSourceId")
						lookupID := routeItemID
						if mediaSourceID != "" {
							lookupID = mediaSourceID
						}
						if playSession, _, found := playbackStore.FindByRoute("", lookupID); found && playSession.RouteItemID == routeItemID {
							if session, ok := resolveCompatToken(r.Context(), sessions, keyAuth, playSession.CompatToken); ok {
								serveWithSession(next, w, r, session)
								return
							}
						}
					}
				}
			}

			writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		})
	}
}

// SessionFromContext returns the authenticated compat session, if present.
func SessionFromContext(ctx context.Context) *Session {
	session, _ := ctx.Value(compatSessionKey).(*Session)
	return session
}
