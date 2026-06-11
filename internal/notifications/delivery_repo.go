package notifications

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DeliveryRepository owns notification_deliveries.
type DeliveryRepository struct {
	pool *pgxpool.Pool
}

// NewDeliveryRepository creates a DeliveryRepository.
func NewDeliveryRepository(pool *pgxpool.Pool) *DeliveryRepository {
	return &DeliveryRepository{pool: pool}
}

// Cursor is an opaque pagination cursor over (created_at, id).
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// Encode returns the opaque wire form of the cursor.
func (c Cursor) Encode() string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses an opaque cursor produced by Encode.
func DecodeCursor(encoded string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Cursor{}, errors.New("invalid cursor")
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Cursor{}, errors.New("invalid cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return Cursor{}, errors.New("invalid cursor")
	}
	return Cursor{CreatedAt: createdAt, ID: parts[1]}, nil
}

// deliveryRowSelect joins display metadata so clients can render a row
// without an extra lookup. LEFT JOINs keep operational delivery types (no
// episode/series) and deleted catalog rows renderable.
const deliveryRowSelect = `
	SELECT d.id, d.release_event_id, d.user_id, d.profile_id, d.library_id, d.series_id, d.episode_id,
	       d.type, d.reason_flags, d.status, d.read_at, d.delivered_at, d.created_at,
	       COALESCE(s.title, '') AS series_title,
	       COALESCE(e.title, '') AS episode_title,
	       e.season_number, e.episode_number,
	       COALESCE(s.poster_path, '') AS poster_path,
	       COALESCE(s.poster_thumbhash, '') AS poster_thumbhash
	FROM notification_deliveries d
	LEFT JOIN episodes e ON e.content_id = d.episode_id
	LEFT JOIN media_items s ON s.content_id = d.series_id`

