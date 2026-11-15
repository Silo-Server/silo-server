package downloads

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// downloadInsertColumns are the columns every download insert writes, in
// downloadInsertArgs order.
const downloadInsertColumns = `id, user_id, profile_id, device_id, media_file_id, content_id,
		episode_id, batch_id, kind, status, format, quality, effective_quality, target_bitrate_kbps, revision, artifact_id, file_size, bytes_sent, error_message,
		created_at, updated_at, completed_at`

// downloadInsertTypes are the SQL types of downloadInsertColumns. An insert
// reads its values through a SELECT, which does not take parameter types from
// the target columns as VALUES does, so each placeholder is cast.
var downloadInsertTypes = strings.Fields(`text int text text int text
	text text text text text text text int int text bigint bigint text
	timestamptz timestamptz timestamptz`)

// registrableFile is the condition, on the media_files row mf, under which a
// download of (content, episode) may name it: the file is present and still
// linked to that title. The writes lock mf FOR SHARE in the statement that
// writes the download, so a retitle (which takes the row FOR UPDATE before it
// moves the row's downloads, see scanner.FileRepository.RetitleMovedFile)
// either waits for the download to commit and then moves it with the old
// title, or commits first, and the write, rechecking this condition on the
// row it waited for, writes nothing.
func registrableFile(contentID, episodeID string) string {
	return `mf.missing_since IS NULL AND mf.content_id = ` + contentID + ` AND mf.episode_id IS NOT DISTINCT FROM ` + episodeID
}

// errFileNotRegistrable is a download write refused by registrableFile. It
// is the not-found answer a request for a missing file gets.
var errFileNotRegistrable = fmt.Errorf("%w: the file is missing or no longer linked to the download's title", catalog.ErrItemNotFound)

// downloadValues returns the cast placeholders for one row of
// downloadInsertColumns, numbered from offset+1.
func downloadValues(offset int) string {
	var sb strings.Builder
	for j, typ := range downloadInsertTypes {
		if j > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("$" + strconv.Itoa(offset+j+1) + "::" + typ)
	}
	return sb.String()
}

// insertDownloadSQL inserts one download from downloadInsertArgs while its
// file passes registrableFile; it inserts nothing otherwise.
var insertDownloadSQL = `INSERT INTO downloads (` + downloadInsertColumns + `)
	SELECT ` + downloadValues(0) + `
	FROM media_files mf
	WHERE mf.id = $5::int AND ` + registrableFile("$6::text", "$7::text") + `
	FOR SHARE OF mf`

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

