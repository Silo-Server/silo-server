package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

// Jellyfin Quick Connect, served by Silo's device sign-in
// (docs/architecture/device-login.md). A Jellyfin app opens a request with
// Initiate and shows its six-digit code. A signed-in person approves the code
// for one of their profiles: on Silo's /activate page, in a Silo app, or from
// a Jellyfin app already signed in here (Authorize). The app polls Connect
// with the request's secret until it reads Authenticated, then exchanges the
// secret for a session at /Users/AuthenticateWithQuickConnect (AuthHandler).

// QuickConnectService is the device sign-in service as Quick Connect uses it
// (auth.DeviceLoginService).
type QuickConnectService interface {
	StartQuickConnect(ctx context.Context, input auth.QuickConnectStartInput) (*auth.QuickConnectStart, error)
	QuickConnectState(ctx context.Context, secret string) (*auth.QuickConnectState, error)
	ApproveQuickConnectAsSession(ctx context.Context, code, accessToken, profileID string) error
	quickConnectCollector
}

// quickConnectResult is Jellyfin's QuickConnectResult.
type quickConnectResult struct {
	Authenticated bool   `json:"Authenticated"`
	Secret        string `json:"Secret"`
	Code          string `json:"Code"`
	DeviceID      string `json:"DeviceId"`
	DeviceName    string `json:"DeviceName"`
	AppName       string `json:"AppName"`
	AppVersion    string `json:"AppVersion"`
	DateAdded     string `json:"DateAdded"`
}

// QuickConnectHandler serves the /QuickConnect routes.
type QuickConnectHandler struct {
	// service is nil when device sign-in is not wired; Quick Connect then
	// reports itself disabled, as on a Jellyfin server that turned it off.
	service QuickConnectService
}

// NewQuickConnectHandler creates a Quick Connect handler. A nil service
// disables Quick Connect.
func NewQuickConnectHandler(service QuickConnectService) *QuickConnectHandler {
	return &QuickConnectHandler{service: service}
}

const quickConnectDisabledMessage = "Quick connect is disabled"

// HandleEnabled serves GET /QuickConnect/Enabled.
func (h *QuickConnectHandler) HandleEnabled(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.service != nil)
}

// HandleInitiate serves POST /QuickConnect/Initiate, and GET for Jellyfin
// 10.8 clients. The device fields come from the MediaBrowser authorization
// header; a client that omits them is still served, with the user agent as
// the device name.
func (h *QuickConnectHandler) HandleInitiate(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", quickConnectDisabledMessage)
		return
	}
	start, err := h.service.StartQuickConnect(r.Context(), auth.QuickConnectStartInput{
		DeviceName: authorizationValue(r, "Device"),
		AppName:    authorizationValue(r, "Client"),
		IPAddress:  clientip.FromContext(r.Context()),
		UserAgent:  r.UserAgent(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "Could not start Quick Connect")
		return
	}
	// The answer carries the secret that collects the session; GET included,
	// no cache may keep it.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, quickConnectResult{
		Secret:     start.Secret,
		Code:       start.Code,
		DeviceID:   authorizationValue(r, "DeviceId"),
		DeviceName: start.DeviceName,
		AppName:    start.AppName,
		AppVersion: authorizationValue(r, "Version"),
		DateAdded:  jellyfinDate(start.CreatedAt),
	})
}

