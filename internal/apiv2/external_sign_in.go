package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// The external sign-in domain (docs/architecture/external-sign-in.md): the
// identities an OIDC or LDAP auth plugin links to accounts, their management
// by the account and by administrators, and the auth plugin connection test.

// ExternalSignInService is the slice of *handlers.ExternalSignInHandler the
// operations use.
type ExternalSignInService interface {
	IdentitiesAvailable() bool
	ConnectionTestAvailable() bool
	ListIdentities(ctx context.Context, userID int) ([]handlers.ExternalIdentityView, error)
	CanUnlinkAccountIdentity(ctx context.Context, userID int) (bool, error)
	UnlinkAccountIdentity(ctx context.Context, userID int, identityID int64) error
	LinkAdminUserIdentity(ctx context.Context, userID int, in handlers.AdminIdentityLinkInput) (handlers.ExternalIdentityView, error)
	UnlinkAdminUserIdentity(ctx context.Context, userID int, identityID int64) error
	TestAuthBinding(ctx context.Context, installationID int, capabilityID string, staged []plugins.StagedConfig) (plugins.AuthConnectionTestResult, error)
	CredentialsLinkingAvailable() bool
	LinkAccountIdentityCredentials(ctx context.Context, userID int, in handlers.CredentialsLinkInput) (handlers.ExternalIdentityView, error)
	NetworkLinkingAvailable() bool
	LinkAccountIdentityNetwork(ctx context.Context, userID int, in handlers.NetworkLinkInput) (handlers.ExternalIdentityView, error)
}

// ExternalSignInCapabilities describes the external sign-in operations this
// server serves.
type ExternalSignInCapabilities struct {
	Capability
	Available           bool `json:"available" doc:"Whether identity storage is configured"`
	Identities          bool `json:"identities" doc:"Whether listAccountIdentities and deleteAccountIdentity are served"`
	AdminIdentities     bool `json:"admin_identities" doc:"Whether administrators can list, link and unlink account identities"`
	BreakGlass          bool `json:"break_glass" doc:"Whether updateAdminUser accepts break_glass and the server honors the auth.local_password_login setting"`
	ConnectionTest      bool `json:"connection_test" doc:"Whether testAdminPluginAuthBinding is served; the plugin must also declare connection_test"`
	LiveProviderChanges bool `json:"live_provider_changes" doc:"Whether auth binding and auth plugin changes apply without a server restart"`
	ProviderRecheck     bool `json:"provider_recheck" doc:"Whether refreshSession re-checks sessions opened through the external provider with that provider (auth.provider_recheck_interval, auth.provider_recheck_outage_policy), and admin identities report last_check_status"`
	// CredentialsLinking is directory (LDAP) linking, which needs no OAuth
	// handshake, so it sits with the identity operations.
	CredentialsLinking bool `json:"credentials_linking" doc:"Whether linkAccountIdentityWithCredentials links a directory (LDAP) identity to the caller's account with the directory username and password"`
	// NetworkSignIn is sign-in and linking through a network identity
	// provider (a network access plugin such as Tailscale), which needs no
	// password or browser.
	NetworkSignIn bool `json:"network_sign_in" doc:"Whether signInWithNetworkIdentity and linkAccountIdentityWithNetwork are served. Whether a given request may use them is answered by listAuthProviders, which lists a network provider only to a request that arrived through that provider's network"`
}

// ExternalSignInCapabilitiesOutput is the getExternalSignInCapabilities
// response.
type ExternalSignInCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         ExternalSignInCapabilities
}

func (c ExternalSignInCapabilities) capabilityState() string {
	return configuredCapabilityState(c.Available)
}

