package streamtelemetry

import (
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

type routeActivity struct {
	Method             string
	Pattern            string
	Role               Role
	Class              Class
	CapRelevant        bool
	Open               int
	Requests           int64
	BytesFolded        int64
	LastSweptBytes     int64
	LastByteAccepted   time.Time
	LastObservationEnd time.Time
}

type boundedSet[T comparable] struct {
	values     map[T]struct{}
	max        int
	overflowed bool
}

func newBoundedSet[T comparable](max int) boundedSet[T] {
	return boundedSet[T]{values: make(map[T]struct{}), max: max}
}

func (s *boundedSet[T]) add(value T) {
	if _, ok := s.values[value]; ok {
		return
	}
	if len(s.values) >= s.max {
		s.overflowed = true
		return
	}
	s.values[value] = struct{}{}
}

type logicalSession struct {
	mu sync.Mutex

	subject         Subject
	profileID       string
	sessionID       string
	mediaFileID     int
	playMethod      string
	startedAt       time.Time
	startedAtSource StartedAtSource
	startedDegraded bool

	bytesFolded         int64
	lastSweptBytes      int64
	lastByteAccepted    time.Time
	lastObservationEnd  time.Time
	openObservations    int
	realtimeAlive       bool
	requestCount        int64
	hasIdentityConflict bool
	identityConflicts   []IdentityConflict
	identityOverflowed  bool
	routes              map[string]*routeActivity
	routesOverflowed    bool
	observations        map[string]*Observation
	viewerIPs           boundedSet[string]
	deviceIDs           boundedSet[string]
	clientVariants      boundedSet[ClientVariant]
	userAgents          boundedSet[string]
	mediaFileIDs        boundedSet[int]
	playMethods         boundedSet[string]
	tokenIssuedAts      boundedSet[int64]
	tokenIssuedSources  map[TokenIssuedAtSource]int64
	outcomes            map[httpstream.StreamOutcome]int64
}

type transfer struct {
	mu sync.Mutex

	id                 string
	subject            Subject
	profileID          string
	mediaFileID        int
	bytesFolded        int64
	lastSweptBytes     int64
	lastByteAccepted   time.Time
	lastObservationEnd time.Time
	openObservations   int
	requestCount       int64
	route              MediaRoute
	capture            CaptureSet
	// observations holds every in-flight request folded into this transfer.
	// Ranged byte routes (audiobook file reads, download resumes, ebook page
	// fetches) issue many overlapping small GETs for the same file, so a
	// transfer is a subject pouring one file over one route, not one request —
	// which is what requestCount has always claimed to count.
	observations map[string]*Observation
	outcomes     map[httpstream.StreamOutcome]int64
}

func newLogicalSession(a Attachment, cfg Config, observedAt time.Time) *logicalSession {
	startedAt, source, degraded := normalizeStartedAt(a.StartedAt, a.StartedAtSource, observedAt)
	session := &logicalSession{
		subject: a.Subject, profileID: a.ProfileID, sessionID: a.SessionID,
		mediaFileID: a.MediaFileID, playMethod: a.PlayMethod,
		startedAt: startedAt, startedAtSource: source, startedDegraded: degraded,
		routes: make(map[string]*routeActivity), observations: make(map[string]*Observation),
		viewerIPs:          newBoundedSet[string](cfg.MaxViewerIPsPerSession),
		deviceIDs:          newBoundedSet[string](cfg.MaxDeviceIDsPerSession),
		clientVariants:     newBoundedSet[ClientVariant](cfg.MaxClientVariantsPerSession),
		userAgents:         newBoundedSet[string](cfg.MaxClientVariantsPerSession),
		mediaFileIDs:       newBoundedSet[int](cfg.MaxMediaFileIDsPerSession),
		playMethods:        newBoundedSet[string](cfg.MaxPlayMethodsPerSession),
		tokenIssuedAts:     newBoundedSet[int64](cfg.MaxTokenIssuedAtPerSession),
		tokenIssuedSources: make(map[TokenIssuedAtSource]int64),
		outcomes:           make(map[httpstream.StreamOutcome]int64),
	}
	if a.MediaFileID != 0 {
		session.mediaFileIDs.add(a.MediaFileID)
	}
	if a.PlayMethod != "" {
		session.playMethods.add(a.PlayMethod)
	}
	return session
}

func normalizeStartedAt(value time.Time, source StartedAtSource, observedAt time.Time) (time.Time, StartedAtSource, bool) {
	if value.IsZero() || startedAtRank(source) == 0 {
		return observedAt, StartedAtSourceFirstSeen, true
	}
	return value, source, source == StartedAtSourceIssuedAt || source == StartedAtSourceFirstSeen
}

func startedAtRank(source StartedAtSource) int {
	switch source {
	case StartedAtSourceClaim:
		return 4
	case StartedAtSourceSession:
		return 3
	case StartedAtSourceIssuedAt:
		return 2
	case StartedAtSourceFirstSeen:
		return 1
	default:
		return 0
	}
}

func routeID(method, pattern string) string { return method + "\x00" + pattern }

func (s *logicalSession) recordConflicts(a Attachment, observedAt time.Time, max int) {
	checks := []struct{ field, existing, offered string }{
		{"subject.kind", string(s.subject.Kind), string(a.Subject.Kind)},
		{"subject.id", s.subject.ID, a.Subject.ID},
		{identityFieldProfileID, s.profileID, a.ProfileID},
	}
	for _, check := range checks {
		if check.existing == "" || check.offered == "" || check.existing == check.offered {
			continue
		}
		s.hasIdentityConflict = true
		if len(s.identityConflicts) >= max {
			s.identityOverflowed = true
			continue
		}
		s.identityConflicts = append(s.identityConflicts, IdentityConflict{
			Field: check.field, Existing: check.existing, Offered: check.offered, ObservedAt: observedAt,
		})
	}
	if a.MediaFileID != 0 {
		s.mediaFileID = a.MediaFileID
		s.mediaFileIDs.add(a.MediaFileID)
	}
	if a.PlayMethod != "" {
		s.playMethod = a.PlayMethod
		s.playMethods.add(a.PlayMethod)
	}
	if rank := startedAtRank(a.StartedAtSource); !a.StartedAt.IsZero() && rank > startedAtRank(s.startedAtSource) {
		previous := s.startedAt
		s.startedAt = a.StartedAt
		s.startedAtSource = a.StartedAtSource
		s.startedDegraded = a.StartedAtSource == StartedAtSourceIssuedAt || a.StartedAtSource == StartedAtSourceFirstSeen
		// Only a change of VALUE is a conflict. A pure authority upgrade that
		// confirms the instant already recorded — the common proxy-then-claim
		// case — is not one, and recording it consumed the per-session conflict
		// budget and could set IdentityConflictsOverflowed for nothing. When the
		// value does move, two sources genuinely disagree about when playback
		// began, so hasIdentityConflict is set alongside the entry: consumers
		// filtering on the flag and consumers reading the list must not
		// disagree about whether a session is conflicted.
		if !previous.Equal(a.StartedAt) {
			s.hasIdentityConflict = true
			if len(s.identityConflicts) < max {
				s.identityConflicts = append(s.identityConflicts, IdentityConflict{
					Field:    "started_at_replaced",
					Existing: previous.Format(time.RFC3339Nano),
					Offered:  a.StartedAt.Format(time.RFC3339Nano), ObservedAt: observedAt,
				})
			} else {
				s.identityOverflowed = true
			}
		}
	}
}
