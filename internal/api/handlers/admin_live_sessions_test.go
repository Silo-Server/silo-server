package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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
	snapshot := streamtelemetry.LiveSnapshot{}
	for _, fact := range facts {
		snapshot[fact.SessionID] = fact
	}
	return snapshot
}

func decorateAtRead(
	snapshot streamtelemetry.LiveSnapshot, rows []playbackSessionRow, includeIdle, viewComplete bool,
) ([]playbackSessionRow, int, int) {
	return decorateLiveSessions(snapshot, rows, includeIdle, viewComplete, false, readAt, minimumDeliveryIdleWindow)
}

// The defect this endpoint exists to correct: a client reports progress forever
// while nothing leaves the building. It is held back by default and counted.
func TestReportedSessionWithNoBytesIsHeldBack(t *testing.T) {
	rows := []playbackSessionRow{{SessionID: "flowing"}, {SessionID: "ghost"}}
	snapshot := snapshotOf(
		streamtelemetry.LiveByteFacts{SessionID: "flowing", Reported: true, ViewerBytes: 4096, StartedAt: settled},
		streamtelemetry.LiveByteFacts{SessionID: "ghost", Reported: true, StartedAt: settled},
	)

	sessions, noDelivery, _ := decorateAtRead(snapshot, rows, false, true)
	if len(sessions) != 1 || sessions[0].SessionID != "flowing" {
		t.Fatalf("sessions = %+v, want only the flowing one", sessions)
	}
	if noDelivery != 1 {
		t.Fatalf("no-delivery count = %d, want 1", noDelivery)
	}
	if sessions[0].Telemetry.Evidence != evidenceBoth {
		t.Fatalf("evidence = %q, want %q", sessions[0].Telemetry.Evidence, evidenceBoth)
	}

	shown, _, _ := decorateAtRead(snapshot, rows, true, true)
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

	sessions, noDelivery, _ := decorateAtRead(snapshot, rows, false, true)
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
		SessionID: "unclaimed", ViewerBytes: 8192, ViewerLastByteAt: readAt.Add(-time.Second),
		Subject: streamtelemetry.UserSubject(42), ProfileID: "p1", MediaFileID: 9,
		PlayMethod: "direct", StartedAt: time.Unix(1000, 0),
	})

	sessions, noDelivery, _ := decorateAtRead(snapshot, nil, false, true)
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

func TestReportedPrunedMeasurementIsNotFlaggedAsGhost(t *testing.T) {
	rows := []playbackSessionRow{{SessionID: "buffered"}}
	snapshot := snapshotOf(streamtelemetry.LiveByteFacts{
		SessionID: "buffered", Reported: true, ViewerBytes: 4096,
		ViewerLastByteAt: settled, MeasurementPruned: true, StartedAt: settled,
	})
	sessions, noDelivery, unclaimedIdle := decorateAtRead(snapshot, rows, false, true)
	if len(sessions) != 1 || noDelivery != 0 || unclaimedIdle != 0 {
		t.Fatalf("reported tombstone = sessions %+v, counts %d/%d", sessions, noDelivery, unclaimedIdle)
	}
	if sessions[0].Telemetry.Evidence != evidenceBoth || sessions[0].Telemetry.NoDelivery ||
		!sessions[0].Telemetry.MeasurementPruned {
		t.Fatalf("reported tombstone telemetry = %+v", sessions[0].Telemetry)
	}
}

// ABS reports paused=false at both call sites, so a fully buffered audiobook
// must be protected by remembered viewer bytes rather than the paused exception.
func TestABSShapedPrunedSessionRemainsVisible(t *testing.T) {
	facts := streamtelemetry.LiveByteFacts{
		SessionID: "abs", Reported: true, ReportedPaused: false, ViewerBytes: 1,
		MeasurementPruned: true, StartedAt: readAt.Add(-8 * time.Hour),
	}
	sessions, noDelivery, _ := decorateAtRead(snapshotOf(facts), []playbackSessionRow{{SessionID: "abs"}}, false, true)
	if len(sessions) != 1 || noDelivery != 0 || sessions[0].Telemetry.NoDelivery {
		t.Fatalf("ABS-shaped tombstone = sessions %+v, no-delivery %d", sessions, noDelivery)
	}
}

