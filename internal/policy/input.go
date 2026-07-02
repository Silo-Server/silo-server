package policy

const (
	// ActionDownload checks whether a user may create or serve original downloads.
	ActionDownload = "download"
	// ActionDownloadTranscode checks whether a user may request transcode-backed downloads.
	ActionDownloadTranscode = "download_transcode"
	// ActionPlaybackAdmission checks whether a playback session may be admitted.
	ActionPlaybackAdmission = "playback_admission"

	// RequestedActionDirectPlay is the playback admission fact for non-transcode playback.
	RequestedActionDirectPlay = "direct_play"
	// RequestedActionTranscode is the playback admission fact for transcode playback.
	RequestedActionTranscode = "transcode"

	// PermissionActingAdmin is the pseudo-permission used for acting-admin gates.
	PermissionActingAdmin = "acting_admin"
	// PermissionMarkerEdit mirrors auth.PermissionMarkerEdit.
	PermissionMarkerEdit = "marker_edit"
	// PermissionMetadataCuration mirrors auth.PermissionMetadataCuration.
	PermissionMetadataCuration = "metadata_curation"
)

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

// PermissionInput is the policy input document for route-level permission
// gates.
//
// acting_as_primary is precomputed in Go from the declared profile because
// Rego never performs database lookups. user_libraries_restricted distinguishes
// nil user library assignment (unrestricted) from an empty allowlist.
type PermissionInput struct {
	SchemaVersion int `json:"schema_version"`

	UserID                  int      `json:"user_id"`
	Role                    string   `json:"role"`
	UserEnabled             bool     `json:"user_enabled"`
	AssignedPermissions     []string `json:"assigned_permissions"`
	Permission              string   `json:"permission"`
	DeclaredProfileID       string   `json:"declared_profile_id"`
	ActingAsPrimary         bool     `json:"acting_as_primary"`
	TargetLibraryIDs        []int    `json:"target_library_ids"`
	UserLibraryIDs          []int    `json:"user_library_ids"`
	UserLibrariesRestricted bool     `json:"user_libraries_restricted"`

	RequestTime string `json:"request_time"`
	DeviceID    string `json:"device_id"`
	ClientIP    string `json:"client_ip"`
}

// PermissionDecision is the policy output document for route-level permission
// gates.
type PermissionDecision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

// ActionInput is the policy input document for download eligibility,
// download-transcode eligibility, and playback admission.
//
// Go supplies all dynamic facts: live playback counts, user flags, and config
// flags. Policy never reads server configuration or session state directly.
type ActionInput struct {
	SchemaVersion int `json:"schema_version"`

	Action                   string `json:"action"`
	UserID                   int    `json:"user_id"`
	DownloadAllowed          bool   `json:"download_allowed"`
	DownloadTranscodeAllowed bool   `json:"download_transcode_allowed"`
	MaxStreams               int    `json:"max_streams"`
	MaxTranscodes            int    `json:"max_transcodes"`

	DownloadsEnabled   bool `json:"downloads_enabled"`
	TranscodeEnabled   bool `json:"transcode_enabled"`
	ArtifactsAvailable bool `json:"artifacts_available"`

	CurrentActiveStreams    int    `json:"current_active_streams"`
	CurrentActiveTranscodes int    `json:"current_active_transcodes"`
	RequestedAction         string `json:"requested_action"`

	RequestedQuality   string `json:"requested_quality"`
	FileQuality        string `json:"file_quality"`
	MaxPlaybackQuality string `json:"max_playback_quality"`
	ContentRating      string `json:"content_rating"`
	MaxContentRating   string `json:"max_content_rating"`

	RequestTime string `json:"request_time"`
	DeviceID    string `json:"device_id"`
	ClientIP    string `json:"client_ip"`
}

// ActionDecision is the policy output document for action checks. QualityCeiling
// is set only when a custom override narrows the input max_playback_quality.
type ActionDecision struct {
	Allowed        bool   `json:"allowed"`
	Reason         string `json:"reason"`
	QualityCeiling string `json:"quality_ceiling"`
}
