package workercontrolcore

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// VersionedRecipeStore fences destructive cleanup across API replicas and
// node moves. Payload equality cannot distinguish identical replacements.
type VersionedRecipeStore interface {
	RecipeVersion(context.Context, string) (*playback.RecipeCard, string, error)
	DeleteVersion(context.Context, string, string) error
}

func (s *Service) captureRecipeDeletion(ctx context.Context, node Node, work string) (func(context.Context) error, error) {
	if s.options.RecipeStore == nil {
		return nil, nil
	}
	store, ok := s.options.RecipeStore.(VersionedRecipeStore)
	if !ok {
		return nil, ErrUnavailable
	}
	card, version, err := store.RecipeVersion(ctx, work)
	if err != nil {
		return nil, err
	}
	if card == nil {
		return nil, nil
	}
	if version == "" {
		return nil, ErrUnavailable
	}
	if !recipeNode(*card, node) {
		return nil, nil
	}
	return func(ctx context.Context) error { return store.DeleteVersion(ctx, work, version) }, nil
}