func TestMeasuredOnlyIdleSessionIsSuppressedAndRevealable(t *testing.T) {
	facts := streamtelemetry.LiveByteFacts{
		SessionID: "ended", ViewerBytes: 8192, ViewerLastByteAt: readAt.Add(-2 * minimumDeliveryIdleWindow),
	}
	hidden, noDelivery, unclaimedIdle := decorateAtRead(snapshotOf(facts), nil, false, true)
	if len(hidden) != 0 || noDelivery != 0 || unclaimedIdle != 1 {
		t.Fatalf("default idle filtering = sessions %+v, counts %d/%d", hidden, noDelivery, unclaimedIdle)
	}
	shown, _, shownIdle := decorateAtRead(snapshotOf(facts), nil, true, true)
	if len(shown) != 1 || shownIdle != 1 || !shown[0].Telemetry.UnclaimedIdle {
		t.Fatalf("revealed idle session = %+v, count %d", shown, shownIdle)
	}
}

func TestStaleViewNeitherClassifiesNorHides(t *testing.T) {
	snapshot := snapshotOf(
		streamtelemetry.LiveByteFacts{SessionID: "ghost", Reported: true, StartedAt: settled},
		streamtelemetry.LiveByteFacts{SessionID: "ended", ViewerBytes: 1, ViewerLastByteAt: settled},
	)
	rows := []playbackSessionRow{{SessionID: "ghost"}, {SessionID: "ended"}}
	sessions, noDelivery, unclaimedIdle := decorateLiveSessions(
		snapshot, rows, false, true, true, readAt, minimumDeliveryIdleWindow,
	)
	if len(sessions) != 2 || noDelivery != 0 || unclaimedIdle != 0 {
		t.Fatalf("stale view = sessions %+v, counts %d/%d", sessions, noDelivery, unclaimedIdle)
	}
	for _, row := range sessions {
		if row.Telemetry.NoDelivery || row.Telemetry.UnclaimedIdle {
			t.Fatalf("stale row classified: %+v", row.Telemetry)
		}
	}
}

