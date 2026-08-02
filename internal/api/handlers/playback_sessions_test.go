package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/contracts/complexv22"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/worker"
)

func TestLiveSnapshotUsesSharedConcreteSessionDTO(t *testing.T) {
	got := reflect.TypeOf(sessionSnapshotResponse{}.Sessions).Elem()
	want := reflect.TypeOf(complexv22.SnapshotSession{})
	if got != want {
		t.Fatalf("live snapshot session type = %v, want shared %v", got, want)
	}
}

func TestSnapshotNormalizesLegacySentinelWithoutExposingIt(t *testing.T) {
	row := playbackSessionRow{SessionGeneration: playback.LegacySessionGenerationSentinel}
	normalizePlaybackSessionGeneration(&row)
	if row.SessionGeneration != "" {
		t.Fatalf("snapshot exposed sentinel generation %q", row.SessionGeneration)
	}
	now := time.Now().UTC()
	bootGeneration := uuid.NewString()
	reason := evaluateSnapshotCompleteness(now,
		[]snapshotReportingNode{{ID: "api-1", BootGeneration: bootGeneration, UpdatedAt: now}},
		[]snapshotWatermark{{ReportingNode: "api-1", BootGeneration: bootGeneration, CompletedAt: now, SessionCount: 1}},
		[]playbackSessionRow{{SessionID: "legacy", SessionGeneration: row.SessionGeneration, UserID: 7, ReportingNode: "api-1", StartedAt: now.Add(-time.Second)}},
	)
	if reason != snapshotReasonInvalidIdentity {
		t.Fatalf("legacy sentinel snapshot reason = %q, want invalid identity", reason)
	}
}

func TestSnapshotLoaderDoesNotExposePersistedLegacySentinel(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run snapshot database coverage test")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)
	sessionID := "snapshot-legacy-" + uuid.NewString()
	startedAt := time.Now().UTC().Add(-time.Minute)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO playback_sessions_sync
			(session_id, session_generation, user_id, media_file_id, play_method, reporting_node, started_at, updated_at)
		VALUES ($1, $2::uuid, 7, 0, 'direct', 'api-legacy', $3, $3)
	`, sessionID, playback.LegacySessionGenerationSentinel, startedAt); err != nil {
		t.Fatalf("seed legacy snapshot row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM playback_sessions_sync WHERE session_id=$1`, sessionID)
	})
	loader := NewPlaybackSessionsLoader(pool, nil, nil)
	rows, err := loader.Load(t.Context(), httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions", nil), PlaybackSessionsQuery{})
	if err != nil {
		t.Fatalf("load snapshot rows: %v", err)
	}
	var got *playbackSessionRow
	for i := range rows {
		if rows[i].SessionID == sessionID {
			got = &rows[i]
			break
		}
	}
	if got == nil || got.SessionGeneration != "" {
		t.Fatalf("legacy snapshot row=%+v, want public empty generation", got)
	}
	now := time.Now().UTC()
	boot := uuid.NewString()
	if reason := evaluateSnapshotCompleteness(now,
		[]snapshotReportingNode{{ID: "api-legacy", BootGeneration: boot, UpdatedAt: now}},
		[]snapshotWatermark{{ReportingNode: "api-legacy", BootGeneration: boot, CompletedAt: now, SessionCount: 1}},
		[]playbackSessionRow{*got},
	); reason != snapshotReasonInvalidIdentity {
		t.Fatalf("legacy persisted snapshot reason=%q, want invalid identity", reason)
	}
}

