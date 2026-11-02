package watchtogether

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidRoomToken = errors.New("invalid watch together room token")

type RoomTokenClaims struct {
	RoomID    string
	UserID    int
	ProfileID string
}

type roomJWTClaims struct {
	RoomID    string `json:"room_id"`
	UserID    int    `json:"user_id"`
	ProfileID string `json:"profile_id"`
	jwt.RegisteredClaims
}

type RoomTokenService struct {
	secret []byte
	ttl    time.Duration
}

func NewRoomTokenService(secret string, ttl time.Duration) *RoomTokenService {
	return &RoomTokenService{secret: []byte(secret), ttl: ttl}
}

func (s *RoomTokenService) Mint(claims RoomTokenClaims) (string, time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(s.ttl)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, roomJWTClaims{
		RoomID:    claims.RoomID,
		UserID:    claims.UserID,
		ProfileID: claims.ProfileID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	})

	signed, err := token.SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("signing watch together room token: %w", err)
	}
	return signed, expiresAt, nil
}

func (s *RoomTokenService) Validate(tokenStr string) (*RoomTokenClaims, error) {
	if tokenStr == "" {
		return nil, ErrInvalidRoomToken
	}

	claims := &roomJWTClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, fmt.Errorf("%w: expired", ErrInvalidRoomToken)
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidRoomToken, err) //nolint:errorlint // the sentinel is the verdict on a client token; the jwt parse error is only reported
	}
	if !token.Valid {
		return nil, ErrInvalidRoomToken
	}

	return &RoomTokenClaims{
		RoomID:    claims.RoomID,
		UserID:    claims.UserID,
		ProfileID: claims.ProfileID,
	}, nil
}