func TestIncompleteCoverageReasonsSuppressGhostClassification(t *testing.T) {
	configuredAll := append([]streamtelemetry.Family(nil), streamtelemetry.AllFamilies...)
	params := streamtelemetry.ViewParams{Freshness: time.Hour, MembershipTTL: time.Hour,
		MaxMergedSessions: 10, MaxMergedTransfers: 10, MaxViewerIPsPerSession: 10,
		MaxDeviceIDsPerSession: 10, MaxClientVariantsPerSession: 10, MaxUserAgentsPerSession: 10,
		MaxMediaFileIDsPerSession: 10, MaxPlayMethodsPerSession: 10, MaxTokenIssuedAtPerSession: 10,
		MaxRoutesPerSession: 10, MaxIdentityConflictsPerSession: 10}
	tests := []struct {
		name     string
		coverage streamtelemetry.PublisherCoverage
		reason   string
		complete bool
	}{
		{name: "complete control", coverage: streamtelemetry.PublisherCoverage{Declared: true, ConfiguredFamilies: configuredAll}, complete: true},
		{name: "unknown publisher coverage", reason: "unknown_publisher_coverage"},
		{name: "partial family observation", coverage: streamtelemetry.PublisherCoverage{Declared: true}, reason: "partial_family_observation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			measuring := streamtelemetry.Snapshot{PublisherID: "api", NodeID: "node", CapturedAt: readAt,
				ReportingPublisherID: "api#reported", Coverage: test.coverage,
				Sessions: []streamtelemetry.SessionView{{SessionID: "ended", StartedAt: settled,
					Routes: []streamtelemetry.RouteActivityView{{Role: streamtelemetry.RoleViewerEgress, BytesAccepted: 1, LastByteAccepted: settled}}}}}
			reporting := streamtelemetry.Snapshot{PublisherID: "api#reported", NodeID: "node", CapturedAt: readAt,
				Coverage: streamtelemetry.PublisherCoverage{Declared: true}, Sessions: []streamtelemetry.SessionView{{
					SessionID: "ghost", StartedAt: settled, Reported: true, ReportedAt: readAt}}}
			set := streamtelemetry.PublisherSet{
				Members:   []streamtelemetry.Member{{PublisherID: "api", LastHeartbeat: readAt}, {PublisherID: "api#reported", LastHeartbeat: readAt}},
				Snapshots: []streamtelemetry.Snapshot{measuring, reporting},
			}
			view := streamtelemetry.BuildGlobalView(set, readAt, params)
			if view.Complete != test.complete {
				t.Fatalf("complete = %v, reasons %v", view.Complete, view.IncompleteReasons)
			}
			if test.reason != "" && !slices.Contains(view.IncompleteReasons, test.reason) {
				t.Fatalf("reasons = %v, want %q", view.IncompleteReasons, test.reason)
			}
			rows, noDelivery, unclaimedIdle := decorateLiveSessions(
				streamtelemetry.LiveByteFactsFromGlobalView(view), []playbackSessionRow{{SessionID: "ghost"}}, true,
				view.Complete, false, readAt, minimumDeliveryIdleWindow,
			)
			if len(rows) != 2 {
				t.Fatalf("rows = %+v", rows)
			}
			if test.complete {
				if noDelivery != 1 || unclaimedIdle != 1 {
					t.Fatalf("control counts = %d/%d", noDelivery, unclaimedIdle)
				}
				return
			}
			if noDelivery != 0 || unclaimedIdle != 0 {
				t.Fatalf("incomplete counts = %d/%d", noDelivery, unclaimedIdle)
			}
			for _, row := range rows {
				if row.Telemetry.NoDelivery || row.Telemetry.UnclaimedIdle {
					t.Fatalf("incomplete row classified: %+v", row.Telemetry)
				}
			}
		})
	}
}

func TestInternalRelayActivityDoesNotVouchForViewerDelivery(t *testing.T) {
	facts := streamtelemetry.LiveByteFacts{
		SessionID: "relay-open", Reported: true, StartedAt: settled,
		OpenObservations: 1, LastByteAt: readAt, // role-agnostic relay activity
	}
	_, noDelivery, _ := decorateAtRead(snapshotOf(facts), []playbackSessionRow{{SessionID: "relay-open"}}, false, true)
	if noDelivery != 1 {
		t.Fatalf("no-delivery count = %d, want relay activity ignored", noDelivery)
	}
}

func TestOpenUnclaimedRelayOnlySessionIsNotIdle(t *testing.T) {
	facts := streamtelemetry.LiveByteFacts{
		SessionID: "relay-only", RelayBytes: 4096, OpenObservations: 1, LastByteAt: readAt,
	}
	sessions, noDelivery, unclaimedIdle := decorateAtRead(snapshotOf(facts), nil, false, true)
	if len(sessions) != 1 || noDelivery != 0 || unclaimedIdle != 0 {
		t.Fatalf("relay-only session = sessions %+v, counts %d/%d", sessions, noDelivery, unclaimedIdle)
	}
	if sessions[0].Telemetry.UnclaimedIdle {
		t.Fatalf("open relay-only session classified as unclaimed idle: %+v", sessions[0].Telemetry)
	}
}

