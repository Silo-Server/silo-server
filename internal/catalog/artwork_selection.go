package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

const (
	defaultArtworkGCGracePeriod    = 24 * time.Hour
	artworkTargetSeason            = "season"
	artworkTargetEpisode           = "episode"
	artworkImagePoster             = "poster"
	artworkImageBackdrop           = "backdrop"
	artworkImageLogo               = "logo"
	artworkImageStill              = "still"
	artworkProviderVariantFeatured = "featured"
	artworkPosterPathColumn        = "poster_path"
	artworkBackdropPathColumn      = "backdrop_path"
	artworkLogoPathColumn          = "logo_path"
	artworkPosterSourceColumn      = "poster_source_path"
	artworkBackdropSourceColumn    = "backdrop_source_path"
	artworkLogoSourceColumn        = "logo_source_path"
	artworkPosterThumbColumn       = "poster_thumbhash"
	artworkBackdropThumbColumn     = "backdrop_thumbhash"
)

// ErrUnsupportedArtworkSelection reports a target/image-type combination that
// has no artwork column. Handlers should reject these before caching anything.
var ErrUnsupportedArtworkSelection = errors.New("catalog: unsupported artwork selection")

// ArtworkRevisionTracker registers every immutable artwork upload before its
// objects are written, allowing the collector to reclaim uploads that are
// never published. Confirmed live revisions remain dormant in the registry so
// database triggers can later reactivate them.
type ArtworkRevisionTracker struct {
	pool         *pgxpool.Pool
	gracePeriod  time.Duration
	storeBinding func() string
}

func NewArtworkRevisionTracker(pool *pgxpool.Pool, storeBinding ...func() string) *ArtworkRevisionTracker {
	if pool == nil {
		return nil
	}
	var binding func() string
	if len(storeBinding) > 0 {
		binding = storeBinding[0]
	}
	return &ArtworkRevisionTracker{pool: pool, gracePeriod: defaultArtworkGCGracePeriod, storeBinding: binding}
}

func (t *ArtworkRevisionTracker) currentStoreBinding() string {
	if t == nil || t.storeBinding == nil {
		return ""
	}
	return strings.TrimSpace(t.storeBinding())
}

const trackArtworkRevisionSQL = `
	INSERT INTO artwork_revision_gc_candidates (
		original_path, image_type, object_keys, not_before, next_attempt_at
	) VALUES ($1, $2, $3, $4, $4)
	ON CONFLICT (original_path) DO UPDATE SET
		object_keys = EXCLUDED.object_keys,
		image_type = CASE
			WHEN artwork_revision_gc_candidates.image_type = '' THEN EXCLUDED.image_type
			ELSE artwork_revision_gc_candidates.image_type
		END,
		not_before = CASE
			WHEN artwork_revision_gc_candidates.next_attempt_at IS NULL THEN artwork_revision_gc_candidates.not_before
			WHEN artwork_revision_gc_candidates.source_class = 'seed' THEN GREATEST(
				COALESCE(artwork_revision_gc_candidates.seed_expires_at, artwork_revision_gc_candidates.not_before),
				EXCLUDED.not_before
			)
			ELSE EXCLUDED.not_before
		END,
		next_attempt_at = CASE
			WHEN artwork_revision_gc_candidates.next_attempt_at IS NULL THEN NULL
			WHEN artwork_revision_gc_candidates.source_class = 'seed' THEN GREATEST(
				COALESCE(artwork_revision_gc_candidates.seed_expires_at, artwork_revision_gc_candidates.next_attempt_at),
				EXCLUDED.next_attempt_at
			)
			ELSE EXCLUDED.next_attempt_at
		END,
		deleted_at = NULL,
		deletion_started_at = NULL,
		tombstoned_at = NULL,
		attempt_count = 0,
		locked_at = NULL,
		locked_by = '',
		last_error = '',
		updated_at = NOW()`

// TrackArtworkRevision records the exact object manifest for a revision before
// its upload starts. Image caching calls this before uploading, so it
// serializes with a collector that may already be deleting an older,
// currently-unreferenced copy. Revisions parked as referenced stay dormant: a
// re-cache of live artwork is not garbage, and displacement triggers re-arm
// the row if the reference later moves away.
func (t *ArtworkRevisionTracker) TrackArtworkRevision(ctx context.Context, originalPath, imageType string, objectKeys []string) error {
	if t == nil || t.pool == nil {
		return fmt.Errorf("catalog: artwork revision tracking is not configured")
	}
	originalPath = strings.TrimSpace(originalPath)
	keys := compactArtworkObjectKeys(objectKeys)
	if originalPath == "" || strings.Contains(originalPath, "://") || len(keys) == 0 {
		return nil
	}
	notBefore := time.Now().Add(t.gracePeriod)
	// deleted_at is cleared because this upsert precedes a re-upload of the
	// exact manifest: the objects exist again once the cacher finishes.
	_, err := t.pool.Exec(ctx, trackArtworkRevisionSQL, originalPath, strings.ToLower(strings.TrimSpace(imageType)), keys, notBefore)
	if err != nil {
		return fmt.Errorf("catalog: track artwork revision: %w", err)
	}
	return nil
}

// ParkArtworkRevision marks a successfully published revision dormant. The
// image-cache repair path publishes through target-specific conditional
// updates rather than DetailService.PublishArtworkSelection, so it calls this
// same lifecycle primitive before completing its durable repair job.
func (t *ArtworkRevisionTracker) ParkArtworkRevision(ctx context.Context, originalPath, imageType string) error {
	if t == nil || t.pool == nil {
		return fmt.Errorf("catalog: artwork revision tracking is not configured")
	}
	if err := parkArtworkRevision(ctx, t.pool, originalPath, imageType, time.Now().Add(t.gracePeriod)); err != nil {
		return fmt.Errorf("catalog: park published artwork revision: %w", err)
	}
	return nil
}

