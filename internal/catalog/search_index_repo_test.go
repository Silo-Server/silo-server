package catalog

import (
	"context"
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
