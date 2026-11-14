package handlers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// The plugin access token carries the caller's validated profile, or none
// when the launch named no profile.
func TestPluginLaunchTokenCarriesProfile(t *testing.T) {
	jwt := auth.NewJWTService("plugin-launch-test-secret", time.Minute, time.Hour)
	handler := NewAuthHandler(nil, jwt, nil)
	for _, profileID := range []string{"profile-1", ""} {
		// A verified profile's PIN revision is bound, so renewal can tell
		// when the PIN changes.
		ctx := access.SetScope(t.Context(), access.Scope{UserID: 7, ProfileID: "profile-1", ProfileVerified: true, ProfilePINRevision: 3})
		token, err := handler.PluginLaunchToken(ctx, &auth.Claims{UserID: 7, Role: "user", SessionID: "session-1", TokenType: auth.TokenTypeAccess}, profileID)
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
		if bound := claims.ProfilePINRevision != nil; bound != (profileID != "") || (bound && *claims.ProfilePINRevision != 3) {
			t.Fatalf("profile %q: profile_pin_revision = %v", profileID, claims.ProfilePINRevision)
		}
	}
}
