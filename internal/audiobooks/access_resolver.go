package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/policy"
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
	// permissions decides permission gates (metadata curation today). It is
	// the same PDP the native route gates use, so custom policy overrides and
	// decision logging cover the ABS surface too. Nil fails those gates
	// closed — see CanCurateMetadata.
	permissions policy.PermissionDecider
}

// SetPermissionDecider wires the policy PDP used for ABS permission gates.
// Wiring it is mandatory for metadata curation over ABS; the gate fails closed
// without it rather than falling back to a check that would bypass custom
// policy overrides and decision logging.
func (r *ABSAccessResolver) SetPermissionDecider(decider policy.PermissionDecider) {
	if r == nil {
		return
	}
	r.permissions = decider
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
			return catalog.AccessFilter{}, fmt.Errorf("%w: %w", abs.ErrAccessDenied, err)
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

// CanCurateMetadata answers the ABS metadata-mutation gate through the policy
// PDP, with the same input shape the native route gate builds
// (internal/api/middleware.PolicyPermissionMiddleware.RequireMetadataCurationForItem):
// the account's role and effective group permissions plus the household
// profile the ABS session selected. Routing through the PDP is what makes
// custom policy overrides and decision logging apply to ABS clients.
//
// ABS authentication skips a second PIN challenge but does not erase the
// selected profile: an admin acting through a non-primary profile is not an
// acting admin, exactly as on the native surface.
func (r *ABSAccessResolver) CanCurateMetadata(ctx context.Context, userID, profileID string) (bool, error) {
	if r == nil || r.users == nil || r.stores == nil {
		return false, fmt.Errorf("ABS metadata authorizer is not configured")
	}
	if r.permissions == nil {
		return false, fmt.Errorf("ABS metadata curation gate: %w", policy.ErrNoPermissionDecider)
	}
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return false, fmt.Errorf("%w: invalid ABS user id %q", abs.ErrAccessDenied, userID)
	}
	user, err := r.users.GetByID(ctx, uid)
	if err != nil {
		return false, fmt.Errorf("load ABS user %d: %w", uid, err)
	}
	if user == nil {
		return false, nil
	}
	effective, err := access.EffectivePolicyForUser(ctx, user, r.groups)
	if err != nil {
		return false, fmt.Errorf("load ABS access group policy for user %d: %w", uid, err)
	}
	actingAsPrimary, err := r.actingAsPrimary(ctx, uid, profileID)
	if err != nil {
		return false, err
	}
	decision, err := policy.CheckMetadataCuration(ctx, r.permissions, policy.GateActor{
		UserID:              user.ID,
		Role:                user.Role,
		Enabled:             user.Enabled,
		AssignedPermissions: effective.Permissions,
		DeclaredProfileID:   profileID,
		ActingAsPrimary:     actingAsPrimary,
	})
	if err != nil {
		return false, fmt.Errorf("evaluate ABS metadata curation for user %d: %w", uid, err)
	}
	return decision.Allowed, nil
}

// actingAsPrimary reports whether the ABS session's selected profile is the
// household's primary profile. No selected profile means the request is
// account-level, where the acting-admin rule keys off the empty profile id.
func (r *ABSAccessResolver) actingAsPrimary(ctx context.Context, userID int, profileID string) (bool, error) {
	if profileID == "" {
		return false, nil
	}
	store, err := r.stores.ForUser(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("open ABS user store for %d: %w", userID, err)
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return false, fmt.Errorf("load ABS profile %q: %w", profileID, err)
	}
	return profile != nil && profile.IsPrimary, nil
}
