package watchtogether

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRoomCreationReceiptPG(t *testing.T) {
	pool := selectionPG(t)
	_, err := pool.Exec(t.Context(), `CREATE TABLE users(id integer PRIMARY KEY);INSERT INTO users VALUES(7),(8);
 ALTER TABLE watch_together_rooms ADD UNIQUE(code),ADD UNIQUE(join_token),ADD FOREIGN KEY(host_user_id) REFERENCES users(id) ON DELETE CASCADE;`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906174406_add_watch_together_room_creation_receipts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), strings.Split(string(migration), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	original := baseRoom(time.Now().UTC())
	original.ID = "request"
	original.Phase = RoomPhaseLobby
	original.PlaybackState = RoomPlaybackStateIdle
	original.SelectionRevision = 0
	original.SelectedContentID = nil
	original.IsPaused = true
	original.AnchorPositionSeconds = 0
	type result struct {
		room    *Room
		created bool
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, suffix := range []string{"A", "B"} {
		input := original
		input.Code += suffix
		input.JoinToken += suffix
		wg.Go(func() {
			row, created, err := repo.CreateRoomOnce(t.Context(), input)
			results <- result{row, created, err}
		})
	}
	wg.Wait()
	close(results)
	created := 0
	var winner *Room
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.created {
			created++
			winner = r.room
		}
	}
	if created != 1 {
		t.Fatalf("created %d", created)
	}
	_, err = pool.Exec(t.Context(), `UPDATE watch_together_rooms SET generation=20,anchor_position_seconds=123,is_paused=false WHERE id=$1`, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	retry := original
	retry.Code = "new-code"
	retry.JoinToken = "new-token"
	retry.CreatedAt = retry.CreatedAt.Add(time.Hour)
	row, made, err := repo.CreateRoomOnce(t.Context(), retry)
	if err != nil || made || row.Code != winner.Code || row.JoinToken != winner.JoinToken || row.Generation != 20 || row.AnchorPositionSeconds != 123 || row.IsPaused {
		t.Fatalf("replay changed current state: %+v %v %v", row, made, err)
	}
	for _, alter := range []func(*Room){func(r *Room) { r.HostUserID = 8 }, func(r *Room) { r.HostProfileID = "other" }, func(r *Room) { r.SelectionMode = RoomSelectionModeVote }} {
		changed := retry
		alter(&changed)
		if _, _, err = repo.CreateRoomOnce(t.Context(), changed); !errors.Is(err, ErrRoomCreationIdentityConflict) {
			t.Fatalf("changed binding: %v", err)
		}
	}
	// Receipt lock and room lock must observe a committed concurrent closure.
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `UPDATE watch_together_rooms SET phase='ended' WHERE id=$1`, original.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := repo.CreateRoomOnce(t.Context(), retry); done <- err }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var blocked bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=current_setting('application_name') AND wait_event_type='Lock')`).Scan(&blocked); err != nil {
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
	case err := <-done:
		if !errors.Is(err, ErrRoomClosed) {
			t.Fatalf("closure: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err = pool.Exec(t.Context(), `DELETE FROM watch_together_rooms WHERE id=$1`, original.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = repo.CreateRoomOnce(t.Context(), retry); !errors.Is(err, ErrRoomCreationIdentityConflict) {
		t.Fatalf("resurrection: %v", err)
	}
	var receipts, rooms int
	if err = pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM watch_together_room_creation_receipts),(SELECT count(*) FROM watch_together_rooms)`).Scan(&receipts, &rooms); err != nil || receipts != 1 || rooms != 0 {
		t.Fatalf("deleted %d %d %v", receipts, rooms, err)
	}
	// A frozen-v1 identity collision cannot be adopted or leave a receipt behind.
	legacy := original
	legacy.ID = "legacy"
	if _, err = repo.CreateRoom(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	if _, _, err = repo.CreateRoomOnce(t.Context(), legacy); !errors.Is(err, ErrRoomCreationIdentityConflict) {
		t.Fatalf("legacy collision: %v", err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM watch_together_room_creation_receipts WHERE room_id='legacy'`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("collision receipt: %d %v", receipts, err)
	}
	if _, err = pool.Exec(t.Context(), `DELETE FROM users WHERE id=7`); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM watch_together_room_creation_receipts),(SELECT count(*) FROM watch_together_rooms)`).Scan(&receipts, &rooms); err != nil || receipts != 0 || rooms != 0 {
		t.Fatalf("account cleanup %d %d %v", receipts, rooms, err)
	}
}