func TestOpenViewerRequestPreventsNoDeliveryDuringSlowStart(t *testing.T) {
	facts := streamtelemetry.LiveByteFacts{
		SessionID: "slow-start", Reported: true, StartedAt: settled, ViewerOpenObservations: 1,
	}
	sessions, noDelivery, _ := decorateAtRead(snapshotOf(facts), []playbackSessionRow{{SessionID: "slow-start"}}, false, true)
	if len(sessions) != 1 || noDelivery != 0 || sessions[0].Telemetry.NoDelivery {
		t.Fatalf("open viewer request classified as no delivery: %+v", sessions)
	}
}

func TestDeliveryIdleWindowTracksSweepAndViewTTL(t *testing.T) {
	if got := deliveryIdleWindow(streamtelemetry.Config{SweepInterval: time.Second, ViewTTL: time.Second}); got != minimumDeliveryIdleWindow {
		t.Fatalf("default floor = %v", got)
	}
	if got := deliveryIdleWindow(streamtelemetry.Config{SweepInterval: 20 * time.Second, ViewTTL: 10 * time.Second}); got != 100*time.Second {
		t.Fatalf("derived window = %v, want 100s", got)
	}
}

func TestLegacyLiveSessionsMarksBothIdleClassesShown(t *testing.T) {
	handler := &AdminHandler{SessionsLoader: &fakePlaybackSessionsReader{known: map[string]struct{}{}}}
	response := liveSessionsResponse{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/sessions/live", nil)
	handler.serveLegacyLiveSessions(recorder, request, &response)
	if recorder.Code != http.StatusOK || !response.NoDeliveryShown || !response.UnclaimedIdleShown {
		t.Fatalf("legacy envelope = status %d, response %+v", recorder.Code, response)
	}
}

// The legacy row supplies the display fields; the view supplies the facts.
func TestLegacyRowSuppliesDisplayFields(t *testing.T) {
	rows := []playbackSessionRow{{SessionID: "s1", MediaTitle: "Dune", PosterURL: "/p.jpg", Username: "ada"}}
	snapshot := snapshotOf(streamtelemetry.LiveByteFacts{SessionID: "s1", Reported: true, ViewerBytes: 10})

	sessions, _, _ := decorateAtRead(snapshot, rows, false, true)
	if sessions[0].MediaTitle != "Dune" || sessions[0].PosterURL != "/p.jpg" || sessions[0].Username != "ada" {
		t.Fatalf("display fields lost: %+v", sessions[0])
	}
}

// An absent rate must stay absent on the wire. Rendering "not yet measured" as
// zero would read as a stalled stream on a session that is streaming fine.
func TestSessionTelemetryOmitsUnmeasuredRate(t *testing.T) {
	block := newSessionTelemetry(streamtelemetry.LiveByteFacts{ViewerBytes: 10}, readAt, minimumDeliveryIdleWindow)
	if block.DeliveryRateKbps != nil {
		t.Fatalf("delivery rate = %v, want absent", *block.DeliveryRateKbps)
	}
	if block.LastByteAt != nil {
		t.Fatalf("last byte = %v, want absent", *block.LastByteAt)
	}

	at := time.Now()
	block = newSessionTelemetry(streamtelemetry.LiveByteFacts{
		ViewerBytes: 10, DeliveryRateKbps: 1234.5, RateAvailable: true, LastByteAt: at,
	}, readAt, minimumDeliveryIdleWindow)
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

	sessions, noDelivery, _ := decorateAtRead(snapshot, rows, false, false)
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

	sessions, noDelivery, _ := decorateAtRead(snapshot, rows, false, true)
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
	if _, count, _ := decorateAtRead(aged, rows, false, true); count != 1 {
		t.Fatalf("no-delivery count = %d past the grace window, want 1", count)
	}
}

// A session whose start instant never reached the view has an unknown age, and
// suppressing on an unknown age would hand it permanent immunity.
func TestUnknownStartInstantStillClassifies(t *testing.T) {
	rows := []playbackSessionRow{{SessionID: "ageless"}}
	snapshot := snapshotOf(streamtelemetry.LiveByteFacts{SessionID: "ageless", Reported: true})

	if _, count, _ := decorateAtRead(snapshot, rows, false, true); count != 1 {
		t.Fatalf("no-delivery count = %d for an unknown start instant, want 1", count)
	}
}

// playbackSessionQueryRecorder records the display lookups a load actually put on
// the wire. That is the only place the chunking is observable: whatever
// loadPlaybackSessionsByID did to get there, it returns one flat slice.
type playbackSessionQueryRecorder struct {
	mu      sync.Mutex
	queries []string
}

func (c *playbackSessionQueryRecorder) TraceQueryStart(
	ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "FROM playback_sessions_sync") {
		c.mu.Lock()
		c.queries = append(c.queries, data.SQL)
		c.mu.Unlock()
	}
	return ctx
}

