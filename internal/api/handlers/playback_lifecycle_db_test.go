package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestPlaybackLifecycleTransitionsDB pins the lifecycle store's rules: a
// start is announced once, an unsequenced pause applies only to frontends
// that report one and only when it changes the state, and the first end
// wins: it alone is announced, every later end records its reason, and the
// history row carries its sequence number. An end that overtakes its start
// leaves a stopped row, so the late start announces nothing.
func TestPlaybackLifecycleTransitionsDB(t *testing.T) {
	ctx, pool, account := lifecycleTestDB(t)
	store := NewPGPlaybackAdminStore(pool, nil)
	started := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	start := func(id, frontend string) (LifecycleTransition, bool) {
		t.Helper()
		got, ok, err := store.StartLifecycle(ctx, LifecycleStart{SessionID: id, Frontend: frontend, UserID: account, ProfileID: "p-1", MediaFileID: 7, RequestedMediaFileID: 7, StartedAt: started})
		if err != nil {
			t.Fatal(err)
		}
		return got, ok
	}
	end := func(id string, reason PlaybackEndReason, position float64) (LifecycleTransition, bool) {
		t.Helper()
		got, won, err := store.RecordEnd(ctx, AdminPlaybackHistoryEntry{
			SessionID: id, UserID: account, ProfileID: "p-1", MediaFileID: 7, PlayMethod: "direct_play",
			StartedAt: started.Format(time.RFC3339Nano), EndedAt: started.Add(time.Hour).Format(time.RFC3339Nano),
			EndPositionSeconds: position, EndReason: reason,
		}, LifecycleFrontendJellyfin)
		if err != nil {
			t.Fatal(err)
		}
		return got, won
	}
	historyRow := func(id string) (string, int64) {
		t.Helper()
		var reason string
		var seq int64
		if err := pool.QueryRow(ctx, `SELECT end_reason, lifecycle_seq FROM admin_playback_history WHERE session_id = $1`, id).Scan(&reason, &seq); err != nil {
			t.Fatal(err)
		}
		return reason, seq
	}

	play := uuid.NewString()
	first, ok := start(play, LifecycleFrontendJellyfin)
	if !ok || !first.Started || first.State != LifecyclePlaying {
		t.Fatalf("start = %+v ok=%v", first, ok)
	}
	if again, ok := start(play, LifecycleFrontendJellyfin); ok {
		t.Fatalf("a second start was announced: %+v", again)
	}
	pause, ok, err := store.SetLifecyclePaused(ctx, play, true)
	if err != nil || !ok || pause.State != LifecyclePaused || pause.Seq <= first.Seq {
		t.Fatalf("pause = %+v ok=%v err=%v", pause, ok, err)
	}
	if _, ok, _ := store.SetLifecyclePaused(ctx, play, true); ok {
		t.Fatal("a repeated pause was announced")
	}
	stopped, won := end(play, PlaybackEndStopped, 1800)
	if !won || stopped.EndReason != PlaybackEndStopped || stopped.Seq <= pause.Seq {
		t.Fatalf("end = %+v won=%v", stopped, won)
	}
	if later, won := end(play, PlaybackEndExpired, 1900); won || later.EndReason != PlaybackEndStopped {
		t.Fatalf("a later end = %+v won=%v; want the stop's reason, not announced", later, won)
	}
	if reason, seq := historyRow(play); reason != string(PlaybackEndStopped) || seq != stopped.Seq {
		t.Fatalf("history = %s at %d, want stopped at %d", reason, seq, stopped.Seq)
	}
	if _, ok, _ := store.SetLifecyclePaused(ctx, play, false); ok {
		t.Fatal("a stopped session resumed")
	}

	native := uuid.NewString()
	start(native, LifecycleFrontendNative)
	if _, ok, _ := store.SetLifecyclePaused(ctx, native, true); ok {
		t.Fatal("an unsequenced pause moved a native session")
	}

	early := uuid.NewString()
	if tomb, won := end(early, PlaybackEndStopped, 60); won || tomb.Seq == 0 {
		t.Fatalf("an end before the start = %+v won=%v; want a silent tombstone", tomb, won)
	}
	if late, ok := start(early, LifecycleFrontendNative); ok {
		t.Fatalf("a start after the end was announced: %+v", late)
	}
}

