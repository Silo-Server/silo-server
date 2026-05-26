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

	// Build user object. displayName falls back to userID when empty.
	name := displayName
	if name == "" {
		name = userID
	}

	// Resolve the audiobook library list + default ID up front so we can
	// emit them in the login envelope. ABS clients require these on the
	// initial login response to seed the library picker before /me lands.
	libs, _ := h.deps.MediaStore.ListAudiobookLibraries(r.Context())
	libraryMaps := make([]map[string]any, 0, len(libs))
	defaultLibraryID := VirtualLibraryID
	for i, lib := range libs {
		if i == 0 {
			defaultLibraryID = audiobookLibraryID(lib)
		}
		libraryMaps = append(libraryMaps, audiobookLibraryMap(lib))
	}

	user := map[string]any{
		"id":                  userID,
		"username":            name,
		"type":                "user",
		"defaultLibraryId":    defaultLibraryID,
		"librariesAccessible": []any{}, // empty = "all libraries accessible"
		"mediaProgress":       []any{},
		"bookmarks":           []any{},
		"isOldToken":          false,
		"token":               access, // legacy field some 2.17- clients still read
		"permissions": map[string]any{
			"update":                true,
			"delete":                true,
			"download":              true,
			"accessExplicitContent": true,
		},
	}

	// x-return-tokens opt-in: when set, embed token pair on user object too
	// (some clients read from the user envelope, others from the top level).
	if strings.EqualFold(r.Header.Get("x-return-tokens"), "true") {
		user["accessToken"] = access
		user["refreshToken"] = refresh
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user":                 user,
		"userDefaultLibraryId": defaultLibraryID,
		"serverSettings": map[string]any{
			"version":  ServerVersion,
			"language": "en-us",
		},
		"ereaderDevices": []any{},
		"libraries":      libraryMaps,
		// Legacy top-level token fields for clients that read them
		// directly (mainline reads from the user object; some third-party
		// clients still read top-level).
		"accessToken":  access,
		"refreshToken": refresh,
	})
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
	libs, _ := h.deps.MediaStore.ListAudiobookLibraries(r.Context())
	libraryMaps := make([]map[string]any, 0, len(libs))
	defaultLibraryID := VirtualLibraryID
	for i, lib := range libs {
		if i == 0 {
			defaultLibraryID = audiobookLibraryID(lib)
		}
		libraryMaps = append(libraryMaps, audiobookLibraryMap(lib))
	}
	user := map[string]any{
		"id":                  a.UserID,
		"username":            a.UserID,
		"type":                "user",
		"defaultLibraryId":    defaultLibraryID,
		"librariesAccessible": []any{},
		"mediaProgress":       []any{},
		"bookmarks":           []any{},
		"isOldToken":          false,
		"permissions": map[string]any{
			"update":                true,
			"delete":                true,
			"download":              true,
			"accessExplicitContent": true,
		},
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":                 user,
		"userDefaultLibraryId": defaultLibraryID,
		"serverSettings": map[string]any{
			"version":  ServerVersion,
			"language": "en-us",
		},
		"ereaderDevices": []any{},
		"libraries":      libraryMaps,
	})
}
