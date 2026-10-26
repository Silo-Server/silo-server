package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// ExternalIdentityView is one external identity linked to an account, as
// the account and administrator views show it.
type ExternalIdentityView struct {
	ID              int64
	InstallationID  int
	ProviderID      string
	ProviderName    string
	ExternalSubject string
	Issuer          string
	Username        string
	Email           string
	DisplayName     string
	LinkedAt        time.Time
	LastSignInAt    *time.Time
	LastCheckedAt   *time.Time
	// LastCheckStatus is the latest provider answer (auth.CheckStatus*), ""
	// before the first.
	LastCheckStatus string
}

// AdminIdentityLinkInput is an administrator linking an account to a
// provider identity by its exact subject.
type AdminIdentityLinkInput struct {
	InstallationID  int
	ExternalSubject string
	Username        string
	Email           string
	DisplayName     string
}

// ExternalSignInHandler serves identity management for accounts and
// administrators, sign-in discovery and the auth plugin connection test.
type ExternalSignInHandler struct {
	identities *auth.IdentityService
	providers  interface {
		ListProviders() []auth.LoginProviderInfo
	}
	users   UserRepository
	plugins interface {
		TestAdminPluginAuthConnection(ctx context.Context, id int, capabilityID string, staged []plugins.StagedConfig) (plugins.AuthConnectionTestResult, error)
	}
	// credentials links a directory identity by its username and password;
	// nil when the providers source cannot.
	credentials credentialsLinker
	// network links the network identity of the request's overlay peer;
	// nil when the providers source cannot.
	network networkLinker
}

// networkLinker is the network identity linking of *auth.Service.
type networkLinker interface {
	LinkNetworkIdentity(ctx context.Context, in auth.NetworkLinkInput) (*auth.LinkedIdentity, error)
}

// NetworkLinkInput is the caller linking the network identity of its
// request's overlay peer, after re-entering its local password.
type NetworkLinkInput struct {
	InstallationID int
	Password       string
}

// credentialsLinker is the directory (credentials provider) linking of
// *auth.Service.
type credentialsLinker interface {
	LinkCredentialsIdentity(ctx context.Context, in auth.CredentialsLinkInput) (*auth.LinkedIdentity, error)
}

// CredentialsLinkInput is the caller linking the directory identity it
// proves with the directory username and password, after re-entering its
// local password.
type CredentialsLinkInput struct {
	InstallationID    int
	Password          string
	DirectoryUsername string
	DirectoryPassword string
}

// NewExternalSignInHandler wires the handler. providers names the provider
// of each identity; pluginHandler may be nil when plugins are not wired.
func NewExternalSignInHandler(identities *auth.IdentityService, providers interface {
	ListProviders() []auth.LoginProviderInfo
}, users UserRepository, pluginHandler *PluginHandler) *ExternalSignInHandler {
	h := &ExternalSignInHandler{identities: identities, providers: providers, users: users}
	if linker, ok := providers.(credentialsLinker); ok {
		h.credentials = linker
	}
	if linker, ok := providers.(networkLinker); ok {
		h.network = linker
	}
	if pluginHandler != nil {
		h.plugins = pluginHandler
	}
	return h
}

// IdentitiesAvailable reports whether identity management is wired.
func (h *ExternalSignInHandler) IdentitiesAvailable() bool {
	return h != nil && h.identities != nil
}

// ConnectionTestAvailable reports whether the auth plugin connection test is
// wired.
func (h *ExternalSignInHandler) ConnectionTestAvailable() bool {
	return h != nil && h.plugins != nil
}

func (h *ExternalSignInHandler) views(identities []auth.LinkedIdentity) []ExternalIdentityView {
	names := map[int]auth.LoginProviderInfo{}
	if h.providers != nil {
		for _, provider := range h.providers.ListProviders() {
			if provider.InstallationID != 0 {
				names[provider.InstallationID] = provider
			}
		}
	}
	out := make([]ExternalIdentityView, 0, len(identities))
	for _, identity := range identities {
		provider := names[identity.InstallationID]
		out = append(out, ExternalIdentityView{
			ID: identity.ID, InstallationID: identity.InstallationID,
			ProviderID: provider.ID, ProviderName: provider.DisplayName,
			ExternalSubject: identity.ExternalSubject, Issuer: identity.Issuer,
			Username: identity.Username, Email: identity.Email, DisplayName: identity.DisplayName,
			LinkedAt: identity.LinkedAt, LastSignInAt: identity.LastSignInAt, LastCheckedAt: identity.LastCheckedAt,
			LastCheckStatus: identity.LastCheckStatus,
		})
	}
	return out
}

