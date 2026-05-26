package abs

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// ErrNotFound is returned by TokenStore when a JTI is absent.
var ErrNotFound = errors.New("abs token not found")

// handleLogin mints ABS access + refresh JWTs for the caller.
//
// Two paths:
//
//  1. Host-proxied: X-Silo-User-Id header is present. The silo host validated
//     the session before forwarding, so the header is trusted unconditionally.
//     ProfileID comes from X-Silo-Profile-Id (may be empty = primary profile).
//
//  2. Standalone body-creds: no host header. The handler delegates to
//     handleStandaloneLogin, which calls CredValidator.Validate and applies
//     rate limiting.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if userID := r.Header.Get("X-Silo-User-Id"); userID != "" {
		profileID := r.Header.Get("X-Silo-Profile-Id")
		name := r.Header.Get("X-Silo-Profile-Name")
		if name == "" {
			name = r.Header.Get("X-Silo-User-Name")
		}
		h.completeLogin(w, r, userID, profileID, name)
		return
	}
	h.handleStandaloneLogin(w, r)
}

// handleStandaloneLogin validates body-credential login when no host-proxy
// header is present. It checks whether standalone login is enabled, enforces
// the per-IP rate limit, decodes the JSON body, and calls CredValidator.
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

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
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
// writes the login response. Shared by the host-proxied and body-creds paths
// so both return the identical response envelope.
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
		JTI:       refreshJTI,
		ExpiresAt: now.Add(refreshTTL),
	}); err != nil {
		http.Error(w, "persist refresh token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Debug("abs completeLogin: tokens persisted",
		"user_id", userID, "access_jti", accessJTI, "refresh_jti", refreshJTI)

	writeJSON(w, http.StatusOK, h.loginEnvelope(r, userID, displayName, access, refresh))
}

// handleABSPing — GET /ping (mounted also as /healthcheck). Wire shape
// matches the continuum-plugin-audiobooks implementation exactly: clients
// use this to validate the URL before showing the login form, and any
// other shape causes the official mobile app to reject the server.
func (h *Handler) handleABSPing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
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
// connection to confirm the server is an ABS install and pull a few
// global flags. Matches the plugin shape exactly (key order intentional).
func (h *Handler) handleABSStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"isInit":        true,
		"language":      "en-us",
		"app":           "audiobookshelf",
		"serverVersion": ServerVersion,
	})
}

