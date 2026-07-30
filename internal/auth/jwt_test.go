package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "super-secret-test-key-for-jwt-testing"

func newTestJWTService() *auth.JWTService {
	return auth.NewJWTService(testSecret, 15*time.Minute, 7*24*time.Hour)
}

func TestJWT_GenerateAccessToken(t *testing.T) {
	svc := newTestJWTService()

	token, err := svc.GenerateAccessToken(42, "admin", "sess-abc-123")
	if err != nil {
		t.Fatalf("GenerateAccessToken() error: %v", err)
	}
	if token == "" {
		t.Fatal("GenerateAccessToken() returned empty token")
	}

	// Token should have three dot-separated parts (header.payload.signature).
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Errorf("expected 3 JWT parts, got %d", len(parts))
	}
}

func TestJWT_GenerateRefreshToken(t *testing.T) {
	svc := newTestJWTService()

	token, err := svc.GenerateRefreshToken(42, "user", "sess-def-456")
	if err != nil {
		t.Fatalf("GenerateRefreshToken() error: %v", err)
	}
	if token == "" {
		t.Fatal("GenerateRefreshToken() returned empty token")
	}
}

func TestJWT_ValidateAccessToken(t *testing.T) {
	svc := newTestJWTService()

	token, err := svc.GenerateAccessToken(1, "user", "sess-uuid-1")
	if err != nil {
		t.Fatalf("GenerateAccessToken() error: %v", err)
	}

	claims, err := svc.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}

	if claims.UserID != 1 {
		t.Errorf("UserID = %d, want 1", claims.UserID)
	}
	if claims.Role != "user" {
		t.Errorf("Role = %q, want %q", claims.Role, "user")
	}
	if claims.SessionID != "sess-uuid-1" {
		t.Errorf("SessionID = %q, want %q", claims.SessionID, "sess-uuid-1")
	}
	if claims.TokenType != auth.TokenTypeAccess {
		t.Errorf("TokenType = %q, want %q", claims.TokenType, auth.TokenTypeAccess)
	}

	// ExpiresAt should be set and in the future.
	if claims.ExpiresAt == nil {
		t.Fatal("ExpiresAt should be set")
	}
	if !claims.ExpiresAt.Time.After(time.Now()) {
		t.Error("ExpiresAt should be in the future")
	}

	// IssuedAt should be set and in the past (or equal to now).
	if claims.IssuedAt == nil {
		t.Fatal("IssuedAt should be set")
	}
	if claims.IssuedAt.Time.After(time.Now().Add(1 * time.Second)) {
		t.Error("IssuedAt should not be in the future")
	}
}

func TestJWT_ValidateRefreshToken(t *testing.T) {
	svc := newTestJWTService()

	token, err := svc.GenerateRefreshToken(99, "admin", "sess-uuid-2")
	if err != nil {
		t.Fatalf("GenerateRefreshToken() error: %v", err)
	}

	claims, err := svc.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}

	if claims.UserID != 99 {
		t.Errorf("UserID = %d, want 99", claims.UserID)
	}
	if claims.Role != "admin" {
		t.Errorf("Role = %q, want %q", claims.Role, "admin")
	}
	if claims.SessionID != "sess-uuid-2" {
		t.Errorf("SessionID = %q, want %q", claims.SessionID, "sess-uuid-2")
	}
	if claims.TokenType != auth.TokenTypeRefresh {
		t.Errorf("TokenType = %q, want %q", claims.TokenType, auth.TokenTypeRefresh)
	}
}

func TestJWT_PluginAccessTokenCarriesProfile(t *testing.T) {
	svc := newTestJWTService()
	token, err := svc.GeneratePluginAccessToken(42, "user", "sess-plugin", "profile-7", time.Minute)
	if err != nil {
		t.Fatalf("GeneratePluginAccessToken: %v", err)
	}
	claims, err := svc.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.TokenType != auth.TokenTypePluginAccess || claims.ProfileID != "profile-7" {
		t.Fatalf("plugin claims = %#v", claims)
	}
}

