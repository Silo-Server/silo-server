package watchtogether

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// selectionOnceStore serializes identity comparison and selection replacement.
// V1 writers keep their existing unconditional reset semantics.
type selectionOnceStore interface {
	SelectOnce(context.Context, string, int, string, SelectItemInput, bool, int64, time.Time) (*Room, bool, error)
}

func (r *Repository) SelectOnce(ctx context.Context, roomID string, user int, profile string, selection SelectItemInput, viaVote bool, expected int64, now time.Time) (*Room, bool, error) {
	return r.selectOnce(ctx, roomID, user, profile, selection, viaVote, expected, now, false)
}

// PromoteOnce preserves the currently selected content variant. Suggestions
// identify content only; resolving a new default file must not restart it.
func (r *Repository) PromoteOnce(ctx context.Context, roomID string, user int, profile string, selection SelectItemInput, viaVote bool, expected int64, now time.Time) (*Room, bool, error) {
	return r.selectOnce(ctx, roomID, user, profile, selection, viaVote, expected, now, true)
}

func (r *Repository) selectOnce(ctx context.Context, roomID string, user int, profile string, selection SelectItemInput, viaVote bool, expected int64, now time.Time, contentOnly bool) (*Room, bool, error) {
	if r == nil || r.pool == nil {
		return nil, false, fmt.Errorf("watch together repository unavailable")
	}
	var tx pgx.Tx
	var err error
	commit := func(context.Context) error { return nil }
	if op := operationFrom(ctx); op != nil {
		tx = op.tx
	} else {
		tx, err = r.pool.Begin(ctx)
		if err != nil {
			return nil, false, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		commit = tx.Commit
	}
	room, err := scanRoom(tx.QueryRow(ctx, `SELECT `+roomColumns+` FROM watch_together_rooms WHERE id=$1 FOR UPDATE`, roomID))
	if err != nil {
		return nil, false, err
	}
	if room.HostUserID != user || room.HostProfileID != profile {
		return nil, false, ErrRoomForbidden
	}
	if room.Phase == RoomPhaseEnded {
		return nil, false, ErrRoomClosed
	}
	if room.SelectionMode == RoomSelectionModeVote && !viaVote {
		return nil, false, ErrVoteRoomSelection
	}
	// A staged lobby item is not "already playing": selecting or promoting
	// the very thing that is staged must still start it.
	identical := room.Phase == RoomPhasePlaying && room.SelectedContentID != nil && *room.SelectedContentID == selection.ContentID && (contentOnly || (equalSelectionID(room.SelectedFileID, selection.FileID) && equalSelectionID(room.SelectedLibraryID, selection.LibraryID)))
	if identical || room.Generation != expected {
		if err := commit(ctx); err != nil {
			return nil, false, err
		}
		return room, false, nil
	}
	room, err = scanRoom(tx.QueryRow(ctx, `UPDATE watch_together_rooms SET
 phase='playing', playback_state='waiting', resume_on_ready=true,
 selected_content_id=$2, selected_file_id=$3, selected_library_id=$4,
 anchor_position_seconds=0, is_paused=true, anchor_updated_at=$5,
 selection_revision=selection_revision+1, generation=generation+1
 WHERE id=$1 RETURNING `+roomColumns, roomID, selection.ContentID, selection.FileID, selection.LibraryID, now.UTC()))
	if err != nil {
		return nil, false, err
	}
	if err := commit(ctx); err != nil {
		return nil, false, err
	}
	return room, true, nil
}

func equalSelectionID(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// SelectItemOnce is the v2 selection path. It never resets an identical current
// resolved selection; a different intervening selection is not a replay receipt.
func (s *Service) SelectItemOnce(ctx context.Context, roomID string, user int, profile string, input SelectItemInput) (Snapshot, error) {
	return s.selectItemOnce(ctx, roomID, user, profile, input, false, false)
}

func (s *Service) selectItemOnceInRoom(ctx context.Context, roomID string, user int, profile string, resolved *ResolvedSelection, viaVote, promotion bool) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, fmt.Errorf("watch together unavailable")
	}
	store, ok := s.repo.(selectionOnceStore)
	if !ok || s.selectionResolver == nil {
		return Snapshot{}, fmt.Errorf("watch together selection unavailable")
	}
	write := store.SelectOnce
	if promotion {
		promoting, ok := s.repo.(interface {
			PromoteOnce(context.Context, string, int, string, SelectItemInput, bool, int64, time.Time) (*Room, bool, error)
		})
		if !ok {
			return Snapshot{}, ErrSuggestionPromotionUnavailable
		}
		write = promoting.PromoteOnce
	}
	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	expected := live.room.Generation
	s.mu.Unlock()
	room, applied, err := write(ctx, roomID, user, profile, SelectItemInput{ContentID: resolved.ContentID, FileID: resolved.FileID, LibraryID: resolved.LibraryID}, viaVote, expected, s.now())
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	// A local close/removal must never be resurrected by a delayed receipt.
	if s.rooms[roomID] != live || live.room.Phase == RoomPhaseEnded {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomClosed
	}
	s.adoptSelectionWriteLocked(live, room)
	snapshot := s.buildSnapshotLocked(live, user, profile)
	if !applied {
		s.mu.Unlock()
		return snapshot, nil
	}
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()
	s.sendDispatches(ctx, dispatches)
	s.publishRoomStateAfterCommit(ctx, *room)
	return snapshot, nil
}

