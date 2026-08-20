package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ABSAccessResolver adapts silo's native profile/library access resolver to
// the ABS compatibility layer. ABS has already authenticated the account with
// a password or refresh token, so profile PIN verification is skipped here.
type ABSAccessResolver struct {
	resolver scopeResolver
	users    access.UserRepository
	stores   userstore.UserStoreProvider
	groups   access.GroupPolicyProvider
}

type scopeResolver interface {
	Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error)
}

// NewABSAccessResolver creates a resolver for ABS-authenticated access checks.
func NewABSAccessResolver(
	users access.UserRepository,
	stores userstore.UserStoreProvider,
	resolver scopeResolver,
	groups ...access.GroupPolicyProvider,
) *ABSAccessResolver {
	var groupProvider access.GroupPolicyProvider
	if len(groups) > 0 {
		groupProvider = groups[0]
	}
	if resolver != nil {
		return &ABSAccessResolver{resolver: resolver, users: users, stores: stores, groups: groupProvider}
	}
	if users == nil || stores == nil {
		return nil
	}
	// Legacy resolver: proxy/test wiring without a policy system. Production integrated/api modes always take the policy path. Removed with the legacy cleanup phase.
	return &ABSAccessResolver{
		resolver: access.NewResolver(users, stores, nil, groupProvider),
		users:    users,
		stores:   stores,
		groups:   groupProvider,
	}
}

func (r *ABSAccessResolver) ResolveABSAccess(ctx context.Context, userID, profileID string) (catalog.AccessFilter, error) {
	if r == nil || r.resolver == nil {
		return catalog.AccessFilter{}, nil
	}
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return catalog.AccessFilter{}, fmt.Errorf("%w: invalid ABS user id %q", abs.ErrAccessDenied, userID)
	}
	// Authentication lets ABS skip a second PIN challenge, but it does not
	// erase the selected household profile's policy scope. Primary profiles are
	// household parents, not account-wide bypasses.
	scope, err := r.resolver.Resolve(ctx, access.ResolveInput{
		UserID:              uid,
		ProfileID:           profileID,
		SkipPINVerification: true,
	})
	if err != nil {
		if errors.Is(err, access.ErrProfileNotFound) || errors.Is(err, access.ErrProfileUnverified) {
			return catalog.AccessFilter{}, fmt.Errorf("%w: %v", abs.ErrAccessDenied, err)
		}
		return catalog.AccessFilter{}, err
	}
	return catalog.AccessFilter{
		AllowedLibraryIDs:  scope.AllowedLibraryIDs,
		DisabledLibraryIDs: scope.DisabledLibraryIDs,
		MaxContentRating:   scope.MaxContentRating,
		MaxPlaybackQuality: scope.MaxPlaybackQuality,
		UserID:             scope.UserID,
		ProfileID:          scope.ProfileID,
	}, nil
}

// CanCurateMetadata applies the same permission model as Silo's native
// metadata mutation gate. Explicit metadata_curation survives only when the
// user's access group permits it. An admin role grants the capability only
// while acting without a profile or through the household's primary profile.
func (r *ABSAccessResolver) CanCurateMetadata(ctx context.Context, userID, profileID string) (bool, error) {
	if r == nil || r.users == nil || r.stores == nil {
		return false, fmt.Errorf("ABS metadata authorizer is not configured")
	}
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return false, fmt.Errorf("%w: invalid ABS user id %q", abs.ErrAccessDenied, userID)
	}
	user, err := r.users.GetByID(ctx, uid)
	if err != nil {
		return false, fmt.Errorf("load ABS user %d: %w", uid, err)
	}
	if user == nil || !user.Enabled {
		return false, nil
	}
	effective, err := access.EffectivePolicyForUser(ctx, user, r.groups)
	if err != nil {
		return false, fmt.Errorf("load ABS access group policy for user %d: %w", uid, err)
	}
	for _, permission := range effective.Permissions {
		if permission == string(auth.PermissionMetadataCuration) {
			return true, nil
		}
	}
	if user.Role != "admin" {
		return false, nil
	}
	if profileID == "" {
		return true, nil
	}
	store, err := r.stores.ForUser(ctx, uid)
	if err != nil {
		return false, fmt.Errorf("open ABS user store for %d: %w", uid, err)
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return false, fmt.Errorf("load ABS profile %q: %w", profileID, err)
	}
	return profile != nil && profile.IsPrimary, nil
}
