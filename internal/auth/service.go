package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Sentinel errors for service operations.
var (
	ErrSessionRevoked          = errors.New("session has been revoked")
	ErrSetupAlreadyComplete    = errors.New("initial setup already complete")
	ErrSignupDisabled          = errors.New("public signups are not enabled")
	ErrImpersonationNotAllowed = errors.New("impersonation not allowed")
	ErrAlreadyImpersonating    = errors.New("already impersonating")
	ErrNotImpersonating        = errors.New("not impersonating")
	ErrPasswordLoginDisabled   = errors.New("local password login is disabled")
	ErrCurrentPasswordInvalid  = errors.New("current password is invalid")
	ErrPasswordTooShort        = errors.New("password is too short")
	ErrPasswordTooLong         = errors.New("password is too long")
	// ErrPasswordUnchanged refuses replacing a temporary password with itself.
	ErrPasswordUnchanged = errors.New("new password matches the temporary password")
	// ErrPasswordChangeRequired refuses a sign-in surface that cannot offer
	// the password change a temporary password requires.
	ErrPasswordChangeRequired = errors.New("password change required")
)

const (
	MinimumPasswordLength = 8
	MaximumPasswordBytes  = 72
)

// TokenPair holds the access and refresh tokens returned after login or refresh.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // seconds until access token expires
	// SessionID is the login session the pair authenticates.
	SessionID string
}

// SettingsGetter retrieves server settings by key.
// Implemented by catalog.ServerSettingsRepo.
type SettingsGetter interface {
	Get(ctx context.Context, key string) (string, error)
}

type claimsContextKey struct{}

// WithClaims stores auth claims on the context for auth-owned flows.
func WithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

// ClaimsFromContext retrieves auth claims previously stored with WithClaims.
func ClaimsFromContext(ctx context.Context) *Claims {
	claims, _ := ctx.Value(claimsContextKey{}).(*Claims)
	return claims
}

// Service orchestrates authentication operations using an AuthProvider,
// JWTService, and session/user repositories.
type Service struct {
	provider    AuthProvider
	jwt         *JWTService
	sessions    *SessionRepository
	users       *UserRepository
	inviteCodes *InviteCodeRepository
	settings    SettingsGetter
	// providers are registered once at start (the built-in local provider,
	// test doubles); pluginSource supplies the auth-plugin providers, which
	// change without a restart.
	providers    map[string]AuthProvider
	metadata     map[string]LoginProviderInfo
	order        []string
	pluginSource PluginProviderSource
	accounts     *AccountProvisioner
	// recheck re-checks sessions opened through an external provider at
	// refresh; nil skips it.
	recheck *ProviderRecheck
}

// PluginProviderSource supplies the current auth-plugin providers. The
// registry rebuilds its list when bindings, plugin configuration or
// installations change, on every node.
type PluginProviderSource interface {
	Providers() []RegisteredProvider
}

// LocalProviderID is the id of the built-in username/password provider.
const LocalProviderID = "local"

type LoginProviderInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Mode        string `json:"mode"`
	Default     bool   `json:"default"`
	// IconURL is rendered next to the "Sign in with X" button. Set for
	// auth_provider.v1 plugins that ship an icon (icon_url manifest field).
	IconURL string `json:"icon_url,omitempty"`
	// InstallationID is non-zero when the provider is backed by a plugin.
	// The login UI uses it to build /api/v1/auth/oauth/{install_id}/init URLs.
	InstallationID int `json:"installation_id,omitempty"`
}

type RegisteredProvider struct {
	Info     LoginProviderInfo
	Provider AuthProvider
}

// NewService creates a new auth Service with the given dependencies.
func NewService(
	provider AuthProvider,
	jwt *JWTService,
	sessions *SessionRepository,
	users *UserRepository,
	inviteCodes *InviteCodeRepository,
	settings SettingsGetter,
	storeProvider userstore.UserStoreProvider,
) *Service {
	service := &Service{
		provider:    provider,
		jwt:         jwt,
		sessions:    sessions,
		users:       users,
		inviteCodes: inviteCodes,
		settings:    settings,
		providers:   map[string]AuthProvider{},
		metadata:    map[string]LoginProviderInfo{},
		accounts:    NewAccountProvisioner(users, storeProvider),
	}
	if provider != nil {
		service.RegisterProvider(LoginProviderInfo{
			ID:          LocalProviderID,
			DisplayName: "Local",
			Mode:        "credentials",
			Default:     true,
		}, provider)
	}
	return service
}

