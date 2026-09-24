package downloads

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const downloadColumns = `id, user_id, profile_id, device_id, media_file_id, content_id, episode_id, batch_id,
	kind, status, format, quality, effective_quality, target_bitrate_kbps, revision, artifact_id, file_size, bytes_sent, error_message,
	created_at, updated_at, completed_at, status_event_at`

const insertDownloadSQL = `INSERT INTO downloads (id, user_id, profile_id, device_id, media_file_id, content_id,
		episode_id, batch_id, kind, status, format, quality, effective_quality, target_bitrate_kbps, revision, artifact_id, file_size, bytes_sent, error_message,
		created_at, updated_at, completed_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)`

// Repository provides CRUD operations for the downloads table.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a new Repository backed by the given pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// managedQueryer lets subscription registration use its monitor transaction.
// Only registration reads/writes use this seam; transfer/quota lifecycle stays
// on the ordinary repository.
type managedQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type managedRegistryStore struct{ db managedQueryer }
type managedRegistrationRepository interface {
	MonitorEntriesToRegister(context.Context, *Subscription, []ManagedEntryKey) (map[ManagedEntryKey]bool, error)
	CreateManagedEntriesBatch(context.Context, []*Download) ([]*Download, error)
	SumManagedFileSize(context.Context, int, string, string) (int64, error)
}

// downloadQuotaLockClassID is the advisory-lock classid for per-user download
// quota serialization (arbitrary but stable; the second key is the user ID).
const downloadQuotaLockClassID = 0x646c6f61 // "dloa"

// WithUserQuotaLock runs fn while holding a cross-node advisory lock for the
// user, serializing download quota check + row creation. Without it the
// check-then-insert pair races: concurrent creates can all observe free quota
// before any of them inserts a row. The lock lives on a dedicated transaction
// used only as its holder — fn's own statements run through the pool and
// commit before the lock releases, so the next holder sees them.
func (r *Repository) WithUserQuotaLock(ctx context.Context, userID int, fn func(ctx context.Context) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin download quota lock: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, downloadQuotaLockClassID, userID); err != nil {
		return fmt.Errorf("acquiring download quota lock for user %d: %w", userID, err)
	}
	return fn(ctx)
}

