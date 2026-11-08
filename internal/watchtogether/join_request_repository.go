package watchtogether

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// JoinRequestRepository implements JoinRequestStore using PostgreSQL.
type JoinRequestRepository struct {
	pool *pgxpool.Pool
}

func NewJoinRequestRepository(pool *pgxpool.Pool) *JoinRequestRepository {
	return &JoinRequestRepository{pool: pool}
}

const joinRequestColumns = `id, room_id, user_id, profile_id, display_name, status, created_at, decided_at`

func (r *JoinRequestRepository) ListPublicRooms(ctx context.Context, viewerUserID int, viewerProfileID string, limit int) ([]publicRoomRow, error) {
	if r == nil || r.pool == nil {
		return nil, ErrPublicRoomsUnavailable
	}
	const query = `
		SELECT r.id, r.host_user_id, r.host_profile_id, r.phase, r.playback_state, r.selection_mode,
		       r.selected_content_id, r.created_at, r.runtime,
		       jr.id, jr.display_name, jr.status, jr.created_at, jr.decided_at
		FROM watch_together_rooms r
		LEFT JOIN watch_together_join_requests jr
		       ON jr.room_id = r.id AND jr.user_id = $1 AND jr.profile_id = $2
		WHERE r.visibility = 'public' AND r.phase <> 'ended'
		ORDER BY r.created_at DESC, r.id
		LIMIT $3`
	rows, err := r.pool.Query(ctx, query, viewerUserID, viewerProfileID, limit)
	if err != nil {
		return nil, fmt.Errorf("list public watch together rooms: %w", err)
	}
	defer rows.Close()

	var out []publicRoomRow
	for rows.Next() {
		var row publicRoomRow
		var requestID, requestName, requestStatus *string
		var requestCreated, requestDecided *time.Time
		if err := rows.Scan(
			&row.Room.ID, &row.Room.HostUserID, &row.Room.HostProfileID, &row.Room.Phase, &row.Room.PlaybackState,
			&row.Room.SelectionMode, &row.Room.SelectedContentID, &row.Room.CreatedAt, &row.Runtime,
			&requestID, &requestName, &requestStatus, &requestCreated, &requestDecided,
		); err != nil {
			return nil, fmt.Errorf("scan public watch together room: %w", err)
		}
		row.Room.Visibility = RoomVisibilityPublic
		if requestID != nil {
			row.Request = &JoinRequest{
				ID: *requestID, RoomID: row.Room.ID, UserID: viewerUserID, ProfileID: viewerProfileID,
				DisplayName: *requestName, Status: JoinRequestStatus(*requestStatus), CreatedAt: *requestCreated, DecidedAt: requestDecided,
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *JoinRequestRepository) CreateJoinRequest(ctx context.Context, request JoinRequest) (*JoinRequest, bool, error) {
	if r == nil || r.pool == nil {
		return nil, false, ErrPublicRoomsUnavailable
	}
	// The room guard runs in the same statement, so a room unlisted or ended
	// after the service checked it takes no new request. Only a denied row is
	// reopened; the conflict update skips pending and approved rows.
	const upsert = `
		INSERT INTO watch_together_join_requests (id, room_id, user_id, profile_id, display_name, status, created_at)
		SELECT $1, r.id, $3, $4, $5, 'pending', $6
		FROM watch_together_rooms r
		WHERE r.id = $2 AND r.visibility = 'public' AND r.phase <> 'ended'
		ON CONFLICT (room_id, user_id, profile_id) DO UPDATE
		SET status = 'pending', display_name = EXCLUDED.display_name,
		    created_at = EXCLUDED.created_at, decided_at = NULL
		WHERE watch_together_join_requests.status = 'denied'
		RETURNING ` + joinRequestColumns
	row, err := scanJoinRequest(r.pool.QueryRow(ctx, upsert,
		request.ID, request.RoomID, request.UserID, request.ProfileID, request.DisplayName, request.CreatedAt.UTC()))
	if err == nil {
		return row, true, nil
	}
	if !errors.Is(err, ErrJoinRequestNotFound) {
		return nil, false, err
	}
	existing, err := scanJoinRequest(r.pool.QueryRow(ctx,
		`SELECT `+joinRequestColumns+` FROM watch_together_join_requests WHERE room_id = $1 AND user_id = $2 AND profile_id = $3`,
		request.RoomID, request.UserID, request.ProfileID))
	if errors.Is(err, ErrJoinRequestNotFound) {
		return nil, false, ErrRoomNotPublic
	}
	if err != nil {
		return nil, false, err
	}
	// A denied row that was not reopened means the room guard failed: the
	// room was unlisted or ended after the service checked it.
	if existing.Status == JoinRequestStatusDenied {
		return nil, false, ErrRoomNotPublic
	}
	return existing, false, nil
}

func (r *JoinRequestRepository) GetJoinRequest(ctx context.Context, requestID string) (*JoinRequest, error) {
	if r == nil || r.pool == nil {
		return nil, ErrPublicRoomsUnavailable
	}
	return scanJoinRequest(r.pool.QueryRow(ctx,
		`SELECT `+joinRequestColumns+` FROM watch_together_join_requests WHERE id = $1`, requestID))
}

func (r *JoinRequestRepository) ListPendingJoinRequests(ctx context.Context, roomID string, limit int) ([]JoinRequest, error) {
	if r == nil || r.pool == nil {
		return nil, ErrPublicRoomsUnavailable
	}
	rows, err := r.pool.Query(ctx, `SELECT `+joinRequestColumns+`
		FROM watch_together_join_requests
		WHERE room_id = $1 AND status = 'pending'
		ORDER BY created_at, id
		LIMIT $2`, roomID, limit)
	if err != nil {
		return nil, fmt.Errorf("list watch together join requests: %w", err)
	}
	defer rows.Close()
	out := []JoinRequest{}
	for rows.Next() {
		row, err := scanJoinRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	return out, rows.Err()
}

func (r *JoinRequestRepository) DecideJoinRequest(ctx context.Context, roomID, requestID string, status JoinRequestStatus, decidedAt time.Time) (*JoinRequest, error) {
	if r == nil || r.pool == nil {
		return nil, ErrPublicRoomsUnavailable
	}
	row, err := scanJoinRequest(r.pool.QueryRow(ctx, `
		UPDATE watch_together_join_requests
		SET status = $3, decided_at = $4
		WHERE id = $1 AND room_id = $2
		  AND NOT (status = 'approved' AND $3 = 'denied')
		RETURNING `+joinRequestColumns, requestID, roomID, status, decidedAt.UTC()))
	if !errors.Is(err, ErrJoinRequestNotFound) {
		return row, err
	}
	existing, lookupErr := scanJoinRequest(r.pool.QueryRow(ctx,
		`SELECT `+joinRequestColumns+` FROM watch_together_join_requests WHERE id = $1 AND room_id = $2`, requestID, roomID))
	if lookupErr != nil {
		return nil, lookupErr
	}
	if existing.Status == JoinRequestStatusApproved {
		return nil, ErrJoinRequestApproved
	}
	return nil, ErrJoinRequestNotFound
}

func scanJoinRequest(row pgx.Row) (*JoinRequest, error) {
	var request JoinRequest
	if err := row.Scan(
		&request.ID, &request.RoomID, &request.UserID, &request.ProfileID,
		&request.DisplayName, &request.Status, &request.CreatedAt, &request.DecidedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrJoinRequestNotFound
		}
		return nil, fmt.Errorf("scan watch together join request: %w", err)
	}
	return &request, nil
}