// Login authenticates the user with the given credentials and creates a new
// session. Returns a TokenPair containing the access and refresh tokens.
func (s *Service) Login(ctx context.Context, username, password, deviceName, ip string) (*TokenPair, *models.User, error) {
	return s.loginWithProvider(ctx, LocalProviderID, username, password, deviceName, ip, false)
}

// CompatLogin is Login for sign-in surfaces that cannot run the forced
// password change (Jellyfin and Audiobookshelf compatibility). An account
// holding a temporary password gets ErrPasswordChangeRequired and no session:
// its holder must sign in to Silo and choose a new password first.
//
// Compatibility clients cannot pick a provider, so the name is routed like
// a login without a provider (see routePasswordLogin): directory users of a
// credentials plugin sign in with their directory password.
func (s *Service) CompatLogin(ctx context.Context, username, password, deviceName, ip string) (*TokenPair, *models.User, error) {
	providerID, err := s.routePasswordLogin(ctx, username)
	if err != nil {
		return nil, nil, err
	}
	return s.loginWithProvider(ctx, providerID, username, password, deviceName, ip, true)
}

// PasswordRoutesLocally reports whether a password sign-in of username
// without a provider is checked by the local provider (see
// routePasswordLogin). A compatibility surface that may retry with a
// different password uses it to keep retries away from a directory, where
// each failed bind counts toward the account's lockout.
func (s *Service) PasswordRoutesLocally(ctx context.Context, username string) (bool, error) {
	providerID, err := s.routePasswordLogin(ctx, username)
	return providerID == LocalProviderID, err
}

func (s *Service) LoginWithProvider(
	ctx context.Context,
	providerID string,
	username string,
	password string,
	deviceName string,
	ip string,
) (*TokenPair, *models.User, error) {
	if providerID == "" {
		routed, err := s.routePasswordLogin(ctx, username)
		if err != nil {
			return nil, nil, err
		}
		providerID = routed
	}
	return s.loginWithProvider(ctx, providerID, username, password, deviceName, ip, false)
}

// routePasswordLogin picks the provider for a password sign-in that named
// none. An account with local password sign-in signs in locally only, even
// while the server turns local sign-in off, so a local password is never
// sent to a directory. Any other name goes to the enabled credentials plugin
// (LDAP), which may link or create the account; without one it is local.
func (s *Service) routePasswordLogin(ctx context.Context, username string) (string, error) {
	directory := ""
	for _, registered := range s.pluginProviders() {
		if registered.Info.Mode == ProviderModeCredentials {
			directory = registered.Info.ID
			break
		}
	}
	if directory == "" || s.users == nil {
		return LocalProviderID, nil
	}
	user, err := LookupLogin(ctx, s.users, username)
	if err != nil {
		if IsNotFound(err) {
			return directory, nil
		}
		return "", fmt.Errorf("looking up user: %w", err)
	}
	if user.LocalPasswordLoginEnabled {
		// Whatever the server-wide switch says: with it off the local
		// provider refuses with local_login_disabled, but the password never
		// reaches the directory.
		return LocalProviderID, nil
	}
	return directory, nil
}

func (s *Service) RegisterProvider(info LoginProviderInfo, provider AuthProvider) {
	if provider == nil || info.ID == "" {
		return
	}
	if info.DisplayName == "" {
		info.DisplayName = info.ID
	}
	if info.Mode == "" {
		info.Mode = "credentials"
	}

	if _, exists := s.providers[info.ID]; !exists {
		s.order = append(s.order, info.ID)
	}
	s.providers[info.ID] = provider
	s.metadata[info.ID] = info
}

// SetProviderRecheck makes Refresh re-check sessions opened through an
// external sign-in provider with that provider.
func (s *Service) SetProviderRecheck(r *ProviderRecheck) {
	s.recheck = r
}

// SetPluginProviderSource makes the auth-plugin providers of src available
// to sign-in, discovery and the OAuth handshake.
func (s *Service) SetPluginProviderSource(src PluginProviderSource) {
	s.pluginSource = src
}

func (s *Service) pluginProviders() []RegisteredProvider {
	if s.pluginSource == nil {
		return nil
	}
	return s.pluginSource.Providers()
}

