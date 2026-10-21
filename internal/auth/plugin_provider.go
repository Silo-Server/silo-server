package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	config   PluginProviderConfig
	client   pluginAuthClientFactory
	sessions *SessionRepository
	resolver *AccountResolver
}

// NewPluginProviderWithClientFactory builds a provider whose accounts are
// resolved by resolver (shared by every provider of one server).
func NewPluginProviderWithClientFactory(
	config PluginProviderConfig,
	sessions *SessionRepository,
	resolver *AccountResolver,
	clientFactory pluginAuthClientFactory,
) *PluginProvider {
	return &PluginProvider{
		config:   config,
		client:   clientFactory,
		sessions: sessions,
		resolver: resolver,
	}
}

// PluginAuthClientResolver loads the auth plugin client of one installation.
// *plugins.Service implements it.
type PluginAuthClientResolver interface {
	AuthProviderClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.AuthProviderClient, error)
}

func NewPluginProvider(
	config PluginProviderConfig,
	sessions *SessionRepository,
	resolver *AccountResolver,
	clients PluginAuthClientResolver,
) *PluginProvider {
	return NewPluginProviderWithClientFactory(config, sessions, resolver, func(ctx context.Context) (pluginAuthClient, error) {
		return clients.AuthProviderClient(ctx, config.InstallationID, config.CapabilityID)
	})
}

// Authenticate is a password sign-in through the plugin (LDAP and other
// credential providers), resolved to a Silo account.
func (p *PluginProvider) Authenticate(ctx context.Context, creds Credentials) (*models.User, error) {
	user, _, err := p.authenticateCredentials(ctx, creds, 0)
	return user, err
}

// authenticateCredentials authenticates creds with the plugin and resolves
// the answer: a sign-in when linkingUserID is 0 (every directory sign-in),
// otherwise a link to that signed-in account
// (Service.LinkCredentialsIdentity). It also answers the identity the
// sign-in went through, for the login session it opens.
func (p *PluginProvider) authenticateCredentials(ctx context.Context, creds Credentials, linkingUserID int) (*models.User, int64, error) {
	client, err := p.client(ctx)
	if err != nil {
		return nil, 0, pluginCallError(ctx, p.config.InstallationID, "load", err)
	}

	response, err := client.Authenticate(ctx, &pluginv1.AuthenticateRequest{
		Username: creds.Username,
		Password: creds.Password,
	})
	if err != nil {
		return nil, 0, pluginCallError(ctx, p.config.InstallationID, "authenticate", err)
	}
	return p.resolve(ctx, response, linkingUserID, false)
}

// CompleteOAuth runs the post-RPC half of plugin authentication for an
// OAuth flow: the handler called the plugin's ExchangeCode itself and passes
// the response in. linkingUserID is the signed-in account a linking flow
// links to, 0 for an ordinary sign-in. It answers the account and the
// identity the sign-in went through.
func (p *PluginProvider) CompleteOAuth(ctx context.Context, response *pluginv1.AuthenticateResponse, linkingUserID int) (*models.User, int64, error) {
	return p.resolve(ctx, response, linkingUserID, false)
}

// resolve runs the plugin's answer through account resolution. network says
// the answer is a network provider's (authenticatePeer).
func (p *PluginProvider) resolve(ctx context.Context, response *pluginv1.AuthenticateResponse, linkingUserID int, network bool) (*models.User, int64, error) {
	identity, err := externalIdentityFromResponse(ctx, p.config.InstallationID, response)
	if err != nil {
		return nil, 0, err
	}
	return p.resolver.Resolve(ctx, ResolveInput{
		InstallationID: p.config.InstallationID,
		AutoProvision:  p.config.AutoProvision,
		Network:        network,
		Identity:       identity,
		LinkingUserID:  linkingUserID,
	})
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

// pluginEndSessionClient is the optional AuthProviderChecks.EndSessionUrl
// half of a plugin client (*pluginhost.AuthProviderClient implements it).
type pluginEndSessionClient interface {
	EndSessionUrl(ctx context.Context, req *pluginv1.AuthEndSessionUrlRequest) (*pluginv1.AuthEndSessionUrlResponse, error)
}

// EndSessionURL asks the plugin for the provider logout URL of the
// account's identity at this installation. Empty when the account has no
// identity here, the plugin predates EndSessionUrl (Unimplemented), or it
// has no end-session endpoint. Only an absolute http(s) URL is returned,
// because the web client navigates to it.
func (p *PluginProvider) EndSessionURL(ctx context.Context, userID int, postLogoutRedirectURI string) (string, error) {
	if p.resolver == nil || p.resolver.pool == nil {
		return "", nil
	}
	identity, err := scanIdentity(p.resolver.pool.QueryRow(ctx, `SELECT `+identityColumns+`
		FROM plugin_auth_identities WHERE user_id = $1 AND plugin_installation_id = $2`, userID, p.config.InstallationID))
	if err != nil {
		if errors.Is(err, ErrIdentityNotFound) {
			return "", nil
		}
		return "", err
	}
	client, err := p.client(ctx)
	if err != nil {
		return "", err
	}
	ender, ok := client.(pluginEndSessionClient)
	if !ok {
		return "", nil
	}
	state, err := p.resolver.loadRefreshState(ctx, p.resolver.pool, identity.ID)
	if err != nil {
		return "", err
	}
	resp, err := ender.EndSessionUrl(ctx, &pluginv1.AuthEndSessionUrlRequest{
		ExternalSubject:       identity.ExternalSubject,
		RefreshState:          state,
		PostLogoutRedirectUri: postLogoutRedirectURI,
	})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return "", nil
		}
		return "", err
	}
	raw := strings.TrimSpace(resp.GetUrl())
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != schemeHTTPS && parsed.Scheme != schemeHTTP) || parsed.Host == "" {
		return "", fmt.Errorf("plugin returned an unusable end-session url")
	}
	return raw, nil
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
