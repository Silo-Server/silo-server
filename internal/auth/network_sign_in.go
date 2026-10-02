package auth

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// Network identity sign-in (docs/architecture/external-sign-in.md#network-identity):
// a network access provider plugin whose auth_provider.v1 capability declares
// the "network" mode signs in the person whose overlay device sent the
// request. Only a request that arrived through that plugin's own listener,
// with its ingress token and a peer it named (netaccess.Path), has a peer to
// ask about; the plugin decides who the peer is (AuthenticatePeer), and the
// answer goes through the same account resolution as every other provider.

// ErrNetworkIdentityRequired: the request did not arrive through the
// provider's overlay with a peer the provider named, so there is nobody to
// sign in.
var ErrNetworkIdentityRequired = errors.New("network identity sign-in needs a request through the provider's overlay")

// NetworkIdentityPreview is who a network provider says the peer of the
// current request is, for the sign-in button's label. It authorizes nothing.
type NetworkIdentityPreview struct {
	DisplayName string `json:"display_name"`
	Username    string `json:"username"`
}

// NetworkSignInInput is a network identity sign-in as the transport received
// it. The peer comes from the request context, never from the client.
type NetworkSignInInput struct {
	InstallationID int
	DeviceName     string
	IP             string
}

// NetworkLinkInput is a signed-in account linking the network identity of the
// request's peer, after re-entering its local password.
type NetworkLinkInput struct {
	UserID         int
	InstallationID int
	Password       string
}

// pluginNetworkClient is the NetworkIdentityAuth half of a plugin client
// (*pluginhost.AuthProviderClient implements it).
type pluginNetworkClient interface {
	AuthenticatePeer(ctx context.Context, req *pluginv1.AuthenticatePeerRequest) (*pluginv1.AuthenticateResponse, error)
}

// requestPeer is the overlay peer of the request in ctx when it came through
// installationID's own listener; otherwise ErrNetworkIdentityRequired.
func requestPeer(ctx context.Context, installationID int) (netip.Addr, error) {
	path := netaccess.PathFromContext(ctx)
	if installationID <= 0 || path.InstallationID != installationID || !path.Peer.IsValid() {
		return netip.Addr{}, ErrNetworkIdentityRequired
	}
	return path.Peer, nil
}

// peerResponse asks the plugin who peer is.
func (p *PluginProvider) peerResponse(ctx context.Context, peer netip.Addr) (*pluginv1.AuthenticateResponse, error) {
	client, err := p.client(ctx)
	if err != nil {
		return nil, pluginCallError(ctx, p.config.InstallationID, "load", err)
	}
	network, ok := client.(pluginNetworkClient)
	if !ok {
		return nil, ErrProviderUnavailable
	}
	response, err := network.AuthenticatePeer(ctx, &pluginv1.AuthenticatePeerRequest{PeerAddress: peer.String()})
	if err != nil {
		return nil, pluginCallError(ctx, p.config.InstallationID, "authenticate_peer", err)
	}
	return response, nil
}

// authenticatePeer resolves the person the plugin answers for peer: a
// sign-in when linkingUserID is 0, otherwise a link to that signed-in
// account. It also answers the identity, for the login session it opens.
func (p *PluginProvider) authenticatePeer(ctx context.Context, peer netip.Addr, linkingUserID int) (*models.User, int64, error) {
	response, err := p.peerResponse(ctx, peer)
	if err != nil {
		return nil, 0, err
	}
	return p.resolve(ctx, response, linkingUserID)
}

// findNetworkInstallation returns the PluginProvider registered for the
// installation when it is an enabled network provider; nil otherwise.
func (s *Service) findNetworkInstallation(installationID int) *PluginProvider {
	if installationID <= 0 {
		return nil
	}
	for _, registered := range s.pluginProviders() {
		pp, ok := registered.Provider.(*PluginProvider)
		if ok && pp != nil && pp.InstallationID() == installationID && registered.Info.Mode == ProviderModeNetwork {
			return pp
		}
	}
	return nil
}

// networkRequest is the enabled network provider of installationID and the
// overlay peer of the request in ctx: ErrUnknownAuthInstallation without
// such a provider, ErrNetworkIdentityRequired when the request did not come
// through its listener with a peer.
func (s *Service) networkRequest(ctx context.Context, installationID int) (*PluginProvider, netip.Addr, error) {
	provider := s.findNetworkInstallation(installationID)
	if provider == nil {
		return nil, netip.Addr{}, ErrUnknownAuthInstallation
	}
	peer, err := requestPeer(ctx, installationID)
	if err != nil {
		return nil, netip.Addr{}, err
	}
	return provider, peer, nil
}