// ListIdentities lists an account's identities, for the account itself
// and for an administrator; the caller authorizes the request.
func (h *ExternalSignInHandler) ListIdentities(ctx context.Context, userID int) ([]ExternalIdentityView, error) {
	identities, err := h.identities.ListForUser(ctx, userID)
	if err != nil {
		return nil, identityError(err)
	}
	return h.views(identities), nil
}

// CanUnlinkAccountIdentity reports whether the caller can disconnect one
// of its identities now (UnlinkAccountIdentity's rule).
func (h *ExternalSignInHandler) CanUnlinkAccountIdentity(ctx context.Context, userID int) (bool, error) {
	allowed, err := h.identities.CanUnlinkOwn(ctx, userID)
	if err != nil {
		return false, identityError(err)
	}
	return allowed, nil
}

// UnlinkAccountIdentity removes one of the caller's identities while the
// account can still sign in another way.
func (h *ExternalSignInHandler) UnlinkAccountIdentity(ctx context.Context, userID int, identityID int64) error {
	return identityError(h.identities.UnlinkOwn(ctx, userID, identityID))
}

// CredentialsLinkingAvailable reports whether an account can link a
// directory identity with its credentials.
func (h *ExternalSignInHandler) CredentialsLinkingAvailable() bool {
	return h.IdentitiesAvailable() && h.credentials != nil
}

// LinkAccountIdentityCredentials links the directory identity the caller
// proves with its directory username and password. The errors are the auth
// package's (auth.Service.LinkCredentialsIdentity); the transport renders
// them.
func (h *ExternalSignInHandler) LinkAccountIdentityCredentials(ctx context.Context, userID int, in CredentialsLinkInput) (ExternalIdentityView, error) {
	if h.credentials == nil {
		return ExternalIdentityView{}, apiError(http.StatusServiceUnavailable, "unavailable", "Directory linking is not configured")
	}
	linked, err := h.credentials.LinkCredentialsIdentity(ctx, auth.CredentialsLinkInput{
		UserID: userID, InstallationID: in.InstallationID, Password: in.Password,
		DirectoryUsername: in.DirectoryUsername, DirectoryPassword: in.DirectoryPassword,
	})
	if err != nil {
		return ExternalIdentityView{}, err
	}
	return h.views([]auth.LinkedIdentity{*linked})[0], nil
}

// NetworkLinkingAvailable reports whether an account can link the network
// identity of its request's overlay peer.
func (h *ExternalSignInHandler) NetworkLinkingAvailable() bool {
	return h.IdentitiesAvailable() && h.network != nil
}

// LinkAccountIdentityNetwork links the network identity of the request's
// overlay peer to the caller's account. The errors are the auth package's
// (auth.Service.LinkNetworkIdentity); the transport renders them.
func (h *ExternalSignInHandler) LinkAccountIdentityNetwork(ctx context.Context, userID int, in NetworkLinkInput) (ExternalIdentityView, error) {
	if h.network == nil {
		return ExternalIdentityView{}, apiError(http.StatusServiceUnavailable, "unavailable", "Network sign-in linking is not configured")
	}
	linked, err := h.network.LinkNetworkIdentity(ctx, auth.NetworkLinkInput{
		UserID: userID, InstallationID: in.InstallationID, Password: in.Password,
	})
	if err != nil {
		return ExternalIdentityView{}, err
	}
	return h.views([]auth.LinkedIdentity{*linked})[0], nil
}

