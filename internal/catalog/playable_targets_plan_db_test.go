package catalog

import (
	"context"
	"encoding/json"
	"fmt"
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

func (*playableTargetSQLCapture) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (*playableTargetSQLCapture) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (*playableTargetSQLCapture) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}

// The resolver sends its statement inside a batch with the JIT guard.
func (c *playableTargetSQLCapture) TraceBatchQuery(_ context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	if strings.HasPrefix(strings.TrimSpace(data.SQL), "WITH requested AS") && strings.Contains(data.SQL, "SELECT COALESCE(") {
		c.query, c.args = data.SQL, slices.Clone(data.Args)
	}
}

func (*playableTargetSQLCapture) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

type playableTargetPlan struct {
	Relation       string               `json:"Relation Name"`
	Index          string               `json:"Index Name"`
	Rows           float64              `json:"Actual Rows"`
	Loops          float64              `json:"Actual Loops"`
	JoinFilterRows float64              `json:"Rows Removed by Join Filter"`
	FilterRows     float64              `json:"Rows Removed by Filter"`
	Children       []playableTargetPlan `json:"Plans"`
}

// episodeRowsRead counts the episode rows the plan's scans read, including
// those a filter then discarded.
func (p playableTargetPlan) episodeRowsRead() float64 {
	var rows float64
	if p.Relation == "episodes" {
		rows = (p.Rows + p.FilterRows) * p.Loops
	}
	for _, child := range p.Children {
		rows += child.episodeRowsRead()
	}
	return rows
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

func (p playableTargetPlan) progressRows() float64 {
	var rows float64
	if p.Relation == "user_watch_progress" {
		rows = p.Rows * p.Loops
	}
	for _, child := range p.Children {
		rows += child.progressRows()
	}
	return rows
}

// explainPlayableTargetPlan runs EXPLAIN ANALYZE on the generic plan the
// resolver keeps for its statement.
func explainPlayableTargetPlan(t *testing.T, pool *pgxpool.Pool, query string, args []any) []byte {
	t.Helper()
	ctx := t.Context()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SET plan_cache_mode = force_generic_plan; PREPARE playable_target_plan AS "+query); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "DEALLOCATE playable_target_plan; RESET plan_cache_mode")
	}()
	params := make([]string, len(args))
	for i := range args {
		params[i] = fmt.Sprintf("$%d", i+1)
	}
	// EXECUTE takes its arguments as SQL text, so pgx interpolates them.
	var raw []byte
	explain := "EXPLAIN (ANALYZE, FORMAT JSON) EXECUTE playable_target_plan(" + strings.Join(params, ", ") + ")"
	if err := conn.QueryRow(ctx, explain, append([]any{pgx.QueryExecModeSimpleProtocol}, args...)...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
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
		CREATE TEMP TABLE media_files (id integer PRIMARY KEY, content_id text, episode_id text, media_folder_id integer, resolution text, missing_since timestamptz,
			multi_episode_start integer, multi_episode_end integer);
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
	raw := explainPlayableTargetPlan(t, pool, capture.query, capture.args)
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
	raw = explainPlayableTargetPlan(t, pool, capture.query, capture.args)
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

	// A page of series cards without anchors reads the profile's resumable
	// progress once per statement, not once per card.
	const cardCount, resumableCount = 60, 500
	for _, statement := range []string{
		`INSERT INTO episodes SELECT 'card-' || s || '-' || e, 'card-' || s, 1, e FROM generate_series(1, $1) s, generate_series(1, 2) e`,
		`INSERT INTO media_files SELECT 100000 + 2 * s + e, 'card-' || s, 'card-' || s || '-' || e, 1, '1080p', NULL FROM generate_series(1, $1) s, generate_series(1, 2) e`,
	} {
		if _, err := pool.Exec(t.Context(), statement, cardCount); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO user_watch_progress
		SELECT 1, 'profile', 'episode-' || n, 60, FALSE, now() - n * interval '1 second' FROM generate_series(1, $1) n
		UNION ALL SELECT 1, 'profile', 'card-7-2', 60, FALSE, now()
	`, resumableCount); err != nil {
		t.Fatal(err)
	}
	query.Items = nil
	for card := 1; card <= cardCount; card++ {
		query.Items = append(query.Items, PlayableTargetInput{ContentID: fmt.Sprintf("card-%d", card), Type: "series"})
	}
	targets, err = resolver.Resolve(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range query.Items {
		want := card.ContentID + "-1"
		if card.ContentID == "card-7" {
			want = "card-7-2"
		}
		if targets[card.Key()] != want {
			t.Fatalf("card %s target = %q, want %q", card.ContentID, targets[card.Key()], want)
		}
	}
	raw = explainPlayableTargetPlan(t, pool, capture.query, capture.args)
	documents = nil
	if err := json.Unmarshal(raw, &documents); err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 {
		t.Fatalf("expected one page query plan, got %d", len(documents))
	}
	rows := documents[0].Plan.progressRows()
	if rows > 2*resumableCount {
		t.Fatalf("%d cards read %.0f progress rows; want at most %d, since a pass per card reads %d\n%s",
			cardCount, rows, 2*resumableCount, cardCount*resumableCount, raw)
	}
	t.Logf("%d cards read %.0f progress rows with %d resumable", cardCount, rows, resumableCount)

	// Season cards of a long series each read their own season. Only every
	// season's last episode has a file, so each card walks its whole season.
	const seasonCount, seasonLength = 30, 100
	for _, statement := range []string{
		`INSERT INTO episodes SELECT 'long-' || s || '-' || e, 'long', s, e FROM generate_series(1, $1) s, generate_series(1, $2) e`,
		`INSERT INTO media_files SELECT 200000 + s, 'long', 'long-' || s || '-' || $2::integer, 1, '1080p', NULL FROM generate_series(1, $1) s`,
	} {
		if _, err := pool.Exec(t.Context(), statement, seasonCount, seasonLength); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(t.Context(), `ANALYZE episodes; ANALYZE media_files`); err != nil {
		t.Fatal(err)
	}
	query.Items = nil
	for season := 1; season <= seasonCount; season++ {
		query.Items = append(query.Items, PlayableTargetInput{
			ContentID: fmt.Sprintf("long-season-%d", season), Type: "season", SeriesID: "long", SeasonNumber: &season,
		})
	}
	targets, err = resolver.Resolve(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range query.Items {
		if want := fmt.Sprintf("long-%d-%d", *card.SeasonNumber, seasonLength); targets[card.Key()] != want {
			t.Fatalf("season card %s target = %q, want %q", card.ContentID, targets[card.Key()], want)
		}
	}
	raw = explainPlayableTargetPlan(t, pool, capture.query, capture.args)
	documents = nil
	if err := json.Unmarshal(raw, &documents); err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 {
		t.Fatalf("expected one season query plan, got %d", len(documents))
	}
	// The resumable pass reads one episode per resumable progress row.
	episodeRows := documents[0].Plan.episodeRowsRead()
	if limit := float64(2*seasonCount*seasonLength + 2*resumableCount); episodeRows > limit {
		t.Fatalf("%d season cards read %.0f episode rows; want at most %.0f, since walking the series per card reads %d\n%s",
			seasonCount, episodeRows, limit, seasonCount*seasonCount*seasonLength, raw)
	}
	t.Logf("%d season cards read %.0f episode rows for %d episodes", seasonCount, episodeRows, seasonCount*seasonLength)
}
