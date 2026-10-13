package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// The planner's leading-picture drop reaches a proxy or transcode node only
// through the identity recipe card that signs the remux stream token.
func TestIdentityRecipeCardCarriesRemuxLeadingPictureDrop(t *testing.T) {
	session := &playback.Session{ID: "session-1", UserID: 42, ProfileID: "profile-1", MediaFileID: 77, PlayMethod: playback.PlayRemux, RemuxResumeLeadingPictureDrop: true}
	if claims := identityRecipeCard(session).ToClaims(); !claims.RemuxResumeLeadingPictureDrop {
		t.Fatal("remux stream claims lost the leading-picture drop")
	}
	session.RemuxResumeLeadingPictureDrop = false
	if claims := identityRecipeCard(session).ToClaims(); claims.RemuxResumeLeadingPictureDrop {
		t.Fatal("remux stream claims requested a leading-picture drop the session did not freeze")
	}
}