// loginEnvelope builds the response body shared by /login and /authorize.
// Both endpoints must return the identical shape so the iOS client's
// resume-on-launch flow validates the same way as fresh login.
// accessToken/refreshToken may be empty for /authorize (client already has
// them); in that case the top-level fields are still included as empty
// strings so the JSON shape stays stable.
func (h *Handler) loginEnvelope(
	r *http.Request,
	userID, displayName, accessToken, refreshToken string,
) map[string]any {
	name := displayName
	if name == "" {
		name = userID
	}

	libraryMaps := make([]map[string]any, 0)
	defaultLibraryID := VirtualLibraryID
	libs, _ := h.deps.MediaStore.ListAudiobookLibraries(r.Context())
	for i, lib := range libs {
		if i == 0 {
			defaultLibraryID = audiobookLibraryID(lib)
		}
		libraryMaps = append(libraryMaps, audiobookLibraryMap(lib))
	}

	nowMs := time.Now().UnixMilli()

	user := map[string]any{
		"id":                              userID,
		"username":                        name,
		"type":                            "user",
		"defaultLibraryId":                defaultLibraryID,
		"librariesAccessible":             []any{},
		"itemTagsAccessible":              []any{},
		"itemTagsSelected":                []any{},
		"mediaProgress":                   []any{},
		"bookmarks":                       []any{},
		"seriesHideFromContinueListening": []any{},
		"isOldToken":                      false,
		"token":                           accessToken,
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

	// x-return-tokens opt-in: when set, embed token pair on user object too
	// (some clients read from the user envelope, others from the top level).
	if strings.EqualFold(r.Header.Get("x-return-tokens"), "true") {
		user["accessToken"] = accessToken
		user["refreshToken"] = refreshToken
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
		"authActiveAuthMethods":             []string{"local"},
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
	// /authorize re-mints the envelope using the caller's bearer as the
	// access token (client already has it); refresh isn't rotated here.
	// userID is passed as displayName because the JWT carries no display-name
	// claim (Claims has sub/pid/jti only) — the client's stored profile name
	// takes precedence anyway, so the echoed userID is just a harmless seed.
	writeJSON(w, http.StatusOK, h.loginEnvelope(r, a.UserID, a.UserID, a.Token, ""))
}

// handleRefresh — POST /auth/refresh
//
// Real ABS clients send the refresh token via x-refresh-token header with
// an empty body; legacy / 3rd-party clients send {refreshToken: "..."} in
// the JSON body. Accept either; header takes precedence when both are sent.
//
// Token rotation semantics (ported from continuum-plugin-audiobooks):
//  1. Validate the refresh token signature + type.
//  2. Confirm the JTI is in the store and not revoked.
//  3. Mint a NEW access + refresh pair with fresh JTIs.
//  4. Persist both new JTIs BEFORE revoking the old one. If anything in
//     step 3-4 fails, the old refresh stays valid and the client can retry.
//  5. Revoke the old refresh JTI.
//  6. Return {user:{accessToken, refreshToken}} AND top-level token fields
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
	row, err := h.deps.TokenStore.GetTokenByJTI(r.Context(), claims.JTI)
	if err != nil {
		slog.Debug("abs refresh: jti lookup failed", "jti", claims.JTI, "err", err)
		http.Error(w, "refresh token revoked", http.StatusUnauthorized)
		return
	}
	if row.RevokedAt != nil {
		http.Error(w, "refresh token revoked", http.StatusUnauthorized)
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
		http.Error(w, "token mint failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	refresh, err := IssueRefreshToken(secret, claims.UserID, claims.ProfileID, newRefreshJTI, refreshTTL)
	if err != nil {
		http.Error(w, "token mint failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	if err := h.deps.TokenStore.InsertToken(r.Context(), ABSToken{
		ID: newAccessJTI, UserID: claims.UserID, ProfileID: claims.ProfileID,
		JTI: newAccessJTI, ExpiresAt: now.Add(accessTTL),
	}); err != nil {
		http.Error(w, "token persist failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.deps.TokenStore.InsertToken(r.Context(), ABSToken{
		ID: newRefreshJTI, UserID: claims.UserID, ProfileID: claims.ProfileID,
		JTI: newRefreshJTI, ExpiresAt: now.Add(refreshTTL),
	}); err != nil {
		http.Error(w, "token persist failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.deps.TokenStore.RevokeTokenByJTI(r.Context(), claims.JTI); err != nil {
		http.Error(w, "token rotation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Debug("abs refresh: rotated", "user", claims.UserID,
		"old_jti", claims.JTI, "new_access_jti", newAccessJTI, "new_refresh_jti", newRefreshJTI)

	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":           claims.UserID,
			"accessToken":  access,
			"refreshToken": refresh,
		},
		"accessToken":  access,
		"refreshToken": refresh,
	})
}

// handleLogout — POST /logout
//
// Mounted inside the bearerAuth group: the middleware has already parsed
// and validated the access JTI. We revoke that JTI (idempotent) and
// return 204. There is no body and no JSON response.
//
// Note: this revokes ONLY the access token. The associated refresh token
// has its own JTI and stays valid until the client also calls /auth/refresh
// with a since-revoked access; the refresh endpoint will then deny the
// rotation. Clients that want a hard "log out everywhere" should iterate
// the sessions list (added in Phase 3) instead.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.JTI == "" {
		// No auth context — middleware shouldn't have let us through, but
		// be defensive and return 204 anyway (logout is idempotent).
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if h.deps.TokenStore == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := h.deps.TokenStore.RevokeTokenByJTI(r.Context(), a.JTI); err != nil {
		slog.Warn("abs logout: revoke failed", "jti", a.JTI, "user", a.UserID, "err", err)
		http.Error(w, "logout failed", http.StatusInternalServerError)
		return
	}
	slog.Debug("abs logout: revoked", "jti", a.JTI, "user", a.UserID)
	w.WriteHeader(http.StatusNoContent)
}
