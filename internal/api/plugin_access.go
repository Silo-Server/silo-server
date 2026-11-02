package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// pluginAccessResolver decides who a plugin route or asset request comes
// from. Plugin routes accept an optional credential, so they run outside the
// auth middleware and resolve the caller here, with the same rules the
// viewer and admin gates apply elsewhere.
type pluginAccessResolver struct {
	jwt      *auth.JWTService
	sessions pluginSessionChecker
	// renewer re-issues a launch cookie's token under the checks token
	// refresh applies to the login session: auth.Service in production. Nil
	// turns renewal off.
	renewer pluginRenewer
	// apiKeys and users enable sa_* API keys; nil leaves them out.
	apiKeys pluginAPIKeys
	users   pluginUsers
	// viewer verifies a declared X-Profile-Id: the profile must belong to the
	// account, and a PIN-locked one needs its X-Profile-Token.
	viewer apimw.ViewerResolver
	// actingAdmin is the decision the admin route groups use: the admin role,
	// exercised through the account's primary profile.
	actingAdmin apimw.ActingAdminDecider
	// noRenewal skips launch-cookie renewal for callers that cannot deliver
	// a renewed cookie, so they don't pay for the profile check and signing.
	noRenewal bool
}

// pluginSessionChecker is the session lookup the resolver needs:
// auth.SessionRepository in production.
type pluginSessionChecker interface {
	ActiveSession(ctx context.Context, sessionID string, userID int) (auth.SessionAuthority, bool, error)
}

// pluginRenewer is auth.Service's plugin token renewal.
type pluginRenewer interface {
	RenewPluginAccess(ctx context.Context, claims *auth.Claims, ttl time.Duration) (string, error)
}

// pluginAPIKeys and pluginUsers are the API-key lookups the resolver needs:
// auth.APIKeyRepository and auth.UserRepository in production.
type pluginAPIKeys interface {
	GetByKey(ctx context.Context, key string) (*models.APIKey, error)
}

type pluginUsers interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// newPluginAccessResolver builds a resolver over the router's repositories.
// A nil repository leaves its field unset, so the resolver treats that
// credential kind as unavailable rather than calling a nil repository.
func newPluginAccessResolver(
	jwt *auth.JWTService, sessions *auth.SessionRepository, renewer *auth.Service,
	apiKeys *auth.APIKeyRepository, users *auth.UserRepository,
	viewer apimw.ViewerResolver, actingAdmin apimw.ActingAdminDecider,
) *pluginAccessResolver {
	resolver := &pluginAccessResolver{jwt: jwt, viewer: viewer, actingAdmin: actingAdmin}
	if sessions != nil {
		resolver.sessions = sessions
	}
	if renewer != nil {
		resolver.renewer = renewer
	}
	if apiKeys != nil && users != nil {
		resolver.apiKeys, resolver.users = apiKeys, users
	}
	return resolver
}

// pluginAccess is the resolved caller of one plugin request.
type pluginAccess struct {
	authenticated bool
	admin         bool
	userID        int
	// profileID is the verified active profile, "" for none.
	profileID string
	// renewedToken, when set, replaces the plugin launch cookie: the request
	// came with a launch cookie past half its lifetime and its login session
	// could still refresh.
	renewedToken string
}

// pluginRenewAfter is how old a launch cookie gets before a request through
// it is answered with a fresh one. A plugin page that keeps calling its
// routes therefore stays signed in for as long as its login session lasts.
const pluginRenewAfter = handlers.PluginLaunchTTL / 2

func (p *pluginAccessResolver) resolve(r *http.Request) pluginAccess {
	if p == nil || p.jwt == nil || p.sessions == nil {
		return pluginAccess{}
	}
	token, fromCookie := pluginRequestToken(r)
	if token == "" {
		return pluginAccess{}
	}
	if strings.HasPrefix(token, "sa_") {
		return p.resolveAPIKey(r, token)
	}

	claims, err := p.jwt.ValidateToken(token)
	if err != nil || (claims.TokenType != auth.TokenTypeAccess && claims.TokenType != auth.TokenTypePluginAccess) {
		return pluginAccess{}
	}
	// A session holding a temporary password may only change it.
	if claims.PasswordChangeRequired {
		return pluginAccess{}
	}
	// Plugin launch tokens copy the role of the access token they were minted
	// from, and a role change keeps the session, so admin access follows the
	// account's current role rather than the token's.
	// A remote-playback handoff only plays on its device; plugins are not
	// part of that.
	authority, active, err := p.sessions.ActiveSession(r.Context(), claims.SessionID, claims.UserID)
	if err != nil || !active || authority.IsRemotePlayback() {
		return pluginAccess{}
	}
	current := *claims
	current.Role = authority.Role

	// A token bound to a profile was minted for a verified one (the launch
	// cookie checks the profile when it is issued). Otherwise a declared
	// X-Profile-Id counts only once the viewer gate's rules accept it.
	profileID := claims.ProfileID
	if profileID == "" {
		var ok bool
		profileID, ok = p.declaredProfile(r, &current, false)
		if !ok {
			return pluginAccess{}
		}
	}

	result := pluginAccess{
		authenticated: true,
		admin:         p.actingAsAdmin(r, &current, profileID),
		userID:        claims.UserID,
		profileID:     profileID,
	}
	if !p.noRenewal && p.renewer != nil && fromCookie && claims.TokenType == auth.TokenTypePluginAccess && claims.ExpiresAt != nil &&
		time.Until(claims.ExpiresAt.Time) < handlers.PluginLaunchTTL-pluginRenewAfter &&
		p.profileUnchanged(r, &current) {
		// A session that could not refresh now (revoked, an impersonating
		// admin demoted, a provider re-check refused or failing closed) is
		// not renewed; the cookie runs out on its own.
		if renewed, err := p.renewer.RenewPluginAccess(r.Context(), claims, handlers.PluginLaunchTTL); err == nil {
			result.renewedToken = renewed
		}
	}
	return result
}

