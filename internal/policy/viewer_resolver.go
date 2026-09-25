package policy

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ViewerResolver resolves viewer access scopes through the policy PDP.
type ViewerResolver struct {
	users        access.UserRepository
	storeFactory userstore.UserStoreProvider
	tokens       access.ProfileTokenValidator
	pdp          *PDP
	groups       access.GroupPolicyProvider
	unrated      access.UnratedContentPolicy
}

// WithUnratedContentPolicy installs the reader for access.unrated_content and
// returns the resolver. See access.Resolver.WithUnratedContentPolicy.
func (r *ViewerResolver) WithUnratedContentPolicy(policy access.UnratedContentPolicy) *ViewerResolver {
	if r != nil {
		r.unrated = policy
	}
	return r
}

// NewViewerResolver creates a PDP-backed viewer scope resolver.
func NewViewerResolver(
	users access.UserRepository,
	storeFactory userstore.UserStoreProvider,
	tokens access.ProfileTokenValidator,
	pdp *PDP,
	groups ...access.GroupPolicyProvider,
) *ViewerResolver {
	var groupProvider access.GroupPolicyProvider
	if len(groups) > 0 {
		groupProvider = groups[0]
	}
	return &ViewerResolver{
		users:        users,
		storeFactory: storeFactory,
		tokens:       tokens,
		pdp:          pdp,
		groups:       groupProvider,
	}
}

// Resolve computes the effective viewer scope for the request.
func (r *ViewerResolver) Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error) {
	user, err := r.users.GetByID(ctx, input.UserID)
	if err != nil {
		return access.Scope{}, fmt.Errorf("loading user %d: %w", input.UserID, err)
	}
	effective, err := access.EffectivePolicyForUser(ctx, user, r.groups)
	if err != nil {
		return access.Scope{}, fmt.Errorf("loading access group policy for user %d: %w", input.UserID, err)
	}

	store, err := r.storeFactory.ForUser(ctx, input.UserID)
	if err != nil {
		return access.Scope{}, fmt.Errorf("opening user store for %d: %w", input.UserID, err)
	}

	var profile *userstore.Profile
	if input.ProfileID != "" {
		profile, err = store.GetProfile(ctx, input.ProfileID)
		if err != nil {
			return access.Scope{}, fmt.Errorf("loading profile %s: %w", input.ProfileID, err)
		}
		if profile == nil {
			return access.Scope{}, access.ErrProfileNotFound
		}
	}
	preferences := access.ResolveViewerPreferences(ctx, store, input.ProfileID)
	return r.ResolveFacts(ctx, input, user, profile, effective, preferences)
}

