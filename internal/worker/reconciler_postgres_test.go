package worker

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Exercise the real migration, snapshot read, upsert and replacement against
// an isolated schema in the disposable CI database, never production data.
func TestReconcilerOutputFormatPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("activity_format_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE") }()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `CREATE TABLE playback_sessions_sync (
		session_id text PRIMARY KEY, user_id integer, profile_id text, media_file_id integer,
		requested_media_file_id integer, play_method text, reporting_node text,
		started_at timestamptz, updated_at timestamptz, last_sync_at timestamptz, client_ip inet,
		client_name text, client_version text, client_build text, client_channel text, client_user_agent text,
		audio_track_index integer, transcode_audio boolean, stream_bitrate_kbps integer, transcode_node_url text,
		target_resolution text, target_video_codec text, target_audio_codec text, target_audio_channels integer,
		target_bitrate_kbps integer, transcode_hw_accel text, tone_map_mode text,
		routing_workload text, routing_execution text, routing_execution_node_id integer, routing_execution_node_url text,
		routing_egress text, routing_egress_node_id integer, routing_egress_node_url text,
		position_seconds double precision, is_paused boolean, has_websocket boolean, compat_origin boolean
	)`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260903013902_add_activity_output_format.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(migration), "-- +goose Down")[0]
	up = strings.ReplaceAll(up, "public.playback_sessions_sync", quoted+".playback_sessions_sync")
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	session := SessionSync{SessionID: "example-session", UserID: 1, MediaFileID: 42, PlayMethod: "remux",
		ReportingNode: "example-node", StartedAt: time.Now(), UpdatedAt: time.Now(), OutputContainer: "fmp4", OutputProtocol: "hls"}
	reconciler := NewReconciler(pool, session.ReportingNode, nil)
	for _, format := range [][2]string{{"fmp4", "hls"}, {"mpegts", "hls"}, {"fmp4", "http"}, {"", ""}} {
		session.OutputContainer, session.OutputProtocol = format[0], format[1]
		if err := reconciler.ReconcileNodeSessions(ctx, session.ReportingNode, []SessionSync{session}); err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got, err := loadNodeSessionsSnapshot(ctx, tx, session.ReportingNode)
		_ = tx.Rollback(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].OutputContainer != format[0] || got[0].OutputProtocol != format[1] {
			t.Fatalf("output format did not round trip: %#v", got)
		}
	}
}
