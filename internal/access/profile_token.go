package access

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ProfileTokenService mints and validates profile verification tokens.
type ProfileTokenService struct {
	secret []byte
	ttl    time.Duration
}

type profileJWTClaims struct {
	UserID    int    `json:"user_id"`
	SessionID string `json:"session_id"`
	ProfileID string `json:"profile_id"`
	// PINRevision is a pointer so a token minted before the claim existed
	// reads as absent rather than as revision 0, and is refused.
	PINRevision    *int64 `json:"pin_revision,omitempty"`
	PolicyRevision int64  `json:"policy_revision"`
	jwt.RegisteredClaims
}

// NewProfileTokenService creates a profile token service.
func NewProfileTokenService(secret string, ttl time.Duration) *ProfileTokenService {
	return &ProfileTokenService{secret: []byte(secret), ttl: ttl}
}

// Mint creates a signed profile token. When ttl is zero or negative, the token
// is durable and is invalidated by the session, profile and PIN-revision checks
// instead.
func (s *ProfileTokenService) Mint(claims ProfileTokenClaims) (string, time.Time, error) {
	now := time.Now().UTC()
	var expiresAt time.Time
	registeredClaims := jwt.RegisteredClaims{
		IssuedAt: jwt.NewNumericDate(now),
	}
	if s.ttl > 0 {
		expiresAt = now.Add(s.ttl)
		registeredClaims.ExpiresAt = jwt.NewNumericDate(expiresAt)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, profileJWTClaims{
		UserID:           claims.UserID,
		SessionID:        claims.SessionID,
		ProfileID:        claims.ProfileID,
		PINRevision:      &claims.PINRevision,
		PolicyRevision:   claims.PolicyRevision,
		RegisteredClaims: registeredClaims,
	})
	signed, err := token.SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("signing profile token: %w", err)
	}
	return signed, expiresAt, nil
}

// Validate parses and validates a profile token.
func (s *ProfileTokenService) Validate(tokenStr string) (*ProfileTokenClaims, error) {
	if tokenStr == "" {
		return nil, ErrProfileUnverified
	}
	claims := &profileJWTClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, fmt.Errorf("%w: expired", ErrProfileUnverified)
		}
		return nil, fmt.Errorf("%w: %v", ErrProfileUnverified, err)
	}
	if !token.Valid {
		return nil, ErrProfileUnverified
	}
	if claims.PINRevision == nil {
		// Minted before tokens were bound to the profile's PIN revision. Its
		// policy_revision cannot show whether the PIN changed since, so it
		// proves nothing now; the client enters the PIN once more.
		return nil, fmt.Errorf("%w: token predates pin_revision", ErrProfileUnverified)
	}
	return &ProfileTokenClaims{
		UserID:         claims.UserID,
		SessionID:      claims.SessionID,
		ProfileID:      claims.ProfileID,
		PINRevision:    *claims.PINRevision,
		PolicyRevision: claims.PolicyRevision,
	}, nil
}
