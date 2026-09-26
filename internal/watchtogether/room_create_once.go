package watchtogether

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrRoomCreateUnavailable = errors.New("room creation unavailable")

type roomCreator interface {
	CreateRoomOnce(context.Context, Room) (*Room, bool, error)
}

// CreateRoomWithIdentity creates or resolves the retained creation intent.
// It builds a current snapshot without hydrating or replacing live membership.
func (s *Service) CreateRoomWithIdentity(ctx context.Context, id string, input CreateRoomInput) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, ErrRoomCreateUnavailable
	}
	store, ok := s.repo.(roomCreator)
	if !ok {
		return Snapshot{}, ErrRoomCreateUnavailable
	}
	if _, err := uuid.Parse(id); err != nil || input.HostUserID <= 0 || input.HostProfileID == "" || (input.SelectionMode != RoomSelectionModeHostPick && input.SelectionMode != RoomSelectionModeVote) {
		return Snapshot{}, ErrInvalidSelection
	}
	now := s.now()
	var row *Room
	var err error
	for range 3 {
		row, _, err = store.CreateRoomOnce(ctx, Room{
			ID: id, Code: randomToken(8), JoinToken: randomToken(24), HostUserID: input.HostUserID, HostProfileID: input.HostProfileID,
			Phase: RoomPhaseLobby, PlaybackState: RoomPlaybackStateIdle, SelectionMode: input.SelectionMode,
			GuestControlPolicy: GuestControlPolicyHostOnly, IsPaused: true, AnchorUpdatedAt: now, Generation: 1, CreatedAt: now,
		})
		if err == nil {
			break
		}
		// Only a definite rolled-back credential uniqueness violation permits a
		// fresh server credential candidate. Never retry an uncertain commit or
		// change the caller's identity/binding.
		pg, ok := errors.AsType[*pgconn.PgError](err)
		if !ok || pg.Code != "23505" || pg.TableName != "watch_together_rooms" ||
			(pg.ConstraintName != "watch_together_rooms_code_key" && pg.ConstraintName != "watch_together_rooms_join_token_key") {
			return Snapshot{}, err
		}
	}
	if err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Build a view without replacing the live room or its membership. A delayed
	// receipt must not roll back a newer local generation or revive a closed room.
	view := liveRoom{room: *row, members: make(map[string]*memberState)}
	if live := s.rooms[id]; live != nil {
		if live.room.Phase == RoomPhaseEnded {
			return Snapshot{}, ErrRoomClosed
		}
		view.members = live.members
		if live.room.Generation > row.Generation {
			view.room = live.room
		}
	}
	return s.buildSnapshotLocked(&view, input.HostUserID, input.HostProfileID), nil
}
