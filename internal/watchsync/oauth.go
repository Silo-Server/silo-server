package watchsync

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	oauthSessionUserCode = "oauth"
	oauthSessionTTL      = 10 * time.Minute
)

// authorizationCodeAuth is the browser sign-in a watch-sync plugin implements
// when its descriptor includes authorization code.
type authorizationCodeAuth interface {
	StartAuthorization(ctx context.Context, redirectURI, state string) (authorizationURL string, providerState []byte, err error)
	ExchangeAuthorizationCode(ctx context.Context, redirectURI, code string, providerState []byte) (TokenSet, error)
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
	return origin + OAuthCallbackPath, nil
}

// StartOAuth begins a browser sign-in and returns the provider's authorization URL.
func (s *Service) StartOAuth(ctx context.Context, userID int, profileID, providerKey string) (string, error) {
	if userID <= 0 {
		return "", fmt.Errorf("user id is required")
	}
	if profileID == "" {
		return "", fmt.Errorf("profile id is required")
	}
	redirectURI, err := s.OAuthRedirectURI()
	if err != nil {
		return "", err
	}
	provider, ok := s.registry.Get(providerKey)
	if !ok {
		return "", UnknownProviderError{Key: providerKey}
	}
	starter, ok := provider.(authorizationCodeAuth)
	if !ok || authMethodOf(provider) != AuthMethodAuthorizationCode {
		return "", ProviderCapabilityError{Key: providerKey, What: "does not support browser sign-in"}
	}
	sessionID := uuid.NewString()
	authorizationURL, state, err := starter.StartAuthorization(ctx, redirectURI, sessionID)
	if err != nil {
		return "", err
	}
	if _, err := s.repo.UpsertAuthSession(ctx, DeviceAuthSession{
		ID:              sessionID,
		Provider:        providerKey,
		UserID:          userID,
		ProfileID:       profileID,
		DeviceCode:      base64.RawURLEncoding.EncodeToString(state),
		UserCode:        oauthSessionUserCode,
		VerificationURL: authorizationURL,
		ExpiresAt:       s.now().Add(oauthSessionTTL),
	}); err != nil {
		return "", err
	}
	return authorizationURL, nil
}

// FinishOAuth exchanges the provider's code for the session state names.
// The returned provider key is the one the browser should land on.
func (s *Service) FinishOAuth(ctx context.Context, state, code string) (string, error) {
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
	if session.UserCode != oauthSessionUserCode {
		return "", ErrAuthSessionMismatch
	}
	if session.CompletedAt != nil {
		return session.Provider, ErrAuthSessionCompleted
	}
	if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(s.now()) {
		return session.Provider, ErrAuthSessionExpired
	}
	provider, ok := s.registry.Get(session.Provider)
	if !ok {
		return session.Provider, UnknownProviderError{Key: session.Provider}
	}
	exchanger, ok := provider.(authorizationCodeAuth)
	if !ok {
		return session.Provider, ProviderCapabilityError{Key: session.Provider, What: "does not support browser sign-in"}
	}
	redirectURI, err := s.OAuthRedirectURI()
	if err != nil {
		return session.Provider, err
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
		return session.Provider, err
	}
	accountProvider, ok := provider.(AuthProvider)
	if !ok {
		return session.Provider, ProviderCapabilityError{Key: session.Provider, What: "does not support auth"}
	}
	account, err := accountProvider.LookupAccount(ctx, cfg, connectionWithTokens(Connection{}, tokens))
	if err != nil {
		return session.Provider, err
	}
	if _, err := s.persistConnection(ctx, session.Provider, session.UserID, session.ProfileID, tokens, account); err != nil {
		return session.Provider, err
	}
	completedAt := s.now()
	session.CompletedAt = &completedAt
	if _, err := s.repo.UpsertAuthSession(ctx, session); err != nil {
		return session.Provider, err
	}
	return session.Provider, nil
}
