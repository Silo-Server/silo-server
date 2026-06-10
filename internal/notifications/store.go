package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time check that *Repository satisfies Store.
var _ Store = (*Repository)(nil)

var ErrNotFound = errors.New("notification not found")

// Store is the data-access interface for the notifications subsystem.
type Store interface {
	Insert(ctx context.Context, n *Notification) (created bool, err error)
	List(ctx context.Context, f ListFilter) ([]*Notification, error)
	UnreadCount(ctx context.Context, userID int, profileID string, childSafe bool) (int, error)
	MarkRead(ctx context.Context, userID int, ids []int64) error
	MarkAllRead(ctx context.Context, userID int) error
	Dismiss(ctx context.Context, userID int, id int64) error
	Preferences(ctx context.Context, userID int) (map[Category]bool, error)
	SetPreference(ctx context.Context, userID int, c Category, enabled bool) error
	InsertAnnouncement(ctx context.Context, a *Announcement) error
	ListAnnouncements(ctx context.Context) ([]*Announcement, error)
	DeleteAnnouncement(ctx context.Context, id int64) error
	DismissUnreadByTypeRef(ctx context.Context, typ, dedupPrefix string) error
	PurgeOld(ctx context.Context, dismissedBefore, allBefore time.Time) (int64, error)
	AdminUserIDs(ctx context.Context) ([]int, error)
	UserIDsWithLibraryAccess(ctx context.Context, libraryID int) ([]int, error)
}

// Repository implements Store against a pgxpool.
type Repository struct{ pool *pgxpool.Pool }

// NewRepository returns a Repository backed by pool.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// nullableString converts an empty string to nil so it stores as SQL NULL.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Insert writes n to the notifications table.  If dedup_ref is non-empty and a
// row with the same (user_id, type, dedup_ref) already exists, the INSERT is
// silently dropped and (false, nil) is returned.
func (r *Repository) Insert(ctx context.Context, n *Notification) (bool, error) {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO notifications
			(user_id, profile_id, category, type, title, body, link, item_id,
			 source_event, dedup_ref, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (user_id, type, dedup_ref) WHERE dedup_ref IS NOT NULL
		DO NOTHING
		RETURNING id, created_at
	`,
		n.UserID,
		n.ProfileID,
		n.Category,
		n.Type,
		n.Title,
		n.Body,
		n.Link,
		n.ItemID,
		nullableString(n.SourceEvent),
		nullableString(n.DedupRef),
		n.ExpiresAt,
	).Scan(&n.ID, &n.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Dedup conflict: the row was not inserted.
			return false, nil
		}
		return false, fmt.Errorf("insert notification: %w", err)
	}
	return true, nil
}

// List returns non-dismissed notifications for the given filter.
func (r *Repository) List(ctx context.Context, f ListFilter) ([]*Notification, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
	}

	args := []any{f.UserID, f.ProfileID}
	sb := strings.Builder{}
	sb.WriteString(`
		SELECT id, user_id, profile_id, category, type, title, body, link, item_id,
		       source_event, dedup_ref, created_at, read_at, dismissed_at, expires_at
		FROM notifications
		WHERE user_id = $1
		  AND dismissed_at IS NULL
		  AND (profile_id IS NULL OR profile_id = $2)
		  AND (expires_at IS NULL OR expires_at > now())
	`)

	if f.ChildSafe {
		sb.WriteString(" AND category NOT IN ('request','system','admin')")
	}
	if f.UnreadOnly {
		sb.WriteString(" AND read_at IS NULL")
	}
	if f.Category != "" {
		args = append(args, f.Category)
		sb.WriteString(" AND category = $" + strconv.Itoa(len(args)))
	}
	if f.Cursor > 0 {
		args = append(args, f.Cursor)
		sb.WriteString(" AND id < $" + strconv.Itoa(len(args)))
	}
	args = append(args, limit)
	sb.WriteString(" ORDER BY id DESC LIMIT $" + strconv.Itoa(len(args)))

	rows, err := r.pool.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()

	var out []*Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("scan notification: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notifications: %w", err)
	}
	return out, nil
}

// scanNotification scans a notifications row (15 columns in declaration order).
func scanNotification(row interface{ Scan(dest ...any) error }) (*Notification, error) {
	var n Notification
	var sourceEvent, dedupRef *string
	if err := row.Scan(
		&n.ID,
		&n.UserID,
		&n.ProfileID,
		&n.Category,
		&n.Type,
		&n.Title,
		&n.Body,
		&n.Link,
		&n.ItemID,
		&sourceEvent,
		&dedupRef,
		&n.CreatedAt,
		&n.ReadAt,
		&n.DismissedAt,
		&n.ExpiresAt,
	); err != nil {
		return nil, err
	}
	if sourceEvent != nil {
		n.SourceEvent = *sourceEvent
	}
	if dedupRef != nil {
		n.DedupRef = *dedupRef
	}
	return &n, nil
}

// UnreadCount returns the number of unread, non-dismissed, non-expired
// notifications for the user.
func (r *Repository) UnreadCount(ctx context.Context, userID int, profileID string, childSafe bool) (int, error) {
	sb := strings.Builder{}
	args := []any{userID, profileID}
	sb.WriteString(`
		SELECT COUNT(*)
		FROM notifications
		WHERE user_id = $1
		  AND dismissed_at IS NULL
		  AND read_at IS NULL
		  AND (profile_id IS NULL OR profile_id = $2)
		  AND (expires_at IS NULL OR expires_at > now())
	`)
	if childSafe {
		sb.WriteString(" AND category NOT IN ('request','system','admin')")
	}
	var count int
	if err := r.pool.QueryRow(ctx, sb.String(), args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("unread notification count: %w", err)
	}
	return count, nil
}

// MarkRead sets read_at = now() for the given notification IDs belonging to userID.
func (r *Repository) MarkRead(ctx context.Context, userID int, ids []int64) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notifications
		SET read_at = now()
		WHERE user_id = $1
		  AND id = ANY($2)
		  AND read_at IS NULL
	`, userID, ids)
	if err != nil {
		return fmt.Errorf("mark notifications read: %w", err)
	}
	return nil
}

