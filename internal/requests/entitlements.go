package requests

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/access"
)

// accessEntitlements resolves a requester's effective playback-quality ceiling
// (account + profile caps combined) via the shared access resolver.
type accessEntitlements struct {
	resolver *access.Resolver
}

// NewAccessEntitlements wraps the shared access resolver as an EntitlementResolver.
func NewAccessEntitlements(resolver *access.Resolver) EntitlementResolver {
	return accessEntitlements{resolver: resolver}
}

func (e accessEntitlements) MaxPlaybackQuality(ctx context.Context, userID int, profileID string) (string, error) {
	scope, err := e.resolveScope(ctx, userID, profileID)
	if err != nil {
		return "", err
	}
	return scope.MaxPlaybackQuality, nil
}

// MaxContentRating implements ContentRatingResolver so request discovery
// honors the profile's parental rating ceiling.
func (e accessEntitlements) MaxContentRating(ctx context.Context, userID int, profileID string) (string, error) {
	scope, err := e.resolveScope(ctx, userID, profileID)
	if err != nil {
		return "", err
	}
	return scope.MaxContentRating, nil
}

func (e accessEntitlements) resolveScope(ctx context.Context, userID int, profileID string) (access.Scope, error) {
	return e.resolver.Resolve(ctx, access.ResolveInput{
		UserID:              userID,
		ProfileID:           profileID,
		SkipPINVerification: true,
	})
}