// registeredProviders lists the static providers in registration order,
// then the plugin providers. The default is the last one marked default,
// else the first.
func (s *Service) registeredProviders() ([]RegisteredProvider, string) {
	all := make([]RegisteredProvider, 0, len(s.order))
	for _, id := range s.order {
		all = append(all, RegisteredProvider{Info: s.metadata[id], Provider: s.providers[id]})
	}
	for _, registered := range s.pluginProviders() {
		if registered.Provider == nil || registered.Info.ID == "" {
			continue
		}
		if _, static := s.providers[registered.Info.ID]; static {
			continue
		}
		all = append(all, registered)
	}
	defaultID := ""
	for _, registered := range all {
		if defaultID == "" || registered.Info.Default {
			defaultID = registered.Info.ID
		}
	}
	return all, defaultID
}

func (s *Service) providerByID(id string) AuthProvider {
	if provider := s.providers[id]; provider != nil {
		return provider
	}
	for _, registered := range s.pluginProviders() {
		if registered.Info.ID == id {
			return registered.Provider
		}
	}
	return nil
}

// FindOAuthInstallation returns the PluginProvider registered for the given
// plugin installation when it is an OAuth provider; nil otherwise. Only
// OAuth providers take part in the /oauth routes and link tickets: a
// credentials (LDAP) installation is not found there.
func (s *Service) FindOAuthInstallation(installationID int) *PluginProvider {
	if installationID <= 0 {
		return nil
	}
	all, _ := s.registeredProviders()
	for _, registered := range all {
		pp, ok := registered.Provider.(*PluginProvider)
		if !ok || pp == nil || pp.InstallationID() != installationID || registered.Info.Mode != ProviderModeOAuth {
			continue
		}
		return pp
	}
	return nil
}

// ResolveOAuthLogin runs the account half of an OAuth sign-in: the handler
// has already called the plugin's ExchangeCode RPC and is passing the
// AuthenticateResponse back. Service finds the matching PluginProvider and
// looks up or auto-provisions the account. It opens no session: the
// completion code does when it is redeemed (OpenOAuthSession).
func (s *Service) ResolveOAuthLogin(ctx context.Context, in OAuthLoginInput) (*models.User, int64, error) {
	provider := s.FindOAuthInstallation(in.InstallationID)
	if provider == nil {
		return nil, 0, ErrInvalidCredentials
	}
	// A linking flow (LinkingUserID > 0) links the identity to that account
	// or refuses one linked elsewhere; see AccountResolver.
	return provider.CompleteOAuth(ctx, in.Response, in.LinkingUserID)
}

// OpenOAuthSession opens the login session of a completion code being
// redeemed, on db (nil: the pool), and mints its token pair. The account is
// read again, so one disabled since the callback is refused.
func (s *Service) OpenOAuthSession(ctx context.Context, db OAuthSessionDB, c OAuthCompletion) (*TokenPair, error) {
	var (
		user *models.User
		err  error
		exec sessionExecQuerier = s.sessions.pool
	)
	if db != nil {
		user, err = userByID(ctx, db, c.UserID)
		exec = db
	} else {
		user, err = s.users.GetByID(ctx, c.UserID)
	}
	if err != nil {
		return nil, fmt.Errorf("load oauth account: %w", err)
	}
	if !user.Enabled {
		return nil, ErrUserDisabled
	}
	sessionID := uuid.New().String()
	session := models.AuthSession{
		ID:         sessionID,
		UserID:     user.ID,
		DeviceName: c.DeviceName,
		IPAddress:  c.IP,
		ExpiresAt:  time.Now().Add(s.jwt.RefreshExpiry()),
		IdentityID: identityRef(c.IdentityID),
	}
	if err := s.sessions.createWithQuerier(ctx, exec, session); err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}
	return s.generateTokenPair(Claims{
		UserID:                 user.ID,
		Role:                   user.Role,
		SessionID:              sessionID,
		PasswordChangeRequired: user.PasswordChangeRequired,
	})
}

// LinkOAuthIdentity finishes a linking flow: the identity the plugin
// answered is linked to the signed-in account that requested the link
// ticket, or refused when another account holds it
// (ErrIdentityLinkedElsewhere). No session is opened; the account keeps the
// sessions it has.
func (s *Service) LinkOAuthIdentity(ctx context.Context, in OAuthLoginInput) (*models.User, error) {
	if in.LinkingUserID <= 0 {
		return nil, ErrNotPermitted
	}
	provider := s.FindOAuthInstallation(in.InstallationID)
	if provider == nil {
		return nil, ErrProviderUnavailable
	}
	user, _, err := provider.CompleteOAuth(ctx, in.Response, in.LinkingUserID)
	return user, err
}

