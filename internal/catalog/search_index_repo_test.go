package catalog

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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
