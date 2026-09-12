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

func healthy(sessions ...LiveSession) TelemetrySide {
	return TelemetrySide{Sessions: sessions, ViewComplete: true}
}

func legacySide(sessions ...LiveSession) LegacySide {
	return LegacySide{Sessions: sessions}
}

func TestCompareLiveSessionsAgreesOnIdenticalSets(t *testing.T) {
	sessions := []LiveSession{liveSession("a"), liveSession("b")}
	report := CompareLiveSessions("legacy", healthy(sessions...), legacySide(sessions...), 0)
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
	report := CompareLiveSessions("legacy", healthy(telemetry...), legacySide(legacy...), 0)
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
		report := CompareLiveSessions("legacy", healthy(telemetry...), legacySide(legacy...), 0)
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
		report := CompareLiveSessions("legacy", healthy(telemetry...), legacySide(legacy...), 0)
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
		if report := CompareLiveSessions("legacy", healthy(telemetry...), legacySide(legacy...), 0); !report.Agrees {
			t.Fatalf("900ms of start skew was reported as a mismatch: %+v", report.Mismatches)
		}
	})

	t.Run("multi-second start skew is a mismatch", func(t *testing.T) {
		telemetry := []LiveSession{liveSession("a")}
		legacy := []LiveSession{liveSession("a", func(s *LiveSession) {
			s.StartedAt = s.StartedAt.Add(-5 * time.Second)
		})}
		report := CompareLiveSessions("legacy", healthy(telemetry...), legacySide(legacy...), 0)
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

	report := CompareLiveSessions("legacy", healthy(telemetry...), legacySide(legacy...), 3)
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

func TestCompareLiveSessionsEvidenceGatesAgreement(t *testing.T) {
	t.Run("reported-only on both sides is not corroboration", func(t *testing.T) {
		telemetry := liveSession("ghost", func(session *LiveSession) { session.ReportedOnly = true })
		report := CompareLiveSessions("legacy", healthy(telemetry), legacySide(liveSession("ghost")), 0)
		if report.InBoth != 1 || report.InBothReportedOnly != 1 || report.InBothMeasured != 0 {
			t.Fatalf("evidence counts = %+v", report)
		}
		if !report.AgreementSelfDerived || report.Agrees {
			t.Fatalf("self-derived agreement was accepted: %+v", report)
		}
		if len(report.AgreesWithheld) != 1 || report.AgreesWithheld[0] != "agreement_self_derived" {
			t.Fatalf("withheld reasons = %+v", report.AgreesWithheld)
		}
		if len(report.Mismatches) != 0 {
			t.Fatalf("evidence veto manufactured mismatches: %+v", report.Mismatches)
		}
	})

	t.Run("measured session on both sides is corroboration", func(t *testing.T) {
		session := liveSession("measured")
		report := CompareLiveSessions("legacy", healthy(session), legacySide(session), 0)
		if report.InBothMeasured != 1 || report.InBothReportedOnly != 0 || report.AgreementSelfDerived || !report.Agrees {
			t.Fatalf("measured agreement = %+v", report)
		}
	})

	t.Run("reported-only fleet stays false", func(t *testing.T) {
		telemetry := make([]LiveSession, 0, 3)
		legacy := make([]LiveSession, 0, 3)
		for _, id := range []string{"a", "b", "c"} {
			telemetry = append(telemetry, liveSession(id, func(session *LiveSession) { session.ReportedOnly = true }))
			legacy = append(legacy, liveSession(id))
		}
		report := CompareLiveSessions("legacy", healthy(telemetry...), legacySide(legacy...), 0)
		if report.Agrees || !report.AgreementSelfDerived || report.InBothReportedOnly != 3 {
			t.Fatalf("reported-only fleet = %+v", report)
		}
	})

	t.Run("mixed fleet still agrees", func(t *testing.T) {
		measured := liveSession("measured")
		reported := liveSession("reported", func(session *LiveSession) { session.ReportedOnly = true })
		report := CompareLiveSessions(
			"legacy", healthy(measured, reported), legacySide(measured, liveSession("reported")), 0,
		)
		if !report.Agrees || report.AgreementSelfDerived || report.InBothMeasured != 1 || report.InBothReportedOnly != 1 {
			t.Fatalf("mixed agreement = %+v", report)
		}
		if len(report.InBothReportedOnlySessions) != 1 || report.InBothReportedOnlySessions[0] != "reported" {
			t.Fatalf("reported-only sessions = %+v", report.InBothReportedOnlySessions)
		}
	})
}

func TestCompareLiveSessionsReadHealthWithholdsAgreement(t *testing.T) {
	session := liveSession("shared")
	tests := []struct {
		name      string
		telemetry TelemetrySide
		legacy    LegacySide
		reason    string
	}{
		{name: "incomplete view", telemetry: TelemetrySide{Sessions: []LiveSession{session}}, legacy: legacySide(session), reason: "view_incomplete"},
		{name: "stale view", telemetry: TelemetrySide{Sessions: []LiveSession{session}, ViewComplete: true, ViewStale: true}, legacy: legacySide(session), reason: "view_stale"},
		{name: "truncated legacy read", telemetry: healthy(session), legacy: LegacySide{Sessions: []LiveSession{session}, Truncated: true}, reason: "legacy_truncated"},
		{name: "lossy legacy read", telemetry: healthy(session), legacy: LegacySide{Sessions: []LiveSession{session}, Lossy: true}, reason: "legacy_lossy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := CompareLiveSessions("legacy", test.telemetry, test.legacy, 0)
			if report.Agrees {
				t.Fatalf("unhealthy read agreed: %+v", report)
			}
			if len(report.AgreesWithheld) != 1 || report.AgreesWithheld[0] != test.reason {
				t.Fatalf("withheld reasons = %+v, want %q", report.AgreesWithheld, test.reason)
			}
		})
	}
}

func TestCompareLiveSessionsSortsAndCapsReportedOnlySessions(t *testing.T) {
	telemetry := make([]LiveSession, 0, 5)
	legacy := make([]LiveSession, 0, 5)
	for _, id := range []string{"e", "d", "c", "b", "a"} {
		telemetry = append(telemetry, liveSession(id, func(session *LiveSession) { session.ReportedOnly = true }))
		legacy = append(legacy, liveSession(id))
	}
	report := CompareLiveSessions("legacy", healthy(telemetry...), legacySide(legacy...), 3)
	if got := report.InBothReportedOnlySessions; len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("reported-only sessions = %+v", got)
	}
	if report.InBothReportedOnlyTruncated != 2 {
		t.Fatalf("reported-only truncated = %d", report.InBothReportedOnlyTruncated)
	}
}

