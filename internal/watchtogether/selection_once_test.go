package watchtogether

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func selectionPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "selection_" + uuid.NewString()
	ident := pgx.Identifier{schema}.Sanitize()
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET search_path TO "+ident)
		return err
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE")
		admin.Close()
	})
	_, err = pool.Exec(t.Context(), `CREATE TABLE watch_together_rooms (
 id text PRIMARY KEY, code text, join_token text, host_user_id integer, host_profile_id text,
 phase text, playback_state text, resume_on_ready boolean, selection_mode text, selection_revision bigint,
 selected_content_id text, selected_file_id integer, selected_library_id integer, guest_control_policy text,
 anchor_position_seconds double precision, is_paused boolean, anchor_updated_at timestamptz,
 generation bigint, created_at timestamptz, closed_at timestamptz)`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260920170519_watch_together_runtime.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, ok := strings.Cut(string(migration), "-- +goose Down")
	if !ok {
		t.Fatal("runtime migration has no down boundary")
	}
	if _, err = pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestSelectionOncePreservesAttachedReadinessPG(t *testing.T) {
	pool := selectionPG(t)
	repo := NewRepository(pool)
	now := time.Now().UTC()
	room := baseRoom(now)
	if _, err := repo.CreateRoom(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	s := newServiceForTest(now, &stubRepo{room: room}, nil, nil, &stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "movie", FileID: new(7), LibraryID: new(8)}})
	s.repo = repo
	conn := new(recordingConn)
	if _, _, err := s.Connect(t.Context(), room.ID, 7, "host", conn); err != nil {
		t.Fatal(err)
	}
	memberKey := buildMemberKey(7, "host")
	first, err := s.SelectItemOnce(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = withRoomOperation(t.Context(), s, room.ID, func(context.Context) (struct{}, error) {
		member := s.rooms[room.ID].members[memberKey]
		member.sessionID = "new-session"
		member.isReady, member.isBuffering, member.ignoreWait = true, true, true
		member.correctionCommand = &TransportCommand{CommandID: "pending-correction", SessionID: member.sessionID, SelectionRevision: first.SelectionRevision}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := len(conn.payloads)
	second, err := s.SelectItemOnce(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	member := s.rooms[room.ID].members[memberKey]
	if second.Generation != first.Generation || second.SelectionRevision != first.SelectionRevision || second.AnchorUpdatedAt != first.AnchorUpdatedAt || member.sessionID != "new-session" || !member.isReady || !member.isBuffering || !member.ignoreWait || len(conn.payloads) != before {
		t.Fatal("identical selection reset state or broadcast")
	}
	if member.correctionCommand == nil || member.correctionCommand.CommandID != "pending-correction" {
		t.Fatal("identical selection lost the pending correction")
	}
	// A separate service has stale local state but must still resolve the same DB identity as a no-op.
	other := newServiceForTest(now, &stubRepo{room: room}, nil, nil, s.selectionResolver)
	other.repo = repo
	third, err := other.SelectItemOnce(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "movie"})
	if err != nil || third.Generation != first.Generation {
		t.Fatalf("stale node: %+v %v", third, err)
	}
	if correction := other.rooms[room.ID].members[memberKey].correctionCommand; correction == nil || correction.CommandID != "pending-correction" {
		t.Fatal("a different node lost the pending correction during an identical selection")
	}
	// A genuinely different resolved selection still resets prior readiness once.
	s.selectionResolver = &stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "next", FileID: new(9), LibraryID: new(8)}}
	changed, err := s.SelectItemOnce(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "next"})
	member = s.rooms[room.ID].members[memberKey]
	if err != nil || changed.SelectionRevision != first.SelectionRevision+1 || changed.Generation != first.Generation+1 || member.sessionID != "" || member.isReady || member.isBuffering || member.ignoreWait || len(conn.payloads) != before+1 {
		t.Fatalf("new selection reset: %+v %v", changed, err)
	}
	if member.correctionCommand != nil {
		t.Fatal("new selection retained the previous correction")
	}

	for _, authority := range []struct {
		user    int
		profile string
	}{{8, "host"}, {7, "guest"}} {
		if _, err := s.SelectItemOnce(t.Context(), room.ID, authority.user, authority.profile, SelectItemInput{ContentID: "movie"}); !errors.Is(err, ErrRoomForbidden) {
			t.Fatalf("authority: %v", err)
		}
	}
	for _, tc := range []struct {
		phase, mode string
		want        error
	}{{"playing", "vote", ErrVoteRoomSelection}, {"ended", "host_pick", ErrRoomClosed}} {
		if _, err := pool.Exec(t.Context(), `UPDATE watch_together_rooms SET phase=$2,selection_mode=$3 WHERE id=$1`, room.ID, tc.phase, tc.mode); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.SelectOnce(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "next", FileID: new(9), LibraryID: new(8)}, false, changed.Generation, now); !errors.Is(err, tc.want) {
			t.Fatalf("no-op refusal: %v", err)
		}
	}

}

