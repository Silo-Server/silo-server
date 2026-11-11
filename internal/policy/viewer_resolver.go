package policy

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// ViewerResolver resolves viewer access scopes through the policy PDP.
type ViewerResolver struct {
	users        access.UserRepository
	storeFactory userstore.UserStoreProvider
	tokens       access.ProfileTokenValidator
	pdp          *PDP
	groups       access.GroupPolicyProvider
	unrated      access.UnratedContentPolicy
	defaults     settingsresolve.ServerSettings
	// shared, when set, is the pool Resolve sends its reads to as one batch.
	shared *pgxpool.Pool
}

// WithUnratedContentPolicy installs the reader for access.unrated_content and
// returns the resolver. See access.Resolver.WithUnratedContentPolicy.
func (r *ViewerResolver) WithUnratedContentPolicy(policy access.UnratedContentPolicy) *ViewerResolver {
	if r != nil {
		r.unrated = policy
	}
	return r
}

// WithAdminDefaults installs the administrator's setting defaults and returns
// the resolver. See access.Resolver.WithAdminDefaults.
func (r *ViewerResolver) WithAdminDefaults(defaults settingsresolve.ServerSettings) *ViewerResolver {
	if r != nil {
		r.defaults = defaults
	}
	return r
}

// sharedTablesProvider is a user store provider that reports whether every
// account's profiles and setting values live in a pool's shared tables, as
// pgstore.PostgresProvider does. The SQLite bridge's provider does not.
type sharedTablesProvider interface {
	StoresAccountsIn(pool *pgxpool.Pool) bool
}

// WithSharedTables lets Resolve send the reads of the account, its access
// group, the profile and the profile's viewer preferences to pool as one
// batch: one round trip in place of up to four. pool must be the one the
// resolver's user and access-group repositories read. It takes effect only
// when the user store provider keeps every account in pool's shared tables;
// otherwise, as with a nil pool, Resolve reads each through its repository.
func (r *ViewerResolver) WithSharedTables(pool *pgxpool.Pool) *ViewerResolver {
	if r == nil || pool == nil {
		return r
	}
	if shared, ok := r.storeFactory.(sharedTablesProvider); ok && shared.StoresAccountsIn(pool) {
		r.shared = pool
	}
	return r
}

// ViewerPreferencesStrict resolves the profile's viewer preferences from
// reader, such as one database snapshot, with this resolver's administrator
// defaults, so a scope built through ResolveFacts matches one built by Resolve.
func (r *ViewerResolver) ViewerPreferencesStrict(
	ctx context.Context, reader access.ViewerPreferenceReader, profileID string,
) (access.ViewerPreferences, error) {
	return access.ResolveViewerPreferencesStrict(ctx, reader, r.defaults, profileID)
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
	if r.shared != nil {
		return r.resolveBatched(ctx, input)
	}
	return r.resolveEach(ctx, input)
}

// resolveEach reads the facts one at a time through the repositories.
func (r *ViewerResolver) resolveEach(ctx context.Context, input access.ResolveInput) (access.Scope, error) {
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
	preferences := access.ResolveViewerPreferences(ctx, store, r.defaults, input.ProfileID)
	return r.ResolveFacts(ctx, input, user, profile, effective, preferences)
}

// viewerReads holds the results of resolveBatched's batch.
type viewerReads struct {
	user        *models.User
	userErr     error
	group       *access.GroupPolicy
	groupErr    error
	profile     *userstore.Profile
	profileErr  error
	preferences access.ViewerPreferenceRows
	readGroup   bool
}

// resolveBatched reads the same facts as resolveEach, in the same order, as
// one batch, and judges them in that order, so each answer and each failure
// is the one resolveEach gives. A failed statement fails the batch's later
// ones too; resolveEach would stop at the same failure, except for an access
// group read that does not apply to the account, after which the facts are
// read again one at a time.
func (r *ViewerResolver) resolveBatched(ctx context.Context, input access.ResolveInput) (access.Scope, error) {
	reads := r.sendViewerReads(ctx, input)
	if reads.userErr != nil {
		return access.Scope{}, fmt.Errorf("loading user %d: %w", input.UserID, reads.userErr)
	}
	user := reads.user
	effective := access.ApplyGroupPolicy(user, nil)
	if reads.readGroup {
		switch {
		case access.GroupApplies(user) && reads.groupErr != nil:
			return access.Scope{}, fmt.Errorf("loading access group policy for user %d: %w", input.UserID, reads.groupErr)
		case access.GroupApplies(user):
			effective = access.ApplyGroupPolicy(user, reads.group)
		case reads.groupErr != nil:
			return r.resolveEach(ctx, input)
		}
	}

	if input.ProfileID != "" {
		if reads.profileErr != nil {
			return access.Scope{}, fmt.Errorf("loading profile %s: %w", input.ProfileID, reads.profileErr)
		}
		if reads.profile == nil {
			return access.Scope{}, access.ErrProfileNotFound
		}
	}
	// Without a profile ResolveViewerPreferences reads nothing, and none was
	// queued.
	preferences := access.ResolveViewerPreferences(ctx, reads.preferences, r.defaults, input.ProfileID)
	return r.ResolveFacts(ctx, input, user, reads.profile, effective, preferences)
}

