package jellycompat

import (
	"context"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// ScopeResolver resolves a viewer request into an effective access scope.
// Implemented by *access.Resolver.
type ScopeResolver interface {
	Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error)
}

// NewScopeAccessFilter returns an AccessFilterResolver backed by the shared
// access scope resolver, so account-level library restrictions
// (users.library_ids), profile library restrictions, user-disabled
// libraries, content-rating ceilings, and playback-quality ceilings all
// apply to the Jellyfin-compat API exactly as they do to the native API.
//
// PIN verification is skipped because compat login already verified the
// profile PIN (password#pin convention). Resolution failures fail closed:
// the viewer gets an empty library allowlist rather than full access.
func NewScopeAccessFilter(resolver ScopeResolver) AccessFilterResolver {
	return func(ctx context.Context, userID int, profileID string) catalog.AccessFilter {
		scope, err := resolver.Resolve(ctx, access.ResolveInput{
			UserID:              userID,
			ProfileID:           profileID,
			SkipPINVerification: true,
		})
		if err != nil {
			slog.Warn("jellycompat: access scope resolution failed; denying library access",
				"user_id", userID,
				"profile_id", profileID,
				"error", err,
			)
			return catalog.AccessFilter{
				AllowedLibraryIDs: []int{},
				UserID:            userID,
				ProfileID:         profileID,
			}
		}
		return catalog.AccessFilter{
			AllowedLibraryIDs:  scope.AllowedLibraryIDs,
			DisabledLibraryIDs: scope.DisabledLibraryIDs,
			MaxContentRating:   scope.MaxContentRating,
			MaxPlaybackQuality: scope.MaxPlaybackQuality,
			UserID:             userID,
			ProfileID:          profileID,
		}
	}
}
