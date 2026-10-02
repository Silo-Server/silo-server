package watchtogether

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestRoomSocketProofRequiresSignedExpiryAndAlgorithm(t *testing.T) {
	service := NewRoomTokenService("synthetic-proof-secret", time.Minute)
	proof, _, err := service.Mint(RoomTokenClaims{RoomID: "room", UserID: 7, ProfileID: "profile"})
	if err != nil {
		t.Fatal(err)
	}
	claims, expiry, err := service.ValidateSocketProof(proof)
	if err != nil || claims.RoomID != "room" || !expiry.After(time.Now()) {
		t.Fatal("valid proof refused", err)
	}
	for _, which := range []string{"missing_expiry", "expired", "wrong_algorithm"} {
		fields := roomJWTClaims{RoomID: "room", UserID: 7, ProfileID: "profile", RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
		var method jwt.SigningMethod = jwt.SigningMethodHS256
		switch which {
		case "missing_expiry":
			fields.ExpiresAt = nil
		case "expired":
			fields.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
		case "wrong_algorithm":
			method = jwt.SigningMethodHS384
		}
		token, err := jwt.NewWithClaims(method, fields).SignedString(service.secret)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = service.ValidateSocketProof(token); err == nil {
			t.Fatal(which, "accepted")
		}
	}
}
