package jellycompat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type uncapturableRecipeNodeStore struct {
	*stubRecipeNodeStore
}

var errRecipeCapture = errors.New("recipe version unavailable")

func (uncapturableRecipeNodeStore) RecipeVersion(context.Context, string) (*playback.RecipeCard, string, error) {
	return nil, "", errRecipeCapture
}

func (uncapturableRecipeNodeStore) DeleteVersion(context.Context, string, string) error {
	return nil
}

func TestPersistRemoteTranscodeCompensatesFailedRecipeCapture(t *testing.T) {
	const oldNode, newNode = "http://old-node.example.test", "http://new-node.example.test"
	for _, replacing := range []bool{true, false} {
		t.Run(map[bool]string{true: "restores replaced recipe", false: "removes candidate"}[replacing], func(t *testing.T) {
			playbackStore := NewPlaybackSessionStore(time.Hour, nil)
			previous := PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"}
			recipes := &stubRecipeNodeStore{}
			if replacing {
				oldRecipe := playback.NewRecipeCard(7, "profile-1", 42, oldNode, playback.TranscodeOpts{SessionID: "upstream-1", InputPath: "/media/movie.mkv"})
				previous.TranscodeStarted, previous.Recipe = true, &oldRecipe
				recipes.cards = map[string]playback.RecipeCard{"upstream-1": oldRecipe}
			}
			playbackStore.Put(previous)
			handler := &PlaybackHandler{
				playbackStore: playbackStore,
				sessionMgr: &lockedCompatSessionManager{session: playback.Session{
					ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: 42, TranscodeNodeURL: newNode,
					PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode,
				}},
				tm:              playback.NewTranscodeManager(),
				JWTSecret:       "test-secret",
				RecipeNodeStore: uncapturableRecipeNodeStore{recipes},
			}

			err := handler.persistTranscodeRecipe(t.Context(), "play-1", "upstream-1", playback.TranscodeOpts{SessionID: "upstream-1", InputPath: "/media/movie.mkv"})
			if !errors.Is(err, errRecipeCapture) {
				t.Fatalf("persist error = %v, want capture failure", err)
			}
			card, found := recipes.Get("upstream-1")
			if replacing && (!found || card.TranscodeNodeURL != oldNode) {
				t.Fatalf("node recipe = %+v, found=%v; want the replaced recipe restored", card, found)
			}
			if !replacing && found {
				t.Fatalf("failed start left node recipe %+v", card)
			}
		})
	}
}
