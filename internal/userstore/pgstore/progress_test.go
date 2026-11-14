package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestCompactMediaItemIDsReturnsEmptySliceForNilInput(t *testing.T) {
	ids := compactMediaItemIDs(nil)
	if ids == nil {
		t.Fatal("compactMediaItemIDs(nil) returned nil, want empty slice for pgx array binding")
	}
	if len(ids) != 0 {
		t.Fatalf("len = %d, want 0", len(ids))
	}
}

// oneSeriesRollupCapture records the one-series rollups, which go out in a
// batch with their plan setting.
type oneSeriesRollupCapture struct {
	statements []oneSeriesRollupStatement
}

type oneSeriesRollupStatement struct {
	query string
	args  []any
}

func (*oneSeriesRollupCapture) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (*oneSeriesRollupCapture) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (*oneSeriesRollupCapture) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}

func (c *oneSeriesRollupCapture) TraceBatchQuery(_ context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	if strings.Contains(data.SQL, "e.series_id = $3") {
		c.statements = append(c.statements, oneSeriesRollupStatement{data.SQL, slices.Clone(data.Args)})
	}
}

func (*oneSeriesRollupCapture) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

type rollupPlanNode struct {
	Relation string           `json:"Relation Name"`
	Rows     float64          `json:"Actual Rows"`
	Loops    float64          `json:"Actual Loops"`
	Children []rollupPlanNode `json:"Plans"`
}

func (n rollupPlanNode) historyRows() float64 {
	var rows float64
	if n.Relation == "user_watch_history" {
		rows = n.Rows * n.Loops
	}
	for _, child := range n.Children {
		rows += child.historyRows()
	}
	return rows
}

// A one-series rollup runs on a generic plan, which prices the completed
// history check for the average profile. It must still look up each episode's
// history rather than hash the whole history of a profile that watched far
// more than the average.
func TestOneSeriesWatchCountsLooksUpHistoryPerEpisode(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// One connection keeps the temporary tables below in scope.
	cfg.MaxConns = 1
	capture := &oneSeriesRollupCapture{}
	cfg.ConnConfig.Tracer = capture
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	const episodeCount = 200
	if _, err := pool.Exec(t.Context(), `
		CREATE TEMP TABLE episodes (content_id text PRIMARY KEY, series_id text, season_number integer, episode_number integer);
		CREATE INDEX ON episodes (series_id, season_number, episode_number);
		CREATE TEMP TABLE episode_libraries (episode_id text, media_folder_id integer, PRIMARY KEY (episode_id, media_folder_id));
		CREATE TEMP TABLE user_watch_progress (user_id integer, profile_id text, media_item_id text,
			position_seconds double precision, completed boolean, updated_at timestamptz,
			PRIMARY KEY (user_id, profile_id, media_item_id));
		CREATE TEMP TABLE user_history_hidden_items (user_id integer, profile_id text, media_item_id text,
			hidden_before timestamptz, PRIMARY KEY (user_id, profile_id, media_item_id));
		CREATE TEMP TABLE user_watch_history (user_id integer, profile_id text, media_item_id text, completed boolean, watched_at timestamptz);
		CREATE INDEX ON user_watch_history (user_id, profile_id, media_item_id, watched_at DESC) WHERE completed;
		-- Twenty series of 200 episodes in seasons of 100.
		INSERT INTO episodes SELECT 'episode-' || n, 'series-' || (1 + (n - 1) / 200), 1 + (n - 1) / 100, n FROM generate_series(1, 4000) n;
		INSERT INTO episode_libraries SELECT content_id, 1 FROM episodes;
		-- A hundred profiles watched 200 titles each. The heavy profile
		-- watched 20,000 other titles and the first 40 episodes of series-1,
		-- and hid some titles.
		INSERT INTO user_watch_history SELECT 1, 'light-' || p, 'movie-' || n, TRUE, now() FROM generate_series(1, 100) p, generate_series(1, 200) n;
		INSERT INTO user_watch_history SELECT 1, 'heavy', 'movie-' || n, TRUE, now() FROM generate_series(1, 20000) n;
		INSERT INTO user_watch_history SELECT 1, 'heavy', 'episode-' || n, TRUE, now() FROM generate_series(1, 40) n;
		INSERT INTO user_history_hidden_items SELECT 1, 'heavy', 'movie-' || n, now() - interval '1 day' FROM generate_series(1, 700) n;
		INSERT INTO user_watch_progress VALUES (1, 'heavy', 'episode-41', 60, FALSE, now());
		ANALYZE episodes; ANALYZE episode_libraries; ANALYZE user_watch_progress;
		ANALYZE user_history_hidden_items; ANALYZE user_watch_history;
	`); err != nil {
		t.Fatal(err)
	}
	store := newStore(pool, 1)
	want := userstore.SeriesWatchCounts{TotalEpisodes: episodeCount, WatchedCount: 40, InProgressCount: 1}
	series, err := store.SeriesEpisodeWatchCounts(t.Context(), "heavy", []string{"series-1"})
	if err != nil || series["series-1"] != want {
		t.Fatalf("one series = %+v, err %v; want %+v", series, err, want)
	}
	page, err := store.SeriesEpisodeWatchCounts(t.Context(), "heavy", []string{"series-1", "series-2"})
	if err != nil || page["series-1"] != want {
		t.Fatalf("series on a page = %+v, err %v; want %+v", page, err, want)
	}
	seasons, err := store.SeriesSeasonWatchCounts(t.Context(), "heavy", "series-1")
	if err != nil {
		t.Fatal(err)
	}
	wantSeasons := map[int]userstore.SeriesWatchCounts{
		1: {TotalEpisodes: 100, WatchedCount: 40, InProgressCount: 1},
		2: {TotalEpisodes: 100},
	}
	if fmt.Sprint(seasons) != fmt.Sprint(wantSeasons) {
		t.Fatalf("seasons = %+v, want %+v", seasons, wantSeasons)
	}
	if len(capture.statements) != 2 {
		t.Fatalf("captured %d one-series rollups, want 2", len(capture.statements))
	}

	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for _, statement := range capture.statements {
		if _, err := conn.Exec(t.Context(), "SET plan_cache_mode = force_generic_plan; PREPARE one_series_rollup AS "+statement.query); err != nil {
			t.Fatal(err)
		}
		// EXECUTE takes its arguments as SQL text, so pgx interpolates them.
		var raw []byte
		err := conn.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON) EXECUTE one_series_rollup($1, $2, $3)",
			append([]any{pgx.QueryExecModeSimpleProtocol}, statement.args...)...).Scan(&raw)
		if _, deallocErr := conn.Exec(context.WithoutCancel(t.Context()), "DEALLOCATE one_series_rollup; RESET plan_cache_mode"); deallocErr != nil {
			t.Fatal(deallocErr)
		}
		if err != nil {
			t.Fatal(err)
		}
		var documents []struct {
			Plan rollupPlanNode `json:"Plan"`
		}
		if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
			t.Fatalf("plan documents = %d, err %v", len(documents), err)
		}
		if rows := documents[0].Plan.historyRows(); rows > episodeCount {
			t.Fatalf("generic plan read %.0f history rows for %d episodes; want at most one per episode\n%s", rows, episodeCount, raw)
		}
	}
}