// AccountIdentity is one external identity linked to the caller's account.
type AccountIdentity struct {
	ID             ID              `json:"id" doc:"Identity id" example:"4"`
	InstallationID ID              `json:"installation_id" doc:"Auth plugin installation the identity belongs to" example:"3"`
	ProviderID     string          `json:"provider_id" doc:"Sign-in provider id as listAuthProviders shows it; empty while that provider is not enabled" example:"plugin:3:oidc"`
	ProviderName   string          `json:"provider_name" doc:"Provider label; empty while that provider is not enabled" example:"Company SSO"`
	Username       string          `json:"username" doc:"Username at the provider, as of the last sign-in; may be empty" example:"alice"`
	Email          string          `json:"email" doc:"Email at the provider, as of the last sign-in; may be empty" example:"alice@example.test"`
	DisplayName    string          `json:"display_name" doc:"Name at the provider; may be empty" example:"Alice Example"`
	LinkedAt       Instant         `json:"linked_at" example:"2026-01-02T03:04:05.678Z"`
	LastSignInAt   NullableInstant `json:"last_sign_in_at" doc:"Most recent sign-in through this identity; null when none" example:"2026-01-02T03:04:05.678Z"`
	LastCheckedAt  NullableInstant `json:"last_checked_at" doc:"Most recent provider answer about the identity: a sign-in through it, or a provider re-check (at session refresh or the scheduled recheck_external_identities pass); null when none" example:"2026-01-02T03:04:05.678Z"`
}

// AccountIdentityCollection is the listAccountIdentities response body.
type AccountIdentityCollection struct {
	Collection[AccountIdentity]
	CanUnlink bool `json:"can_unlink" doc:"Whether deleteAccountIdentity would disconnect an identity now: the account has another identity, or its local password still signs in (the account's password sign-in is on, and local password sign-in is on for the server or the account is break-glass). False with no identity" example:"true"`
}

// AccountIdentityCollectionOutput is the listAccountIdentities response.
type AccountIdentityCollectionOutput struct {
	Body AccountIdentityCollection
}

// AdminUserIdentity is one account identity as an administrator sees it.
type AdminUserIdentity struct {
	AccountIdentity
	ExternalSubject string `json:"external_subject" doc:"The provider's exact subject (OIDC issuer|sub, LDAP unique id)" example:"https://id.example.test/realms/silo|8f14e45f"`
	Issuer          string `json:"issuer" doc:"Who asserted the subject, as the provider last said; may be empty" example:"https://id.example.test"`
	LastCheckStatus string `json:"last_check_status" enum:"none,active,not_found,disabled,not_permitted,unsupported,unavailable" doc:"That answer: active; not_found, disabled or not_permitted (the account's sessions were revoked); unsupported (the provider cannot re-check this identity, so its sessions end an absolute age after sign-in, and the account's API keys and Audiobookshelf sessions are revoked once the person has not signed in through the provider for that long); unavailable (the provider could not be reached; retried at the next refresh or scheduled pass); none before the first" example:"active"`
}

// identityCheckStatusNone is last_check_status before the provider's first
// answer about an identity.
const identityCheckStatusNone = "none"

// AdminUserIdentityCollection is the listAdminUserIdentities response body.
type AdminUserIdentityCollection struct {
	Collection[AdminUserIdentity]
}

// AdminUserIdentityCollectionOutput is the listAdminUserIdentities response.
type AdminUserIdentityCollectionOutput struct {
	Body AdminUserIdentityCollection
}

// AdminUserIdentityOutput is the createAdminUserIdentity response.
type AdminUserIdentityOutput struct {
	Location string `header:"Location"`
	Body     AdminUserIdentity
}

// AccountIdentityInput names one of the caller's identities.
type AccountIdentityInput struct {
	ID ID `path:"id" doc:"Identity id" example:"4"`
}

// AdminUserIdentitiesInput names an account.
type AdminUserIdentitiesInput struct {
	ID ID `path:"id" doc:"Account id" example:"1"`
}

// AdminUserIdentityInput names one identity of an account.
type AdminUserIdentityInput struct {
	ID         ID `path:"id" doc:"Account id" example:"1"`
	IdentityID ID `path:"identity_id" doc:"Identity id" example:"4"`
}