// ProviderLogoutURL is the end-session URL of the OAuth provider an account
// is linked to, for the web client to visit after the Silo session ends.
// It is empty unless the account has an identity at an enabled OAuth
// provider and that plugin answers EndSessionUrl with a URL. The plugin's
// own configuration is the only switch: a plugin whose operator turned
// provider logout off answers empty. A plugin failure is logged and answers
// empty: the Silo sign-out never depends on the provider.
func (s *Service) ProviderLogoutURL(ctx context.Context, userID int, postLogoutRedirectURI string) (string, error) {
	for _, registered := range s.pluginProviders() {
		pp, ok := registered.Provider.(*PluginProvider)
		if !ok || pp == nil || registered.Info.Mode != ProviderModeOAuth {
			continue
		}
		endURL, err := pp.EndSessionURL(ctx, userID, postLogoutRedirectURI)
		if err != nil {
			slog.WarnContext(ctx, "provider logout url unavailable", "component", "auth",
				"installation_id", pp.InstallationID(), "user_id", userID, "error", err)
			continue
		}
		if endURL != "" {
			return endURL, nil
		}
	}
	return "", nil
}

func (s *Service) ListProviders() []LoginProviderInfo {
	all, defaultID := s.registeredProviders()
	providers := make([]LoginProviderInfo, 0, len(all))
	for _, registered := range all {
		info := registered.Info
		info.Default = info.ID == defaultID
		providers = append(providers, info)
	}
	sortProviders(providers)
	return providers
}

func sortProviders(providers []LoginProviderInfo) {
	sort.Slice(providers, func(i, j int) bool {
		if providers[i].Default != providers[j].Default {
			return providers[i].Default
		}
		return providers[i].DisplayName < providers[j].DisplayName
	})
}

// ProviderDiscovery is the sign-in discovery apps read: the providers a
// client may offer and whether any of them takes a password. While the
// server turns local password sign-in off, the local provider is left out
// (break-glass admins sign in on the web's local form); a credentials
// plugin (LDAP) still takes passwords.
type ProviderDiscovery struct {
	Providers     []LoginProviderInfo
	PasswordLogin bool
}

// DiscoverProviders answers ProviderDiscovery for the current settings.
func (s *Service) DiscoverProviders(ctx context.Context) (ProviderDiscovery, error) {
	localAllowed := true
	if s.users != nil {
		allowed, err := s.users.LocalPasswordLoginAllowed(ctx)
		if err != nil {
			return ProviderDiscovery{}, err
		}
		localAllowed = allowed
	}
	all, defaultID := s.registeredProviders()
	listed := make([]LoginProviderInfo, 0, len(all))
	defaultListed := false
	for _, registered := range all {
		if registered.Info.ID == LocalProviderID && !localAllowed {
			continue
		}
		listed = append(listed, registered.Info)
		defaultListed = defaultListed || registered.Info.ID == defaultID
	}
	discovery := ProviderDiscovery{Providers: listed}
	for i := range listed {
		listed[i].Default = listed[i].ID == defaultID || (!defaultListed && i == 0)
		if listed[i].Mode == ProviderModeCredentials {
			discovery.PasswordLogin = true
		}
	}
	sortProviders(listed)
	return discovery, nil
}

func (s *Service) loginWithProvider(
	ctx context.Context,
	providerID string,
	username string,
	password string,
	deviceName string,
	ip string,
	refusePasswordChange bool,
) (*TokenPair, *models.User, error) {
	provider := s.providerByID(providerID)
	if provider == nil {
		return nil, nil, ErrInvalidCredentials
	}

	creds := Credentials{Username: username, Password: password}
	var (
		user       *models.User
		identityID int64
		err        error
	)
	if plugin, ok := provider.(*PluginProvider); ok {
		user, identityID, err = plugin.authenticateCredentials(ctx, creds, 0)
	} else {
		user, err = provider.Authenticate(ctx, creds)
	}
	if err != nil {
		return nil, nil, err
	}
	if refusePasswordChange && user.PasswordChangeRequired {
		return nil, nil, ErrPasswordChangeRequired
	}

	// Create a new session with a pre-generated ID to avoid the race condition
	// of looking up the session after creation.
	sessionID := uuid.New().String()
	session := models.AuthSession{
		ID:         sessionID,
		UserID:     user.ID,
		DeviceName: deviceName,
		IPAddress:  ip,
		ExpiresAt:  time.Now().Add(s.jwt.RefreshExpiry()),
		IdentityID: identityRef(identityID),
	}

	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, nil, fmt.Errorf("creating session: %w", err)
	}

	pair, err := s.generateTokenPair(Claims{
		UserID:                 user.ID,
		Role:                   user.Role,
		SessionID:              sessionID,
		PasswordChangeRequired: user.PasswordChangeRequired,
	})
	if err != nil {
		return nil, nil, err
	}

	return pair, user, nil
}

