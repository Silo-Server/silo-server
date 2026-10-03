package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/plugins"
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
	// A network identity never matches an account by email: whoever uses
	// the device is its owner, which says nothing about who owns the address.
	// A nil response is left for resolve to refuse.
	if response != nil {
		response.EmailVerified = nil
	}
	return p.resolve(ctx, response, linkingUserID, true)
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
		UserID: user.ID, IdentityID: identityID, DeviceName: in.DeviceName, IP: in.IP, Network: true,
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

// primaryAuthority is what decides for an account that has identities at
// both a network provider and an enabled primary (non-network) sign-in
// provider: the primary provider is the account's authority.
type primaryAuthority struct {
	// defers: the installation is a network provider and the account has an
	// identity at an enabled primary provider. The network identity then
	// neither sets the account's role nor, when its provider refuses it, ends
	// more than the sessions opened through it. Otherwise the two would each
	// apply their own role and sign the account out at every change.
	defers bool
	// refused: defers, and the primary provider's latest answer refused the
	// account, so the network identity cannot sign it back in. A refusal
	// whose revocation rolled back counts: it is kept as pending_refusal
	// while last_check_status still holds the previous answer.
	refused bool
}

// primaryAuthorityOf answers primaryAuthority for a network identity of
// userID at installationID; the zero value for any other installation.
func primaryAuthorityOf(ctx context.Context, db rowQuerier, userID, installationID int) (primaryAuthority, error) {
	isNetwork := plugins.AuthBindingIsNetworkSQL("b.plugin_installation_id", "b.capability_id")
	var network, primary, refused bool
	err := db.QueryRow(ctx, `WITH primary_identities AS (
			SELECT i.last_check_status, i.pending_refusal FROM plugin_auth_identities i
			JOIN plugin_installations p ON p.id = i.plugin_installation_id AND p.enabled
			JOIN plugin_auth_bindings b ON b.plugin_installation_id = p.id AND b.enabled
			WHERE i.user_id = $1 AND i.plugin_installation_id <> $2 AND NOT `+isNetwork+`)
		SELECT EXISTS (SELECT 1 FROM plugin_auth_bindings b WHERE b.plugin_installation_id = $2 AND `+isNetwork+`),
			EXISTS (SELECT 1 FROM primary_identities),
			EXISTS (SELECT 1 FROM primary_identities WHERE last_check_status = ANY($3) OR pending_refusal = ANY($3))`,
		userID, installationID, []string{CheckStatusNotFound, CheckStatusDisabled, CheckStatusNotPermitted}).Scan(&network, &primary, &refused)
	if err != nil {
		return primaryAuthority{}, fmt.Errorf("checking for a primary sign-in identity: %w", err)
	}
	defers := network && primary
	return primaryAuthority{defers: defers, refused: defers && refused}, nil
}

// networkPreviewTTL bounds how long discovery reuses a plugin's answer about
// one peer. Sign-in and linking always ask again.
const networkPreviewTTL = 30 * time.Second

// networkPreviewTimeout bounds discovery's lookup, loading the plugin client
// included: the provider list waits on it, so a plugin that hangs costs the
// login page at most this long and leaves the network provider out.
const networkPreviewTimeout = 2 * time.Second

// networkPreviewLimit caps the cached answers; discovery is unauthenticated,
// though only overlay peers reach it.
const networkPreviewLimit = 4096

// networkPreviewKey keys a cached answer by the provider instance, not its
// installation: the registry builds new instances whenever bindings or the
// plugin's configuration change, so a changed access rule is never answered
// from the cache. Expired answers, a replaced instance's included, are
// dropped at the next miss.
type networkPreviewKey struct {
	provider *PluginProvider
	peer     netip.Addr
}

type networkPreviewEntry struct {
	// preview is nil when the plugin refused the peer.
	preview *NetworkIdentityPreview
	expires time.Time
}

// networkPreviews caches the plugin's answers for discovery. lookups runs
// one plugin call per key at a time: concurrent misses share its answer.
type networkPreviews struct {
	mu      sync.Mutex
	entries map[networkPreviewKey]networkPreviewEntry
	lookups singleflight.Group
	// joined, when set (tests), runs once a cache miss has joined or
	// started the shared lookup, before it waits for the answer.
	joined func()
}

// networkPreview answers who the plugin says the request's peer is, or nil
// when the request has no peer from this provider, the plugin refuses the
// peer, or it cannot answer. Refusals and identities are cached briefly per
// peer; a failure to answer, or no answer within networkPreviewTimeout, is
// not.
func (s *Service) networkPreview(ctx context.Context, provider *PluginProvider) *NetworkIdentityPreview {
	peer, err := requestPeer(ctx, provider.InstallationID())
	if err != nil {
		return nil
	}
	key := networkPreviewKey{provider: provider, peer: peer}
	if preview, ok := s.cachedNetworkPreview(key); ok {
		return preview
	}
	lookup := s.previews.lookups.DoChan(fmt.Sprintf("%p|%s", provider, peer), func() (any, error) {
		// The shared lookup outlives a caller that goes away, but not
		// networkPreviewTimeout.
		lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), networkPreviewTimeout)
		defer cancel()
		return s.lookupNetworkPreview(lookupCtx, key), nil
	})
	if s.previews.joined != nil {
		s.previews.joined()
	}
	preview, _ := (<-lookup).Val.(*NetworkIdentityPreview)
	return preview
}

// lookupNetworkPreview asks the plugin about key's peer and caches a
// refusal or an identity. A caller that missed the cache while the previous
// lookup was finishing finds its answer here instead of asking again.
func (s *Service) lookupNetworkPreview(ctx context.Context, key networkPreviewKey) *NetworkIdentityPreview {
	if preview, ok := s.cachedNetworkPreview(key); ok {
		return preview
	}
	provider, peer := key.provider, key.peer
	now := time.Now()
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
	for cached, entry := range s.previews.entries {
		if !now.Before(entry.expires) {
			delete(s.previews.entries, cached)
		}
	}
	if s.previews.entries == nil || len(s.previews.entries) >= networkPreviewLimit {
		s.previews.entries = make(map[networkPreviewKey]networkPreviewEntry)
	}
	s.previews.entries[key] = networkPreviewEntry{preview: preview, expires: now.Add(networkPreviewTTL)}
	s.previews.mu.Unlock()
	return preview
}

// cachedNetworkPreview is the unexpired cached answer for key, if any.
func (s *Service) cachedNetworkPreview(key networkPreviewKey) (*NetworkIdentityPreview, bool) {
	s.previews.mu.Lock()
	defer s.previews.mu.Unlock()
	entry, ok := s.previews.entries[key]
	if !ok || !time.Now().Before(entry.expires) {
		return nil, false
	}
	return entry.preview, true
}