// ResolveFacts evaluates the same PDP and PIN policy for facts loaded from one
// database snapshot. Callers own account/profile identity validation and reads.
func (r *ViewerResolver) ResolveFacts(ctx context.Context, input access.ResolveInput, user *models.User, profile *userstore.Profile, effective access.EffectiveUserPolicy, preferences access.ViewerPreferences) (access.Scope, error) {
	if user == nil || user.ID != input.UserID {
		return access.Scope{}, access.ErrProfileNotFound
	}
	profileVerified := input.ProfileID == ""
	if input.ProfileID != "" {
		if profile == nil || profile.ID != input.ProfileID {
			return access.Scope{}, access.ErrProfileNotFound
		}
		var err error
		profileVerified, err = access.VerifyProfileForRequest(profile, input, user.ID, user.AccessPolicyRevision, r.tokens)
		if err != nil {
			return access.Scope{}, err
		}
	}

	policyInput := ScopeInput{
		SchemaVersion:        1,
		UserID:               user.ID,
		SessionID:            input.SessionID,
		ProfileID:            input.ProfileID,
		AccountLibraryIDs:    slices.Clone(effective.LibraryIDs),
		AccountRestricted:    effective.LibraryIDs != nil,
		AccountMaxQuality:    effective.MaxPlaybackQuality,
		AccessPolicyRevision: user.AccessPolicyRevision,
		DisabledLibraryIDs:   preferences.DisabledLibraryIDs,
		ProfileVerified:      profileVerified,
		RequestTime:          time.Now().UTC().Format(time.RFC3339),
		// ResolveInput cannot distinguish API keys from compat callers that
		// also skip PIN verification, so v1 leaves this false.
		IsAPIKey: false,
	}
	if profile != nil {
		policyInput.ProfilePresent = true
		policyInput.ProfileMaxRating = profile.MaxContentRating
		policyInput.ProfileMaxAdvisoryAge = profile.MaxAdvisoryAge
		policyInput.ProfileMaxQuality = profile.MaxPlaybackQuality
		policyInput.ProfileLibraryLimited = profile.LibraryRestrictionsEnabled
		policyInput.ProfileLibraryIDs = slices.Clone(profile.AllowedLibraryIDs)
		policyInput.ProfileHasPIN = profile.PINHash != ""
		// Resolved canonically (profile scope -> contract default), not read off
		// the legacy profile column it migrated from. scope.rego relays this
		// value unchanged as a preference; the manifest deliberately declares no
		// constraint on it, since constraining a setting by a policy input fed
		// from that same setting would be circular.
		policyInput.ProfileMetadataLang = preferences.PreferredMetadataLanguage
	}

	if r.pdp == nil {
		return access.Scope{}, fmt.Errorf("resolve viewer scope policy: missing PDP")
	}
	decision, _, err := r.pdp.ResolveViewerScope(ctx, policyInput)
	if err != nil {
		return access.Scope{}, fmt.Errorf("resolve viewer scope policy: %w", err)
	}
	// The scope contract emits a tighten-only profile_verified output: a custom
	// override may revoke verification but never grant it. Enforce a revocation
	// the same way legacy PIN failures surface, so the middleware returns
	// 403 profile_unverified instead of silently proceeding.
	if profileVerified && !decision.ProfileVerified {
		return access.Scope{}, fmt.Errorf("%w: revoked by policy", access.ErrProfileUnverified)
	}

	var allowed []int
	if !decision.Unrestricted {
		allowed = slices.Clone(decision.AllowedLibraryIDs)
		if allowed == nil {
			allowed = []int{}
		}
	}
	disabled := slices.Clone(decision.DisabledLibraryIDs)
	if len(disabled) == 0 {
		disabled = nil
	}

	allowUnrated := false
	if r.unrated != nil {
		allowUnrated = r.unrated.AllowUnratedContent(ctx)
	}
	// Read off the profile, not the policy decision, the way AllowUnratedContent
	// comes from the server setting: the option only ever hides more, so no
	// override needs to loosen it. It applies to whatever limit the policy
	// settles on, including one an override lowered.
	requireAdvisory := profile != nil && profile.RequireAdvisoryAge && decision.MaxAdvisoryAge > 0

	return access.Scope{
		UserID:              user.ID,
		ProfileID:           input.ProfileID,
		AllowedLibraryIDs:   allowed,
		DisabledLibraryIDs:  disabled,
		LibrariesRestricted: decision.LibrariesRestricted,
		MaturityLimits: access.MaturityLimits{
			MaxContentRating:    access.StricterCeiling(decision.MaxContentRating, decision.MaxContentRatingOverride),
			AllowUnratedContent: allowUnrated,
			MaxAdvisoryAge:      decision.MaxAdvisoryAge,
			RequireAdvisoryAge:  requireAdvisory,
		},
		MaxPlaybackQuality:         decision.MaxPlaybackQuality,
		MaxRemoteStreamBitrateKbps: effective.MaxRemoteStreamBitrateKbps,
		MaxLocalStreamBitrateKbps:  effective.MaxLocalStreamBitrateKbps,
		PreferredMetadataLanguage:  decision.PreferredMetadataLanguage,
		MetadataLanguageOverrides:  preferences.MetadataLanguageOverrides,
		NextUpMode:                 preferences.NextUpMode,
		PolicyRevision:             user.AccessPolicyRevision,
		// The policy output is tighten-only (merged_profile_verified), so a
		// custom override may revoke verification but never grant it. ANDing
		// with the Go-computed fact keeps that invariant even if a policy bug
		// emitted true for an unverified profile.
		ProfileVerified: profileVerified && decision.ProfileVerified,
		// Same fact the legacy resolver records: the profile counts as
		// verified only because the caller (an API key) skipped the PIN
		// check. Mutations that v1 gates on a real verification read this.
		PINVerificationSkipped: profileVerified && decision.ProfileVerified &&
			profile != nil && profile.PINHash != "" && input.SkipPINVerification,
	}, nil
}
