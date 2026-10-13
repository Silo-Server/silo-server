package watchsync

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

const (
	oauthSessionUserCodePrefix = "oauth."
	oauthSessionTTL            = 10 * time.Minute
	oauthBinderCookiePrefix    = "silo_watch_oauth_"
	oauthBinderCookieSkew      = time.Hour
)

// authorizationCodeAuth is the browser sign-in a watch-sync plugin implements
// when its descriptor includes authorization code.
type authorizationCodeAuth interface {
	StartAuthorization(ctx context.Context, redirectURI, state string) (authorizationURL string, providerState []byte, err error)
	ExchangeAuthorizationCode(ctx context.Context, redirectURI, code string, providerState []byte) (TokenSet, error)
}

// OAuthStart is the browser sign-in URL plus the cookie that binds completion
// to the browser that started the flow.
type OAuthStart struct {
	AuthorizationURL string
	RedirectURI      string
	BinderCookie     http.Cookie
}

func (s *Service) SetPublicURL(publicURL string) {
	if s == nil {
		return
	}
	normalized := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	s.publicURL.Store(&normalized)
}

func (s *Service) publicOrigin() string {
	if s == nil {
		return ""
	}
	if value := s.publicURL.Load(); value != nil {
		return *value
	}
	return ""
}

// OAuthRedirectURI is the address a provider app must have registered.
func (s *Service) OAuthRedirectURI() (string, error) {
	origin := s.publicOrigin()
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", ErrPublicURLRequired
	}
	if parsed.Scheme != "https" {
		return "", ErrPublicURLNotHTTPS
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + OAuthCallbackPath
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String(), nil
}

// StartOAuth begins a browser sign-in and returns the provider's authorization URL.
func (s *Service) StartOAuth(ctx context.Context, userID int, profileID, providerKey string) (OAuthStart, error) {
	if userID <= 0 {
		return OAuthStart{}, fmt.Errorf("user id is required")
	}
	if profileID == "" {
		return OAuthStart{}, fmt.Errorf("profile id is required")
	}
	redirectURI, err := s.OAuthRedirectURI()
	if err != nil {
		return OAuthStart{}, err
	}
	provider, ok := s.registry.Get(providerKey)
	if !ok {
		return OAuthStart{}, UnknownProviderError{Key: providerKey}
	}
	starter, ok := provider.(authorizationCodeAuth)
	if !ok || authMethodOf(provider) != AuthMethodAuthorizationCode {
		return OAuthStart{}, ProviderCapabilityError{Key: providerKey, What: "does not support browser sign-in"}
	}
	binderValue, binderHash, err := newOAuthBinder()
	if err != nil {
		return OAuthStart{}, err
	}
	sessionID := uuid.NewString()
	authorizationURL, state, err := starter.StartAuthorization(ctx, redirectURI, sessionID)
	if err != nil {
		if limited, ok := AsRateLimited(err); ok {
			return OAuthStart{}, RateLimitedError{Provider: providerKey, RetryAfter: limited.RetryAfter}
		}
		if IsRetryableProviderError(err) {
			return OAuthStart{}, retryableProviderError{message: "watch provider authorization start failed"}
		}
		return OAuthStart{}, fmt.Errorf("watch provider authorization start failed")
	}
	if _, err := s.repo.UpsertAuthSession(ctx, DeviceAuthSession{
		ID:              sessionID,
		Provider:        providerKey,
		UserID:          userID,
		ProfileID:       profileID,
		DeviceCode:      base64.RawURLEncoding.EncodeToString(state),
		UserCode:        oauthSessionUserCodePrefix + binderHash,
		VerificationURL: redirectURI,
		ExpiresAt:       s.now().Add(oauthSessionTTL),
	}); err != nil {
		return OAuthStart{}, err
	}
	return OAuthStart{
		AuthorizationURL: authorizationURL,
		RedirectURI:      redirectURI,
		BinderCookie:     oauthBinderCookie(sessionID, binderValue, redirectURI, int((oauthSessionTTL+oauthBinderCookieSkew)/time.Second)),
	}, nil
}

