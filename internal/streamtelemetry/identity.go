package streamtelemetry

import (
	"strconv"
	"time"
)

type SubjectKind string

const (
	SubjectUser    SubjectKind = "user"
	SubjectABSUser SubjectKind = "abs_user"
	SubjectIP      SubjectKind = "ip"
)

type Subject struct {
	Kind SubjectKind
	ID   string
}

func UserSubject(id int) Subject {
	return Subject{Kind: SubjectUser, ID: strconv.Itoa(id)}
}

type StartedAtSource string

const (
	StartedAtSourceClaim     StartedAtSource = "claim"
	StartedAtSourceSession   StartedAtSource = "session"
	StartedAtSourceIssuedAt  StartedAtSource = "issued_at"
	StartedAtSourceFirstSeen StartedAtSource = "first_seen"
)

type TokenIssuedAtSource string

const (
	TokenIssuedAtSourceNone     TokenIssuedAtSource = "none"
	TokenIssuedAtSourceVerified TokenIssuedAtSource = "verified"
)

type ClientVariant struct {
	Name    string
	Version string
	Build   string
	Channel string
}

// PathKind says whether a request reached the server over the regular listeners
// or over a direct path.
type PathKind string

const (
	PathKindRegular PathKind = "regular"
	PathKindDirect  PathKind = "direct"
)

// Transport is how a request reached the server, recorded only for requests on
// a direct path. Class and PathKind are the only values metrics may label with;
// the ports and the path generation are for admin responses, never for labels.
type Transport struct {
	PathKind       PathKind
	Class          string
	HostID         string
	AddressFamily  string
	LocalPort      int
	PeerPort       int
	PathGeneration uint64
}

type CaptureSet struct {
	Method          string
	Pattern         string
	ViewerIP        string
	DeviceID        string
	Client          ClientVariant
	UserAgent       string
	ReceivedAt      time.Time
	TokenIssuedAt   time.Time
	TokenIssuedFrom TokenIssuedAtSource
	// Transport is the zero value for a request on the regular path.
	Transport Transport
}

type Attachment struct {
	Subject             Subject
	ProfileID           string
	SessionID           string
	MediaFileID         int
	PlayMethod          string
	StartedAt           time.Time
	StartedAtSource     StartedAtSource
	TokenIssuedAt       time.Time
	TokenIssuedAtSource TokenIssuedAtSource
}

type IdentityConflict struct {
	Field      string
	Existing   string
	Offered    string
	ObservedAt time.Time
}