// AdminUserIdentityCreateInput links an account to a provider identity.
type AdminUserIdentityCreateInput struct {
	ID      ID `path:"id" doc:"Account id" example:"1"`
	RawBody []byte
	Body    struct {
		InstallationID  ID     `json:"installation_id" doc:"Auth plugin installation with a sign-in binding" example:"3"`
		ExternalSubject string `json:"external_subject" minLength:"1" maxLength:"1024" doc:"The provider's exact subject: OIDC issuer|sub (Entra tenant|object id), or the LDAP unique id attribute value" example:"https://id.example.test/realms/silo|8f14e45f"`
		Username        string `json:"username,omitempty" maxLength:"255" doc:"Username at the provider, shown until the first sign-in refreshes it"`
		Email           string `json:"email,omitempty" maxLength:"320" doc:"Email at the provider, shown until the first sign-in refreshes it"`
		DisplayName     string `json:"display_name,omitempty" maxLength:"255" doc:"Name at the provider, shown until the first sign-in refreshes it"`
	}
}

// AccountIdentityCredentialsLinkInput links a directory identity to the
// caller's account.
type AccountIdentityCredentialsLinkInput struct {
	RawBody []byte
	Body    struct {
		InstallationID    ID     `json:"installation_id" doc:"The credentials (LDAP) auth plugin installation, as listAuthProviders shows it" example:"4"`
		Password          string `json:"password" minLength:"1" maxLength:"1024" doc:"The account's current local password" example:"correct horse battery staple"`
		Username          string `json:"username" minLength:"1" maxLength:"256" doc:"Username at the directory" example:"alice"`
		DirectoryPassword string `json:"directory_password" minLength:"1" maxLength:"1024" doc:"Password at the directory; passed to the directory plugin only" example:"directory password"`
	}
}

// AccountIdentityNetworkLinkInput links the network identity of the
// request's overlay peer to the caller's account.
type AccountIdentityNetworkLinkInput struct {
	RawBody []byte
	Body    struct {
		InstallationID ID     `json:"installation_id" doc:"The network identity auth plugin installation, as listAuthProviders shows it" example:"5"`
		Password       string `json:"password" minLength:"1" maxLength:"1024" doc:"The account's current local password" example:"correct horse battery staple"`
	}
}

// AccountIdentityOutput is the linkAccountIdentityWithCredentials and
// linkAccountIdentityWithNetwork response.
type AccountIdentityOutput struct {
	Location string `header:"Location"`
	Body     AccountIdentity
}

// AdminPluginAuthBindingTestInput runs an auth plugin's connection test on
// staged settings.
type AdminPluginAuthBindingTestInput struct {
	ID      ID `path:"id" pattern:"^[1-9][0-9]*$"`
	RawBody []byte
	Body    struct {
		CapabilityID string                   `json:"capability_id,omitempty" maxLength:"256" doc:"auth_provider.v1 capability to test; empty tests the plugin's first one"`
		Config       []AdminPluginConfigWrite `json:"config,omitempty" maxItems:"64" doc:"Staged global configuration entries, unsaved, merged as updateAdminPluginInstallationConfig would merge them. Entries and fields not named here use the stored values; a declared non-secret field sent as null or a blank string is cleared so the plugin default applies; blank secret fields use the stored secret; clear_secrets drops it from the test"`
	}
}

// AuthConnectionTestStep is one check the plugin ran.
type AuthConnectionTestStep struct {
	ID      string `json:"id" doc:"Stable check identifier" example:"discovery"`
	Label   string `json:"label" doc:"Operator-facing check name" example:"Discovery document reachable"`
	OK      bool   `json:"ok" example:"true"`
	Message string `json:"message" doc:"Result or failure reason from the plugin" example:"Loaded https://id.example.test/.well-known/openid-configuration"`
}

// AuthConnectionTestResult is the plugin's verdict.
type AuthConnectionTestResult struct {
	OK    bool                     `json:"ok" doc:"True only when every check passed"`
	Steps []AuthConnectionTestStep `json:"steps" doc:"Checks in the order they ran"`
	// CallbackURL is the redirect URI an OAuth provider must have
	// registered for this installation.
	CallbackURL string `json:"callback_url" doc:"Redirect URI to register at an OAuth (OIDC) provider for this installation, on the public URL; empty when no public URL is configured. Password (LDAP) providers do not use it" example:"https://silo.example.test/api/v2/auth/oauth/3/callback"`
}