// adoptSelectionWriteLocked reconciles the live room with a row a serialized
// selection write returned. A newer selection revision means playback
// restarted: every member's attached session, pending command, buffering
// readiness and lobby "ready" belong to the previous epoch and are dropped.
// Must be called with s.mu held.
func (s *Service) adoptSelectionWriteLocked(live *liveRoom, room *Room) {
	if room == nil || room.Generation < live.room.Generation {
		return
	}
	if room.SelectionRevision > live.room.SelectionRevision {
		live.command = nil
		for _, member := range live.members {
			if member != nil {
				member.resetForSelection()
			}
		}
		live.bufferingWaitAt = time.Time{}
		s.disarmWaitingDeadlineLocked(live)
	}
	live.room = *room
}

// startOnceStore serializes the lobby-to-playing transition of a staged item.
type startOnceStore interface {
	StartStagedOnce(context.Context, string, int, string, int64, time.Time) (*Room, bool, error)
}

// StartStagedOnce moves a lobby with a staged item to playing/waiting exactly
// the way a selection does, without changing what is staged. An already
// playing room is a no-op receipt (a double press), so the host never restarts
// playback by accident.
func (r *Repository) StartStagedOnce(ctx context.Context, roomID string, user int, profile string, expected int64, now time.Time) (*Room, bool, error) {
	if r == nil || r.pool == nil {
		return nil, false, fmt.Errorf("watch together repository unavailable")
	}
	var tx pgx.Tx
	var err error
	commit := func(context.Context) error { return nil }
	if op := operationFrom(ctx); op != nil {
		tx = op.tx
	} else {
		tx, err = r.pool.Begin(ctx)
		if err != nil {
			return nil, false, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		commit = tx.Commit
	}
	room, err := scanRoom(tx.QueryRow(ctx, `SELECT `+roomColumns+` FROM watch_together_rooms WHERE id=$1 FOR UPDATE`, roomID))
	if err != nil {
		return nil, false, err
	}
	if room.HostUserID != user || room.HostProfileID != profile {
		return nil, false, ErrRoomForbidden
	}
	if room.Phase == RoomPhaseEnded {
		return nil, false, ErrRoomClosed
	}
	if room.Phase == RoomPhasePlaying || room.Generation != expected {
		if err := commit(ctx); err != nil {
			return nil, false, err
		}
		return room, false, nil
	}
	if room.SelectedContentID == nil || strings.TrimSpace(*room.SelectedContentID) == "" {
		return nil, false, ErrNoStagedSelection
	}
	room, err = scanRoom(tx.QueryRow(ctx, `UPDATE watch_together_rooms SET
 phase='playing', playback_state='waiting', resume_on_ready=true,
 anchor_position_seconds=0, is_paused=true, anchor_updated_at=$2,
 selection_revision=selection_revision+1, generation=generation+1
 WHERE id=$1 RETURNING `+roomColumns, roomID, now.UTC()))
	if err != nil {
		return nil, false, err
	}
	if err := commit(ctx); err != nil {
		return nil, false, err
	}
	return room, true, nil
}

// StartStagedOnce is the host's explicit "play" for a staged lobby item.
func (s *Service) startStagedOnce(ctx context.Context, roomID string, user int, profile string) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, fmt.Errorf("watch together unavailable")
	}
	store, ok := s.repo.(startOnceStore)
	if !ok {
		return Snapshot{}, fmt.Errorf("watch together start unavailable")
	}
	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	expected := live.room.Generation
	s.mu.Unlock()
	room, applied, err := store.StartStagedOnce(ctx, roomID, user, profile, expected, s.now())
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	if s.rooms[roomID] != live || live.room.Phase == RoomPhaseEnded {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomClosed
	}
	s.adoptSelectionWriteLocked(live, room)
	snapshot := s.buildSnapshotLocked(live, user, profile)
	if !applied {
		s.mu.Unlock()
		return snapshot, nil
	}
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()
	s.sendDispatches(ctx, dispatches)
	s.publishRoomStateAfterCommit(ctx, *room)
	return snapshot, nil
}

