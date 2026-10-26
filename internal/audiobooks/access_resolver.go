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
// the ABS compatibility layer. The native profile-token check is skipped: an
// ABS sign-in checks the profile's PIN itself, and abs.ProfileVerifier refuses
// the token on every request once that PIN changes.
type ABSAccessResolver struct {
	resolver scopeResolver
	users    access.UserRepository
	stores   userstore.UserStoreProvider
}

type scopeResolver interface {
	Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error)
}

var _ abs.AccessResolver = (*ABSAccessResolver)(nil)

// NewABSAccessResolver creates a resolver for ABS-authenticated access checks.
func NewABSAccessResolver(
	users access.UserRepository,
	stores userstore.UserStoreProvider,
	resolver scopeResolver,
	groups ...access.GroupPolicyProvider,
) *ABSAccessResolver {
	if resolver != nil {
		return &ABSAccessResolver{resolver: resolver, users: users, stores: stores}
	}
	if users == nil || stores == nil {
		return nil
	}
	// Legacy resolver: proxy/test wiring without a policy system. Production integrated/api modes always take the policy path. Removed with the legacy cleanup phase.
	return &ABSAccessResolver{resolver: access.NewResolver(users, stores, nil, groups...), users: users, stores: stores}
}

func (r *ABSAccessResolver) ResolveABSAccess(ctx context.Context, userID, profileID string) (catalog.AccessFilter, error) {
	if r == nil || r.resolver == nil {
		return catalog.AccessFilter{}, nil
	}
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return catalog.AccessFilter{}, fmt.Errorf("invalid ABS user id %q: %w", userID, err)
	}
	scope, err := r.resolver.Resolve(ctx, access.ResolveInput{
		UserID:              uid,
		ProfileID:           profileID,
		SkipPINVerification: true,
	})
	if err != nil {
		return catalog.AccessFilter{}, err
	}
	return catalog.AccessFilter{
		AllowedLibraryIDs:  scope.AllowedLibraryIDs,
		DisabledLibraryIDs: scope.DisabledLibraryIDs,
		MaturityLimits:     scope.MaturityLimits,
		MaxPlaybackQuality: scope.MaxPlaybackQuality,
		UserID:             scope.UserID,
		ProfileID:          scope.ProfileID,
	}, nil
}

// ResolveABSOwnerAccess resolves the current access of a stored ABS principal
// that is not the caller, such as the owner of a public RSS feed. Unlike
// ResolveABSAccess it fails closed: it returns abs.ErrOwnerUnavailable when the
// account is missing or disabled, when the profile no longer exists or policy
// refuses it, or when a principal without a profile belongs to an account
// that now has profiles (the rule SiloCredValidator.VerifyTokenProfile applies
// to tokens).
func (r *ABSAccessResolver) ResolveABSOwnerAccess(ctx context.Context, userID, profileID string) (catalog.AccessFilter, error) {
	if r == nil || r.resolver == nil || r.users == nil || r.stores == nil {
		return catalog.AccessFilter{}, errors.New("ABS owner access resolver not configured")
	}
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return catalog.AccessFilter{}, abs.ErrOwnerUnavailable
	}
	user, err := r.users.GetByID(ctx, uid)
	if errors.Is(err, auth.ErrNotFound) {
		return catalog.AccessFilter{}, abs.ErrOwnerUnavailable
	}
	if err != nil {
		return catalog.AccessFilter{}, fmt.Errorf("load ABS owner %d: %w", uid, err)
	}
	if user == nil || !user.Enabled {
		return catalog.AccessFilter{}, abs.ErrOwnerUnavailable
	}
	if profileID == "" {
		store, err := r.stores.ForUser(ctx, uid)
		if err != nil {
			return catalog.AccessFilter{}, fmt.Errorf("open user store: %w", err)
		}
		profiles, err := store.ListProfiles(ctx)
		if err != nil {
			return catalog.AccessFilter{}, fmt.Errorf("list profiles: %w", err)
		}
		if len(profiles) > 0 {
			return catalog.AccessFilter{}, abs.ErrOwnerUnavailable
		}
	}
	filter, err := r.ResolveABSAccess(ctx, userID, profileID)
	if errors.Is(err, access.ErrProfileNotFound) || errors.Is(err, access.ErrProfileUnverified) {
		return catalog.AccessFilter{}, abs.ErrOwnerUnavailable
	}
	return filter, err
}