// AuthConnectionTestOutput is the testAdminPluginAuthBinding response.
type AuthConnectionTestOutput struct {
	Body AuthConnectionTestResult
}

func (reg *Registry) externalSignIn() (ExternalSignInService, *Problem) {
	if reg.deps.ExternalSignIn == nil || !reg.deps.ExternalSignIn.IdentitiesAvailable() {
		return nil, unavailable("external sign-in")
	}
	return reg.deps.ExternalSignIn, nil
}

func accountIdentityOf(v handlers.ExternalIdentityView) AccountIdentity {
	out := AccountIdentity{
		ID: IDFromInt(v.ID), InstallationID: IDFromInt(int64(v.InstallationID)),
		ProviderID: v.ProviderID, ProviderName: v.ProviderName,
		Username: v.Username, Email: v.Email, DisplayName: v.DisplayName,
		LinkedAt: NewInstant(v.LinkedAt),
	}
	if v.LastSignInAt != nil {
		out.LastSignInAt = NullableInstant{Valid: true, Time: NewInstant(*v.LastSignInAt)}
	}
	if v.LastCheckedAt != nil {
		out.LastCheckedAt = NullableInstant{Valid: true, Time: NewInstant(*v.LastCheckedAt)}
	}
	return out
}

func adminUserIdentityOf(v handlers.ExternalIdentityView) AdminUserIdentity {
	out := AdminUserIdentity{AccountIdentity: accountIdentityOf(v), ExternalSubject: v.ExternalSubject, Issuer: v.Issuer, LastCheckStatus: v.LastCheckStatus}
	if out.LastCheckStatus == "" {
		out.LastCheckStatus = identityCheckStatusNone
	}
	return out
}

// credentialsLinkProblem renders a failed
// linkAccountIdentityWithCredentials. Clients branch on the problem type, so
// each refusal has its own, shared with the other linking operations
// (linkingRefusalProblem).
func credentialsLinkProblem(err error) error {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		return validationProblem(locationBody+".directory_password", codeInvalid, "The directory username or password is incorrect.")
	case errors.Is(err, auth.ErrUnknownAuthInstallation):
		return NewProblem(TypeNotFound, "No enabled directory sign-in provider has this installation.")
	}
	if p := linkingRefusalProblem(err, "directory"); p != nil {
		return p
	}
	return serviceProblem(err)
}

// networkLinkProblem renders a failed linkAccountIdentityWithNetwork.
func networkLinkProblem(err error) error {
	switch {
	case errors.Is(err, auth.ErrNetworkIdentityRequired):
		return NewProblem(TypeNetworkIdentityRequired, "Open this server through the sign-in provider's network address to link this device's identity.")
	case errors.Is(err, auth.ErrUnknownAuthInstallation):
		return NewProblem(TypeNotFound, "No enabled network sign-in provider has this installation.")
	case errors.Is(err, auth.ErrInvalidCredentials):
		// The plugin could not identify the device; nothing the person
		// typed was wrong.
		return NewProblem(TypeNotPermitted, "The provider does not permit this device to link.")
	}
	if p := linkingRefusalProblem(err, "provider"); p != nil {
		return p
	}
	return serviceProblem(err)
}

