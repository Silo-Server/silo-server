package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// APIKeyStore is the storage the API key endpoints need. It is an interface
// so the handlers can be exercised without a database.
type APIKeyStore interface {
	Create(ctx context.Context, userID int, label string, scopes []string) (*models.APIKey, error)
	CreateAsActor(ctx context.Context, actorID, userID int, label string, scopes []string) (*models.APIKey, error)
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
	// Owners, when set, keeps other admins from revoking or retiering keys on
	// the server Owner's account. Minting a key for another account always
	// applies the same rule, in the store, under the accounts' locks.
	Owners ownerTargetChecker
	// Stores holds personal key creation to the household manager
	// (canManageHouseholdAs). A key skips profile PIN verification, so minting
	// one from a child profile would hand that profile every other profile on
	// the account. Without Stores creation fails closed.
	Stores userstore.UserStoreProvider
	// ProfilesOutsidePostgres is set when the profile store is not the
	// Postgres one (userdb.backend=sqlite). A user key's profile binding and
	// the PIN recheck at its insert both read user_profiles in Postgres, so
	// user keys are off there whatever the server setting says.
	ProfilesOutsidePostgres bool
}

// MayCreatePersonalAPIKey reports whether the caller acting as
// activeProfileID may mint a personal API key: only the household manager may,
// which on an admin account is the acting admin. verify confirms a PIN-locked
// primary profile and returns access.ErrProfileUnverified when it is not.
// The admin-role check stays with the callers, which run it first.
func (h *APIKeyHandler) MayCreatePersonalAPIKey(ctx context.Context, userID int, activeProfileID string, verify func(profile *userstore.Profile) error) (bool, error) {
	return mayManageHouseholdOf(ctx, h.Stores, userID, activeProfileID, verify)
}

// personalAPIKeyStore is the storage behind keys an account creates for
// itself; auth.APIKeyRepository provides it.
type personalAPIKeyStore interface {
	UserAPIKeysEnabled(ctx context.Context) (bool, error)
	CreatePersonal(ctx context.Context, userID int, acting auth.APIKeyActingProfile, profileID, label string, scopes []string) (*models.APIKey, error)
	ListByUserProfilePage(ctx context.Context, userID int, profileID string, after *auth.APIKeyPageKey, limit int) ([]*models.APIKeyMetadataWithUser, bool, error)
	DeleteForProfile(ctx context.Context, id int64, userID int, profileID string) error
}

func (h *APIKeyHandler) personal() (personalAPIKeyStore, error) {
	store, ok := h.repo.(personalAPIKeyStore)
	if !ok {
		return nil, apiError(501, "capability_unsupported", "Personal API keys are unavailable")
	}
	return store, nil
}

// UserAPIKeysEnabled reports whether accounts that are not admins, and
// profiles other than the primary one, may create keys for themselves.
func (h *APIKeyHandler) UserAPIKeysEnabled(ctx context.Context) (bool, error) {
	store, err := h.personal()
	if err != nil || h.ProfilesOutsidePostgres {
		return false, nil //nolint:nilerr // A store without personal keys has them off.
	}
	return store.UserAPIKeysEnabled(ctx)
}

// MayCreateProfileAPIKey reports whether the caller acting as activeProfileID
// may mint a personal key that is not an admin's account key: one bound to
// targetProfileID, or (empty target) an account key on an account that is not
// an admin. The household manager may mint either, for any profile on the
// account. Every other profile may mint only a key bound to itself, and only
// with its own PIN proof: verify returns access.ErrProfileUnverified without
// it. Only the account's own login session mints these, never a view-as
// session or a remote-playback handoff. The acting profile comes back at the
// PIN revision it was verified against, for CreatePersonalAPIKey to recheck
// where the key is stored.
func (h *APIKeyHandler) MayCreateProfileAPIKey(ctx context.Context, userID int, activeProfileID, targetProfileID string, verify func(profile *userstore.Profile) error) (bool, auth.APIKeyActingProfile, error) {
	none := auth.APIKeyActingProfile{}
	if !apimw.GetClaims(ctx).IsOwnLoginSession() || h.Stores == nil || h.ProfilesOutsidePostgres {
		return false, none, nil
	}
	store, err := h.Stores.ForUser(ctx, userID)
	if err != nil {
		return false, none, fmt.Errorf("opening user store: %w", err)
	}
	if store == nil {
		return false, none, nil
	}
	if verify == nil {
		verify = func(*userstore.Profile) error { return access.ErrProfileUnverified }
	}
	manager, active, err := householdManagerAs(ctx, store, activeProfileID, verify)
	if err != nil {
		return false, none, err
	}
	if !manager {
		if activeProfileID == "" || targetProfileID != activeProfileID {
			return false, none, nil
		}
		if active, err = store.GetProfile(ctx, activeProfileID); err != nil || active == nil {
			return false, none, err
		}
		if err := verify(active); err != nil {
			return false, none, err
		}
	}
	if active == nil {
		// A profile-less household manager has no PIN proof to recheck.
		return true, none, nil
	}
	return true, auth.APIKeyActingProfile{ID: active.ID, PINRevision: active.PINRevision}, nil
}

