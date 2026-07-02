package policy

// ScopeInput is the policy input document for resolving an authenticated
// viewer request into an effective access scope.
//
// Library lists retain the server-side nil-vs-empty distinction through the
// account_restricted and profile_library_restricted booleans. A nil or empty
// account_library_ids slice is not enough for policy authors to infer whether
// the account is unrestricted.
type ScopeInput struct {
	SchemaVersion int `json:"schema_version"`

	UserID                int    `json:"user_id"`
	SessionID             string `json:"session_id"`
	ProfileID             string `json:"profile_id"`
	AccountLibraryIDs     []int  `json:"account_library_ids"`
	AccountRestricted     bool   `json:"account_restricted"`
	AccountMaxQuality     string `json:"account_max_playback_quality"`
	AccessPolicyRevision  int64  `json:"access_policy_revision"`
	DisabledLibraryIDs    []int  `json:"disabled_library_ids"`
	ProfilePresent        bool   `json:"profile_present"`
	ProfileMaxRating      string `json:"profile_max_content_rating"`
	ProfileMaxQuality     string `json:"profile_max_playback_quality"`
	ProfileLibraryLimited bool   `json:"profile_library_restricted"`
	ProfileLibraryIDs     []int  `json:"profile_allowed_library_ids"`
	ProfileHasPIN         bool   `json:"profile_has_pin"`
	ProfileVerified       bool   `json:"profile_verified"`
	ProfileMetadataLang   string `json:"profile_preferred_metadata_language"`

	RequestTime string `json:"request_time"`
	DeviceID    string `json:"device_id"`
	ClientIP    string `json:"client_ip"`
	IsAPIKey    bool   `json:"is_api_key"`
}

// ScopeDecision is the policy output document for viewer scope resolution.
//
// Rego and JSON cannot preserve Go's nil-vs-empty slice semantics, so
// unrestricted explicitly records whether allowed_library_ids is meaningful.
// Adapters map unrestricted=true to a nil access.Scope.AllowedLibraryIDs.
type ScopeDecision struct {
	SchemaVersion             int    `json:"schema_version"`
	Unrestricted              bool   `json:"unrestricted"`
	AllowedLibraryIDs         []int  `json:"allowed_library_ids"`
	DisabledLibraryIDs        []int  `json:"disabled_library_ids"`
	LibrariesRestricted       bool   `json:"libraries_restricted"`
	MaxContentRating          string `json:"max_content_rating"`
	MaxPlaybackQuality        string `json:"max_playback_quality"`
	PreferredMetadataLanguage string `json:"preferred_metadata_language"`
	PolicyRevision            int64  `json:"policy_revision"`
	ProfileVerified           bool   `json:"profile_verified"`
}
