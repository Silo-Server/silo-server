package access

// Scope is the resolved effective access policy for a viewer request.
type Scope struct {
	UserID              int
	ProfileID           string
	AllowedLibraryIDs   []int
	DisabledLibraryIDs  []int // libraries whose membership globally hides an item
	LibrariesRestricted bool
	// MaturityLimits are the profile's maturity restrictions, embedded so a
	// Scope copies them into a catalog filter as one value. JSON flattens the
	// embedded fields, so the access fingerprints that hash a Scope (socket
	// tickets, progress snapshots) see the same keys as before they moved here.
	MaturityLimits
	MaxPlaybackQuality         string
	MaxRemoteStreamBitrateKbps int
	MaxLocalStreamBitrateKbps  int
	// PreferredMetadataLanguage is the profile's metadata (presentation)
	// language; "" inherits the library's metadata language.
	PreferredMetadataLanguage string
	// MetadataLanguageOverrides maps a media item's canonical original-language
	// code to the profile's target metadata language for that source.
	MetadataLanguageOverrides map[string]string
	PolicyRevision            int64
	ProfileVerified           bool
	// PINVerificationSkipped is set when ProfileVerified holds only because
	// the request was exempt from PIN verification (an API-key credential,
	// ResolveInput.SkipPINVerification), not because a profile token proved
	// the PIN. Checks that need an actual verification, such as household
	// management by a PIN-locked primary profile, treat such a scope as
	// unverified.
	PINVerificationSkipped bool
	// NextUpMode is the profile's resolved ui.next_up_mode, or "" when the
	// scope has no profile or its preferences could not be read. It is a
	// presentation preference, not access, so it stays out of the JSON that
	// socket tickets and progress snapshots hash as the access fingerprint:
	// changing the row mid-session must not invalidate either.
	NextUpMode string `json:"-"`
}

// MaturityLimits are the per-viewer maturity restrictions every catalog read
// enforces (see catalog.ApplyMaturityLimits). They travel as one value, embedded
// in Scope and in each catalog filter, so code that narrows a filter down to
// its maturity limits copies all of them at once and cannot keep one limit and
// silently drop another. The zero value restricts nothing.
type MaturityLimits struct {
	// MaxContentRating is the profile's content-rating ceiling ("PG-13",
	// "FSK 16", "12"). Only "" means no ceiling (see HasCeiling).
	MaxContentRating string
	// AllowUnratedContent carries the server-wide decision for titles with no
	// rating (empty, or explicitly unrated). False, the default, hides them
	// from any viewer with a MaxContentRating ceiling; true shows them. It has
	// no effect without a ceiling, never admits an unrecognized rating (see
	// UnrecognizedRatingAge), and does not touch MaxAdvisoryAge. Resolved from
	// the server setting access.unrated_content.
	AllowUnratedContent bool
	// MaxAdvisoryAge is the profile's advisory-age limit: titles whose advisory
	// age (media_items.advisory_age, e.g. Common Sense Media's "13+") is above
	// it are hidden. A title with no advisory age is not hidden by it; the
	// MaxContentRating ceiling alone decides. 0 means no limit.
	//
	// omitempty keeps a Scope without a limit hashing exactly as it did before
	// the field existed, so a deploy does not invalidate every in-flight socket
	// ticket and progress snapshot at once.
	MaxAdvisoryAge int `json:",omitempty"`
}

// Active reports whether the limits restrict anything at all.
func (m MaturityLimits) Active() bool {
	return HasCeiling(m.MaxContentRating) || m.MaxAdvisoryAge > 0
}

// Advisory-age limit bounds a profile may store. They match the range the host
// accepts for a title's advisory age, so every storable limit can bite.
const (
	MinAdvisoryAgeLimit = 1
	MaxAdvisoryAgeLimit = 21
)

// ValidAdvisoryAgeLimit reports whether age is a storable advisory-age limit.
// 0, "no limit", is not a stored value: callers represent it as absent/null.
func ValidAdvisoryAgeLimit(age int) bool {
	return age >= MinAdvisoryAgeLimit && age <= MaxAdvisoryAgeLimit
}

// ResolveInput is the request input for resolving a viewer access scope.
type ResolveInput struct {
	UserID              int
	SessionID           string
	ProfileID           string
	ProfileToken        string
	SkipPINVerification bool
}

// ProfileTokenClaims are the claims embedded in a verified profile token.
type ProfileTokenClaims struct {
	UserID         int
	SessionID      string
	ProfileID      string
	PolicyRevision int64
}