// Create inserts a new download record. It returns errFileNotRegistrable,
// and inserts nothing, unless the file is present and linked to the
// download's title (see registrableFile).
func (r *Repository) Create(ctx context.Context, d *Download) error {
	tag, err := r.pool.Exec(ctx, insertDownloadSQL, downloadInsertArgs(d)...)
	if err != nil {
		return fmt.Errorf("inserting download: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errFileNotRegistrable
	}
	return nil
}

// CreateBatch inserts multiple download records in a single transaction. A
// download whose file fails registrableFile fails the batch with
// errFileNotRegistrable.
func (r *Repository) CreateBatch(ctx context.Context, downloads []*Download) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning batch insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, d := range downloads {
		tag, err := tx.Exec(ctx, insertDownloadSQL, downloadInsertArgs(d)...)
		if err != nil {
			return fmt.Errorf("inserting batch download: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return errFileNotRegistrable
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing batch insert: %w", err)
	}
	return nil
}

// CreateManagedEntriesBatch inserts managed rows in one statement, skipping
// identities that already exist (including concurrent-insert races) via ON
// CONFLICT DO NOTHING on the managed-entry unique index, and rows whose file
// fails registrableFile. Returns only the rows actually inserted, so callers
// can report an honest newly-registered count. A monitor sync relies on the
// skip: an episode whose file left its title registers on a later sync, as
// one with no file does.
func (r *Repository) CreateManagedEntriesBatch(ctx context.Context, ds []*Download) ([]*Download, error) {
	return (managedRegistryStore{r.pool}).CreateManagedEntriesBatch(ctx, ds)
}

// CreateManagedEntriesAllOrNothing is CreateManagedEntriesBatch for a request
// that registers its entries together: when any row's file fails
// registrableFile, the statement inserts none of them. Conflicts still skip
// only their own rows.
func (r *Repository) CreateManagedEntriesAllOrNothing(ctx context.Context, ds []*Download) ([]*Download, error) {
	return (managedRegistryStore{r.pool}).createManagedEntries(ctx, ds, true)
}

func (r managedRegistryStore) CreateManagedEntriesBatch(ctx context.Context, ds []*Download) ([]*Download, error) {
	return r.createManagedEntries(ctx, ds, false)
}

func (r managedRegistryStore) createManagedEntries(ctx context.Context, ds []*Download, allOrNothing bool) ([]*Download, error) {
	if len(ds) == 0 {
		return nil, nil
	}
	var values strings.Builder
	args := make([]any, 0, len(ds)*len(downloadInsertTypes))
	for i, d := range ds {
		if i > 0 {
			values.WriteString(", ")
		}
		values.WriteString("(" + downloadValues(i*len(downloadInsertTypes)) + ")")
		args = append(args, downloadInsertArgs(d)...)
	}
	registrable := `SELECT v.* FROM (VALUES ` + values.String() + `) AS v(` + downloadInsertColumns + `)
		JOIN media_files mf ON mf.id = v.media_file_id AND ` + registrableFile("v.content_id", "v.episode_id") + `
		FOR SHARE OF mf`
	query := `INSERT INTO downloads (` + downloadInsertColumns + `)
		` + registrable
	if allOrNothing {
		// The CTE locks and checks every row's file once; the insert reads
		// rows only when none was refused.
		query = `WITH registrable AS (` + registrable + `)
		INSERT INTO downloads (` + downloadInsertColumns + `)
		SELECT * FROM registrable WHERE (SELECT count(*) FROM registrable) = ` + strconv.Itoa(len(ds))
	}
	query += `
		ON CONFLICT (user_id, profile_id, device_id, content_id, (COALESCE(episode_id, ''))) WHERE device_id IS NOT NULL DO NOTHING RETURNING ` + downloadColumns
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("batch inserting managed entries: %w", err)
	}
	defer rows.Close()
	return scanDownloads(rows)
}

// ReplaceManagedEntry updates an existing managed row to a new file/quality
// target, incrementing its revision so clients can replace stale local bytes.
// It returns errFileNotRegistrable, and changes nothing, unless the new file
// is present and linked to the entry's title (see registrableFile).
func (r *Repository) ReplaceManagedEntry(ctx context.Context, existing *Download, replacement *Download) (*Download, error) {
	query := `WITH mf AS (
			SELECT id, missing_since, content_id, episode_id FROM media_files
			WHERE id = $6 FOR SHARE
		)
		UPDATE downloads SET
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
			revoked_at = NULL, revoked_by = NULL, revoked_reason = '',
			updated_at = now()
		WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND device_id = $4 AND revision = $5
		  AND content_id = $16 AND episode_id IS NOT DISTINCT FROM $17
		  AND EXISTS (SELECT 1 FROM mf WHERE ` + registrableFile("$16", "$17") + `)
		RETURNING ` + downloadColumns
	row := r.pool.QueryRow(ctx, query,
		existing.ID, existing.UserID, existing.ProfileID, existing.DeviceID, existing.Revision,
		replacement.MediaFileID, nilIfEmpty(replacement.BatchID), replacement.Kind, replacement.Status,
		replacement.Format, replacement.Quality, replacement.EffectiveQuality, replacement.TargetBitrateKbps,
		nilIfEmpty(replacement.ArtifactID), replacement.FileSize, existing.ContentID, nilIfEmpty(existing.EpisodeID),
	)
	d, err := scanDownload(row)
	if err == nil {
		return d, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("replacing managed download: %w", err)
	}
	// Tell a refused file from a lost revision race. A file that changes
	// again between the two reads is classified by its later state; either
	// answer leaves the entry as it was.
	var registrable bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM media_files mf WHERE mf.id = $1 AND `+registrableFile("$2", "$3")+`)`,
		replacement.MediaFileID, existing.ContentID, nilIfEmpty(existing.EpisodeID)).Scan(&registrable); err != nil {
		return nil, fmt.Errorf("checking replacement file: %w", err)
	}
	if !registrable {
		return nil, errFileNotRegistrable
	}
	return r.GetManagedEntry(ctx, existing.UserID, existing.ProfileID, existing.DeviceID, existing.ContentID, existing.EpisodeID)
}
