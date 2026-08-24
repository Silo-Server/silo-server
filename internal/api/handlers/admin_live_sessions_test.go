package handlers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// readAt is the instant the tests read the view at, and settled is a session
// start old enough to be past noDeliveryGrace — the shape every assertion about
// no-delivery classification wants, since a just-started session has not had
// time to be measured yet.
var (
	readAt  = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	settled = readAt.Add(-time.Hour)
)

func snapshotOf(facts ...streamtelemetry.LiveByteFacts) streamtelemetry.LiveSnapshot {
	snapshot := streamtelemetry.LiveSnapshot{Facts: map[string]streamtelemetry.LiveByteFacts{}}
	for _, fact := range facts {
		snapshot.Facts[fact.SessionID] = fact
	}
	return snapshot
}

// The defect this endpoint exists to correct: a client reports progress forever
// while nothing leaves the building. It is held back by default and counted.
func TestReportedSessionWithNoBytesIsHeldBack(t *testing.T) {
	rows := []playbackSessionRow{{SessionID: "flowing"}, {SessionID: "ghost"}}
	snapshot := snapshotOf(
		streamtelemetry.LiveByteFacts{SessionID: "flowing", Reported: true, ViewerBytes: 4096, StartedAt: settled},
		streamtelemetry.LiveByteFacts{SessionID: "ghost", Reported: true, StartedAt: settled},
	)

	sessions, noDelivery := decorateLiveSessions(snapshot, rows, false, true, readAt)
	if len(sessions) != 1 || sessions[0].SessionID != "flowing" {
		t.Fatalf("sessions = %+v, want only the flowing one", sessions)
	}
	if noDelivery != 1 {
		t.Fatalf("no-delivery count = %d, want 1", noDelivery)
	}
	if sessions[0].Telemetry.Evidence != evidenceBoth {
		t.Fatalf("evidence = %q, want %q", sessions[0].Telemetry.Evidence, evidenceBoth)
	}

	shown, _ := decorateLiveSessions(snapshot, rows, true, true, readAt)
	if len(shown) != 2 {
		t.Fatalf("with include_idle: %+v, want both", shown)
	}
}

// A paused client stops pulling bytes, so silence is the expected shape rather
// than an anomaly (#243). This is read off two fields of one row now, not from
// reconciling two stores.
func TestPausedSessionWithNoBytesIsNotFlagged(t *testing.T) {
	rows := []playbackSessionRow{{SessionID: "paused"}}
	snapshot := snapshotOf(streamtelemetry.LiveByteFacts{
		SessionID: "paused", Reported: true, ReportedPaused: true, StartedAt: settled,
	})

	sessions, noDelivery := decorateLiveSessions(snapshot, rows, false, true, readAt)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v, want the paused session kept", sessions)
	}
	if noDelivery != 0 {
		t.Fatalf("no-delivery count = %d, want 0 for a paused session", noDelivery)
	}
	if sessions[0].Telemetry.NoDelivery {
		t.Fatal("paused session flagged as undelivered")
	}
	if sessions[0].Telemetry.Evidence != evidenceReported {
		t.Fatalf("evidence = %q, want %q", sessions[0].Telemetry.Evidence, evidenceReported)
	}
}

// Bytes with nobody claiming them is the other half of the picture, and it must
// survive into the list even though Postgres has no row to decorate it with.
func TestMeasuredSessionWithNoLegacyRowStillAppears(t *testing.T) {
	snapshot := snapshotOf(streamtelemetry.LiveByteFacts{
		SessionID: "unclaimed", ViewerBytes: 8192,
		Subject: streamtelemetry.UserSubject(42), ProfileID: "p1", MediaFileID: 9,
		PlayMethod: "direct", StartedAt: time.Unix(1000, 0),
	})

	sessions, noDelivery := decorateLiveSessions(snapshot, nil, false, true, readAt)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v, want the unclaimed delivery", sessions)
	}
	if noDelivery != 0 {
		t.Fatalf("no-delivery count = %d, want 0", noDelivery)
	}
	row := sessions[0]
	if row.Telemetry.Evidence != evidenceMeasured {
		t.Fatalf("evidence = %q, want %q", row.Telemetry.Evidence, evidenceMeasured)
	}
	// Identity comes off the view so the row is not anonymous.
	if row.UserID != 42 || row.ProfileID != "p1" || row.MediaFileID != 9 || row.PlayMethod != "direct" {
		t.Fatalf("synthesized row lost identity: %+v", row)
	}
	// And nothing telemetry is not canonical for was invented.
	if row.MediaTitle != "" || row.PosterURL != "" || row.Username != "" {
		t.Fatalf("synthesized row invented display fields: %+v", row)
	}
}

// The legacy row supplies the display fields; the view supplies the facts.
func TestLegacyRowSuppliesDisplayFields(t *testing.T) {
	rows := []playbackSessionRow{{SessionID: "s1", MediaTitle: "Dune", PosterURL: "/p.jpg", Username: "ada"}}
	snapshot := snapshotOf(streamtelemetry.LiveByteFacts{SessionID: "s1", Reported: true, ViewerBytes: 10})

	sessions, _ := decorateLiveSessions(snapshot, rows, false, true, readAt)
	if sessions[0].MediaTitle != "Dune" || sessions[0].PosterURL != "/p.jpg" || sessions[0].Username != "ada" {
		t.Fatalf("display fields lost: %+v", sessions[0])
	}
}

