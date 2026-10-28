package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
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
	// Stores holds personal key creation to the household manager
	// (canManageHouseholdAs). A key skips profile PIN verification, so minting
	// one from a child profile would hand that profile every other profile on
	// the account. Without Stores creation fails closed.
	Stores userstore.UserStoreProvider
}

// MayCreatePersonalAPIKey reports whether the caller acting as
// activeProfileID may mint a personal API key: only the household manager may,
// which on an admin account is the acting admin. verify confirms a PIN-locked
// primary profile and returns access.ErrProfileUnverified when it is not.
// The admin-role check stays with the callers, which run it first.
func (h *APIKeyHandler) MayCreatePersonalAPIKey(ctx context.Context, userID int, activeProfileID string, verify func(profileID string) error) (bool, error) {
	return mayManageHouseholdOf(ctx, h.Stores, userID, activeProfileID, verify)
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

// apiKeyScopesResponse is the feature-detection payload for API key scopes:
// clients read the scopes this server understands instead of sniffing the
// server version before offering them.
type apiKeyScopesResponse struct {
	Scopes []auth.APIKeyScope `json:"scopes"`
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
