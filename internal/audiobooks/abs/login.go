package abs

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// ErrNotFound is returned by TokenStore when a JTI is absent.
var ErrNotFound = errors.New("abs token not found")

// handleLogin mints ABS access + refresh JWTs for the caller.
//
// Login always validates body credentials. Public ABS listeners cannot safely
// trust user/profile headers because clients can spoof them directly.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	h.handleStandaloneLogin(w, r)
}

// handleStandaloneLogin validates body-credential login. It checks whether
// standalone login is enabled, enforces the per-IP rate limit, decodes the JSON
// body, and calls CredValidator.
func (h *Handler) handleStandaloneLogin(w http.ResponseWriter, r *http.Request) {
	if h.deps.Config == nil {
		http.Error(w, "config not available", http.StatusServiceUnavailable)
		return
	}
	enabled, err := h.deps.Config.StandaloneLoginEnabled(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	if !enabled {
		http.Error(w, "standalone login is disabled on this server", http.StatusUnauthorized)
		return
	}
	if h.deps.CredValidator == nil {
		http.Error(w, "standalone login is unavailable in this deployment", http.StatusServiceUnavailable)
		return
	}

	ip := clientIP(r)
	if !h.deps.LoginLimiter.allow(ip) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many login attempts; try again shortly", http.StatusTooManyRequests)
		return
	}

	// Real ABS (express body-parser + passport local) accepts BOTH JSON and
	// application/x-www-form-urlencoded credential bodies; different clients
	// send different encodings. Buffer the body once, try JSON, then fall back
	// to form-encoded — a JSON-only parse 400s a form-encoded client, which
	// the app surfaces as a generic "unknown error" on sign-in.
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if jsonErr := json.Unmarshal(raw, &body); jsonErr != nil || strings.TrimSpace(body.Username) == "" {
		if vals, formErr := url.ParseQuery(string(raw)); formErr == nil {
			if u := vals.Get("username"); u != "" {
				body.Username = u
				body.Password = vals.Get("password")
			}
		}
	}
	if strings.TrimSpace(body.Username) == "" || body.Password == "" {
		http.Error(w, "username and password are required", http.StatusUnauthorized)
		return
	}

	userID, profileID, displayName, err := h.deps.CredValidator.Validate(r.Context(), body.Username, body.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, auth.ErrUserDisabled) {
			http.Error(w, "invalid username or password", http.StatusUnauthorized)
		} else {
			slog.Error("abs login: cred validator failed", "username", body.Username, "err", err)
			http.Error(w, "login service unavailable", http.StatusServiceUnavailable)
		}
		return
	}

	// If the validator didn't return a displayName, derive it from the username
	// (the profile portion after '#', or the whole username).
	if displayName == "" {
		displayName = body.Username
		if i := strings.LastIndexByte(displayName, '#'); i >= 0 && i < len(displayName)-1 {
			displayName = displayName[i+1:]
		}
	}

	slog.Debug("abs standalone login: validator OK",
		"username", body.Username, "user_id", userID, "profile_id", profileID)
	h.completeLogin(w, r, userID, profileID, displayName)
}

