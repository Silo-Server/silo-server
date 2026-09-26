package historyimport

import "errors"

var (
	ErrPersonalCredentialsUnavailable = errors.New("personal import credentials are unavailable")
	ErrPersonalSessionChanged         = errors.New("personal import login session changed; authenticate again")
	ErrPersonalAdmissionUncertain     = errors.New("personal import acceptance could not be confirmed; check imports before submitting again")
)

// Version 2 is reserved for personal intent. Older admin workers only claim
// version 1 and therefore cannot mistake a personal run for an admin mapping.
const personalDispatchVersion = 2
const dispatchKindPersonal = "personal"
const personalCredentialVersion = 1

// These types are private execution inputs. They must never be embedded in a
// public run, logged, or serialized into an API response. Passwords are absent.
type personalRunCredentials struct {
	BaseURL        string `json:"base_url"`
	ExternalUserID string `json:"external_user_id"`
	ServerToken    string `json:"server_token"`
	AccountToken   string `json:"account_token"`
}

type personalRunAdmission struct {
	UserID           int
	ProfileID        string
	SourceType       string
	ConnectionMode   string
	SourceID         int
	SourceRevision   int64
	Credentials      personalRunCredentials
	ConnectSession   *ConnectSession
	PlexSession      *PlexSession
	SelectedServerID string
}
