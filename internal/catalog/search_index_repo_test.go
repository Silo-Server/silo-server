package catalog

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recordingSearchIndexExecer struct {
	query string
	args  []any
	calls int
}

func (e *recordingSearchIndexExecer) Exec(_ context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	e.query = sql
	e.args = arguments
	e.calls++
	return pgconn.CommandTag{}, nil
}

func TestEnqueueSearchIndexUpsertsUsesSingleBulkOutboxInsert(t *testing.T) {
	execer := &recordingSearchIndexExecer{}

	err := EnqueueSearchIndexUpserts(context.Background(), execer, []string{" movie-1 ", "", "series-1", "movie-1"})
	if err != nil {
		t.Fatalf("EnqueueSearchIndexUpserts returned error: %v", err)
	}

	if execer.calls != 1 {
		t.Fatalf("expected one Exec call, got %d", execer.calls)
	}
	if !strings.Contains(execer.query, "INSERT INTO catalog_search_index_events") {
		t.Fatalf("bulk enqueue query did not insert search index events: %s", execer.query)
	}
	if !strings.Contains(execer.query, "FROM unnest($3::text[])") {
		t.Fatalf("bulk enqueue query did not use unnested content IDs: %s", execer.query)
	}
	if strings.Contains(execer.query, "server_settings") {
		t.Fatalf("bulk enqueue query must not depend on current provider setting: %s", execer.query)
	}
	if got, want := execer.args[0], SearchProviderMeilisearch; got != want {
		t.Fatalf("provider arg = %v, want %v", got, want)
	}
	if got, want := execer.args[1], SearchIndexEventUpsert; got != want {
		t.Fatalf("action arg = %v, want %v", got, want)
	}
	gotIDs, ok := execer.args[2].([]string)
	if !ok {
		t.Fatalf("content IDs arg has type %T, want []string", execer.args[2])
	}
	if want := []string{"movie-1", "series-1"}; !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("content IDs = %#v, want %#v", gotIDs, want)
	}
}

func TestEnqueueSearchIndexUpsertsSkipsEmptyInput(t *testing.T) {
	execer := &recordingSearchIndexExecer{}

	if err := EnqueueSearchIndexUpserts(context.Background(), execer, []string{"", "  "}); err != nil {
		t.Fatalf("EnqueueSearchIndexUpserts returned error: %v", err)
	}
	if execer.calls != 0 {
		t.Fatalf("expected no Exec calls for empty input, got %d", execer.calls)
	}
}

func TestEnqueueSearchIndexUpsertSkipsWhenProviderIsPostgres(t *testing.T) {
	execer := &recordingSearchIndexExecer{}
	repo := NewSearchIndexEventRepository(nil).WithActiveProvider(SearchProviderPostgres)

	if err := repo.EnqueueUpsert(context.Background(), execer, "movie-1"); err != nil {
		t.Fatalf("EnqueueUpsert returned error: %v", err)
	}
	if execer.calls != 0 {
		t.Fatalf("expected no Exec calls when provider is postgres, got %d", execer.calls)
	}
}

func TestEnqueueSearchIndexUpsertRunsWhenProviderIsMeilisearch(t *testing.T) {
	execer := &recordingSearchIndexExecer{}
	repo := NewSearchIndexEventRepository(nil).WithActiveProvider(SearchProviderMeilisearch)

	if err := repo.EnqueueUpsert(context.Background(), execer, "movie-1"); err != nil {
		t.Fatalf("EnqueueUpsert returned error: %v", err)
	}
	if execer.calls != 1 {
		t.Fatalf("expected one Exec call when provider is meilisearch, got %d", execer.calls)
	}
}

func TestItemRepositoryActiveProviderDisablesSearchIndexEvents(t *testing.T) {
	repo := (&ItemRepository{}).WithActiveSearchProvider(SearchProviderPostgres)

	if repo.searchIndexEvents == nil {
		t.Fatal("searchIndexEvents is nil")
	}
	if !repo.searchIndexEvents.disabledByActiveProvider() {
		t.Fatal("postgres active provider should disable search index event work")
	}
}

func TestPruneProcessedSearchIndexEventsPreservesRecoveryRows(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	provider := fmt.Sprintf("retention-test-%d", time.Now().UnixNano())
	now := time.Now().UTC()
	rows, err := pool.Query(ctx, `
		INSERT INTO catalog_search_index_events (
			provider, action, content_id, attempts, processed_at, last_error, created_at
		)
		VALUES
			($1, 'upsert', 'old-success', 0, $2, '', $2),
			($1, 'upsert', 'recent-success', 0, $3, '', $3),
			($1, 'upsert', 'pending', 0, NULL, '', $2),
			($1, 'upsert', 'dead-letter', $4, $5, 'dead-lettered', $5),
			($1, 'upsert', 'superseded-dead-letter', $4, $2, 'dead-lettered', $2),
			($1, 'upsert', 'above-watermark', 0, $2, '', $2)
		RETURNING id, content_id
	`, provider, now.Add(-48*time.Hour), now.Add(-30*time.Minute), searchIndexEventMaxAttempts, now.Add(-30*time.Hour))
	if err != nil {
		t.Fatalf("seed search events: %v", err)
	}
	ids := make(map[string]int64)
	for rows.Next() {
		var id int64
		var contentID string
		if err := rows.Scan(&id, &contentID); err != nil {
			t.Fatalf("scan seeded search event: %v", err)
		}
		ids[contentID] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read seeded search events: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM catalog_search_index_events WHERE provider = $1`, provider)
		_, _ = pool.Exec(ctx, `DELETE FROM catalog_search_index_state WHERE provider = $1`, provider)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO catalog_search_index_state (provider, last_processed_event_id)
		VALUES ($1, $2)
	`, provider, ids["superseded-dead-letter"]); err != nil {
		t.Fatalf("seed search state: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE catalog_search_index_state
		SET last_rebuild_at = $2
		WHERE provider = $1
	`, provider, now.Add(-36*time.Hour)); err != nil {
		t.Fatalf("seed rebuild watermark: %v", err)
	}

	repo := NewSearchIndexEventRepository(pool)
	deleted, lastID, err := repo.PruneProcessed(ctx, provider, now.Add(-24*time.Hour), 0, 1)
	if err != nil {
		t.Fatalf("PruneProcessed returned error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("PruneProcessed deleted %d rows, want 1", deleted)
	}
	deleted, _, err = repo.PruneProcessed(ctx, provider, now.Add(-24*time.Hour), lastID, 10)
	if err != nil {
		t.Fatalf("second PruneProcessed returned error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("second PruneProcessed deleted %d rows, want 1 superseded dead letter", deleted)
	}

	rows, err = pool.Query(ctx, `
		SELECT content_id
		FROM catalog_search_index_events
		WHERE provider = $1
		ORDER BY id
	`, provider)
	if err != nil {
		t.Fatalf("list retained search events: %v", err)
	}
	var retained []string
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			t.Fatalf("scan retained search event: %v", err)
		}
		retained = append(retained, contentID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read retained search events: %v", err)
	}
	want := []string{"recent-success", "pending", "dead-letter", "above-watermark"}
	if !reflect.DeepEqual(retained, want) {
		t.Fatalf("retained events = %v, want %v", retained, want)
	}
}