// completeLogin mints ABS access + refresh JWTs for the validated user and
// writes the login response.
func (h *Handler) completeLogin(w http.ResponseWriter, r *http.Request, userID, profileID, displayName string) {
	if h.deps.Config == nil {
		http.Error(w, "config not available", http.StatusServiceUnavailable)
		return
	}
	if h.deps.TokenStore == nil {
		http.Error(w, "token store not available", http.StatusServiceUnavailable)
		return
	}

	secret, err := h.deps.Config.JWTSecret(r.Context())
	if err != nil {
		http.Error(w, "jwt secret unavailable", http.StatusInternalServerError)
		return
	}

	accessTTL, err := h.deps.Config.AccessTTL(r.Context())
	if err != nil || accessTTL == 0 {
		accessTTL = 24 * time.Hour
	}
	refreshTTL, err := h.deps.Config.RefreshTTL(r.Context())
	if err != nil || refreshTTL == 0 {
		refreshTTL = 30 * 24 * time.Hour
	}

	accessJTI := ulid.Make().String()
	refreshJTI := ulid.Make().String()

	access, err := IssueAccessToken(secret, userID, profileID, accessJTI, accessTTL)
	if err != nil {
		http.Error(w, "mint access token: "+err.Error(), http.StatusInternalServerError)
		return
	}
	refresh, err := IssueRefreshToken(secret, userID, profileID, refreshJTI, refreshTTL)
	if err != nil {
		http.Error(w, "mint refresh token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	now := time.Now()
	if err := h.deps.TokenStore.InsertToken(r.Context(), ABSToken{
		ID:        accessJTI,
		UserID:    userID,
		ProfileID: profileID,
		Type:      "access",
		JTI:       accessJTI,
		ExpiresAt: now.Add(accessTTL),
	}); err != nil {
		http.Error(w, "persist access token: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Refresh-token insert must also succeed: a client that receives a refresh
	// token whose JTI isn't in the store will fail on its first use (the
	// bearer middleware looks up by JTI), forcing an interactive re-login.
	if err := h.deps.TokenStore.InsertToken(r.Context(), ABSToken{
		ID:        refreshJTI,
		UserID:    userID,
		ProfileID: profileID,
		Type:      "refresh",
		JTI:       refreshJTI,
		ExpiresAt: now.Add(refreshTTL),
	}); err != nil {
		http.Error(w, "persist refresh token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Debug("abs completeLogin: tokens persisted",
		"user_id", userID, "access_jti", accessJTI, "refresh_jti", refreshJTI)

	// Real ABS delivers the refresh token in the body when the client opts in
	// via x-return-tokens, otherwise as an HttpOnly refresh_token cookie.
	// Mirrors server/Auth.js: setRefreshTokenCookie when !returnTokens.
	returnRefreshInBody := strings.EqualFold(r.Header.Get("x-return-tokens"), "true")
	if !returnRefreshInBody {
		setRefreshCookie(w, r, refresh, refreshTTL)
	}

	writeJSON(w, http.StatusOK, h.loginEnvelope(r, now, userID, displayName, access, refresh, returnRefreshInBody))
}

// handleABSPing — GET /ping (mounted also as /healthcheck). Wire shape
// matches the continuum-plugin-audiobooks implementation exactly: clients
// use this to validate the URL before showing the login form, and any
// other shape causes the official mobile app to reject the server.
func (h *Handler) handleABSPing(w http.ResponseWriter, _ *http.Request) {
	// Real ABS /ping returns {"success": true} (server/Server.js). The ABS
	// apps validate a server address by reading response.success — without it
	// they report "unable to reach". The pong/server/version keys are kept as
	// harmless extras for plugin-shape clients.
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"server":  "audiobookshelf",
		"version": ServerVersion,
		"pong":    true,
	})
}

// handleABSInit — GET /init. Real ABS first-run detection probe.
func (h *Handler) handleABSInit(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"isInit": true})
}

// handleABSStatus — GET /status. Mobile clients call this on every
// connection to confirm the server is an ABS install and to learn which
// auth methods to render on the login form. Mirrors real ABS Server.js
// /status: {app, serverVersion, isInit, language, authMethods, authFormData}.
// authMethods drives the login UI — omitting it can leave the app unable to
// present a usable login flow.
func (h *Handler) handleABSStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"app":           "audiobookshelf",
		"serverVersion": ServerVersion,
		"isInit":        true,
		"language":      "en-us",
		"authMethods":   []string{"local"},
		"authFormData":  map[string]any{},
	})
}