// NeedsSetup reports whether the system still needs its initial user account.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	count, err := s.users.Count(ctx)
	if err != nil {
		return false, fmt.Errorf("counting users: %w", err)
	}
	return count == 0, nil
}

// SetupInitialUser creates the first admin account and signs it in.
//
// The emptiness check, account, optional profile, and login session share one
// transaction under the database-wide setup lock (UserRepository.ClaimInitialSetup),
// so competing callers on any replica see exactly one winner; every other
// caller gets ErrSetupAlreadyComplete. The session and token pair match what
// Login would issue for the new account.
func (s *Service) SetupInitialUser(
	ctx context.Context,
	username, email, password string,
	createDefaultProfile bool,
	defaultProfileName string,
	deviceName, ip string,
) (*TokenPair, *models.User, error) {
	if err := ValidateNewPassword(password); err != nil {
		return nil, nil, err
	}

	var (
		user *models.User
		pair *TokenPair
	)
	err := s.users.ClaimInitialSetup(ctx, func(tx pgx.Tx) error {
		created, err := s.accounts.CreateInitialAccountInTransaction(ctx, tx, CreateAccountInput{
			User: models.CreateUserInput{
				Username: username,
				Email:    email,
				Password: password,
				Role:     models.RoleAdmin,
			},
			DefaultProfile: DefaultProfileOptions{
				Enabled: createDefaultProfile,
				Name:    defaultProfileName,
			},
		})
		if err != nil {
			return fmt.Errorf("creating initial user: %w", err)
		}

		sessionID := uuid.New().String()
		session := models.AuthSession{
			ID:         sessionID,
			UserID:     created.ID,
			DeviceName: deviceName,
			IPAddress:  ip,
			ExpiresAt:  time.Now().Add(s.jwt.RefreshExpiry()),
		}
		if err := s.sessions.createWithQuerier(ctx, tx, session); err != nil {
			return err
		}
		tokens, err := s.generateTokenPair(Claims{
			UserID:    created.ID,
			Role:      created.Role,
			SessionID: sessionID,
		})
		if err != nil {
			return err
		}
		user, pair = created, tokens
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return pair, user, nil
}

// Signup creates a new user account using an invite code. Requires that
// public signups are enabled via the "signup.enabled" server setting.
func (s *Service) Signup(
	ctx context.Context,
	username, email, password, code string,
	createDefaultProfile bool,
	defaultProfileName string,
	deviceName, ip string,
) (*TokenPair, *models.User, error) {
	if err := ValidateNewPassword(password); err != nil {
		return nil, nil, err
	}
	// Check global signup toggle.
	if s.settings != nil {
		enabled, err := s.settings.Get(ctx, "signup.enabled")
		if err != nil {
			return nil, nil, fmt.Errorf("checking signup setting: %w", err)
		}
		if enabled != "true" {
			return nil, nil, ErrSignupDisabled
		}
	} else {
		return nil, nil, ErrSignupDisabled
	}
	// A signup creates a local-password account, which cannot sign in while
	// the server turns local password sign-in off; refuse before the invite
	// code is spent.
	if allowed, err := s.LocalPasswordLoginAllowed(ctx); err != nil {
		return nil, nil, err
	} else if !allowed {
		return nil, nil, ErrLocalLoginDisabled
	}

	// Create the user with standard role and access to all libraries.
	if _, err := s.accounts.CreateInvitedAccount(ctx, CreateAccountInput{
		User: models.CreateUserInput{
			Username: username,
			Email:    email,
			Password: password,
			Role:     "user",
		},
		DefaultProfile: DefaultProfileOptions{
			Enabled: createDefaultProfile,
			Name:    defaultProfileName,
		},
	}, code); err != nil {
		return nil, nil, fmt.Errorf("creating user: %w", err)
	}

	// Log them in to create a session and return tokens.
	return s.Login(ctx, username, password, deviceName, ip)
}

// SetupWizardCompleted reports whether the first-run setup wizard recorded
// its completion. It is meaningful only once an account exists; before that
// there is nothing to have completed.
func (s *Service) SetupWizardCompleted(ctx context.Context) (bool, error) {
	if s.settings == nil {
		return false, nil
	}
	value, err := s.settings.Get(ctx, config.SetupCompletedSettingKey)
	if err != nil {
		return false, fmt.Errorf("checking setup completion: %w", err)
	}
	return value == "true", nil
}

// IsSignupEnabled reports whether public signups are enabled: the
// signup.enabled setting is on and local password sign-in, which a new
// account needs, is not turned off.
func (s *Service) IsSignupEnabled(ctx context.Context) (bool, error) {
	if s.settings == nil {
		return false, nil
	}
	enabled, err := s.settings.Get(ctx, "signup.enabled")
	if err != nil {
		return false, fmt.Errorf("checking signup setting: %w", err)
	}
	if enabled != "true" {
		return false, nil
	}
	return s.LocalPasswordLoginAllowed(ctx)
}

// LocalPasswordLoginAllowed reports the server-wide local password switch
// (auth.local_password_login); true without account storage.
func (s *Service) LocalPasswordLoginAllowed(ctx context.Context) (bool, error) {
	if s.users == nil {
		return true, nil
	}
	return s.users.LocalPasswordLoginAllowed(ctx)
}

// Logout revokes the session identified by sessionID.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return s.sessions.Revoke(ctx, sessionID)
}

// StartImpersonation creates a new target-user session with admin provenance.
func (s *Service) StartImpersonation(ctx context.Context, adminUserID, targetUserID int, deviceName, ip string) (*TokenPair, *models.User, *models.User, error) {
	if claims := ClaimsFromContext(ctx); claims != nil {
		if claims.TokenType == TokenTypeAPIKey || claims.SessionID == "" {
			return nil, nil, nil, ErrImpersonationNotAllowed
		}
		currentSession, err := s.sessions.GetByID(ctx, claims.SessionID)
		if err != nil {
			if !IsSessionNotFound(err) {
				return nil, nil, nil, fmt.Errorf("getting current session: %w", err)
			}
		} else if currentSession.ImpersonatorUserID != nil {
			return nil, nil, nil, ErrAlreadyImpersonating
		}
	}

	admin, err := s.users.GetByID(ctx, adminUserID)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil, nil, ErrImpersonationNotAllowed
		}
		return nil, nil, nil, fmt.Errorf("getting admin user: %w", err)
	}
	if admin.Role != "admin" || !admin.Enabled {
		return nil, nil, nil, ErrImpersonationNotAllowed
	}
	if adminUserID == targetUserID {
		return nil, nil, nil, ErrImpersonationNotAllowed
	}

	target, err := s.users.GetByID(ctx, targetUserID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("getting target user: %w", err)
	}
	// Admins may not act as another admin; only the server Owner may, and
	// nobody may act as the Owner.
	if !target.Enabled || target.IsOwner || (target.Role == "admin" && !admin.IsOwner) {
		return nil, nil, nil, ErrImpersonationNotAllowed
	}

	sessionID := uuid.New().String()
	impersonatorUserID := admin.ID
	startedAt := time.Now()
	session := models.AuthSession{
		ID:                     sessionID,
		UserID:                 target.ID,
		DeviceName:             deviceName,
		IPAddress:              ip,
		ExpiresAt:              startedAt.Add(s.jwt.RefreshExpiry()),
		ImpersonatorUserID:     &impersonatorUserID,
		ImpersonationStartedAt: &startedAt,
	}

	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, nil, nil, fmt.Errorf("creating session: %w", err)
	}

	pair, err := s.generateTokenPair(Claims{
		UserID:             target.ID,
		Role:               target.Role,
		SessionID:          sessionID,
		ImpersonatorUserID: &impersonatorUserID,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	return pair, admin, target, nil
}

// EndImpersonation revokes an impersonated session without affecting the original admin session.
func (s *Service) EndImpersonation(ctx context.Context, sessionID string, impersonatorUserID int) error {
	session, err := s.sessions.GetByID(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.ImpersonatorUserID == nil {
		return ErrNotImpersonating
	}
	if *session.ImpersonatorUserID != impersonatorUserID {
		return ErrImpersonationNotAllowed
	}

	return s.sessions.Revoke(ctx, sessionID)
}

// Refresh validates the refresh token, checks that the associated session is
// still valid, and issues a new token pair.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	claims, err := s.jwt.ValidateToken(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("invalid refresh token: %w", err)
	}
	if claims.TokenType != TokenTypeRefresh {
		return nil, fmt.Errorf("invalid refresh token: %w", ErrInvalidToken)
	}

	session, err := s.sessions.GetByID(ctx, claims.SessionID)
	if err != nil {
		if IsSessionNotFound(err) {
			return nil, ErrSessionRevoked
		}
		return nil, fmt.Errorf("getting session: %w", err)
	}
	if session.RevokedAt != nil || !session.ExpiresAt.After(time.Now()) {
		return nil, ErrSessionRevoked
	}

	user, err := s.users.GetByID(ctx, session.UserID)
	if err != nil {
		if IsNotFound(err) {
			return nil, ErrSessionRevoked
		}
		return nil, fmt.Errorf("getting user: %w", err)
	}
	if !user.Enabled {
		return nil, ErrSessionRevoked
	}
	if err := s.validateImpersonator(ctx, session.ImpersonatorUserID); err != nil {
		return nil, err
	}

	// Slide the session window forward so an active client never hits the
	// hard expires_at set at login. A failure here is non-fatal: the refresh
	// still returns fresh tokens; the session just keeps its prior expiry.
	now := time.Now()
	newExpiry := now.Add(s.jwt.RefreshExpiry())

	// A session opened through an external provider is re-checked with the
	// provider once its identity's last answer is older than the re-check
	// interval (docs/architecture/external-sign-in.md). One whose identity is
	// gone (unlinked, plugin removed) can no longer be re-checked at all.
	absoluteAge := session.IdentityID == nil && session.ProviderSince != nil && session.ImpersonatorUserID == nil
	rechecked := session.IdentityID != nil && session.ImpersonatorUserID == nil && s.recheck != nil
	if rechecked {
		verdict, err := s.recheck.check(ctx, session)
		if err != nil {
			return nil, err
		}
		absoluteAge = verdict == verdictAbsoluteAge
	}
	if absoluteAge {
		// The provider cannot vouch for the session any more: it ends an
		// absolute age (auth.refresh_token_expiry) after the provider last
		// did, and refresh no longer slides it.
		limit := providerAnchor(session).Add(s.jwt.RefreshExpiry())
		if !limit.After(now) {
			return nil, ErrSessionRevoked
		}
		newExpiry = limit
		if session.ExpiresAt.Before(limit) {
			newExpiry = session.ExpiresAt
		}
	}
	if err := s.sessions.ExtendExpiresAt(ctx, session.ID, newExpiry); err != nil {
		switch {
		case !IsSessionNotFound(err):
			return nil, fmt.Errorf("extending session: %w", err)
		case rechecked:
			// A check on another node (a role change, a refused account)
			// revoked the session while this refresh waited for it.
			return nil, ErrSessionRevoked
		}
	}

	// An impersonating administrator is not the one who must change the
	// password, so only the account's own sessions are restricted.
	return s.generateTokenPair(Claims{
		UserID:                 user.ID,
		Role:                   user.Role,
		SessionID:              session.ID,
		ImpersonatorUserID:     session.ImpersonatorUserID,
		PasswordChangeRequired: user.PasswordChangeRequired && session.ImpersonatorUserID == nil,
	})
}

