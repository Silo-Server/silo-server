package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A proxy or transcode node learns which codec a remux copies only from the
// stream token. A transcoded track must not claim one: its planned "aac" is
// the encoder output, not ADTS from the source.
func TestIdentityRecipeCardCarriesCopiedAudioCodec(t *testing.T) {
	session := &playback.Session{ID: "session-1", UserID: 42, ProfileID: "profile-1", MediaFileID: 77, PlayMethod: playback.PlayRemux, TargetAudioCodec: "aac"}
	claims := identityRecipeCard(session).ToClaims()
	if claims.CopyAudioCodec != "aac" {
		t.Fatalf("copied remux claims CopyAudioCodec = %q, want aac", claims.CopyAudioCodec)
	}
	if card := playback.RecipeCardFromClaims(&claims); card.CopyAudioCodec != "aac" {
		t.Fatalf("recipe rebuilt from claims lost CopyAudioCodec: %q", card.CopyAudioCodec)
	}
	session.TranscodeAudio = true
	if claims := identityRecipeCard(session).ToClaims(); claims.CopyAudioCodec != "" {
		t.Fatalf("transcoded remux claims CopyAudioCodec = %q, want empty", claims.CopyAudioCodec)
	}
}