// absUserObject builds the ABS user object shared by the login/authorize
// envelope and GET /me. It mirrors the key set of audiobookshelf
// User.toOldJSONForBrowser (server/models/User.js) so every endpoint that
// emits a user decodes with one client model — a missing key crashes strict
// clients, so we emit the full set even where silo has no analog (email is
// "", the flags are constant).
//
// `token` is the caller's current access token — real ABS's deprecated
// non-expiring `token` slot. The login/authorize caller additionally sets
// user.accessToken / user.refreshToken; GET /me never carries those.
func absUserObject(userID, displayName, token, defaultLibraryID string, now time.Time) map[string]any {
	name := displayName
	if name == "" {
		name = userID
	}
	nowMs := now.UnixMilli()
	return map[string]any{
		"id":                              userID,
		"username":                        name,
		"email":                           "",
		"type":                            "user",
		"defaultLibraryId":                defaultLibraryID,
		"librariesAccessible":             []any{},
		"itemTagsAccessible":              []any{},
		"itemTagsSelected":                []any{},
		"mediaProgress":                   []any{},
		"bookmarks":                       []any{},
		"seriesHideFromContinueListening": []any{},
		"isOldToken":                      false,
		"isActive":                        true,
		"isLocked":                        false,
		"hasOpenIDLink":                   false,
		"token":                           token,
		"lastSeen":                        nowMs,
		"createdAt":                       nowMs,
		"permissions": map[string]any{
			"download":                  true,
			"update":                    true,
			"delete":                    true,
			"upload":                    true,
			"accessAllLibraries":        true,
			"accessAllTags":             true,
			"accessExplicitContent":     true,
			"selectedTagsNotAccessible": false,
		},
	}
}

// loginEnvelope builds the response body shared by /login and /authorize.
// Both endpoints must return the identical shape so the iOS client's
// resume-on-launch flow validates the same way as fresh login.
// accessToken/refreshToken may be empty for /authorize (client already has
// them); in that case the top-level fields are still included as empty
// strings so the JSON shape stays stable.
//
// `now` is threaded in from the caller so the user.lastSeen/createdAt
// timestamps share a single instant with the token ExpiresAt the caller
// persisted — avoids two near-simultaneous time.Now() calls drifting apart.
func (h *Handler) loginEnvelope(
	r *http.Request,
	now time.Time,
	userID, displayName, accessToken, refreshToken string,
	returnRefreshInBody bool,
) map[string]any {
	// displayName falls back to userID when the validator didn't supply one.
	libraryMaps := make([]map[string]any, 0)
	defaultLibraryID := VirtualLibraryID
	access, _, _ := h.accessFilterFromRequest(r)
	libs, _ := h.deps.MediaStore.ListAudiobookLibraries(r.Context(), access)
	for i, lib := range libs {
		if i == 0 {
			defaultLibraryID = audiobookLibraryID(lib)
		}
		libraryMaps = append(libraryMaps, audiobookLibraryMap(lib))
	}

	user := absUserObject(userID, displayName, accessToken, defaultLibraryID, now)

	// Real ABS (v2.26+) ALWAYS sets user.accessToken; modern clients read it
	// from exactly res.user.accessToken. The x-return-tokens header gates ONLY
	// the refresh token: present in the body when the client opts in, otherwise
	// null (and delivered as the refresh_token cookie by the caller). See
	// audiobookshelf server/Auth.js handleLoginSuccess.
	user["accessToken"] = accessToken
	if returnRefreshInBody {
		user["refreshToken"] = refreshToken
	} else {
		user["refreshToken"] = nil
	}

	serverSettings := map[string]any{
		"id":                                "server-settings",
		"version":                           ServerVersion,
		"buildNumber":                       1,
		"language":                          "en-us",
		"dateFormat":                        "MM/dd/yyyy",
		"timeFormat":                        "HH:mm",
		"timeZone":                          "UTC",
		"coverAspectRatio":                  1,
		"storeCoverWithItem":                false,
		"storeMetadataWithItem":             false,
		"metadataFileFormat":                "json",
		"scannerDisableWatcher":             true,
		"scannerParseSubtitle":              false,
		"scannerFindCovers":                 false,
		"scannerCoverProvider":              "google",
		"scannerPreferMatchedMetadata":      false,
		"scannerPreferOverdriveMediaMarker": false,
		"sortingIgnorePrefix":               false,
		"sortingPrefixes":                   []string{"the", "a"},
		"chromecastEnabled":                 false,
		"enableEReader":                     false,
		"dateString":                        "",
		"logLevel":                          1,
		"version_id":                        ServerVersion,
		"sessionTimeout":                    0,
		"backupSchedule":                    false,
		"backupsToKeep":                     2,
		"maxBackupSize":                     1,
		"loggerDailyLogsToKeep":             7,
		"loggerScannerLogsToKeep":           2,
		"homeBookshelfView":                 1,
		"bookshelfView":                     1,
		"podcastEpisodeSchedule":            "0 * * * *",
		"sortingIgnorePrefixesValue":        "",
		"allowIframe":                       false,
		// Auth / rate-limit / OpenID fields from real ABS
		// ServerSettings.toJSONForBrowser. OIDC-aware clients (Prologue)
		// decode serverSettings into a strict model that includes these keys;
		// omitting them throws keyNotFound and the whole login response fails
		// to decode ("unknown error" on the login screen). Emit real ABS's
		// defaults for an OIDC-disabled server — authActiveAuthMethods still
		// advertises only "local", so no client tries the OpenID flow.
		"rateLimitLoginRequests":             10,
		"rateLimitLoginWindow":               600000,
		"backupPath":                         "/metadata/backups",
		"allowedOrigins":                     []string{},
		"authActiveAuthMethods":              []string{"local"},
		"authLoginCustomMessage":             nil,
		"authOpenIDIssuerURL":                nil,
		"authOpenIDAuthorizationURL":         nil,
		"authOpenIDTokenURL":                 nil,
		"authOpenIDUserInfoURL":              nil,
		"authOpenIDJwksURL":                  nil,
		"authOpenIDLogoutURL":                nil,
		"authOpenIDTokenSigningAlgorithm":    "RS256",
		"authOpenIDButtonText":               "Login with OpenID",
		"authOpenIDAutoLaunch":               false,
		"authOpenIDAutoRegister":             false,
		"authOpenIDMatchExistingBy":          nil,
		"authOpenIDSubfolderForRedirectURLs": "",
	}

	return map[string]any{
		"user":                 user,
		"userDefaultLibraryId": defaultLibraryID,
		"serverSettings":       serverSettings,
		"Source":               "silo",
		"ereaderDevices":       []any{},
		"libraries":            libraryMaps,
		"accessToken":          accessToken,
		"refreshToken":         refreshToken,
	}
}

