package playback

import (
	"context"
	"errors"
	"testing"
)

type capturedRecipeStore struct {
	card    *RecipeCard
	version string
	err     error
	deleted bool
}

func (s *capturedRecipeStore) Delete(context.Context, string) error {
	return errors.New("versioned store must not use unscoped deletion")
}

func (s *capturedRecipeStore) RecipeVersion(context.Context, string) (*RecipeCard, string, error) {
	return s.card, s.version, s.err
}

func (s *capturedRecipeStore) DeleteVersion(_ context.Context, work, version string) error {
	if work != "work" || version != "captured-publication" {
		return errors.New("cleanup lost its captured publication")
	}
	s.deleted = true
	return nil
}

func TestCapturedRecipeCleanupMatchesCanonicalAndLegacyNodeURLs(t *testing.T) {
	for _, scenario := range []struct {
		name, stored, stopped string
		match                 bool
	}{
		{"canonical-https", "https://Worker.Example:00443/", "https://worker.example", true},
		{"canonical-http", "http://Worker.Example:00080/", "http://worker.example", true},
		{"legacy-prefix", "https://worker.example/node-prefix/", "https://worker.example/node-prefix", true},
		{"other-node", "https://other.example", "https://worker.example", false},
		{"other-prefix", "https://worker.example/other-prefix", "https://worker.example/node-prefix", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store := &capturedRecipeStore{card: &RecipeCard{TranscodeNodeURL: scenario.stored}, version: "captured-publication"}
			cleanup, err := CaptureNodeRecipeDeletion(t.Context(), store, "work", scenario.stopped)
			if err != nil {
				t.Fatal(err)
			}
			// Teardown may wait while newer state appears. The cleanup must
			// retain the previously captured token rather than read it again.
			store.version = "successor-publication"
			if err := cleanup(t.Context()); err != nil {
				t.Fatal(err)
			}
			if store.deleted != scenario.match {
				t.Fatal("cleanup targeted the wrong node publication")
			}
		})
	}
}

func TestCapturedRecipeCleanupRejectsUnknownPublication(t *testing.T) {
	for _, store := range []*capturedRecipeStore{
		{card: &RecipeCard{}, err: errors.New("capture failed")},
		{card: &RecipeCard{}},
	} {
		if cleanup, err := CaptureNodeRecipeDeletion(t.Context(), store, "work", ""); err == nil || cleanup != nil || store.deleted {
			t.Fatal("failed capture allowed unscoped deletion")
		}
	}
}