// TestNativeSessionsListLifecycleDB pins the native list's lifecycle
// projection: a row takes its pause state and number from the lifecycle row
// together, a session the lifecycle says stopped is left out, and a native
// session no server holds but whose attempt is live is listed from the row
// it recorded at start, at the attempt's last position.
func TestNativeSessionsListLifecycleDB(t *testing.T) {
	ctx, pool, account := lifecycleTestDB(t)
	var folder, file int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('movies',$1) RETURNING id`, "lc-"+uuid.NewString()).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder) })
	if err := pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`, folder, "/lc/"+uuid.NewString()+".mkv").Scan(&file); err != nil {
		t.Fatal(err)
	}
	synced, ended, unheld := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{synced, ended} {
		if _, err := pool.Exec(ctx, `INSERT INTO playback_sessions_sync (session_id, user_id, profile_id, media_file_id, play_method, reporting_node, started_at, updated_at, last_sync_at, is_paused)
			VALUES ($1, $2, 'p-1', $3, 'direct_play', 'node-1', NOW(), NOW(), NOW(), FALSE)`, id, account, file); err != nil {
			t.Fatal(err)
		}
	}
	lifecycle := func(id, state string, facts any) int64 {
		t.Helper()
		raw, _ := json.Marshal(facts)
		var seq int64
		reason := any(nil)
		if state == LifecycleStopped {
			reason = "stopped"
		}
		if err := pool.QueryRow(ctx, `INSERT INTO playback_session_lifecycle
			(session_id, frontend, state, end_reason, seq, user_id, profile_id, media_file_id, requested_media_file_id, started_at, start_facts)
			VALUES ($1, 'native', $2, $3, nextval('playback_session_lifecycle_seq'), $4, 'p-1', $5, $5, NOW(), $6::jsonb) RETURNING seq`,
			id, state, reason, account, file, string(raw)).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		return seq
	}
	pausedSeq := lifecycle(synced, LifecyclePaused, map[string]any{})
	lifecycle(ended, LifecycleStopped, map[string]any{})
	lifecycle(unheld, LifecyclePlaying, map[string]any{
		"session_id": unheld, "user_id": account, "profile_id": "p-1", "media_file_id": file,
		"play_method": "direct_play", "started_at": time.Now().UTC(), "client_name": "Silo Web",
	})
	if _, err := pool.Exec(ctx, `INSERT INTO playback_v3_attempts
		(playback_attempt_id, session_id, user_id, profile_id, requested_media_file_id, effective_media_file_id,
		 current_plan_id, current_plan, normalized_request, expires_at, last_sequence, last_sample)
		VALUES ($1, $2::uuid, $3, 'p-1', $4, $4, 'plan-1', '{}', '{}', NOW() + interval '1 hour', 3,
		 '{"sequence":3,"position":1234.5,"is_paused":false}')`, uuid.NewString(), unheld, account, file); err != nil {
		t.Fatal(err)
	}

	admin := &AdminHandler{pool: pool}
	rows, err := admin.ReadAdminPlaybackSessions(ctx, PlaybackSessionsQuery{UserID: account}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]AdminPlaybackSessionView{}
	for _, row := range rows {
		got[row.SessionID] = row
	}
	if row, ok := got[synced]; !ok || !row.IsPaused || row.LifecycleSeq == nil || *row.LifecycleSeq != pausedSeq {
		t.Errorf("synced row = %+v, want paused at %d", row, pausedSeq)
	}
	if _, ok := got[ended]; ok {
		t.Error("a session the lifecycle says stopped is listed")
	}
	if row, ok := got[unheld]; !ok || row.PositionSeconds != 1234.5 || row.ClientName != "Silo Web" || row.ReportingNode != "" {
		t.Errorf("unheld row = %+v (listed %v), want the start row at the attempt's position", row, ok)
	}
}

func lifecycleTestDB(t *testing.T) (context.Context, *pgxpool.Pool, int) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var account int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "lifecycle-"+uuid.NewString()).Scan(&account); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM playback_sessions_sync WHERE user_id=$1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, account)
	})
	return ctx, pool, account
}