// linkingRefusalProblem renders the refusals every way of linking an
// identity shares (link tickets, native link completion, directory
// linking), so each answers them with the same problem type. provider names
// the other side in the detail ("provider", "directory"). It answers nil for
// any other error.
func linkingRefusalProblem(err error, provider string) *Problem {
	switch {
	case errors.Is(err, auth.ErrLinkTicketPassword):
		return validationProblem(locationBody+".password", codeInvalid, "The password is incorrect.")
	case errors.Is(err, auth.ErrPasswordLoginDisabled):
		return NewProblem(TypeLocalPasswordRequired, "This account has no local password to confirm.")
	case errors.Is(err, auth.ErrAccountAlreadyLinked):
		return NewProblem(TypeAlreadyLinked, "This account is already linked to the "+provider+".")
	case errors.Is(err, auth.ErrIdentityLinkedElsewhere):
		return NewProblem(TypeIdentityLinkedElsewhere, "This "+provider+" account is already connected to another account.")
	case errors.Is(err, auth.ErrProviderAccountDisabled):
		// Before ErrUserDisabled, which it wraps: the provider's account,
		// not the Silo account, is disabled.
		return NewProblem(TypeAccountDisabled, "The "+provider+" account is disabled, locked or expired.")
	case errors.Is(err, auth.ErrUserDisabled):
		return NewProblem(TypePermissionDenied, "The account is disabled.")
	case errors.Is(err, auth.ErrNotPermitted):
		return NewProblem(TypeNotPermitted, "The "+provider+" does not permit this account to link.")
	case errors.Is(err, auth.ErrProviderPasswordExpired):
		return NewProblem(TypeProviderPasswordExpired, "The "+provider+" requires a new password first.")
	case errors.Is(err, auth.ErrProviderUnavailable):
		return NewProblem(TypeProviderUnavailable, "The "+provider+" is unavailable; try again later.")
	}
	return nil
}

// externalSignInProblem maps the plugin-side errors of the connection test;
// everything else keeps its shared-handler decision.
func externalSignInProblem(err error) error {
	switch {
	case errors.Is(err, plugins.ErrAuthConnectionTestUnsupported):
		return NewProblem(TypeConflict, "This sign-in plugin does not offer a connection test.")
	case errors.Is(err, plugins.ErrInstallationDisabled):
		return NewProblem(TypeConflict, "Enable the plugin before testing its connection.")
	}
	return adminPluginMutationProblem(err)
}

