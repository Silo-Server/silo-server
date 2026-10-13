package access

import "github.com/Silo-Server/silo-server/internal/userstore"

// ProfileIsLimited reports whether acting as the profile is narrower than
// acting as its account: the profile is PIN-protected, has active maturity
// limits (MaturityLimits.Active, so a stored " " deny-all ceiling counts), or
// is restricted to a subset of libraries. The child flag and the
// playback-quality ceiling do not count: neither hides a title.
//
// Only the stored columns count. A custom scope policy that narrows one
// profile through input.profile_id is not consulted: it applies when a
// request names that profile, and a household that must not fall back to
// account scope gives the profile one of these limits.
func ProfileIsLimited(profile userstore.Profile) bool {
	maturity := MaturityLimits{MaxContentRating: profile.MaxContentRating, MaxAdvisoryAge: profile.MaxAdvisoryAge}
	return profile.PINHash != "" ||
		maturity.Active() ||
		profile.LibraryRestrictionsEnabled
}

// HouseholdRequiresProfile reports whether a request on the account must name
// a profile to read the catalog. A request without one resolves to the
// account's own limits, which would let a device signed into the household
// bypass a limited profile's restrictions or PIN by omitting X-Profile-Id.
// An account whose profiles are all unlimited loses nothing to account scope,
// so it keeps serving profile-less requests.
func HouseholdRequiresProfile(profiles []userstore.Profile) bool {
	for _, profile := range profiles {
		if ProfileIsLimited(profile) {
			return true
		}
	}
	return false
}
