package subtitles

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/jackc/pgx/v5/pgxpool"
)

func aiPublicationDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := subtitleStorageDatabase(t)
	_, err := pool.Exec(t.Context(), `CREATE TABLE subtitle_ai_jobs (
 id BIGINT PRIMARY KEY,media_file_id BIGINT NOT NULL,requested_by BIGINT,status TEXT NOT NULL,
 progress DOUBLE PRECISION NOT NULL DEFAULT 0,result_subtitle_id BIGINT REFERENCES downloaded_subtitles(id),
 error_message TEXT NOT NULL DEFAULT '',updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now());
 INSERT INTO subtitle_ai_jobs(id,media_file_id,requested_by,status) VALUES(1,42,7,'running'),(2,42,7,'running')`)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
func aiPublicationRequest() StoreSubtitleRequest {
	return StoreSubtitleRequest{Publication: &AIJobPublication{JobID: 1, Complete: true}, MediaFileID: 42, UserID: new(7), Provider: "ai-translated", Language: "en", Format: FormatSRT, Data: []byte("synthetic AI subtitle")}
}
func assertAIPublicationState(t *testing.T, pool *pgxpool.Pool, status string, count int) {
	t.Helper()
	var got string
	var tracks int
	if err := pool.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM downloaded_subtitles) FROM subtitle_ai_jobs WHERE id=1`).Scan(&got, &tracks); err != nil {
		t.Fatal(err)
	}
	if got != status || tracks != count {
		t.Fatalf("state=(%s,%d), want=(%s,%d)", got, tracks, status, count)
	}
}

func TestAIPublicationPostgresCancelDuringUpload(t *testing.T) {
	for _, terminal := range []string{string(jobrunner.StatusCancelled), "failed"} {
		for _, complete := range []bool{true, false} {
			t.Run(terminal+map[bool]string{true: " final", false: " intermediate"}[complete], func(t *testing.T) {
				pool := aiPublicationDatabase(t)
				objects := newMockBlobStore()
				gate := &gatedSubtitleObjectStore{BlobStore: objects, putReady: make(chan struct{}, 1), putRelease: make(chan struct{})}
				manager := NewManager(NewPgRepository(pool, nil), gate)
				req := aiPublicationRequest()
				req.Publication.Complete = complete
				result := make(chan error, 1)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				go func() { _, err := manager.StoreSubtitle(ctx, req); result <- err }()
				select {
				case <-gate.putReady:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if _, err := pool.Exec(ctx, `UPDATE subtitle_ai_jobs SET status=$1 WHERE id=1`, terminal); err != nil {
					t.Fatal(err)
				}
				close(gate.putRelease)
				if err := <-result; !errors.Is(err, ErrAIJobInactive) {
					t.Fatalf("publication error=%v", err)
				}
				assertAIPublicationState(t, pool, terminal, 0)
				if objects.deletes != 1 {
					t.Fatal("refused candidate not cleaned")
				}
			})
		}
	}
}

func TestAIPublicationPostgresDuplicateAndTerminalReuse(t *testing.T) {
	pool := aiPublicationDatabase(t)
	objects := newMockBlobStore()
	gate := &gatedSubtitleObjectStore{BlobStore: objects, putReady: make(chan struct{}, 2), putRelease: make(chan struct{})}
	manager := NewManager(NewPgRepository(pool, nil), gate)
	type result struct {
		sub *DownloadedSubtitle
		err error
	}
	results := make(chan result, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, id := range []int64{1, 2} {
		req := aiPublicationRequest()
		req.Publication.JobID = id
		go func() { sub, err := manager.StoreSubtitle(ctx, req); results <- result{sub, err} }()
	}
	for range 2 {
		select {
		case <-gate.putReady:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(gate.putRelease)
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.sub.ID != b.sub.ID {
		t.Fatalf("results=%+v %+v", a, b)
	}
	var completed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM subtitle_ai_jobs WHERE status='completed' AND result_subtitle_id=$1`, a.sub.ID).Scan(&completed); err != nil || completed != 2 {
		t.Fatalf("completed=%d err=%v", completed, err)
	}
	assertAIPublicationState(t, pool, "completed", 1)
	manager = NewManager(NewPgRepository(pool, nil), objects)
	if _, err := manager.StoreSubtitle(ctx, aiPublicationRequest()); !errors.Is(err, ErrAIJobInactive) {
		t.Fatalf("terminal duplicate reuse=%v", err)
	}
	if _, _, err := manager.GetSubtitleContent(ctx, a.sub.ID); err != nil {
		t.Fatal("candidate cleanup removed winner", err)
	}
}

