package streamtelemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

const (
	codecVersion = 1
	maxWireSlice = 100_000
	maxWireMap   = 4_096
)

type errUnsupportedCodecVersion struct{ Version int }

func (e errUnsupportedCodecVersion) Error() string {
	return fmt.Sprintf("unsupported stream telemetry codec version %d", e.Version)
}

type wireSubject struct {
	Kind SubjectKind `json:"k"`
	ID   string      `json:"id"`
}

type wireClientVariant struct {
	Name    string `json:"n"`
	Version string `json:"v"`
	Build   string `json:"b"`
	Channel string `json:"c"`
}

type wireRouteActivity struct {
	Method             string `json:"m"`
	Pattern            string `json:"p"`
	Role               Role   `json:"r"`
	Class              Class  `json:"c"`
	CapRelevant        bool   `json:"cr"`
	Open               int    `json:"o"`
	Requests           int64  `json:"rq"`
	BytesAccepted      int64  `json:"b"`
	LastByteAccepted   int64  `json:"lb"`
	LastObservationEnd int64  `json:"le"`
}

type wireIdentityConflict struct {
	Field      string `json:"f"`
	Existing   string `json:"e"`
	Offered    string `json:"o"`
	ObservedAt int64  `json:"at"`
}

type wireSession struct {
	V                           int                                `json:"v"`
	Subject                     wireSubject                        `json:"sub"`
	ProfileID                   string                             `json:"pid"`
	SessionID                   string                             `json:"sid"`
	MediaFileID                 int                                `json:"mfid"`
	PlayMethod                  string                             `json:"pm"`
	MediaFileIDs                []int                              `json:"mfids"`
	MediaFileIDsOverflowed      bool                               `json:"mfido"`
	PlayMethods                 []string                           `json:"pms"`
	PlayMethodsOverflowed       bool                               `json:"pmo"`
	StartedAt                   int64                              `json:"st"`
	StartedAtSource             StartedAtSource                    `json:"sts"`
	StartedAtDegraded           bool                               `json:"std"`
	BytesAccepted               int64                              `json:"ba"`
	LastByteAccepted            int64                              `json:"lb"`
	LastObservationEnd          int64                              `json:"le"`
	OpenObservations            int                                `json:"oo"`
	RealtimeConnectionAlive     bool                               `json:"rt"`
	RequestCount                int64                              `json:"rc"`
	Routes                      []wireRouteActivity                `json:"routes"`
	RoutesOverflowed            bool                               `json:"ro"`
	ViewerIPs                   []string                           `json:"ips"`
	ViewerIPsOverflowed         bool                               `json:"ipso"`
	DeviceIDs                   []string                           `json:"dids"`
	DeviceIDsOverflowed         bool                               `json:"didso"`
	ClientVariants              []wireClientVariant                `json:"clients"`
	ClientVariantsOverflowed    bool                               `json:"clientso"`
	UserAgents                  []string                           `json:"uas"`
	UserAgentsOverflowed        bool                               `json:"uaso"`
	TokenIssuedAts              []int64                            `json:"tiats"`
	TokenIssuedAtsOverflowed    bool                               `json:"tiatso"`
	TokenIssuedAtSources        map[TokenIssuedAtSource]int64      `json:"tis"`
	Outcomes                    map[httpstream.StreamOutcome]int64 `json:"out"`
	HasIdentityConflict         bool                               `json:"hic"`
	IdentityConflicts           []wireIdentityConflict             `json:"ics"`
	IdentityConflictsOverflowed bool                               `json:"icso"`
}

type wireTransfer struct {
	V                  int                                `json:"v"`
	ID                 string                             `json:"id"`
	Subject            wireSubject                        `json:"sub"`
	ProfileID          string                             `json:"pid"`
	MediaFileID        int                                `json:"mfid"`
	Method             string                             `json:"m"`
	Pattern            string                             `json:"p"`
	Role               Role                               `json:"r"`
	BytesAccepted      int64                              `json:"ba"`
	LastByteAccepted   int64                              `json:"lb"`
	LastObservationEnd int64                              `json:"le"`
	OpenObservations   int                                `json:"oo"`
	RequestCount       int64                              `json:"rc"`
	ViewerIP           string                             `json:"ip"`
	DeviceID           string                             `json:"did"`
	Client             wireClientVariant                  `json:"client"`
	UserAgent          string                             `json:"ua"`
	Outcomes           map[httpstream.StreamOutcome]int64 `json:"out"`
}

