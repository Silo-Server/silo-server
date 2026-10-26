package catalog

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type playableTargetSQLCapture struct {
	query string
	args  []any
}

func (c *playableTargetSQLCapture) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(strings.TrimSpace(data.SQL), "WITH requested AS") && strings.Contains(data.SQL, "SELECT COALESCE(") {
		c.query, c.args = data.SQL, slices.Clone(data.Args)
	}
	return ctx
}

func (*playableTargetSQLCapture) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type playableTargetPlan struct {
	Relation       string               `json:"Relation Name"`
	Index          string               `json:"Index Name"`
	Rows           float64              `json:"Actual Rows"`
	Loops          float64              `json:"Actual Loops"`
	JoinFilterRows float64              `json:"Rows Removed by Join Filter"`
	Children       []playableTargetPlan `json:"Plans"`
}

func (p playableTargetPlan) work() (joinComparisons, progressLookupLoops float64) {
	joinComparisons = p.JoinFilterRows * p.Loops
	if p.Relation == "user_watch_progress" && strings.HasSuffix(p.Index, "_pkey") && p.Rows <= 1 {
		progressLookupLoops = p.Loops
	}
	for _, child := range p.Children {
		joins, lookups := child.work()
		joinComparisons += joins
		progressLookupLoops += lookups
	}
	return joinComparisons, progressLookupLoops
}

// Statistics can briefly describe an empty profile after a bulk history import.
// Ensure completed membership remains a unique-key lookup instead of comparing
// every requested episode with every materialized profile progress row.
func TestPlayableTargetResolverStaleCompletedProgressPlan(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	capture := &playableTargetSQLCapture{}
	cfg.ConnConfig.Tracer = capture
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	const episodeCount = 4200
	_, err = pool.Exec(t.Context(), `
		CREATE TEMP TABLE media_folders (id integer PRIMARY KEY, enabled boolean);
		CREATE TEMP TABLE episodes (content_id text PRIMARY KEY, series_id text, season_number integer, episode_number integer);
		CREATE INDEX ON episodes (series_id, season_number, episode_number);
		CREATE TEMP TABLE seasons (content_id text, series_id text, season_number integer);
		CREATE TEMP TABLE media_files (id integer PRIMARY KEY, content_id text, episode_id text, media_folder_id integer, resolution text, missing_since timestamptz);
		CREATE INDEX ON media_files (content_id);
		CREATE INDEX ON media_files (episode_id);
		CREATE TEMP TABLE user_watch_progress (user_id integer, profile_id text, media_item_id text,
			position_seconds double precision, completed boolean, updated_at timestamptz,
			PRIMARY KEY (user_id, profile_id, media_item_id));
		CREATE INDEX ON user_watch_progress (user_id, profile_id, updated_at DESC) WHERE position_seconds > 0;
		CREATE TEMP TABLE user_history_hidden_items (user_id integer, profile_id text, media_item_id text,
			hidden_before timestamptz, PRIMARY KEY (user_id, profile_id, media_item_id));
		INSERT INTO media_folders VALUES (1, TRUE);
		ANALYZE user_watch_progress; ANALYZE user_history_hidden_items;
	`)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO episodes SELECT 'episode-' || n, 'series', 1, n FROM generate_series(1, $1) n`,
		`INSERT INTO media_files SELECT n, 'series', 'episode-' || n, 1, '1080p', NULL FROM generate_series(1, $1) n`,
		`INSERT INTO user_watch_progress SELECT 1, 'profile', 'episode-' || n, 0, TRUE, now() FROM generate_series(1, $1) n`,
	} {
		if _, err := pool.Exec(t.Context(), statement, episodeCount); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(t.Context(), `ANALYZE media_folders; ANALYZE episodes; ANALYZE media_files`); err != nil {
		t.Fatal(err)
	}
	store, err := pgstore.NewPostgresProvider(pool).ForUser(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	input := PlayableTargetInput{ContentID: "series", Type: "series"}
	resolver := NewPlayableTargetResolver(pool)
	query := PlayableTargetQuery{
		UserID: 1, ProfileID: "profile", Items: []PlayableTargetInput{input},
		ProgressStore: store, Access: AccessFilter{AllowedLibraryIDs: []int{1}},
	}
	targets, err := resolver.Resolve(t.Context(), query)
	if err != nil || targets[input.Key()] != "episode-1" {
		t.Fatalf("all-completed target = %#v, err %v; want episode-1", targets, err)
	}
	if capture.query == "" {
		t.Fatal("PostgreSQL winner query was not captured")
	}
	var raw []byte
	if err := pool.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON) "+capture.query, capture.args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []struct {
		Plan playableTargetPlan `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &documents); err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 {
		t.Fatalf("expected one query plan, got %d", len(documents))
	}
	comparisons, lookups := documents[0].Plan.work()
	if comparisons > episodeCount*2 || lookups < episodeCount {
		t.Fatalf("stale profile caused %.0f join comparisons and %.0f unique progress lookups; want linear comparisons and at least %d unique lookups\n%s",
			comparisons, lookups, episodeCount, raw)
	}
	t.Logf("stale completed profile: %.0f join comparisons, %.0f unique progress lookups for %d episodes", comparisons, lookups, episodeCount)

	// Ordinary unseen cards should stop near the first indexed episode rather
	// than check progress for every episode merely to sort CASE season order.
	if _, err := pool.Exec(t.Context(), `DELETE FROM user_watch_progress`); err != nil {
		t.Fatal(err)
	}
	targets, err = resolver.Resolve(t.Context(), query)
	if err != nil || targets[input.Key()] != "episode-1" {
		t.Fatalf("unseen target = %#v, err %v; want episode-1", targets, err)
	}
	if err := pool.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON) "+capture.query, capture.args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	documents = nil
	if err := json.Unmarshal(raw, &documents); err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 {
		t.Fatalf("expected one unseen query plan, got %d", len(documents))
	}
	comparisons, lookups = documents[0].Plan.work()
	if comparisons > episodeCount*2 || lookups < 1 || lookups >= episodeCount/2 {
		t.Fatalf("unseen profile caused %.0f join comparisons and %.0f unique progress lookups; want an early indexed winner\n%s", comparisons, lookups, raw)
	}
	t.Logf("unseen profile: %.0f unique progress lookups for %d episodes", lookups, episodeCount)
}
