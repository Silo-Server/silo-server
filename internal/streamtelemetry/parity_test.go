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