type publisherMeta struct {
	V                        int    `json:"v"`
	PublisherID              string `json:"pid"`
	NodeID                   string `json:"nid"`
	Epoch                    int64  `json:"ep"`
	Sequence                 uint64 `json:"sq"`
	CapturedAtUnixNano       int64  `json:"cap"`
	Truncated                bool   `json:"tr"`
	DroppedObservations      int64  `json:"do"`
	DroppedBytes             int64  `json:"db"`
	UnattributedObservations int64  `json:"uo"`
	UnattributedBytes        int64  `json:"ub"`
	SessionCount             int    `json:"sc"`
	TransferCount            int    `json:"tc"`
}

func timeToUnixNano(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

func timeFromUnixNano(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value)
}

// checkVersion validates the version a record already carries. It takes the
// decoded field rather than the raw bytes: every wire type embeds V, so parsing
// the payload a second time into a throwaway header struct doubled the JSON work
// of every record in a merged-view rebuild — measured at ~347 ms for 50 000
// sessions, all of it decode.
func checkVersion(version int) error {
	if version != codecVersion {
		return errUnsupportedCodecVersion{Version: version}
	}
	return nil
}

func encodeSession(value SessionView) ([]byte, error) {
	w := wireSession{V: codecVersion, Subject: wireSubject{Kind: value.Subject.Kind, ID: value.Subject.ID}, ProfileID: value.ProfileID,
		SessionID: value.SessionID, MediaFileID: value.MediaFileID, PlayMethod: value.PlayMethod,
		MediaFileIDs: value.MediaFileIDs, MediaFileIDsOverflowed: value.MediaFileIDsOverflowed,
		PlayMethods: value.PlayMethods, PlayMethodsOverflowed: value.PlayMethodsOverflowed,
		StartedAt: timeToUnixNano(value.StartedAt), StartedAtSource: value.StartedAtSource, StartedAtDegraded: value.StartedAtDegraded,
		BytesAccepted: value.BytesAccepted, LastByteAccepted: timeToUnixNano(value.LastByteAccepted), LastObservationEnd: timeToUnixNano(value.LastObservationEnd),
		OpenObservations: value.OpenObservations, RealtimeConnectionAlive: value.RealtimeConnectionAlive, RequestCount: value.RequestCount,
		RoutesOverflowed: value.RoutesOverflowed, ViewerIPs: value.ViewerIPs, ViewerIPsOverflowed: value.ViewerIPsOverflowed,
		DeviceIDs: value.DeviceIDs, DeviceIDsOverflowed: value.DeviceIDsOverflowed, UserAgents: value.UserAgents, UserAgentsOverflowed: value.UserAgentsOverflowed,
		TokenIssuedAtsOverflowed: value.TokenIssuedAtsOverflowed,
		TokenIssuedAtSources:     value.TokenIssuedAtSources, Outcomes: value.Outcomes, HasIdentityConflict: value.HasIdentityConflict,
		IdentityConflictsOverflowed: value.IdentityConflictsOverflowed, ClientVariantsOverflowed: value.ClientVariantsOverflowed}
	for _, route := range value.Routes {
		w.Routes = append(w.Routes, wireRouteActivity{Method: route.Method, Pattern: route.Pattern, Role: route.Role, Class: route.Class, CapRelevant: route.CapRelevant, Open: route.Open, Requests: route.Requests, BytesAccepted: route.BytesAccepted, LastByteAccepted: timeToUnixNano(route.LastByteAccepted), LastObservationEnd: timeToUnixNano(route.LastObservationEnd)})
	}
	for _, client := range value.ClientVariants {
		w.ClientVariants = append(w.ClientVariants, wireClientVariant(client))
	}
	for _, issued := range value.TokenIssuedAts {
		w.TokenIssuedAts = append(w.TokenIssuedAts, timeToUnixNano(issued))
	}
	for _, conflict := range value.IdentityConflicts {
		w.IdentityConflicts = append(w.IdentityConflicts, wireIdentityConflict{Field: conflict.Field, Existing: conflict.Existing, Offered: conflict.Offered, ObservedAt: timeToUnixNano(conflict.ObservedAt)})
	}
	return json.Marshal(w)
}