// TestPlaybackLifecycleOrderAndStartStateDB covers two orderings the events
// promise: an end that waits behind a pause still takes a higher number than
// that pause, and a start announces the pause state the attempt had already
// accepted rather than assuming playing.
func TestPlaybackLifecycleOrderAndStartStateDB(t *testing.T) {
	ctx, pool, account := lifecycleTestDB(t)
	store := NewPGPlaybackAdminStore(pool, nil)
	file := lifecycleTestFile(t, ctx, pool)
	started := time.Now().UTC()

	play := uuid.NewString()
	if _, ok, err := store.StartLifecycle(ctx, LifecycleStart{SessionID: play, Frontend: LifecycleFrontendJellyfin, UserID: account, ProfileID: "p-1", MediaFileID: file, RequestedMediaFileID: file, StartedAt: started}); err != nil || !ok {
		t.Fatalf("start: %v %v", ok, err)
	}
	// A pause holds the row while the end is already waiting on it.
	pause, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pause.Rollback(context.Background()) }()
	if _, err := pause.Exec(ctx, `SELECT 1 FROM playback_session_lifecycle WHERE session_id = $1 FOR UPDATE`, play); err != nil {
		t.Fatal(err)
	}
	ended := make(chan LifecycleTransition, 1)
	go func() {
		end, _, err := store.RecordEnd(context.Background(), AdminPlaybackHistoryEntry{
			SessionID: play, UserID: account, ProfileID: "p-1", MediaFileID: file, PlayMethod: "direct_play",
			StartedAt: started.Format(time.RFC3339Nano), EndedAt: started.Add(time.Minute).Format(time.RFC3339Nano), EndReason: PlaybackEndStopped,
		}, LifecycleFrontendJellyfin)
		if err != nil {
			t.Error(err)
		}
		ended <- end
	}()
	// Wait until the end is blocked on the row the pause holds.
	for deadline := time.Now().Add(5 * time.Second); ; {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%INSERT INTO playback_session_lifecycle%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the end never waited on the paused row")
		}
		<-time.After(10 * time.Millisecond)
	}
	var pauseSeq int64
	if err := pause.QueryRow(ctx, `UPDATE playback_session_lifecycle SET state='paused', seq=nextval('playback_session_lifecycle_seq')
		WHERE session_id = $1 RETURNING seq`, play).Scan(&pauseSeq); err != nil {
		t.Fatal(err)
	}
	if err := pause.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if end := <-ended; end.Seq <= pauseSeq {
		t.Fatalf("end seq %d is not above the pause it waited for (%d)", end.Seq, pauseSeq)
	}

	native := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO playback_v3_attempts
		(playback_attempt_id, session_id, user_id, profile_id, requested_media_file_id, effective_media_file_id,
		 current_plan_id, current_plan, normalized_request, expires_at, last_sequence, last_sample)
		VALUES ($1, $2::uuid, $3, 'p-1', $4, $4, 'plan-1', '{}', '{}', NOW() + interval '1 hour', 1,
		 '{"sequence":1,"position":30,"is_paused":true}')`, uuid.NewString(), native, account, file); err != nil {
		t.Fatal(err)
	}
	start, ok, err := store.StartLifecycle(ctx, LifecycleStart{SessionID: native, Frontend: LifecycleFrontendNative, UserID: account, ProfileID: "p-1", MediaFileID: file, RequestedMediaFileID: file, StartedAt: started})
	if err != nil || !ok || start.State != LifecyclePaused {
		t.Fatalf("start after an accepted pause = %+v ok=%v err=%v; want paused", start, ok, err)
	}
}

// TestLifecycleSweepSelectsWhatItCanEndDB pins that sessions still active
// never fill the sweep's batch: a hundred unheld sessions with fresh
// progress are left out, and the abandoned one behind them is selected.
func TestLifecycleSweepSelectsWhatItCanEndDB(t *testing.T) {
	ctx, pool, account := lifecycleTestDB(t)
	file := lifecycleTestFile(t, ctx, pool)
	insert := func(attemptAge time.Duration) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO playback_session_lifecycle
			(session_id, frontend, state, seq, user_id, profile_id, media_file_id, requested_media_file_id, started_at, updated_at)
			VALUES ($1, 'native', 'playing', nextval('playback_session_lifecycle_seq'), $2, 'p-1', $3, $3, NOW() - interval '2 hours', NOW() - interval '2 hours')`,
			id, account, file); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO playback_v3_attempts
			(playback_attempt_id, session_id, user_id, profile_id, requested_media_file_id, effective_media_file_id,
			 current_plan_id, current_plan, normalized_request, expires_at, updated_at)
			VALUES ($1, $2::uuid, $3, 'p-1', $4, $4, 'plan-1', '{}', '{}', NOW() + interval '1 hour', $5)`,
			uuid.NewString(), id, account, file, time.Now().Add(-attemptAge)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	active := map[string]bool{}
	for range lifecycleSweepBatch {
		active[insert(time.Second)] = true
	}
	abandoned := insert(time.Hour)
	candidates, err := loadLifecycleCandidates(ctx, pool, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range candidates {
		if active[c.sessionID] {
			t.Fatalf("an active session was selected: %s", c.sessionID)
		}
		found = found || c.sessionID == abandoned
	}
	if !found {
		t.Fatalf("the abandoned session was not selected among %d candidates", len(candidates))
	}
}

// TestLifecycleSweepPassesLiveJellyfinPlaysDB pins that unheld Jellyfin plays
// still reporting never starve the sweep: a hundred plays the expiry claim
// refuses (a report arrived) are moved behind sessions idle for longer, so
// the next selection reaches the abandoned play queued after them.
func TestLifecycleSweepPassesLiveJellyfinPlaysDB(t *testing.T) {
	ctx, pool, account := lifecycleTestDB(t)
	file := lifecycleTestFile(t, ctx, pool)
	insert := func(idle time.Duration) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO playback_session_lifecycle
			(session_id, frontend, state, seq, user_id, profile_id, media_file_id, requested_media_file_id, started_at, updated_at)
			VALUES ($1, 'jellyfin', 'playing', nextval('playback_session_lifecycle_seq'), $2, 'p-1', $3, $3, NOW() - interval '3 hours', $4)`,
			id, account, file, time.Now().Add(-idle)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	reporting := map[string]bool{}
	for range lifecycleSweepBatch {
		reporting[insert(2*time.Hour)] = true
	}
	abandoned := insert(time.Hour)
	mgr := playback.NewSessionManager(0, 0)
	mgr.SetCompatExpiryClaimer(func(context.Context, []playback.SessionExpiryCandidate) (map[string]bool, error) {
		return map[string]bool{}, nil // every play reported after the cutoff
	})
	now := time.Now()
	newKeyedPlaybackHandler(mgr).sweepLifecycle(ctx, pool, now)
	candidates, err := loadLifecycleCandidates(ctx, pool, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 || candidates[0].sessionID != abandoned || reporting[candidates[0].sessionID] {
		t.Fatalf("first candidate after the sweep = %+v, want the abandoned play %s", candidates, abandoned)
	}
}

