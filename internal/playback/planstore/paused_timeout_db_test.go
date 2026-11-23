package planstore

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const pausedAttemptTimeout = 30 * time.Minute

// pausedAttemptFixture is a live attempt whose session is paused.
type pausedAttemptFixture struct {
	store   *Postgres
	session string
	exec    func(sql string, args ...any)
	// file is the attempt's effective media file, the one the sweep classified.
	file int
	// pausedFor backdates the session's pause.
	pausedFor func(time.Duration)
}

func newPausedAttemptFixture(t *testing.T) pausedAttemptFixture {
	t.Helper()
	ctx, pool, store, session := newLifecycleAttemptDB(t)
	if _, err := store.ApplyProgress(ctx, session, playback.ProgressSampleV3{Sequence: 1, Position: 600, IsPaused: true}); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var file int
	if err := pool.QueryRow(ctx, `SELECT effective_media_file_id FROM playback_v3_attempts WHERE session_id = $1`, session).Scan(&file); err != nil {
		t.Fatal(err)
	}
	return pausedAttemptFixture{store: store, session: session, exec: exec, file: file, pausedFor: func(d time.Duration) {
		t.Helper()
		exec(`UPDATE playback_session_lifecycle SET updated_at = NOW() - make_interval(secs => $2) WHERE session_id = $1`, session, d.Seconds())
	}}
}

// expire runs the claim under stopID and reports whether it ended the attempt.
func (f pausedAttemptFixture) expire(t *testing.T, stopID string) bool {
	t.Helper()
	stopped, err := f.store.ExpirePausedAttempt(t.Context(), f.session, stopID, f.file, pausedAttemptTimeout)
	if err != nil {
		t.Fatal(err)
	}
	return stopped
}

