package planstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPostgresLiveRouteRestorationSerializesStop(t *testing.T) {
	f := newPlanstoreFixture(t)
	store := NewPostgres(f.pool)
	record := f.attemptRecord(uuid.NewString(), uuid.NewString(), "restore")
	record.FrozenRecipe.TranscodeNodeURL, record.FrozenRecipe.TranscodeTransportID = "https://old", "work"
	other := f.attemptRecord(uuid.NewString(), uuid.NewString(), "other")
	for _, item := range []playback.AttemptRecordV3{record, other} {
		if err := store.SaveAttempt(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	cfg := f.pool.Config().Copy()
	app := "route-restore-stop-" + uuid.NewString()
	cfg.ConnConfig.RuntimeParams["application_name"] = app
	stopPool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer stopPool.Close()
	stopper := NewPostgres(stopPool)
	owned, err := store.AcquireRouteSession(t.Context(), record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	entered, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	done := make(chan error, 1)
	go func() {
		called, err := owned.WithLiveRoute(t.Context(), record, func(ctx context.Context) error {
			close(entered)
			select {
			case <-resume:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err == nil && !called {
			err = errors.New("live predecessor callback refused")
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("row-owned callback did not begin")
	}
	stopped := make(chan error, 1)
	go func() {
		_, _, err := stopper.StopAttempt(t.Context(), record.SessionID, uuid.NewString(), nil)
		stopped <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waiting bool
		if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, app).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("direct PostgreSQL STOP bypassed restoration row ownership")
		}
		select {
		case err := <-stopped:
			t.Fatalf("STOP completed during restoration: %v", err)
		case <-time.After(time.Millisecond):
		}
	}
	progressCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	_, err = store.ApplyProgress(progressCtx, other.SessionID, playback.ProgressSampleV3{Sequence: 1, Position: 12})
	cancel()
	if err != nil {
		t.Fatal("restoration blocked an unrelated attempt", err)
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("row transaction survived restoration")
	}
	called, err := owned.WithLiveRoute(t.Context(), record, func(context.Context) error {
		t.Error("STOP was followed by a stale recipe restoration")
		return nil
	})
	if called || err != nil {
		t.Fatalf("stopped callback called=%v err=%v", called, err)
	}
}

func TestPostgresLiveRouteRestorationReleasesCanceledRows(t *testing.T) {
	f := newPlanstoreFixture(t)
	store := NewPostgres(f.pool)
	record := f.attemptRecord(uuid.NewString(), uuid.NewString(), "restore-cancel")
	if err := store.SaveAttempt(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	owned, err := store.AcquireRouteSession(t.Context(), record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	blocker, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := blocker.Exec(t.Context(), `SELECT 1 FROM playback_v3_attempts WHERE session_id=$1::uuid FOR UPDATE`, record.SessionID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	_, err = owned.WithLiveRoute(ctx, record, func(context.Context) error {
		t.Error("callback entered after a canceled row wait")
		return nil
	})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("row wait ignored cancellation: %v", err)
	}
	if err := blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A canceled backend is discarded. Rejoin the same ownership boundary
	// explicitly before another callback may run on that route session.
	if _, err := owned.GetAttempt(t.Context()); err != nil {
		t.Fatal("could not rejoin after canceled row wait", err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
	_, err = owned.WithLiveRoute(ctx, record, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("callback ignored cancellation: %v", err)
	}
	stopCtx, cancelStop := context.WithTimeout(t.Context(), time.Second)
	defer cancelStop()
	if _, _, err := store.StopAttempt(stopCtx, record.SessionID, uuid.NewString(), nil); err != nil {
		t.Fatal("canceled callback retained a row lock or transaction", err)
	}
}