// scanInto scans a single download row's columns (in downloadColumns order)
// into d, mapping nullable text columns to empty strings.
func scanInto(row pgx.Row, d *Download) error {
	var profileID, deviceID, episodeID, batchID, artifactID *string
	err := row.Scan(
		&d.ID, &d.UserID, &profileID, &deviceID, &d.MediaFileID, &d.ContentID, &episodeID, &batchID,
		&d.Kind, &d.Status, &d.Format, &d.Quality, &d.EffectiveQuality, &d.TargetBitrateKbps, &d.Revision, &artifactID, &d.FileSize, &d.BytesSent, &d.ErrorMessage,
		&d.CreatedAt, &d.UpdatedAt, &d.CompletedAt, &d.StatusEventAt,
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

func downloadInsertArgs(d *Download) []any {
	format := d.Format
	if format == "" {
		format = FormatOriginal
	}
	quality := d.Quality
	if quality == "" {
		quality = QualityOriginal
	}
	effectiveQuality := d.EffectiveQuality
	if effectiveQuality == "" {
		effectiveQuality = quality
	}
	revision := d.Revision
	if revision <= 0 {
		revision = 1
	}
	return []any{
		d.ID, d.UserID, nilIfEmpty(d.ProfileID), nilIfEmpty(d.DeviceID), d.MediaFileID, d.ContentID,
		nilIfEmpty(d.EpisodeID), nilIfEmpty(d.BatchID), d.Kind, d.Status, format, quality, effectiveQuality,
		d.TargetBitrateKbps, revision, nilIfEmpty(d.ArtifactID), d.FileSize, d.BytesSent, d.ErrorMessage,
		d.CreatedAt, d.UpdatedAt, d.CompletedAt,
	}
}

// Create inserts a new download record.
func (r *Repository) Create(ctx context.Context, d *Download) error {
	if _, err := r.pool.Exec(ctx, insertDownloadSQL, downloadInsertArgs(d)...); err != nil {
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
		if _, err := tx.Exec(ctx, insertDownloadSQL, downloadInsertArgs(d)...); err != nil {
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

// PruneEphemeralOlderThan deletes ephemeral (device-less) rows not touched
// since cutoff, regardless of status — they are convenience records for
// one-shot web downloads, not managed library entries. Pruning bounds
// GET /downloads growth for long-lived accounts and unpins artifacts
// (HasActiveLink counts ephemeral links). Returns the number of rows removed.
func (r *Repository) PruneEphemeralOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM downloads WHERE device_id IS NULL AND updated_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("pruning ephemeral downloads: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountActiveByUser returns the number of active downloads for a user.
// 'preparing' counts: an artifact-backed row holds encode work in flight, so
// it must consume the concurrent quota like a live transfer does.
func (r *Repository) CountActiveByUser(ctx context.Context, userID int) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM downloads WHERE user_id = $1 AND status IN ('queued', 'downloading', 'preparing')`,
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

// PurgeProfileDevices removes a profile's device rows (cascading its managed
// downloads and subscriptions via their composite FKs). Profiles may live
// outside Postgres, so no FK cascade covers user_devices on profile deletion;
// the profile-deletion handler calls this instead.
func (r *Repository) PurgeProfileDevices(ctx context.Context, userID int, profileID string) error {
	if profileID == "" {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`DELETE FROM user_devices WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID,
	)
	if err != nil {
		return fmt.Errorf("purging devices for profile %q: %w", profileID, err)
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

// CreateManagedEntry inserts d and returns it, or — if a concurrent insert won
// the race to the same managed identity — returns the existing winning row.
// Callers have already classified the item as new.
func (r *Repository) CreateManagedEntry(ctx context.Context, d *Download) (*Download, error) {
	if err := r.Create(ctx, d); err != nil {
		if existing, gerr := r.GetManagedEntry(ctx, d.UserID, d.ProfileID, d.DeviceID, d.ContentID, d.EpisodeID); gerr == nil {
			return existing, nil
		}
		return nil, err
	}
	return d, nil
}

// ManagedEntryKey is the (content, episode) identity of a managed entry within
// one device library. EpisodeID "" is the movie/no-episode slot.
type ManagedEntryKey struct {
	ContentID string
	EpisodeID string
}

// GetManagedEntriesByKeys returns the device's existing managed rows for the
// given identities in one query, keyed by ManagedEntryKey. The bulk
// registration paths use this instead of a per-item GetManagedEntry loop (a
// 300-episode series would otherwise issue 300 sequential lookups).
func (r *Repository) GetManagedEntriesByKeys(ctx context.Context, userID int, profileID, deviceID string, keys []ManagedEntryKey) (map[ManagedEntryKey]*Download, error) {
	return (managedRegistryStore{r.pool}).GetManagedEntriesByKeys(ctx, userID, profileID, deviceID, keys)
}

func (r managedRegistryStore) GetManagedEntriesByKeys(ctx context.Context, userID int, profileID, deviceID string, keys []ManagedEntryKey) (map[ManagedEntryKey]*Download, error) {
	out := make(map[ManagedEntryKey]*Download, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	contentIDs := make([]string, 0, len(keys))
	episodeIDs := make([]string, 0, len(keys))
	want := make(map[ManagedEntryKey]bool, len(keys))
	for _, k := range keys {
		contentIDs = append(contentIDs, k.ContentID)
		episodeIDs = append(episodeIDs, k.EpisodeID)
		want[k] = true
	}
	// ANY() on each column over-selects the cross product within this device's
	// rows; the exact-pair filter below trims it.
	rows, err := r.db.Query(ctx,
		`SELECT `+downloadColumns+` FROM downloads
		 WHERE user_id = $1 AND profile_id = $2 AND device_id = $3
		   AND content_id = ANY($4) AND COALESCE(episode_id, '') = ANY($5)`,
		userID, profileID, deviceID, contentIDs, episodeIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("listing managed entries by key: %w", err)
	}
	defer rows.Close()
	list, err := scanDownloads(rows)
	if err != nil {
		return nil, err
	}
	for _, d := range list {
		k := ManagedEntryKey{ContentID: d.ContentID, EpisodeID: d.EpisodeID}
		if want[k] {
			out[k] = d
		}
	}
	return out, nil
}

// MonitorEntriesToRegister returns which of keys the monitor may register on
// its device: the device holds no entry with that key, and the monitor has no
// exclusion for the episode (see Repository.DeleteManaged). One statement
// reads both, so a concurrent delete, which removes the row and records its
// exclusion in one commit, is seen as one or the other.
func (r managedRegistryStore) MonitorEntriesToRegister(ctx context.Context, sub *Subscription, keys []ManagedEntryKey) (map[ManagedEntryKey]bool, error) {
	out := make(map[ManagedEntryKey]bool, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	contentIDs := make([]string, len(keys))
	episodeIDs := make([]string, len(keys))
	for i, k := range keys {
		contentIDs[i], episodeIDs[i] = k.ContentID, k.EpisodeID
	}
	rows, err := r.db.Query(ctx,
		`SELECT k.content_id, k.episode_id
		 FROM unnest($4::text[], $5::text[]) AS k(content_id, episode_id)
		 WHERE NOT EXISTS (
		         SELECT 1 FROM downloads d
		         WHERE d.user_id = $1 AND d.profile_id = $2 AND d.device_id = $3
		           AND d.content_id = k.content_id AND COALESCE(d.episode_id, '') = k.episode_id)
		   AND NOT EXISTS (
		         SELECT 1 FROM download_subscription_exclusions x
		         WHERE x.subscription_id = $6 AND x.episode_id = k.episode_id)`,
		sub.UserID, sub.ProfileID, sub.DeviceID, contentIDs, episodeIDs, sub.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing monitor entries to register: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k ManagedEntryKey
		if err := rows.Scan(&k.ContentID, &k.EpisodeID); err != nil {
			return nil, fmt.Errorf("scanning monitor entry to register: %w", err)
		}
		out[k] = true
	}
	return out, rows.Err()
}

// CreateManagedEntriesBatch inserts managed rows in one statement, skipping
// identities that already exist (including concurrent-insert races) via ON
// CONFLICT DO NOTHING on the managed-entry unique index. Returns only the rows
// actually inserted, so callers can report an honest newly-registered count.
func (r *Repository) CreateManagedEntriesBatch(ctx context.Context, ds []*Download) ([]*Download, error) {
	return (managedRegistryStore{r.pool}).CreateManagedEntriesBatch(ctx, ds)
}

func (r managedRegistryStore) CreateManagedEntriesBatch(ctx context.Context, ds []*Download) ([]*Download, error) {
	if len(ds) == 0 {
		return nil, nil
	}
	const insertCols = 22
	var sb strings.Builder
	sb.WriteString(`INSERT INTO downloads (id, user_id, profile_id, device_id, media_file_id, content_id,
		episode_id, batch_id, kind, status, format, quality, effective_quality, target_bitrate_kbps, revision, artifact_id, file_size, bytes_sent, error_message,
		created_at, updated_at, completed_at) VALUES `)
	args := make([]any, 0, len(ds)*insertCols)
	for i, d := range ds {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteByte('(')
		for j := 0; j < insertCols; j++ {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('$')
			sb.WriteString(strconv.Itoa(i*insertCols + j + 1))
		}
		sb.WriteByte(')')
		args = append(args, downloadInsertArgs(d)...)
	}
	sb.WriteString(` ON CONFLICT (user_id, profile_id, device_id, content_id, (COALESCE(episode_id, ''))) WHERE device_id IS NOT NULL DO NOTHING RETURNING ` + downloadColumns)
	rows, err := r.db.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("batch inserting managed entries: %w", err)
	}
	defer rows.Close()
	return scanDownloads(rows)
}

// ReplaceManagedEntry updates an existing managed row to a new file/quality
// target, incrementing its revision so clients can replace stale local bytes.
func (r *Repository) ReplaceManagedEntry(ctx context.Context, existing *Download, replacement *Download) (*Download, error) {
	query := `UPDATE downloads SET
			media_file_id = $6,
			batch_id = $7,
			kind = $8,
			status = $9,
			format = $10,
			quality = $11,
			effective_quality = $12,
			target_bitrate_kbps = $13,
			artifact_id = $14,
			file_size = $15,
			bytes_sent = 0,
			error_message = '',
			completed_at = NULL,
			revision = revision + 1,
 status_event_at = NULL,
			updated_at = now()
		WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND device_id = $4 AND revision = $5
		RETURNING ` + downloadColumns
	row := r.pool.QueryRow(ctx, query,
		existing.ID, existing.UserID, existing.ProfileID, existing.DeviceID, existing.Revision,
		replacement.MediaFileID, nilIfEmpty(replacement.BatchID), replacement.Kind, replacement.Status,
		replacement.Format, replacement.Quality, replacement.EffectiveQuality, replacement.TargetBitrateKbps,
		nilIfEmpty(replacement.ArtifactID), replacement.FileSize,
	)
	d, err := scanDownload(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return r.GetManagedEntry(ctx, existing.UserID, existing.ProfileID, existing.DeviceID, existing.ContentID, existing.EpisodeID)
		}
		return nil, fmt.Errorf("replacing managed download: %w", err)
	}
	return d, nil
}

// UpdateManagedBatch moves an otherwise unchanged managed row into the latest
// batch without incrementing revision or resetting local completion state.
func (r *Repository) UpdateManagedBatch(ctx context.Context, existing *Download, batchID string) (*Download, error) {
	query := `UPDATE downloads SET
			batch_id = $6,
			updated_at = now()
		WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND device_id = $4 AND revision = $5
		RETURNING ` + downloadColumns
	row := r.pool.QueryRow(ctx, query,
		existing.ID, existing.UserID, existing.ProfileID, existing.DeviceID, existing.Revision,
		nilIfEmpty(batchID),
	)
	d, err := scanDownload(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return r.GetManagedEntry(ctx, existing.UserID, existing.ProfileID, existing.DeviceID, existing.ContentID, existing.EpisodeID)
		}
		return nil, fmt.Errorf("updating managed download batch: %w", err)
	}
	return d, nil
}

// SumManagedFileSize returns the total file_size of a device's managed entries
// that still count toward on-device storage (excluding revoked, failed, and
// canceled rows). It backs the auto-download storage soft-gate; it is the server's
// best-effort view, since the client is the source of truth for what is actually
// on disk.
func (r *Repository) SumManagedFileSize(ctx context.Context, userID int, profileID, deviceID string) (int64, error) {
	return (managedRegistryStore{r.pool}).SumManagedFileSize(ctx, userID, profileID, deviceID)
}

func (r managedRegistryStore) SumManagedFileSize(ctx context.Context, userID int, profileID, deviceID string) (int64, error) {
	var total int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(file_size), 0) FROM downloads
		 WHERE user_id = $1 AND profile_id = $2 AND device_id = $3
		   AND status NOT IN ('revoked', 'failed', 'cancelled')`,
		userID, profileID, deviceID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("summing managed download size: %w", err)
	}
	return total, nil
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

// ListManagedByBatch returns managed entries in a batch for one authorized
// device. It powers batch manifest fetches without revealing other devices' rows.
func (r *Repository) ListManagedByBatch(ctx context.Context, userID int, profileID, deviceID, batchID string) ([]*Download, error) {
	query := `SELECT ` + downloadColumns + ` FROM downloads
		WHERE user_id = $1 AND profile_id = $2 AND device_id = $3 AND batch_id = $4
		ORDER BY created_at ASC`
	rows, err := r.pool.Query(ctx, query, userID, profileID, deviceID, batchID)
	if err != nil {
		return nil, fmt.Errorf("listing managed batch downloads: %w", err)
	}
	defer rows.Close()
	result, err := scanDownloads(rows)
	if err != nil {
		return nil, fmt.Errorf("scanning managed batch downloads: %w", err)
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
// state), authorized on (user, profile, device). Only an artifact-ready entry
// may be moved into the client-driven serve lifecycle, so the transition is
// gated to source states ('ready','downloading','completed'); a 'preparing'
// (artifact not yet encoded), 'failed', or 'revoked' row is never patchable to
// downloading/completed. Returns ErrNotFound when nothing matches the gate.
func (r *Repository) UpdateManagedStatus(ctx context.Context, id string, userID int, profileID, deviceID, status string, completedAt *time.Time) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE downloads SET status = $5, completed_at = $6, status_event_at = now(), updated_at = now()
		WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND device_id = $4
		  AND status IN ('ready', 'downloading', 'completed')`,
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

// deleteManagedSQL is DeleteManaged's statement: it returns the number of rows
// deleted. Tests run it inside an open transaction to hold a delete between its
// statement and its commit.
const deleteManagedSQL = `WITH deleted AS (
	DELETE FROM downloads WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND device_id = $4
	RETURNING content_id, episode_id
), monitor AS (
	SELECT s.id, d.episode_id FROM deleted d
	JOIN download_subscriptions s
	  ON s.user_id = $2 AND s.profile_id = $3 AND s.device_id = $4 AND s.series_id = d.content_id
	WHERE d.episode_id IS NOT NULL
	FOR KEY SHARE OF s
), excluded AS (
	INSERT INTO download_subscription_exclusions (subscription_id, episode_id)
	SELECT id, episode_id FROM monitor
	ON CONFLICT DO NOTHING
)
SELECT count(*) FROM deleted`

// DeleteManaged removes a managed entry, authorized on (user, profile, device).
// Deleting an episode of a series this device monitors also records a monitor
// exclusion in the same statement, so later syncs do not register the episode
// again. The row and its exclusion commit together, so a sync, which reads
// both in one statement (MonitorEntriesToRegister), sees one or the other.
// The statement locks the monitor FOR KEY SHARE, waiting out a sync or edit
// that holds it, so a monitor deleted meanwhile yields no row and the delete
// records nothing instead of failing the exclusion's foreign key. It deletes
// the downloads row before locking the monitor. A device delete cascades in
// the same order when the downloads cascade trigger fires first; Postgres
// fires triggers in name order, and the cascade triggers' names end in their
// OIDs compared as text, so a database can get the opposite order. There a
// device delete racing this one can fail one of them with a deadlock error,
// which a retry resolves. Returns ErrNotFound when nothing matches.
func (r *Repository) DeleteManaged(ctx context.Context, id string, userID int, profileID, deviceID string) error {
	var deleted int
	err := r.pool.QueryRow(ctx, deleteManagedSQL, id, userID, profileID, deviceID).Scan(&deleted)
	if err != nil {
		return fmt.Errorf("deleting managed download: %w", err)
	}
	if deleted == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearMonitorExclusions forgets the device's monitor exclusions for episodes
// of seriesID that the user downloaded explicitly: an explicit download
// overrides the earlier delete. It runs after the download commits, so it
// forgets an episode only while the device still holds the episode's row. A
// delete that removed the row in between came after the download, so its
// exclusion stays. The row is locked FOR KEY SHARE SKIP LOCKED: a delete that
// has run its statement but not committed still holds the row, and a plain
// read would see the row and erase that delete's exclusion. The exclusion is
// locked FOR UPDATE SKIP LOCKED. The clear is best-effort cleanup after the
// download succeeded, so it never makes the download wait on another
// transaction's row lock, such as a delete of the row that is itself waiting
// on the monitor behind a sync. A skipped row or exclusion keeps the
// exclusion, the direction forgetMonitorDeletes already tolerates.
func (r *Repository) ClearMonitorExclusions(ctx context.Context, userID int, profileID, deviceID, seriesID string, episodeIDs []string) error {
	if profileID == "" || deviceID == "" || len(episodeIDs) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`WITH forgotten AS (
			SELECT x.subscription_id, x.episode_id
			FROM download_subscription_exclusions x
			JOIN download_subscriptions s ON s.id = x.subscription_id
			WHERE s.user_id = $1 AND s.profile_id = $2 AND s.device_id = $3 AND s.series_id = $4
			  AND x.episode_id = ANY($5)
			  AND EXISTS (
			        SELECT 1 FROM downloads d
			        WHERE d.user_id = $1 AND d.profile_id = $2 AND d.device_id = $3
			          AND d.content_id = $4 AND COALESCE(d.episode_id, '') = x.episode_id
			        FOR KEY SHARE SKIP LOCKED)
			FOR UPDATE OF x SKIP LOCKED
		)
		DELETE FROM download_subscription_exclusions x USING forgotten f
		WHERE x.subscription_id = f.subscription_id AND x.episode_id = f.episode_id`,
		userID, profileID, deviceID, seriesID, episodeIDs,
	)
	if err != nil {
		return fmt.Errorf("clearing monitor exclusions: %w", err)
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

// ReconcileLinkedDownloads repairs downloads stranded in 'preparing' against a
// terminal artifact state. It covers the crash window between an artifact's
// MarkReady and its MarkLinkedDownloadsReady (the two are not one transaction),
// and any preparing row whose artifact reached 'failed' without its links being
// flipped. Two set-based updates (no per-artifact N+1): preparing→ready for rows
// linked to a ready artifact (recording the artifact size), and preparing→failed
// for rows linked to a failed artifact. Returns the rows it changed so the
// caller can publish state events. Idempotent — only 'preparing' rows are
// touched, so re-running it is a no-op.
func (r *Repository) ReconcileLinkedDownloads(ctx context.Context) (ready []*Download, failed []*Download, err error) {
	readyRows, err := r.pool.Query(ctx,
		`UPDATE downloads SET status = 'ready',
		     file_size = COALESCE((SELECT a.file_size FROM download_artifacts a WHERE a.id = downloads.artifact_id), file_size),
		     updated_at = now()
		 WHERE status = 'preparing' AND artifact_id IS NOT NULL
		   AND artifact_id IN (SELECT id FROM download_artifacts WHERE status IN ('ready', 'tone_map_ready', 'audio_v2_ready'))
		 RETURNING `+downloadColumns,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("reconciling ready downloads: %w", err)
	}
	ready, err = scanDownloads(readyRows)
	readyRows.Close()
	if err != nil {
		return nil, nil, err
	}

	failedRows, err := r.pool.Query(ctx,
		`UPDATE downloads SET status = 'failed',
		     error_message = COALESCE((SELECT NULLIF(a.error_message, '') FROM download_artifacts a WHERE a.id = downloads.artifact_id), 'artifact encode failed'),
		     updated_at = now()
		 WHERE status = 'preparing' AND artifact_id IS NOT NULL
		   AND artifact_id IN (SELECT id FROM download_artifacts WHERE status = 'failed')
		 RETURNING `+downloadColumns,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("reconciling failed downloads: %w", err)
	}
	failed, err = scanDownloads(failedRows)
	failedRows.Close()
	if err != nil {
		return nil, nil, err
	}
	return ready, failed, nil
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
