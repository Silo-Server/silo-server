package streamtelemetry

import (
	"testing"
	"time"
)

func liveSession(id string, mutate ...func(*LiveSession)) LiveSession {
	session := LiveSession{
		SessionID: id, Subject: UserSubject(7), ProfileID: "profile-1",
		MediaFileID: 42, PlayMethod: "direct", Node: "node-a",
		StartedAt: time.Unix(1_700_000_000, 0),
	}
	for _, apply := range mutate {
		apply(&session)
	}
	return session
}

func TestCompareLiveSessionsAgreesOnIdenticalSets(t *testing.T) {
	sessions := []LiveSession{liveSession("a"), liveSession("b")}
	report := CompareLiveSessions("legacy", sessions, sessions, 0)
	if !report.Agrees {
		t.Fatalf("identical sets disagreed: %+v", report)
	}
	if report.InBoth != 2 || report.TelemetryCount != 2 || report.LegacyCount != 2 {
		t.Fatalf("counts = %+v", report)
	}
	if len(report.Mismatches) != 0 || len(report.TelemetryOnly) != 0 || len(report.LegacyOnly) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestCompareLiveSessionsReportsOnlySides(t *testing.T) {
	telemetry := []LiveSession{liveSession("a"), liveSession("only-telemetry")}
	legacy := []LiveSession{liveSession("a"), liveSession("only-legacy")}
	report := CompareLiveSessions("legacy", telemetry, legacy, 0)
	if report.Agrees {
		t.Fatal("differing sets agreed")
	}
	if len(report.TelemetryOnly) != 1 || report.TelemetryOnly[0] != "only-telemetry" {
		t.Fatalf("telemetry only = %+v", report.TelemetryOnly)
	}
	if len(report.LegacyOnly) != 1 || report.LegacyOnly[0] != "only-legacy" {
		t.Fatalf("legacy only = %+v", report.LegacyOnly)
	}
	if report.InBoth != 1 {
		t.Fatalf("in both = %d", report.InBoth)
	}
}

func TestCompareLiveSessionsFieldRules(t *testing.T) {
	t.Run("subject disagreement is a mismatch", func(t *testing.T) {
		telemetry := []LiveSession{liveSession("a")}
		legacy := []LiveSession{liveSession("a", func(s *LiveSession) { s.Subject = UserSubject(9) })}
		report := CompareLiveSessions("legacy", telemetry, legacy, 0)
		if len(report.Mismatches) != 1 || report.Mismatches[0].Field != "subject" {
			t.Fatalf("mismatches = %+v", report.Mismatches)
		}
		if report.Mismatches[0].Telemetry != "user:7" || report.Mismatches[0].Legacy != "user:9" {
			t.Fatalf("mismatch values = %+v", report.Mismatches[0])
		}
	})

	// A value only one projection carries is a gap in that projection, not a
	// disagreement between them. Counting it as a mismatch would bury the real
	// ones under every field the older projection never populated.
	t.Run("absence is not a mismatch", func(t *testing.T) {
		telemetry := []LiveSession{liveSession("a")}
		legacy := []LiveSession{liveSession("a", func(s *LiveSession) {
			s.ProfileID = ""
			s.MediaFileID = 0
			s.Node = ""
		})}
		report := CompareLiveSessions("legacy", telemetry, legacy, 0)
		if len(report.Mismatches) != 0 {
			t.Fatalf("absence produced mismatches: %+v", report.Mismatches)
		}
		// Agrees covers set membership and real contradiction only. Folding
		// absences in would make it permanently false, since legacy rows carry
		// no value for several of these fields, and therefore useless — so the
		// absences are reported on their own axis instead.
		if !report.Agrees {
			t.Fatalf("absence alone was reported as disagreement: %+v", report)
		}
		for _, field := range []string{"profile_id", "media_file_id", "node"} {
			if report.FieldsAbsent[field] != 1 {
				t.Fatalf("fields absent = %+v", report.FieldsAbsent)
			}
		}
	})

	// Two independent writers stamping the same session cannot be expected to
	// agree to the nanosecond.
	t.Run("sub-second start skew is tolerated", func(t *testing.T) {
		telemetry := []LiveSession{liveSession("a")}
		legacy := []LiveSession{liveSession("a", func(s *LiveSession) {
			s.StartedAt = s.StartedAt.Add(900 * time.Millisecond)
		})}
		if report := CompareLiveSessions("legacy", telemetry, legacy, 0); !report.Agrees {
			t.Fatalf("900ms of start skew was reported as a mismatch: %+v", report.Mismatches)
		}
	})

	t.Run("multi-second start skew is a mismatch", func(t *testing.T) {
		telemetry := []LiveSession{liveSession("a")}
		legacy := []LiveSession{liveSession("a", func(s *LiveSession) {
			s.StartedAt = s.StartedAt.Add(-5 * time.Second)
		})}
		report := CompareLiveSessions("legacy", telemetry, legacy, 0)
		if len(report.Mismatches) != 1 || report.Mismatches[0].Field != "started_at" {
			t.Fatalf("mismatches = %+v", report.Mismatches)
		}
	})
}

// Truncation must be visible. A capped list with no count would read as
// "covered everything".
func TestCompareLiveSessionsCapsEveryList(t *testing.T) {
	telemetry := make([]LiveSession, 0, 10)
	legacy := make([]LiveSession, 0, 10)
	for i := 0; i < 10; i++ {
		telemetry = append(telemetry, liveSession(string(rune('a'+i))+"-telemetry"))
		legacy = append(legacy, liveSession(string(rune('a'+i))+"-legacy"))
	}
	// Sessions present in both, disagreeing on subject, to exercise the mismatch cap.
	for i := 0; i < 10; i++ {
		id := "shared-" + string(rune('a'+i))
		telemetry = append(telemetry, liveSession(id))
		legacy = append(legacy, liveSession(id, func(s *LiveSession) { s.Subject = UserSubject(99) }))
	}

	report := CompareLiveSessions("legacy", telemetry, legacy, 3)
	if len(report.TelemetryOnly) != 3 || report.TelemetryMore != 7 {
		t.Fatalf("telemetry only = %d (+%d)", len(report.TelemetryOnly), report.TelemetryMore)
	}
	if len(report.LegacyOnly) != 3 || report.LegacyMore != 7 {
		t.Fatalf("legacy only = %d (+%d)", len(report.LegacyOnly), report.LegacyMore)
	}
	if len(report.Mismatches) != 3 || report.MismatchesMore != 7 {
		t.Fatalf("mismatches = %d (+%d)", len(report.Mismatches), report.MismatchesMore)
	}
}

func TestLiveSessionsFromGlobalView(t *testing.T) {
	view := GlobalMonitoringView{Sessions: []GlobalSessionView{
		{
			SessionID: "b", Subject: UserSubject(7), ProfileID: "profile-1", MediaFileID: 42,
			StartedAt:   time.Unix(1_700_000_000, 0),
			PlayMethods: []string{"direct"},
			ViewerEdgePublishers: []PublisherRef{
				{PublisherID: "p1", NodeID: ""},
				{PublisherID: "p2", NodeID: "node-b"},
			},
		},
		{
			SessionID: "a", Subject: UserSubject(8),
			// Two publishers disagreed about the play method, so §2.5 leaves the
			// merged scalar unset and the projection must not invent one.
			PlayMethods: []string{"direct", "transcode"},
			// Relay-only: no viewer edge, so this session claims no node.
			Publishers: []PublisherRef{{PublisherID: "node", NodeID: "node-c"}},
		},
	}}

	sessions := LiveSessionsFromGlobalView(view)
	if len(sessions) != 2 || sessions[0].SessionID != "a" || sessions[1].SessionID != "b" {
		t.Fatalf("projection not sorted by session id: %+v", sessions)
	}
	if sessions[1].Node != "node-b" {
		t.Fatalf("node = %q, want the first viewer-edge publisher with a node id", sessions[1].Node)
	}
	if sessions[1].PlayMethod != "direct" {
		t.Fatalf("play method = %q", sessions[1].PlayMethod)
	}
	if sessions[0].PlayMethod != "" {
		t.Fatalf("a disputed play method was rendered as %q; §2.5 forbids picking one", sessions[0].PlayMethod)
	}
	if sessions[0].Node != "" {
		t.Fatalf("a relay-only session claimed node %q", sessions[0].Node)
	}
}
