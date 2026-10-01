package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdminSessionPagesBeyondBridgeLimitPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL not set")
	}
	root, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := "identity_sessions_" + uuid.NewString()[:8]
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := root.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = root.Exec(context.WithoutCancel(t.Context()), "DROP SCHEMA "+quoted+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, table := range []string{"playback_sessions_sync", "users", "media_files", "media_items", "episodes", "stream_nodes"} {
		if _, err := pool.Exec(t.Context(), "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" (LIKE public."+pgx.Identifier{table}.Sanitize()+" INCLUDING DEFAULTS)"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO playback_sessions_sync(session_id,user_id,profile_id,media_file_id,play_method,reporting_node,started_at,updated_at)
 SELECT 'session-'||lpad(i::text,3,'0'),1,'primary',0,'direct_play','api',now()+i*interval '1 second',now() FROM generate_series(1,205) i`); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(t.Context(), `UPDATE playback_sessions_sync SET routing_network_provider = CASE WHEN session_id = 'session-001' THEN 'tailscale' ELSE '' END, stream_location = CASE WHEN session_id = 'session-001' THEN 'local' ELSE 'remote' END WHERE session_id IN ('session-001', 'session-002')`); err != nil {
		t.Fatal(err)
	}
	loader := handlers.NewPlaybackSessionsLoader(pool, nil, nil)
	legacy, err := loader.Load(t.Context(), handlers.PlaybackSessionsQuery{})
	if err != nil || len(legacy) != 200 || legacy[0].SessionID != "session-205" || legacy[199].SessionID != "session-006" {
		t.Fatalf("bridge cap/order changed: count=%d err=%v", len(legacy), err)
	}
	deps := pilotDeps(nil, nil)
	deps.AdminPlaybackSessions = &handlers.AdminHandler{SessionsLoader: loader}
	h := NewHandler(deps)
	ids := []string{}
	cursor := ""
	for pageNumber := 0; pageNumber < 3; pageNumber++ {
		rec := do(t, h, "GET", Prefix+"/admin/sessions?limit=100&cursor="+url.QueryEscape(cursor), "", bearer(adminToken))
		var page Collection[AdminPlaybackSession]
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || page.Page == nil {
			t.Fatal(rec.Code, rec.Body.String())
		}
		want := 100
		if pageNumber == 2 {
			want = 5
		}
		if len(page.Items) != want || page.Page.HasMore != (pageNumber < 2) {
			t.Fatalf("page%d: %s", pageNumber, rec.Body.String())
		}
		for _, row := range page.Items {

			switch row.SessionID {
			case "session-001":
				if row.RoutingNetworkProvider == nil || *row.RoutingNetworkProvider != "tailscale" || row.StreamLocation != "local" {
					t.Fatalf("overlay provider: %#v", row)
				}
			case "session-002":
				if row.RoutingNetworkProvider == nil || *row.RoutingNetworkProvider != "" {
					t.Fatalf("default provider: %#v", row)
				}
			default:
				if row.RoutingNetworkProvider != nil {
					t.Fatalf("unknown provider: %#v", row)
				}
			}
			ids = append(ids, row.SessionID)
		}
		cursor = page.Page.NextCursor
	}
	for i, id := range ids {
		if want := fmt.Sprintf("session-%03d", i+1); id != want {
			t.Fatal(i, id, want)
		}
	}
	if len(ids) != 205 {
		t.Fatal(len(ids))
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO playback_sessions_sync(session_id,user_id,profile_id,media_file_id,play_method,reporting_node,started_at,updated_at)
	 SELECT 'account-two-'||i,2,'private-profile',0,'direct_play','api',now()+i*interval '1 second',now() FROM generate_series(1,3) i`); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, "GET", Prefix+"/admin/sessions?user_id=2&limit=1", "", bearer(adminToken))
	var filtered Collection[AdminPlaybackSession]
	if err := json.Unmarshal(rec.Body.Bytes(), &filtered); err != nil || rec.Code != 200 || len(filtered.Items) != 1 || filtered.Items[0].UserID != "2" || !filtered.Page.HasMore {
		t.Fatal(rec.Code, rec.Body.String(), err)
	}
	pageCursor := url.QueryEscape(filtered.Page.NextCursor)
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/sessions?user_id=1&limit=1&cursor="+pageCursor, "", bearer(adminToken)), TypeInvalidCursor)
	for _, tc := range []struct {
		query        string
		count, items int
	}{
		{"user_id=2&limit=2", 3, 2}, {"user_id=1&limit=1", 205, 1}, {"user_id=3", 0, 0}, {"limit=1", 208, 1},
	} {
		rec := do(t, h, "GET", Prefix+"/admin/sessions/summary?"+tc.query, "", bearer(adminToken))
		var summary AdminPlaybackSummaryOutput
		if err := json.Unmarshal(rec.Body.Bytes(), &summary.Body); err != nil || rec.Code != 200 || summary.Body.Count != tc.count || len(summary.Body.Items) != tc.items {
			t.Fatal(tc.query, rec.Code, rec.Body.String(), err)
		}
		if summary.Body.Items == nil {
			t.Fatal("empty summary must be an array")
		}
	}

}
