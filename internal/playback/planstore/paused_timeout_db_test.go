package planstore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestExpirePausedAttemptDB pins the paused-timeout claim: it ends an attempt
// only while the session is paused and has been for the whole timeout, by
// the database's clock, and a resume that lands first, or commits while the
// claim waits on the attempt's lock, keeps it live.
func TestExpirePausedAttemptDB(t *testing.T) {
	const timeout = 30 * time.Minute
	type fixture struct {
		store   *Postgres
		session string
		// pausedFor backdates the session's pause.
		pausedFor func(time.Duration)
	}
	setup := func(t *testing.T) fixture {
		ctx, pool, store, session := newLifecycleAttemptDB(t)
		if _, err := store.ApplyProgress(ctx, session, playback.ProgressSampleV3{Sequence: 1, Position: 600, IsPaused: true}); err != nil {
			t.Fatal(err)
		}
		return fixture{store: store, session: session, pausedFor: func(d time.Duration) {
			t.Helper()
			if _, err := pool.Exec(ctx, `UPDATE playback_session_lifecycle SET updated_at = NOW() - make_interval(secs => $2) WHERE session_id = $1`,
				session, d.Seconds()); err != nil {
				t.Fatal(err)
			}
		}}
	}
	expire := func(t *testing.T, f fixture, stopID string) bool {
		t.Helper()
		stopped, err := f.store.ExpirePausedAttempt(t.Context(), f.session, stopID, timeout)
		if err != nil {
			t.Fatal(err)
		}
		return stopped
	}

	t.Run("paused past the timeout", func(t *testing.T) {
		f := setup(t)
		f.pausedFor(timeout + time.Minute)
		stopID := uuid.NewString()
		if !expire(t, f, stopID) {
			t.Fatal("a session paused past the timeout was not ended")
		}
		receipt, first, err := f.store.StopAttempt(t.Context(), f.session, uuid.NewString(), nil)
		if err != nil || first || receipt.StopID != stopID || receipt.Accepted == nil || !receipt.Accepted.IsPaused || receipt.Accepted.Position != 600 {
			t.Fatalf("stop after the claim = %+v first=%v err=%v; want the claim's receipt at the paused sample", receipt, first, err)
		}
		if expire(t, f, stopID) {
			t.Fatal("a stopped attempt was claimed again")
		}
	})

	t.Run("paused too recently", func(t *testing.T) {
		f := setup(t)
		f.pausedFor(timeout - time.Minute)
		if expire(t, f, uuid.NewString()) {
			t.Fatal("a session paused for less than the timeout was ended")
		}
	})

	t.Run("a resume lands first", func(t *testing.T) {
		f := setup(t)
		f.pausedFor(timeout + time.Minute)
		if _, err := f.store.ApplyProgress(t.Context(), f.session, playback.ProgressSampleV3{Sequence: 2, Position: 600}); err != nil {
			t.Fatal(err)
		}
		if expire(t, f, uuid.NewString()) {
			t.Fatal("a resumed session was ended")
		}
	})

	// A resume and a new pause commit while the claim waits on the attempt's
	// lock: the attempt's last sample is paused again, so only the lifecycle
	// row, read after the lock, shows the pause is new. A claim that judged
	// the lifecycle row as of before the wait would end the session.
	t.Run("a resume commits while the claim waits", func(t *testing.T) {
		f := setup(t)
		f.pausedFor(timeout + time.Minute)
		tx, err := f.store.db.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(t.Context(), `UPDATE playback_v3_attempts SET last_sequence = 3, updated_at = NOW(),
			last_sample = '{"sequence":3,"position":620,"is_paused":true}' WHERE session_id = $1`, f.session); err != nil {
			t.Fatal(err)
		}
		claimed := make(chan bool, 1)
		go func() {
			stopped, err := f.store.ExpirePausedAttempt(context.Background(), f.session, uuid.NewString(), timeout)
			if err != nil {
				t.Error(err)
			}
			claimed <- stopped
		}()
		waitForLockWait(t, t.Context(), f.store, "%FOR UPDATE%")
		if _, err := tx.Exec(t.Context(), `UPDATE playback_session_lifecycle SET state = 'paused',
			seq = nextval('playback_session_lifecycle_seq'), updated_at = NOW() WHERE session_id = $1`, f.session); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if <-claimed {
			t.Fatal("the claim ended a session whose pause began while it waited")
		}
	})
}

// waitForLockWait waits until a statement matching query blocks on a lock.
func waitForLockWait(t *testing.T, ctx context.Context, store *Postgres, query string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; {
		var waiting int
		if err := store.db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE $1`, query).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no statement like %q waited on a lock", query)
		}
		<-time.After(10 * time.Millisecond)
	}
}
