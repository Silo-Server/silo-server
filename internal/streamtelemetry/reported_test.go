package streamtelemetry

import (
	"strings"
	"testing"
	"time"
)

type stubReportedSource struct{ sessions []ReportedSession }

func (s stubReportedSource) ReportedSessions() []ReportedSession { return s.sessions }

func reportedConfig(t testing.TB) Config {
	t.Helper()
	cfg := DefaultConfig("node-1")
	cfg.Enabled = true
	cfg.PublisherID = "api-1"
	return cfg
}

func TestReportedPublisherUsesASuffixedPublisherID(t *testing.T) {
	publisher := NewReportedPublisher(reportedConfig(t), stubReportedSource{}, NewLocalStore(), nil)
	if got, want := publisher.PublisherID(), "api-1"+ReportedPublisherSuffix; got != want {
		t.Fatalf("publisher id = %q, want %q", got, want)
	}
	// The measuring publisher must keep its own id: the merge tells a claim from a
	// measurement by which publisher supplied it.
	if ReportedPublisherIDFor("api-1") == "api-1" {
		t.Fatal("reported publisher id collides with the measuring one")
	}
}

func TestReportedPublisherDeclaresNoMeasuredFamilies(t *testing.T) {
	snapshot := NewReportedPublisher(reportedConfig(t), stubReportedSource{}, NewLocalStore(), nil).SnapshotAt(time.Unix(0, 0))
	if !snapshot.Coverage.Declared || len(snapshot.Coverage.ConfiguredFamilies) != 0 {
		t.Fatalf("reported coverage = %+v", snapshot.Coverage)
	}
}

// The whole contract of this publisher: it says a session EXISTS and what the
// client claimed about it, and nothing about delivery. Publishing bytes or a
// viewer address here would let a claim be read as a measurement.
func TestReportedSnapshotCarriesNoBytesAndNoViewerAddresses(t *testing.T) {
	source := stubReportedSource{sessions: []ReportedSession{{
		SessionID: "s1", Subject: UserSubject(7), ProfileID: "p1", MediaFileID: 33,
		PlayMethod: "transcode", StartedAt: time.Unix(1000, 0), Paused: true,
		PositionSeconds: 42.5, RealtimeAlive: true,
	}}}
	publisher := NewReportedPublisher(reportedConfig(t), source, NewLocalStore(), nil)

	at := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	snapshot := publisher.SnapshotAt(at)
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("sessions = %+v", snapshot.Sessions)
	}
	session := snapshot.Sessions[0]
	if session.BytesAccepted != 0 || len(session.ViewerIPs) != 0 || len(session.Routes) != 0 {
		t.Fatalf("reported session claimed delivery: bytes %d ips %v routes %v",
			session.BytesAccepted, session.ViewerIPs, session.Routes)
	}
	if !session.Reported || !session.ReportedPaused || session.ReportedPositionSeconds != 42.5 {
		t.Fatalf("reported state = %+v", session)
	}
	if !session.ReportedAt.Equal(at) {
		t.Fatalf("reported at = %v, want the capture time %v", session.ReportedAt, at)
	}
	if session.Subject != UserSubject(7) || session.ProfileID != "p1" || session.MediaFileID != 33 {
		t.Fatalf("identity = %+v", session)
	}
	if session.StartedAtSource != StartedAtSourceSession {
		t.Fatalf("started-at source = %q, want %q", session.StartedAtSource, StartedAtSourceSession)
	}
}

func TestReportedSnapshotSkipsSessionsWithNoID(t *testing.T) {
	source := stubReportedSource{sessions: []ReportedSession{{SessionID: ""}, {SessionID: "s1"}}}
	publisher := NewReportedPublisher(reportedConfig(t), source, NewLocalStore(), nil)
	snapshot := publisher.SnapshotAt(time.Unix(0, 0))
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].SessionID != "s1" {
		t.Fatalf("sessions = %+v, want only the identified one", snapshot.Sessions)
	}
}

func TestReportedSnapshotTruncatesAtTheSessionCap(t *testing.T) {
	cfg := reportedConfig(t)
	cfg.MaxSessions = 2
	source := stubReportedSource{sessions: []ReportedSession{
		{SessionID: "a"}, {SessionID: "b"}, {SessionID: "c"},
	}}
	snapshot := NewReportedPublisher(cfg, source, NewLocalStore(), nil).SnapshotAt(time.Unix(0, 0))
	if len(snapshot.Sessions) != 2 {
		t.Fatalf("sessions = %d, want the cap of 2", len(snapshot.Sessions))
	}
	if !snapshot.Truncated {
		t.Fatal("truncation not reported; a short view that does not say so reads as complete")
	}
}

func TestReportedPublisherDisabledWithoutSourceOrStore(t *testing.T) {
	cfg := reportedConfig(t)
	if NewReportedPublisher(cfg, nil, NewLocalStore(), nil).Enabled() {
		t.Fatal("enabled with no source")
	}
	if NewReportedPublisher(cfg, stubReportedSource{}, nil, nil).Enabled() {
		t.Fatal("enabled with no store")
	}
	off := cfg
	off.Enabled = false
	if NewReportedPublisher(off, stubReportedSource{}, NewLocalStore(), nil).Enabled() {
		t.Fatal("enabled with telemetry off")
	}
}