// TestLifecycleSweepBacksOffFailedEndsDB pins what the sweep does when a
// session's history write fails: the session stays live so a later sweep
// can still record it, but waits a grace before the next try and moves
// behind sessions idle for longer, so a hundred ends that keep failing
// neither retry every sweep nor keep the abandoned session behind them from
// being ended.
func TestLifecycleSweepBacksOffFailedEndsDB(t *testing.T) {
	ctx, pool, account := lifecycleTestDB(t)
	file := lifecycleTestFile(t, ctx, pool)
	insert := func(idle time.Duration) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO playback_session_lifecycle
			(session_id, frontend, state, seq, user_id, profile_id, media_file_id, requested_media_file_id, started_at, updated_at)
			VALUES ($1, 'native', 'playing', nextval('playback_session_lifecycle_seq'), $2, 'p-1', $3, $3, NOW() - interval '3 hours', $4)`,
			id, account, file, time.Now().Add(-idle)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	failing := map[string]bool{}
	for range lifecycleSweepBatch {
		failing[insert(2*time.Hour)] = true
	}
	abandoned := insert(time.Hour)
	h := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	h.AdminStore = failingEndAdminStore{NewPGPlaybackAdminStore(pool, nil)}
	h.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: file, Duration: 3600}}
	now := time.Now()
	h.sweepLifecycle(ctx, pool, now)
	candidates, err := loadLifecycleCandidates(ctx, pool, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 || candidates[0].sessionID != abandoned || failing[candidates[0].sessionID] {
		t.Fatalf("first candidate after the sweep = %+v, want the abandoned session %s", candidates, abandoned)
	}
	var live int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM playback_session_lifecycle WHERE user_id = $1 AND state = 'playing'`,
		account).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != len(failing)+1 {
		t.Fatalf("%d sessions still live, want all %d kept for a later retry", live, len(failing)+1)
	}
}

