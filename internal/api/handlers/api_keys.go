package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	ListByUser(ctx context.Context, userID int) ([]*models.APIKeyMetadataWithUsage, error)
	ListByUserAdmin(ctx context.Context, userID int) ([]*models.APIKeyMetadataWithUsage, error)
	ListAll(ctx context.Context) ([]*models.APIKeyMetadataWithUser, error)
	Delete(ctx context.Context, id int64, userID int) error
	DeleteByAdmin(ctx context.Context, id int64) error
	UpdateTier(ctx context.Context, id int64, tier string) error
	GetMetadataByID(ctx context.Context, id int64) (*models.APIKeyMetadata, error)
	ListAllPage(ctx context.Context, after *auth.APIKeyPageKey, limit int) ([]*models.APIKeyMetadataWithUser, bool, error)
	UpdateTierConditional(ctx context.Context, id int64, tier string, guard auth.APIKeyPrecondition) (*models.APIKeyMetadata, error)
	DeleteByAdminConditional(ctx context.Context, id int64, guard auth.APIKeyPrecondition) error
}

// APIKeyHandler handles API key management endpoints.
type APIKeyHandler struct {
	repo APIKeyStore
	// Owners, when set, keeps other admins from minting, revoking or
	// retiering keys on the server Owner's account.
	Owners ownerTargetChecker
}

// checkOwnerAccount refuses the admin key operations on the Owner's account
// unless the caller is the Owner. An unknown account passes, so the operation
// reports it the way it always has.
func (h *APIKeyHandler) checkOwnerAccount(ctx context.Context, userID int) error {
	if h.Owners == nil {
		return nil
	}
	err := h.Owners.CheckOwnerTargetByID(ctx, actorUserID(ctx), userID)
	if errors.Is(err, auth.ErrNotFound) {
		return nil
	}
	return ownerError(err)
}