// A process publishes twice — measuring and reporting. LocalStore holds exactly
// one snapshot, so two of them would leave the reported sessions somewhere
// nothing reads and a non-distributed deployment would serve a "complete" view
// with every paused and pre-delivery viewer missing.
func TestLocalHubLetsBothPublishersSeeEachOther(t *testing.T) {
	hub := NewLocalHub()
	measuring := hub.Store("api-1")
	reporting := hub.Store(ReportedPublisherIDFor("api-1"))
	at := time.Unix(5000, 0)

	if err := measuring.Publish(t.Context(), Snapshot{PublisherID: "api-1", CapturedAt: at,
		Sessions: []SessionView{{SessionID: "s1", BytesAccepted: 10}}}); err != nil {
		t.Fatal(err)
	}
	if err := reporting.Publish(t.Context(), Snapshot{PublisherID: "api-1#reported", CapturedAt: at,
		Sessions: []SessionView{{SessionID: "s2", Reported: true, ReportedAt: at}}}); err != nil {
		t.Fatal(err)
	}

	set, err := measuring.LoadAll(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Snapshots) != 2 || len(set.Members) != 2 {
		t.Fatalf("set = %d snapshots / %d members, want both publishers", len(set.Snapshots), len(set.Members))
	}
}

// One publisher leaving must not blank the other's contribution.
func TestLocalHubLeaveRemovesOnlyThatPublisher(t *testing.T) {
	hub := NewLocalHub()
	measuring := hub.Store("api-1")
	reporting := hub.Store("api-1#reported")
	at := time.Unix(5000, 0)
	_ = measuring.Publish(t.Context(), Snapshot{PublisherID: "api-1", CapturedAt: at})
	_ = reporting.Publish(t.Context(), Snapshot{PublisherID: "api-1#reported", CapturedAt: at})

	if err := reporting.Leave(t.Context()); err != nil {
		t.Fatal(err)
	}
	set, _ := measuring.LoadAll(t.Context())
	if len(set.Snapshots) != 1 || set.Snapshots[0].PublisherID != "api-1" {
		t.Fatalf("after leave: %+v, want only the measuring publisher", set.Snapshots)
	}
}

func TestLocalHubRejectsPublisherIDMismatch(t *testing.T) {
	hub := NewLocalHub()
	store := hub.Store("api-1")
	err := store.Publish(t.Context(), Snapshot{PublisherID: "api-2", CapturedAt: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "publisher id changed") {
		t.Fatalf("publish error = %v, want publisher id mismatch", err)
	}
	set, loadErr := store.LoadAll(t.Context())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(set.Members) != 0 || len(set.Snapshots) != 0 {
		t.Fatalf("mismatched snapshot entered the hub: %+v", set)
	}
}

func TestLocalHubAllowsRegistryToGeneratePublisherID(t *testing.T) {
	hub := NewLocalHub()
	// Config leaves PublisherID empty and NewRegistry generates it after the
	// local handle has already been constructed, matching startup wiring.
	store := hub.Store("")
	if err := store.Publish(t.Context(), Snapshot{PublisherID: "generated", CapturedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	set, err := store.LoadAll(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Snapshots) != 1 || set.Snapshots[0].PublisherID != "generated" {
		t.Fatalf("snapshots = %+v, want the generated publisher", set.Snapshots)
	}
}

// A publisher that goes away without leaving keeps its last snapshot readable
// and shows operators ghost sessions — the exact thing this publisher exists to
// remove.
func TestReportedPublisherLeavesTheRosterOnStop(t *testing.T) {
	hub := NewLocalHub()
	store := hub.Store(ReportedPublisherIDFor("api-1"))
	publisher := NewReportedPublisher(reportedConfig(t), stubReportedSource{
		sessions: []ReportedSession{{SessionID: "s1"}},
	}, store, nil)

	publisher.Start(t.Context())
	publisher.started.Store(true)
	if err := publisher.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if set, _ := store.LoadAll(t.Context()); len(set.Snapshots) != 0 {
		t.Fatalf("snapshots after stop = %+v, want the publisher gone", set.Snapshots)
	}
}

// AllSessions returns randomized map order, so truncating before sorting
// published a different arbitrary subset every sweep.
func TestReportedSnapshotTruncatesTheSameSubsetEveryTime(t *testing.T) {
	cfg := reportedConfig(t)
	cfg.MaxSessions = 2
	source := stubReportedSource{sessions: []ReportedSession{
		{SessionID: "c"}, {SessionID: "a"}, {SessionID: ""}, {SessionID: "b"},
	}}
	snapshot := NewReportedPublisher(cfg, source, NewLocalStore(), nil).SnapshotAt(time.Unix(0, 0))
	if len(snapshot.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want the cap of 2", snapshot.Sessions)
	}
	// Lowest ids win, deterministically — and the empty id did not consume a slot.
	if snapshot.Sessions[0].SessionID != "a" || snapshot.Sessions[1].SessionID != "b" {
		t.Fatalf("subset = %+v, want a,b", snapshot.Sessions)
	}
}
