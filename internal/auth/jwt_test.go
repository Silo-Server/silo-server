package auth_test

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "super-secret-test-key-for-jwt-testing"

func newTestJWTService() *auth.JWTService {
	return auth.NewJWTService(testSecret, 15*time.Minute, 7*24*time.Hour)
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

func TestJWT_ApplePushDisplayTokenIsProfileScopedAndLongLived(t *testing.T) {
	svc := newTestJWTService()

	admin := 7
	token, expiresAt, err := svc.GenerateApplePushDisplayToken(42, "user", "sess-1", "profile-1", &admin)
	if err != nil {
		t.Fatalf("GenerateApplePushDisplayToken() error: %v", err)
	}
	claims, err := svc.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if claims.TokenType != auth.TokenTypeApplePushDisplay {
		t.Fatalf("token_type = %q, want %q", claims.TokenType, auth.TokenTypeApplePushDisplay)
	}
	if claims.UserID != 42 || claims.SessionID != "sess-1" || claims.ProfileID != "profile-1" ||
		claims.ImpersonatorUserID == nil || *claims.ImpersonatorUserID != 7 {
		t.Fatalf("claims = %+v", claims)
	}
	// Follows the refresh expiry (7d in the test service), not the 15m access expiry.
	if remaining := time.Until(expiresAt); remaining < 6*24*time.Hour {
		t.Fatalf("expiry too short: %v", remaining)
	}
	if _, _, err := svc.GenerateApplePushDisplayToken(42, "user", "", "profile-1", nil); err == nil {
		t.Fatal("expected error without session")
	}
	if _, _, err := svc.GenerateApplePushDisplayToken(42, "user", "sess-1", "", nil); err == nil {
		t.Fatal("expected error without profile")
	}
}

func TestJWT_DirectDownloadLinkTokenIsFileAndProfileScoped(t *testing.T) {
	svc := newTestJWTService()

	admin := 7
	token, expiresAt, err := svc.GenerateDirectDownloadLinkToken(42, "user", "sess-1", "profile-1", &admin, 99)
	if err != nil {
		t.Fatalf("GenerateDirectDownloadLinkToken() error: %v", err)
	}
	claims, err := svc.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if claims.TokenType != auth.TokenTypeDirectDownloadLink {
		t.Fatalf("token_type = %q, want %q", claims.TokenType, auth.TokenTypeDirectDownloadLink)
	}
	if claims.UserID != 42 || claims.SessionID != "sess-1" || claims.ProfileID != "profile-1" || claims.FileID != 99 ||
		claims.ImpersonatorUserID == nil || *claims.ImpersonatorUserID != 7 {
		t.Fatalf("claims = %+v", claims)
	}
	if exp := claims.ExpiresAt.Time; expiresAt.After(exp) || exp.Sub(expiresAt) > 2*time.Second {
		t.Fatalf("expires_at %v, token exp %v", expiresAt, exp)
	}
	if remaining := time.Until(expiresAt); remaining > auth.DirectDownloadLinkTTL || remaining < auth.DirectDownloadLinkTTL-time.Minute {
		t.Fatalf("lifetime = %v, want about %v", remaining, auth.DirectDownloadLinkTTL)
	}
	for _, tc := range []struct {
		session, profile string
		file             int
	}{{"", "profile-1", 99}, {"sess-1", "", 99}, {"sess-1", "profile-1", 0}} {
		if _, _, err := svc.GenerateDirectDownloadLinkToken(42, "user", tc.session, tc.profile, nil, tc.file); err == nil {
			t.Fatalf("expected error for %+v", tc)
		}
	}
}

// TestStoredRefreshTokenSessionID: the login session named by a stored
// refresh token is read even after the token expires; other tokens name none.
func TestStoredRefreshTokenSessionID(t *testing.T) {
	svc := newTestJWTService()
	refresh, err := svc.GenerateRefreshToken(1, "user", "sess-uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := auth.StoredRefreshTokenSessionID(refresh); err != nil || got != "sess-uuid-1" {
		t.Fatalf("refresh token = %q, %v", got, err)
	}
	expired := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{UserID: 1, SessionID: "sess-uuid-2", TokenType: auth.TokenTypeRefresh,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))}})
	expiredToken, err := expired.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := auth.StoredRefreshTokenSessionID(expiredToken); err != nil || got != "sess-uuid-2" {
		t.Fatalf("expired refresh token = %q, %v", got, err)
	}
	access, err := svc.GenerateAccessToken(1, "user", "sess-uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	sessionless, err := svc.GenerateRefreshToken(1, "user", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"access token": access, "no session": sessionless, "not a JWT": "refresh", "empty": ""} {
		if got, err := auth.StoredRefreshTokenSessionID(token); err == nil {
			t.Errorf("%s = %q, want an error", name, got)
		}
	}
}