// handleABSAuthorize — POST /api/authorize. Real ABS uses this to
// validate a bearer token and re-mint the login envelope so the client
// can resume without retyping credentials. Mounted inside the bearerAuth
// group so it inherits the same token validation.
//
// The response shape is the FULL login envelope — same fields, same
// ordering — not a `{"user": ...}` wrapper. Mirrors the
// continuum-plugin-audiobooks handleAuthorize verbatim.
func (h *Handler) handleABSAuthorize(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Mint a NEW access token. ABS v2.26.0+ clients check whether the
	// stored `serverConfig.token` equals the `user.token` echoed by
	// /authorize; if equal they assume the server is still on the
	// pre-v2.26 single-token shape and force re-login. Rotating the
	// access JWT here keeps the client happy without changing the
	// refresh-token contract. The OLD access JTI stays valid for its
	// natural TTL (don't revoke — multiple devices share the same JTI
	// via the share-on-login pattern, and a hard revoke would log
	// other devices out).
	access := a.Token
	if h.deps.Config != nil && h.deps.TokenStore != nil {
		if secret, err := h.deps.Config.JWTSecret(r.Context()); err == nil {
			accessTTL, _ := h.deps.Config.AccessTTL(r.Context())
			if accessTTL == 0 {
				accessTTL = 24 * time.Hour
			}
			newJTI := ulid.Make().String()
			if token, mintErr := IssueAccessToken(secret, a.UserID, a.ProfileID, newJTI, accessTTL); mintErr == nil {
				if persistErr := h.deps.TokenStore.InsertToken(r.Context(), ABSToken{
					ID:        newJTI,
					UserID:    a.UserID,
					ProfileID: a.ProfileID,
					Type:      "access",
					JTI:       newJTI,
					ExpiresAt: time.Now().Add(accessTTL),
				}); persistErr == nil {
					access = token
				}
			}
		}
	}
	// /authorize never issues a refresh token (the client already holds one),
	// so there is nothing to return in the body.
	writeJSON(w, http.StatusOK, h.loginEnvelope(r, time.Now(), a.UserID, a.UserID, access, "", false))
}

// setRefreshCookie writes the ABS refresh_token cookie exactly as real ABS
// does when a client did not opt into body-token delivery. HttpOnly + Lax,
// Secure only under TLS so plain-HTTP LAN deployments still receive it.
func setRefreshCookie(w http.ResponseWriter, r *http.Request, refresh string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    refresh,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl / time.Second),
	})
}

