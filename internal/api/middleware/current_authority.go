package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
)

var ErrCurrentCredentialInvalid = errors.New("current credential is invalid")
var ErrCurrentCredentialForbidden = errors.New("current credential does not permit this operation")

// RevalidateCurrent rechecks the exact credential after a bounded operation.
// It neither replaces the captured identity nor updates API-key usage metadata.
func (am *AuthMiddleware) RevalidateCurrent(ctx context.Context, bearer string, expected *auth.Claims, method, path string) error {
	token, ok := parseBearerHeader(bearer)
	if am == nil || expected == nil || !ok {
		return ErrCurrentCredentialInvalid
	}
	if expected.TokenType == auth.TokenTypeAPIKey {
		if am.apiKeyValidator == nil {
			return ErrCurrentCredentialInvalid
		}
		key, err := am.apiKeyValidator.GetByKey(ctx, token)
		if err != nil {
			if errors.Is(err, auth.ErrAPIKeyNotFound) {
				return ErrCurrentCredentialInvalid
			}
			return err
		}
		if key == nil || key.ID != expected.APIKeyID || key.UserID != expected.UserID {
			return ErrCurrentCredentialInvalid
		}
		if !apiKeyScopesAllow(key.Scopes, &http.Request{Method: method, URL: &url.URL{Path: path}}) {
			return ErrCurrentCredentialForbidden
		}
	} else {
		if am.tokenValidator == nil || am.sessionValidator == nil {
			return ErrCurrentCredentialInvalid
		}
		current, err := am.tokenValidator.ValidateToken(token)
		if err != nil || current == nil || current.TokenType != auth.TokenTypeAccess || current.UserID != expected.UserID || current.SessionID != expected.SessionID {
			return ErrCurrentCredentialInvalid
		}
		valid, err := am.sessionValidator.IsValid(ctx, current.SessionID)
		if err != nil {
			return err
		}
		if !valid {
			return ErrCurrentCredentialInvalid
		}
	}
	if am.apiKeyUserLoader == nil {
		return ErrCurrentCredentialInvalid
	}
	user, err := am.apiKeyUserLoader.GetByID(ctx, expected.UserID)
	if err != nil {
		if auth.IsNotFound(err) {
			return ErrCurrentCredentialInvalid
		}
		return err
	}
	if user == nil || !user.Enabled {
		return ErrCurrentCredentialInvalid
	}
	return nil
}

// ResolveCurrent repeats the owning resolver with captured request inputs.
func (m *ViewerAccessMiddleware) ResolveCurrent(ctx context.Context, input access.ResolveInput) (access.Scope, error) {
	if m == nil || m.resolver == nil {
		return access.Scope{}, ErrCurrentCredentialInvalid
	}
	return m.resolver.Resolve(ctx, input)
}