func TestSelectionOnceWaitsForCommittedIdentityPG(t *testing.T) {
	pool := selectionPG(t)
	repo := NewRepository(pool)
	now := time.Now().UTC()
	room := baseRoom(now)
	if _, err := repo.CreateRoom(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), `UPDATE watch_together_rooms SET selected_content_id='movie',selected_file_id=7,selected_library_id=8,selection_revision=9,generation=20,anchor_position_seconds=123,is_paused=false WHERE id=$1`, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	type receipt struct {
		room    *Room
		applied bool
		err     error
	}
	done := make(chan receipt, 1)
	go func() {
		r, a, e := repo.SelectOnce(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "movie", FileID: new(7), LibraryID: new(8)}, false, room.Generation, now)
		done <- receipt{r, a, e}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var blocked bool
		err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=current_setting('application_name') AND wait_event_type='Lock')`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		runtime.Gosched()
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.applied || r.room.Generation != 20 || r.room.SelectionRevision != 9 || r.room.AnchorPositionSeconds != 123 || r.room.IsPaused {
			t.Fatalf("locked no-op: %+v %v %v", r.room, r.applied, r.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Different selection with an old generation returns the winner without resetting it.
	r, applied, err := repo.SelectOnce(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "different"}, false, room.Generation, now)
	if err != nil || applied || r.Generation != 20 || *r.SelectedContentID != "movie" {
		t.Fatalf("conflict %v %v %+v", err, applied, r)
	}
}

func TestStartStagedOncePG(t *testing.T) {
	pool := selectionPG(t)
	repo := NewRepository(pool)
	now := time.Now().UTC()
	room := baseRoom(now)
	room.Phase = RoomPhaseLobby
	room.PlaybackState = RoomPlaybackStateIdle
	room.SelectionRevision = 0
	room.SelectedContentID = nil
	if _, err := repo.CreateRoom(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	s := newServiceForTest(now, &stubRepo{room: room}, nil, nil, &stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "movie", FileID: new(7), LibraryID: new(8)}})
	s.repo = repo
	t.Cleanup(s.Close)
	conn := new(recordingConn)
	reg, _, err := s.Connect(t.Context(), room.ID, 7, "host", conn)
	if err != nil {
		t.Fatal(err)
	}
	hostReady := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.rooms[room.ID].members[reg.memberKey].lobbyReady
	}

	if _, err := s.StartStagedOnce(t.Context(), room.ID, 7, "host"); !errors.Is(err, ErrNoStagedSelection) {
		t.Fatalf("empty lobby start: %v", err)
	}
	staged, err := s.StageItem(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	if staged.Phase != RoomPhaseLobby || staged.SelectionRevision != 0 || staged.Generation != room.Generation+1 {
		t.Fatalf("stage: %+v", staged)
	}
	if _, err := s.HandleLobbyReadyForConnection(t.Context(), reg, 7, "host", true); err != nil || !hostReady() {
		t.Fatalf("lobby ready: %v ready=%v", err, hostReady())
	}
	started, err := s.StartStagedOnce(t.Context(), room.ID, 7, "host")
	if err != nil {
		t.Fatal(err)
	}
	if started.Phase != RoomPhasePlaying || started.PlaybackState != RoomPlaybackStateWaiting || started.SelectionRevision != 1 || started.Generation != staged.Generation+1 || hostReady() || started.SelectedFileID == nil || *started.SelectedFileID != 7 {
		t.Fatalf("start: %+v ready=%v", started, hostReady())
	}
	again, err := s.StartStagedOnce(t.Context(), room.ID, 7, "host")
	if err != nil || again.Generation != started.Generation || again.SelectionRevision != started.SelectionRevision {
		t.Fatalf("double start: %+v %v", again, err)
	}
	if _, err := s.StartStagedOnce(t.Context(), room.ID, 8, "guest"); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("guest start: %v", err)
	}
	if _, err := s.StageItem(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "movie"}); !errors.Is(err, ErrRoomNotInLobby) {
		t.Fatalf("stage while playing: %v", err)
	}
}

func TestSelectOnceStartsAnIdenticalStagedItemPG(t *testing.T) {
	pool := selectionPG(t)
	repo := NewRepository(pool)
	now := time.Now().UTC()
	room := baseRoom(now)
	room.Phase = RoomPhaseLobby
	room.PlaybackState = RoomPlaybackStateIdle
	room.SelectionRevision = 0
	room.SelectedContentID = stringPtr("movie")
	room.SelectedFileID = new(7)
	room.SelectedLibraryID = new(8)
	if _, err := repo.CreateRoom(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	// "Play in <room>" from a detail page sends the very item that is staged.
	// That must start it, not be swallowed as an identical no-op.
	updated, applied, err := repo.SelectOnce(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "movie", FileID: new(7), LibraryID: new(8)}, false, room.Generation, now)
	if err != nil || !applied || updated.Phase != RoomPhasePlaying || updated.SelectionRevision != 1 {
		t.Fatalf("staged identical selection: applied=%v %+v %v", applied, updated, err)
	}
	// Once playing, the same identical selection is the documented no-op.
	_, applied, err = repo.SelectOnce(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "movie", FileID: new(7), LibraryID: new(8)}, false, updated.Generation, now)
	if err != nil || applied {
		t.Fatalf("playing identical selection: applied=%v %v", applied, err)
	}
}

func TestStopPlaybackOncePG(t *testing.T) {
	pool := selectionPG(t)
	repo := NewRepository(pool)
	now := time.Now().UTC()
	room := baseRoom(now)
	if _, err := repo.CreateRoom(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	s := newServiceForTest(now, &stubRepo{room: room}, &stubSessions{session: &playback.Session{UserID: 7, ProfileID: "host", MediaFileID: 1}}, &stubFiles{file: &models.MediaFile{ContentID: "movie-1"}}, &stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "movie", FileID: new(7), LibraryID: new(8)}})
	s.repo = repo
	t.Cleanup(s.Close)
	conn := new(recordingConn)
	reg, _, err := s.Connect(t.Context(), room.ID, 7, "host", conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AttachSessionForConnection(t.Context(), reg, 7, "host", "session"); err != nil {
		t.Fatal(err)
	}
	hostSession := func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.rooms[room.ID].members[reg.memberKey].sessionID
	}

	if _, err := s.StopPlaybackOnce(t.Context(), room.ID, 8, "guest"); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("guest stop: %v", err)
	}
	stopped, err := s.StopPlaybackOnce(t.Context(), room.ID, 7, "host")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Phase != RoomPhaseLobby || stopped.PlaybackState != RoomPlaybackStateIdle || stopped.SelectionRevision != room.SelectionRevision+1 || stopped.Generation != room.Generation+1 || hostSession() != "" || stopped.SelectedContentID == nil {
		t.Fatalf("stop: %+v session=%q", stopped, hostSession())
	}
	again, err := s.StopPlaybackOnce(t.Context(), room.ID, 7, "host")
	if err != nil || again.Generation != stopped.Generation || again.SelectionRevision != stopped.SelectionRevision {
		t.Fatalf("double stop: %+v %v", again, err)
	}
	restarted, err := s.StartStagedOnce(t.Context(), room.ID, 7, "host")
	if err != nil || restarted.Phase != RoomPhasePlaying || restarted.SelectionRevision != stopped.SelectionRevision+1 {
		t.Fatalf("restart: %+v %v", restarted, err)
	}
}
