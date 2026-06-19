package downloads

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const downloadColumns = `id, user_id, profile_id, device_id, media_file_id, content_id, episode_id, batch_id,
	kind, status, format, artifact_id, file_size, bytes_sent, error_message,
	created_at, updated_at, completed_at`

const insertDownloadSQL = `INSERT INTO downloads (id, user_id, profile_id, device_id, media_file_id, content_id,
		episode_id, batch_id, kind, status, format, artifact_id, file_size, bytes_sent, error_message,
		created_at, updated_at, completed_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`

// Repository provides CRUD operations for the downloads table.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a new Repository backed by the given pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// scanInto scans a single download row's columns (in downloadColumns order)
// into d, mapping nullable text columns to empty strings.
func scanInto(row pgx.Row, d *Download) error {
	var profileID, deviceID, episodeID, batchID, artifactID *string
	err := row.Scan(
		&d.ID, &d.UserID, &profileID, &deviceID, &d.MediaFileID, &d.ContentID, &episodeID, &batchID,
		&d.Kind, &d.Status, &d.Format, &artifactID, &d.FileSize, &d.BytesSent, &d.ErrorMessage,
		&d.CreatedAt, &d.UpdatedAt, &d.CompletedAt,
	)
	if err != nil {
		return err
	}
	d.ProfileID = deref(profileID)
	d.DeviceID = deref(deviceID)
	d.EpisodeID = deref(episodeID)
	d.BatchID = deref(batchID)
	d.ArtifactID = deref(artifactID)
	return nil
}

func scanDownload(row pgx.Row) (*Download, error) {
	var d Download
	if err := scanInto(row, &d); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning download: %w", err)
	}
	return &d, nil
}

func scanDownloads(rows pgx.Rows) ([]*Download, error) {
	var downloads []*Download
	for rows.Next() {
		var d Download
		if err := scanInto(rows, &d); err != nil {
			return nil, fmt.Errorf("scanning download row: %w", err)
		}
		downloads = append(downloads, &d)
	}
	return downloads, rows.Err()
}

func (r *Repository) insertArgs(d *Download) []any {
	return []any{
		d.ID, d.UserID, nilIfEmpty(d.ProfileID), nilIfEmpty(d.DeviceID), d.MediaFileID, d.ContentID,
		nilIfEmpty(d.EpisodeID), nilIfEmpty(d.BatchID), d.Kind, d.Status, d.Format, nilIfEmpty(d.ArtifactID),
		d.FileSize, d.BytesSent, d.ErrorMessage, d.CreatedAt, d.UpdatedAt, d.CompletedAt,
	}
}

// Create inserts a new download record.
func (r *Repository) Create(ctx context.Context, d *Download) error {
	if _, err := r.pool.Exec(ctx, insertDownloadSQL, r.insertArgs(d)...); err != nil {
		return fmt.Errorf("inserting download: %w", err)
	}
	return nil
}

// CreateBatch inserts multiple download records in a single transaction.
func (r *Repository) CreateBatch(ctx context.Context, downloads []*Download) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning batch insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, d := range downloads {
		if _, err := tx.Exec(ctx, insertDownloadSQL, r.insertArgs(d)...); err != nil {
			return fmt.Errorf("inserting batch download: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing batch insert: %w", err)
	}
	return nil
}

// GetByID retrieves a download by its ID.
func (r *Repository) GetByID(ctx context.Context, id string) (*Download, error) {
	query := `SELECT ` + downloadColumns + ` FROM downloads WHERE id = $1`
	return scanDownload(r.pool.QueryRow(ctx, query, id))
}

// ListByUser returns all downloads for a user, most recent first.
func (r *Repository) ListByUser(ctx context.Context, userID int) ([]*Download, error) {
	query := `SELECT ` + downloadColumns + ` FROM downloads
		WHERE user_id = $1
		ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("listing downloads for user: %w", err)
	}
	defer rows.Close()
	result, err := scanDownloads(rows)
	if err != nil {
		return nil, fmt.Errorf("scanning download rows: %w", err)
	}
	return result, nil
}

// CountActiveByUser returns the number of active (queued or downloading) downloads for a user.
func (r *Repository) CountActiveByUser(ctx context.Context, userID int) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM downloads WHERE user_id = $1 AND status IN ('queued', 'downloading')`,
		userID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting active downloads: %w", err)
	}
	return count, nil
}