// stopOnceStore serializes the playing-to-lobby transition.
type stopOnceStore interface {
	StopPlaybackOnce(context.Context, string, int, string, int64, time.Time) (*Room, bool, error)
}

// StopPlaybackOnce returns a playing room to the lobby without ending it. The
// selection stays staged so the host can start it again or pick something
// else; the revision advances because the playback epoch is over and every
// attached session belongs to it. A room that is not playing is a no-op
// receipt, so a double press cannot disturb a lobby.
func (r *Repository) StopPlaybackOnce(ctx context.Context, roomID string, user int, profile string, expected int64, now time.Time) (*Room, bool, error) {
	if r == nil || r.pool == nil {
		return nil, false, fmt.Errorf("watch together repository unavailable")
	}
	var tx pgx.Tx
	var err error
	commit := func(context.Context) error { return nil }
	if op := operationFrom(ctx); op != nil {
		tx = op.tx
	} else {
		tx, err = r.pool.Begin(ctx)
		if err != nil {
			return nil, false, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		commit = tx.Commit
	}
	room, err := scanRoom(tx.QueryRow(ctx, `SELECT `+roomColumns+` FROM watch_together_rooms WHERE id=$1 FOR UPDATE`, roomID))
	if err != nil {
		return nil, false, err
	}
	if room.HostUserID != user || room.HostProfileID != profile {
		return nil, false, ErrRoomForbidden
	}
	if room.Phase == RoomPhaseEnded {
		return nil, false, ErrRoomClosed
	}
	if room.Phase != RoomPhasePlaying || room.Generation != expected {
		if err := commit(ctx); err != nil {
			return nil, false, err
		}
		return room, false, nil
	}
	room, err = scanRoom(tx.QueryRow(ctx, `UPDATE watch_together_rooms SET
 phase='lobby', playback_state='idle', resume_on_ready=false,
 anchor_position_seconds=0, is_paused=true, anchor_updated_at=$2,
 selection_revision=selection_revision+1, generation=generation+1
 WHERE id=$1 RETURNING `+roomColumns, roomID, now.UTC()))
	if err != nil {
		return nil, false, err
	}
	if err := commit(ctx); err != nil {
		return nil, false, err
	}
	return room, true, nil
}

// StopPlaybackOnce is the host's "stop for everyone": the party stays open in
// the lobby with the item still staged.
func (s *Service) stopPlaybackOnce(ctx context.Context, roomID string, user int, profile string) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, fmt.Errorf("watch together unavailable")
	}
	store, ok := s.repo.(stopOnceStore)
	if !ok {
		return Snapshot{}, fmt.Errorf("watch together stop unavailable")
	}
	_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	expected := live.room.Generation
	s.mu.Unlock()
	room, applied, err := store.StopPlaybackOnce(ctx, roomID, user, profile, expected, s.now())
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	if s.rooms[roomID] != live || live.room.Phase == RoomPhaseEnded {
		s.mu.Unlock()
		return Snapshot{}, ErrRoomClosed
	}
	s.adoptSelectionWriteLocked(live, room)
	snapshot := s.buildSnapshotLocked(live, user, profile)
	if !applied {
		s.mu.Unlock()
		return snapshot, nil
	}
	dispatches := s.prepareSnapshotDispatchesLocked(live)
	s.mu.Unlock()
	s.sendDispatches(ctx, dispatches)
	s.publishRoomStateAfterCommit(ctx, *room)
	return snapshot, nil
}
