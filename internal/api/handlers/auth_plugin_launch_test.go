package handlers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// The plugin access token carries the caller's validated profile, or none
// when the launch named no profile.
func TestPluginLaunchTokenCarriesProfile(t *testing.T) {
	jwt := auth.NewJWTService("plugin-launch-test-secret", time.Minute, time.Hour)
	handler := NewAuthHandler(nil, jwt, nil)
	for _, profileID := range []string{"profile-1", ""} {
		token, err := handler.PluginLaunchToken(&auth.Claims{UserID: 7, Role: "user", SessionID: "session-1", TokenType: auth.TokenTypeAccess}, profileID)
		if err != nil {
			t.Fatalf("PluginLaunchToken(%q): %v", profileID, err)
		}
		claims, err := jwt.ValidateToken(token)
		if err != nil {
			t.Fatalf("validating plugin token: %v", err)
		}
		if claims.ProfileID != profileID || claims.TokenType != auth.TokenTypePluginAccess || claims.UserID != 7 {
			t.Fatalf("profile %q: plugin claims = %#v", profileID, claims)
		}
	}
}
