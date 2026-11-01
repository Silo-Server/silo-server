package worker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSessionSyncRowJSONMatchesTheSyncedRowDB pins that a session rendered by
// SessionSyncRowJSON reads back, through jsonb_populate_record, as exactly
// the row a sync stores for it, so a lifecycle event built from the rendered
// row shows what the live list shows.
func TestSessionSyncRowJSONMatchesTheSyncedRowDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var account int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "sync-row-"+uuid.NewString()).Scan(&account); err != nil {
		t.Fatal(err)
	}
	node := "sync-row-node-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM playback_sessions_sync WHERE reporting_node=$1`, node)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, account)
	})
	provider := "lan"
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := SessionSync{
		SessionID: uuid.NewString(), UserID: account, ProfileID: "p-1", MediaFileID: 7, RequestedMediaFileID: 8,
		PlayMethod: "transcode", StartedAt: now.Add(-time.Hour), UpdatedAt: now, ClientIP: "203.0.113.9",
		StreamLocation: "remote", ClientName: "Silo", ClientVersion: "2.0", ClientBuild: "42", ClientChannel: "beta",
		ClientUserAgent: "ua", AudioTrackIndex: 1, TranscodeAudio: true, StreamBitrateKbps: 8000,
		TranscodeNodeURL: "http://node", TargetResolution: "1080p", TargetVideoCodec: "h264", TargetAudioCodec: "aac",
		TargetAudioChannels: 2, TargetBitrateKbps: 6000, TranscodeHWAccel: "qsv", ToneMapMode: "zscale",
		RoutingNetworkProvider: &provider, RoutingWorkload: "transcode", RoutingExecution: "node",
		RoutingExecutionNodeID: 3, RoutingExecutionNodeURL: "http://node", RoutingEgress: "proxy",
		RoutingEgressNodeID: 4, RoutingEgressNodeURL: "http://proxy", PositionSeconds: 812.5, IsPaused: true,
		HasWebSocket: true, IsJellyfinCompat: true, OutputContainer: "fmp4", OutputProtocol: "hls",
		DeviceID: "tv-1", DevicePlatform: "tvOS",
	}
	reconciler := NewReconciler(pool, node, nil)
	if err := reconciler.ReconcileNodeSessions(ctx, node, []SessionSync{session}); err != nil {
		t.Fatal(err)
	}
	rendered, err := SessionSyncRowJSON(session, node)
	if err != nil {
		t.Fatal(err)
	}
	var stored, read string
	if err := pool.QueryRow(ctx, `
		SELECT (to_jsonb(s) - 'last_sync_at')::text,
		       (to_jsonb(jsonb_populate_record(NULL::playback_sessions_sync, $2::jsonb)) - 'last_sync_at')::text
		FROM playback_sessions_sync s WHERE s.session_id = $1`, session.SessionID, string(rendered)).Scan(&stored, &read); err != nil {
		t.Fatal(err)
	}
	if stored != read {
		t.Fatalf("rendered row differs from the synced row:\nsynced   %s\nrendered %s", stored, read)
	}
}