// failingEndAdminStore is the Postgres admin store whose end write always
// fails, as a NOT NULL violation does.
type failingEndAdminStore struct{ *PGPlaybackAdminStore }

func (failingEndAdminStore) RecordEnd(context.Context, AdminPlaybackHistoryEntry, string) (LifecycleTransition, bool, error) {
	return LifecycleTransition{}, false, errors.New("null value in column violates not-null constraint")
}

func lifecycleTestFile(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var folder, file int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('movies',$1) RETURNING id`, "lc-"+uuid.NewString()).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder) })
	if err := pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`, folder, "/lc/"+uuid.NewString()+".mkv").Scan(&file); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestLifecycleSweepEndsReportedPlaysDB pins how an Audiobookshelf play a
// client only reports through its local session is listed and ended, with
// no server holding it: Activity lists it from its start row at the row's
// position; once reports stop for the reported grace the sweep closes its
// row and records history with its position and the listening its reports
// credited; a report landing after the sweep looked keeps the play going.
func TestLifecycleSweepEndsReportedPlaysDB(t *testing.T) {
	ctx, pool, account := lifecycleTestDB(t)
	file := lifecycleTestFile(t, ctx, pool)
	book := "lc-book-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title) VALUES($1,'audiobook','Book')`, book); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM admin_playback_history WHERE user_id=$1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM abs_playback_sessions WHERE user_id=$1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM playback_session_lifecycle WHERE user_id=$1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, book)
	})
	silent := insertReportedPlay(t, ctx, pool, account, file, book, 2*time.Minute)
	live := insertReportedPlay(t, ctx, pool, account, file, book, 10*time.Second)

	admin := &AdminHandler{pool: pool}
	listed := func() map[string]AdminPlaybackSessionView {
		t.Helper()
		rows, err := admin.ReadAdminPlaybackSessions(ctx, PlaybackSessionsQuery{UserID: account}, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]AdminPlaybackSessionView{}
		for _, row := range rows {
			got[row.SessionID] = row
		}
		return got
	}
	if row, ok := listed()[live]; !ok || row.PositionSeconds != 600 || row.ClientName != "Plappa" {
		t.Fatalf("live reported play = %+v (listed %v), want its start row at 600 s", row, ok)
	}

	h := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	h.AdminStore = NewPGPlaybackAdminStore(pool, nil)
	h.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: file, Duration: 3600}}

	// A report that lands after the sweep read the silent play keeps it.
	candidates, err := loadLifecycleCandidates(ctx, pool, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var stale *lifecycleCandidate
	for i := range candidates {
		if candidates[i].sessionID == live {
			t.Fatal("a reported play still reporting was selected")
		}
		if candidates[i].sessionID == silent {
			stale = &candidates[i]
		}
	}
	if stale == nil || !stale.reported || !stale.reportedOpen {
		t.Fatalf("silent reported play not selected as one: %+v", candidates)
	}
	if _, err := pool.Exec(ctx, `UPDATE abs_playback_sessions SET last_sync_at = NOW() WHERE id = $1`, silent); err != nil {
		t.Fatal(err)
	}
	h.endReportedPlay(ctx, pool, *stale, time.Now())
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM playback_session_lifecycle WHERE session_id = $1`, silent).Scan(&state); err != nil || state != LifecyclePlaying {
		t.Fatalf("play with a late report: state %q, %v; want still playing", state, err)
	}

	// Silent past the grace: the sweep ends it.
	if _, err := pool.Exec(ctx, `UPDATE abs_playback_sessions SET last_sync_at = NOW() - interval '2 minutes' WHERE id = $1`, silent); err != nil {
		t.Fatal(err)
	}
	h.sweepLifecycle(ctx, pool, time.Now())
	var closed bool
	var watched, played, start float64
	var reason string
	if err := pool.QueryRow(ctx, `SELECT s.closed_at IS NOT NULL, h.watched_seconds, h.played_seconds, h.start_position_seconds, h.end_reason
		FROM abs_playback_sessions s JOIN admin_playback_history h ON h.session_id = s.id WHERE s.id = $1`, silent).
		Scan(&closed, &watched, &played, &start, &reason); err != nil {
		t.Fatalf("ended reported play has no history row: %v", err)
	}
	// It began at 100 s and seeked: the start is not 600 less 300 listened.
	if !closed || watched != 600 || played != 300 || start != 100 || reason != string(PlaybackEndExpired) {
		t.Fatalf("ended play: closed %v, watched %v, played %v, start %v, reason %q; want closed, 600, 300, 100, expired",
			closed, watched, played, start, reason)
	}
	got := listed()
	if _, ok := got[silent]; ok {
		t.Error("an ended reported play is still listed")
	}
	if _, ok := got[live]; !ok {
		t.Error("the sweep ended a reported play still reporting")
	}
}

// insertReportedPlay stores an open Audiobookshelf reported play that began
// ten minutes ago, last reported lastReport ago, and its lifecycle start.
func insertReportedPlay(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account, file int, book string, lastReport time.Duration) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO abs_playback_sessions
		(id, user_id, profile_id, content_id, media_file_id, started_at, last_sync_at,
		 time_listening_seconds, current_position_seconds, client_session_id)
		VALUES ($1, $2, 'p-1', $3, $4, NOW() - interval '10 minutes', $5, 300, 600, $6)`,
		id, account, book, file, time.Now().Add(-lastReport), "local-"+id); err != nil {
		t.Fatal(err)
	}
	facts, _ := json.Marshal(map[string]any{
		"session_id": id, "user_id": account, "profile_id": "p-1", "media_file_id": file,
		"play_method": "direct_play", "started_at": time.Now().Add(-10 * time.Minute).UTC(), "client_name": "Plappa",
		"position_seconds": 100,
	})
	if _, err := pool.Exec(ctx, `INSERT INTO playback_session_lifecycle
		(session_id, frontend, state, seq, user_id, profile_id, media_file_id, requested_media_file_id, started_at, updated_at, start_facts)
		VALUES ($1, 'abs', 'playing', nextval('playback_session_lifecycle_seq'), $2, 'p-1', $3, $3,
		 NOW() - interval '10 minutes', NOW() - interval '10 minutes', $4::jsonb)`, id, account, file, string(facts)); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestEpisodeLiveRowNamesItAsHistoryDB pins that a live row and the history