// CountByUserSince returns the number of successful downloads created since the given time.
// Canceled and failed downloads are excluded so transient failures don't consume quota.
func (r *Repository) CountByUserSince(ctx context.Context, userID int, since time.Time) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM downloads WHERE user_id = $1 AND created_at >= $2 AND status NOT IN ('cancelled', 'failed')`,
		userID, since,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting downloads in period: %w", err)
	}
	return count, nil
}

// UpdateStatus sets the status and optionally the bytes_sent and completed_at fields.
func (r *Repository) UpdateStatus(ctx context.Context, id, status string, bytesSent int64, completedAt *time.Time) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE downloads SET status = $1, bytes_sent = $2, completed_at = $3, updated_at = now() WHERE id = $4`,
		status, bytesSent, completedAt, id,
	)
	if err != nil {
		return fmt.Errorf("updating download status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// TransitionStatus atomically transitions a download from expectedStatus to newStatus.
// Returns ErrStatusConflict if the row is not in expectedStatus (another request
// already transitioned it).
func (r *Repository) TransitionStatus(ctx context.Context, id, expectedStatus, newStatus string, bytesSent int64, completedAt *time.Time) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE downloads SET status = $1, bytes_sent = $2, completed_at = $3, updated_at = now()
		WHERE id = $4 AND status = $5`,
		newStatus, bytesSent, completedAt, id, expectedStatus,
	)
	if err != nil {
		return fmt.Errorf("transitioning download status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStatusConflict
	}
	return nil
}

// Delete removes a download record. Returns ErrNotFound if the row doesn't exist
// or doesn't belong to the given user.
func (r *Repository) Delete(ctx context.Context, id string, userID int) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM downloads WHERE id = $1 AND user_id = $2`, id, userID,
	)
	if err != nil {
		return fmt.Errorf("deleting download: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CancelByID sets a download to canceled if it is still queued or downloading.
func (r *Repository) CancelByID(ctx context.Context, id string, userID int) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE downloads SET status = 'cancelled', updated_at = now()
		WHERE id = $1 AND user_id = $2 AND status IN ('queued', 'downloading')`,
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("canceling download: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EnsureDevice upserts a row in public.user_devices so a managed download's
// composite FK (user_id, profile_id, device_id) is satisfied even when the
// device's first request is the download itself. Mirrors the per-user store's
// RegisterDevice upsert; a no-op when profile or device is empty.
func (r *Repository) EnsureDevice(ctx context.Context, userID int, profileID, deviceID, deviceName, devicePlatform string) error {
	if profileID == "" || deviceID == "" {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO user_devices (user_id, profile_id, device_id, device_name, device_platform, last_seen_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (user_id, profile_id, device_id) DO UPDATE SET
			device_name = CASE WHEN excluded.device_name <> '' THEN excluded.device_name ELSE user_devices.device_name END,
			device_platform = CASE WHEN excluded.device_platform <> '' THEN excluded.device_platform ELSE user_devices.device_platform END,
			last_seen_at = now()`,
		userID, profileID, deviceID, deviceName, devicePlatform,
	)
	if err != nil {
		return fmt.Errorf("ensuring device %q: %w", deviceID, err)
	}
	return nil
}

// GetManagedEntry returns the managed download uniquely identified by
// (user, profile, device, content, episode), or ErrNotFound. episodeID "" maps
// to the movie/no-episode slot via COALESCE(episode_id,”).
func (r *Repository) GetManagedEntry(ctx context.Context, userID int, profileID, deviceID, contentID, episodeID string) (*Download, error) {
	query := `SELECT ` + downloadColumns + ` FROM downloads
		WHERE user_id = $1 AND profile_id = $2 AND device_id = $3
		  AND content_id = $4 AND COALESCE(episode_id, '') = $5
		LIMIT 1`
	return scanDownload(r.pool.QueryRow(ctx, query, userID, profileID, deviceID, contentID, episodeID))
}

// GetManagedByID returns a managed download by id, authorized on
// (user_id, profile_id, device_id). A mismatch yields ErrNotFound so the
// endpoint never reveals the existence of another profile's/device's row.
func (r *Repository) GetManagedByID(ctx context.Context, id string, userID int, profileID, deviceID string) (*Download, error) {
	query := `SELECT ` + downloadColumns + ` FROM downloads
		WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND device_id = $4`
	return scanDownload(r.pool.QueryRow(ctx, query, id, userID, profileID, deviceID))
}

// ListManaged returns the managed entries for one device, most recent first.
func (r *Repository) ListManaged(ctx context.Context, userID int, profileID, deviceID string) ([]*Download, error) {
	query := `SELECT ` + downloadColumns + ` FROM downloads
		WHERE user_id = $1 AND profile_id = $2 AND device_id = $3
		ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, query, userID, profileID, deviceID)
	if err != nil {
		return nil, fmt.Errorf("listing managed downloads: %w", err)
	}
	defer rows.Close()
	result, err := scanDownloads(rows)
	if err != nil {
		return nil, fmt.Errorf("scanning managed download rows: %w", err)
	}
	return result, nil
}

// ListEphemeral returns the account-level (device_id IS NULL) rows for a user.
func (r *Repository) ListEphemeral(ctx context.Context, userID int) ([]*Download, error) {
	query := `SELECT ` + downloadColumns + ` FROM downloads
		WHERE user_id = $1 AND device_id IS NULL
		ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("listing ephemeral downloads: %w", err)
	}
	defer rows.Close()
	result, err := scanDownloads(rows)
	if err != nil {
		return nil, fmt.Errorf("scanning ephemeral download rows: %w", err)
	}
	return result, nil
}