func TestLiveSessionsFromGlobalViewClassifiesEvidence(t *testing.T) {
	reportedPublisher := PublisherRef{PublisherID: "api-1" + ReportedPublisherSuffix}
	measuringPublisher := PublisherRef{PublisherID: "proxy-1"}
	tests := []struct {
		name         string
		session      GlobalSessionView
		reportedOnly bool
	}{
		{
			name: "reporting publisher only", reportedOnly: true,
			session: GlobalSessionView{Publishers: []PublisherRef{reportedPublisher}, ReportingPublishers: []PublisherRef{reportedPublisher}},
		},
		{
			name:    "reporting and measuring publishers",
			session: GlobalSessionView{Publishers: []PublisherRef{reportedPublisher, measuringPublisher}, ReportingPublishers: []PublisherRef{reportedPublisher}},
		},
		{
			name: "malformed reporting publisher absent from reporting list", reportedOnly: true,
			session: GlobalSessionView{Publishers: []PublisherRef{reportedPublisher}},
		},
		{
			name: "measurement tombstone retains evidence",
			session: GlobalSessionView{
				MeasurementPruned: true, Reported: true, Publishers: []PublisherRef{reportedPublisher},
				ReportingPublishers: []PublisherRef{reportedPublisher}, ViewerBytesAccepted: 4096,
				Routes: []RouteActivityView{{Role: RoleViewerEgress, BytesAccepted: 4096}},
			},
		},
		{
			name:    "measuring publisher only",
			session: GlobalSessionView{Publishers: []PublisherRef{measuringPublisher}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.session.SessionID = "session"
			got := LiveSessionsFromGlobalView(GlobalMonitoringView{Sessions: []GlobalSessionView{test.session}})
			if len(got) != 1 || got[0].ReportedOnly != test.reportedOnly {
				t.Fatalf("projection = %+v, want reported_only=%t", got, test.reportedOnly)
			}
		})
	}
}