// CreatePersonalAPIKey mints the key MayCreateProfileAPIKey authorized. The
// store holds it to user scopes, the server setting and the account's key
// limit in the inserting transaction.
func (h *APIKeyHandler) CreatePersonalAPIKey(ctx context.Context, userID int, acting auth.APIKeyActingProfile, profileID, label string, scopes []string) (*models.APIKey, error) {
	if userID <= 0 || label == "" {
		return nil, ErrInvalidAPIKeyCreation
	}
	normalized, err := auth.NormalizeAPIKeyScopes(scopes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAPIKeyCreation, err)
	}
	store, err := h.personal()
	if err != nil {
		return nil, err
	}
	return store.CreatePersonal(ctx, userID, acting, profileID, label, normalized)
}

// PersonalAPIKeyView decides which of the account's keys the caller acting as
// activeProfileID may list and revoke. The primary profile sees them all. Any
// other profile sees only the keys bound to it, returned as only. A request
// naming no profile sees them all: the operations run the household profile
// gate first, which refuses one on a household with a limited profile, so a
// device cannot drop the header to reach another profile's keys.
func (h *APIKeyHandler) PersonalAPIKeyView(ctx context.Context, userID int, activeProfileID string) (only string, allowed bool, err error) {
	if h.Stores == nil {
		return "", false, nil
	}
	store, err := h.Stores.ForUser(ctx, userID)
	if err != nil {
		return "", false, fmt.Errorf("opening user store: %w", err)
	}
	if store == nil {
		return "", false, nil
	}
	if activeProfileID == "" {
		return "", true, nil
	}
	active, err := store.GetProfile(ctx, activeProfileID)
	if err != nil || active == nil {
		return "", false, err
	}
	if active.IsPrimary {
		return "", true, nil
	}
	return active.ID, true, nil
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
	ProfileID string    `json:"profile_id,omitempty"`
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
	return APIKeyConfiguration{ID: k.ID, UserID: k.UserID, Label: k.Label, KeyPrefix: k.KeyPrefix, RateTier: k.RateTier, Scopes: apiKeyScopesOrEmpty(k.Scopes), ProfileID: k.ProfileID, CreatedAt: k.CreatedAt, Revision: k.Revision}
}
func apiKeyListItemOf(k *models.APIKeyMetadataWithUsage) APIKeyListItem {
	return APIKeyListItem{APIKeyConfiguration: apiKeyConfigurationOf(&k.APIKeyMetadata), LastUsedAt: k.LastUsedAt}
}
func adminAPIKeyListItemOf(k *models.APIKeyMetadataWithUser) AdminAPIKeyListItem {
	return AdminAPIKeyListItem{APIKeyListItem: apiKeyListItemOf(&k.APIKeyMetadataWithUsage), Username: k.Username}
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
	return h.createAsActor(ctx, userID, label, normalized)
}

// createAsActor mints a key on account userID for the request's account,
// refusing it under the Owner rules in the transaction that inserts it, so a
// concurrent promotion or ownership transfer cannot leave an admin holding a
// key it may no longer mint.
func (h *APIKeyHandler) createAsActor(ctx context.Context, userID int, label string, scopes []string) (*models.APIKey, error) {
	key, err := h.repo.CreateAsActor(ctx, actorUserID(ctx), userID, label, scopes)
	return key, ownerError(err)
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

// ListPersonalAPIKeysPage returns only metadata belonging to the login
// account, narrowed to the keys bound to profileID when it is set.
func (h *APIKeyHandler) ListPersonalAPIKeysPage(ctx context.Context, userID int, profileID string, after *auth.APIKeyPageKey, limit int) ([]APIKeyListItem, bool, error) {
	store, err := h.personal()
	if err != nil {
		return nil, false, err
	}
	rows, more, err := store.ListByUserProfilePage(ctx, userID, profileID, after, limit)
	if err != nil {
		return nil, false, err
	}
	items := make([]APIKeyListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, apiKeyListItemOf(&row.APIKeyMetadataWithUsage))
	}
	return items, more, nil
}

// RevokePersonalAPIKey checks ownership, and the profile binding when
// profileID is set, in the deleting statement.
func (h *APIKeyHandler) RevokePersonalAPIKey(ctx context.Context, userID int, profileID string, id int64) error {
	store, err := h.personal()
	if err != nil {
		return err
	}
	return store.DeleteForProfile(ctx, id, userID, profileID)
}