// TestExpirePausedAttemptDB pins the paused-timeout claim: it ends an attempt
// only while the session is paused and has been for the whole timeout, by
// the database's clock, and a resume that lands first, or commits while the
// claim waits on the attempt's lock, keeps it live. The stopped attempt's
// identity carries the stop id and client features its 410 is chosen by.
func TestExpirePausedAttemptDB(t *testing.T) {
	t.Run("paused past the timeout", func(t *testing.T) {
		f := newPausedAttemptFixture(t)
		f.pausedFor(pausedAttemptTimeout + time.Minute)
		stopID := uuid.NewString()
		if !f.expire(t, stopID) {
			t.Fatal("a session paused past the timeout was not ended")
		}
		receipt, first, err := f.store.StopAttempt(t.Context(), f.session, uuid.NewString(), nil)
		if err != nil || first || receipt.StopID != stopID || receipt.Accepted == nil || !receipt.Accepted.IsPaused || receipt.Accepted.Position != 600 {
			t.Fatalf("stop after the claim = %+v first=%v err=%v; want the claim's receipt at the paused sample", receipt, first, err)
		}
		if f.expire(t, stopID) {
			t.Fatal("a stopped attempt was claimed again")
		}
	})

	t.Run("identity carries the stop and features", func(t *testing.T) {
		f := newPausedAttemptFixture(t)
		f.pausedFor(pausedAttemptTimeout + time.Minute)
		stopID := uuid.NewString()
		if !f.expire(t, stopID) {
			t.Fatal("a session paused past the timeout was not ended")
		}
		for _, tc := range []struct {
			name, features string
			want           []string
		}{
			{"missing", ``, nil},
			{"JSON null", `null`, nil},
			{"listed", `["paused_timeout_v1"]`, []string{playback.FeaturePausedTimeoutV3}},
		} {
			if tc.features == "" {
				f.exec(`UPDATE playback_v3_attempts SET normalized_request = normalized_request - 'client_features' WHERE session_id = $1`, f.session)
			} else {
				f.exec(`UPDATE playback_v3_attempts SET normalized_request = jsonb_set(normalized_request, '{client_features}', $2::jsonb) WHERE session_id = $1`,
					f.session, tc.features)
			}
			identity, err := f.store.GetAttemptIdentity(t.Context(), f.session)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if !identity.Stopped || identity.StopID != stopID || !slices.Equal(identity.ClientFeatures, tc.want) {
				t.Errorf("%s: identity = %+v; want stopped under %s with features %v", tc.name, identity, stopID, tc.want)
			}
		}
	})

	t.Run("paused too recently", func(t *testing.T) {
		f := newPausedAttemptFixture(t)
		f.pausedFor(pausedAttemptTimeout - time.Minute)
		if f.expire(t, uuid.NewString()) {
			t.Fatal("a session paused for less than the timeout was ended")
		}
	})

	t.Run("a resume lands first", func(t *testing.T) {
		f := newPausedAttemptFixture(t)
		f.pausedFor(pausedAttemptTimeout + time.Minute)
		if _, err := f.store.ApplyProgress(t.Context(), f.session, playback.ProgressSampleV3{Sequence: 2, Position: 600}); err != nil {
			t.Fatal(err)
		}
		if f.expire(t, uuid.NewString()) {
			t.Fatal("a resumed session was ended")
		}
	})

	// A resume and a new pause commit while the claim waits on the attempt's
	// lock: the attempt's last sample is paused again, so only the lifecycle
	// row, read after the lock, shows the pause is new. A claim that judged
	// the lifecycle row as of before the wait would end the session.
	t.Run("a resume commits while the claim waits", func(t *testing.T) {
		f := newPausedAttemptFixture(t)
		f.pausedFor(pausedAttemptTimeout + time.Minute)
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
			stopped, err := f.store.ExpirePausedAttempt(context.Background(), f.session, uuid.NewString(), f.file, pausedAttemptTimeout)
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

// TestExpirePausedAttemptRechecksTheSelectionDB pins that the claim rechecks,
// under the attempt's lock, what the sweep selected by: a client that went
// quiet since is left to the idle expiry, and an attempt that a replan moved
// to another file than the one the sweep classified waits for a later sweep.
func TestExpirePausedAttemptRechecksTheSelectionDB(t *testing.T) {
	t.Run("the client went quiet", func(t *testing.T) {
		f := newPausedAttemptFixture(t)
		f.pausedFor(pausedAttemptTimeout + time.Minute)
		f.exec(`UPDATE playback_v3_attempts SET updated_at = NOW() - make_interval(secs => $2) WHERE session_id = $1`,
			f.session, (playback.PausedStreamSlotGrace + time.Minute).Seconds())
		if f.expire(t, uuid.NewString()) {
			t.Fatal("a session whose client stopped reporting was ended by the paused timeout")
		}
	})
	t.Run("the file changed", func(t *testing.T) {
		f := newPausedAttemptFixture(t)
		f.pausedFor(pausedAttemptTimeout + time.Minute)
		stopped, err := f.store.ExpirePausedAttempt(t.Context(), f.session, uuid.NewString(), f.file+1, pausedAttemptTimeout)
		if err != nil {
			t.Fatal(err)
		}
		if stopped {
			t.Fatal("the claim ended an attempt playing another file than the one the sweep classified")
		}
	})
}

// TestExpirePausedAttemptExemptionsDB pins the exemptions the claim checks
// again under the attempt's lock: a Cast receiver's play and a member of an
// open Watch Together room are not ended, and a room whose runtime holds no
// members object neither fails the claim nor holds the session.
func TestExpirePausedAttemptExemptionsDB(t *testing.T) {
	t.Run("exempt plays", func(t *testing.T) {
		for name, exempt := range map[string]func(pausedAttemptFixture){
			"cast receiver": func(f pausedAttemptFixture) {
				f.exec(`UPDATE playback_v3_attempts SET normalized_request = jsonb_set(normalized_request,
					'{client_playback_context}', '{"output":{"current_sink":"cast_receiver"}}') WHERE session_id = $1`, f.session)
			},
			"watch together member": func(f pausedAttemptFixture) {
				f.exec(`INSERT INTO watch_together_rooms (id, code, join_token, host_user_id, host_profile_id, phase, runtime)
					SELECT $1, $1, $1, user_id, 'profile-1', 'playing',
						jsonb_build_object('members', jsonb_build_object('m-1', jsonb_build_object('session_id', $2::text)))
					FROM playback_v3_attempts WHERE session_id = $2::uuid`, "pt-"+uuid.NewString(), f.session)
			},
		} {
			t.Run(name, func(t *testing.T) {
				f := newPausedAttemptFixture(t)
				f.pausedFor(pausedAttemptTimeout + time.Minute)
				exempt(f)
				if f.expire(t, uuid.NewString()) {
					t.Fatal("the claim ended an exempt play")
				}
			})
		}
	})

	// A room whose runtime holds no members object lists nobody: the claim
	// neither fails on it nor treats it as holding the session.
	t.Run("a room without a members object", func(t *testing.T) {
		f := newPausedAttemptFixture(t)
		f.pausedFor(pausedAttemptTimeout + time.Minute)
		for _, members := range []string{`null`, `[]`} {
			f.exec(`INSERT INTO watch_together_rooms (id, code, join_token, host_user_id, host_profile_id, phase, runtime)
				SELECT $1, $1, $1, user_id, 'profile-1', 'playing', jsonb_build_object('members', $3::jsonb)
				FROM playback_v3_attempts WHERE session_id = $2::uuid`, "pt-"+uuid.NewString(), f.session, members)
		}
		if !f.expire(t, uuid.NewString()) {
			t.Fatal("a room without members kept a session past the timeout")
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