func TestSnapshotCompletenessRequiresFreshReportingCoverage(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	freshNode := snapshotReportingNode{
		ID:             "api-1",
		BootGeneration: "47cf5dbe-65d4-4975-bdc8-039443ebe16d",
		UpdatedAt:      now.Add(-time.Second),
	}
	freshWatermark := snapshotWatermark{
		ReportingNode:  "api-1",
		BootGeneration: freshNode.BootGeneration,
		CompletedAt:    now.Add(-time.Second),
		SessionCount:   0,
	}
	validSession := playbackSessionRow{
		SessionID:         "session-1",
		SessionGeneration: "52ebfda7-2025-49f8-8c1c-0d345043dc10",
		UserID:            7,
		ReportingNode:     "api-1",
		StartedAt:         now.Add(-time.Minute),
	}

	tests := []struct {
		name       string
		nodes      []snapshotReportingNode
		watermarks []snapshotWatermark
		sessions   []playbackSessionRow
		want       string
	}{
		{name: "zero nodes", want: snapshotReasonNoReportingNodes},
		{name: "zero sessions with fresh completed watermark", nodes: []snapshotReportingNode{freshNode}, watermarks: []snapshotWatermark{freshWatermark}},
		{name: "missing watermark", nodes: []snapshotReportingNode{freshNode}, want: snapshotReasonMissingWatermark},
		{name: "watermark from previous process boot", nodes: []snapshotReportingNode{freshNode}, watermarks: []snapshotWatermark{{ReportingNode: "api-1", BootGeneration: "dbcd1710-cf21-4695-b35f-c0b0d5f33889", CompletedAt: now, SessionCount: 0}}, want: snapshotReasonBootGenerationMismatch},
		{name: "orphan zero-session watermark", nodes: []snapshotReportingNode{freshNode}, watermarks: []snapshotWatermark{freshWatermark, {ReportingNode: "api-orphan", CompletedAt: now, SessionCount: 0}}, want: snapshotReasonOrphanReportingNode},
		{name: "persisted whitespace mismatch", nodes: []snapshotReportingNode{{ID: " api-1 ", BootGeneration: freshNode.BootGeneration, UpdatedAt: freshNode.UpdatedAt}}, watermarks: []snapshotWatermark{freshWatermark}, want: snapshotReasonMissingWatermark},
		{name: "blank heartbeat node", nodes: []snapshotReportingNode{{ID: "   ", UpdatedAt: freshNode.UpdatedAt}}, watermarks: []snapshotWatermark{{ReportingNode: "   ", CompletedAt: now}}, want: snapshotReasonStaleHeartbeat},
		{name: "blank watermark node", nodes: []snapshotReportingNode{freshNode}, watermarks: []snapshotWatermark{{ReportingNode: "   ", CompletedAt: now}}, want: snapshotReasonMissingWatermark},
		{name: "stale watermark", nodes: []snapshotReportingNode{freshNode}, watermarks: []snapshotWatermark{{ReportingNode: "api-1", BootGeneration: freshNode.BootGeneration, CompletedAt: now.Add(-time.Minute)}}, want: snapshotReasonStaleWatermark},
		{name: "stale heartbeat", nodes: []snapshotReportingNode{{ID: "api-1", UpdatedAt: now.Add(-time.Minute)}}, watermarks: []snapshotWatermark{freshWatermark}, want: snapshotReasonStaleHeartbeat},
		{name: "count mismatch", nodes: []snapshotReportingNode{freshNode}, watermarks: []snapshotWatermark{{ReportingNode: "api-1", BootGeneration: freshNode.BootGeneration, CompletedAt: now, SessionCount: 2}}, sessions: []playbackSessionRow{validSession}, want: snapshotReasonCountMismatch},
		{name: "orphan reporting node", nodes: []snapshotReportingNode{freshNode}, watermarks: []snapshotWatermark{freshWatermark}, sessions: []playbackSessionRow{{SessionID: "orphan", SessionGeneration: validSession.SessionGeneration, UserID: 7, ReportingNode: "api-2", StartedAt: validSession.StartedAt}}, want: snapshotReasonOrphanReportingNode},
		{name: "invalid generation", nodes: []snapshotReportingNode{freshNode}, watermarks: []snapshotWatermark{{ReportingNode: "api-1", BootGeneration: freshNode.BootGeneration, CompletedAt: now, SessionCount: 1}}, sessions: []playbackSessionRow{{SessionID: "bad", UserID: 7, ReportingNode: "api-1", StartedAt: validSession.StartedAt}}, want: snapshotReasonInvalidIdentity},
		{name: "invalid start", nodes: []snapshotReportingNode{freshNode}, watermarks: []snapshotWatermark{{ReportingNode: "api-1", BootGeneration: freshNode.BootGeneration, CompletedAt: now, SessionCount: 1}}, sessions: []playbackSessionRow{{SessionID: "bad", SessionGeneration: validSession.SessionGeneration, UserID: 7, ReportingNode: "api-1"}}, want: snapshotReasonInvalidStartedAt},
		{name: "future start", nodes: []snapshotReportingNode{freshNode}, watermarks: []snapshotWatermark{{ReportingNode: "api-1", BootGeneration: freshNode.BootGeneration, CompletedAt: now, SessionCount: 1}}, sessions: []playbackSessionRow{{SessionID: "bad", SessionGeneration: validSession.SessionGeneration, UserID: 7, ReportingNode: "api-1", StartedAt: now.Add(time.Minute)}}, want: snapshotReasonInvalidStartedAt},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := evaluateSnapshotCompleteness(now, tc.nodes, tc.watermarks, tc.sessions); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSnapshotRowsExposeStateAndTranscodeFlag(t *testing.T) {
	row := playbackSessionRow{PlayMethod: "remux", TranscodeAudio: true, IsPaused: true}
	enrichPlaybackSessionRow(&row, nil)
	if row.State != "paused" {
		t.Fatalf("state = %q, want paused", row.State)
	}
	if !row.IsTranscoded {
		t.Fatal("audio-transcoded remux must report is_transcoded=true")
	}
}

func TestSnapshotQueryIsUncappedWhileLegacyListRemainsCapped(t *testing.T) {
	snapshotSQL, _ := finishPlaybackSessionsSQL("SELECT * FROM playback_sessions_sync s", PlaybackSessionsQuery{}, false)
	if strings.Contains(snapshotSQL, "LIMIT") {
		t.Fatalf("snapshot SQL is capped: %s", snapshotSQL)
	}
	legacySQL, _ := finishPlaybackSessionsSQL("SELECT * FROM playback_sessions_sync s", PlaybackSessionsQuery{}, true)
	if !strings.Contains(legacySQL, "LIMIT 200") {
		t.Fatalf("legacy SQL lost its compatibility cap: %s", legacySQL)
	}
}

func TestTerminateSessionSnapshotHandlerRegistersOnlyCompleteSnapshotIdentities(t *testing.T) {
	registry := playback.NewSnapshotRegistry(4, SessionSnapshotFreshness)
	handler := NewAdminHandler(nil, nil, nil)
	handler.SnapshotRegistry = registry
	generation := uuid.NewString()
	complete := sessionSnapshotResponse{
		SnapshotID:  "complete",
		GeneratedAt: time.Now().UTC(),
		Complete:    true,
		Sessions: []playbackSessionRow{{
			SessionID:         "session-1",
			SessionGeneration: generation,
		}},
	}
	if err := handler.registerCompleteSnapshot(&complete); err != nil {
		t.Fatalf("register complete snapshot: %v", err)
	}
	if err := registry.Validate("complete", playback.SnapshotSessionIdentity{SessionID: "session-1", Generation: generation}); err != nil {
		t.Fatalf("complete snapshot was not registered: %v", err)
	}

	incomplete := complete
	incomplete.SnapshotID = "incomplete"
	incomplete.Complete = false
	incomplete.IncompleteReason = snapshotReasonCountMismatch
	if err := handler.registerCompleteSnapshot(&incomplete); err != nil {
		t.Fatalf("register incomplete snapshot: %v", err)
	}
	if err := registry.Validate("incomplete", playback.SnapshotSessionIdentity{SessionID: "session-1", Generation: generation}); !errors.Is(err, playback.ErrSnapshotUnknown) {
		t.Fatalf("incomplete snapshot validation error = %v, want ErrSnapshotUnknown", err)
	}
}

func TestSnapshotResponseIsIncompleteWhenRegistryCannotStoreIt(t *testing.T) {
	registry := playback.NewSnapshotRegistry(4, SessionSnapshotFreshness, 1)
	handler := NewAdminHandler(nil, nil, nil)
	handler.SnapshotRegistry = registry
	response := sessionSnapshotResponse{
		SnapshotID:  "too-large",
		GeneratedAt: time.Now().UTC(),
		Complete:    true,
		Sessions: []playbackSessionRow{
			{SessionID: "session-1", SessionGeneration: uuid.NewString()},
			{SessionID: "session-2", SessionGeneration: uuid.NewString()},
		},
	}

	err := handler.registerCompleteSnapshot(&response)
	if !errors.Is(err, playback.ErrSnapshotCapacity) {
		t.Fatalf("register oversized snapshot error = %v, want ErrSnapshotCapacity", err)
	}
	if response.Complete || response.IncompleteReason != "registry_capacity" {
		t.Fatalf("response = %+v, want safe registry-capacity incomplete envelope", response)
	}
	if got := registry.Len(); got != 0 {
		t.Fatalf("registry size = %d, want 0", got)
	}
}

func TestSnapshotCompleteForFreshZeroSessionNode(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run snapshot database coverage test")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), `DELETE FROM playback_sessions_sync; DELETE FROM playback_session_snapshot_watermarks; DELETE FROM node_heartbeats`); err != nil {
		t.Fatalf("clear snapshot tables: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO node_heartbeats (node_id, node_type, updated_at) VALUES ('api-test', 'api', NOW());
		INSERT INTO playback_session_snapshot_watermarks
			(reporting_node, boot_generation, reconciliation_generation, completed_at, session_count)
		VALUES ('api-test', (SELECT boot_generation FROM node_heartbeats WHERE node_id='api-test'), gen_random_uuid(), NOW(), 0)
	`); err != nil {
		t.Fatalf("seed coverage: %v", err)
	}
	handler := NewAdminHandler(nil, pool, nil)
	recorder := httptest.NewRecorder()
	handler.HandleGetSessionsSnapshot(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/snapshot", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var response sessionSnapshotResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Complete || response.IncompleteReason != "" || len(response.Sessions) != 0 {
		t.Fatalf("snapshot = %+v, want complete empty snapshot", response)
	}
}

func TestSnapshotIncompleteAfterSameNodeRestartsBeforeReconciliation(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run snapshot database coverage test")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)

	node := "snapshot-restart-" + uuid.NewString()
	sessionID := "snapshot-restart-session-" + uuid.NewString()
	oldBoot := uuid.New()
	newBoot := uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM playback_sessions_sync WHERE session_id=$1`, sessionID)
		_, _ = pool.Exec(t.Context(), `DELETE FROM playback_session_snapshot_watermarks WHERE reporting_node=$1`, node)
		_, _ = pool.Exec(t.Context(), `DELETE FROM node_heartbeats WHERE node_id=$1`, node)
	})

	oldHeartbeat := worker.NewHeartbeatWriter(pool, node, "api", "", oldBoot)
	oldReconciler := worker.NewReconciler(pool, node, nil, oldBoot)
	oldSnapshot := []worker.SessionSync{{
		SessionID:         sessionID,
		SessionGeneration: uuid.NewString(),
		UserID:            7,
		MediaFileID:       42,
		PlayMethod:        "direct",
		ReportingNode:     node,
		StartedAt:         time.Now().UTC().Add(-time.Minute),
		UpdatedAt:         time.Now().UTC(),
	}}
	if err := oldHeartbeat.Beat(t.Context()); err != nil {
		t.Fatalf("old process heartbeat: %v", err)
	}
	if err := oldReconciler.ReconcileNodeSessions(t.Context(), node, oldSnapshot); err != nil {
		t.Fatalf("old process reconciliation: %v", err)
	}

	handler := NewAdminHandler(nil, pool, nil)
	readSnapshot := func() sessionSnapshotResponse {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.HandleGetSessionsSnapshot(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/snapshot", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		var response sessionSnapshotResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return response
	}
	if response := readSnapshot(); !response.Complete {
		t.Fatalf("old process snapshot = %+v, want complete", response)
	}

	newHeartbeat := worker.NewHeartbeatWriter(pool, node, "api", "", newBoot)
	if err := newHeartbeat.Beat(t.Context()); err != nil {
		t.Fatalf("new process heartbeat: %v", err)
	}
	if response := readSnapshot(); response.Complete || response.IncompleteReason != snapshotReasonBootGenerationMismatch {
		t.Fatalf("pre-reconciliation restart snapshot = %+v, want boot-generation mismatch", response)
	}

	newReconciler := worker.NewReconciler(pool, node, nil, newBoot)
	if err := newReconciler.ReconcileNodeSessions(t.Context(), node, oldSnapshot); err != nil {
		t.Fatalf("new process reconciliation: %v", err)
	}
	if response := readSnapshot(); !response.Complete {
		t.Fatalf("new process snapshot = %+v, want complete after matching reconciliation", response)
	}
}

func TestSnapshotIncompleteForOrphanZeroSessionWatermark(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run snapshot database coverage test")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), `DELETE FROM playback_sessions_sync; DELETE FROM playback_session_snapshot_watermarks; DELETE FROM node_heartbeats`); err != nil {
		t.Fatalf("clear snapshot tables: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO node_heartbeats (node_id, node_type, updated_at) VALUES ('api-test', 'api', NOW());
		INSERT INTO playback_session_snapshot_watermarks
			(reporting_node, boot_generation, reconciliation_generation, completed_at, session_count)
		VALUES ('api-test', (SELECT boot_generation FROM node_heartbeats WHERE node_id='api-test'), gen_random_uuid(), NOW(), 0),
		       ('api-orphan', gen_random_uuid(), gen_random_uuid(), NOW(), 0)
	`); err != nil {
		t.Fatalf("seed orphan coverage: %v", err)
	}
	handler := NewAdminHandler(nil, pool, nil)
	recorder := httptest.NewRecorder()
	handler.HandleGetSessionsSnapshot(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/snapshot", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var response sessionSnapshotResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Complete || response.IncompleteReason != snapshotReasonOrphanReportingNode {
		t.Fatalf("snapshot = %+v, want incomplete orphan reporting node", response)
	}
}

func TestSnapshotIncompleteForPersistedWhitespaceNodeMismatch(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run snapshot database coverage test")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), `DELETE FROM playback_sessions_sync; DELETE FROM playback_session_snapshot_watermarks; DELETE FROM node_heartbeats`); err != nil {
		t.Fatalf("clear snapshot tables: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO node_heartbeats (node_id, node_type, updated_at) VALUES (' api-legacy ', 'api', NOW());
		INSERT INTO playback_session_snapshot_watermarks
			(reporting_node, boot_generation, reconciliation_generation, completed_at, session_count)
		VALUES ('api-legacy', gen_random_uuid(), gen_random_uuid(), NOW(), 0)
	`); err != nil {
		t.Fatalf("seed whitespace-mismatched coverage: %v", err)
	}
	handler := NewAdminHandler(nil, pool, nil)
	recorder := httptest.NewRecorder()
	handler.HandleGetSessionsSnapshot(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/snapshot", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var response sessionSnapshotResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Complete || response.IncompleteReason == "" {
		t.Fatalf("snapshot = %+v, want incomplete whitespace-mismatched coverage", response)
	}
}

func TestSessionComponentDecisionLabelsCopiedAudioDuringHLSAsRemux(t *testing.T) {
	videoDecision, audioDecision := sessionComponentDecision("transcode", false, "copy")

	if videoDecision != "remux" {
		t.Fatalf("videoDecision = %q, want remux", videoDecision)
	}
	if audioDecision != "remux" {
		t.Fatalf("audioDecision = %q, want remux", audioDecision)
	}
}

// TestEffectivePlayMethodBuckets pins the bucket for every decision pair
// sessionComponentDecision can produce, plus the unknown case.
func TestEffectivePlayMethodBuckets(t *testing.T) {
	cases := []struct {
		name           string
		playMethod     string
		transcodeAudio bool
		targetVideo    string
		want           string
	}{
		{"direct play", "direct", false, "", "direct"},
		{"plain remux", "remux", false, "", "remux"},
		{"audio-only re-encode via remux", "remux", true, "", "audio"},
		{"full video transcode", "transcode", true, "h264", "transcode"},
		{"video transcode with copied audio", "transcode", false, "h264", "transcode"},
		{"video-copy HLS repackage", "transcode", false, "copy", "remux"},
		{"video-copy HLS with audio re-encode", "transcode", true, "copy", "audio"},
		// Unknown play_method (stale row from an older node): the bucket must
		// stay empty rather than inventing a method from transcode_audio.
		{"unknown method with transcode_audio set", "hls", true, "", ""},
		{"empty method", "", false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video, audio := sessionComponentDecision(tc.playMethod, tc.transcodeAudio, tc.targetVideo)
			if got := effectivePlayMethod(video, audio); got != tc.want {
				t.Fatalf("effectivePlayMethod(%q, %q) = %q, want %q", video, audio, got, tc.want)
			}
		})
	}
}

