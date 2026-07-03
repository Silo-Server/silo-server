package watchtogether

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrRoomNotFound = errors.New("watch together room not found")
var ErrRoomStateConflict = errors.New("watch together room state conflict")

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) CreateRoom(ctx context.Context, room Room) (*Room, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("watch together repository unavailable")
	}

	const query = `
		INSERT INTO watch_together_rooms (
			id, code, join_token, host_user_id, host_profile_id,
			phase, playback_state, resume_on_ready, selection_mode, selection_revision,
			selected_content_id, selected_file_id, selected_library_id,
			guest_control_policy,
			anchor_position_seconds, is_paused, anchor_updated_at,
			generation, created_at, closed_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13,
			$14,
			$15, $16, $17,
			$18, $19, $20
		)
		RETURNING ` + roomColumns

	created, err := scanRoom(r.pool.QueryRow(
		ctx,
		query,
		room.ID,
		room.Code,
		room.JoinToken,
		room.HostUserID,
		room.HostProfileID,
		room.Phase,
		room.PlaybackState,
		room.ResumeOnReady,
		room.SelectionMode,
		room.SelectionRevision,
		room.SelectedContentID,
		room.SelectedFileID,
		room.SelectedLibraryID,
		room.GuestControlPolicy,
		room.AnchorPositionSeconds,
		room.IsPaused,
		room.AnchorUpdatedAt,
		room.Generation,
		room.CreatedAt,
		room.ClosedAt,
	))
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (r *Repository) GetRoomByID(ctx context.Context, roomID string) (*Room, error) {
	return r.getRoom(ctx, `SELECT `+roomColumns+` FROM watch_together_rooms WHERE id = $1`, roomID)
}

func (r *Repository) GetRoomByCode(ctx context.Context, code string) (*Room, error) {
	return r.getRoom(
		ctx,
		`SELECT `+roomColumns+` FROM watch_together_rooms WHERE LOWER(code) = LOWER($1)`,
		strings.TrimSpace(code),
	)
}

func (r *Repository) GetRoomByJoinToken(ctx context.Context, joinToken string) (*Room, error) {
	return r.getRoom(
		ctx,
		`SELECT `+roomColumns+` FROM watch_together_rooms WHERE join_token = $1`,
		strings.TrimSpace(joinToken),
	)
}

func (r *Repository) UpdatePolicy(
	ctx context.Context,
	roomID string,
	policy GuestControlPolicy,
	generation int64,
	expectedGeneration int64,
) (*Room, error) {
	const query = `
		UPDATE watch_together_rooms
		SET guest_control_policy = $2,
		    generation = $3
		WHERE id = $1
		  AND generation = $4
		  AND phase <> 'ended'
		RETURNING ` + roomColumns

	return r.scanConditionalUpdate(ctx, query, roomID, policy, generation, expectedGeneration)
}

func (r *Repository) UpdateAnchor(
	ctx context.Context,
	roomID string,
	positionSeconds float64,
	isPaused bool,
	playbackState RoomPlaybackState,
	resumeOnReady bool,
	anchorUpdatedAt time.Time,
	generation int64,
	expectedGeneration int64,
) (*Room, error) {
	const query = `
		UPDATE watch_together_rooms
		SET anchor_position_seconds = $2,
		    is_paused = $3,
		    playback_state = $4,
		    resume_on_ready = $5,
		    anchor_updated_at = $6,
		    generation = $7
		WHERE id = $1
		  AND generation = $8
		  AND phase = 'playing'
		RETURNING ` + roomColumns

	return r.scanConditionalUpdate(
		ctx,
		query,
		roomID,
		positionSeconds,
		isPaused,
		playbackState,
		resumeOnReady,
		anchorUpdatedAt.UTC(),
		generation,
		expectedGeneration,
	)
}