func (s *Service) validateImpersonator(ctx context.Context, impersonatorUserID *int) error {
	if impersonatorUserID == nil {
		return nil
	}

	impersonator, err := s.users.GetByID(ctx, *impersonatorUserID)
	if err != nil {
		if IsNotFound(err) {
			return ErrSessionRevoked
		}
		return fmt.Errorf("getting impersonator user: %w", err)
	}
	if !impersonator.Enabled || impersonator.Role != "admin" {
		return ErrSessionRevoked
	}
	return nil
}

// GetCurrentUser retrieves the user associated with the given JWT claims.
func (s *Service) GetCurrentUser(ctx context.Context, claims *Claims) (*models.User, error) {
	user, err := s.users.GetByID(ctx, claims.UserID)
	if err != nil {
		return nil, fmt.Errorf("getting user: %w", err)
	}
	return user, nil
}

// PasswordChangeAvailable reports whether the account has a local password
// that can be verified and replaced through the self-service password flow.
// OAuth-only accounts keep their provider-managed credential boundary.
func (s *Service) PasswordChangeAvailable(ctx context.Context, userID int) (bool, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("getting user: %w", err)
	}
	return user.LocalPasswordLoginEnabled && user.PasswordHash != "", nil
}

// ChangePassword verifies the existing local credential before replacing it.
// Profile authorization and impersonation checks belong to the HTTP boundary;
// this method owns only the account credential transition. sessionID is the
// login session making the change: when it replaces a temporary password,
// every other session of the account is revoked.
func (s *Service) ChangePassword(ctx context.Context, userID int, sessionID, currentPassword, newPassword string) error {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("getting user: %w", err)
	}
	if err := validatePasswordChange(user, currentPassword, newPassword); err != nil {
		return err
	}

	swap := s.users.CompareAndSwapPassword
	if user.PasswordChangeRequired {
		swap = func(ctx context.Context, id int, expectedHash, newPassword string) error {
			return s.users.ReplaceTemporaryPassword(ctx, id, expectedHash, newPassword, sessionID)
		}
	}
	if err := swap(ctx, userID, user.PasswordHash, newPassword); err != nil {
		return fmt.Errorf("updating password: %w", err)
	}
	return nil
}