// TestSessionsCapabilitiesAdvertisesActivityFields pins the feature-detection
// contract: the additive session fields are omitempty on the wire, so this
// endpoint is how independently deployed clients distinguish an older server
// from a supported one reporting an unknown method / non-Jellyfin session.
func TestSessionsCapabilitiesAdvertisesActivityFields(t *testing.T) {
	rr := httptest.NewRecorder()
	(&AdminHandler{}).HandleGetSessionsCapabilities(rr, httptest.NewRequest(http.MethodGet, "/admin/sessions/capabilities", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp playbackSessionsCapabilitiesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if !resp.EffectivePlayMethod || !resp.IsJellyfinClient {
		t.Fatalf("capabilities must advertise both fields: %+v", resp)
	}
	want := []string{"direct", "remux", "transcode", "audio"}
	if len(resp.EffectivePlayMethodValues) != len(want) {
		t.Fatalf("bucket vocabulary = %v, want %v", resp.EffectivePlayMethodValues, want)
	}
	for i, v := range want {
		if resp.EffectivePlayMethodValues[i] != v {
			t.Fatalf("bucket vocabulary = %v, want %v", resp.EffectivePlayMethodValues, want)
		}
	}
}

func TestIsJellyfinEcosystemClient(t *testing.T) {
	cases := []struct {
		name       string
		clientName string
		userAgent  string
		want       bool
	}{
		{"jellyfin web by name", "Jellyfin Web", "", true},
		{"findroid by name", "Findroid", "Findroid/0.15", true},
		{"infuse by user agent only", "", "Infuse-Direct/8.4.6", true},
		{"kodi addon by name", "Kodi", "Kodi/21.0", true},
		{"mpv shim by user agent", "", "mpv 0.38.0", true},
		{"native android client", "Silo Android", "okhttp/4.12", false},
		{"generic browser", "", "Mozilla/5.0 (X11) Chrome/120.0 Safari/537.36", false},
		{"no metadata", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isJellyfinEcosystemClient(tc.clientName, tc.userAgent); got != tc.want {
				t.Fatalf("isJellyfinEcosystemClient(%q, %q) = %v, want %v", tc.clientName, tc.userAgent, got, tc.want)
			}
		})
	}
}

func TestPlaybackClientDisplayNameAndroidDevices(t *testing.T) {
	const curlClientLabel = "curl"

	cases := []struct {
		name      string
		userAgent string
		want      string
	}{
		{
			name:      "fire tv stick 4k max",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 11; AFTKRT Build/RS8180.3729N)",
			want:      "Fire TV Stick 4K Max",
		},
		{
			name:      "fire tv stick 4k",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 9; AFTMM Build/PS7279)",
			want:      "Fire TV Stick 4K",
		},
		{
			name:      "fire tv stick 4k second generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 11; AFTKM Build/RS8139)",
			want:      "Fire TV Stick 4K (2nd Gen)",
		},
		{
			name:      "fire tv stick 4k max first generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 9; AFTKA Build/PS7646)",
			want:      "Fire TV Stick 4K Max (1st Gen)",
		},
		{
			name:      "fire tv stick third generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 9; AFTSSS Build/PS7279)",
			want:      "Fire TV Stick (3rd Gen)",
		},
		{
			name:      "fire tv stick lite first generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 9; AFTSS Build/PS7279)",
			want:      "Fire TV Stick Lite (1st Gen)",
		},
		{
			name:      "fire tv stick second generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 5.1; AFTT Build/LVY48F)",
			want:      "Fire TV Stick (2nd Gen)",
		},
		{
			name:      "fire tv first generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 4.2; AFTB Build/JDQ39)",
			want:      "Fire TV (1st Gen)",
		},
		{
			name:      "fire tv second generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 5.1; AFTS Build/LVY48F)",
			want:      "Fire TV (2nd Gen)",
		},
		{
			name:      "fire tv third generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 7.1; AFTN Build/NS6265)",
			want:      "Fire TV (3rd Gen)",
		},
		{
			name:      "fire tv cube second generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 9; AFTR Build/PS7646)",
			want:      "Fire TV Cube (2nd Gen)",
		},
		{
			name:      "fire tv cube first generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 7.1; AFTA Build/NS6265)",
			want:      "Fire TV Cube (1st Gen)",
		},
		{
			name:      "unmapped multi-word android model preserved",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 13; Pixel 7 Build/TQ3A)",
			want:      "Android · Pixel 7",
		},
		{
			name:      "shield model",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 11; SHIELD Android TV Build/RQ1A)",
			want:      "NVIDIA Shield",
		},
		{
			name:      "chrome remains browser label",
			userAgent: "Mozilla/5.0 (X11; Linux x86_64) Chrome/120.0.0.0 Safari/537.36",
			want:      "Chrome 120",
		},
		{
			name:      "non-android build user agent keeps existing fallback",
			userAgent: "curl/8.0 (Linux; Device Build/42)",
			want:      curlClientLabel,
		},
		{
			name:      "explicit client fallback wins over android device model",
			userAgent: "curl/8.0 (Linux; U; Android 13; Pixel 7 Build/TQ3A)",
			want:      curlClientLabel,
		},
		{
			name:      "android substring is not an android platform token",
			userAgent: "Dalvik/2.1.0 (Linux; U; NotAndroid 13; Pixel 7 Build/TQ3A)",
			want:      "Dalvik",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := playbackClientDisplayName("", "", tc.userAgent)
			if got != tc.want {
				t.Fatalf("playbackClientDisplayName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEnrichPlaybackSessionRowUsesCompatOrigin(t *testing.T) {
	row := playbackSessionRow{
		ClientName:      "Unrecognized Client",
		ClientUserAgent: "Dalvik/2.1.0",
		CompatOrigin:    true,
	}

	enrichPlaybackSessionRow(&row, nil)

	if !row.IsJellyfinClient {
		t.Fatal("compat-origin session must be marked as a Jellyfin client")
	}
}