func TestReportedGhostIsAttributedInsteadOfSilentlyAgreed(t *testing.T) {
	publisher := PublisherRef{PublisherID: "api-1" + ReportedPublisherSuffix}
	view := GlobalMonitoringView{Sessions: []GlobalSessionView{{
		SessionID: "ghost-666", Reported: true,
		Publishers: []PublisherRef{publisher}, ReportingPublishers: []PublisherRef{publisher},
	}}}
	telemetry := TelemetrySide{Sessions: LiveSessionsFromGlobalView(view), ViewComplete: true}
	report := CompareLiveSessions("legacy", telemetry, legacySide(LiveSession{SessionID: "ghost-666"}), 0)
	if report.InBothReportedOnly != 1 || report.Agrees {
		t.Fatalf("ghost agreement = %+v", report)
	}
	if len(report.InBothReportedOnlySessions) != 1 || report.InBothReportedOnlySessions[0] != "ghost-666" {
		t.Fatalf("reported-only sessions = %+v", report.InBothReportedOnlySessions)
	}
}

func TestLiveSessionsFromGlobalView(t *testing.T) {
	view := GlobalMonitoringView{Sessions: []GlobalSessionView{
		{
			SessionID: "b", Subject: UserSubject(7), ProfileID: "profile-1", MediaFileID: 42,
			StartedAt:   time.Unix(1_700_000_000, 0),
			PlayMethods: []string{"direct"},
			Publishers: []PublisherRef{
				{PublisherID: "p1", NodeID: ""},
				{PublisherID: "p2", NodeID: "node-b"},
			},
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
	if sessions[0].ReportedOnly || sessions[1].ReportedOnly {
		t.Fatalf("measuring publishers were classified as reported-only: %+v", sessions)
	}
}

// An ended session leaves playback_sessions_sync as soon as playback stops but
// stays in the telemetry projection as a tombstone for Retention plus
// TombstoneRetention. Projecting those tombstones put every stream that ended in
// the last half hour into telemetry_only, and Agrees requires that list to be
// empty — so on any server with more than a couple of streams an hour the parity
// endpoint could never agree, which is the one verdict it exists to deliver.
//
// A tombstone a session manager still reports is a different thing: a live
// session whose measurement was merely pruned. It stays, as measured evidence.
func TestLiveSessionsFromGlobalViewDropsUnreportedTombstones(t *testing.T) {
	startedAt := time.Unix(1_700_000_000, 0)
	view := GlobalMonitoringView{Sessions: []GlobalSessionView{
		{SessionID: "ended", StartedAt: startedAt, MeasurementPruned: true,
			Routes: []RouteActivityView{{Method: "GET", Pattern: "/stream"}}, ViewerBytesAccepted: 4096,
			Publishers: []PublisherRef{{PublisherID: "measuring"}}},
		{SessionID: "pruned-but-reported", StartedAt: startedAt, MeasurementPruned: true, Reported: true,
			Routes: []RouteActivityView{{Method: "GET", Pattern: "/stream"}}, ViewerBytesAccepted: 4096,
			Publishers: []PublisherRef{{PublisherID: "measuring"}}},
		{SessionID: "live", StartedAt: startedAt, Reported: true, ViewerBytesAccepted: 1,
			Publishers: []PublisherRef{{PublisherID: "measuring"}}},
	}}

	sessions := LiveSessionsFromGlobalView(view)
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.SessionID)
		if session.ReportedOnly {
			t.Fatalf("%s lost its measured evidence", session.SessionID)
		}
	}
	if len(ids) != 2 || ids[0] != "live" || ids[1] != "pruned-but-reported" {
		t.Fatalf("projection = %v", ids)
	}

	// The legacy projection has already forgotten the ended session, and the
	// report has to be able to call that agreement.
	legacy := []LiveSession{
		liveSession("live", func(s *LiveSession) { s.ProfileID = ""; s.MediaFileID = 0; s.PlayMethod = ""; s.Node = "" }),
		liveSession("pruned-but-reported", func(s *LiveSession) { s.ProfileID = ""; s.MediaFileID = 0; s.PlayMethod = ""; s.Node = "" }),
	}
	report := CompareLiveSessions("legacy", TelemetrySide{Sessions: sessions, ViewComplete: true}, legacySide(legacy...), 0)
	if !report.Agrees {
		t.Fatalf("ended session blocked agreement: telemetry_only=%v withheld=%v",
			report.TelemetryOnly, report.AgreesWithheld)
	}
}
