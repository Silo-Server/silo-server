package streamtelemetry

import (
	"sort"
	"strconv"
	"time"
)

// DefaultParityLimit bounds every list in a ParityReport. An admin endpoint that
// returned 50 000 session ids would be its own outage.
const DefaultParityLimit = 50

// parityStartedAtTolerance is how far apart two projections' start times may be
// before it counts as a disagreement.
//
// Postgres stores a timestamp and telemetry keeps nanoseconds, and the two are
// written by independent processes, so sub-second skew is normal rather than a
// parity failure. It is also below the resolution any consumer acts on: the
// design's victim ordering is (startedAtUnixNano, sessionID), which only has to
// be a total order, not agree across stores.
const parityStartedAtTolerance = time.Second

// LiveSession is one live streaming session reduced to the fields every
// projection can express.
//
// It is deliberately small. Telemetry has no media title, poster or playback
// position, and the legacy projections have no byte counts or viewer-edge
// publisher. Comparing a field only one side can express would manufacture
// mismatches and bury the real ones.
type LiveSession struct {
	SessionID   string
	Subject     Subject
	ProfileID   string
	MediaFileID int
	PlayMethod  string
	Node        string
	StartedAt   time.Time
}

// LiveSessionsFromGlobalView projects the merged view onto the comparable core,
// sorted by session id.
//
// Node comes from the session's viewer-edge publisher only: a session a node
// merely relayed is not a session that node served a viewer from, and claiming
// otherwise would disagree with the legacy projections for the wrong reason.
func LiveSessionsFromGlobalView(view GlobalMonitoringView) []LiveSession {
	sessions := make([]LiveSession, 0, len(view.Sessions))
	for _, session := range view.Sessions {
		live := LiveSession{
			SessionID: session.SessionID, Subject: session.Subject, ProfileID: session.ProfileID,
			MediaFileID: session.MediaFileID, StartedAt: session.StartedAt,
		}
		if len(session.PlayMethods) == 1 {
			// A merged scalar play method is deliberately absent when publishers
			// disagree (§2.5); a single unioned value is the only unambiguous one.
			live.PlayMethod = session.PlayMethods[0]
		}
		for _, publisher := range session.ViewerEdgePublishers {
			if publisher.NodeID != "" {
				live.Node = publisher.NodeID
				break
			}
		}
		sessions = append(sessions, live)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].SessionID < sessions[j].SessionID })
	return sessions
}

// ParityMismatch is one field two projections disagree about for a session they
// both know.
type ParityMismatch struct {
	SessionID string `json:"session_id"`
	Field     string `json:"field"`
	Telemetry string `json:"telemetry"`
	Legacy    string `json:"legacy"`
}

// ParityReport is the diff between the telemetry projection and one legacy
// projection. Every list is capped, with an explicit count of what the cap
// dropped: silent truncation would read as "covered everything".
type ParityReport struct {
	Source         string `json:"source"`
	TelemetryCount int    `json:"telemetry_count"`
	LegacyCount    int    `json:"legacy_count"`
	InBoth         int    `json:"in_both"`
	// Agrees means the two projections describe the same set of sessions and
	// disagree on no field they both express. It deliberately does NOT account
	// for FieldsAbsent: a field only one side carries is a different question
	// from a field they contradict each other about, and folding the two
	// together would make this flag permanently false — legacy rows carry no
	// value for several of these — and therefore useless. Read FieldsAbsent as
	// well before treating agreement as clearance to cut over.
	Agrees         bool             `json:"agrees"`
	TelemetryOnly  []string         `json:"telemetry_only"`
	TelemetryMore  int              `json:"telemetry_only_truncated"`
	LegacyOnly     []string         `json:"legacy_only"`
	LegacyMore     int              `json:"legacy_only_truncated"`
	Mismatches     []ParityMismatch `json:"mismatches"`
	MismatchesMore int              `json:"mismatches_truncated"`
	// FieldsAbsent counts, per field, sessions both projections know where one
	// side carries no value at all. That is a gap in a projection, not a
	// disagreement between them, and counting it as a mismatch would bury the
	// real ones.
	FieldsAbsent map[string]int `json:"fields_absent,omitempty"`
}

