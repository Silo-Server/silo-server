package progresssync

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func fixture(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, Visibility) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL SILO_TEST_DATABASE_URL")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "bootstrap_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	open := func() *pgxpool.Pool {
		cfg, e := pgxpool.ParseConfig(dsn)
		if e != nil {
			t.Fatal(e)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		p, e := pgxpool.NewWithConfig(ctx, cfg)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(p.Close)
		return p
	}
	p, q := open(), open()
	exec(t, p, `CREATE TABLE users(id integer PRIMARY KEY); INSERT INTO users VALUES(1),(2);
 CREATE TABLE user_watch_progress(user_id integer,profile_id text,media_item_id text,position_seconds double precision,duration_seconds double precision,completed boolean,updated_at timestamptz,PRIMARY KEY(user_id,profile_id,media_item_id));
 CREATE TABLE user_history_hidden_items(user_id integer,profile_id text,media_item_id text,hidden_before timestamptz);
 CREATE TABLE policy(user_id integer,profile_id text,digest text,PRIMARY KEY(user_id,profile_id)); INSERT INTO policy VALUES(1,'a','v1'),(1,'b','v1'),(2,'a','v1');`)
	migration, err := os.ReadFile("../../migrations/sql/20260905204740_progress_bootstrap_snapshots.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, p, strings.Split(string(migration), "-- +goose Down")[0])
	visibility := func(ctx context.Context, tx pgx.Tx, id Identity, ids []string) (string, map[string]bool, error) {
		var digest string
		err := tx.QueryRow(ctx, `SELECT digest FROM policy WHERE user_id=$1 AND profile_id=$2`, id.UserID, id.ProfileID).Scan(&digest)
		visible := map[string]bool{}
		for _, id := range ids {
			visible[id] = id != "denied"
		}
		return digest, visible, err
	}
	return p, q, visibility
}
func exec(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := p.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func repo(t *testing.T, p *pgxpool.Pool, v Visibility) *Repository {
	t.Helper()
	r, e := New(p, "installation", v)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func seed(t *testing.T, p *pgxpool.Pool) {
	exec(t, p, `INSERT INTO user_watch_progress SELECT 1,'a',id,12,90,id='b',now() FROM unnest(ARRAY['a','b','c','denied','hidden']) id; INSERT INTO user_history_hidden_items VALUES(1,'a','hidden',now()+interval '1 second')`)
}
func TestSnapshotImmutableReplayAndBinding(t *testing.T) {
	p, q, v := fixture(t)
	seed(t, p)
	r, s := repo(t, p, v), repo(t, q, v)
	id := Identity{1, "a"}
	request := uuid.NewString()
	first, err := r.Create(t.Context(), id, request, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot.ItemCount != 3 || first.Next == nil || first.Items[0].MediaItemID != "a" {
		t.Fatalf("first=%+v", first)
	}
	exec(t, q, `DELETE FROM user_watch_progress WHERE media_item_id='b'; UPDATE user_watch_progress SET position_seconds=99 WHERE media_item_id='c'`)
	replay, err := s.Create(t.Context(), id, request, 1)
	if err != nil || replay.Snapshot != first.Snapshot {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	second, err := s.Read(t.Context(), id, *first.Next)
	if err != nil || len(second.Items) != 1 || second.Items[0].MediaItemID != "b" || !second.Items[0].Completed {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	last, err := r.Read(t.Context(), id, *second.Next)
	if err != nil || last.Next != nil || last.Items[0].PositionSeconds != 12 {
		t.Fatalf("last=%+v err=%v", last, err)
	}
	if _, err = s.Create(t.Context(), id, request, 2); !errors.Is(err, ErrRequestConflict) {
		t.Fatal(err)
	}
	if _, err = s.Read(t.Context(), Identity{1, "b"}, *first.Next); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	pos := *first.Next
	pos.InstallationID = "other"
	if _, err = s.Read(t.Context(), id, pos); !errors.Is(err, ErrResetRequired) {
		t.Fatal(err)
	}
	exec(t, q, `UPDATE policy SET digest='v2' WHERE user_id=1`)
	if _, err = s.Read(t.Context(), id, *first.Next); !errors.Is(err, ErrResetRequired) {
		t.Fatal(err)
	}
}
func TestConcurrentAdmissionAndReplay(t *testing.T) {
	p, q, v := fixture(t)
	r, s := repo(t, p, v), repo(t, q, v)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := range 12 {
		wg.Go(func() {
			owner := r
			if i%2 == 1 {
				owner = s
			}
			_, e := owner.Create(t.Context(), Identity{1, "a"}, uuid.NewString(), 1)
			results <- e
		})
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrQuota) {
			t.Fatal(err)
		}
	}
	if accepted != 2 {
		t.Fatalf("accepted=%d", accepted)
	}
	exec(t, p, `DELETE FROM progress_bootstrap_snapshots`)
	request := uuid.NewString()
	ids := make(chan string, 12)
	for i := range 12 {
		wg.Go(func() {
			owner := r
			if i%2 == 1 {
				owner = s
			}
			page, e := owner.Create(t.Context(), Identity{1, "a"}, request, 1)
			if e != nil {
				t.Error(e)
			}
			ids <- page.Snapshot.ID
		})
	}
	wg.Wait()
	close(ids)
	one := ""
	for id := range ids {
		if one == "" {
			one = id
		}
		if id != one {
			t.Fatalf("different replay snapshots %q %q", one, id)
		}
	}
}
func TestGenerationAtomicityAndRetention(t *testing.T) {
	p, q, v := fixture(t)
	seed(t, p)
	r := repo(t, p, v)
	id := Identity{1, "a"}
	first, e := r.Create(t.Context(), id, uuid.NewString(), 1)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Create(t.Context(), id, uuid.NewString(), 1); e != nil {
		t.Fatal(e)
	}

	tx, e := q.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	_, e = RotateGeneration(t.Context(), tx, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(t.Context()); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Read(t.Context(), id, *first.Next); e != nil {
		t.Fatal(e)
	}
	tx, e = q.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	_, e = RotateGeneration(t.Context(), tx, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Read(t.Context(), id, *first.Next); !errors.Is(e, ErrResetRequired) {
		t.Fatal(e)
	}
	if _, e = r.Create(t.Context(), id, uuid.NewString(), 1); e != nil {
		t.Fatalf("new generation admission blocked by invalidated snapshots: %v", e)
	}

	exec(t, p, `UPDATE progress_bootstrap_snapshots SET expires_at=now()-interval '1 minute'`)
	if e = r.Cleanup(t.Context()); e != nil {
		t.Fatal(e)
	}
	var items, metadata int
	_ = p.QueryRow(t.Context(), `SELECT count(*) FROM progress_bootstrap_items`).Scan(&items)
	_ = p.QueryRow(t.Context(), `SELECT count(*) FROM progress_bootstrap_snapshots`).Scan(&metadata)
	if items != 0 || metadata != 3 {
		t.Fatalf("items=%d metadata=%d", items, metadata)
	}
	if _, e = r.Create(t.Context(), id, first.Snapshot.RequestID, 1); !errors.Is(e, ErrResetRequired) {
		t.Fatal(e)
	}
	exec(t, p, `UPDATE progress_bootstrap_snapshots SET expires_at=now()-interval '25 hours'`)
	if e = r.Cleanup(t.Context()); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Create(t.Context(), id, first.Snapshot.RequestID, 1); e != nil {
		t.Fatal(e)
	}
}

func TestSnapshotWriterAndGenerationFences(t *testing.T) {
	p, q, v := fixture(t)
	seed(t, p)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	blocked := func(ctx context.Context, tx pgx.Tx, id Identity, ids []string) (string, map[string]bool, error) {
		if len(ids) == 0 {
			once.Do(func() { close(entered); <-release })
		}
		return v(ctx, tx, id, ids)
	}
	r := repo(t, p, blocked)
	id := Identity{1, "a"}
	result := make(chan Page, 1)
	fail := make(chan error, 1)
	go func() { page, e := r.Create(t.Context(), id, uuid.NewString(), 1); result <- page; fail <- e }()
	<-entered
	// A concurrent ordinary progress writer does not modify the captured RR view.
	exec(t, q, `UPDATE user_watch_progress SET position_seconds=77; DELETE FROM user_watch_progress WHERE media_item_id='b'`)
	close(release)
	first := <-result
	if e := <-fail; e != nil {
		t.Fatal(e)
	}
	if first.Items[0].PositionSeconds != 12 {
		t.Fatal("snapshot mixed a later writer")
	}
	entered, release = make(chan struct{}), make(chan struct{})
	once = sync.Once{}
	go func() { page, e := r.Read(t.Context(), id, *first.Next); result <- page; fail <- e }()
	<-entered
	tx, e := q.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(t.Context(), `SELECT user_id FROM user_progress_sync_state WHERE user_id=1 FOR UPDATE NOWAIT`)
	if pgerr, ok := errors.AsType[*pgconn.PgError](e); !ok || pgerr.Code != "55P03" {
		t.Errorf("generation lock error=%v", e)
	}
	_ = tx.Rollback(t.Context())
	close(release)
	second := <-result
	if e := <-fail; e != nil {
		t.Fatal(e)
	}
	if second.Items[0].MediaItemID != "b" {
		t.Fatal("deleted row disappeared from immutable snapshot")
	}
	tx, e = q.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	generation, e := RotateGeneration(t.Context(), tx, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	fresh, e := repo(t, q, v).Create(t.Context(), id, uuid.NewString(), 1)
	if e != nil || fresh.Snapshot.Generation != generation || fresh.Items[0].PositionSeconds != 77 {
		t.Fatalf("fresh=%+v error=%v", fresh, e)
	}
}

func TestAdmissionBoundsRollback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		width int
	}{{"rows", MaxSnapshotItems + 1, 0}, {"bytes", 60000, 1200}} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, v := fixture(t)
			exec(t, p, `INSERT INTO user_watch_progress SELECT 1,'a',lpad(i::text,8,'0')||repeat('x',$2),0,90,false,now() FROM generate_series(1,$1) i`, tc.count, tc.width)
			_, err := repo(t, p, v).Create(t.Context(), Identity{1, "a"}, uuid.NewString(), MaxPageSize)
			if !errors.Is(err, ErrTooLarge) {
				t.Fatalf("want admission bound, got %v", err)
			}
			var count int
			if err = p.QueryRow(t.Context(), `SELECT count(*) FROM progress_bootstrap_snapshots`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("failed admission persisted partial snapshot")
			}
		})
	}
}

func TestCanceledAdmissionRollsBack(t *testing.T) {
	p, _, v := fixture(t)
	seed(t, p)
	ctx, cancel := context.WithCancel(t.Context())
	stop := func(ctx context.Context, tx pgx.Tx, id Identity, ids []string) (string, map[string]bool, error) {
		if len(ids) > 0 {
			cancel()
		}
		return v(ctx, tx, id, ids)
	}
	if _, err := repo(t, p, stop).Create(ctx, Identity{1, "a"}, uuid.NewString(), 1); err == nil {
		t.Fatal("canceled admission succeeded")
	}
	var count int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM progress_bootstrap_snapshots`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("partial admission survived cancellation")
	}
}

func TestEmptySnapshotStillRequiresAuthority(t *testing.T) {
	p, _, v := fixture(t)
	r := repo(t, p, v)
	if _, err := r.Create(t.Context(), Identity{1, "unknown"}, uuid.NewString(), 1); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing profile error=%v", err)
	}
	page, err := r.Create(t.Context(), Identity{2, "a"}, uuid.NewString(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.Next != nil || page.Snapshot.ItemCount != 0 {
		t.Fatalf("empty=%+v", page)
	}
}