func decodeSession(data []byte) (SessionView, error) {
	var w wireSession
	if err := json.Unmarshal(data, &w); err != nil {
		return SessionView{}, err
	}
	if err := checkVersion(w.V); err != nil {
		return SessionView{}, err
	}
	if err := validateSessionWire(w); err != nil {
		return SessionView{}, err
	}
	v := SessionView{Subject: Subject{Kind: w.Subject.Kind, ID: w.Subject.ID}, ProfileID: w.ProfileID, SessionID: w.SessionID,
		MediaFileID: w.MediaFileID, PlayMethod: w.PlayMethod, MediaFileIDs: w.MediaFileIDs, MediaFileIDsOverflowed: w.MediaFileIDsOverflowed,
		PlayMethods: w.PlayMethods, PlayMethodsOverflowed: w.PlayMethodsOverflowed, StartedAt: timeFromUnixNano(w.StartedAt), StartedAtSource: w.StartedAtSource,
		StartedAtDegraded: w.StartedAtDegraded, BytesAccepted: w.BytesAccepted, LastByteAccepted: timeFromUnixNano(w.LastByteAccepted), LastObservationEnd: timeFromUnixNano(w.LastObservationEnd),
		OpenObservations: w.OpenObservations, RealtimeConnectionAlive: w.RealtimeConnectionAlive, RequestCount: w.RequestCount, RoutesOverflowed: w.RoutesOverflowed,
		ViewerIPs: w.ViewerIPs, ViewerIPsOverflowed: w.ViewerIPsOverflowed, DeviceIDs: w.DeviceIDs, DeviceIDsOverflowed: w.DeviceIDsOverflowed,
		UserAgents: w.UserAgents, UserAgentsOverflowed: w.UserAgentsOverflowed, TokenIssuedAtsOverflowed: w.TokenIssuedAtsOverflowed,
		TokenIssuedAtSources: w.TokenIssuedAtSources, Outcomes: w.Outcomes, HasIdentityConflict: w.HasIdentityConflict,
		IdentityConflictsOverflowed: w.IdentityConflictsOverflowed, ClientVariantsOverflowed: w.ClientVariantsOverflowed}
	for _, route := range w.Routes {
		v.Routes = append(v.Routes, RouteActivityView{Method: route.Method, Pattern: route.Pattern, Role: route.Role, Class: route.Class, CapRelevant: route.CapRelevant, Open: route.Open, Requests: route.Requests, BytesAccepted: route.BytesAccepted, LastByteAccepted: timeFromUnixNano(route.LastByteAccepted), LastObservationEnd: timeFromUnixNano(route.LastObservationEnd)})
	}
	for _, client := range w.ClientVariants {
		v.ClientVariants = append(v.ClientVariants, ClientVariant(client))
	}
	for _, issued := range w.TokenIssuedAts {
		v.TokenIssuedAts = append(v.TokenIssuedAts, timeFromUnixNano(issued))
	}
	for _, conflict := range w.IdentityConflicts {
		v.IdentityConflicts = append(v.IdentityConflicts, IdentityConflict{Field: conflict.Field, Existing: conflict.Existing, Offered: conflict.Offered, ObservedAt: timeFromUnixNano(conflict.ObservedAt)})
	}
	return v, nil
}

