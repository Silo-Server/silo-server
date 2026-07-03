package watchtogether

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

const fallbackMemberName = "Guest"

// ProfileNameResolver resolves a profile's display name for room member lists.
type ProfileNameResolver interface {
	ProfileDisplayName(ctx context.Context, userID int, profileID string) string
}

type userStoreProfileNames struct {
	provider userstore.UserStoreProvider
}

// NewProfileNameResolver adapts a UserStoreProvider into a ProfileNameResolver.
func NewProfileNameResolver(provider userstore.UserStoreProvider) ProfileNameResolver {
	if provider == nil {
		return nil
	}
	return &userStoreProfileNames{provider: provider}
}

func (r *userStoreProfileNames) ProfileDisplayName(ctx context.Context, userID int, profileID string) string {
	if r == nil || r.provider == nil {
		return fallbackMemberName
	}
	store, err := r.provider.ForUser(ctx, userID)
	if err != nil || store == nil {
		return fallbackMemberName
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil || profile == nil || strings.TrimSpace(profile.Name) == "" {
		return fallbackMemberName
	}
	return profile.Name
}