// HandleConnect serves GET /QuickConnect/Connect?secret=. A pending request
// answers Authenticated false and an approved or collected one true. A
// denied, withdrawn, expired or unknown request answers 404, as Jellyfin
// answers a request it no longer holds, so the app stops polling.
func (h *QuickConnectHandler) HandleConnect(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", quickConnectDisabledMessage)
		return
	}
	// Jellyfin Web asks with ?Secret=; ASP.NET binds query names in any case.
	secret := newCaseInsensitiveQuery(r.URL.Query()).Get("secret")
	if secret == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "The secret is required")
		return
	}
	state, err := h.service.QuickConnectState(r.Context(), secret)
	if errors.Is(err, auth.ErrDeviceLoginNotFound) {
		writeError(w, http.StatusNotFound, "NotFound", "Unknown secret")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "Could not read Quick Connect")
		return
	}
	var authenticated bool
	switch state.Status {
	case auth.DeviceLoginStatusPending:
	case auth.DeviceLoginStatusApproved, auth.DeviceLoginStatusConsumed:
		authenticated = true
	default:
		writeError(w, http.StatusNotFound, "NotFound", "Unknown secret")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, quickConnectResult{
		Authenticated: authenticated,
		Secret:        secret,
		// Jellyfin apps redraw the code from every poll.
		Code:       state.Code,
		DeviceID:   authorizationValue(r, "DeviceId"),
		DeviceName: state.DeviceName,
		AppName:    state.AppName,
		AppVersion: authorizationValue(r, "Version"),
		DateAdded:  jellyfinDate(state.CreatedAt),
	})
}

// HandleAuthorize serves POST /QuickConnect/Authorize?code= for a Jellyfin
// app that is already signed in: the requesting app signs in as the same
// account and profile. Jellyfin lets an administrator name another user with
// userId; Silo has no such administrator here, so any other user is 403.
func (h *QuickConnectHandler) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", quickConnectDisabledMessage)
		return
	}
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	query := newCaseInsensitiveQuery(r.URL.Query())
	if raw := query.Get("userId"); raw != "" {
		if id, err := uuid.Parse(raw); err != nil || id != session.PseudoUserID {
			writeError(w, http.StatusForbidden, "Forbidden", "Quick Connect can only sign a device in as you")
			return
		}
	}
	code := query.Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "The code is required")
		return
	}
	err := h.service.ApproveQuickConnectAsSession(r.Context(), code, session.StreamAppAccessToken, session.ProfileID)
	if err != nil {
		writeAuthorizeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, true)
}

func writeAuthorizeError(w http.ResponseWriter, err error) {
	var attempts *auth.QuickConnectAttemptsError
	switch {
	case errors.As(err, &attempts):
		w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(attempts.RetryAfter)))
		writeError(w, http.StatusTooManyRequests, "TooManyRequests", "Too many Quick Connect codes tried. Try again later.")
	case errors.Is(err, auth.ErrDeviceLoginNotFound), errors.Is(err, auth.ErrDeviceLoginExpired),
		errors.Is(err, auth.ErrDeviceLoginDenied), errors.Is(err, auth.ErrDeviceLoginCanceled),
		errors.Is(err, auth.ErrDeviceLoginConsumed), errors.Is(err, auth.ErrDeviceLoginPurpose):
		// A code that is not a live Quick Connect request is one Jellyfin
		// would not find.
		writeError(w, http.StatusNotFound, "NotFound", "Unable to find request")
	case errors.Is(err, auth.ErrDeviceLoginConflict):
		writeError(w, http.StatusConflict, "Conflict", "Request is already authorized")
	case errors.Is(err, auth.ErrSessionRevoked):
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Session expired")
	case errors.Is(err, auth.ErrUserDisabled), errors.Is(err, auth.ErrDeviceLoginNoProfile):
		writeError(w, http.StatusForbidden, "Forbidden", "This account cannot authorize devices")
	default:
		writeError(w, http.StatusInternalServerError, "ServerError", "Could not authorize the request")
	}
}

// authorizationValue reads a MediaBrowser authorization field. Some clients
// URL-encode the values, as Jellyfin decodes them.
func authorizationValue(r *http.Request, key string) string {
	value := firstMediaBrowserAuthorizationValue(r, key)
	if decoded, err := url.QueryUnescape(value); err == nil {
		return decoded
	}
	return value
}

// jellyfinDate formats a time the way Jellyfin serializes DateTime.
func jellyfinDate(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.0000000Z")
}