func scanDeliveryRows(rows pgx.Rows) ([]DeliveryRow, error) {
	defer rows.Close()
	out := make([]DeliveryRow, 0, 25)
	for rows.Next() {
		var row DeliveryRow
		if err := rows.Scan(
			&row.ID, &row.ReleaseEventID, &row.UserID, &row.ProfileID,
			&row.LibraryID, &row.SeriesID, &row.EpisodeID,
			&row.Type, &row.ReasonFlags, &row.Status, &row.ReadAt, &row.DeliveredAt, &row.CreatedAt,
			&row.SeriesTitle, &row.EpisodeTitle, &row.SeasonNumber, &row.EpisodeNumber,
			&row.PosterPath, &row.PosterThumbhash,
		); err != nil {
			return nil, fmt.Errorf("scan delivery row: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// BulkInsert inserts deliveries with ON CONFLICT DO NOTHING (both partial
// uniques participate: per-release-event and cross-library per-episode) and
// returns only the rows actually inserted. Realtime publish and channel
// dispatch must operate on the returned set, never the candidate set.
func (r *DeliveryRepository) BulkInsert(ctx context.Context, tx pgx.Tx, deliveries []Delivery) ([]InsertedDelivery, error) {
	const chunkSize = 500
	inserted := make([]InsertedDelivery, 0, len(deliveries))
	for start := 0; start < len(deliveries); start += chunkSize {
		end := min(start+chunkSize, len(deliveries))
		chunk := deliveries[start:end]

		var sb strings.Builder
		sb.WriteString(`
			INSERT INTO notification_deliveries
				(id, release_event_id, user_id, profile_id, library_id, series_id, episode_id,
				 type, reason_flags, status, delivered_at)
			VALUES `)
		args := make([]any, 0, len(chunk)*11)
		for i, delivery := range chunk {
			if i > 0 {
				sb.WriteString(", ")
			}
			base := len(args)
			sb.WriteString(fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11))
			status := delivery.Status
			if status == "" {
				status = "delivered"
			}
			args = append(args,
				delivery.ID, delivery.ReleaseEventID, delivery.UserID, delivery.ProfileID,
				delivery.LibraryID, delivery.SeriesID, delivery.EpisodeID,
				delivery.Type, delivery.ReasonFlags, status, time.Now().UTC(),
			)
		}
		sb.WriteString(" ON CONFLICT DO NOTHING RETURNING id, user_id, profile_id, created_at")

		rows, err := tx.Query(ctx, sb.String(), args...)
		if err != nil {
			return nil, fmt.Errorf("bulk insert deliveries: %w", err)
		}
		for rows.Next() {
			var row InsertedDelivery
			if err := rows.Scan(&row.ID, &row.UserID, &row.ProfileID, &row.CreatedAt); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan inserted delivery: %w", err)
			}
			inserted = append(inserted, row)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read inserted deliveries: %w", err)
		}
	}
	return inserted, nil
}

// ListInbox returns inbox rows newest-first for the profile.
func (r *DeliveryRepository) ListInbox(ctx context.Context, profileID string, unreadOnly bool, limit int, before *Cursor) ([]DeliveryRow, error) {
	conditions := []string{"d.profile_id = $1"}
	args := []any{profileID}
	if unreadOnly {
		conditions = append(conditions, "d.read_at IS NULL")
	}
	if before != nil {
		args = append(args, before.CreatedAt, before.ID)
		conditions = append(conditions, fmt.Sprintf("(d.created_at, d.id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit)
	query := deliveryRowSelect +
		" WHERE " + strings.Join(conditions, " AND ") +
		fmt.Sprintf(" ORDER BY d.created_at DESC, d.id DESC LIMIT $%d", len(args))
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list inbox: %w", err)
	}
	return scanDeliveryRows(rows)
}

// ListSync returns rows ascending from the cursor for forward sync (the
// mobile wake-fetch endpoint). A nil cursor returns the most recent page
// (still ascending) so first-time callers get a cursor to persist.
func (r *DeliveryRepository) ListSync(ctx context.Context, profileID string, since *Cursor, limit int) ([]DeliveryRow, error) {
	if since != nil {
		rows, err := r.pool.Query(ctx,
			deliveryRowSelect+`
			WHERE d.profile_id = $1 AND (d.created_at, d.id) > ($2, $3)
			ORDER BY d.created_at ASC, d.id ASC
			LIMIT $4`,
			profileID, since.CreatedAt, since.ID, limit)
		if err != nil {
			return nil, fmt.Errorf("list sync: %w", err)
		}
		return scanDeliveryRows(rows)
	}
	// No cursor: most recent page, returned in ascending order.
	rows, err := r.pool.Query(ctx, `
		SELECT * FROM (`+deliveryRowSelect+`
			WHERE d.profile_id = $1
			ORDER BY d.created_at DESC, d.id DESC
			LIMIT $2
		) recent ORDER BY recent.created_at ASC, recent.id ASC`,
		profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("list sync: %w", err)
	}
	return scanDeliveryRows(rows)
}

// GetByID returns one delivery scoped to the profile; (nil, nil) when absent.
func (r *DeliveryRepository) GetByID(ctx context.Context, profileID, id string) (*DeliveryRow, error) {
	rows, err := r.pool.Query(ctx,
		deliveryRowSelect+` WHERE d.profile_id = $1 AND d.id = $2`, profileID, id)
	if err != nil {
		return nil, fmt.Errorf("get delivery: %w", err)
	}
	out, err := scanDeliveryRows(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

// GetRowByID loads one delivery without profile scoping. Internal use only
// (webhook attempt processing); API paths must use GetByID.
func (r *DeliveryRepository) GetRowByID(ctx context.Context, id string) (*DeliveryRow, error) {
	rows, err := r.pool.Query(ctx, deliveryRowSelect+` WHERE d.id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("get delivery row: %w", err)
	}
	out, err := scanDeliveryRows(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

// RecentUnread returns the newest unread rows for the websocket snapshot.
func (r *DeliveryRepository) RecentUnread(ctx context.Context, profileID string, limit int) ([]DeliveryRow, error) {
	rows, err := r.pool.Query(ctx,
		deliveryRowSelect+`
		WHERE d.profile_id = $1 AND d.read_at IS NULL
		ORDER BY d.created_at DESC, d.id DESC
		LIMIT $2`,
		profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent unread: %w", err)
	}
	return scanDeliveryRows(rows)
}

// UnreadCount returns the unread badge count for the profile.
func (r *DeliveryRepository) UnreadCount(ctx context.Context, profileID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM notification_deliveries WHERE profile_id = $1 AND read_at IS NULL`,
		profileID,
	).Scan(&count)
	return count, err
}

// MarkRead marks one delivery read. Idempotent; reports whether the row
// transitioned from unread to read.
func (r *DeliveryRepository) MarkRead(ctx context.Context, profileID, id string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE notification_deliveries
		SET read_at = now()
		WHERE profile_id = $1 AND id = $2 AND read_at IS NULL`,
		profileID, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Exists reports whether a delivery belongs to the profile (used to make
// mark-read idempotent without leaking other profiles' IDs).
func (r *DeliveryRepository) Exists(ctx context.Context, profileID, id string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM notification_deliveries WHERE profile_id = $1 AND id = $2)`,
		profileID, id,
	).Scan(&exists)
	return exists, err
}

// MarkAllRead marks every unread delivery read for the profile.
func (r *DeliveryRepository) MarkAllRead(ctx context.Context, profileID string) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE notification_deliveries
		SET read_at = now()
		WHERE profile_id = $1 AND read_at IS NULL`,
		profileID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeleteAllForProfile removes every delivery for a deleted profile (profiles
// may live outside Postgres, so no cascade).
func (r *DeliveryRepository) DeleteAllForProfile(ctx context.Context, profileID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM notification_deliveries WHERE profile_id = $1`, profileID)
	return err
}

// DeleteOld applies retention: rows read longer ago than readCutoff, unread
// rows created before unreadCutoff. Read rows age from read_at, not
// created_at — an old notification read today starts a fresh read window.
func (r *DeliveryRepository) DeleteOld(ctx context.Context, readCutoff, unreadCutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM notification_deliveries
		WHERE (read_at IS NOT NULL AND read_at < $1)
		   OR (read_at IS NULL AND created_at < $2)`,
		readCutoff, unreadCutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
