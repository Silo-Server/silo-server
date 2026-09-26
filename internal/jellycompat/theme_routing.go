package jellycompat

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/themedelivery"
)

// compatThemeRouter routes Jellyfin theme audio with the same planner,
// secret, recipe store and routing policy as compatibility video.
func compatThemeRouter(deps Dependencies, playbackHandler *PlaybackHandler) *themedelivery.Router {
	router := &themedelivery.Router{
		Secret: func() string {
			if cfg := deps.CurrentConfig(); cfg != nil && cfg.Auth.JWTSecret != "" {
				return cfg.Auth.JWTSecret
			}
			return deps.JWTSecret
		},
		Policy: func() config.PlaybackRoutingPolicy { return playbackHandler.playbackRoutingPolicy() },
		LocalConversion: func(ctx context.Context) bool {
			registry, err := playbackHandler.localAudioTransformationRegistry(ctx)
			return err == nil && registry != nil && registry.Available(playback.TransformationAudioToAACV3)
		},
	}
	// A nil *Planner stored in the interface would read as a worker pool.
	if planner, ok := deps.NodePlanner.(*nodepool.Planner); !ok || planner != nil {
		router.Planner = noderouting.AdaptSessionPlanner(deps.NodePlanner)
	}
	if store, ok := deps.RecipeNodeStore.(themedelivery.RecipeStore); ok {
		router.Recipes = store
	}
	return router
}