func (c *playbackSessionQueryRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func (c *playbackSessionQueryRecorder) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.queries...)
}

// fakePlaybackSessionsReader answers a display lookup from an in-memory set and
// records the id set of every call, so the chunking in loadPlaybackSessionsByID
// is observable without a database.
type fakePlaybackSessionsReader struct {
	known map[string]struct{}
	calls [][]string
}

func (f *fakePlaybackSessionsReader) Load(
	_ context.Context, _ *http.Request, query PlaybackSessionsQuery,
) ([]playbackSessionRow, error) {
	f.calls = append(f.calls, append([]string(nil), query.SessionIDs...))
	rows := make([]playbackSessionRow, 0, len(query.SessionIDs))
	for _, id := range query.SessionIDs {
		if _, ok := f.known[id]; ok {
			rows = append(rows, playbackSessionRow{SessionID: id})
		}
	}
	return rows, nil
}

// TestLoadPlaybackSessionsByIDChunksWithoutLosingOrDuplicatingRowsFake pins the
// reassembly contract: a chunked loader is only correct if the chunks come back
// as exactly what one query would have returned. The id set is two whole chunks
// plus a short tail, which is the arrangement an off-by-one in the loop shows up
// in — a boundary crossed more than once, and a final partial chunk.
//
// This is the leg that runs everywhere. The companion DB-backed test asserts the
// per-chunk SQL bound, which is only visible against a real connection.
func TestLoadPlaybackSessionsByIDChunksWithoutLosingOrDuplicatingRowsFake(t *testing.T) {
	const sessions = 2*playbackSessionIDChunk + 7
	ids := make([]string, 0, sessions)
	known := make(map[string]struct{}, sessions)
	for i := range sessions {
		id := fmt.Sprintf("chunk-%d", i)
		ids = append(ids, id)
		known[id] = struct{}{}
	}

	reader := &fakePlaybackSessionsReader{known: known}
	handler := &AdminHandler{SessionsLoader: reader}
	request := httptest.NewRequest(http.MethodGet, "/admin/sessions/live", nil)
	rows, err := handler.loadPlaybackSessionsByID(context.Background(), request, ids)
	if err != nil {
		t.Fatalf("loadPlaybackSessionsByID: %v", err)
	}

	if len(reader.calls) < 2 {
		t.Fatalf("display lookups = %d for %d ids, want the set split across chunks", len(reader.calls), sessions)
	}
	for i, call := range reader.calls {
		if len(call) > playbackSessionIDChunk {
			t.Fatalf("lookup %d carried %d ids, want at most %d", i, len(call), playbackSessionIDChunk)
		}
	}
	seen := make(map[string]int, len(rows))
	for _, row := range rows {
		seen[row.SessionID]++
	}
	if len(rows) != sessions || len(seen) != sessions {
		t.Fatalf("rows = %d (%d distinct), want %d of each", len(rows), len(seen), sessions)
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Fatalf("session %s came back %d times, want exactly once", id, seen[id])
		}
	}

	// A set that fits in one chunk must not be split: the chunking is a bound on
	// a pathological case, not a fixed cost on the ordinary one.
	reader.calls = nil
	if _, err := handler.loadPlaybackSessionsByID(context.Background(), request, ids[:3]); err != nil {
		t.Fatalf("loadPlaybackSessionsByID small set: %v", err)
	}
	if len(reader.calls) != 1 {
		t.Fatalf("display lookups = %d for 3 ids, want 1", len(reader.calls))
	}
}