// sendViewerReads sends resolveEach's reads as one batch and collects every
// result before returning the connection. A request without a profile reads
// no profile and no preferences, and a resolver without an access-group
// provider reads no group.
func (r *ViewerResolver) sendViewerReads(ctx context.Context, input access.ResolveInput) viewerReads {
	batch := &pgx.Batch{}
	readUser := auth.QueueUser(batch, input.UserID)
	var readGroup func(pgx.BatchResults) (*access.GroupPolicy, error)
	if r.groups != nil {
		readGroup = access.QueueGroupPolicy(batch, input.UserID)
	}
	var readProfile func(pgx.BatchResults) (*userstore.Profile, error)
	if input.ProfileID != "" {
		readProfile = pgstore.QueueProfile(batch, input.UserID, input.ProfileID)
	}
	var readPreferences func(pgx.BatchResults) ([]userstore.SettingValue, error)
	if strings.TrimSpace(input.ProfileID) != "" {
		readPreferences = pgstore.QueueSettingValuesForResolution(batch, input.UserID, access.ViewerPreferenceQuery(input.ProfileID))
	}

	results := r.shared.SendBatch(ctx, batch)
	// Close reports the first failed read again, and every read below is
	// checked on its own.
	defer func() { _ = results.Close() }()

	var reads viewerReads
	reads.user, reads.userErr = readUser(results)
	if readGroup != nil {
		reads.readGroup = true
		reads.group, reads.groupErr = readGroup(results)
	}
	if readProfile != nil {
		reads.profile, reads.profileErr = readProfile(results)
	}
	if readPreferences != nil {
		reads.preferences.Rows, reads.preferences.Err = readPreferences(results)
	}
	return reads
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
		profileVerified, err = access.VerifyProfileForRequest(profile, input, user.ID, r.tokens)
		if err != nil {
			return access.Scope{}, err
		}
	}

	disabledPreference := preferences.DisabledLibraryIDs
	if input.ContentAccessOnly {
		disabledPreference = nil
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
		DisabledLibraryIDs:   disabledPreference,
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
		profileLibraryIDs, profileLibraryLimited := access.ProfileLibraryLimit(profile)
		policyInput.ProfileLibraryLimited = profileLibraryLimited
		policyInput.ProfileLibraryIDs = slices.Clone(profileLibraryIDs)
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
	hidden := slices.Clone(decision.HiddenLibraryIDs)
	if len(hidden) == 0 {
		hidden = nil
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

	var pinRevision int64
	var allowedTitles []string
	if profile != nil {
		pinRevision = profile.PINRevision
		// A household's allowances lift the profile's own limits only. When
		// a custom scope policy tightens them, the administrator's limit
		// wins and no allowance applies.
		if !maturityOverridden(decision, profile) {
			allowedTitles = profile.AllowedTitleIDs
		}
	}
	return access.Scope{
		UserID:              user.ID,
		ProfileID:           input.ProfileID,
		ProfilePINRevision:  pinRevision,
		AllowedLibraryIDs:   allowed,
		DisabledLibraryIDs:  disabled,
		LibrariesRestricted: decision.LibrariesRestricted,
		HiddenLibraryIDs:    hidden,
		MaturityLimits: access.MaturityLimits{
			MaxContentRating:    access.StricterCeiling(decision.MaxContentRating, decision.MaxContentRatingOverride),
			AllowUnratedContent: allowUnrated,
			MaxAdvisoryAge:      decision.MaxAdvisoryAge,
			RequireAdvisoryAge:  requireAdvisory,
		}.WithAllowedTitles(allowedTitles),
		MaxPlaybackQuality:         decision.MaxPlaybackQuality,
		MaxRemoteStreamBitrateKbps: effective.MaxRemoteStreamBitrateKbps,
		MaxLocalStreamBitrateKbps:  effective.MaxLocalStreamBitrateKbps,
		PreferredMetadataLanguage:  decision.PreferredMetadataLanguage,
		MetadataLanguageOverrides:  preferences.MetadataLanguageOverrides,
		NextUpMode:                 preferences.NextUpMode,
		NextUpNewReleasesFirst:     preferences.NextUpNewReleasesFirst,
		HideWatchedItemsOnHome:     preferences.HideWatchedItemsOnHome,
		PreferencesDegraded:        preferences.Degraded,
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

// maturityOverridden reports whether the scope policy set limits other than
// the profile's own: a content-rating override, or an advisory-age limit
// that is not the profile's.
func maturityOverridden(decision ScopeDecision, profile *userstore.Profile) bool {
	return access.HasCeiling(decision.MaxContentRatingOverride) || decision.MaxAdvisoryAge != profile.MaxAdvisoryAge
}
