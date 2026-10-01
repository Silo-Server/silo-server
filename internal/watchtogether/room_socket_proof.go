package watchtogether

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const RoomSocketProtocol = "silo.room.v2"

// ValidateSocketProof requires the server's current signing algorithm and a
// signed expiry, without changing frozen v1 room-token validation.
func (s *RoomTokenService) ValidateSocketProof(token string) (*RoomTokenClaims, time.Time, error) {
	if s == nil || token == "" {
		return nil, time.Time{}, ErrInvalidRoomToken
	}
	claims := new(roomJWTClaims)
	parsed, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return s.secret, nil }, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid || claims.ExpiresAt == nil {
		return nil, time.Time{}, ErrInvalidRoomToken
	}
	return &RoomTokenClaims{RoomID: claims.RoomID, UserID: claims.UserID, ProfileID: claims.ProfileID}, claims.ExpiresAt.Time, nil
}