// entry its play leaves describe an episode the same way: monitors match the
// two by these fields.
func TestEpisodeLiveRowNamesItAsHistoryDB(t *testing.T) {
	ctx, pool, account := lifecycleTestDB(t)
	file := lifecycleTestFile(t, ctx, pool)
	series, episode, session := "lc-series-"+uuid.NewString(), "lc-episode-"+uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM admin_playback_history WHERE user_id=$1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM episodes WHERE content_id=$1`, episode)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, series)
	})
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items(content_id,type,title) VALUES($1,'series','Example Show')`, []any{series}},
		{`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,3,'Pilot')`, []any{episode, series}},
		{`UPDATE media_files SET content_id=$1, episode_id=$2 WHERE id=$3`, []any{series, episode, file}},
		{`INSERT INTO playback_sessions_sync (session_id, user_id, profile_id, media_file_id, play_method, reporting_node, started_at, updated_at, last_sync_at, is_paused)
			VALUES ($1, $2, 'p-1', $3, 'transcode', 'node-1', NOW(), NOW(), NOW(), FALSE)`, []any{session, account, file}},
		{`INSERT INTO admin_playback_history(session_id,user_id,profile_id,media_item_id,media_file_id,play_method,started_at,ended_at,watched_seconds)
			VALUES($1,$2,'p-1',$3,$4,'transcode',NOW(),NOW(),60)`, []any{session, account, episode, file}},
	} {
		if _, err := pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	admin := &AdminHandler{pool: pool}
	rows, err := admin.ReadAdminPlaybackSessions(ctx, PlaybackSessionsQuery{UserID: account}, "", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("live rows = %+v, %v; want one", rows, err)
	}
	history, err := admin.ReadAdminPlaybackHistoryEntry(ctx, session)
	if err != nil || history == nil {
		t.Fatalf("history entry = %+v, %v", history, err)
	}
	live := rows[0]
	got := [4]string{live.MediaItemID, live.MediaTitle, live.MediaType, live.SeriesTitle}
	want := [4]string{history.MediaItemID, history.MediaTitle, history.MediaType, history.SeriesTitle}
	if got != want || want != [4]string{episode, "Pilot", "episode", "Example Show"} {
		t.Fatalf("live naming %q, history %q; want both %q", got, want, [4]string{episode, "Pilot", "episode", "Example Show"})
	}
}
