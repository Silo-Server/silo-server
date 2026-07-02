package access

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// settingKeyDisabledLibraryIDs is the user-settings key that stores a JSON
// array of library IDs the user has chosen to hide.
const settingKeyDisabledLibraryIDs = "disabled_library_ids"

// UserRepository loads account-level access settings.
type UserRepository interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// ProfileTokenValidator validates short-lived profile verification tokens.
type ProfileTokenValidator interface {
	Validate(tokenStr string) (*ProfileTokenClaims, error)
}

// Resolver resolves a viewer request into an effective access scope.
type Resolver struct {
	users        UserRepository
	storeFactory userstore.UserStoreProvider
	tokens       ProfileTokenValidator
	groups       GroupPolicyProvider
}

// NewResolver creates a new scope resolver.
func NewResolver(users UserRepository, storeFactory userstore.UserStoreProvider, tokens ProfileTokenValidator, groups ...GroupPolicyProvider) *Resolver {
	var groupProvider GroupPolicyProvider
	if len(groups) > 0 {
		groupProvider = groups[0]
	}
	return &Resolver{
		users:        users,
		storeFactory: storeFactory,
		tokens:       tokens,
		groups:       groupProvider,
	}
}

// Resolve computes the effective viewer scope for the request.
func (r *Resolver) Resolve(ctx context.Context, input ResolveInput) (Scope, error) {
	user, err := r.users.GetByID(ctx, input.UserID)
	if err != nil {
		return Scope{}, fmt.Errorf("loading user %d: %w", input.UserID, err)
	}
	effective, err := EffectivePolicyForUser(ctx, user, r.groups)
	if err != nil {
		return Scope{}, fmt.Errorf("loading access group policy for user %d: %w", input.UserID, err)
	}

	scope := Scope{
		UserID:              user.ID,
		ProfileID:           input.ProfileID,
		AllowedLibraryIDs:   cloneInts(effective.LibraryIDs),
		LibrariesRestricted: effective.LibraryIDs != nil,
		MaxPlaybackQuality:  NormalizePlaybackQuality(effective.MaxPlaybackQuality),
		PolicyRevision:      user.AccessPolicyRevision,
		ProfileVerified:     input.ProfileID == "",
	}

	store, err := r.storeFactory.ForUser(ctx, input.UserID)
	if err != nil {
		return Scope{}, fmt.Errorf("opening user store for %d: %w", input.UserID, err)
	}

	if input.ProfileID != "" {
		profile, err := store.GetProfile(ctx, input.ProfileID)
		if err != nil {
			return Scope{}, fmt.Errorf("loading profile %s: %w", input.ProfileID, err)
		}
		if profile == nil {
			return Scope{}, ErrProfileNotFound
		}

		scope.MaxContentRating = profile.MaxContentRating
		scope.MaxPlaybackQuality = MinQuality(scope.MaxPlaybackQuality, NormalizePlaybackQuality(profile.MaxPlaybackQuality))
		scope.PreferredMetadataLanguage = profile.PreferredMetadataLanguage
		scope.AllowedLibraryIDs, scope.LibrariesRestricted = effectiveLibraries(effective.LibraryIDs, profile)
		verified, err := VerifyProfileForRequest(profile, input, user.ID, user.AccessPolicyRevision, r.tokens)
		if err != nil {
			return Scope{}, err
		}
		scope.ProfileVerified = verified
	}

	// Apply user-level disabled library IDs setting.
	disabled := DisabledLibraryIDs(ctx, store)
	if len(disabled) > 0 {
		if scope.AllowedLibraryIDs != nil {
			// Restricted user: subtract disabled IDs from the allowed set.
			scope.AllowedLibraryIDs = subtractInts(scope.AllowedLibraryIDs, disabled)
		} else {
			// Unrestricted user: pass disabled IDs through so query layer
			// can apply a NOT IN filter.
			scope.DisabledLibraryIDs = disabled
		}
	}

	return scope, nil
}

// VerifyProfileForRequest applies the legacy profile PIN/token verification
// checks for a resolved profile and returns whether the profile is verified for
// the request.
func VerifyProfileForRequest(
	profile *userstore.Profile,
	input ResolveInput,
	userID int,
	policyRevision int64,
	tokens ProfileTokenValidator,
) (bool, error) {
	if profile == nil {
		return input.ProfileID == "", nil
	}

	profileVerified := profile.PINHash == "" || input.SkipPINVerification
	if profile.PINHash != "" && !input.SkipPINVerification {
		if tokens == nil {
			return false, ErrProfileUnverified
		}
		claims, err := tokens.Validate(input.ProfileToken)
		if err != nil {
			return false, err
		}
		if claims.UserID != userID || claims.SessionID != input.SessionID || claims.ProfileID != profile.ID || claims.PolicyRevision != policyRevision {
			return false, ErrProfileUnverified
		}
		profileVerified = true
	}
	return profileVerified, nil
}

// DisabledLibraryIDs reads and parses the disabled_library_ids user setting.
func DisabledLibraryIDs(ctx context.Context, store userstore.UserStore) []int {
	raw, err := store.GetSetting(ctx, settingKeyDisabledLibraryIDs)
	if err != nil || raw == "" {
		return nil
	}
	var ids []int
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	// Filter out invalid values.
	n := 0
	for _, id := range ids {
		if id > 0 {
			ids[n] = id
			n++
		}
	}
	return ids[:n]
}

// subtractInts removes all values in exclude from src, preserving order.
func subtractInts(src, exclude []int) []int {
	if len(exclude) == 0 {
		return src
	}
	set := make(map[int]struct{}, len(exclude))
	for _, id := range exclude {
		set[id] = struct{}{}
	}
	out := make([]int, 0, len(src))
	for _, id := range src {
		if _, ok := set[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

func effectiveLibraries(accountLibraryIDs []int, profile *userstore.Profile) ([]int, bool) {
	accountRestricted := accountLibraryIDs != nil
	profileRestricted := profile.LibraryRestrictionsEnabled

	switch {
	case accountRestricted && profileRestricted:
		return intersectInts(accountLibraryIDs, profile.AllowedLibraryIDs), true
	case accountRestricted:
		return cloneInts(accountLibraryIDs), true
	case profileRestricted:
		return sortedUniqueInts(profile.AllowedLibraryIDs), true
	default:
		return nil, false
	}
}

func intersectInts(left, right []int) []int {
	if len(left) == 0 || len(right) == 0 {
		return []int{}
	}
	set := make(map[int]struct{}, len(left))
	for _, id := range left {
		set[id] = struct{}{}
	}
	var out []int
	for _, id := range right {
		if _, ok := set[id]; ok {
			out = append(out, id)
		}
	}
	return sortedUniqueInts(out)
}

func sortedUniqueInts(values []int) []int {
	if len(values) == 0 {
		return []int{}
	}
	out := cloneInts(values)
	sort.Ints(out)
	n := 1
	for i := 1; i < len(out); i++ {
		if out[i] != out[i-1] {
			out[n] = out[i]
			n++
		}
	}
	return out[:n]
}

func cloneInts(values []int) []int {
	if values == nil {
		return nil
	}
	out := make([]int, len(values))
	copy(out, values)
	return out
}