const retainUntrackedArtworkRevisionSQL = `
	UPDATE artwork_revision_gc_candidates
	SET source_class = 'seed',
		seed_imported_at = COALESCE(seed_imported_at, NOW()),
		seed_expires_at = NULL,
		next_attempt_at = NULL,
		not_before = GREATEST(not_before, NOW()),
		deleted_at = NULL,
		deletion_started_at = NULL,
		tombstoned_at = NULL,
		attempt_count = 0,
		locked_at = NULL,
		locked_by = '',
		last_error = '',
		updated_at = NOW()
	WHERE original_path = $1`

// RetainUntrackedArtworkRevision preserves an existing candidate that has just
// been published by a user-store surface outside PostgreSQL's reference union.
// Keeping it as an unarmed seed makes the coverage limitation visible to
// accounting without allowing GC to delete a live, unverifiable upload.
func (t *ArtworkRevisionTracker) RetainUntrackedArtworkRevision(ctx context.Context, originalPath string) error {
	if t == nil || t.pool == nil {
		return fmt.Errorf("catalog: artwork revision tracking is not configured")
	}
	originalPath = strings.TrimSpace(originalPath)
	if originalPath == "" || strings.Contains(originalPath, "://") {
		return nil
	}
	if _, err := t.pool.Exec(ctx, retainUntrackedArtworkRevisionSQL, originalPath); err != nil {
		return fmt.Errorf("catalog: retain untracked artwork revision: %w", err)
	}
	return nil
}

// RecordArtworkRevision marks a tracked revision complete after every object,
// including manifest.json, is durable. Sizes are the exact encoded bytes or
// store-confirmed metadata supplied by the writer; this method never derives
// estimates from dimensions.
func (t *ArtworkRevisionTracker) RecordArtworkRevision(
	ctx context.Context,
	originalPath string,
	sourceClass string,
	objects []artworkstore.ObjectInfo,
) error {
	if t == nil || t.pool == nil {
		return fmt.Errorf("catalog: artwork revision tracking is not configured")
	}
	originalPath = strings.TrimSpace(originalPath)
	if originalPath == "" || strings.Contains(originalPath, "://") || len(objects) == 0 {
		return nil
	}
	sourceClass = normalizeArtworkSourceClass(sourceClass)
	objects = append([]artworkstore.ObjectInfo(nil), objects...)
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	keys := make([]string, 0, len(objects))
	sizes := make([]int64, 0, len(objects))
	contentTypes := make([]string, 0, len(objects))
	var total int64
	for _, object := range objects {
		key := strings.TrimSpace(object.Key)
		if key == "" || object.SizeBytes < 0 {
			return fmt.Errorf("catalog: invalid artwork inventory object %q", object.Key)
		}
		keys = append(keys, key)
		sizes = append(sizes, object.SizeBytes)
		contentTypes = append(contentTypes, strings.TrimSpace(object.MediaType))
		total += object.SizeBytes
	}
	tag, err := t.pool.Exec(ctx, `
		UPDATE artwork_revision_gc_candidates
		SET object_keys = $2,
			object_sizes_bytes = $3,
			object_content_types = $4,
			total_physical_bytes = $5,
			source_class = CASE
				WHEN seed_imported_at IS NOT NULL AND seed_expires_at IS NULL THEN 'seed'
				ELSE $6
			END,
			store_generation = $7,
			inventory_complete = TRUE,
			last_verified_at = NOW(),
			seed_imported_at = CASE
				WHEN seed_imported_at IS NOT NULL AND seed_expires_at IS NULL THEN seed_imported_at
				ELSE NULL
			END,
			seed_expires_at = NULL,
			deleted_at = NULL,
			deletion_started_at = NULL,
			tombstoned_at = NULL,
			updated_at = NOW()
		WHERE original_path = $1`, originalPath, keys, sizes, contentTypes, total, sourceClass, t.currentStoreBinding())
	if err != nil {
		return fmt.Errorf("catalog: record artwork revision inventory: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("catalog: artwork revision %q was not tracked before completion", originalPath)
	}
	return nil
}

// artworkSourceClassProvider is the canonical provider source class; the
// remaining classes appear once here and stay literals.
const artworkSourceClassProvider = "provider"

func normalizeArtworkSourceClass(sourceClass string) string {
	switch strings.ToLower(strings.TrimSpace(sourceClass)) {
	case artworkSourceClassProvider, "plugin", "library_sidecar", "embedded", "generated", "upload", "bundled":
		return strings.ToLower(strings.TrimSpace(sourceClass))
	default:
		return "unknown"
	}
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
			next_attempt_at = CASE
				WHEN artwork_revision_gc_candidates.seed_imported_at IS NOT NULL
					AND artwork_revision_gc_candidates.seed_expires_at IS NULL THEN NULL
				ELSE EXCLUDED.next_attempt_at
			END,
			attempt_count = 0,
			locked_at = NULL,
			locked_by = '',
			last_error = '',
			deleted_at = NULL,
			deletion_started_at = NULL,
			tombstoned_at = NULL,
			updated_at = NOW()`, originalPath, imageType, notBefore, dormant)
	if err != nil {
		return fmt.Errorf("catalog: queue artwork revision cleanup: %w", err)
	}
	return nil
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
		if imageType == artworkImagePoster {
			return "seasons", artworkPosterPathColumn, artworkPosterSourceColumn, artworkPosterThumbColumn, ErrSeasonNotFound, nil
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