// MarkAllRead sets read_at = now() for all unread notifications belonging to userID.
func (r *Repository) MarkAllRead(ctx context.Context, userID int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notifications
		SET read_at = now()
		WHERE user_id = $1
		  AND read_at IS NULL
	`, userID)
	if err != nil {
		return fmt.Errorf("mark all notifications read: %w", err)
	}
	return nil
}

// Dismiss sets dismissed_at = now() for the notification. Returns ErrNotFound if
// the notification does not exist or is already dismissed.
func (r *Repository) Dismiss(ctx context.Context, userID int, id int64) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE notifications
		SET dismissed_at = now()
		WHERE user_id = $1
		  AND id = $2
		  AND dismissed_at IS NULL
	`, userID, id)
	if err != nil {
		return fmt.Errorf("dismiss notification: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Preferences returns a map of Category → enabled for the user.
func (r *Repository) Preferences(ctx context.Context, userID int) (map[Category]bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT category, enabled
		FROM notification_preferences
		WHERE user_id = $1
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("get notification preferences: %w", err)
	}
	defer rows.Close()

	out := make(map[Category]bool)
	for rows.Next() {
		var cat Category
		var enabled bool
		if err := rows.Scan(&cat, &enabled); err != nil {
			return nil, fmt.Errorf("scan notification preference: %w", err)
		}
		out[cat] = enabled
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notification preferences: %w", err)
	}
	return out, nil
}

// SetPreference upserts a notification preference for the user.
func (r *Repository) SetPreference(ctx context.Context, userID int, c Category, enabled bool) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_preferences (user_id, category, enabled)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, category) DO UPDATE SET enabled = EXCLUDED.enabled
	`, userID, c, enabled)
	if err != nil {
		return fmt.Errorf("set notification preference: %w", err)
	}
	return nil
}

// InsertAnnouncement writes a to the announcements table, populating ID and CreatedAt.
func (r *Repository) InsertAnnouncement(ctx context.Context, a *Announcement) error {
	audienceJSON, err := json.Marshal(a.Audience)
	if err != nil {
		return fmt.Errorf("marshal announcement audience: %w", err)
	}
	err = r.pool.QueryRow(ctx, `
		INSERT INTO announcements (title, body, audience, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at
	`, a.Title, a.Body, audienceJSON, a.CreatedBy, a.ExpiresAt).Scan(&a.ID, &a.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert announcement: %w", err)
	}
	return nil
}

// ListAnnouncements returns all announcements ordered newest-first.
func (r *Repository) ListAnnouncements(ctx context.Context) ([]*Announcement, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, title, body, audience, created_by, created_at, expires_at
		FROM announcements
		ORDER BY id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list announcements: %w", err)
	}
	defer rows.Close()

	var out []*Announcement
	for rows.Next() {
		var a Announcement
		var audienceJSON []byte
		if err := rows.Scan(
			&a.ID,
			&a.Title,
			&a.Body,
			&audienceJSON,
			&a.CreatedBy,
			&a.CreatedAt,
			&a.ExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan announcement: %w", err)
		}
		if err := json.Unmarshal(audienceJSON, &a.Audience); err != nil {
			return nil, fmt.Errorf("unmarshal announcement audience: %w", err)
		}
		out = append(out, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate announcements: %w", err)
	}
	return out, nil
}

// DeleteAnnouncement removes the announcement by ID. Returns ErrNotFound if it
// does not exist.
func (r *Repository) DeleteAnnouncement(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM announcements WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete announcement: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DismissUnreadByTypeRef bulk-dismisses unread notifications whose type matches
// typ and whose dedup_ref starts with dedupPrefix.
func (r *Repository) DismissUnreadByTypeRef(ctx context.Context, typ, dedupPrefix string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notifications
		SET dismissed_at = now()
		WHERE type = $1
		  AND dedup_ref LIKE $2 || '%'
		  AND read_at IS NULL
		  AND dismissed_at IS NULL
	`, typ, dedupPrefix)
	if err != nil {
		return fmt.Errorf("dismiss notifications by type ref: %w", err)
	}
	return nil
}