// handleRefresh — POST /auth/refresh
//
// Real ABS clients send the refresh token via x-refresh-token header with
// an empty body; legacy / 3rd-party clients send {refreshToken: "..."} in
// the JSON body. Accept either; header takes precedence when both are sent.
//
// Token rotation semantics:
//  1. Validate the refresh token signature + type.
//  2. Atomically revoke the old refresh JTI. A concurrent reuse loses here.
//  3. Mint a NEW access + refresh pair with fresh JTIs.
//  4. Persist both new JTIs.
//  5. Return {user:{accessToken, refreshToken}} AND top-level token fields
//     for client compatibility — mainline app reads from user{}, third-party
//     readers may read from the top level.
func (h *Handler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	refreshTok := strings.TrimSpace(r.Header.Get("x-refresh-token"))
	if refreshTok == "" {
		var p struct {
			RefreshToken string `json:"refreshToken"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err == nil {
			refreshTok = p.RefreshToken
		}
	}
	if refreshTok == "" {
		// Cookie-flow clients (those that omit x-return-tokens at login) hold the
		// refresh token only in the HttpOnly refresh_token cookie the server set —
		// they send neither header nor body. Read it here or they get a spurious
		// 400 once the access token expires. Mirrors real ABS Auth.js, which
		// checks req.cookies.refresh_token.
		if c, err := r.Cookie("refresh_token"); err == nil {
			refreshTok = strings.TrimSpace(c.Value)
		}
	}
	if refreshTok == "" {
		http.Error(w, "refreshToken required", http.StatusBadRequest)
		return
	}
	if h.deps.Config == nil || h.deps.TokenStore == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	secret, err := h.deps.Config.JWTSecret(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	claims, err := ParseToken(secret, refreshTok)
	if err != nil || claims.Type != "refresh" {
		claimsType := ""
		if claims != nil {
			claimsType = claims.Type
		}
		slog.Debug("abs refresh: parse/type failed", "err", err, "type", claimsType)
		http.Error(w, "invalid refresh token", http.StatusUnauthorized)
		return
	}
	row, err := h.deps.TokenStore.RevokeTokenIfActive(r.Context(), claims.JTI)
	if err != nil {
		slog.Debug("abs refresh: jti revoke failed", "jti", claims.JTI, "err", err)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "refresh token revoked", http.StatusUnauthorized)
		} else {
			http.Error(w, "token rotation failed", http.StatusInternalServerError)
		}
		return
	}
	if row.UserID != "" && row.UserID != claims.UserID {
		http.Error(w, "invalid refresh token", http.StatusUnauthorized)
		return
	}
	if row.ProfileID != "" && row.ProfileID != claims.ProfileID {
		http.Error(w, "invalid refresh token", http.StatusUnauthorized)
		return
	}
	if row.Type != "" && row.Type != "refresh" {
		http.Error(w, "invalid refresh token", http.StatusUnauthorized)
		return
	}
	if !row.ExpiresAt.IsZero() && time.Now().After(row.ExpiresAt) {
		http.Error(w, "refresh token expired", http.StatusUnauthorized)
		return
	}

	accessTTL, err := h.deps.Config.AccessTTL(r.Context())
	if err != nil || accessTTL == 0 {
		accessTTL = 24 * time.Hour
	}
	refreshTTL, err := h.deps.Config.RefreshTTL(r.Context())
	if err != nil || refreshTTL == 0 {
		refreshTTL = 30 * 24 * time.Hour
	}

	newAccessJTI := ulid.Make().String()
	newRefreshJTI := ulid.Make().String()
	access, err := IssueAccessToken(secret, claims.UserID, claims.ProfileID, newAccessJTI, accessTTL)
	if err != nil {
		slog.Error("abs refresh: mint access failed", "user", claims.UserID, "err", err)
		http.Error(w, "token mint failed", http.StatusInternalServerError)
		return
	}
	refresh, err := IssueRefreshToken(secret, claims.UserID, claims.ProfileID, newRefreshJTI, refreshTTL)
	if err != nil {
		slog.Error("abs refresh: mint refresh failed", "user", claims.UserID, "err", err)
		http.Error(w, "token mint failed", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	if err := h.deps.TokenStore.InsertToken(r.Context(), ABSToken{
		ID: newAccessJTI, UserID: claims.UserID, ProfileID: claims.ProfileID,
		Type: "access", JTI: newAccessJTI, ExpiresAt: now.Add(accessTTL),
	}); err != nil {
		slog.Error("abs refresh: persist access failed", "user", claims.UserID, "jti", newAccessJTI, "err", err)
		http.Error(w, "token persist failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.TokenStore.InsertToken(r.Context(), ABSToken{
		ID: newRefreshJTI, UserID: claims.UserID, ProfileID: claims.ProfileID,
		Type: "refresh", JTI: newRefreshJTI, ExpiresAt: now.Add(refreshTTL),
	}); err != nil {
		slog.Error("abs refresh: persist refresh failed", "user", claims.UserID, "jti", newRefreshJTI, "err", err)
		http.Error(w, "token persist failed", http.StatusInternalServerError)
		return
	}
	slog.Debug("abs refresh: rotated", "user", claims.UserID,
		"old_jti", claims.JTI, "new_access_jti", newAccessJTI, "new_refresh_jti", newRefreshJTI)

	// Real ABS /auth/refresh returns the SAME payload as /login. Return the
	// full envelope (not a thin token map) so a strict client can decode it
	// with the same model it uses for login. The rotated refresh token goes
	// in the body when the client sent x-refresh-token (mobile), otherwise as
	// the refresh_token cookie. No displayName is available at refresh time —
	// loginEnvelope falls back to the userID for username.
	returnRefreshInBody := strings.TrimSpace(r.Header.Get("x-refresh-token")) != ""
	if !returnRefreshInBody {
		setRefreshCookie(w, r, refresh, refreshTTL)
	}
	writeJSON(w, http.StatusOK, h.loginEnvelope(r, now, claims.UserID, "", access, refresh, returnRefreshInBody))
}

// handleLogout — POST /logout (and /api/logout, /abs/api/logout, /abs/api/auth/logout)
//
// Mounted OUTSIDE the bearerAuth group so a client whose access token has
// expired — the most common "I want to sign out" moment — can still revoke
// their JTI without first re-authenticating.
//
// Real ABS returns HTTP 200 with { redirect_url } (null for local auth), NOT
// an empty 204 — a strict client decodes the body and would fail on 204. It
// also clears the refresh_token cookie. Token/session revocation is
// best-effort: a failure there must not turn logout into an error the client
// can't recover from.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	h.revokeLogoutPrincipal(r)

	// Clear the refresh_token cookie set at login (MaxAge<0 deletes it).
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	// redirect_url is null for local auth (only OpenID logout returns a URL).
	writeJSON(w, http.StatusOK, map[string]any{"redirect_url": nil})
}

// revokeLogoutPrincipal best-effort revokes every active token for the bearer's
// principal and closes its open playback sessions. All failures are logged and
// swallowed — the caller always responds 200.
func (h *Handler) revokeLogoutPrincipal(r *http.Request) {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if raw == "" {
		raw = r.URL.Query().Get("token")
	}
	if raw == "" || h.deps.TokenStore == nil || h.deps.Config == nil {
		return
	}
	secret, err := h.deps.Config.JWTSecret(r.Context())
	if err != nil {
		slog.Debug("abs logout: jwt secret fetch failed", "err", err)
		return
	}
	claims, err := ParseToken(secret, raw)
	if err != nil || claims.JTI == "" {
		slog.Debug("abs logout: parse failed", "err", err)
		return
	}
	if err := h.deps.TokenStore.RevokeTokensForPrincipal(r.Context(), claims.UserID, claims.ProfileID); err != nil {
		slog.Warn("abs logout: revoke principal tokens failed", "jti", claims.JTI, "user", claims.UserID, "err", err)
		return
	}
	if h.deps.PlaybackSessionStore != nil {
		if err := h.deps.PlaybackSessionStore.CloseOpenSessionsForPrincipal(r.Context(), claims.UserID, claims.ProfileID); err != nil {
			slog.Warn("abs logout: close sessions failed", "user", claims.UserID, "profile", claims.ProfileID, "err", err)
		}
	}
	slog.Debug("abs logout: revoked principal tokens", "jti", claims.JTI, "user", claims.UserID)
}