// adminIdentityTarget applies the Owner rules to identity changes: only the
// Owner changes another admin's sign-in, and a scoped API key never changes
// an admin's. The identity service repeats these checks against the locked
// actor and target when it writes, since their standing may change here.
func (h *ExternalSignInHandler) adminIdentityTarget(ctx context.Context, userID int) error {
	target, err := h.users.GetByID(ctx, userID)
	if err != nil {
		return identityError(err)
	}
	if actorIsScopedAPIKey(ctx) && target.Role == models.RoleAdmin {
		return apiError(http.StatusForbidden, "insufficient_scope", "A scoped API key may not change an admin account's sign-in")
	}
	actor, err := requestOwnerActor(ctx, h.users)
	if err != nil {
		return err
	}
	return ownerError(auth.CheckOwnerTarget(actor, target))
}

// LinkAdminUserIdentity links an account to a provider identity.
func (h *ExternalSignInHandler) LinkAdminUserIdentity(ctx context.Context, userID int, in AdminIdentityLinkInput) (ExternalIdentityView, error) {
	if strings.TrimSpace(in.ExternalSubject) == "" {
		return ExternalIdentityView{}, fieldError("external_subject", "The provider's exact subject is required")
	}
	if err := h.adminIdentityTarget(ctx, userID); err != nil {
		return ExternalIdentityView{}, err
	}
	linked, err := h.identities.AdminLink(ctx, auth.AdminLinkInput{
		UserID: userID, InstallationID: in.InstallationID, ActorID: actorUserID(ctx),
		ScopedAPIKey: actorIsScopedAPIKey(ctx),
		Identity: auth.ExternalIdentity{
			Subject: in.ExternalSubject, Username: strings.TrimSpace(in.Username),
			Email: strings.TrimSpace(in.Email), DisplayName: strings.TrimSpace(in.DisplayName),
		},
	})
	if err != nil {
		return ExternalIdentityView{}, identityError(err)
	}
	return h.views([]auth.LinkedIdentity{*linked})[0], nil
}

// UnlinkAdminUserIdentity removes an identity from an account.
func (h *ExternalSignInHandler) UnlinkAdminUserIdentity(ctx context.Context, userID int, identityID int64) error {
	if err := h.adminIdentityTarget(ctx, userID); err != nil {
		return err
	}
	return identityError(h.identities.AdminUnlink(ctx, userID, identityID, actorUserID(ctx), actorIsScopedAPIKey(ctx)))
}

// TestAuthBinding runs the auth plugin's connection test on staged
// configuration.
func (h *ExternalSignInHandler) TestAuthBinding(ctx context.Context, installationID int, capabilityID string, staged []plugins.StagedConfig) (plugins.AuthConnectionTestResult, error) {
	if h.plugins == nil {
		return plugins.AuthConnectionTestResult{}, apiError(http.StatusServiceUnavailable, "unavailable", "Plugin service not configured")
	}
	return h.plugins.TestAdminPluginAuthConnection(ctx, installationID, capabilityID, staged)
}

// identityError maps identity management failures to their codes.
func identityError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrNotFound):
		return apiError(http.StatusNotFound, "not_found", "Account not found")
	case errors.Is(err, auth.ErrIdentityNotFound):
		return apiError(http.StatusNotFound, "not_found", "Identity not found")
	case errors.Is(err, auth.ErrLastSignInMethod):
		return apiError(http.StatusConflict, "last_sign_in_method", "The account has no other way to sign in; ask an administrator to set a password for it first")
	case errors.Is(err, auth.ErrIdentityLinkedElsewhere):
		return apiError(http.StatusConflict, "identity_linked_elsewhere", "This identity is already linked to another account")
	case errors.Is(err, auth.ErrAccountAlreadyLinked):
		return apiError(http.StatusConflict, "already_linked", "The account is already linked to this provider")
	case errors.Is(err, auth.ErrUnknownAuthInstallation):
		return fieldError("installation_id", "The installation has no sign-in provider binding")
	case errors.Is(err, auth.ErrScopedKeyAdminIdentity):
		return apiError(http.StatusForbidden, "insufficient_scope", "A scoped API key may not change an admin account's sign-in")
	case errors.Is(err, auth.ErrNotPermitted):
		return apiError(http.StatusForbidden, "permission_denied", "The acting account is no longer an enabled administrator")
	}
	return ownerError(err)
}