// profileUnchanged reports whether a launch cookie's profile can be carried
// into a renewed cookie: it must still exist, belong to the account and have
// the PIN revision the cookie was launched at. The PIN was proven at launch;
// a changed revision means the PIN was set, changed or cleared since, which
// ends the profile's verification tokens (access.CheckProfileToken) and so
// ends renewal too. A cookie from a release that did not record the revision
// is not renewed and expires on its own. A profile-less cookie has nothing
// to check.
func (p *pluginAccessResolver) profileUnchanged(r *http.Request, claims *auth.Claims) bool {
	if claims.ProfileID == "" {
		return true
	}
	if p.viewer == nil || claims.ProfilePINRevision == nil {
		return false
	}
	scope, err := p.viewer.Resolve(r.Context(), access.ResolveInput{
		UserID:              claims.UserID,
		SessionID:           claims.SessionID,
		ProfileID:           claims.ProfileID,
		SkipPINVerification: true,
	})
	return err == nil && scope.ProfilePINRevision == *claims.ProfilePINRevision
}

func (p *pluginAccessResolver) resolveAPIKey(r *http.Request, token string) pluginAccess {
	if p.apiKeys == nil || p.users == nil {
		return pluginAccess{}
	}
	apiKey, err := p.apiKeys.GetByKey(r.Context(), token)
	if err != nil {
		return pluginAccess{}
	}
	// Scoped keys are allowlist credentials for the routes their scopes
	// name; plugin access is not on any scope's allowlist.
	if len(apiKey.Scopes) > 0 {
		return pluginAccess{}
	}
	user, err := p.users.GetByID(r.Context(), apiKey.UserID)
	if err != nil || !user.Enabled {
		return pluginAccess{}
	}
	claims := &auth.Claims{UserID: user.ID, Role: user.Role, TokenType: auth.TokenTypeAPIKey, APIKeyID: apiKey.ID}
	// API keys have no PIN proof by design; a declared profile must still
	// exist and belong to the account.
	profileID, ok := p.declaredProfile(r, claims, true)
	if !ok {
		return pluginAccess{}
	}
	return pluginAccess{
		authenticated: true,
		admin:         p.actingAsAdmin(r, claims, profileID),
		userID:        user.ID,
		profileID:     profileID,
	}
}

// declaredProfile verifies the request's X-Profile-Id. It returns "" and true
// when none is declared, and false when the viewer gate would refuse it: an
// unknown profile, another account's, a PIN-locked one without a valid
// X-Profile-Token, or a check that could not complete. Such a request is
// served as anonymous rather than as the account, failing closed.
func (p *pluginAccessResolver) declaredProfile(r *http.Request, claims *auth.Claims, skipPIN bool) (string, bool) {
	declared := strings.TrimSpace(r.Header.Get("X-Profile-Id"))
	if declared == "" {
		return "", true
	}
	if p.viewer == nil {
		return "", false
	}
	_, err := p.viewer.Resolve(r.Context(), access.ResolveInput{
		UserID:              claims.UserID,
		SessionID:           claims.SessionID,
		ProfileID:           declared,
		ProfileToken:        r.Header.Get("X-Profile-Token"),
		SkipPINVerification: skipPIN,
	})
	if err != nil {
		return "", false
	}
	return declared, true
}

// actingAsAdmin applies the admin route groups' rule: the admin role is not
// enough, the request must act as the account's primary profile (or as no
// profile while the household does not require one). It fails closed.
func (p *pluginAccessResolver) actingAsAdmin(r *http.Request, claims *auth.Claims, profileID string) bool {
	if claims.Role != models.RoleAdmin || p.actingAdmin == nil {
		return false
	}
	allowed, err := p.actingAdmin(r.Context(), apimw.ActingAdminRequest{
		Claims:    claims,
		ProfileID: profileID,
		// The sign-in normalizer drops a malformed or over-long device id
		// rather than passing it to the decision.
		DeviceID: auth.ClientDeviceFromHeaders(r.Header).ID,
	})
	return err == nil && allowed
}

// pluginRequestToken returns the request's credential and whether it came
// from the plugin launch cookie.
func pluginRequestToken(r *http.Request) (string, bool) {
	if header := r.Header.Get("Authorization"); header != "" {
		parts := strings.SplitN(header, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			if token := strings.TrimSpace(parts[1]); token != "" {
				return token, false
			}
		}
	}
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		return token, false
	}
	if cookie, err := r.Cookie(auth.PluginAccessCookieName); err == nil {
		return strings.TrimSpace(cookie.Value), true
	}
	return "", false
}

// renewedPluginCookie is the launch cookie that replaces the request's on
// path, with the attributes the launch endpoints use, or nil when the
// request's cookie needs no renewal. Callers set it with
// plugins.WithRenewedCookie.
func renewedPluginCookie(r *http.Request, access pluginAccess, path string) *http.Cookie {
	if access.renewedToken == "" {
		return nil
	}
	// Secure follows the request's scheme exactly as the launch endpoints set
	// it, so a renewed cookie replaces the launched one on plain-HTTP LAN
	// deployments too. HttpOnly and SameSite=Lax are always set.
	return &http.Cookie{ //nolint:gosec // G124: Secure must match the launch cookie it replaces
		Name:     auth.PluginAccessCookieName,
		Value:    access.renewedToken,
		Path:     path,
		MaxAge:   int(handlers.PluginLaunchTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   handlers.IsSecureRequest(r),
	}
}