// checkOwnerKey is checkOwnerAccount for the account holding key id.
func (h *APIKeyHandler) checkOwnerKey(ctx context.Context, id int64) error {
	if h.Owners == nil {
		return nil
	}
	key, err := h.repo.GetMetadataByID(ctx, id)
	if errors.Is(err, auth.ErrAPIKeyNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return h.checkOwnerAccount(ctx, key.UserID)
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

// APIKeyConfiguration is the canonical editor representation. Usage and owner
// display names are separate list decorations, so they cannot invalidate edits.
// A complete credential cannot be represented by this type.
type APIKeyConfiguration struct {
	ID        int64     `json:"id"`
	UserID    int       `json:"user_id"`
	Label     string    `json:"label"`
	KeyPrefix string    `json:"key_prefix"`
	RateTier  string    `json:"rate_tier"`
	Scopes    []string  `json:"scopes"`
	CreatedAt time.Time `json:"created_at"`
	Revision  int64     `json:"revision"`
}

type APIKeyListItem struct {
	APIKeyConfiguration
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type AdminAPIKeyListItem struct {
	APIKeyListItem
	Username string `json:"username"`
}

func apiKeyConfigurationOf(k *models.APIKeyMetadata) APIKeyConfiguration {
	return APIKeyConfiguration{ID: k.ID, UserID: k.UserID, Label: k.Label, KeyPrefix: k.KeyPrefix, RateTier: k.RateTier, Scopes: apiKeyScopesOrEmpty(k.Scopes), CreatedAt: k.CreatedAt, Revision: k.Revision}
}
func apiKeyListItemOf(k *models.APIKeyMetadataWithUsage) APIKeyListItem {
	return APIKeyListItem{APIKeyConfiguration: apiKeyConfigurationOf(&k.APIKeyMetadata), LastUsedAt: k.LastUsedAt}
}
func adminAPIKeyListItemOf(k *models.APIKeyMetadataWithUser) AdminAPIKeyListItem {
	return AdminAPIKeyListItem{APIKeyListItem: apiKeyListItemOf(&k.APIKeyMetadataWithUsage), Username: k.Username}
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

	scopes, err := auth.NormalizeV1APIKeyScopes(req.Scopes)
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
	writeJSON(w, http.StatusOK, apiKeyScopesResponse{Scopes: auth.V1APIKeyScopeCatalog()})
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

	resp := make([]APIKeyListItem, 0, len(keys))
	for _, k := range keys {
		resp = append(resp, apiKeyListItemOf(k))
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

	resp := make([]APIKeyListItem, 0, len(keys))
	for _, k := range keys {
		resp = append(resp, apiKeyListItemOf(k))
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
	if err := h.checkOwnerKey(r.Context(), id); err != nil {
		writeAPIError(w, err)
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

	resp := make([]AdminAPIKeyListItem, 0, len(keys))
	for _, k := range keys {
		resp = append(resp, adminAPIKeyListItemOf(k))
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
	if err := h.checkOwnerKey(r.Context(), id); err != nil {
		writeAPIError(w, err)
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

	scopes, err := auth.NormalizeV1APIKeyScopes(req.Scopes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	targetUserID := claims.UserID
	if req.UserID != nil {
		targetUserID = *req.UserID
	}
	if err := h.checkOwnerAccount(r.Context(), targetUserID); err != nil {
		writeAPIError(w, err)
		return
	}

	key, err := h.repo.Create(r.Context(), targetUserID, req.Label, scopes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create API key")
		return
	}

	writeJSON(w, http.StatusCreated, toAPIKeyResponse(key))
}

// These application methods are shared by the canonical admin editor. HTTP
// authentication and precondition parsing stay at the transport boundary.
var ErrInvalidAPIKeyCreation = errors.New("invalid API key creation input")

func (h *APIKeyHandler) CreateAdminAPIKey(ctx context.Context, userID int, label string, scopes []string) (*models.APIKey, error) {
	if userID <= 0 || label == "" {
		return nil, ErrInvalidAPIKeyCreation
	}
	// The canonical editor is the v2 surface, so creation validates against the
	// full scope catalog. The v1 transport handlers above keep the frozen v1
	// validator so the v1 catalog stays unchanged.
	normalized, err := auth.NormalizeAPIKeyScopes(scopes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAPIKeyCreation, err)
	}
	if err := h.checkOwnerAccount(ctx, userID); err != nil {
		return nil, err
	}
	return h.repo.Create(ctx, userID, label, normalized)
}

func (h *APIKeyHandler) GetAdminAPIKey(ctx context.Context, id int64) (*APIKeyConfiguration, error) {
	key, err := h.repo.GetMetadataByID(ctx, id)
	if err != nil {
		return nil, err
	}
	out := apiKeyConfigurationOf(key)
	return &out, nil
}
func (h *APIKeyHandler) ListAdminAPIKeysPage(ctx context.Context, after *auth.APIKeyPageKey, limit int) ([]AdminAPIKeyListItem, bool, error) {
	keys, more, err := h.repo.ListAllPage(ctx, after, limit)
	if err != nil {
		return nil, false, err
	}
	out := make([]AdminAPIKeyListItem, 0, len(keys))
	for _, key := range keys {
		out = append(out, adminAPIKeyListItemOf(key))
	}
	return out, more, nil
}
func (h *APIKeyHandler) UpdateAdminAPIKeyTier(ctx context.Context, id int64, tier string, guard auth.APIKeyPrecondition) (*APIKeyConfiguration, error) {
	if err := h.checkOwnerKey(ctx, id); err != nil {
		return nil, err
	}
	key, err := h.repo.UpdateTierConditional(ctx, id, tier, guard)
	if err != nil {
		return nil, err
	}
	out := apiKeyConfigurationOf(key)
	return &out, nil
}
func (h *APIKeyHandler) DeleteAdminAPIKey(ctx context.Context, id int64, guard auth.APIKeyPrecondition) error {
	if err := h.checkOwnerKey(ctx, id); err != nil {
		return err
	}
	return h.repo.DeleteByAdminConditional(ctx, id, guard)
}

func (h *APIKeyHandler) ListAdminUserAPIKeysPage(ctx context.Context, userID int, after *auth.APIKeyPageKey, limit int) ([]AdminAPIKeyListItem, bool, error) {
	repo, ok := h.repo.(interface {
		ListByUserAdminPage(context.Context, int, *auth.APIKeyPageKey, int) ([]*models.APIKeyMetadataWithUser, bool, error)
	})
	if !ok {
		return nil, false, apiError(501, "capability_unsupported", "Account API key paging is unavailable")
	}
	keys, more, err := repo.ListByUserAdminPage(ctx, userID, after, limit)
	if err != nil {
		return nil, false, err
	}
	out := make([]AdminAPIKeyListItem, 0, len(keys))
	for _, key := range keys {
		out = append(out, adminAPIKeyListItemOf(key))
	}
	return out, more, nil
}

// ListPersonalAPIKeysPage returns only metadata belonging to the login account.
func (h *APIKeyHandler) ListPersonalAPIKeysPage(ctx context.Context, userID int, after *auth.APIKeyPageKey, limit int) ([]APIKeyListItem, bool, error) {
	rows, more, err := h.ListAdminUserAPIKeysPage(ctx, userID, after, limit)
	if err != nil {
		return nil, false, err
	}
	items := make([]APIKeyListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.APIKeyListItem)
	}
	return items, more, nil
}

// RevokePersonalAPIKey checks ownership in the deleting statement.
func (h *APIKeyHandler) RevokePersonalAPIKey(ctx context.Context, userID int, id int64) error {
	return h.repo.Delete(ctx, id, userID)
}