// The live endpoint hands loadPlaybackSessionsByID every session id in the merged
// telemetry view, and the view's only bound is MaxMergedSessions — 50 000. One
// ANY() of that size against a SELECT carrying seven LEFT JOINs and a per-row
// poster presign is what the chunking exists to avoid, on a path the dashboard
// hits on every refresh. A chunked loader is only correct if the chunks
// reassemble into exactly what one query would have returned, so this asserts the
// split happened AND that nothing fell out of a boundary or came back twice.
//
// DB-backed because the second assertion is about the SQL that actually reaches
// Postgres, which no fake can observe. The reassembly half of the contract is
// covered without a database by
// TestLoadPlaybackSessionsByIDChunksWithoutLosingOrDuplicatingRowsFake, so a CI
// run with no SILO_TEST_DATABASE_URL still gates the chunking itself.
func TestLoadPlaybackSessionsByIDChunkedQueriesAreBoundedInSQL(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run the DB-backed playback session chunking test")
	}

	ctx := context.Background()
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	recorder := &playbackSessionQueryRecorder{}
	poolConfig.ConnConfig.Tracer = recorder
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(pool.Close)

	// Two whole chunks and a short one, so a boundary is crossed more than once
	// and the tail is partial — the arrangement an off-by-one in the loop shows
	// up in. Ids carry a per-run tag so a shared test database cannot contribute
	// rows of its own to the exact-count assertions below.
	const sessions = 2*playbackSessionIDChunk + 7
	tag := time.Now().UnixNano()
	ids := make([]string, 0, sessions)
	for i := 0; i < sessions; i++ {
		ids = append(ids, fmt.Sprintf("chunk-%d-%d", tag, i))
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM playback_sessions_sync WHERE session_id = ANY($1)`, ids)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO playback_sessions_sync
			(session_id, user_id, media_file_id, play_method, reporting_node, started_at, updated_at)
		SELECT id, 1, 0, 'direct', '', NOW(), NOW() FROM UNNEST($1::text[]) AS id
	`, ids); err != nil {
		t.Fatalf("seed playback sessions: %v", err)
	}

	handler := &AdminHandler{SessionsLoader: NewPlaybackSessionsLoader(pool, nil, nil)}
	request := httptest.NewRequest(http.MethodGet, "/admin/sessions/live", nil)
	rows, err := handler.loadPlaybackSessionsByID(ctx, request, ids)
	if err != nil {
		t.Fatalf("loadPlaybackSessionsByID: %v", err)
	}

	lookups := recorder.recorded()
	if len(lookups) < 2 {
		t.Fatalf("display lookups = %d for %d ids, want the set split across chunks", len(lookups), sessions)
	}
	// The bound has to be in the SQL, not only in the caller that chunks. A
	// planner reading an unbounded ORDER BY over seven LEFT JOINs is free to
	// choose a sort of the whole join, and the next caller to reach this loader
	// need not chunk at all.
	for i, sql := range lookups {
		if !strings.Contains(sql, " LIMIT $") {
			t.Fatalf("display lookup %d has no row bound of its own: %s", i, sql)
		}
	}
	seen := make(map[string]int, len(rows))
	for _, row := range rows {
		seen[row.SessionID]++
	}
	if len(rows) != sessions || len(seen) != sessions {
		t.Fatalf("rows = %d (%d distinct), want %d of each", len(rows), len(seen), sessions)
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Fatalf("session %s came back %d times, want exactly once", id, seen[id])
		}
	}
}