func validateSessionWire(w wireSession) error {
	if w.MediaFileID < 0 || w.BytesAccepted < 0 || w.OpenObservations < 0 || w.RequestCount < 0 {
		return errors.New("negative session counter")
	}
	lengths := []int{len(w.MediaFileIDs), len(w.PlayMethods), len(w.Routes), len(w.ViewerIPs), len(w.DeviceIDs), len(w.ClientVariants), len(w.UserAgents), len(w.TokenIssuedAts), len(w.IdentityConflicts)}
	for _, length := range lengths {
		if length > maxWireSlice {
			return errors.New("session collection too large")
		}
	}
	if len(w.TokenIssuedAtSources) > maxWireMap || len(w.Outcomes) > maxWireMap {
		return errors.New("session map too large")
	}
	for _, value := range w.MediaFileIDs {
		if value < 0 {
			return errors.New("negative media file id")
		}
	}
	for _, route := range w.Routes {
		if route.Open < 0 || route.Requests < 0 || route.BytesAccepted < 0 {
			return errors.New("negative route counter")
		}
	}
	for _, value := range w.TokenIssuedAtSources {
		if value < 0 {
			return errors.New("negative token source counter")
		}
	}
	for _, value := range w.Outcomes {
		if value < 0 {
			return errors.New("negative outcome counter")
		}
	}
	return nil
}

func encodeTransfer(value TransferView) ([]byte, error) {
	w := wireTransfer{V: codecVersion, ID: value.ID, Subject: wireSubject{Kind: value.Subject.Kind, ID: value.Subject.ID}, ProfileID: value.ProfileID, MediaFileID: value.MediaFileID,
		Method: value.Method, Pattern: value.Pattern, Role: value.Role, BytesAccepted: value.BytesAccepted, LastByteAccepted: timeToUnixNano(value.LastByteAccepted), LastObservationEnd: timeToUnixNano(value.LastObservationEnd),
		OpenObservations: value.OpenObservations, RequestCount: value.RequestCount, ViewerIP: value.ViewerIP, DeviceID: value.DeviceID,
		Client: wireClientVariant(value.Client), UserAgent: value.UserAgent, Outcomes: value.Outcomes}
	return json.Marshal(w)
}

func decodeTransfer(data []byte) (TransferView, error) {
	var w wireTransfer
	if err := json.Unmarshal(data, &w); err != nil {
		return TransferView{}, err
	}
	if err := checkVersion(w.V); err != nil {
		return TransferView{}, err
	}
	if w.MediaFileID < 0 || w.BytesAccepted < 0 || w.OpenObservations < 0 || w.RequestCount < 0 {
		return TransferView{}, errors.New("negative transfer counter")
	}
	if len(w.Outcomes) > maxWireMap {
		return TransferView{}, errors.New("transfer map too large")
	}
	for _, value := range w.Outcomes {
		if value < 0 {
			return TransferView{}, errors.New("negative outcome counter")
		}
	}
	return TransferView{ID: w.ID, Subject: Subject{Kind: w.Subject.Kind, ID: w.Subject.ID}, ProfileID: w.ProfileID, MediaFileID: w.MediaFileID, Method: w.Method, Pattern: w.Pattern, Role: w.Role,
		BytesAccepted: w.BytesAccepted, LastByteAccepted: timeFromUnixNano(w.LastByteAccepted), LastObservationEnd: timeFromUnixNano(w.LastObservationEnd), OpenObservations: w.OpenObservations,
		RequestCount: w.RequestCount, ViewerIP: w.ViewerIP, DeviceID: w.DeviceID, Client: ClientVariant(w.Client), UserAgent: w.UserAgent, Outcomes: w.Outcomes}, nil
}

func encodeMeta(value publisherMeta) ([]byte, error) {
	value.V = codecVersion
	return json.Marshal(value)
}

func decodeMeta(data []byte) (publisherMeta, error) {
	var value publisherMeta
	if err := json.Unmarshal(data, &value); err != nil {
		return publisherMeta{}, err
	}
	if err := checkVersion(value.V); err != nil {
		return publisherMeta{}, err
	}
	if value.DroppedObservations < 0 || value.DroppedBytes < 0 || value.UnattributedObservations < 0 || value.UnattributedBytes < 0 || value.SessionCount < 0 || value.TransferCount < 0 {
		return publisherMeta{}, errors.New("negative publisher metadata counter")
	}
	if value.SessionCount > maxWireSlice || value.TransferCount > maxWireSlice {
		return publisherMeta{}, errors.New("publisher count too large")
	}
	return value, nil
}
