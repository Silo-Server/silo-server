package streamtelemetry

import (
	"sort"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

type RouteActivityView struct {
	Method             string
	Pattern            string
	Role               Role
	Class              Class
	CapRelevant        bool
	Open               int
	Requests           int64
	BytesAccepted      int64
	LastByteAccepted   time.Time
	LastObservationEnd time.Time
}

type SessionView struct {
	Subject                     Subject
	ProfileID                   string
	SessionID                   string
	MediaFileID                 int
	PlayMethod                  string
	MediaFileIDs                []int
	MediaFileIDsOverflowed      bool
	PlayMethods                 []string
	PlayMethodsOverflowed       bool
	StartedAt                   time.Time
	StartedAtSource             StartedAtSource
	StartedAtDegraded           bool
	BytesAccepted               int64 // pre-compression at the enrollment point; see package documentation.
	LastByteAccepted            time.Time
	LastObservationEnd          time.Time
	OpenObservations            int
	RealtimeConnectionAlive     bool
	RequestCount                int64
	Routes                      []RouteActivityView
	RoutesOverflowed            bool
	ViewerIPs                   []string
	ViewerIPsOverflowed         bool
	DeviceIDs                   []string
	DeviceIDsOverflowed         bool
	ClientVariants              []ClientVariant
	ClientVariantsOverflowed    bool
	UserAgents                  []string
	UserAgentsOverflowed        bool
	TokenIssuedAts              []time.Time
	TokenIssuedAtsOverflowed    bool
	TokenIssuedAtSources        map[TokenIssuedAtSource]int64
	Outcomes                    map[httpstream.StreamOutcome]int64
	HasIdentityConflict         bool
	IdentityConflicts           []IdentityConflict
	IdentityConflictsOverflowed bool
}

type TransferView struct {
	ID                 string
	Subject            Subject
	ProfileID          string
	MediaFileID        int
	Method             string
	Pattern            string
	Role               Role
	BytesAccepted      int64
	LastByteAccepted   time.Time
	LastObservationEnd time.Time
	OpenObservations   int
	RequestCount       int64
	ViewerIP           string
	DeviceID           string
	Client             ClientVariant
	UserAgent          string
	Outcomes           map[httpstream.StreamOutcome]int64
}

type Snapshot struct {
	PublisherID              string
	NodeID                   string
	PublisherEpoch           int64
	Sequence                 uint64
	CapturedAt               time.Time
	Sessions                 []SessionView
	Transfers                []TransferView
	Truncated                bool
	DroppedObservations      int64
	DroppedBytes             int64
	UnattributedObservations int64
	UnattributedBytes        int64
}

func sessionViewOf(s *logicalSession) SessionView {
	view := SessionView{
		Subject: s.subject, ProfileID: s.profileID, SessionID: s.sessionID,
		MediaFileID: s.mediaFileID, PlayMethod: s.playMethod,
		MediaFileIDsOverflowed: s.mediaFileIDs.overflowed,
		PlayMethodsOverflowed:  s.playMethods.overflowed,
		StartedAt:              s.startedAt, StartedAtSource: s.startedAtSource, StartedAtDegraded: s.startedDegraded,
		BytesAccepted: s.lastSweptBytes, LastByteAccepted: s.lastByteAccepted,
		LastObservationEnd: s.lastObservationEnd, OpenObservations: s.openObservations,
		RealtimeConnectionAlive: s.realtimeAlive, RequestCount: s.requestCount,
		RoutesOverflowed: s.routesOverflowed, ViewerIPsOverflowed: s.viewerIPs.overflowed,
		DeviceIDsOverflowed: s.deviceIDs.overflowed, ClientVariantsOverflowed: s.clientVariants.overflowed,
		UserAgentsOverflowed: s.userAgents.overflowed, TokenIssuedAtsOverflowed: s.tokenIssuedAts.overflowed,
		TokenIssuedAtSources: cloneTokenSources(s.tokenIssuedSources),
		Outcomes:             cloneOutcomes(s.outcomes), HasIdentityConflict: s.hasIdentityConflict,
		IdentityConflicts:           append([]IdentityConflict(nil), s.identityConflicts...),
		IdentityConflictsOverflowed: s.identityOverflowed,
	}
	for _, route := range s.routes {
		view.Routes = append(view.Routes, RouteActivityView{Method: route.Method, Pattern: route.Pattern,
			Role: route.Role, Class: route.Class, CapRelevant: route.CapRelevant, Open: route.Open,
			Requests: route.Requests, BytesAccepted: route.LastSweptBytes,
			LastByteAccepted: route.LastByteAccepted, LastObservationEnd: route.LastObservationEnd})
	}
	for value := range s.viewerIPs.values {
		view.ViewerIPs = append(view.ViewerIPs, value)
	}
	for value := range s.deviceIDs.values {
		view.DeviceIDs = append(view.DeviceIDs, value)
	}
	for value := range s.clientVariants.values {
		view.ClientVariants = append(view.ClientVariants, value)
	}
	for value := range s.userAgents.values {
		view.UserAgents = append(view.UserAgents, value)
	}
	for value := range s.mediaFileIDs.values {
		view.MediaFileIDs = append(view.MediaFileIDs, value)
	}
	for value := range s.playMethods.values {
		view.PlayMethods = append(view.PlayMethods, value)
	}
	for value := range s.tokenIssuedAts.values {
		view.TokenIssuedAts = append(view.TokenIssuedAts, time.Unix(0, value))
	}
	sort.Slice(view.Routes, func(i, j int) bool {
		return view.Routes[i].Method+view.Routes[i].Pattern < view.Routes[j].Method+view.Routes[j].Pattern
	})
	sort.Strings(view.ViewerIPs)
	sort.Strings(view.DeviceIDs)
	sort.Strings(view.UserAgents)
	sort.Ints(view.MediaFileIDs)
	sort.Strings(view.PlayMethods)
	sort.Slice(view.ClientVariants, func(i, j int) bool {
		return clientVariantKey(view.ClientVariants[i]) < clientVariantKey(view.ClientVariants[j])
	})
	sort.Slice(view.TokenIssuedAts, func(i, j int) bool { return view.TokenIssuedAts[i].Before(view.TokenIssuedAts[j]) })
	return view
}

func transferViewOf(t *transfer) TransferView {
	return TransferView{ID: t.id, Subject: t.subject, ProfileID: t.profileID, MediaFileID: t.mediaFileID,
		Method: t.capture.Method, Pattern: t.capture.Pattern, Role: t.route.Role,
		BytesAccepted: t.lastSweptBytes, LastByteAccepted: t.lastByteAccepted,
		LastObservationEnd: t.lastObservationEnd, OpenObservations: t.openObservations,
		RequestCount: t.requestCount, ViewerIP: t.capture.ViewerIP, DeviceID: t.capture.DeviceID,
		Client: t.capture.Client, UserAgent: t.capture.UserAgent, Outcomes: cloneOutcomes(t.outcomes)}
}

func cloneSnapshot(source Snapshot) Snapshot {
	destination := source
	destination.Sessions = make([]SessionView, len(source.Sessions))
	for i := range source.Sessions {
		destination.Sessions[i] = source.Sessions[i]
		destination.Sessions[i].Routes = append([]RouteActivityView(nil), source.Sessions[i].Routes...)
		destination.Sessions[i].ViewerIPs = append([]string(nil), source.Sessions[i].ViewerIPs...)
		destination.Sessions[i].DeviceIDs = append([]string(nil), source.Sessions[i].DeviceIDs...)
		destination.Sessions[i].ClientVariants = append([]ClientVariant(nil), source.Sessions[i].ClientVariants...)
		destination.Sessions[i].UserAgents = append([]string(nil), source.Sessions[i].UserAgents...)
		destination.Sessions[i].MediaFileIDs = append([]int(nil), source.Sessions[i].MediaFileIDs...)
		destination.Sessions[i].PlayMethods = append([]string(nil), source.Sessions[i].PlayMethods...)
		destination.Sessions[i].TokenIssuedAts = append([]time.Time(nil), source.Sessions[i].TokenIssuedAts...)
		destination.Sessions[i].IdentityConflicts = append([]IdentityConflict(nil), source.Sessions[i].IdentityConflicts...)
		destination.Sessions[i].Outcomes = cloneOutcomes(source.Sessions[i].Outcomes)
		destination.Sessions[i].TokenIssuedAtSources = cloneTokenSources(source.Sessions[i].TokenIssuedAtSources)
	}
	destination.Transfers = make([]TransferView, len(source.Transfers))
	for i := range source.Transfers {
		destination.Transfers[i] = source.Transfers[i]
		destination.Transfers[i].Outcomes = cloneOutcomes(source.Transfers[i].Outcomes)
	}
	return destination
}

func cloneTokenSources(source map[TokenIssuedAtSource]int64) map[TokenIssuedAtSource]int64 {
	destination := make(map[TokenIssuedAtSource]int64, len(source))
	for key, value := range source {
		destination[key] = value
	}
	return destination
}

func cloneOutcomes(source map[httpstream.StreamOutcome]int64) map[httpstream.StreamOutcome]int64 {
	destination := make(map[httpstream.StreamOutcome]int64, len(source))
	for key, value := range source {
		destination[key] = value
	}
	return destination
}

func clientVariantKey(value ClientVariant) string {
	return value.Name + "\x00" + value.Version + "\x00" + value.Build + "\x00" + value.Channel
}
