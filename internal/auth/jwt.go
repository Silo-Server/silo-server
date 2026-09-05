package auth

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Sentinel errors for JWT operations.
var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token has expired")
)

// Claims represents the custom JWT claims used for authentication.
type Claims struct {
	UserID             int    `json:"user_id"`
	Role               string `json:"role"`
	SessionID          string `json:"session_id"`
	ProfileID          string `json:"profile_id,omitempty"`
	TokenType          string `json:"token_type"`
	ImpersonatorUserID *int   `json:"impersonator_user_id,omitempty"`
	APIKeyID           int64  `json:"api_key_id,omitempty"`
	RateTier           string `json:"rate_tier,omitempty"`
	// APIKeyScopes carries the authenticating API key's scopes; empty for
	// JWT sessions and unscoped keys. Never serialized into issued JWTs —
	// it only exists on claims built for API-key requests.
	APIKeyScopes []string `json:"-"`
	jwt.RegisteredClaims
}

const (
	TokenTypeAccess       = "access"
	TokenTypeRefresh      = "refresh"
	TokenTypeAPIKey       = "api_key"
	TokenTypePluginAccess = "plugin_access"
	// TokenTypeApplePushDisplay is a long-lived, profile-scoped credential
	// issued at Apple push registration. It is only accepted by the
	// notification display endpoint the iOS Notification Service extension
	// calls, so an expired short-lived access token no longer degrades every
	// push to generic text.
	TokenTypeApplePushDisplay = "apple_push_display"
)

const PluginAccessCookieName = "silo_plugin_access"

// JWTService handles JWT token generation and validation using HMAC-SHA256.
// The expiry durations are atomics so they can be hot-reloaded from admin
// settings while tokens are being generated; the secret is fixed for the
// service lifetime (changing it would invalidate every outstanding token).
type JWTService struct {
	secret        []byte
	accessExpiry  atomic.Int64 // nanoseconds
	refreshExpiry atomic.Int64 // nanoseconds
}

// NewJWTService creates a new JWTService with the given secret and expiry durations.
func NewJWTService(secret string, accessExpiry, refreshExpiry time.Duration) *JWTService {
	j := &JWTService{secret: []byte(secret)}
	j.SetExpiries(accessExpiry, refreshExpiry)
	return j
}

// SetExpiries updates the token expiry durations. Safe for concurrent use;
// applies to tokens generated afterwards.
func (j *JWTService) SetExpiries(access, refresh time.Duration) {
	j.accessExpiry.Store(int64(access))
	j.refreshExpiry.Store(int64(refresh))
}

// AccessExpiry returns the configured access token expiry duration.
func (j *JWTService) AccessExpiry() time.Duration {
	return time.Duration(j.accessExpiry.Load())
}

// RefreshExpiry returns the configured refresh token expiry duration.
func (j *JWTService) RefreshExpiry() time.Duration {
	return time.Duration(j.refreshExpiry.Load())
}

// GenerateAccessToken creates a signed JWT access token with the configured
// access token expiry duration.
func (j *JWTService) GenerateAccessToken(userID int, role, sessionID string) (string, error) {
	return j.generateAccessToken(Claims{
		UserID:    userID,
		Role:      role,
		SessionID: sessionID,
	})
}

// GenerateRefreshToken creates a signed JWT refresh token with the configured
// refresh token expiry duration.
func (j *JWTService) GenerateRefreshToken(userID int, role, sessionID string) (string, error) {
	return j.generateRefreshToken(Claims{
		UserID:    userID,
		Role:      role,
		SessionID: sessionID,
	})
}

func (j *JWTService) GeneratePluginAccessToken(
	userID int, role, sessionID, profileID string, ttl time.Duration,
) (string, error) {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return j.generateToken(Claims{
		UserID:    userID,
		Role:      role,
		SessionID: sessionID,
		ProfileID: profileID,
	}, TokenTypePluginAccess, ttl)
}

// GenerateApplePushDisplayToken creates a signed token scoped to one
// profile that the notification display endpoint accepts. Its lifetime
// follows the refresh token: the session must still be valid at use time,
// so revoking the session revokes the display token too.
// impersonatorUserID is carried so display fetches made under an
// impersonated session stay attributed to the acting admin.
func (j *JWTService) GenerateApplePushDisplayToken(
	userID int, role, sessionID, profileID string, impersonatorUserID *int,
) (string, time.Time, error) {
	if sessionID == "" {
		return "", time.Time{}, fmt.Errorf("%w: session is required", ErrInvalidToken)
	}
	if profileID == "" {
		return "", time.Time{}, fmt.Errorf("%w: profile is required", ErrInvalidToken)
	}
	ttl := j.RefreshExpiry()
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	token, err := j.generateToken(Claims{
		UserID:             userID,
		Role:               role,
		SessionID:          sessionID,
		ProfileID:          profileID,
		ImpersonatorUserID: impersonatorUserID,
	}, TokenTypeApplePushDisplay, ttl)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, time.Now().Add(ttl), nil
}

func (j *JWTService) generateAccessToken(claims Claims) (string, error) {
	return j.generateToken(claims, TokenTypeAccess, j.AccessExpiry())
}

func (j *JWTService) generateRefreshToken(claims Claims) (string, error) {
	return j.generateToken(claims, TokenTypeRefresh, j.RefreshExpiry())
}

// generateToken creates a signed JWT with the given claims and expiry duration.
func (j *JWTService) generateToken(claims Claims, tokenType string, expiry time.Duration) (string, error) {
	now := time.Now()
	claims.TokenType = tokenType
	claims.RegisteredClaims = jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
		IssuedAt:  jwt.NewNumericDate(now),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, &claims)
	signedToken, err := token.SignedString(j.secret)
	if err != nil {
		return "", fmt.Errorf("signing token: %w", err)
	}

	return signedToken, nil
}

// ValidateToken parses and validates a JWT token string. It verifies the
// signature, expiry, and signing method (HMAC-SHA256). Returns the parsed
// claims on success.
func (j *JWTService) ValidateToken(tokenStr string) (*Claims, error) {
	if tokenStr == "" {
		return nil, ErrInvalidToken
	}

	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (any, error) {
		// Reject any signing method other than HMAC.
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return j.secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, fmt.Errorf("%w: %w", ErrExpiredToken, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	if !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}
