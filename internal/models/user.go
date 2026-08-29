package models

import "time"

// User represents a row in the users table.
//
// Policy fields follow inherit/override semantics: a nil pointer (or nil
// LibraryIDs) means the field is unset on the account and the value is
// inherited from the user's access group; a non-nil value is an explicit
// per-user override that replaces the group value for that field. Nothing
// outside internal/access should read these raw — resolve them through
// access.EffectivePolicyForUser.
type User struct {
	ID                        int
	Email                     string
	Username                  string
	PasswordHash              string
	LocalPasswordLoginEnabled bool
	Role                      string
	Permissions               []string
	Enabled                   bool
	LibraryIDs                []int   // nil = inherit; non-nil = explicit library list (empty = none)
	MaxPlaybackQuality        *string // nil = inherit; "" = explicit "no ceiling"
	AccessPolicyRevision      int64
	MaxStreams                *int  // nil = inherit; 0 = explicit unlimited
	MaxTranscodes             *int  // nil = inherit; 0 = explicit unlimited
	TranscodeAllowed          *bool // nil = inherit
	AudioTranscodeAllowed     *bool // nil = inherit
	MaxProfiles               int
	DownloadAllowed           *bool // nil = inherit
	DownloadTranscodeAllowed  *bool // nil = inherit
	RequestsAllowed           *bool // nil = inherit
	AccessGroupID             *int64
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// CreateUserInput contains the fields required to create a new user.
//
// Account roles. An admin account is never a member of an access group: group
// ceilings (stream caps, library lists) must not apply to the server operator,
// so the repository keeps admins ungrouped and the policy resolver ignores any
// group an admin row still carries.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// Policy pointers: nil = inherit from the access group (stored as NULL);
// non-nil = explicit override.
type CreateUserInput struct {
	Email                     string // required
	Username                  string // required
	Password                  string // plaintext, will be bcrypt-hashed
	LocalPasswordLoginEnabled *bool
	Role                      string // e.g. "admin", "user"
	Permissions               []string
	LibraryIDs                []int
	MaxPlaybackQuality        *string
	MaxStreams                *int
	MaxTranscodes             *int
	TranscodeAllowed          *bool
	AudioTranscodeAllowed     *bool
	MaxProfiles               *int // nil = use DB default (5); minimum 1
	DownloadAllowed           *bool
	DownloadTranscodeAllowed  *bool
	RequestsAllowed           *bool
	AccessGroupID             *int64
}

// Optional is a tri-state update field: Set=false leaves the column alone,
// Set=true with a nil Value clears it to NULL (inherit), Set=true with a
// non-nil Value stores an explicit override.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// SetValue returns an Optional that stores value.
func SetValue[T any](value T) Optional[T] {
	return Optional[T]{Set: true, Value: &value}
}

// ClearValue returns an Optional that clears the column (inherit).
func ClearValue[T any]() Optional[T] {
	return Optional[T]{Set: true}
}

// UpdateUserInput contains optional fields for updating a user.
// Plain pointer fields: nil means "don't update", non-nil means "set to this
// value". Optional fields carry the tri-state needed by nullable policy
// columns (leave / clear to inherit / set override).
type UpdateUserInput struct {
	Email                     *string
	Username                  *string
	Password                  *string // plaintext, will be bcrypt-hashed if provided
	LocalPasswordLoginEnabled *bool
	Role                      *string
	Permissions               *[]string
	Enabled                   *bool
	LibraryIDs                Optional[[]int]
	MaxPlaybackQuality        Optional[string]
	MaxStreams                Optional[int]
	MaxTranscodes             Optional[int]
	TranscodeAllowed          Optional[bool]
	AudioTranscodeAllowed     Optional[bool]
	MaxProfiles               *int
	DownloadAllowed           Optional[bool]
	DownloadTranscodeAllowed  Optional[bool]
	RequestsAllowed           Optional[bool]
	AccessGroupID             Optional[int64]
}
