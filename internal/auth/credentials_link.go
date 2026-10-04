package auth

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// CredentialsLinkInput is a signed-in account linking the directory
// (credentials provider, LDAP) identity it proves with the directory's
// username and password. Password is the account's local password,
// re-entered to confirm the link like a link ticket.
type CredentialsLinkInput struct {
	UserID            int
	InstallationID    int
	Password          string
	DirectoryUsername string
	DirectoryPassword string
}

// findCredentialsInstallation returns the PluginProvider registered for the
// installation when it is an enabled credentials (password) provider; nil
// otherwise.
func (s *Service) findCredentialsInstallation(installationID int) *PluginProvider {
	if installationID <= 0 {
		return nil
	}
	for _, registered := range s.pluginProviders() {
		pp, ok := registered.Provider.(*PluginProvider)
		if ok && pp != nil && pp.InstallationID() == installationID && registered.Info.Mode == ProviderModeCredentials {
			return pp
		}
	}
	return nil
}

// LinkCredentialsIdentity links the directory identity the credentials
// plugin answers for the given directory username and password to the
// signed-in account, after the account re-entered its local password. It is
// the self-service counterpart of an OAuth linking flow, with the same
// rules: the identity is linked through account resolution (an identity of
// another account is ErrIdentityLinkedElsewhere, another identity of this
// account at the installation ErrAccountAlreadyLinked), the provider's
// managed role applies, and local password sign-in turns off unless the
// account is break-glass. An account without local password sign-in has no
// password to confirm (ErrPasswordLoginDisabled); a disabled account is
// ErrUserDisabled; a wrong local password is ErrLinkTicketPassword; refused
// directory credentials are ErrInvalidCredentials; a directory account that
// is disabled, locked or expired is ErrProviderAccountDisabled; the plugin's
// other refusals keep their sign-in errors (ErrNotPermitted,
// ErrProviderPasswordExpired, ErrProviderUnavailable).
func (s *Service) LinkCredentialsIdentity(ctx context.Context, in CredentialsLinkInput) (*LinkedIdentity, error) {
	provider := s.findCredentialsInstallation(in.InstallationID)
	if provider == nil {
		return nil, ErrUnknownAuthInstallation
	}
	if s.users == nil {
		return nil, ErrProviderUnavailable
	}
	if _, err := confirmLocalPassword(ctx, s.users, in.UserID, in.Password); err != nil {
		return nil, err
	}
	username := strings.TrimSpace(in.DirectoryUsername)
	if username == "" || in.DirectoryPassword == "" {
		return nil, ErrInvalidCredentials
	}
	_, identityID, err := provider.authenticateCredentials(ctx, Credentials{Username: username, Password: in.DirectoryPassword}, in.UserID)
	if err != nil {
		return nil, err
	}
	return identityByID(ctx, provider.resolver.pool, identityID)
}

// confirmLocalPassword loads the account and checks the local password it
// re-entered to confirm a link, for both linking paths (a link ticket and
// directory linking), which share this error contract: a disabled account
// is ErrUserDisabled, an account without local password sign-in has none to
// confirm (ErrPasswordLoginDisabled), and a wrong password is
// ErrLinkTicketPassword.
func confirmLocalPassword(ctx context.Context, users OAuthUserReader, userID int, password string) (*models.User, error) {
	user, err := users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !user.Enabled {
		return nil, ErrUserDisabled
	}
	if !user.LocalPasswordLoginEnabled || user.PasswordHash == "" {
		return nil, ErrPasswordLoginDisabled
	}
	if !CheckPassword(user, password) {
		return nil, ErrLinkTicketPassword
	}
	return user, nil
}
