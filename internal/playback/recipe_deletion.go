package playback

import (
	"context"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

type RecipeDeleter interface {
	Delete(context.Context, string) error
}

type RecipePublicationStore interface {
	RecipeVersion(context.Context, string) (*RecipeCard, string, error)
	DeleteVersion(context.Context, string, string) error
}

// CaptureNodeRecipeDeletion runs before teardown waits. Current stores fence
// cleanup to a unique publication; legacy adapters retain their Delete callback.
func CaptureNodeRecipeDeletion(ctx context.Context, store RecipeDeleter, work, nodeURL string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if store == nil || work == "" {
		return noop, nil
	}
	versioned, ok := store.(RecipePublicationStore)
	if !ok {
		return func(ctx context.Context) error { return store.Delete(ctx, work) }, nil
	}
	card, version, err := versioned.RecipeVersion(ctx, work)
	if err != nil {
		return nil, err
	}
	if card == nil {
		return noop, nil
	}
	if version == "" {
		return nil, errors.New("node recipe publication unavailable")
	}
	if nodeURL != "" && !workeridentity.SameOrigin(card.TranscodeNodeURL, nodeURL, true) && strings.TrimRight(card.TranscodeNodeURL, "/") != strings.TrimRight(nodeURL, "/") {
		return noop, nil
	}
	return func(ctx context.Context) error { return versioned.DeleteVersion(ctx, work, version) }, nil
}
