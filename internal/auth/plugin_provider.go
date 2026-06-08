package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

type pluginAuthClient interface {
	Authenticate(ctx context.Context, req *pluginv1.AuthenticateRequest) (*pluginv1.AuthenticateResponse, error)
	InitAuthorize(ctx context.Context, req *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error)
	ExchangeCode(ctx context.Context, req *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error)
}

type pluginAuthClientFactory func(ctx context.Context) (pluginAuthClient, error)

type PluginProviderConfig struct {
	InstallationID int
	CapabilityID   string
	DisplayName    string
	AutoProvision  bool
}

type PluginProvider struct {
	config       PluginProviderConfig
	client       pluginAuthClientFactory
	sessions     *SessionRepository
	users        *UserRepository
	identityPool *pgxpool.Pool
	accounts     *AccountProvisioner
}

func NewPluginProviderWithClientFactory(
	config PluginProviderConfig,
	sessions *SessionRepository,
	users *UserRepository,
	pool *pgxpool.Pool,
	clientFactory pluginAuthClientFactory,
) *PluginProvider {
	return &PluginProvider{
		config:       config,
		client:       clientFactory,
		sessions:     sessions,
		users:        users,
		identityPool: pool,
		accounts:     NewAccountProvisioner(users, nil),
	}
}

func NewPluginProvider(
	config PluginProviderConfig,
	sessions *SessionRepository,
	users *UserRepository,
	pool *pgxpool.Pool,
	resolver interface {
		AuthProviderClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.AuthProviderClient, error)
	},
) *PluginProvider {
	return NewPluginProviderWithClientFactory(config, sessions, users, pool, func(ctx context.Context) (pluginAuthClient, error) {
		return resolver.AuthProviderClient(ctx, config.InstallationID, config.CapabilityID)
	})
}

func (p *PluginProvider) Authenticate(ctx context.Context, creds Credentials) (*models.User, error) {
	client, err := p.client(ctx)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrUserDisabled) {
			return nil, err
		}
		if errors.Is(err, plugins.ErrInstallationDisabled) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("load plugin auth client: %w", err)
	}

	response, err := client.Authenticate(ctx, &pluginv1.AuthenticateRequest{
		Username: creds.Username,
		Password: creds.Password,
	})
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrUserDisabled) {
			return nil, err
		}
		return nil, fmt.Errorf("plugin auth authenticate: %w", err)
	}
	if response.GetExternalSubject() == "" {
		return nil, ErrInvalidCredentials
	}

	user, err := p.lookupIdentity(ctx, response.GetExternalSubject())
	if err == nil && user != nil {
		if !user.Enabled {
			return nil, ErrUserDisabled
		}
		return user, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if !p.config.AutoProvision {
		return nil, ErrInvalidCredentials
	}

	user, err = p.autoProvisionUser(ctx, creds, response)
	if err != nil {
		return nil, err
	}
	if err := p.upsertIdentity(ctx, response.GetExternalSubject(), user.ID); err != nil {
		return nil, err
	}
	return user, nil
}