// PurgeOld deletes old notifications according to three criteria and returns
// the number of rows deleted.
func (r *Repository) PurgeOld(ctx context.Context, dismissedBefore, allBefore time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM notifications
		WHERE (dismissed_at IS NOT NULL AND dismissed_at < $1)
		   OR created_at < $2
		   OR (expires_at IS NOT NULL AND expires_at < now() AND read_at IS NULL)
	`, dismissedBefore, allBefore)
	if err != nil {
		return 0, fmt.Errorf("purge old notifications: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AdminUserIDs returns the IDs of all enabled admin users.
func (r *Repository) AdminUserIDs(ctx context.Context) ([]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id FROM users WHERE role = 'admin' AND enabled = true
	`)
	if err != nil {
		return nil, fmt.Errorf("get admin user ids: %w", err)
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan admin user id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin user ids: %w", err)
	}
	return out, nil
}

// UserIDsWithLibraryAccess returns the IDs of enabled users who either have no
// library restriction or have libraryID in their library_ids array.
func (r *Repository) UserIDsWithLibraryAccess(ctx context.Context, libraryID int) ([]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id FROM users
		WHERE enabled = true
		  AND (library_ids IS NULL OR $1 = ANY(library_ids))
	`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("get user ids with library access: %w", err)
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan user id with library access: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user ids with library access: %w", err)
	}
	return out, nil
}