func registerExternalSignIn(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/auth/external-sign-in/capabilities", "getExternalSignInCapabilities", "auth",
		"Discover which external sign-in (OIDC, LDAP) management operations this server serves."), Class: ClassPublic, ServiceBacked: true},
		func(context.Context, *CapabilityInput) (*ExternalSignInCapabilitiesOutput, error) {
			out := new(ExternalSignInCapabilitiesOutput)
			if svc := reg.deps.ExternalSignIn; svc != nil && svc.IdentitiesAvailable() {
				out.Body.Available = true
				out.Body.Identities = true
				out.Body.AdminIdentities = true
				out.Body.BreakGlass = true
				out.Body.LiveProviderChanges = true
				out.Body.ProviderRecheck = true
				out.Body.ConnectionTest = svc.ConnectionTestAvailable()
				out.Body.CredentialsLinking = svc.CredentialsLinkingAvailable()
				out.Body.NetworkSignIn = svc.NetworkLinkingAvailable() && reg.deps.Sessions != nil
			}
			return out, nil
		})

	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/account/identities", "listAccountIdentities", "account",
		"List the external sign-in identities linked to the caller's account."), Class: ClassAuthenticated, ServiceBacked: true},
		func(ctx context.Context, _ *struct{}) (*AccountIdentityCollectionOutput, error) {
			svc, p := reg.externalSignIn()
			if p != nil {
				return nil, p
			}
			claims := claimsFrom(ctx)
			if claims == nil {
				return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
			}
			views, err := svc.ListIdentities(ctx, claims.UserID)
			if err != nil {
				return nil, serviceProblem(err)
			}
			items := make([]AccountIdentity, 0, len(views))
			for _, v := range views {
				items = append(items, accountIdentityOf(v))
			}
			canUnlink := false
			if len(items) > 0 {
				if canUnlink, err = svc.CanUnlinkAccountIdentity(ctx, claims.UserID); err != nil {
					return nil, serviceProblem(err)
				}
			}
			return &AccountIdentityCollectionOutput{Body: AccountIdentityCollection{Collection: NewCollection(items), CanUnlink: canUnlink}}, nil
		})

	unlink := humaOp(http.MethodDelete, Prefix+"/account/identities/{id}", "deleteAccountIdentity", "account",
		"Disconnect an external sign-in identity from the caller's account.")
	unlink.Description = "Allowed only while the account can still sign in another way: its local password (with local password sign-in on, or as a break-glass account) or another identity; otherwise 409 last_sign_in_method. An API key or an impersonation session is refused with 403."
	unlink.DefaultStatus = http.StatusNoContent
	unlink.Errors = []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict}
	Register(reg, Operation{Operation: unlink, Class: ClassAuthenticated, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent},
		func(ctx context.Context, in *AccountIdentityInput) (*struct{}, error) {
			svc, p := reg.externalSignIn()
			if p != nil {
				return nil, p
			}
			claims := claimsFrom(ctx)
			if claims == nil {
				return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
			}
			if !claims.IsOwnLoginSession() {
				return nil, NewProblem(TypePermissionDenied, "Only the account's own signed-in session can disconnect a sign-in.")
			}
			identityID, p := in.ID.positive64("path.id")
			if p != nil {
				return nil, p
			}
			if err := svc.UnlinkAccountIdentity(ctx, claims.UserID, identityID); err != nil {
				return nil, serviceProblem(err)
			}
			return nil, nil
		})

	credentials := humaOp(http.MethodPost, Prefix+"/account/identities/link-credentials", "linkAccountIdentityWithCredentials", "account",
		"Link a directory (LDAP) identity to the caller's account with the directory username and password.")
	credentials.Description = "The account re-enters its local password; the credentials plugin then checks the directory username and password, and the identity it answers for is linked to this account with the same rules as other linking: linking turns local password sign-in off unless the account is break-glass, and is audited. Answers 201 with the linked identity as listAccountIdentities shows it. Refusals, by problem type: 422 validation_failed at body.password (wrong local password) or at body.directory_password (the directory refused the credentials); 409 local_password_required (the account has no local password sign-in); 403 not_permitted (the directory's group rules); 403 account_disabled (the directory account is disabled, locked or expired); 403 password_expired (the directory password expired); 403 permission_denied (the Silo account is disabled, or the caller is an API key or impersonation session); 409 identity_linked_elsewhere; 409 already_linked (this account already has an identity at the installation); 404 not_found (not an enabled credentials provider, an OAuth one included); 503 provider_unavailable. Spends the login rate-limit budget. getExternalSignInCapabilities reports credentials_linking."
	credentials.DefaultStatus = http.StatusCreated
	credentials.Errors = []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable}
	Register(reg, Operation{Operation: credentials, Class: ClassAuthenticated, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable, RateLimitBucket: loginDomain},
		func(ctx context.Context, in *AccountIdentityCredentialsLinkInput) (*AccountIdentityOutput, error) {
			svc, p := reg.externalSignIn()
			if p != nil {
				return nil, p
			}
			if !svc.CredentialsLinkingAvailable() {
				return nil, unavailable("directory linking")
			}
			if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
				return nil, p
			}
			claims, p := linkingClaims(ctx)
			if p != nil {
				return nil, p
			}
			installationID, p := in.Body.InstallationID.positive(locationBody + ".installation_id")
			if p != nil {
				return nil, p
			}
			b := in.Body
			view, err := svc.LinkAccountIdentityCredentials(ctx, claims.UserID, handlers.CredentialsLinkInput{
				InstallationID: installationID, Password: b.Password,
				DirectoryUsername: b.Username, DirectoryPassword: b.DirectoryPassword,
			})
			if err != nil {
				return nil, credentialsLinkProblem(err)
			}
			return &AccountIdentityOutput{Location: Prefix + "/account/identities", Body: accountIdentityOf(view)}, nil
		})

	network := humaOp(http.MethodPost, Prefix+"/account/identities/link-network", "linkAccountIdentityWithNetwork", "account",
		"Link the network identity of this device (such as its Tailscale login) to the caller's account.")
	network.Description = "Only a request that arrived through the network identity provider's own network address can link: the provider's plugin says who owns the device that sent it, and that identity is linked to this account after the account re-enters its local password, with the same rules as other linking (local password sign-in turns off unless the account is break-glass; audited). listAuthProviders lists the provider, with the device owner's name, only to such a request. Answers 201 with the linked identity as listAccountIdentities shows it. Refusals, by problem type: 403 network_identity_required (the request did not come through that provider's network); 422 validation_failed at body.password (wrong local password); 409 local_password_required; 403 not_permitted (the provider refuses this device, for example a tagged device or one its policy leaves out); 403 permission_denied (the Silo account is disabled, or the caller is an API key or impersonation session); 409 identity_linked_elsewhere; 409 already_linked; 404 not_found (not an enabled network identity provider); 503 provider_unavailable. Spends the login rate-limit budget. getExternalSignInCapabilities reports network_sign_in."
	network.DefaultStatus = http.StatusCreated
	network.Errors = []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable}
	Register(reg, Operation{Operation: network, Class: ClassAuthenticated, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable, RateLimitBucket: loginDomain},
		func(ctx context.Context, in *AccountIdentityNetworkLinkInput) (*AccountIdentityOutput, error) {
			svc, p := reg.externalSignIn()
			if p != nil {
				return nil, p
			}
			if !svc.NetworkLinkingAvailable() {
				return nil, unavailable("network sign-in linking")
			}
			if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
				return nil, p
			}
			claims, p := linkingClaims(ctx)
			if p != nil {
				return nil, p
			}
			installationID, p := in.Body.InstallationID.positive(locationBody + ".installation_id")
			if p != nil {
				return nil, p
			}
			view, err := svc.LinkAccountIdentityNetwork(ctx, claims.UserID, handlers.NetworkLinkInput{
				InstallationID: installationID, Password: in.Body.Password,
			})
			if err != nil {
				return nil, networkLinkProblem(err)
			}
			return &AccountIdentityOutput{Location: Prefix + "/account/identities", Body: accountIdentityOf(view)}, nil
		})

	adminOp := func(method, path, id, summary string) Operation {
		op := Operation{Operation: humaOp(method, Prefix+"/admin/users/{id}/identities"+path, id, "admin-users", summary), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true}
		op.Errors = []int{http.StatusNotFound}
		return op
	}
	Register(reg, adminOp(http.MethodGet, "", "listAdminUserIdentities", "List the external sign-in identities linked to an account."),
		func(ctx context.Context, in *AdminUserIdentitiesInput) (*AdminUserIdentityCollectionOutput, error) {
			svc, p := reg.externalSignIn()
			if p != nil {
				return nil, p
			}
			userID, p := adminAccountID(in.ID)
			if p != nil {
				return nil, p
			}
			views, err := svc.ListIdentities(ctx, userID)
			if err != nil {
				return nil, serviceProblem(err)
			}
			items := make([]AdminUserIdentity, 0, len(views))
			for _, v := range views {
				items = append(items, adminUserIdentityOf(v))
			}
			return &AdminUserIdentityCollectionOutput{Body: AdminUserIdentityCollection{NewCollection(items)}}, nil
		})

	link := adminOp(http.MethodPost, "", "createAdminUserIdentity", "Link an account to an external sign-in identity by the provider's exact subject.")
	link.Description = "The installation must have a sign-in binding. An identity linked to another account is 409 identity_linked_elsewhere; an account already linked to that installation is 409 already_linked. Linking turns the account's local password sign-in off unless it is a break-glass account. Only the server Owner may change another admin's sign-in."
	link.DefaultStatus = http.StatusCreated
	link.RetrySafety = RetrySafetyUniqueConstraint
	link.Errors = append(link.Errors, http.StatusForbidden, http.StatusConflict)
	Register(reg, link, func(ctx context.Context, in *AdminUserIdentityCreateInput) (*AdminUserIdentityOutput, error) {
		svc, p := reg.externalSignIn()
		if p != nil {
			return nil, p
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		userID, p := adminAccountID(in.ID)
		if p != nil {
			return nil, p
		}
		installationID, p := in.Body.InstallationID.positive("body.installation_id")
		if p != nil {
			return nil, p
		}
		b := in.Body
		view, err := svc.LinkAdminUserIdentity(ctx, userID, handlers.AdminIdentityLinkInput{
			InstallationID: installationID, ExternalSubject: b.ExternalSubject,
			Username: b.Username, Email: b.Email, DisplayName: b.DisplayName,
		})
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &AdminUserIdentityOutput{
			Location: Prefix + "/admin/users/" + strconv.Itoa(userID) + "/identities",
			Body:     adminUserIdentityOf(view),
		}, nil
	})

	del := adminOp(http.MethodDelete, "/{identity_id}", "deleteAdminUserIdentity", "Unlink an external sign-in identity from an account.")
	del.Description = "The account may be left without a way to sign in: an account linked to a provider has local password sign-in off. Setting a password with updateAdminUser turns it back on. Only the server Owner may change another admin's sign-in."
	del.DefaultStatus = http.StatusNoContent
	del.RetrySafety = RetrySafetyNaturalIdempotent
	del.Errors = append(del.Errors, http.StatusForbidden)
	Register(reg, del, func(ctx context.Context, in *AdminUserIdentityInput) (*struct{}, error) {
		svc, p := reg.externalSignIn()
		if p != nil {
			return nil, p
		}
		userID, p := adminAccountID(in.ID)
		if p != nil {
			return nil, p
		}
		identityID, p := in.IdentityID.positive64("path.identity_id")
		if p != nil {
			return nil, p
		}
		if err := svc.UnlinkAdminUserIdentity(ctx, userID, identityID); err != nil {
			return nil, serviceProblem(err)
		}
		return nil, nil
	})

	test := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/plugins/installations/{id}/auth-binding/test", "testAdminPluginAuthBinding", "admin-plugins",
		"Test an auth plugin's connection with staged, unsaved settings."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	test.Description = "The running plugin tests the staged configuration (merged over the stored entries it does not replace) and persists nothing. A failed check is a 200 result with ok false. A plugin without a connection test, or a disabled installation, is 409. The check reaches the provider and is bounded by a server timeout; never retry an uncertain result automatically."
	test.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, test, func(ctx context.Context, in *AdminPluginAuthBindingTestInput) (*AuthConnectionTestOutput, error) {
		if reg.deps.ExternalSignIn == nil || !reg.deps.ExternalSignIn.ConnectionTestAvailable() {
			return nil, unavailable("plugin connection test")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		id, p := adminPluginInstallationID(in.ID)
		if p != nil {
			return nil, p
		}
		staged := make([]plugins.StagedConfig, 0, len(in.Body.Config))
		for i, entry := range in.Body.Config {
			if strings.TrimSpace(entry.Key) == "" {
				return nil, validationProblem(locationBody+".config["+strconv.Itoa(i)+"].key", codeInvalid, "A configuration key is required.")
			}
			staged = append(staged, plugins.StagedConfig{Key: entry.Key, Value: map[string]any(entry.Value), ClearSecrets: entry.ClearSecrets})
		}
		result, err := reg.deps.ExternalSignIn.TestAuthBinding(ctx, id, in.Body.CapabilityID, staged)
		if err != nil {
			return nil, externalSignInProblem(err)
		}
		out := &AuthConnectionTestOutput{Body: AuthConnectionTestResult{OK: result.OK, Steps: make([]AuthConnectionTestStep, 0, len(result.Steps))}}
		// Only an OAuth provider has a redirect URI to register; a password
		// (LDAP) provider's result leaves it empty.
		if svc := reg.deps.OAuth; svc != nil && svc.PublicURL("/", nil) != "" && auth.ProviderModeForAuthModes(result.AuthModes) == auth.ProviderModeOAuth {
			out.Body.CallbackURL = svc.CallbackURL(Prefix, id)
		}
		for _, step := range result.Steps {
			out.Body.Steps = append(out.Body.Steps, AuthConnectionTestStep{ID: step.ID, Label: step.Label, OK: step.OK, Message: step.Message})
		}
		return out, nil
	})
}
