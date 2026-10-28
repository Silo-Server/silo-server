package handlers

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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
			WatchedSeconds: position, EndReason: reason,
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
	NewPlaybackHandler(mgr).sweepLifecycle(ctx, pool, now)
	candidates, err := loadLifecycleCandidates(ctx, pool, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 || candidates[0].sessionID != abandoned || reporting[candidates[0].sessionID] {
		t.Fatalf("first candidate after the sweep = %+v, want the abandoned play %s", candidates, abandoned)
	}
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