func validatePasswordChange(user *models.User, currentPassword, newPassword string) error {
	if !user.LocalPasswordLoginEnabled || user.PasswordHash == "" {
		return ErrPasswordLoginDisabled
	}
	if !CheckPassword(user, currentPassword) {
		return ErrCurrentPasswordInvalid
	}
	if err := ValidateNewPassword(newPassword); err != nil {
		return err
	}
	if user.PasswordChangeRequired && newPassword == currentPassword {
		return ErrPasswordUnchanged
	}
	return nil
}

// ValidateNewPassword applies the shared local credential policy before a new
// account or password is persisted. The minimum counts characters; bcrypt
// limits the UTF-8 encoding to 72 bytes.
func ValidateNewPassword(password string) error {
	if utf8.RuneCountInString(password) < MinimumPasswordLength {
		return ErrPasswordTooShort
	}
	if len(password) > MaximumPasswordBytes {
		return ErrPasswordTooLong
	}
	return nil
}

// GetSessions returns all sessions for the given user ID.
func (s *Service) GetSessions(ctx context.Context, userID int) ([]*models.AuthSession, error) {
	return s.sessions.ListByUser(ctx, userID)
}

// GetSessionsPage returns one keyset page of the user's live sessions; see
// SessionRepository.ListByUserPage.
func (s *Service) GetSessionsPage(ctx context.Context, userID int, after *SessionKey, limit int) ([]*models.AuthSession, error) {
	return s.sessions.ListByUserPage(ctx, userID, after, limit)
}

