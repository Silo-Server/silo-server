package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type countingRecipePublicationStore struct {
	recipeCardStoreV3
	captures int
}

func (s *countingRecipePublicationStore) RecipeVersion(context.Context, string) (*playback.RecipeCard, string, error) {
	s.captures++
	return nil, "", nil
}

func (s *countingRecipePublicationStore) DeleteVersion(context.Context, string, string) error {
	return nil
}

func TestPredecessorRecipeCaptureSkipsLocalTransports(t *testing.T) {
	store := &countingRecipePublicationStore{}
	h := &PlaybackHandler{NodeRecipeStore: store}
	h.capturePredecessorRecipeDeletionV3(t.Context(), "session-1", "")()
	if store.captures != 0 {
		t.Fatalf("local predecessor captured %d recipe publications", store.captures)
	}
	h.capturePredecessorRecipeDeletionV3(t.Context(), "session-1", "http://node.example.test")()
	if store.captures != 1 {
		t.Fatalf("remote predecessor captured %d recipe publications, want 1", store.captures)
	}
}
