package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
)

// AuthHandler handles authentication-related HTTP endpoints.
type AuthHandler struct {
	service *auth.Service
	jwt     *auth.JWTService
	device  *auth.DeviceLoginService
}

// NewAuthHandler creates a new AuthHandler backed by the given auth, JWT,
// and device login services.
func NewAuthHandler(service *auth.Service, jwt *auth.JWTService, device *auth.DeviceLoginService) *AuthHandler {
	return &AuthHandler{
		service: service,
		jwt:     jwt,
		device:  device,
	}
}

// --- Request/Response types ---

// loginRequest represents the JSON body of a POST /auth/login request.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Provider string `json:"provider,omitempty"`
}

// loginResponse represents the JSON body of a successful login response.
type loginResponse struct {
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	ExpiresIn    int          `json:"expires_in"`
	User         userResponse `json:"user"`
}

// setupRequest represents the JSON body of a POST /auth/setup request.
type setupRequest struct {
	Username             string `json:"username"`
	Email                string `json:"email"`
	Password             string `json:"password"`
	CreateDefaultProfile bool   `json:"create_default_profile"`
	DefaultProfileName   string `json:"default_profile_name,omitempty"`
}

// setupStatusResponse represents the JSON body of a GET /auth/setup request.
type setupStatusResponse struct {
	NeedsSetup bool `json:"needs_setup"`
}

// signupRequest represents the JSON body of a POST /auth/signup request.
type signupRequest struct {
	Username             string `json:"username"`
	Email                string `json:"email"`
	Password             string `json:"password"`
	InviteCode           string `json:"invite_code"`
	CreateDefaultProfile bool   `json:"create_default_profile"`
	DefaultProfileName   string `json:"default_profile_name,omitempty"`
}

// signupStatusResponse represents the JSON body of a GET /auth/signup request.
type signupStatusResponse struct {
	Enabled bool `json:"enabled"`
}

// refreshRequest represents the JSON body of a POST /auth/refresh request.
type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// refreshResponse represents the JSON body of a successful refresh response.
type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type pluginLaunchResponse struct {
	ExpiresIn int `json:"expires_in"`
}

type impersonationResponse struct {
	Active               bool   `json:"active"`
	ImpersonatorUserID   int    `json:"impersonator_user_id"`
	ImpersonatorUsername string `json:"impersonator_username"`
}

// userResponse represents a user in JSON responses.
type userResponse struct {
	ID              int                    `json:"id"`
	Username        string                 `json:"username"`
	Email           string                 `json:"email"`
	Role            string                 `json:"role"`
	Permissions     []string               `json:"permissions"`
	DownloadAllowed bool                   `json:"download_allowed"`
	Impersonation   *impersonationResponse `json:"impersonation,omitempty"`
}