// RevokeSession revokes a specific session. It verifies the session belongs
// to the given user before revoking.
func (s *Service) RevokeSession(ctx context.Context, sessionID string, userID int) error {
	session, err := s.sessions.GetByID(ctx, sessionID)
	if err != nil {
		return err
	}

	if session.UserID != userID {
		return ErrSessionNotFound
	}

	return s.sessions.Revoke(ctx, sessionID)
}

// generateTokenPair creates a new access/refresh token pair for the given
// claims.
func (s *Service) generateTokenPair(claims Claims) (*TokenPair, error) {
	accessToken, err := s.jwt.generateAccessToken(claims)
	if err != nil {
		return nil, fmt.Errorf("generating access token: %w", err)
	}

	refreshToken, err := s.jwt.generateRefreshToken(claims)
	if err != nil {
		return nil, fmt.Errorf("generating refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int(s.jwt.AccessExpiry().Seconds()),
		SessionID:    claims.SessionID,
	}, nil
}

// providerAnchor is when the provider last vouched for session's chain: its
// provider_since, or its creation for a session that predates it.
func providerAnchor(session *models.AuthSession) time.Time {
	if session.ProviderSince != nil && session.ProviderSince.Before(session.CreatedAt) {
		return *session.ProviderSince
	}
	return session.CreatedAt
}

// identityRef is the IdentityID of a session opened through identityID; nil
// for 0 (no identity).
func identityRef(identityID int64) *int64 {
	if identityID == 0 {
		return nil
	}
	return &identityID
}