func TestAIPublicationPostgresIntermediateThenCancel(t *testing.T) {
	pool := aiPublicationDatabase(t)
	manager := NewManager(NewPgRepository(pool, nil), newMockBlobStore())
	req := aiPublicationRequest()
	req.Publication.Complete = false
	if _, err := manager.StoreSubtitle(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	assertAIPublicationState(t, pool, "running", 1)
	if _, err := pool.Exec(t.Context(), `UPDATE subtitle_ai_jobs SET status='cancelled' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	req.Publication.Complete = true
	req.Language = "fr"
	if _, err := manager.StoreSubtitle(t.Context(), req); !errors.Is(err, ErrAIJobInactive) {
		t.Fatal(err)
	}
	assertAIPublicationState(t, pool, string(jobrunner.StatusCancelled), 1)
}

func TestAIPublicationPostgresCompletionFailureRollsBackMetadata(t *testing.T) {
	pool := aiPublicationDatabase(t)
	_, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_complete() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'synthetic completion failure'; END $$;
 CREATE TRIGGER reject_complete BEFORE UPDATE ON subtitle_ai_jobs FOR EACH ROW EXECUTE FUNCTION reject_complete()`)
	if err != nil {
		t.Fatal(err)
	}
	objects := newMockBlobStore()
	manager := NewManager(NewPgRepository(pool, nil), objects)
	if _, err := manager.StoreSubtitle(t.Context(), aiPublicationRequest()); err == nil {
		t.Fatal("completion unexpectedly succeeded")
	}
	assertAIPublicationState(t, pool, "running", 0)
	if objects.deletes != 0 {
		t.Fatal("uncertain candidate deleted")
	}
}

type lostAIPublicationReply struct{ *PgRepository }

func (r lostAIPublicationReply) PublishAISubtitle(ctx context.Context, sub *DownloadedSubtitle, fence AIJobPublication, legacy *DownloadedSubtitle) (*DownloadedSubtitle, error) {
	if _, err := r.PgRepository.PublishAISubtitle(ctx, sub, fence, legacy); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: synthetic lost commit reply", ErrAIPublicationUncertain)
}
func TestAIPublicationPostgresLostReplyRetainsBytes(t *testing.T) {
	pool := aiPublicationDatabase(t)
	objects := newMockBlobStore()
	manager := NewManager(lostAIPublicationReply{NewPgRepository(pool, nil)}, objects)
	if _, err := manager.StoreSubtitle(t.Context(), aiPublicationRequest()); !errors.Is(err, ErrAIPublicationUncertain) {
		t.Fatalf("lost reply did not preserve uncertainty: %v", err)
	}
	assertAIPublicationState(t, pool, "completed", 1)
	var id int
	if err := pool.QueryRow(t.Context(), `SELECT result_subtitle_id FROM subtitle_ai_jobs WHERE id=1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.GetSubtitleContent(t.Context(), id); err != nil {
		t.Fatal("lost reply removed committed bytes", err)
	}
	if objects.deletes != 0 {
		t.Fatal("uncertain candidate deleted")
	}
}

func waitForAIBlocker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker uint32) int {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		err := pool.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)) LIMIT 1),0)`, blocker).Scan(&pid)
		if err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("publication did not reach SQL lock barrier", ctx.Err())
		}
	}
}

func TestAIPublicationPostgresCancellationWinsRowLock(t *testing.T) {
	pool := aiPublicationDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // No-op after commit.
	if _, err := tx.Exec(ctx, `SELECT id FROM subtitle_ai_jobs WHERE id=1 FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(NewPgRepository(pool, nil), newMockBlobStore())
	result := make(chan error, 1)
	go func() { _, err := manager.StoreSubtitle(ctx, aiPublicationRequest()); result <- err }()
	waitForAIBlocker(t, ctx, pool, conn.Conn().PgConn().PID())
	if _, err := tx.Exec(ctx, `UPDATE subtitle_ai_jobs SET status='cancelled' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrAIJobInactive) {
		t.Fatalf("post-lock publication=%v", err)
	}
	assertAIPublicationState(t, pool, string(jobrunner.StatusCancelled), 0)
}

