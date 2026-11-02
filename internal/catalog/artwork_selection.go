package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultArtworkGCGracePeriod = 24 * time.Hour
	artworkTargetSeason         = "season"
	artworkTargetEpisode        = "episode"
	artworkImagePoster          = "poster"
	artworkImageBackdrop        = "backdrop"
	artworkImageLogo            = "logo"
	artworkImageStill           = "still"
	artworkPosterPathColumn     = "poster_path"
	artworkBackdropPathColumn   = "backdrop_path"
	artworkLogoPathColumn       = "logo_path"
	artworkPosterSourceColumn   = "poster_source_path"
	artworkBackdropSourceColumn = "backdrop_source_path"
	artworkLogoSourceColumn     = "logo_source_path"
	artworkPosterThumbColumn    = "poster_thumbhash"
	artworkBackdropThumbColumn  = "backdrop_thumbhash"
)

// ErrUnsupportedArtworkSelection reports a target/image-type combination that
// has no artwork column. Handlers should reject these before caching anything.
var ErrUnsupportedArtworkSelection = errors.New("catalog: unsupported artwork selection")

// ArtworkRevisionTracker registers every immutable artwork upload before its
// objects are written, allowing the collector to reclaim uploads that are
// never published. Confirmed live revisions remain dormant in the registry so
// database triggers can later reactivate them.
type ArtworkRevisionTracker struct {
	pool        *pgxpool.Pool
	gracePeriod time.Duration
}

func NewArtworkRevisionTracker(pool *pgxpool.Pool) *ArtworkRevisionTracker {
	if pool == nil {
		return nil
	}
	return &ArtworkRevisionTracker{pool: pool, gracePeriod: defaultArtworkGCGracePeriod}
}