func TestJWT_AccessTokenExpiry(t *testing.T) {
	svc := newTestJWTService()

	accessToken, err := svc.GenerateAccessToken(1, "user", "sess-1")
	if err != nil {
		t.Fatalf("GenerateAccessToken() error: %v", err)
	}

	refreshToken, err := svc.GenerateRefreshToken(1, "user", "sess-1")
	if err != nil {
		t.Fatalf("GenerateRefreshToken() error: %v", err)
	}

	accessClaims, err := svc.ValidateToken(accessToken)
	if err != nil {
		t.Fatalf("ValidateToken(access) error: %v", err)
	}
	refreshClaims, err := svc.ValidateToken(refreshToken)
	if err != nil {
		t.Fatalf("ValidateToken(refresh) error: %v", err)
	}

	accessExpiry := accessClaims.ExpiresAt.Time.Sub(accessClaims.IssuedAt.Time)
	refreshExpiry := refreshClaims.ExpiresAt.Time.Sub(refreshClaims.IssuedAt.Time)

	// Access token should have shorter expiry than refresh token.
	if accessExpiry >= refreshExpiry {
		t.Errorf("access expiry (%v) should be shorter than refresh expiry (%v)", accessExpiry, refreshExpiry)
	}

	// Verify the access token expiry is approximately 15 minutes.
	expectedAccess := 15 * time.Minute
	if accessExpiry < expectedAccess-time.Second || accessExpiry > expectedAccess+time.Second {
		t.Errorf("access token expiry = %v, want ~%v", accessExpiry, expectedAccess)
	}

	// Verify the refresh token expiry is approximately 7 days.
	expectedRefresh := 7 * 24 * time.Hour
	if refreshExpiry < expectedRefresh-time.Second || refreshExpiry > expectedRefresh+time.Second {
		t.Errorf("refresh token expiry = %v, want ~%v", refreshExpiry, expectedRefresh)
	}
}

func TestJWT_ExpiredToken(t *testing.T) {
	// Create a service with a negative expiry so tokens are immediately expired.
	svc := auth.NewJWTService(testSecret, -1*time.Second, -1*time.Second)

	token, err := svc.GenerateAccessToken(1, "user", "sess-expired")
	if err != nil {
		t.Fatalf("GenerateAccessToken() error: %v", err)
	}

	_, err = svc.ValidateToken(token)
	if err == nil {
		t.Fatal("ValidateToken() should return error for expired token")
	}
}

func TestJWT_TamperedToken(t *testing.T) {
	svc := newTestJWTService()

	token, err := svc.GenerateAccessToken(1, "user", "sess-tamper")
	if err != nil {
		t.Fatalf("GenerateAccessToken() error: %v", err)
	}

	// Tamper with the token by flipping a bit in the middle of the signature.
	//
	// Not the last character: an HMAC-SHA256 signature is 32 bytes, so its
	// base64url encoding is 43 characters and the last one carries only four
	// significant bits. U, V, W and X all decode to the same trailing byte, so
	// overwriting the last character with "X" left roughly one token in
	// sixteen byte-identical and validly signed — a real 6% flake, measured
	// over 50k distinct signatures.
	middle := len(token) - 20
	flipped := byte('A')
	if token[middle] == flipped {
		flipped = 'B'
	}
	tampered := token[:middle] + string(flipped) + token[middle+1:]

	_, err = svc.ValidateToken(tampered)
	if err == nil {
		t.Fatal("ValidateToken() should return error for tampered token")
	}
}

func TestJWT_WrongSecret(t *testing.T) {
	svc1 := auth.NewJWTService("secret-one", 15*time.Minute, 7*24*time.Hour)
	svc2 := auth.NewJWTService("secret-two", 15*time.Minute, 7*24*time.Hour)

	token, err := svc1.GenerateAccessToken(1, "user", "sess-wrong-secret")
	if err != nil {
		t.Fatalf("GenerateAccessToken() error: %v", err)
	}

	_, err = svc2.ValidateToken(token)
	if err == nil {
		t.Fatal("ValidateToken() should return error for token signed with different secret")
	}
}

func TestJWT_WrongSigningMethod(t *testing.T) {
	// Create a token using RSA-style "none" algorithm trick.
	// We construct a token with alg=none to ensure the validator rejects it.
	claims := jwt.MapClaims{
		"user_id":    1,
		"role":       "admin",
		"session_id": "sess-none",
		"token_type": auth.TokenTypeAccess,
		"exp":        time.Now().Add(1 * time.Hour).Unix(),
		"iat":        time.Now().Unix(),
	}
	unsignedToken := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	tokenStr, err := unsignedToken.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("creating unsigned token: %v", err)
	}

	svc := newTestJWTService()
	_, err = svc.ValidateToken(tokenStr)
	if err == nil {
		t.Fatal("ValidateToken() should reject token with 'none' signing method")
	}
}

func TestJWT_EmptyToken(t *testing.T) {
	svc := newTestJWTService()

	_, err := svc.ValidateToken("")
	if err == nil {
		t.Fatal("ValidateToken() should return error for empty token")
	}
}

func TestJWT_GarbageToken(t *testing.T) {
	svc := newTestJWTService()

	_, err := svc.ValidateToken("not.a.valid.jwt.token")
	if err == nil {
		t.Fatal("ValidateToken() should return error for garbage token")
	}
}

func TestJWT_DifferentUsersGetDifferentTokens(t *testing.T) {
	svc := newTestJWTService()

	token1, err := svc.GenerateAccessToken(1, "user", "sess-1")
	if err != nil {
		t.Fatalf("GenerateAccessToken(1) error: %v", err)
	}

	token2, err := svc.GenerateAccessToken(2, "admin", "sess-2")
	if err != nil {
		t.Fatalf("GenerateAccessToken(2) error: %v", err)
	}

	if token1 == token2 {
		t.Error("tokens for different users should be different")
	}
}