func TestAIPublicationPostgresPublicationWinsCancellation(t *testing.T) {
	pool := aiPublicationDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	_, err = conn.Exec(ctx, `SELECT pg_advisory_lock(734881);
 CREATE FUNCTION gate_publication() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_advisory_xact_lock(734881); RETURN NEW; END $$;
 CREATE TRIGGER gate_publication BEFORE INSERT ON downloaded_subtitles FOR EACH ROW EXECUTE FUNCTION gate_publication()`)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock_all()`) //nolint:errcheck // Release test barrier on every exit.
	manager := NewManager(NewPgRepository(pool, nil), newMockBlobStore())
	result := make(chan error, 1)
	go func() { _, err := manager.StoreSubtitle(ctx, aiPublicationRequest()); result <- err }()
	publisherPID := waitForAIBlocker(t, ctx, pool, conn.Conn().PgConn().PID())
	type canceled struct {
		rows int64
		err  error
	}
	cancellation := make(chan canceled, 1)
	go func() {
		tag, err := pool.Exec(ctx, `UPDATE subtitle_ai_jobs SET status='cancelled' WHERE id=1 AND status IN ('pending','running')`)
		cancellation <- canceled{tag.RowsAffected(), err}
	}()
	waitForAIBlocker(t, ctx, pool, uint32(publisherPID))
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(734881)`); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	got := <-cancellation
	if got.err != nil || got.rows != 0 {
		t.Fatalf("late cancellation=%+v", got)
	}
	assertAIPublicationState(t, pool, "completed", 1)
}

func TestAIPublicationPostgresIdentityFence(t *testing.T) {
	for _, wrong := range []string{"file", "account", "missing"} {
		t.Run(wrong, func(t *testing.T) {
			pool := aiPublicationDatabase(t)
			req := aiPublicationRequest()
			switch wrong {
			case "file":
				req.MediaFileID = 99
			case "account":
				req.UserID = new(9)
			case "missing":
				req.Publication.JobID = 99
			}
			manager := NewManager(NewPgRepository(pool, nil), newMockBlobStore())
			if _, err := manager.StoreSubtitle(t.Context(), req); !errors.Is(err, ErrAIJobInactive) {
				t.Fatalf("identity fence=%v", err)
			}
			assertAIPublicationState(t, pool, "running", 0)
		})
	}
}

func TestAIPublicationPostgresLegacyReuse(t *testing.T) {
	pool := aiPublicationDatabase(t)
	req := aiPublicationRequest()
	repo := NewPgRepository(pool, nil)
	objects := newMockBlobStore()
	legacy := &DownloadedSubtitle{MediaFileID: req.MediaFileID, Provider: req.Provider, Language: req.Language, Format: req.Format, S3Key: buildSubtitleS3Key(req.MediaFileID, req.Language, req.Provider, req.Format, req.Data)}
	if err := repo.InsertDownloadedSubtitle(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(t.Context(), legacy.S3Key, req.Data); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(repo, objects)
	sub, err := manager.StoreSubtitle(t.Context(), req)
	if err != nil || sub.ID != legacy.ID {
		t.Fatalf("legacy reuse=%+v %v", sub, err)
	}
	assertAIPublicationState(t, pool, "completed", 1)
	if _, data, err := manager.GetSubtitleContent(t.Context(), sub.ID); err != nil || string(data) != string(req.Data) {
		t.Fatal("legacy bytes lost", err)
	}
}

func TestAIPublicationRequiresAtomicRepository(t *testing.T) {
	repo := newMockSubtitleRepo()
	objects := newMockBlobStore()
	manager := NewManager(repo, objects)
	if _, err := manager.StoreSubtitle(t.Context(), aiPublicationRequest()); err == nil {
		t.Fatal("missing atomic repository silently accepted publication")
	}
	if repo.inserts != 0 || objects.puts != 0 {
		t.Fatal("unsupported publication performed storage effects")
	}
}