// CompleteOAuth runs the post-RPC half of plugin authentication for an
// OAuth flow: validate the AuthenticateResponse, look up an existing
// plugin_auth_identities row, auto-provision a new user if needed, and
// upsert the identity. The handler calls plugin ExchangeCode itself and
// passes the response in here.
func (p *PluginProvider) CompleteOAuth(ctx context.Context, response *pluginv1.AuthenticateResponse) (*models.User, error) {
	if response.GetExternalSubject() == "" {
		return nil, ErrInvalidCredentials
	}

	user, err := p.lookupIdentity(ctx, response.GetExternalSubject())
	if err == nil && user != nil {
		if !user.Enabled {
			return nil, ErrUserDisabled
		}
		return user, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if !p.config.AutoProvision {
		return nil, ErrInvalidCredentials
	}

	user, err = p.autoProvisionUser(ctx, Credentials{}, response)
	if err != nil {
		return nil, err
	}
	if err := p.upsertIdentity(ctx, response.GetExternalSubject(), user.ID); err != nil {
		return nil, err
	}
	return user, nil
}

// InstallationID exposes the plugin install this provider is bound to —
// used by the OAuth handler to match incoming /oauth/{install_id}/... requests.
func (p *PluginProvider) InstallationID() int { return p.config.InstallationID }

// CapabilityID exposes the bound capability slug (e.g. "whmcs").
func (p *PluginProvider) CapabilityID() string { return p.config.CapabilityID }

// OAuthClient returns a host-side gRPC client wrapping the plugin's
// AuthProvider service. Used by the OAuth handler to call InitAuthorize
// and ExchangeCode without re-resolving the installation.
func (p *PluginProvider) OAuthClient(ctx context.Context) (OAuthClient, error) {
	c, err := p.client(ctx)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (p *PluginProvider) ValidateSession(ctx context.Context, sessionID string) (bool, error) {
	if p.sessions == nil {
		return false, nil
	}
	if _, err := p.client(ctx); err != nil {
		if errors.Is(err, plugins.ErrInstallationDisabled) {
			return false, nil
		}
		return false, fmt.Errorf("load plugin auth client: %w", err)
	}
	return p.sessions.IsValid(ctx, sessionID)
}

func (p *PluginProvider) lookupIdentity(ctx context.Context, externalSubject string) (*models.User, error) {
	var userID int
	err := p.identityPool.QueryRow(ctx, `
		SELECT user_id
		FROM plugin_auth_identities
		WHERE plugin_installation_id = $1 AND external_subject = $2
	`,
		p.config.InstallationID,
		externalSubject,
	).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("lookup plugin auth identity: %w", err)
	}
	user, err := p.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (p *PluginProvider) upsertIdentity(ctx context.Context, externalSubject string, userID int) error {
	_, err := p.identityPool.Exec(ctx, `
		INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (plugin_installation_id, external_subject) DO UPDATE SET
			user_id = EXCLUDED.user_id,
			updated_at = NOW()
	`,
		p.config.InstallationID,
		externalSubject,
		userID,
	)
	if err != nil {
		return fmt.Errorf("upsert plugin auth identity: %w", err)
	}
	return nil
}

func (p *PluginProvider) autoProvisionUser(
	ctx context.Context,
	creds Credentials,
	response *pluginv1.AuthenticateResponse,
) (*models.User, error) {
	usernameBase := strings.TrimSpace(response.GetDisplayName())
	if usernameBase == "" {
		usernameBase = strings.TrimSpace(creds.Username)
	}
	if usernameBase == "" {
		usernameBase = response.GetExternalSubject()
	}
	usernameBase = sanitizeUsername(usernameBase)
	if usernameBase == "" {
		usernameBase = fmt.Sprintf("plugin_%d", p.config.InstallationID)
	}

	email := strings.TrimSpace(response.GetEmail())
	if email == "" {
		email = fmt.Sprintf("%s@plugin-%d.local", usernameBase, p.config.InstallationID)
	}

	localPasswordLoginEnabled := false
	password, err := randomPluginOnlyPassword()
	if err != nil {
		return nil, fmt.Errorf("generate plugin-only password: %w", err)
	}

	username := usernameBase
	for i := 0; i < 10; i++ {
		user, err := p.accounts.CreateAccount(ctx, CreateAccountInput{
			User: models.CreateUserInput{
				Email:                     email,
				Username:                  username,
				Password:                  password,
				LocalPasswordLoginEnabled: &localPasswordLoginEnabled,
				Role:                      "user",
			},
		})
		if err == nil {
			return user, nil
		}
		if !IsDuplicate(err) {
			return nil, fmt.Errorf("auto-provision plugin user: %w", err)
		}
		username = fmt.Sprintf("%s_%d", usernameBase, i+2)
	}
	return nil, fmt.Errorf("auto-provision plugin user: exhausted username attempts")
}

func randomPluginOnlyPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "plugin-only-" + hex.EncodeToString(buf), nil
}

func sanitizeUsername(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "_")
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "_.-")
}