// CompareLiveSessions diffs two projections of the same live activity, keyed by
// session id. It is a pure function — no clock, no store, no logger — so every
// rule below is unit-testable in CI on a machine with neither Postgres nor
// Redis, which is the same reason BuildGlobalView is pure.
func CompareLiveSessions(source string, telemetry, legacy []LiveSession, limit int) ParityReport {
	if limit <= 0 {
		limit = DefaultParityLimit
	}
	report := ParityReport{
		Source: source, TelemetryCount: len(telemetry), LegacyCount: len(legacy),
		TelemetryOnly: []string{}, LegacyOnly: []string{}, Mismatches: []ParityMismatch{},
		FieldsAbsent: map[string]int{},
	}

	legacyByID := make(map[string]LiveSession, len(legacy))
	for _, session := range legacy {
		legacyByID[session.SessionID] = session
	}
	telemetryByID := make(map[string]LiveSession, len(telemetry))
	for _, session := range telemetry {
		telemetryByID[session.SessionID] = session
	}

	telemetryOnly := make([]string, 0)
	mismatches := make([]ParityMismatch, 0)
	for _, session := range telemetry {
		counterpart, ok := legacyByID[session.SessionID]
		if !ok {
			telemetryOnly = append(telemetryOnly, session.SessionID)
			continue
		}
		report.InBoth++
		mismatches = append(mismatches, compareSession(session, counterpart, report.FieldsAbsent)...)
	}
	legacyOnly := make([]string, 0)
	for _, session := range legacy {
		if _, ok := telemetryByID[session.SessionID]; !ok {
			legacyOnly = append(legacyOnly, session.SessionID)
		}
	}

	sort.Strings(telemetryOnly)
	sort.Strings(legacyOnly)
	sort.Slice(mismatches, func(i, j int) bool {
		if mismatches[i].SessionID == mismatches[j].SessionID {
			return mismatches[i].Field < mismatches[j].Field
		}
		return mismatches[i].SessionID < mismatches[j].SessionID
	})

	report.Agrees = len(telemetryOnly) == 0 && len(legacyOnly) == 0 && len(mismatches) == 0
	report.TelemetryOnly, report.TelemetryMore = capStrings(telemetryOnly, limit)
	report.LegacyOnly, report.LegacyMore = capStrings(legacyOnly, limit)
	if len(mismatches) > limit {
		report.MismatchesMore = len(mismatches) - limit
		mismatches = mismatches[:limit]
	}
	report.Mismatches = mismatches
	if len(report.FieldsAbsent) == 0 {
		report.FieldsAbsent = nil
	}
	return report
}

func compareSession(telemetry, legacy LiveSession, absent map[string]int) []ParityMismatch {
	mismatches := make([]ParityMismatch, 0, 4)
	add := func(field, left, right string) {
		// Only a field both sides carry can disagree.
		if left == "" || right == "" {
			if left != right {
				absent[field]++
			}
			return
		}
		if left != right {
			mismatches = append(mismatches, ParityMismatch{
				SessionID: telemetry.SessionID, Field: field, Telemetry: left, Legacy: right,
			})
		}
	}
	add(identityFieldSubject, subjectKey(telemetry.Subject), subjectKey(legacy.Subject))
	add(identityFieldProfileID, telemetry.ProfileID, legacy.ProfileID)
	add(identityFieldMediaFileID, positiveInt(telemetry.MediaFileID), positiveInt(legacy.MediaFileID))
	add("play_method", telemetry.PlayMethod, legacy.PlayMethod)
	add("node", telemetry.Node, legacy.Node)

	switch {
	case telemetry.StartedAt.IsZero() || legacy.StartedAt.IsZero():
		if telemetry.StartedAt.IsZero() != legacy.StartedAt.IsZero() {
			absent["started_at"]++
		}
	default:
		if delta := telemetry.StartedAt.Sub(legacy.StartedAt); delta > parityStartedAtTolerance || delta < -parityStartedAtTolerance {
			mismatches = append(mismatches, ParityMismatch{
				SessionID: telemetry.SessionID, Field: "started_at",
				Telemetry: telemetry.StartedAt.UTC().Format(time.RFC3339Nano),
				Legacy:    legacy.StartedAt.UTC().Format(time.RFC3339Nano),
			})
		}
	}
	return mismatches
}

func subjectKey(subject Subject) string {
	if subject.Kind == "" || subject.ID == "" {
		return ""
	}
	return string(subject.Kind) + ":" + subject.ID
}

func positiveInt(value int) string {
	if value <= 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func capStrings(values []string, limit int) ([]string, int) {
	if len(values) <= limit {
		return values, 0
	}
	return values[:limit], len(values) - limit
}
