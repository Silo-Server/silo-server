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
	BaseURL string `json:"base_url"`
	// BaseURLs holds the remaining advertised addresses for a Plex run, in
	// preference order after BaseURL. Credentials written before this field
	// existed decode with it empty and run against BaseURL alone.
	BaseURLs       []string `json:"base_urls,omitempty"`
	ExternalUserID string   `json:"external_user_id"`
	ServerToken    string   `json:"server_token"`
	AccountToken   string   `json:"account_token"`
}

// candidates is the normalized address list a run races, preferred first.
func (c personalRunCredentials) candidates() []string {
	return plexBaseURLCandidates(c.BaseURL, c.BaseURLs)
}

// plexRunCredentials splits a normalized candidate list into the stored
// preferred address and its fallbacks. BaseURLs stays nil rather than empty
// when there is only one candidate: omitempty drops an empty slice on the way
// into the encrypted payload, so a credential that kept one would not compare
// equal to itself after a round trip.
func plexRunCredentials(candidates []string, serverToken, accountToken string) personalRunCredentials {
	credential := personalRunCredentials{BaseURL: candidates[0], ServerToken: serverToken, AccountToken: accountToken}
	if len(candidates) > 1 {
		credential.BaseURLs = candidates[1:]
	}
	return credential
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
