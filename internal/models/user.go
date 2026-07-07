package models

import "time"

// User represents a row in the users table.
type User struct {
	ID                        int
	Email                     string
	Username                  string
	PasswordHash              string
	LocalPasswordLoginEnabled bool
	Role                      string
	Permissions               []string
	Enabled                   bool
	LibraryIDs                []int // nullable in PG (nil = all libraries)
	MaxPlaybackQuality        string
	AccessPolicyRevision      int64
	MaxStreams                int
	MaxTranscodes             int
	MaxProfiles               int
	DownloadAllowed           bool
	DownloadTranscodeAllowed  bool
	AccessGroupID             *int64
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// CreateUserInput contains the fields required to create a new user.
type CreateUserInput struct {
	Email                     string // required
	Username                  string // required
	Password                  string // plaintext, will be bcrypt-hashed
	LocalPasswordLoginEnabled *bool
	Role                      string // e.g. "admin", "user"
	Permissions               []string
	LibraryIDs                []int
	MaxPlaybackQuality        string
	MaxStreams                *int  // nil = use DB default (0 = unrestricted at the user layer; the access group governs)
	MaxTranscodes             *int  // nil = use DB default (0 = unrestricted at the user layer; the access group governs)
	MaxProfiles               *int  // nil = use DB default (5); minimum 1
	DownloadAllowed           *bool // nil = use DB default (true)
	DownloadTranscodeAllowed  *bool // nil = use DB default (false)
	AccessGroupID             *int64
}

// UpdateUserInput contains optional fields for updating a user.
// Pointer fields: nil means "don't update", non-nil means "set to this value".
type UpdateUserInput struct {
	Email                     *string
	Username                  *string
	Password                  *string // plaintext, will be bcrypt-hashed if provided
	LocalPasswordLoginEnabled *bool
	Role                      *string
	Permissions               *[]string
	Enabled                   *bool
	LibraryIDs                *[]int
	MaxPlaybackQuality        *string
	MaxStreams                *int
	MaxTranscodes             *int
	MaxProfiles               *int
	DownloadAllowed           *bool
	DownloadTranscodeAllowed  *bool
	AccessGroupIDSet          bool
	AccessGroupID             *int64
}
