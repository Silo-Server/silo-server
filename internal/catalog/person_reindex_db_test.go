package catalog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// personWriteTracer records the statements a person write issues.
type personWriteTracer struct {
	mu      sync.Mutex
	queries []string
}

func (t *personWriteTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queries = append(t.queries, data.SQL)
	return ctx
}

func (*personWriteTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *personWriteTracer) take() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	queries := t.queries
	t.queries = nil
	return queries
}

func reindexedPersonItems(queries []string) bool {
	for _, query := range queries {
		if strings.Contains(query, "FROM item_people") {
			return true
		}
	}
	return false
}

// TestPersonWriteReindexesTitlesOnlyOnRenamePostgres pins that a person write
// queues a search reindex of their titles only when it changes their name,
// the only person field search documents carry. A lookup that fills in a bio
// or photo used to reindex every title the person appears in.
func TestPersonWriteReindexesTitlesOnlyOnRenamePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	tracer := &personWriteTracer{}
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	repo := NewPersonRepository(pool)

	personID := seedRefreshPerson(t, pool, "reindex")
	contentID := fmt.Sprintf("person-reindex-movie-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', 'Reindex Movie')`, contentID); err != nil {
		t.Fatalf("seed movie: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO item_people (id, content_id, person_id, kind) VALUES ($1, $2, $1, 1)`, personID, contentID); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	person, err := repo.Get(ctx, personID)
	if err != nil {
		t.Fatalf("load person: %v", err)
	}
	tracer.take()

	person.Bio = "A bio from a provider."
	if _, err := repo.UpdateRefreshed(ctx, *person, PersonIdentityOf(*person)); err != nil {
		t.Fatalf("refresh write keeping the name: %v", err)
	}
	if queries := tracer.take(); reindexedPersonItems(queries) {
		t.Fatalf("a write keeping the name reindexed the person's titles: %q", queries)
	}
	if err := repo.Update(ctx, *person); err != nil {
		t.Fatalf("repeat write: %v", err)
	}
	if queries := tracer.take(); reindexedPersonItems(queries) {
		t.Fatalf("a repeat write reindexed the person's titles: %q", queries)
	}

	person.Name = "Reindex Renamed"
	if err := repo.Update(ctx, *person); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if queries := tracer.take(); !reindexedPersonItems(queries) {
		t.Fatalf("a rename did not reindex the person's titles: %q", queries)
	}

	stored, err := repo.Get(ctx, personID)
	if err != nil {
		t.Fatalf("reload person: %v", err)
	}
	if stored.Name != "Reindex Renamed" || stored.Bio != "A bio from a provider." {
		t.Fatalf("stored person = name %q bio %q, want both writes kept", stored.Name, stored.Bio)
	}
}

// TestPersonWriteWaitingOnARenameReindexesTitlesPostgres pins that a write
// compares against the name it replaces, not the one its statement started
// with: a refresh that waits on another transaction's rename, then writes the
// old name back, is a rename too and reindexes the person's titles.
func TestPersonWriteWaitingOnARenameReindexesTitlesPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	tracer := &personWriteTracer{}
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	other, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect second pool: %v", err)
	}
	t.Cleanup(other.Close)
	repo := NewPersonRepository(pool)

	personID := seedRefreshPerson(t, pool, "waiting")
	person, err := repo.Get(ctx, personID)
	if err != nil {
		t.Fatalf("load person: %v", err)
	}

	rename, err := other.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rename: %v", err)
	}
	defer func() { _ = rename.Rollback(ctx) }()
	if _, err := rename.Exec(ctx, `UPDATE people SET name = 'Renamed Elsewhere' WHERE id = $1`, personID); err != nil {
		t.Fatalf("rename: %v", err)
	}

	tracer.take()
	person.Bio = "A bio from a provider."
	done := make(chan error, 1)
	go func() {
		_, err := repo.UpdateRefreshed(ctx, *person, PersonIdentityOf(*person))
		done <- err
	}()
	// Wait until the refresh write is blocked on the rename's row lock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := other.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE wait_event_type = 'Lock' AND query LIKE '%prior_name%'
			)`).Scan(&waiting); err != nil {
			t.Fatalf("check lock wait: %v", err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh write never waited on the rename")
		}
		select {
		case err := <-done:
			t.Fatalf("refresh write finished (err %v) before the rename committed", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := rename.Commit(ctx); err != nil {
		t.Fatalf("commit rename: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("refresh write: %v", err)
	}
	if queries := tracer.take(); !reindexedPersonItems(queries) {
		t.Fatalf("a write that undid a concurrent rename did not reindex: %q", queries)
	}
}
