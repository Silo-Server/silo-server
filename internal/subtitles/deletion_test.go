package subtitles

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type deletionObjectSpy struct {
	BlobStore
	keys []string
	err  error
}

func (s *deletionObjectSpy) Delete(_ context.Context, key string) error {
	s.keys = append(s.keys, key)
	return s.err
}

type uncertainSubtitleDelete struct{ *PgRepository }

func (r uncertainSubtitleDelete) DeleteDownloadedSubtitleWithRevision(ctx context.Context, id int, revision *int64) (*DownloadedSubtitle, error) {
	row, err := r.PgRepository.DeleteDownloadedSubtitleWithRevision(ctx, id, revision)
	if err != nil {
		return nil, err
	}
	return row, errors.New("lost successful SQL reply")
}
func TestSubtitleGuardedDeleteRequiresRepositorySupport(t *testing.T) {
	objects := &deletionObjectSpy{}
	err := NewManager(newMockSubtitleRepo(), objects).DeleteSubtitleWithRevision(t.Context(), 1, new(int64(1)))
	if !errors.Is(err, ErrSubtitleGuardedDeletionUnavailable) || len(objects.keys) != 0 {
		t.Fatalf("unsupported: %v %v", err, objects.keys)
	}
}
func TestSubtitleGuardedDeletionDB(t *testing.T) {
	pool := subtitleStorageDatabase(t)
	repo := NewPgRepository(pool, nil)
	ctx := t.Context()
	seed := func(key string) *DownloadedSubtitle {
		t.Helper()
		row := &DownloadedSubtitle{MediaFileID: 42, Provider: "upload", Language: "en", Format: FormatSRT, ReleaseName: "Original", S3Key: key, ContentSHA256: strings.Repeat(key, 64)}
		if err := repo.InsertDownloadedSubtitle(ctx, row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	row := seed("a")
	objects := &deletionObjectSpy{}
	manager := NewManager(repo, objects)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `UPDATE downloaded_subtitles SET release_name='Changed',hearing_impaired=true WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.DeleteSubtitleWithRevision(ctx, row.ID, new(row.Revision)) }()
	waitForSubtitleDeleteLock(t, pool)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = <-done
	conflict, ok := errors.AsType[*SubtitleRevisionConflict](err)
	if !ok || conflict.Current.ReleaseName != "Changed" || len(objects.keys) != 0 {
		t.Fatalf("racing delete: %v %v", err, objects.keys)
	}
	snapshot := func(id int) string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(s)::text FROM downloaded_subtitles s WHERE id=$1`, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot(row.ID)
	if err = manager.DeleteSubtitleWithRevision(ctx, row.ID, new(row.Revision)); err == nil || snapshot(row.ID) != before || len(objects.keys) != 0 {
		t.Fatalf("stale persisted snapshot: %v", err)
	}
	current, err := repo.GetDownloadedSubtitle(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	objects.err = errors.New("object store unavailable")
	if err = manager.DeleteSubtitleWithRevision(ctx, row.ID, new(current.Revision)); err != nil {
		t.Fatal(err)
	}
	absent, err := repo.GetDownloadedSubtitle(ctx, row.ID)
	if err != nil || absent != nil || len(objects.keys) != 1 || objects.keys[0] != "a" {
		t.Fatalf("confirmed deletion: %+v %v %v", absent, err, objects.keys)
	}
	if err = manager.DeleteSubtitleWithRevision(ctx, row.ID, new(current.Revision)); !errors.Is(err, ErrSubtitleNotFound) || len(objects.keys) != 1 {
		t.Fatalf("missing replay: %v", err)
	}
	replacement := seed("b")
	uncertain := NewManager(uncertainSubtitleDelete{repo}, objects)
	if err = uncertain.DeleteSubtitleWithRevision(ctx, replacement.ID, new(replacement.Revision)); err == nil || len(objects.keys) != 1 {
		t.Fatalf("uncertain reply cleaned object: %v", err)
	}
	absent, err = repo.GetDownloadedSubtitle(ctx, replacement.ID)
	if err != nil || absent != nil {
		t.Fatalf("lost reply did not persist: %v", err)
	}
	wildcard := seed("c")
	objects.err = nil
	if err = manager.DeleteSubtitleWithRevision(ctx, wildcard.ID, nil); err != nil || len(objects.keys) != 2 || objects.keys[1] != "c" {
		t.Fatalf("existence delete: %v %v", err, objects.keys)
	}
	t.Log("DELETE waits behind UPDATE then reevaluates revision; full stale snapshot unchanged; exact removed-object cleanup; cleanup failure retains metadata success; lost successful SQL reply never cleans object; explicit existence delete PASS")
}
func waitForSubtitleDeleteLock(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'DELETE FROM downloaded_subtitles%')`).Scan(&blocked)
		if err != nil {
			t.Fatal(fmt.Errorf("observe delete lock: %w", err))
		}
		if blocked {
			return
		}
		runtime.Gosched()
	}
}
