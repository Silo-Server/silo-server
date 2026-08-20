package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// APIKeyStore is the storage the API key endpoints need. It is an interface
// so the handlers can be exercised without a database.
type APIKeyStore interface {
	Create(ctx context.Context, userID int, label string, scopes []string) (*models.APIKey, error)
	ListByUser(ctx context.Context, userID int) ([]*models.APIKey, error)
	ListByUserAdmin(ctx context.Context, userID int) ([]*models.APIKey, error)
	ListAll(ctx context.Context) ([]*models.APIKeyWithUser, error)
	Delete(ctx context.Context, id int64, userID int) error
	DeleteByAdmin(ctx context.Context, id int64) error
	UpdateTier(ctx context.Context, id int64, tier string) error
}

// APIKeyHandler handles API key management endpoints.
type APIKeyHandler struct {
	repo APIKeyStore
}

// NewAPIKeyHandler creates a new APIKeyHandler.
func NewAPIKeyHandler(repo APIKeyStore) *APIKeyHandler {
	return &APIKeyHandler{repo: repo}
}

// --- Request/Response types ---

type createAPIKeyRequest struct {
	Label  string   `json:"label"`
	Scopes []string `json:"scopes,omitempty"`
}

type apiKeyResponse struct {
	ID         int64      `json:"id"`
	UserID     int        `json:"user_id"`
	Label      string     `json:"label"`
	Key        string     `json:"key"`
	RateTier   string     `json:"rate_tier"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// apiKeyScopesOrEmpty renders a key's scopes for JSON. An unscoped key stores
// NULL/nil scopes; the API always reports an array so clients never have to
// distinguish null from empty.
func apiKeyScopesOrEmpty(scopes []string) []string {
	if scopes == nil {
		return []string{}
	}
	return scopes
}

func toAPIKeyResponse(k *models.APIKey) apiKeyResponse {
	return apiKeyResponse{
		ID:         k.ID,
		UserID:     k.UserID,
		Label:      k.Label,
		Key:        k.Key,
		RateTier:   k.RateTier,
		Scopes:     apiKeyScopesOrEmpty(k.Scopes),
		CreatedAt:  k.CreatedAt,
		LastUsedAt: k.LastUsedAt,
	}
}

type adminApiKeyResponse struct {
	ID         int64      `json:"id"`
	UserID     int        `json:"user_id"`
	Username   string     `json:"username"`
	Label      string     `json:"label"`
	Key        string     `json:"key"`
	RateTier   string     `json:"rate_tier"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type adminCreateAPIKeyRequest struct {
	Label  string   `json:"label"`
	UserID *int     `json:"user_id,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
}

// requireJWTAuth checks that the request was authenticated with a JWT, not an API key.
// Returns the claims if valid, or writes a 403 and returns nil.
func requireJWTAuth(w http.ResponseWriter, r *http.Request) *auth.Claims {
	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return nil
	}
	if claims.TokenType == auth.TokenTypeAPIKey {
		writeError(w, http.StatusForbidden, "forbidden", "API key management is not accessible via API key authentication")
		return nil
	}
	return claims
}

// HandleCreateAPIKey handles POST /api-keys.
func (h *APIKeyHandler) HandleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	claims := requireJWTAuth(w, r)
	if claims == nil {
		return
	}

	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Label == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Label is required")
		return
	}

	scopes, err := auth.NormalizeAPIKeyScopes(req.Scopes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	key, err := h.repo.Create(r.Context(), claims.UserID, req.Label, scopes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create API key")
		return
	}

	writeJSON(w, http.StatusCreated, toAPIKeyResponse(key))
}

// apiKeyScopesResponse is the feature-detection payload for API key scopes:
// clients read the scopes this server understands instead of sniffing the
// server version before offering them.
type apiKeyScopesResponse struct {
	Scopes []auth.APIKeyScope `json:"scopes"`
}

// HandleListAPIKeyScopes handles GET /api-keys/scopes.
func (h *APIKeyHandler) HandleListAPIKeyScopes(w http.ResponseWriter, r *http.Request) {
	if apimw.GetClaims(r.Context()) == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	writeJSON(w, http.StatusOK, apiKeyScopesResponse{Scopes: auth.APIKeyScopeCatalog()})
}

// HandleListAPIKeys handles GET /api-keys.
func (h *APIKeyHandler) HandleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	claims := requireJWTAuth(w, r)
	if claims == nil {
		return
	}

	keys, err := h.repo.ListByUser(r.Context(), claims.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list API keys")
		return
	}

	resp := make([]apiKeyResponse, 0, len(keys))
	for _, k := range keys {
		resp = append(resp, toAPIKeyResponse(k))
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleDeleteAPIKey handles DELETE /api-keys/{id}.
func (h *APIKeyHandler) HandleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	claims := requireJWTAuth(w, r)
	if claims == nil {
		return
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid API key ID")
		return
	}

	if err := h.repo.Delete(r.Context(), id, claims.UserID); err != nil {
		if errors.Is(err, auth.ErrAPIKeyNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "API key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete API key")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleAdminListUserAPIKeys handles GET /admin/users/{userId}/api-keys.
func (h *APIKeyHandler) HandleAdminListUserAPIKeys(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.Atoi(chi.URLParam(r, "userId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid user ID")
		return
	}

	keys, err := h.repo.ListByUserAdmin(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list API keys")
		return
	}

	resp := make([]apiKeyResponse, 0, len(keys))
	for _, k := range keys {
		resp = append(resp, toAPIKeyResponse(k))
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleAdminDeleteAPIKey handles DELETE /admin/api-keys/{id}.
func (h *APIKeyHandler) HandleAdminDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid API key ID")
		return
	}

	if err := h.repo.DeleteByAdmin(r.Context(), id); err != nil {
		if errors.Is(err, auth.ErrAPIKeyNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "API key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete API key")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleAdminListAllAPIKeys handles GET /admin/api-keys.
func (h *APIKeyHandler) HandleAdminListAllAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.repo.ListAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list API keys")
		return
	}

	resp := make([]adminApiKeyResponse, 0, len(keys))
	for _, k := range keys {
		resp = append(resp, adminApiKeyResponse{
			ID:         k.ID,
			UserID:     k.UserID,
			Username:   k.Username,
			Label:      k.Label,
			Key:        k.Key,
			RateTier:   k.RateTier,
			Scopes:     apiKeyScopesOrEmpty(k.Scopes),
			CreatedAt:  k.CreatedAt,
			LastUsedAt: k.LastUsedAt,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleAdminUpdateTier handles PUT /admin/api-keys/{id}/tier.
func (h *APIKeyHandler) HandleAdminUpdateTier(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid API key ID")
		return
	}

	var req struct {
		Tier string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Tier != "standard" && req.Tier != "elevated" {
		writeError(w, http.StatusBadRequest, "bad_request", "Tier must be 'standard' or 'elevated'")
		return
	}

	if err := h.repo.UpdateTier(r.Context(), id, req.Tier); err != nil {
		if errors.Is(err, auth.ErrAPIKeyNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "API key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update tier")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// HandleAdminCreateAPIKey handles POST /admin/api-keys.
func (h *APIKeyHandler) HandleAdminCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req adminCreateAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Label == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Label is required")
		return
	}

	scopes, err := auth.NormalizeAPIKeyScopes(req.Scopes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	targetUserID := claims.UserID
	if req.UserID != nil {
		targetUserID = *req.UserID
	}

	key, err := h.repo.Create(r.Context(), targetUserID, req.Label, scopes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create API key")
		return
	}

	writeJSON(w, http.StatusCreated, toAPIKeyResponse(key))
}