// TrackArtworkRevision records an immutable revision's object manifest. Image
// caching first supplies an empty manifest before uploading, so it serializes
// with a collector that may already be deleting an older, unreferenced copy;
// the image type lets GC expand that pending manifest if an upload only partly
// succeeds. The cacher replaces it with exact keys only after every upload
// succeeds. Revisions parked as referenced stay dormant: a re-cache of live
// artwork is not garbage, and displacement triggers re-arm the row if the
// reference later moves away.
func (t *ArtworkRevisionTracker) TrackArtworkRevision(ctx context.Context, originalPath, imageType string, objectKeys []string) error {
	if t == nil || t.pool == nil {
		return fmt.Errorf("catalog: artwork revision tracking is not configured")
	}
	originalPath = strings.TrimSpace(originalPath)
	imageType = strings.ToLower(strings.TrimSpace(imageType))
	keys := compactArtworkObjectKeys(objectKeys)
	if originalPath == "" || strings.Contains(originalPath, "://") || (len(keys) == 0 && imageType == "") {
		return nil
	}
	notBefore := time.Now().Add(t.gracePeriod)
	// deleted_at is cleared because this upsert precedes a re-upload of the
	// exact manifest: the objects exist again once the cacher finishes.
	// Exact keys arrive only after every upload succeeded, so they make the
	// previous delivery verdict stale. It loses its authority but keeps its
	// keys, which still prove which variants delivered before.
	_, err := t.pool.Exec(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, image_type, object_keys, published_keys, not_before, next_attempt_at
		) VALUES ($1, $2, $3, $3, $4, $4)
		ON CONFLICT (original_path) DO UPDATE SET
			object_keys = EXCLUDED.object_keys,
            published_keys = CASE
                WHEN cardinality(EXCLUDED.object_keys) > 0 THEN EXCLUDED.object_keys
                WHEN artwork_revision_gc_candidates.deleted_at IS NOT NULL THEN '{}'::text[]
                ELSE coalesce(artwork_revision_gc_candidates.published_keys, '{}'::text[]) END,
            delivery_next_check = NOW(),
            delivery_lease = '',
            delivery_keys = CASE WHEN artwork_revision_gc_candidates.deleted_at IS NOT NULL
                THEN '{}'::text[] ELSE artwork_revision_gc_candidates.delivery_keys END,
            delivery_checked_at = CASE WHEN artwork_revision_gc_candidates.deleted_at IS NOT NULL
                    OR cardinality(EXCLUDED.object_keys) > 0
                THEN NULL ELSE artwork_revision_gc_candidates.delivery_checked_at END,
            delivery_failures = CASE WHEN artwork_revision_gc_candidates.deleted_at IS NOT NULL
                    OR cardinality(EXCLUDED.object_keys) > 0
                THEN 0 ELSE artwork_revision_gc_candidates.delivery_failures END,
			image_type = CASE
				WHEN artwork_revision_gc_candidates.image_type = '' THEN EXCLUDED.image_type
				ELSE artwork_revision_gc_candidates.image_type
			END,
			not_before = CASE
				WHEN artwork_revision_gc_candidates.next_attempt_at IS NULL THEN artwork_revision_gc_candidates.not_before
				ELSE EXCLUDED.not_before
			END,
			next_attempt_at = CASE
				WHEN artwork_revision_gc_candidates.next_attempt_at IS NULL THEN NULL
				ELSE EXCLUDED.next_attempt_at
			END,
			deleted_at = NULL,
			attempt_count = 0,
			locked_at = NULL,
			locked_by = '',
			last_error = '',
			updated_at = NOW()`, originalPath, imageType, keys, notBefore)
	if err != nil {
		return fmt.Errorf("catalog: track artwork revision: %w", err)
	}
	return nil
}

// ArtworkSelection describes a manually selected, already-cached artwork
// revision. PublishArtworkSelection makes the database pointer and image lock
// visible atomically, then schedules the displaced revision for delayed cleanup.
type ArtworkSelection struct {
	TargetType      string
	TargetContentID string
	ParentContentID string
	ImageType       string
	StoredPath      string
	SourcePath      string
	Thumbhash       string
	LockField       int
}

// ValidateArtworkSelectionTarget reports whether a target/image-type pair maps
// to a real artwork column, so handlers can reject unsupported requests before
// downloading or uploading anything.
func ValidateArtworkSelectionTarget(targetType, imageType string) error {
	_, _, _, _, _, err := artworkTargetColumns(
		strings.ToLower(strings.TrimSpace(targetType)),
		strings.ToLower(strings.TrimSpace(imageType)),
	)
	return err
}

// PublishArtworkSelection atomically changes the selected artwork, records its
// source, locks automatic image refreshes, and queues the old immutable revision
// for reference-aware garbage collection after a grace period.
func (s *DetailService) PublishArtworkSelection(ctx context.Context, selection ArtworkSelection) error {
	if s == nil || s.itemRepo == nil || s.itemRepo.pool == nil {
		return fmt.Errorf("catalog: artwork persistence is not configured")
	}
	selection.TargetType = strings.ToLower(strings.TrimSpace(selection.TargetType))
	selection.ImageType = strings.ToLower(strings.TrimSpace(selection.ImageType))
	selection.TargetContentID = strings.TrimSpace(selection.TargetContentID)
	selection.ParentContentID = strings.TrimSpace(selection.ParentContentID)
	selection.StoredPath = strings.TrimSpace(selection.StoredPath)
	if selection.TargetContentID == "" || selection.StoredPath == "" {
		return fmt.Errorf("catalog: artwork target and stored path are required")
	}

	table, pathColumn, sourceColumn, thumbhashColumn, notFound, err := artworkTargetColumns(selection.TargetType, selection.ImageType)
	if err != nil {
		return err
	}

	tx, err := s.itemRepo.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("catalog: begin artwork publication: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var previousPath string
	selectSQL := fmt.Sprintf("SELECT COALESCE(%s, '') FROM %s WHERE content_id = $1 FOR UPDATE", pathColumn, table)
	if err := tx.QueryRow(ctx, selectSQL, selection.TargetContentID).Scan(&previousPath); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound
		}
		return fmt.Errorf("catalog: lock artwork target: %w", err)
	}

	setThumbhash := ""
	args := []any{selection.StoredPath, selection.SourcePath, selection.TargetContentID}
	if thumbhashColumn != "" {
		setThumbhash = fmt.Sprintf(", %s = $4", thumbhashColumn)
		args = append(args, selection.Thumbhash)
	}
	updateSQL := fmt.Sprintf(`
		UPDATE %s
		SET %s = $1, %s = $2%s, updated_at = NOW()
		WHERE content_id = $3`, table, pathColumn, sourceColumn, setThumbhash)
	if _, err := tx.Exec(ctx, updateSQL, args...); err != nil {
		return fmt.Errorf("catalog: publish artwork pointer: %w", err)
	}

	lockContentID := selection.TargetContentID
	if selection.TargetType == artworkTargetSeason || selection.TargetType == artworkTargetEpisode {
		lockContentID = selection.ParentContentID
	}
	if lockContentID == "" {
		return fmt.Errorf("catalog: parent content ID is required for %s artwork", selection.TargetType)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE media_items
		SET locked_fields = CASE
				WHEN $2 = ANY(COALESCE(locked_fields, '{}'::integer[])) THEN COALESCE(locked_fields, '{}'::integer[])
				ELSE array_append(COALESCE(locked_fields, '{}'::integer[]), $2)
			END,
			updated_at = NOW()
		WHERE content_id = $1`, lockContentID, selection.LockField)
	if err != nil {
		return fmt.Errorf("catalog: lock manual artwork: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	// The selected revision is referenced by construction once this transaction
	// commits, so park it dormant immediately: no first-pass verification cycle
	// is needed, and database triggers re-arm it if a later writer displaces it.
	if err := parkArtworkRevision(ctx, tx, selection.StoredPath, selection.ImageType, time.Now().Add(defaultArtworkGCGracePeriod)); err != nil {
		return err
	}

	if previousPath != "" && previousPath != selection.StoredPath {
		if err := queueArtworkRevisionGC(ctx, tx, previousPath, selection.ImageType, time.Now().Add(defaultArtworkGCGracePeriod)); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("catalog: commit artwork publication: %w", err)
	}
	return nil
}

// QueueArtworkRevisionGC schedules an unreferenced cached revision for cleanup.
// It is used to reclaim an upload when publication fails after object storage
// succeeded. Cleanup still verifies database references before deleting.
func (s *DetailService) QueueArtworkRevisionGC(ctx context.Context, originalPath, imageType string, notBefore time.Time) error {
	if s == nil || s.itemRepo == nil || s.itemRepo.pool == nil {
		return fmt.Errorf("catalog: artwork persistence is not configured")
	}
	return queueArtworkRevisionGC(ctx, s.itemRepo.pool, originalPath, imageType, notBefore)
}

type artworkGCExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// queueArtworkRevisionGC arms a revision for verification after notBefore. The
// stored manifest (when one was tracked pre-upload) is kept; rows without one
// carry the image type so the collector can expand the object keys itself.
func queueArtworkRevisionGC(ctx context.Context, db artworkGCExecer, originalPath, imageType string, notBefore time.Time) error {
	return upsertArtworkRevision(ctx, db, originalPath, imageType, notBefore, false)
}

// parkArtworkRevision registers a revision as referenced and dormant. Only a
// displacement trigger or the collector's dormant sweep re-arms it.
func parkArtworkRevision(ctx context.Context, db artworkGCExecer, originalPath, imageType string, notBefore time.Time) error {
	return upsertArtworkRevision(ctx, db, originalPath, imageType, notBefore, true)
}

func upsertArtworkRevision(
	ctx context.Context,
	db artworkGCExecer,
	originalPath string,
	imageType string,
	notBefore time.Time,
	dormant bool,
) error {
	originalPath = strings.TrimSpace(originalPath)
	imageType = strings.ToLower(strings.TrimSpace(imageType))
	if originalPath == "" || strings.Contains(originalPath, "://") || imageType == "" {
		return nil
	}
	_, err := db.Exec(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, image_type, object_keys, not_before, next_attempt_at
		) VALUES ($1, $2, '{}', $3::timestamptz, CASE WHEN $4 THEN NULL ELSE $3::timestamptz END)
		ON CONFLICT (original_path) DO UPDATE SET
			image_type = CASE
				WHEN artwork_revision_gc_candidates.image_type = '' THEN EXCLUDED.image_type
				ELSE artwork_revision_gc_candidates.image_type
			END,
			not_before = EXCLUDED.not_before,
			next_attempt_at = EXCLUDED.next_attempt_at,
			attempt_count = 0,
			locked_at = NULL,
			locked_by = '',
			last_error = '',
			updated_at = NOW()`, originalPath, imageType, notBefore, dormant)
	if err != nil {
		return fmt.Errorf("catalog: queue artwork revision cleanup: %w", err)
	}
	return nil
}

// lockedArtworkAssignments returns ON CONFLICT DO UPDATE assignments for one
// artwork slot of table: columns[0] is the slot's path, the rest its source and
// thumbhash. While lockedSQL holds and the stored slot shows an image, the
// stored columns stay, so a refresh that read the row before an admin
// published artwork cannot write its older copy back. An empty slot still
// fills, as the refresh merge does under the lock.
//
// DO UPDATE sees the conflicting row's latest version, but a subquery in
// lockedSQL reads the statement's snapshot; lockArtworkRowsForUpsert keeps a
// publication that commits while the upsert waits from slipping past it.
func lockedArtworkAssignments(table, lockedSQL string, columns ...string) string {
	assignments := make([]string, len(columns))
	for i, column := range columns {
		assignments[i] = fmt.Sprintf("%[3]s = CASE WHEN %[2]s AND COALESCE(%[1]s.%[4]s, '') <> '' THEN %[1]s.%[3]s ELSE EXCLUDED.%[3]s END",
			table, lockedSQL, column, columns[0])
	}
	return strings.Join(assignments, ",\n\t\t\t")
}

// imagesLockedSQL holds when the locked_fields column named by lockedFields
// has FieldImages locked.
func imagesLockedSQL(lockedFields string) string {
	return fmt.Sprintf("%d = ANY(%s)", fieldImagesLocked, lockedFields)
}

// seriesImagesLockedSQL holds when the series of the table row being written
// has FieldImages locked, which is where an admin pick of a season poster or
// episode still sets the lock.
func seriesImagesLockedSQL(table string) string {
	return fmt.Sprintf("EXISTS (SELECT 1 FROM media_items WHERE content_id = %s.series_id AND %s)",
		table, imagesLockedSQL("media_items.locked_fields"))
}

// artworkUpsertTxOptions runs a season or episode upsert at READ COMMITTED, so
// the upsert statement takes a snapshot after lockArtworkRowsForUpsert waited.
var artworkUpsertTxOptions = pgx.TxOptions{IsoLevel: pgx.ReadCommitted}

// lockArtworkRowsForUpsert locks the existing rows a season or episode upsert
// is about to update, in content-id order, and returns their content IDs; query
// selects content_id. PublishArtworkSelection changes a season or episode row
// and its series' images lock in one transaction, so once these locks are
// held, the next statement's snapshot either includes a publication of these
// rows or none can commit until the upsert does. Rows are locked as the upsert
// would lock them anyway, child before series.
//
// A row another writer creates after this query is not covered, so the upsert
// passes each row it updated (rather than inserted) to checkArtworkRowLocked.
func lockArtworkRowsForUpsert(ctx context.Context, tx pgx.Tx, query string, args ...any) (map[string]struct{}, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("locking artwork rows: %w", err)
	}
	contentIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("locking artwork rows: %w", err)
	}
	locked := make(map[string]struct{}, len(contentIDs))
	for _, contentID := range contentIDs {
		locked[contentID] = struct{}{}
	}
	return locked, nil
}

// errArtworkRowAppeared reports that an upsert updated a row
// lockArtworkRowsForUpsert did not lock: another writer created it in between,
// possibly with published artwork the upsert's snapshot predates.
var errArtworkRowAppeared = errors.New("catalog: artwork row appeared after its lock")

// checkArtworkRowLocked fails an upsert that updated a row it never locked.
// inserted comes from RETURNING (xmax = 0).
func checkArtworkRowLocked(locked map[string]struct{}, contentID string, inserted bool) error {
	if _, ok := locked[contentID]; ok || inserted {
		return nil
	}
	return errArtworkRowAppeared
}

// retryArtworkUpsert reruns a season or episode upsert transaction that failed
// with errArtworkRowAppeared. The rerun locks the row that appeared, so one
// retry normally settles it.
func retryArtworkUpsert(upsert func() error) error {
	const attempts = 3
	var err error
	for range attempts {
		if err = upsert(); !errors.Is(err, errArtworkRowAppeared) {
			return err
		}
	}
	return err
}

func compactArtworkObjectKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || strings.Contains(key, "://") {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

// artworkTargetColumns maps a selection target to its artwork columns. Season
// and episode rows have dedicated tables; every other catalog item type
// (movie, series, audiobook, ebook, manga, ...) stores artwork on its
// media_items row.
func artworkTargetColumns(targetType, imageType string) (table, pathColumn, sourceColumn, thumbhashColumn string, notFound error, err error) {
	switch targetType {
	case artworkTargetSeason:
		switch imageType {
		case artworkImagePoster:
			return "seasons", artworkPosterPathColumn, artworkPosterSourceColumn, artworkPosterThumbColumn, ErrSeasonNotFound, nil
		case artworkImageBackdrop:
			return "seasons", artworkBackdropPathColumn, artworkBackdropSourceColumn, artworkBackdropThumbColumn, ErrSeasonNotFound, nil
		}
	case artworkTargetEpisode:
		if imageType == artworkImageStill {
			return "episodes", "still_path", "still_source_path", "still_thumbhash", ErrEpisodeNotFound, nil
		}
	default:
		table = "media_items"
		notFound = ErrItemNotFound
		switch imageType {
		case artworkImagePoster:
			return table, artworkPosterPathColumn, artworkPosterSourceColumn, artworkPosterThumbColumn, notFound, nil
		case artworkImageBackdrop:
			return table, artworkBackdropPathColumn, artworkBackdropSourceColumn, artworkBackdropThumbColumn, notFound, nil
		case artworkImageLogo:
			return table, artworkLogoPathColumn, artworkLogoSourceColumn, "", notFound, nil
		}
	}
	return "", "", "", "", nil, fmt.Errorf("%w: %s does not accept %q images", ErrUnsupportedArtworkSelection, targetType, imageType)
}
