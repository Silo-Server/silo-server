package access

// Scope is the resolved effective access policy for a viewer request.
type Scope struct {
	UserID              int
	ProfileID           string
	AllowedLibraryIDs   []int
	DisabledLibraryIDs  []int // libraries whose membership globally hides an item
	LibrariesRestricted bool
	MaxContentRating    string
	// AllowUnratedContent carries the server-wide decision for titles with no
	// rating (empty, or explicitly unrated). False, the default, hides them
	// from any viewer with a MaxContentRating ceiling; true shows them. It has
	// no effect without a ceiling, and never admits an unrecognized rating
	// (see UnrecognizedRatingAge). Resolved from the server setting
	// access.unrated_content.
	AllowUnratedContent        bool
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