// ListIdleRoomIDs returns rooms that are still open but have seen no
// playback-anchor activity since the cutoff, so the janitor can close them.
func (r *Repository) ListIdleRoomIDs(ctx context.Context, cutoff time.Time, limit int) ([]string, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("watch together repository unavailable")
	}

	const query = `
		SELECT id FROM watch_together_rooms
		WHERE phase <> 'ended'
		  AND GREATEST(anchor_updated_at, created_at) < $1
		LIMIT $2
	`

	rows, err := r.pool.Query(ctx, query, cutoff.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list idle watch together rooms: %w", err)
	}
	defer rows.Close()

	var roomIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan idle watch together room id: %w", err)
		}
		roomIDs = append(roomIDs, id)
	}
	return roomIDs, rows.Err()
}

func (r *Repository) CloseRoom(ctx context.Context, roomID string, closedAt time.Time) (*Room, error) {
	const query = `
		UPDATE watch_together_rooms
		SET phase = $2,
		    closed_at = $3
		WHERE id = $1
		  AND phase <> 'ended'
		RETURNING ` + roomColumns

	return r.scanConditionalUpdate(ctx, query, roomID, RoomPhaseEnded, closedAt.UTC())
}

func (r *Repository) UpdateSelection(
	ctx context.Context,
	roomID string,
	selection SelectItemInput,
	phase RoomPhase,
	playbackState RoomPlaybackState,
	resumeOnReady bool,
	anchorPosition float64,
	isPaused bool,
	anchorUpdatedAt time.Time,
	selectionRevision int64,
	generation int64,
	expectedGeneration int64,
) (*Room, error) {
	const query = `
		UPDATE watch_together_rooms
		SET phase = $2,
		    playback_state = $3,
		    resume_on_ready = $4,
		    selected_content_id = $5,
		    selected_file_id = $6,
		    selected_library_id = $7,
		    anchor_position_seconds = $8,
		    is_paused = $9,
		    anchor_updated_at = $10,
		    selection_revision = $11,
		    generation = $12
		WHERE id = $1
		  AND generation = $13
		  AND phase <> 'ended'
		RETURNING ` + roomColumns

	return r.scanConditionalUpdate(
		ctx,
		query,
		roomID,
		phase,
		playbackState,
		resumeOnReady,
		selection.ContentID,
		selection.FileID,
		selection.LibraryID,
		anchorPosition,
		isPaused,
		anchorUpdatedAt.UTC(),
		selectionRevision,
		generation,
		expectedGeneration,
	)
}

const roomColumns = `
	id, code, join_token, host_user_id, host_profile_id,
	phase, playback_state, resume_on_ready, selection_mode, selection_revision,
	selected_content_id, selected_file_id, selected_library_id,
	guest_control_policy,
	anchor_position_seconds, is_paused, anchor_updated_at,
	generation, created_at, closed_at
`

func (r *Repository) getRoom(ctx context.Context, query string, arg string) (*Room, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("watch together repository unavailable")
	}
	return scanRoom(r.pool.QueryRow(ctx, query, arg))
}

func scanRoom(row pgx.Row) (*Room, error) {
	var room Room
	if err := row.Scan(
		&room.ID,
		&room.Code,
		&room.JoinToken,
		&room.HostUserID,
		&room.HostProfileID,
		&room.Phase,
		&room.PlaybackState,
		&room.ResumeOnReady,
		&room.SelectionMode,
		&room.SelectionRevision,
		&room.SelectedContentID,
		&room.SelectedFileID,
		&room.SelectedLibraryID,
		&room.GuestControlPolicy,
		&room.AnchorPositionSeconds,
		&room.IsPaused,
		&room.AnchorUpdatedAt,
		&room.Generation,
		&room.CreatedAt,
		&room.ClosedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRoomNotFound
		}
		return nil, fmt.Errorf("scan watch together room: %w", err)
	}
	return &room, nil
}

func (r *Repository) scanConditionalUpdate(ctx context.Context, query string, args ...any) (*Room, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("watch together repository unavailable")
	}

	room, err := scanRoom(r.pool.QueryRow(ctx, query, args...))
	if !errors.Is(err, ErrRoomNotFound) {
		return room, err
	}

	roomID, _ := args[0].(string)
	existing, lookupErr := r.GetRoomByID(ctx, roomID)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if existing.Phase == RoomPhaseEnded {
		return nil, ErrRoomClosed
	}
	return nil, ErrRoomStateConflict
}