// sessionResponse represents a session in JSON responses.
type sessionResponse struct {
	ID         string     `json:"id"`
	DeviceName string     `json:"device_name"`
	IPAddress  string     `json:"ip_address"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// sessionsListResponse represents the JSON body of a GET /auth/sessions response.
type sessionsListResponse struct {
	Sessions []sessionResponse `json:"sessions"`
}

// errorResponse represents an error in JSON responses.
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type authProviderResponse struct {
	ID             string `json:"id"`
	DisplayName    string `json:"display_name"`
	Mode           string `json:"mode"`
	Default        bool   `json:"default"`
	IconURL        string `json:"icon_url,omitempty"`
	InstallationID int    `json:"installation_id,omitempty"`
}

// --- Handler methods ---

// HandleLogin handles POST /auth/login.
func (h *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Username and password are required")
		return
	}

	// Extract device name from User-Agent header and IP from request.
	deviceName := r.UserAgent()
	ip := clientip.FromContext(r.Context())

	pair, user, err := h.service.LoginWithProvider(r.Context(), req.Provider, req.Username, req.Password, deviceName, ip)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password")
			return
		}
		if errors.Is(err, auth.ErrUserDisabled) {
			writeError(w, http.StatusForbidden, "user_disabled", "User account is disabled")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		return
	}

	writeJSON(w, http.StatusOK, buildLoginResponse(pair, user, nil))
}

func (h *AuthHandler) HandleProviders(w http.ResponseWriter, r *http.Request) {
	providers := h.service.ListProviders()
	response := make([]authProviderResponse, 0, len(providers))
	for _, provider := range providers {
		response = append(response, authProviderResponse{
			ID:             provider.ID,
			DisplayName:    provider.DisplayName,
			Mode:           provider.Mode,
			Default:        provider.Default,
			IconURL:        provider.IconURL,
			InstallationID: provider.InstallationID,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

// HandleSetupStatus handles GET /auth/setup.
func (h *AuthHandler) HandleSetupStatus(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.service.NeedsSetup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		return
	}

	writeJSON(w, http.StatusOK, setupStatusResponse{
		NeedsSetup: needsSetup,
	})
}

// HandleSetup handles POST /auth/setup.
func (h *AuthHandler) HandleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Username == "" || req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Username, email, and password are required")
		return
	}

	deviceName := r.UserAgent()
	ip := clientip.FromContext(r.Context())

	pair, user, err := h.service.SetupInitialUser(
		r.Context(),
		req.Username,
		req.Email,
		req.Password,
		req.CreateDefaultProfile,
		req.DefaultProfileName,
		deviceName,
		ip,
	)
	if err != nil {
		if errors.Is(err, auth.ErrSetupAlreadyComplete) {
			writeError(w, http.StatusUnauthorized, "setup_complete", "Initial setup has already been completed")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		return
	}

	writeJSON(w, http.StatusCreated, buildLoginResponse(pair, user, nil))
}

// HandleLogout handles POST /auth/logout. Requires authentication.
func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	claims, err := h.extractClaims(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or missing authentication token")
		return
	}

	if err := h.service.Logout(r.Context(), claims.SessionID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleEndImpersonation handles POST /auth/impersonation/end. Requires authentication.
func (h *AuthHandler) HandleEndImpersonation(w http.ResponseWriter, r *http.Request) {
	claims, err := h.extractClaims(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or missing authentication token")
		return
	}
	if claims.ImpersonatorUserID == nil {
		writeError(w, http.StatusBadRequest, "not_impersonating", "No active impersonation session")
		return
	}

	if err := h.service.EndImpersonation(r.Context(), claims.SessionID, *claims.ImpersonatorUserID); err != nil {
		if errors.Is(err, auth.ErrNotImpersonating) {
			writeError(w, http.StatusBadRequest, "not_impersonating", "No active impersonation session")
			return
		}
		if errors.Is(err, auth.ErrImpersonationNotAllowed) {
			writeError(w, http.StatusForbidden, "impersonation_not_allowed", "Impersonation is not allowed")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleRefresh handles POST /auth/refresh.
func (h *AuthHandler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Refresh token is required")
		return
	}

	pair, err := h.service.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		if errors.Is(err, auth.ErrSessionRevoked) {
			writeError(w, http.StatusUnauthorized, "session_revoked", "Session has been revoked")
			return
		}
		if errors.Is(err, auth.ErrInvalidToken) || errors.Is(err, auth.ErrExpiredToken) {
			writeError(w, http.StatusUnauthorized, "invalid_token", "Invalid or expired refresh token")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid_token", "Invalid or expired refresh token")
		return
	}

	writeJSON(w, http.StatusOK, refreshResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
	})
}

func (h *AuthHandler) HandlePluginLaunch(w http.ResponseWriter, r *http.Request) {
	claims, err := h.extractClaims(r)
	if err != nil || claims.SessionID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or missing authentication token")
		return
	}

	const ttl = 5 * time.Minute
	token, err := h.jwt.GeneratePluginAccessToken(claims.UserID, claims.Role, claims.SessionID, ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to prepare plugin access")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.PluginAccessCookieName,
		Value:    token,
		Path:     "/api/v1",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecureRequest(r),
	})
	writeJSON(w, http.StatusOK, pluginLaunchResponse{ExpiresIn: int(ttl.Seconds())})
}

// HandleMe handles GET /auth/me. Requires authentication.
func (h *AuthHandler) HandleMe(w http.ResponseWriter, r *http.Request) {
	claims, err := h.extractClaims(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or missing authentication token")
		return
	}

	user, err := h.service.GetCurrentUser(r.Context(), claims)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		return
	}

	impersonator, err := h.loadImpersonator(r.Context(), claims)
	if err != nil && !auth.IsNotFound(err) {
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		return
	}

	writeJSON(w, http.StatusOK, buildUserResponse(user, claims.ImpersonatorUserID, impersonator))
}

// HandleListSessions handles GET /auth/sessions. Requires authentication.
func (h *AuthHandler) HandleListSessions(w http.ResponseWriter, r *http.Request) {
	claims, err := h.extractClaims(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or missing authentication token")
		return
	}

	sessions, err := h.service.GetSessions(r.Context(), claims.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		return
	}

	resp := sessionsListResponse{
		Sessions: make([]sessionResponse, 0, len(sessions)),
	}
	for _, s := range sessions {
		resp.Sessions = append(resp.Sessions, sessionResponse{
			ID:         s.ID,
			DeviceName: s.DeviceName,
			IPAddress:  s.IPAddress,
			CreatedAt:  s.CreatedAt,
			ExpiresAt:  s.ExpiresAt,
			RevokedAt:  s.RevokedAt,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleDeleteSession handles DELETE /auth/sessions/{id}. Requires authentication.
func (h *AuthHandler) HandleDeleteSession(w http.ResponseWriter, r *http.Request) {
	claims, err := h.extractClaims(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or missing authentication token")
		return
	}

	sessionID := chi.URLParam(r, "id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Session ID is required")
		return
	}

	err = h.service.RevokeSession(r.Context(), sessionID, claims.UserID)
	if err != nil {
		if auth.IsSessionNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "Session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleSignupStatus handles GET /auth/signup.
func (h *AuthHandler) HandleSignupStatus(w http.ResponseWriter, r *http.Request) {
	enabled, err := h.service.IsSignupEnabled(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		return
	}
	writeJSON(w, http.StatusOK, signupStatusResponse{Enabled: enabled})
}

// HandleSignup handles POST /auth/signup.
func (h *AuthHandler) HandleSignup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Username == "" || req.Email == "" || req.Password == "" || req.InviteCode == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Username, email, password, and invite code are required")
		return
	}

	deviceName := r.UserAgent()
	ip := clientip.FromContext(r.Context())

	pair, user, err := h.service.Signup(
		r.Context(),
		req.Username,
		req.Email,
		req.Password,
		req.InviteCode,
		req.CreateDefaultProfile,
		req.DefaultProfileName,
		deviceName,
		ip,
	)
	if err != nil {
		if errors.Is(err, auth.ErrSignupDisabled) {
			writeError(w, http.StatusForbidden, "signup_disabled", "Public signups are not currently enabled")
			return
		}
		if errors.Is(err, auth.ErrInviteCodeNotFound) {
			writeError(w, http.StatusBadRequest, "invalid_code", "Invalid invite code")
			return
		}
		if errors.Is(err, auth.ErrInviteCodeExhausted) {
			writeError(w, http.StatusBadRequest, "code_exhausted", "This invite code has reached its maximum uses")
			return
		}
		if errors.Is(err, auth.ErrInviteCodeDisabled) {
			writeError(w, http.StatusBadRequest, "code_disabled", "This invite code is no longer active")
			return
		}
		if auth.IsDuplicate(err) {
			writeError(w, http.StatusBadRequest, "duplicate", "Username or email already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		return
	}

	writeJSON(w, http.StatusCreated, buildLoginResponse(pair, user, nil))
}

// --- Helper functions ---

func buildLoginResponse(pair *auth.TokenPair, user *models.User, impersonator *models.User) loginResponse {
	return loginResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		User:         buildUserResponse(user, impersonatorUserID(impersonator), impersonator),
	}
}

func buildUserResponse(user *models.User, impersonatorUserID *int, impersonator *models.User) userResponse {
	resp := userResponse{
		ID:              user.ID,
		Username:        user.Username,
		Email:           user.Email,
		Role:            user.Role,
		Permissions:     auth.EffectivePermissions(user),
		DownloadAllowed: user.DownloadAllowed,
	}
	if impersonatorUserID != nil {
		resp.Impersonation = &impersonationResponse{
			Active:             true,
			ImpersonatorUserID: *impersonatorUserID,
		}
		if impersonator != nil {
			resp.Impersonation.ImpersonatorUsername = impersonator.Username
		}
	}
	return resp
}

func impersonatorUserID(impersonator *models.User) *int {
	if impersonator == nil {
		return nil
	}
	return &impersonator.ID
}

func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (h *AuthHandler) loadImpersonator(ctx context.Context, claims *auth.Claims) (*models.User, error) {
	if claims == nil || claims.ImpersonatorUserID == nil {
		return nil, nil
	}
	return h.service.GetCurrentUser(ctx, &auth.Claims{UserID: *claims.ImpersonatorUserID})
}

// extractClaims extracts JWT claims from the Authorization header.
func (h *AuthHandler) extractClaims(r *http.Request) (*auth.Claims, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, auth.ErrInvalidToken
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return nil, auth.ErrInvalidToken
	}

	return h.jwt.ValidateToken(parts[1])
}

// writeJSON marshals the given value as JSON and writes it to the response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response with the given status code,
// error code, and message.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{
		Error:   code,
		Message: message,
	})
}