// UpdateManagedStatus sets a managed entry's status (client confirming local
// state), authorized on (user, profile, device). A revoked entry cannot be
// transitioned out of revoked. Returns ErrNotFound when nothing matches.
func (r *Repository) UpdateManagedStatus(ctx context.Context, id string, userID int, profileID, deviceID, status string, completedAt *time.Time) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE downloads SET status = $5, completed_at = $6, updated_at = now()
		WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND device_id = $4 AND status <> 'revoked'`,
		id, userID, profileID, deviceID, status, completedAt,
	)
	if err != nil {
		return fmt.Errorf("updating managed download status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteManaged removes a managed entry, authorized on (user, profile, device).
// Returns ErrNotFound when nothing matches.
func (r *Repository) DeleteManaged(ctx context.Context, id string, userID int, profileID, deviceID string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM downloads WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND device_id = $4`,
		id, userID, profileID, deviceID,
	)
	if err != nil {
		return fmt.Errorf("deleting managed download: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkLinkedDownloadsReady flips every preparing download linked to a now-ready
// artifact to ready (recording the real size), returning the affected rows for
// client notification.
func (r *Repository) MarkLinkedDownloadsReady(ctx context.Context, artifactID string, fileSize int64) ([]*Download, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE downloads SET status = 'ready', file_size = $2, updated_at = now()
		 WHERE artifact_id = $1 AND status = 'preparing'
		 RETURNING `+downloadColumns,
		artifactID, fileSize,
	)
	if err != nil {
		return nil, fmt.Errorf("flipping linked downloads ready: %w", err)
	}
	defer rows.Close()
	return scanDownloads(rows)
}

// MarkLinkedDownloadsFailed flips every preparing download linked to a failed
// artifact to failed, returning the affected rows for client notification.
func (r *Repository) MarkLinkedDownloadsFailed(ctx context.Context, artifactID, errMsg string) ([]*Download, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE downloads SET status = 'failed', error_message = $2, updated_at = now()
		 WHERE artifact_id = $1 AND status = 'preparing'
		 RETURNING `+downloadColumns,
		artifactID, errMsg,
	)
	if err != nil {
		return nil, fmt.Errorf("flipping linked downloads failed: %w", err)
	}
	defer rows.Close()
	return scanDownloads(rows)
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