// NetworkSignIn signs in the overlay peer of the request in ctx through the
// network provider of in.InstallationID and opens a login session vouched for
// by its identity, like an OAuth sign-in. Errors: ErrUnknownAuthInstallation
// (no enabled network provider has the installation),
// ErrNetworkIdentityRequired (the request has no peer from that provider),
// the plugin's refusals as sign-in errors (ErrNotPermitted,
// ErrProviderUnavailable, ErrInvalidCredentials), and account resolution's
// (ErrAccountRequired, ErrEmailInUse, ErrUserDisabled).
func (s *Service) NetworkSignIn(ctx context.Context, in NetworkSignInInput) (*TokenPair, error) {
	provider, peer, err := s.networkRequest(ctx, in.InstallationID)
	if err != nil {
		return nil, err
	}
	user, identityID, err := provider.authenticatePeer(ctx, peer, 0)
	if err != nil {
		return nil, err
	}
	return s.OpenIdentitySession(ctx, nil, IdentitySession{
		UserID: user.ID, IdentityID: identityID, DeviceName: in.DeviceName, IP: in.IP,
	})
}

// LinkNetworkIdentity links the network identity of the request's peer to
// the signed-in account, after the account re-entered its local password,
// with the rules of LinkCredentialsIdentity: the identity is linked through
// account resolution, the provider's managed role applies, and local password
// sign-in turns off unless the account is break-glass. Errors are
// LinkCredentialsIdentity's, plus ErrNetworkIdentityRequired.
func (s *Service) LinkNetworkIdentity(ctx context.Context, in NetworkLinkInput) (*LinkedIdentity, error) {
	provider, peer, err := s.networkRequest(ctx, in.InstallationID)
	if err != nil {
		return nil, err
	}
	if s.users == nil {
		return nil, ErrProviderUnavailable
	}
	if _, err := confirmLocalPassword(ctx, s.users, in.UserID, in.Password); err != nil {
		return nil, err
	}
	_, identityID, err := provider.authenticatePeer(ctx, peer, in.UserID)
	if err != nil {
		return nil, err
	}
	return identityByID(ctx, provider.resolver.pool, identityID)
}

// networkPreviewTTL bounds how long discovery reuses a plugin's answer about
// one peer. Sign-in and linking always ask again.
const networkPreviewTTL = 30 * time.Second

// networkPreviewLimit caps the cached answers; discovery is unauthenticated,
// though only overlay peers reach it.
const networkPreviewLimit = 4096

type networkPreviewKey struct {
	installationID int
	peer           netip.Addr
}

type networkPreviewEntry struct {
	// preview is nil when the plugin refused the peer.
	preview *NetworkIdentityPreview
	expires time.Time
}

// networkPreviews caches the plugin's answers for discovery.
type networkPreviews struct {
	mu      sync.Mutex
	entries map[networkPreviewKey]networkPreviewEntry
}

// networkPreview answers who the plugin says the request's peer is, or nil
// when the request has no peer from this provider, the plugin refuses the
// peer, or it cannot answer. Refusals and identities are cached briefly per
// peer; a failure to answer is not.
func (s *Service) networkPreview(ctx context.Context, provider *PluginProvider) *NetworkIdentityPreview {
	peer, err := requestPeer(ctx, provider.InstallationID())
	if err != nil {
		return nil
	}
	key := networkPreviewKey{installationID: provider.InstallationID(), peer: peer}
	now := time.Now()
	s.previews.mu.Lock()
	entry, ok := s.previews.entries[key]
	s.previews.mu.Unlock()
	if ok && now.Before(entry.expires) {
		return entry.preview
	}
	response, err := provider.peerResponse(ctx, peer)
	if err != nil {
		return nil
	}
	var preview *NetworkIdentityPreview
	if identity, err := externalIdentityFromResponse(ctx, provider.InstallationID(), response); err == nil {
		preview = &NetworkIdentityPreview{DisplayName: identity.DisplayName, Username: identity.Username}
	} else if errors.Is(err, ErrProviderUnavailable) {
		return nil
	}
	s.previews.mu.Lock()
	if s.previews.entries == nil || len(s.previews.entries) >= networkPreviewLimit {
		s.previews.entries = make(map[networkPreviewKey]networkPreviewEntry)
	}
	s.previews.entries[key] = networkPreviewEntry{preview: preview, expires: now.Add(networkPreviewTTL)}
	s.previews.mu.Unlock()
	return preview
}
