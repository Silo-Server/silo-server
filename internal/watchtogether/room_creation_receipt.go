package watchtogether

import (
	"context"
	"errors"
	"fmt"
)

var ErrRoomCreationIdentityConflict = errors.New("room creation identity was already used")

// CreateRoomOnce binds a v2 caller-selected ID to its original host and mode.
// Replay returns current active state; closed/deleted rooms are never recreated.
// The caller supplies server-generated credentials and initial room state.
func (r *Repository) CreateRoomOnce(ctx context.Context, room Room) (*Room, bool, error) {
	if r == nil || r.pool == nil {
		return nil, false, fmt.Errorf("watch together repository unavailable")
	}
	if room.ID == "" || room.HostUserID <= 0 || room.HostProfileID == "" || (room.SelectionMode != RoomSelectionModeHostPick && room.SelectionMode != RoomSelectionModeVote) {
		return nil, false, ErrInvalidSelection
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `INSERT INTO watch_together_room_creation_receipts(room_id,host_user_id,host_profile_id,selection_mode) VALUES($1,$2,$3,$4) ON CONFLICT(room_id) DO NOTHING`, room.ID, room.HostUserID, room.HostProfileID, room.SelectionMode)
	if err != nil {
		return nil, false, err
	}
	inserted := tag.RowsAffected() == 1
	if !inserted {
		var user int
		var profile string
		var mode RoomSelectionMode
		if err = tx.QueryRow(ctx, `SELECT host_user_id,host_profile_id,selection_mode FROM watch_together_room_creation_receipts WHERE room_id=$1 FOR UPDATE`, room.ID).Scan(&user, &profile, &mode); err != nil {
			return nil, false, err
		}
		if user != room.HostUserID || profile != room.HostProfileID || mode != room.SelectionMode {
			return nil, false, ErrRoomCreationIdentityConflict
		}
	}
	var result *Room
	if inserted {
		result, err = scanRoom(tx.QueryRow(ctx, `INSERT INTO watch_together_rooms (
 id,code,join_token,host_user_id,host_profile_id,phase,playback_state,resume_on_ready,selection_mode,selection_revision,
 selected_content_id,selected_file_id,selected_library_id,guest_control_policy,anchor_position_seconds,is_paused,
 anchor_updated_at,generation,created_at,closed_at
 ) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
 ON CONFLICT(id) DO NOTHING RETURNING `+roomColumns,
			room.ID, room.Code, room.JoinToken, room.HostUserID, room.HostProfileID, room.Phase, room.PlaybackState, room.ResumeOnReady, room.SelectionMode, room.SelectionRevision,
			room.SelectedContentID, room.SelectedFileID, room.SelectedLibraryID, room.GuestControlPolicy, room.AnchorPositionSeconds, room.IsPaused, room.AnchorUpdatedAt.UTC(), room.Generation, room.CreatedAt.UTC(), room.ClosedAt))
	} else {
		result, err = scanRoom(tx.QueryRow(ctx, `SELECT `+roomColumns+` FROM watch_together_rooms WHERE id=$1 FOR UPDATE`, room.ID))
	}
	if errors.Is(err, ErrRoomNotFound) {
		return nil, false, ErrRoomCreationIdentityConflict
	}
	if err != nil {
		return nil, false, err
	}
	if result.HostUserID != room.HostUserID || result.HostProfileID != room.HostProfileID {
		return nil, false, ErrRoomCreationIdentityConflict
	}
	if result.Phase == RoomPhaseEnded {
		return nil, false, ErrRoomClosed
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return result, inserted, nil
}