// FinishOAuth exchanges the provider's code for the session state names.
// The returned provider key is the one the browser should land on.
// binder is the value of the HttpOnly cookie StartOAuth set on the initiating browser.
func (s *Service) FinishOAuth(ctx context.Context, state, code, binder string) (string, error) {
	if _, err := uuid.Parse(strings.TrimSpace(state)); err != nil {
		return "", ErrAuthSessionMismatch
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", fmt.Errorf("authorization code is required")
	}
	session, err := s.repo.GetAuthSession(ctx, state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAuthSessionMismatch
	}
	if err != nil {
		return "", err
	}
	binderHash, ok := oauthBinderHashFromUserCode(session.UserCode)
	if !ok {
		return "", ErrAuthSessionMismatch
	}
	if !oauthBinderMatches(binder, binderHash) {
		return session.Provider, ErrAuthSessionMismatch
	}
	if session.CompletedAt != nil {
		return session.Provider, ErrAuthSessionCompleted
	}
	if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(s.now()) {
		return session.Provider, ErrAuthSessionExpired
	}
	claimed, err := s.repo.ClaimAuthSession(ctx, state, s.now())
	if err != nil {
		return session.Provider, err
	}
	session = claimed
	provider, ok := s.registry.Get(session.Provider)
	if !ok {
		return session.Provider, UnknownProviderError{Key: session.Provider}
	}
	exchanger, ok := provider.(authorizationCodeAuth)
	if !ok {
		return session.Provider, ProviderCapabilityError{Key: session.Provider, What: "does not support browser sign-in"}
	}
	redirectURI := strings.TrimSpace(session.VerificationURL)
	if redirectURI == "" {
		redirectURI, err = s.OAuthRedirectURI()
		if err != nil {
			return session.Provider, err
		}
	}
	providerState, err := base64.RawURLEncoding.DecodeString(session.DeviceCode)
	if err != nil {
		return session.Provider, fmt.Errorf("watch provider authorization state is invalid")
	}
	cfg, err := s.serverConfig(ctx, session.Provider)
	if err != nil {
		return session.Provider, err
	}
	tokens, err := exchanger.ExchangeAuthorizationCode(ctx, redirectURI, code, providerState)
	if err != nil {
		if limited, ok := AsRateLimited(err); ok {
			return session.Provider, RateLimitedError{Provider: session.Provider, RetryAfter: limited.RetryAfter}
		}
		if IsRetryableProviderError(err) {
			return session.Provider, retryableProviderError{message: "watch provider authorization exchange failed"}
		}
		return session.Provider, fmt.Errorf("watch provider authorization exchange failed")
	}
	accountProvider, ok := provider.(AuthProvider)
	if !ok {
		return session.Provider, ProviderCapabilityError{Key: session.Provider, What: "does not support auth"}
	}
	account, err := accountProvider.LookupAccount(ctx, cfg, connectionWithTokens(Connection{}, tokens))
	if err != nil {
		if limited, ok := AsRateLimited(err); ok {
			return session.Provider, RateLimitedError{Provider: session.Provider, RetryAfter: limited.RetryAfter}
		}
		if IsRetryableProviderError(err) {
			return session.Provider, retryableProviderError{message: "watch provider account lookup failed"}
		}
		return session.Provider, fmt.Errorf("watch provider account lookup failed")
	}
	if _, err := s.persistConnection(ctx, session.Provider, session.UserID, session.ProfileID, tokens, account); err != nil {
		return session.Provider, err
	}
	return session.Provider, nil
}

// ClearOAuthBinderCookie expires the browser-binding cookie for state.
func ClearOAuthBinderCookie(state, callbackURL string) http.Cookie {
	return oauthBinderCookie(state, "", callbackURL, -1)
}

// OAuthBinderCallbackURL is the redirect URI StartOAuth stored for state, used
// to clear the binder cookie on the same Path it was set with.
func (s *Service) OAuthBinderCallbackURL(ctx context.Context, state string) (string, error) {
	session, err := s.repo.GetAuthSession(ctx, state)
	if err != nil {
		return "", err
	}
	if !isOAuthAuthSession(session) {
		return "", ErrAuthSessionMismatch
	}
	if uri := strings.TrimSpace(session.VerificationURL); uri != "" {
		return uri, nil
	}
	return s.OAuthRedirectURI()
}

// OnPublicOrigin reports whether r is on the configured https public URL origin
// so the binder cookie is set where the provider callback will land.
func (s *Service) OnPublicOrigin(r *http.Request) bool {
	if s == nil || r == nil {
		return true
	}
	base, err := url.Parse(s.publicOrigin())
	if err != nil || base.Host == "" {
		return true
	}
	return strings.EqualFold(base.Scheme, clientip.RequestScheme(r)) &&
		strings.EqualFold(base.Host, clientip.RequestHost(r))
}

func isOAuthAuthSession(session DeviceAuthSession) bool {
	return strings.HasPrefix(session.UserCode, oauthSessionUserCodePrefix)
}

func oauthBinderHashFromUserCode(userCode string) (string, bool) {
	if !strings.HasPrefix(userCode, oauthSessionUserCodePrefix) {
		return "", false
	}
	hash := strings.TrimPrefix(userCode, oauthSessionUserCodePrefix)
	return hash, hash != ""
}

func newOAuthBinder() (value, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generating watch provider oauth binder: %w", err)
	}
	value = base64.RawURLEncoding.EncodeToString(buf)
	return value, oauthBinderHash(value), nil
}

func oauthBinderHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func oauthBinderMatches(presented, storedHash string) bool {
	if presented == "" || storedHash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(oauthBinderHash(presented)), []byte(storedHash)) == 1
}

// OAuthBinderCookieNameForState is the browser-binding cookie StartOAuth sets
// for this authorization state.
func OAuthBinderCookieNameForState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return oauthBinderCookiePrefix + hex.EncodeToString(sum[:8])
}

func oauthBinderCookie(state, value, callbackURL string, maxAge int) http.Cookie {
	path := OAuthCallbackPath
	secure := false
	if u, err := url.Parse(callbackURL); err == nil {
		if u.Path != "" {
			path = u.Path
		}
		secure = strings.EqualFold(u.Scheme, "https")
	}
	return http.Cookie{
		Name:     OAuthBinderCookieNameForState(state),
		Value:    value,
		Path:     path,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}