// An absent rate must stay absent on the wire. Rendering "not yet measured" as
// zero would read as a stalled stream on a session that is streaming fine.
func TestSessionTelemetryOmitsUnmeasuredRate(t *testing.T) {
	block := newSessionTelemetry(streamtelemetry.LiveByteFacts{ViewerBytes: 10})
	if block.DeliveryRateKbps != nil {
		t.Fatalf("delivery rate = %v, want absent", *block.DeliveryRateKbps)
	}
	if block.LastByteAt != nil {
		t.Fatalf("last byte = %v, want absent", *block.LastByteAt)
	}

	at := time.Now()
	block = newSessionTelemetry(streamtelemetry.LiveByteFacts{
		ViewerBytes: 10, DeliveryRateKbps: 1234.5, RateAvailable: true, LastByteAt: at,
	})
	if block.DeliveryRateKbps == nil || *block.DeliveryRateKbps != 1234.5 {
		t.Fatalf("delivery rate = %+v, want 1234.5", block.DeliveryRateKbps)
	}
	if block.LastByteAt == nil || !block.LastByteAt.Equal(at) {
		t.Fatalf("last byte = %+v, want %v", block.LastByteAt, at)
	}
}

// Rows come back newest-first regardless of which bucket they came from, so
// revealing the held-back rows cannot scramble the list.
func TestSortPlaybackSessionRowsIsNewestFirst(t *testing.T) {
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	rows := []playbackSessionRow{
		{SessionID: "old", StartedAt: base.Add(-time.Hour)},
		{SessionID: "new", StartedAt: base},
		{SessionID: "b", StartedAt: base.Add(-time.Hour)},
	}
	sortPlaybackSessionRows(rows)
	if rows[0].SessionID != "new" {
		t.Fatalf("first row = %q, want the newest", rows[0].SessionID)
	}
	if rows[1].SessionID != "b" || rows[2].SessionID != "old" {
		t.Fatalf("tie broken as %q,%q; want session id order b,old", rows[1].SessionID, rows[2].SessionID)
	}
}

// include_idle is read through the package's existing truthy parser, so the
// spellings it accepts are the spellings this endpoint accepts.
func TestIncludeIdleAcceptsTruthySpellings(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", " yes ", "on"} {
		if !parseBoolFormValue(value) {
			t.Fatalf("include_idle=%q read false, want true", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "maybe"} {
		if parseBoolFormValue(value) {
			t.Fatalf("include_idle=%q read true, want false", value)
		}
	}
}

// An incomplete view is blindness, not disagreement: the publisher holding a
// session's bytes may be the missing one, so nothing may be classified as
// undelivered and nothing may be hidden on that basis.
func TestIncompleteViewNeitherFlagsNorHides(t *testing.T) {
	rows := []playbackSessionRow{{SessionID: "ghost"}}
	snapshot := snapshotOf(streamtelemetry.LiveByteFacts{SessionID: "ghost", Reported: true, StartedAt: settled})

	sessions, noDelivery := decorateLiveSessions(snapshot, rows, false, false, readAt)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v, want the row kept while the view is incomplete", sessions)
	}
	if sessions[0].Telemetry.NoDelivery {
		t.Fatal("classified as undelivered against an incomplete view")
	}
	if noDelivery != 0 {
		t.Fatalf("no-delivery count = %d, want 0 while the view is incomplete", noDelivery)
	}
}

// A session exists before anything has asked it for a byte, so a play that has
// only just started is reported with nothing measured — the same shape as a
// ghost. Classifying on it would put every new play in the hidden bucket and
// count it in no_delivery_count, which is the number an operator reads to size
// the ghost problem.
func TestFreshlyStartedSessionIsNotYetUndelivered(t *testing.T) {
	rows := []playbackSessionRow{{SessionID: "starting"}}
	snapshot := snapshotOf(streamtelemetry.LiveByteFacts{
		SessionID: "starting", Reported: true, StartedAt: readAt.Add(-noDeliveryGrace / 2),
	})

	sessions, noDelivery := decorateLiveSessions(snapshot, rows, false, true, readAt)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v, want the just-started session shown", sessions)
	}
	if sessions[0].Telemetry.NoDelivery {
		t.Fatal("a session younger than the grace window was flagged as undelivered")
	}
	if noDelivery != 0 {
		t.Fatalf("no-delivery count = %d, want 0 while inside the grace window", noDelivery)
	}

	// Past the window with still nothing measured, it is the real thing.
	aged := snapshotOf(streamtelemetry.LiveByteFacts{
		SessionID: "starting", Reported: true, StartedAt: readAt.Add(-2 * noDeliveryGrace),
	})
	if _, count := decorateLiveSessions(aged, rows, false, true, readAt); count != 1 {
		t.Fatalf("no-delivery count = %d past the grace window, want 1", count)
	}
}

// A session whose start instant never reached the view has an unknown age, and
// suppressing on an unknown age would hand it permanent immunity.
func TestUnknownStartInstantStillClassifies(t *testing.T) {
	rows := []playbackSessionRow{{SessionID: "ageless"}}
	snapshot := snapshotOf(streamtelemetry.LiveByteFacts{SessionID: "ageless", Reported: true})

	if _, count := decorateLiveSessions(snapshot, rows, false, true, readAt); count != 1 {
		t.Fatalf("no-delivery count = %d for an unknown start instant, want 1", count)
	}
}
