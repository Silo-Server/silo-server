package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pathscope"
	"github.com/Silo-Server/silo-server/internal/requestlock"
)

// Sentinel errors for item repository operations.
var (
	ErrItemNotFound = errors.New("media item not found")
)

// ItemRepository provides CRUD operations for the media_items table.
type ItemRepository struct {
	pool              *pgxpool.Pool
	searchIndexEvents *SearchIndexEventRepository
}

type itemExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

type VirtualPurgeOptions struct {
	DryRun         bool
	LibraryID      int
	InstallationID int
}

// ReconcileCollectionVirtualLibraryLinks repairs historical collection virtual
// items that were linked to a library incompatible with their media type. It
// only moves database links/files; it never contacts a provider or deletes the
// media item itself.
func (r *ItemRepository) ReconcileCollectionVirtualLibraryLinks(ctx context.Context, collectionID string) (int64, int64, error) {
	if r == nil || r.pool == nil {
		return 0, 0, errors.New("item repository is not configured")
	}
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		return 0, 0, errors.New("collection id is required")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin collection virtual reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE reconcile_virtual_targets ON COMMIT DROP AS
		SELECT DISTINCT ON (lci.media_item_id)
			lci.media_item_id AS content_id, mf.id AS media_folder_id
		FROM library_collection_items lci
		JOIN media_items mi ON mi.content_id = lci.media_item_id
		JOIN library_collection_libraries lcl ON lcl.collection_id = lci.collection_id
		JOIN media_folders mf ON mf.id = lcl.library_id AND mf.enabled IS NOT FALSE
		WHERE lci.collection_id = $1
		  AND (
		      mi.virtual_source='collection'
		      OR left(mi.virtual_source,11)='collection:'
		      OR EXISTS (
		          SELECT 1 FROM virtual_media_source_claims claim
		          WHERE claim.content_id=mi.content_id
		            AND (claim.source_key='collection' OR left(claim.source_key,11)='collection:')
		      )
		  )
		  AND ((mi.type = 'movie' AND mf.type IN ('movies','mixed'))
		    OR (mi.type = 'series' AND mf.type IN ('series','mixed')))
		ORDER BY lci.media_item_id,
			CASE WHEN (mi.type = 'movie' AND mf.type = 'movies') OR (mi.type = 'series' AND mf.type = 'series') THEN 0 ELSE 1 END,
			mf.id`, collectionID); err != nil {
		return 0, 0, fmt.Errorf("identify compatible collection libraries: %w", err)
	}
	badLinks, err := tx.Exec(ctx, `
		DELETE FROM media_item_libraries mil
		USING media_items mi
		WHERE mil.content_id = mi.content_id
		  AND (
		      mi.virtual_source='collection'
		      OR left(mi.virtual_source,11)='collection:'
		      OR EXISTS (
		          SELECT 1 FROM virtual_media_source_claims claim
		          WHERE claim.content_id=mi.content_id
		            AND (claim.source_key='collection' OR left(claim.source_key,11)='collection:')
		      )
		  )
		  AND mi.content_id IN (SELECT content_id FROM reconcile_virtual_targets)
		  AND NOT EXISTS (
			SELECT 1 FROM media_folders mf
			WHERE mf.id = mil.media_folder_id AND mf.enabled IS NOT FALSE
			  AND ((mi.type = 'movie' AND mf.type IN ('movies','mixed'))
			    OR (mi.type = 'series' AND mf.type IN ('series','mixed')))
		  )`)
	if err != nil {
		return 0, 0, fmt.Errorf("remove incompatible collection links: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_item_libraries(content_id, media_folder_id, first_seen_at)
		SELECT content_id, media_folder_id, NOW() FROM reconcile_virtual_targets
		ON CONFLICT DO NOTHING`); err != nil {
		return 0, 0, fmt.Errorf("link compatible collection libraries: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE reconcile_virtual_file_targets ON COMMIT DROP AS
		SELECT mf.id,t.media_folder_id,
		       EXISTS (
		           SELECT 1 FROM media_files target_file
		           WHERE target_file.media_folder_id=t.media_folder_id
		             AND target_file.file_path=mf.file_path
		             AND target_file.virtual_owner_installation_id
		                 IS NOT DISTINCT FROM mf.virtual_owner_installation_id
		       ) AS target_exists,
		       row_number() OVER (
		           PARTITION BY t.media_folder_id,mf.file_path,mf.virtual_owner_installation_id
		           ORDER BY mf.id
		       ) AS target_rank
		FROM media_files mf
		JOIN reconcile_virtual_targets t ON t.content_id=mf.content_id
		JOIN media_folders current_folder ON current_folder.id=mf.media_folder_id
		JOIN media_items current_item ON current_item.content_id=mf.content_id
		WHERE (mf.container='virtual' OR mf.file_path LIKE 'virtual://%')
		  AND mf.media_folder_id<>t.media_folder_id
		  AND (
		      current_folder.enabled IS FALSE
		      OR (current_item.type='movie' AND current_folder.type NOT IN ('movies','mixed'))
		      OR (current_item.type='series' AND current_folder.type NOT IN ('series','mixed'))
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims claim
		      WHERE claim.content_id=mf.content_id
		        AND claim.media_folder_id=mf.media_folder_id
		        AND claim.file_path=mf.file_path
		  )`); err != nil {
		return 0, 0, fmt.Errorf("identify collection virtual files to move: %w", err)
	}
	duplicateFiles, err := tx.Exec(ctx, `
		DELETE FROM media_files mf
		USING reconcile_virtual_file_targets target
		WHERE mf.id=target.id
		  AND (target.target_exists OR target.target_rank>1)`)
	if err != nil {
		return 0, 0, fmt.Errorf("remove duplicate collection virtual files: %w", err)
	}
	files, err := tx.Exec(ctx, `
		UPDATE media_files mf
		SET media_folder_id=target.media_folder_id,updated_at=NOW()
		FROM reconcile_virtual_file_targets target
		WHERE mf.id=target.id
		  AND NOT target.target_exists
		  AND target.target_rank=1`)
	if err != nil {
		return 0, 0, fmt.Errorf("move collection virtual files: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit collection virtual reconciliation: %w", err)
	}
	return badLinks.RowsAffected(), files.RowsAffected() + duplicateFiles.RowsAffected(), nil
}

// VirtualPlaybackVariant describes a provider-neutral profile variant. The
// URI is resolved lazily by a virtual playback plugin when playback starts.
type VirtualPlaybackVariant struct {
	VirtualURI          string
	Label               string
	Resolution          string
	CodecVideo          string
	CodecAudio          string
	HDR                 string
	OwnerInstallationID int
}

// PurgeVirtualPlaybackItems removes zero-storage virtual files and any
// catalog items that no longer have a physical or virtual file. Filters are
// optional and make administrative cleanup safe to scope.
func (r *ItemRepository) PurgeVirtualPlaybackItems(ctx context.Context, opts VirtualPurgeOptions) (filesDeleted int64, itemsDeleted int64, err error) {
	if r == nil || r.pool == nil {
		return 0, 0, errors.New("item repository is not configured")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin virtual playback purge: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE purge_virtual_items ON COMMIT DROP AS
		SELECT DISTINCT mf.content_id FROM media_files mf
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%')
		  AND ($1 = 0 OR mf.media_folder_id = $1)
		  AND ($2 = 0 OR mf.virtual_owner_installation_id = $2 OR (mf.virtual_owner_installation_id = 0 AND mi.virtual_owner_installation_id = $2))`, opts.LibraryID, opts.InstallationID); err != nil {
		return 0, 0, fmt.Errorf("identify virtual media items: %w", err)
	}
	// Source claims are independent catalog ownership records, not foreign
	// keys to media_files. Remove the exact claims for files selected by this
	// purge first so retained local items do not keep ghost plugin ownership.
	if _, err := tx.Exec(ctx, `
		DELETE FROM virtual_media_file_source_claims claim
		USING media_files mf, media_items mi
		WHERE mf.content_id=mi.content_id
		  AND claim.content_id=mf.content_id
		  AND claim.media_folder_id=mf.media_folder_id
		  AND claim.file_path=mf.file_path
		  AND (mf.container='virtual' OR mf.file_path LIKE 'virtual://%')
		  AND mf.content_id IN (SELECT content_id FROM purge_virtual_items)
		  AND ($1=0 OR mf.media_folder_id=$1)
		  AND ($2=0 OR mf.virtual_owner_installation_id=$2
		      OR (mf.virtual_owner_installation_id=0 AND mi.virtual_owner_installation_id=$2))
		  AND ($2=0 OR claim.plugin_installation_id=$2)`,
		opts.LibraryID, opts.InstallationID); err != nil {
		return 0, 0, fmt.Errorf("delete virtual file source claims: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM virtual_media_source_claims claim
		WHERE claim.content_id IN (SELECT content_id FROM purge_virtual_items)
		  AND ($1=0 OR claim.media_folder_id=$1)
		  AND ($2=0 OR claim.plugin_installation_id=$2)
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims file_claim
		      WHERE file_claim.plugin_installation_id=claim.plugin_installation_id
		        AND file_claim.source_key=claim.source_key
		        AND file_claim.content_id=claim.content_id
		        AND file_claim.media_folder_id=claim.media_folder_id
		  )`, opts.LibraryID, opts.InstallationID); err != nil {
		return 0, 0, fmt.Errorf("delete empty virtual source claims: %w", err)
	}
	result, err := tx.Exec(ctx, `
		DELETE FROM media_files mf
		USING media_items mi
		WHERE mf.content_id = mi.content_id
		  AND (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%')
		  AND mf.content_id IN (SELECT content_id FROM purge_virtual_items)
		  AND ($1 = 0 OR mf.media_folder_id = $1)
		  AND ($2 = 0 OR mf.virtual_owner_installation_id = $2 OR (mf.virtual_owner_installation_id = 0 AND mi.virtual_owner_installation_id = $2))
		  AND ($2=0 OR NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims remaining
		      WHERE remaining.content_id=mf.content_id
		        AND remaining.media_folder_id=mf.media_folder_id
		        AND remaining.file_path=mf.file_path
		  ))`, opts.LibraryID, opts.InstallationID)
	if err != nil {
		return 0, 0, fmt.Errorf("delete virtual files: %w", err)
	}
	filesDeleted = result.RowsAffected()

	if _, err := tx.Exec(ctx, `
		WITH chosen AS (
			SELECT DISTINCT ON (claim.content_id)
				claim.content_id,claim.plugin_installation_id,claim.source_key,
				claim.media_folder_id
			FROM virtual_media_source_claims claim
			WHERE claim.content_id IN (SELECT content_id FROM purge_virtual_items)
			  AND NOT EXISTS (
			      SELECT 1 FROM media_files physical
			      WHERE physical.content_id=claim.content_id
			        AND physical.container<>'virtual'
			        AND physical.file_path NOT LIKE 'virtual://%'
			  )
			ORDER BY claim.content_id,claim.owns_item_metadata DESC,
			         claim.last_seen_at DESC,claim.plugin_installation_id,
			         claim.source_key,claim.media_folder_id
		)
		UPDATE virtual_media_source_claims claim
		SET owns_item_metadata=(
			chosen.plugin_installation_id IS NOT NULL
			AND claim.plugin_installation_id=chosen.plugin_installation_id
			AND claim.source_key=chosen.source_key
			AND claim.media_folder_id=chosen.media_folder_id
		),updated_at=NOW()
		FROM purge_virtual_items affected
		LEFT JOIN chosen ON chosen.content_id=affected.content_id
		WHERE claim.content_id=affected.content_id`); err != nil {
		return 0, 0, fmt.Errorf("reconcile remaining virtual ownership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE media_items mi
		SET virtual_owner_installation_id=claim.plugin_installation_id,
		    virtual_source=claim.source_key,
		    virtual_last_seen_at=claim.last_seen_at,
		    updated_at=NOW()
		FROM (
		    SELECT DISTINCT ON (content_id)
		        content_id,plugin_installation_id,source_key,last_seen_at
		    FROM virtual_media_source_claims
		    WHERE owns_item_metadata
		    ORDER BY content_id,last_seen_at DESC,plugin_installation_id,source_key
		) claim
		WHERE mi.content_id=claim.content_id
		  AND mi.content_id IN (SELECT content_id FROM purge_virtual_items)`); err != nil {
		return 0, 0, fmt.Errorf("restore remaining virtual ownership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE media_items mi
		SET virtual_owner_installation_id=NULL,
		    virtual_source='',
		    virtual_last_seen_at=NULL,
		    updated_at=NOW()
		WHERE mi.content_id IN (SELECT content_id FROM purge_virtual_items)
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_source_claims claim
		      WHERE claim.content_id=mi.content_id AND claim.owns_item_metadata
		  )`); err != nil {
		return 0, 0, fmt.Errorf("clear purged virtual ownership: %w", err)
	}

	rows, err := tx.Query(ctx, `
		DELETE FROM media_items mi
		WHERE EXISTS (SELECT 1 FROM purge_virtual_items pvi WHERE pvi.content_id = mi.content_id)
		  AND NOT EXISTS (SELECT 1 FROM media_files mf WHERE mf.content_id = mi.content_id)
		RETURNING mi.content_id`)
	if err != nil {
		return 0, 0, fmt.Errorf("delete orphaned virtual media items: %w", err)
	}
	deletedIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, 0, fmt.Errorf("collect deleted virtual media IDs: %w", err)
	}
	itemsDeleted = int64(len(deletedIDs))
	if opts.DryRun {
		return filesDeleted, itemsDeleted, nil
	}
	if err := EnqueueSearchIndexDeletes(ctx, tx, deletedIDs); err != nil {
		return 0, 0, fmt.Errorf("enqueue virtual media search deletes: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM library_collection_items lci
		WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = lci.media_item_id)`); err != nil {
		return 0, 0, fmt.Errorf("clean virtual collection links: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit virtual playback purge: %w", err)
	}
	return filesDeleted, itemsDeleted, nil
}

// CleanupRequestVirtualMedia removes only virtual sources for a canceled
// request. Physical files and collection membership are never removed.
func (r *ItemRepository) CleanupRequestVirtualMedia(ctx context.Context, mediaType string, tmdbID int, tvdbID, imdbID string) error {
	if r == nil || r.pool == nil {
		return errors.New("item repository is not configured")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin request virtual cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Match the request repository's per-title lock so a replacement request
	// cannot race cancellation cleanup.
	if lockKey, ok := requestlock.MediaKey(mediaType, tmdbID, tvdbID, imdbID); ok {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
			return fmt.Errorf("lock request virtual media: %w", err)
		}
	}
	var activeReplacement bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM media_requests
			WHERE media_type=$1
			  AND outcome='active'
			  AND status<>'completed'
			  AND (($2>0 AND tmdb_id=$2)
			    OR ($3<>'' AND tvdb_id::text=$3)
			    OR ($4<>'' AND imdb_id=$4))
		)`,
		mediaType, tmdbID, strings.TrimSpace(tvdbID), strings.TrimSpace(imdbID),
	).Scan(&activeReplacement); err != nil {
		return fmt.Errorf("check replacement request before virtual cleanup: %w", err)
	}
	if activeReplacement {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit skipped request virtual cleanup: %w", err)
		}
		return nil
	}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE request_virtual_items ON COMMIT DROP AS
		SELECT mi.content_id
		FROM media_items mi
		WHERE mi.type=$1
		  AND (($2>0 AND mi.tmdb_id=$2::text)
		    OR ($3<>'' AND mi.tvdb_id=$3)
		    OR ($4<>'' AND mi.imdb_id=$4))`,
		mediaType, tmdbID, strings.TrimSpace(tvdbID), strings.TrimSpace(imdbID)); err != nil {
		return fmt.Errorf("identify request virtual media: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE request_virtual_files ON COMMIT DROP AS
		SELECT plugin_installation_id,source_key,content_id,media_folder_id,file_path
		FROM virtual_media_file_source_claims
		WHERE (source_key='request' OR left(source_key,8)='request:')
		  AND content_id IN (SELECT content_id FROM request_virtual_items)`); err != nil {
		return fmt.Errorf("identify request virtual file claims: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM virtual_media_file_source_claims claim
		USING request_virtual_files target
		WHERE claim.plugin_installation_id=target.plugin_installation_id
		  AND claim.source_key=target.source_key
		  AND claim.content_id=target.content_id
		  AND claim.media_folder_id=target.media_folder_id
		  AND claim.file_path=target.file_path`); err != nil {
		return fmt.Errorf("delete request virtual file claims: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM media_files mf
		USING request_virtual_files target
		WHERE mf.content_id=target.content_id
		  AND mf.media_folder_id=target.media_folder_id
		  AND mf.file_path=target.file_path
		  AND mf.virtual_owner_installation_id=target.plugin_installation_id
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims remaining
		      WHERE remaining.plugin_installation_id=target.plugin_installation_id
		        AND remaining.content_id=target.content_id
		        AND remaining.media_folder_id=target.media_folder_id
		        AND remaining.file_path=target.file_path
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM library_collection_items membership
		      WHERE membership.media_item_id=target.content_id
		  )`); err != nil {
		return fmt.Errorf("delete unshared request virtual files: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM virtual_media_source_claims claim
		WHERE (claim.source_key='request' OR left(claim.source_key,8)='request:')
		  AND claim.content_id IN (SELECT content_id FROM request_virtual_items)
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims file_claim
		      WHERE file_claim.plugin_installation_id=claim.plugin_installation_id
		        AND file_claim.source_key=claim.source_key
		        AND file_claim.content_id=claim.content_id
		        AND file_claim.media_folder_id=claim.media_folder_id
		  )`); err != nil {
		return fmt.Errorf("delete request virtual source claims: %w", err)
	}
	// New registrations are claim-scoped above. Retain a narrow scalar fallback
	// for request rows created before source claims, while preserving anything
	// still referenced by a collection.
	if _, err := tx.Exec(ctx, `
		DELETE FROM media_files mf
		USING media_items mi
		WHERE mf.content_id=mi.content_id
		  AND mi.content_id IN (SELECT content_id FROM request_virtual_items)
		  AND mi.virtual_source='request'
		  AND (mf.container='virtual' OR mf.file_path LIKE 'virtual://%')
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims claim
		      WHERE claim.content_id=mf.content_id
		        AND claim.media_folder_id=mf.media_folder_id
		        AND claim.file_path=mf.file_path
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM library_collection_items lci
		      WHERE lci.media_item_id=mi.content_id
		  )`); err != nil {
		return fmt.Errorf("delete legacy request virtual files: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE media_items mi
		SET virtual_owner_installation_id=claim.plugin_installation_id,
		    virtual_source=claim.source_key,
		    virtual_last_seen_at=claim.last_seen_at,
		    updated_at=NOW()
		FROM (
		    SELECT DISTINCT ON (content_id)
		        content_id,plugin_installation_id,source_key,last_seen_at
		    FROM virtual_media_source_claims
		    WHERE owns_item_metadata
		    ORDER BY content_id,last_seen_at DESC,plugin_installation_id,source_key
		) claim
		WHERE mi.content_id=claim.content_id
		  AND mi.content_id IN (SELECT content_id FROM request_virtual_items)`); err != nil {
		return fmt.Errorf("restore request media ownership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE media_items mi
		SET virtual_owner_installation_id=NULL,
		    virtual_source='',
		    virtual_last_seen_at=NULL,
		    updated_at=NOW()
		WHERE mi.content_id IN (SELECT content_id FROM request_virtual_items)
		  AND mi.virtual_source='request'
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_source_claims claim
		      WHERE claim.content_id=mi.content_id AND claim.owns_item_metadata
		  )`); err != nil {
		return fmt.Errorf("clear removed request media ownership: %w", err)
	}
	rows, err := tx.Query(ctx, `
		DELETE FROM media_items mi
		WHERE mi.content_id IN (SELECT content_id FROM request_virtual_items)
		  AND NOT EXISTS (SELECT 1 FROM media_files mf WHERE mf.content_id = mi.content_id)
		  AND NOT EXISTS (SELECT 1 FROM library_collection_items lci WHERE lci.media_item_id = mi.content_id)
		  AND NOT EXISTS (SELECT 1 FROM virtual_media_source_claims claim WHERE claim.content_id=mi.content_id)
		RETURNING mi.content_id`)
	if err != nil {
		return fmt.Errorf("delete request virtual media items: %w", err)
	}
	deletedIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("collect request virtual media IDs: %w", err)
	}
	if err := EnqueueSearchIndexDeletes(ctx, tx, deletedIDs); err != nil {
		return fmt.Errorf("enqueue request virtual media search deletes: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit request virtual cleanup: %w", err)
	}
	return nil
}

// NewItemRepository creates a new ItemRepository backed by the given pool.
func NewItemRepository(pool *pgxpool.Pool) *ItemRepository {
	return &ItemRepository{
		pool:              pool,
		searchIndexEvents: NewSearchIndexEventRepository(pool),
	}
}

func (r *ItemRepository) WithSearchIndexEvents(events *SearchIndexEventRepository) *ItemRepository {
	if r == nil {
		return nil
	}
	r.searchIndexEvents = events
	return r
}

func (r *ItemRepository) WithActiveSearchProvider(provider string) *ItemRepository {
	if r == nil {
		return nil
	}
	if r.searchIndexEvents == nil {
		r.searchIndexEvents = NewSearchIndexEventRepository(r.pool)
	}
	r.searchIndexEvents.WithActiveProvider(provider)
	return r
}

// GetPoster returns the current poster path and thumbhash for a media item.
// Missing or NULL values are returned as empty strings.
func (r *ItemRepository) GetPoster(ctx context.Context, contentID string) (posterPath string, posterThumbhash string, err error) {
	if r == nil || r.pool == nil {
		return "", "", ErrItemNotFound
	}
	if err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(poster_path, ''), COALESCE(poster_thumbhash, '')
		FROM media_items
		WHERE content_id = $1
	`, contentID).Scan(&posterPath, &posterThumbhash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", ErrItemNotFound
		}
		return "", "", fmt.Errorf("getting media item poster: %w", err)
	}
	return posterPath, posterThumbhash, nil
}

// SetLocalPoster records a locally extracted poster without ever overwriting
// provider or manually applied artwork: the row is updated only when the item
// has no poster yet or its current poster lives under localPrefix. The
// condition is evaluated atomically in SQL so concurrent metadata writers
// (e.g. the ebook enrichment sweep) cannot be clobbered between a read and a
// write. Returns true when a row was updated.
func (r *ItemRepository) SetLocalPoster(ctx context.Context, contentID, posterPath, thumbhash, localPrefix string) (bool, error) {
	if r == nil || r.pool == nil {
		return false, ErrItemNotFound
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_items
		SET poster_path = $2,
		    poster_thumbhash = $3,
		    updated_at = NOW()
		WHERE content_id = $1
		  AND (poster_path IS NULL OR poster_path = '' OR poster_path LIKE $4 || '%')
	`, contentID, posterPath, thumbhash, localPrefix)
	if err != nil {
		return false, fmt.Errorf("setting local poster: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// GetPosterPath returns the current poster path for a media item. Missing or
// NULL poster values are returned as an empty string.
func (r *ItemRepository) GetPosterPath(ctx context.Context, contentID string) (string, error) {
	if r == nil || r.pool == nil {
		return "", ErrItemNotFound
	}
	var posterPath string
	if err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(poster_path, '')
		FROM media_items
		WHERE content_id = $1
	`, contentID).Scan(&posterPath); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrItemNotFound
		}
		return "", fmt.Errorf("getting media item poster path: %w", err)
	}
	return posterPath, nil
}

// overviewMatchFloor is the minimum ts_rank_cd score an overview-only match
// must achieve to be returned when no title FTS match exists in the candidate
// set. The overview tsvector is built without setweight(), so PostgreSQL's
// default D weight (0.1) applies: a single occurrence of a single-term query
// scores ~0.1. A floor of 0.15 effectively requires more than one occurrence
// (or a tightly clustered match for multi-term queries), which keeps niche
// description-only searches viable while suppressing the long tail of
// incidental one-mention hits that flooded results before.
const overviewMatchFloor = 0.15

// fuzzyFallbackThreshold is the FTS result count below which SearchPage reaches
// for the trigram fuzzy fallback (buildFuzzySearchSQL). At or above it the FTS
// result set is considered rich enough that a "did you mean" pass would only add
// noise, so common searches stay on the unchanged FTS path and the fuzzy query
// fires only in the sparse/misspelling case.
const fuzzyFallbackThreshold = 5

// fuzzyMaxResults caps how many trigram fuzzy matches the fallback contributes.
// The combined result the fuzzy path serves is therefore bounded at
// (fuzzyFallbackThreshold-1) FTS rows + fuzzyMaxResults fuzzy rows, small enough
// to materialize once and slice per page. A "did you mean" fallback has no need
// to paginate hundreds of low-similarity typo matches; when the true match count
// exceeds this, the overflow is logged and the total is reported as inexact.
const fuzzyMaxResults = 50

// trgmWordSimilarityThreshold pins pg_trgm.strict_word_similarity_threshold
// for the fuzzy query via SET LOCAL. The <<% operator's selectivity is
// otherwise governed by a cluster-wide GUC a DBA (or another application) can
// change, silently altering both match quality and scan cost per deployment —
// and its server default (0.6) would reject ordinary one-edit typos
// ("avegners" vs "avengers" scores ~0.38), so pinning is load-bearing, not
// just hygiene.
const trgmWordSimilarityThreshold = 0.30

// fuzzyAugmentSimilarityFloor is the minimum WHOLE-TITLE similarity demanded of
// fuzzy rows when the sparse FTS block is non-empty. A correctly spelled query
// with a few genuine hits ("coraline") should only gain near-identical titles,
// not every title clearing the permissive base threshold — word similarity is
// deliberately not used here, since it scores embedded prefix words far too
// high ("coral" is 0.5 to "coraline"). A zero-hit query (a likely misspelling)
// skips the floor entirely so typo recall, including word-extent matches into
// long titles, stays high.
const fuzzyAugmentSimilarityFloor = 0.45

// itemColumnNames lists, in scan order, every column selected by media_items
// item queries. Shared by itemColumns, qualifiedItemColumns, and
// qualifiedListItemColumns so the select lists can never drift from each
// other or from scanItem.
var itemColumnNames = []string{
	"content_id", "type", "title", "sort_title", "default_metadata_language", "original_title", "year", "genres",
	"content_rating", "runtime", "overview", "tagline",
	"rating_imdb", "rating_tmdb", "rating_rt_critic", "rating_rt_audience",
	"imdb_id", "tmdb_id", "tvdb_id",
	"poster_path", "poster_source_path", "poster_thumbhash", "backdrop_path", "backdrop_source_path", "backdrop_thumbhash", "logo_path", "logo_source_path",
	"metadata_s3_path", "metadata_etag", "season_count",
	"studios", "networks", "countries", "keywords", "original_language", "release_date::text", "first_air_date", "last_air_date", "air_time", "air_timezone",
	"show_status",
	"matched_at", "last_refreshed", "refresh_failures",
	"episode_metadata_incomplete", "episode_metadata_last_checked_at", "locked_fields", "status", "created_at", "updated_at",
}

// nullableStringItemColumns are media_items columns that may hold NULL but
// scan into plain (non-pointer) string fields on models.MediaItem, so select
// lists coalesce them to ”.
var nullableStringItemColumns = map[string]bool{
	"poster_path":          true,
	"poster_source_path":   true,
	"poster_thumbhash":     true,
	"backdrop_path":        true,
	"backdrop_source_path": true,
	"backdrop_thumbhash":   true,
	"logo_path":            true,
	"logo_source_path":     true,
	"metadata_s3_path":     true,
	"metadata_etag":        true,
}

// itemColumnExpr renders one select-list entry for col, qualified with alias
// when non-empty. Nullable string columns are coalesced to ” and aliased
// back to their own name so queries that wrap the select list in a CTE or
// subquery can still reference the column by name.
func itemColumnExpr(alias, col string) string {
	qualified := col
	if alias != "" {
		qualified = alias + "." + col
	}
	if nullableStringItemColumns[col] {
		return fmt.Sprintf("COALESCE(%s, '') AS %s", qualified, col)
	}
	return qualified
}

func joinItemColumns(alias string) string {
	exprs := make([]string, len(itemColumnNames))
	for i, col := range itemColumnNames {
		exprs[i] = itemColumnExpr(alias, col)
	}
	return strings.Join(exprs, ", ")
}

// itemColumns is the list of columns returned by all SELECT queries on media_items.
var itemColumns = joinItemColumns("")

func qualifiedItemColumns(alias string) string {
	return joinItemColumns(alias)
}

// qualifiedItemColumnRefs renders plain alias-qualified column references
// without COALESCE or AS aliases, for contexts like GROUP BY where output
// aliases are invalid.
func qualifiedItemColumnRefs(alias string) string {
	refs := make([]string, len(itemColumnNames))
	for i, col := range itemColumnNames {
		refs[i] = alias + "." + col
	}
	return strings.Join(refs, ", ")
}

func qualifiedListItemColumns(alias string) string {
	exprs := make([]string, len(itemColumnNames))
	for i, col := range itemColumnNames {
		if col == "last_air_date" {
			exprs[i] = effectiveLastAirDateExpr(alias) + " AS last_air_date"
			continue
		}
		exprs[i] = itemColumnExpr(alias, col)
	}
	return strings.Join(exprs, ", ")
}

// scanItem scans a single row into a *models.MediaItem.
func scanItem(row pgx.Row) (*models.MediaItem, error) {
	var item models.MediaItem
	err := row.Scan(
		&item.ContentID,
		&item.Type,
		&item.Title,
		&item.SortTitle,
		&item.DefaultMetadataLanguage,
		&item.OriginalTitle,
		&item.Year,
		&item.Genres,
		&item.ContentRating,
		&item.Runtime,
		&item.Overview,
		&item.Tagline,
		&item.RatingIMDB,
		&item.RatingTMDB,
		&item.RatingRTCritic,
		&item.RatingRTAudience,
		&item.ImdbID,
		&item.TmdbID,
		&item.TvdbID,
		&item.PosterPath,
		&item.PosterSourcePath,
		&item.PosterThumbhash,
		&item.BackdropPath,
		&item.BackdropSourcePath,
		&item.BackdropThumbhash,
		&item.LogoPath,
		&item.LogoSourcePath,
		&item.MetadataS3Path,
		&item.MetadataEtag,
		&item.SeasonCount,
		&item.Studios,
		&item.Networks,
		&item.Countries,
		&item.Keywords,
		&item.OriginalLanguage,
		&item.ReleaseDate,
		&item.FirstAirDate,
		&item.LastAirDate,
		&item.AirTime,
		&item.AirTimezone,
		&item.ShowStatus,
		&item.MatchedAt,
		&item.LastRefreshed,
		&item.RefreshFailures,
		&item.EpisodeMetadataIncomplete,
		&item.EpisodeMetadataLastCheckedAt,
		&item.LockedFields,
		&item.Status,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrItemNotFound
		}
		return nil, fmt.Errorf("scanning media item: %w", err)
	}
	return &item, nil
}

// scanItems scans multiple rows into a []*models.MediaItem slice.
// listItemScanDests returns the scan destinations matching
// qualifiedListItemColumns, in column order. Every scan over that select list
// must use this so the column list and destinations cannot drift apart.
func listItemScanDests(item *models.MediaItem) []any {
	return []any{
		&item.ContentID,
		&item.Type,
		&item.Title,
		&item.SortTitle,
		&item.DefaultMetadataLanguage,
		&item.OriginalTitle,
		&item.Year,
		&item.Genres,
		&item.ContentRating,
		&item.Runtime,
		&item.Overview,
		&item.Tagline,
		&item.RatingIMDB,
		&item.RatingTMDB,
		&item.RatingRTCritic,
		&item.RatingRTAudience,
		&item.ImdbID,
		&item.TmdbID,
		&item.TvdbID,
		&item.PosterPath,
		&item.PosterSourcePath,
		&item.PosterThumbhash,
		&item.BackdropPath,
		&item.BackdropSourcePath,
		&item.BackdropThumbhash,
		&item.LogoPath,
		&item.LogoSourcePath,
		&item.MetadataS3Path,
		&item.MetadataEtag,
		&item.SeasonCount,
		&item.Studios,
		&item.Networks,
		&item.Countries,
		&item.Keywords,
		&item.OriginalLanguage,
		&item.ReleaseDate,
		&item.FirstAirDate,
		&item.LastAirDate,
		&item.AirTime,
		&item.AirTimezone,
		&item.ShowStatus,
		&item.MatchedAt,
		&item.LastRefreshed,
		&item.RefreshFailures,
		&item.EpisodeMetadataIncomplete,
		&item.EpisodeMetadataLastCheckedAt,
		&item.LockedFields,
		&item.Status,
		&item.CreatedAt,
		&item.UpdatedAt,
	}
}

func scanItems(rows pgx.Rows) ([]*models.MediaItem, error) {
	var items []*models.MediaItem
	for rows.Next() {
		var item models.MediaItem
		if err := rows.Scan(listItemScanDests(&item)...); err != nil {
			return nil, fmt.Errorf("scanning media item row: %w", err)
		}
		items = append(items, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating media item rows: %w", err)
	}
	return items, nil
}

// scanItemsWithMangaCounts scans rows selected with qualifiedListItemColumns
// followed by mangaCountColumns. The count subqueries return 0 for non-manga
// rows; they are nilled out so only manga cards carry the counts (mirrors
// scanBrowseItems).
func scanItemsWithMangaCounts(rows pgx.Rows) ([]*models.MediaItem, error) {
	var items []*models.MediaItem
	for rows.Next() {
		var item models.MediaItem
		dests := append(listItemScanDests(&item), &item.MangaChapterCount, &item.MangaVolumeCount)
		if err := rows.Scan(dests...); err != nil {
			return nil, fmt.Errorf("scanning media item row with manga counts: %w", err)
		}
		if item.Type != "manga" {
			item.MangaChapterCount = nil
			item.MangaVolumeCount = nil
		}
		items = append(items, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating media item rows: %w", err)
	}
	return items, nil
}

// scanItemsWithTotal scans rows that include a trailing total_count column
// emitted by COUNT(*) OVER (). The total is identical for every row in the
// result set; we read it from the first row (or leave it zero when the result
// is empty). Used by paginated paths that previously fired a separate
// SELECT COUNT(*) before the data query.
func scanItemsWithTotal(rows pgx.Rows) ([]*models.MediaItem, int, error) {
	var (
		items []*models.MediaItem
		total int
	)
	for rows.Next() {
		var item models.MediaItem
		var rowTotal int
		dests := append(listItemScanDests(&item), &rowTotal)
		if err := rows.Scan(dests...); err != nil {
			return nil, 0, fmt.Errorf("scanning media item row with total: %w", err)
		}
		items = append(items, &item)
		total = rowTotal
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating media item rows: %w", err)
	}
	return items, total, nil
}

// Upsert inserts a new media item or updates all mutable fields if the
// content_id already exists. The created_at timestamp is preserved on update.
func (r *ItemRepository) Upsert(ctx context.Context, item *models.MediaItem) error {
	if r.searchIndexEvents.disabledByActiveProvider() {
		return r.upsert(ctx, r.pool, item)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin media item upsert tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := r.upsert(ctx, tx, item); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit media item upsert tx: %w", err)
	}
	return nil
}

// UpsertTx inserts or updates a media item using the caller's transaction.
func (r *ItemRepository) UpsertTx(ctx context.Context, tx pgx.Tx, item *models.MediaItem) error {
	return r.upsert(ctx, tx, item)
}

func (r *ItemRepository) upsert(ctx context.Context, execer itemExecer, item *models.MediaItem) error {
	if item.ContentID == "" {
		return fmt.Errorf("refusing to upsert media item with empty content_id")
	}
	studios := nonNilStringSlice(item.Studios)
	networks := nonNilStringSlice(item.Networks)
	countries := nonNilStringSlice(item.Countries)
	keywords := nonNilStringSlice(item.Keywords)
	query := `
		INSERT INTO media_items (
			content_id, type, title, sort_title, default_metadata_language, original_title, year, genres,
			content_rating, runtime, overview, tagline,
			rating_imdb, rating_tmdb, rating_rt_critic, rating_rt_audience,
			imdb_id, tmdb_id, tvdb_id,
			poster_path, poster_source_path, poster_thumbhash, backdrop_path, backdrop_source_path, backdrop_thumbhash, logo_path, logo_source_path,
			metadata_s3_path, metadata_etag, season_count,
			studios, networks, countries, keywords, original_language, release_date, first_air_date, last_air_date, air_time, air_timezone,
			show_status,
			matched_at, last_refreshed, refresh_failures,
			episode_metadata_incomplete, episode_metadata_last_checked_at, status
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12,
			$13, $14, $15, $16,
			$17, $18, $19,
			$20, $21, $22, $23, $24, $25, $26, $27,
			$28, $29, $30,
			$31, $32, $33, $34, $35, $36, $37, $38, $39, $40,
			$41,
			$42, $43, $44,
			$45, $46, $47
		)
		ON CONFLICT (content_id) DO UPDATE SET
			type = EXCLUDED.type,
			title = EXCLUDED.title,
			sort_title = EXCLUDED.sort_title,
			default_metadata_language = COALESCE(NULLIF(EXCLUDED.default_metadata_language, ''), media_items.default_metadata_language),
			original_title = EXCLUDED.original_title,
			year = EXCLUDED.year,
			genres = EXCLUDED.genres,
			content_rating = EXCLUDED.content_rating,
			runtime = EXCLUDED.runtime,
			overview = EXCLUDED.overview,
			tagline = EXCLUDED.tagline,
			rating_imdb = EXCLUDED.rating_imdb,
			rating_tmdb = EXCLUDED.rating_tmdb,
			rating_rt_critic = EXCLUDED.rating_rt_critic,
			rating_rt_audience = EXCLUDED.rating_rt_audience,
			imdb_id = EXCLUDED.imdb_id,
			tmdb_id = EXCLUDED.tmdb_id,
			tvdb_id = EXCLUDED.tvdb_id,
			poster_path = EXCLUDED.poster_path,
			poster_source_path = EXCLUDED.poster_source_path,
			poster_thumbhash = EXCLUDED.poster_thumbhash,
			backdrop_path = EXCLUDED.backdrop_path,
			backdrop_source_path = EXCLUDED.backdrop_source_path,
			backdrop_thumbhash = EXCLUDED.backdrop_thumbhash,
			logo_path = EXCLUDED.logo_path,
			logo_source_path = EXCLUDED.logo_source_path,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			season_count = EXCLUDED.season_count,
			studios = EXCLUDED.studios,
			networks = EXCLUDED.networks,
			countries = EXCLUDED.countries,
			keywords = EXCLUDED.keywords,
			original_language = EXCLUDED.original_language,
			release_date = EXCLUDED.release_date,
			first_air_date = EXCLUDED.first_air_date,
			last_air_date = EXCLUDED.last_air_date,
			air_time = EXCLUDED.air_time,
			air_timezone = EXCLUDED.air_timezone,
			show_status = EXCLUDED.show_status,
			matched_at = EXCLUDED.matched_at,
			last_refreshed = EXCLUDED.last_refreshed,
			refresh_failures = EXCLUDED.refresh_failures,
			episode_metadata_incomplete = EXCLUDED.episode_metadata_incomplete,
			episode_metadata_last_checked_at = EXCLUDED.episode_metadata_last_checked_at,
			status = EXCLUDED.status,
			updated_at = NOW()`

	_, err := execer.Exec(ctx, query,
		item.ContentID,
		item.Type,
		item.Title,
		item.SortTitle,
		item.DefaultMetadataLanguage,
		item.OriginalTitle,
		item.Year,
		item.Genres,
		item.ContentRating,
		item.Runtime,
		item.Overview,
		item.Tagline,
		item.RatingIMDB,
		item.RatingTMDB,
		item.RatingRTCritic,
		item.RatingRTAudience,
		item.ImdbID,
		item.TmdbID,
		item.TvdbID,
		item.PosterPath,
		item.PosterSourcePath,
		item.PosterThumbhash,
		item.BackdropPath,
		item.BackdropSourcePath,
		item.BackdropThumbhash,
		item.LogoPath,
		item.LogoSourcePath,
		item.MetadataS3Path,
		item.MetadataEtag,
		item.SeasonCount,
		studios,
		networks,
		countries,
		keywords,
		item.OriginalLanguage,
		item.ReleaseDate,
		item.FirstAirDate,
		item.LastAirDate,
		item.AirTime,
		item.AirTimezone,
		item.ShowStatus,
		item.MatchedAt,
		item.LastRefreshed,
		item.RefreshFailures,
		item.EpisodeMetadataIncomplete,
		item.EpisodeMetadataLastCheckedAt,
		item.Status,
	)
	if err != nil {
		return fmt.Errorf("upserting media item: %w", err)
	}

	if err := r.searchIndexEvents.EnqueueUpsert(ctx, execer, item.ContentID); err != nil {
		return fmt.Errorf("enqueueing catalog search upsert: %w", err)
	}

	return nil
}

func nonNilStringSlice(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// virtualPlaybackItemURI returns the stable provider-neutral handle used for
// collection-created virtual files. IMDb remains the preferred streaming
// identity, while TVDB (series only) and TMDB keep otherwise valid catalog
// entries materializable until metadata enrichment supplies an IMDb ID.
func virtualPlaybackItemURI(item *models.MediaItem) (string, error) {
	if item == nil {
		return "", errors.New("virtual playback item is required")
	}
	mediaType := strings.ToLower(strings.TrimSpace(item.Type))
	if mediaType == "" {
		mediaType = "movie"
	}
	if mediaType != "movie" && mediaType != "series" {
		return "", fmt.Errorf("virtual playback item has unsupported media type %q", mediaType)
	}
	if imdbID := strings.ToLower(strings.TrimSpace(item.ImdbID)); imdbIDPattern.MatchString(imdbID) {
		return "virtual://" + mediaType + "/" + imdbID, nil
	}
	if mediaType == "series" {
		if tvdbID := strings.TrimSpace(item.TvdbID); numericProviderIDPattern.MatchString(tvdbID) {
			return "virtual://series/tvdb/" + tvdbID, nil
		}
	}
	if tmdbID := strings.TrimSpace(item.TmdbID); numericProviderIDPattern.MatchString(tmdbID) {
		return "virtual://" + mediaType + "/tmdb/" + tmdbID, nil
	}
	return "", errors.New("virtual playback item requires a valid IMDb, TVDB, or TMDB ID")
}

func virtualPlaybackContentID(item *models.MediaItem) (string, error) {
	if item == nil {
		return "", errors.New("virtual playback item is required")
	}
	mediaType := strings.ToLower(strings.TrimSpace(item.Type))
	if mediaType != "movie" && mediaType != "series" {
		return "", fmt.Errorf("virtual playback item has unsupported media type %q", mediaType)
	}
	imdbID := strings.ToLower(strings.TrimSpace(item.ImdbID))
	if imdbID != "" && !imdbIDPattern.MatchString(imdbID) {
		imdbID = ""
	}
	tmdbID := strings.TrimSpace(item.TmdbID)
	if tmdbID != "" && !numericProviderIDPattern.MatchString(tmdbID) {
		tmdbID = ""
	}
	tvdbID := strings.TrimSpace(item.TvdbID)
	if tvdbID != "" && !numericProviderIDPattern.MatchString(tvdbID) {
		tvdbID = ""
	}
	in := VirtualMedia{
		MediaType: mediaType,
		IMDbID:    imdbID,
		TMDBID:    tmdbID,
		TVDBID:    tvdbID,
	}
	if mediaType == "movie" {
		in.TVDBID = ""
	}
	contentID := virtualContentID(in)
	if strings.HasSuffix(contentID, "-imdb-") {
		return "", errors.New("virtual playback item requires a valid canonical external ID")
	}
	return contentID, nil
}

// MaterializeVirtualPlaybackItemWithVariants creates the base virtual source
// and any configured profile variants without contacting a streaming provider.
func (r *ItemRepository) MaterializeVirtualPlaybackItemWithVariants(ctx context.Context, item *models.MediaItem, libraryIDs []int, variants []VirtualPlaybackVariant) (bool, error) {
	if item == nil || strings.TrimSpace(item.ContentID) == "" {
		return false, errors.New("virtual playback item requires a content ID")
	}
	virtualPath, err := virtualPlaybackItemURI(item)
	if err != nil {
		return false, err
	}
	if len(libraryIDs) == 0 {
		return false, fmt.Errorf("virtual playback item requires at least one library")
	}
	mediaType := strings.TrimSpace(item.Type)
	if mediaType == "" {
		mediaType = "movie"
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin virtual item transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, item.ContentID); err != nil {
		return false, fmt.Errorf("lock canonical virtual item: %w", err)
	}
	// Mixed collections can target both libraries. A virtual file still has a
	// single owning folder, so select it from the item's type instead of using
	// the first collection target.
	wantedFolderType := "movies"
	if mediaType == "series" {
		wantedFolderType = "series"
	}
	var folderID int
	if err := tx.QueryRow(ctx, `
		SELECT id FROM media_folders
		WHERE id = ANY($1::int[]) AND enabled IS NOT FALSE AND type IN ($2, 'mixed')
		ORDER BY CASE WHEN type = $2 THEN 0 ELSE 1 END,
		         array_position($1::int[], id)
		LIMIT 1`, libraryIDs, wantedFolderType).Scan(&folderID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if fbErr := tx.QueryRow(ctx, `
				SELECT id FROM media_folders
				WHERE enabled IS NOT FALSE AND type IN ($1, 'mixed')
				ORDER BY sort_order, id
				LIMIT 1`, wantedFolderType).Scan(&folderID); fbErr != nil {
				return false, fmt.Errorf("virtual playback item has no compatible %s library among selected targets", mediaType)
			}
		} else {
			return false, fmt.Errorf("selecting virtual playback library: %w", err)
		}
	}
	created := false
	var existingType string
	err = tx.QueryRow(ctx, `
		SELECT type FROM media_items WHERE content_id=$1 FOR UPDATE`,
		item.ContentID,
	).Scan(&existingType)
	switch {
	case err == nil:
		if existingType != mediaType {
			return false, fmt.Errorf("canonical virtual content ID belongs to media type %q", existingType)
		}
	case errors.Is(err, pgx.ErrNoRows):
		if err := r.UpsertTx(ctx, tx, item); err != nil {
			return false, err
		}
		created = true
	default:
		return false, fmt.Errorf("checking canonical virtual item: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, NOW()) ON CONFLICT DO NOTHING`, item.ContentID, folderID); err != nil {
		return false, fmt.Errorf("linking virtual item to library: %w", err)
	}
	ownerInstallationID := 0
	ownerIDs := make([]int64, 0, len(variants))
	seenOwners := make(map[int]struct{}, len(variants))
	for _, variant := range variants {
		if variant.OwnerInstallationID > 0 {
			if ownerInstallationID == 0 {
				ownerInstallationID = variant.OwnerInstallationID
			}
			if _, exists := seenOwners[variant.OwnerInstallationID]; !exists {
				seenOwners[variant.OwnerInstallationID] = struct{}{}
				ownerIDs = append(ownerIDs, int64(variant.OwnerInstallationID))
			}
		}
	}
	if ownerInstallationID <= 0 {
		return false, errors.New("virtual playback item requires an owning provider installation")
	}
	desiredPaths := make([]string, 0, len(variants)+len(ownerIDs))
	desiredOwners := make([]int64, 0, len(variants)+len(ownerIDs))
	for _, ownerID := range ownerIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_files (
				content_id, media_folder_id, file_path, file_size, container,
				probe_source, virtual_owner_installation_id
			)
			VALUES ($1, $2, $3, 0, 'virtual', 'virtual_collection', $4)
			ON CONFLICT (file_path, virtual_owner_installation_id, media_folder_id)
				WHERE virtual_owner_installation_id IS NOT NULL
			DO UPDATE SET
				content_id=EXCLUDED.content_id,
				probe_source='virtual_collection',
				missing_since=NULL,
				updated_at=NOW()`,
			item.ContentID, folderID, virtualPath, ownerID); err != nil {
			return false, fmt.Errorf("creating virtual playback file: %w", err)
		}
		desiredPaths = append(desiredPaths, virtualPath)
		desiredOwners = append(desiredOwners, ownerID)
	}
	for _, variant := range variants {
		path := strings.TrimSpace(variant.VirtualURI)
		if path == "" || path == virtualPath || !strings.HasPrefix(path, "virtual://") {
			continue
		}
		variantOwnerID := variant.OwnerInstallationID
		if variantOwnerID <= 0 {
			variantOwnerID = ownerInstallationID
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_files (
				content_id, media_folder_id, file_path, file_size, resolution,
				codec_video, codec_audio, hdr, container, probe_source,
				virtual_owner_installation_id
			) VALUES (
				$1, $2, $3, 0, NULLIF($4,''), NULLIF($5,''), NULLIF($6,''),
				CASE WHEN NULLIF($7,'') IS NULL THEN false ELSE true END,
				'virtual', 'virtual_collection', $8
			)
			ON CONFLICT (file_path, virtual_owner_installation_id, media_folder_id)
				WHERE virtual_owner_installation_id IS NOT NULL
			DO UPDATE SET
				content_id=EXCLUDED.content_id,
				media_folder_id=EXCLUDED.media_folder_id,
				resolution=EXCLUDED.resolution,
				codec_video=EXCLUDED.codec_video,
				codec_audio=EXCLUDED.codec_audio,
				hdr=EXCLUDED.hdr,
				probe_source='virtual_collection',
				missing_since=NULL,
				updated_at=NOW()`,
			item.ContentID, folderID, path, variant.Resolution, variant.CodecVideo,
			variant.CodecAudio, variant.HDR, variantOwnerID); err != nil {
			return false, fmt.Errorf("creating virtual playback variant: %w", err)
		}
		desiredPaths = append(desiredPaths, path)
		desiredOwners = append(desiredOwners, int64(variantOwnerID))
	}
	if _, err := tx.Exec(ctx, `
		WITH desired(owner_id,file_path) AS (
			SELECT * FROM unnest($3::bigint[],$4::text[])
		), stale AS (
			SELECT mf.content_id,mf.media_folder_id,mf.file_path,mf.virtual_owner_installation_id
			FROM media_files mf
			WHERE mf.content_id=$1
			  AND mf.media_folder_id=$2
			  AND mf.container='virtual'
			  AND mf.probe_source='virtual_collection'
			  AND mf.virtual_owner_installation_id IS NOT NULL
			  AND NOT EXISTS (
			      SELECT 1 FROM desired
			      WHERE desired.owner_id=mf.virtual_owner_installation_id
			        AND desired.file_path=mf.file_path
			  )
		)
		DELETE FROM virtual_media_file_source_claims claim
		USING stale
		WHERE claim.content_id=stale.content_id
		  AND claim.media_folder_id=stale.media_folder_id
		  AND claim.file_path=stale.file_path
		  AND claim.plugin_installation_id=stale.virtual_owner_installation_id
		  AND (claim.source_key='collection' OR left(claim.source_key,11)='collection:')`,
		item.ContentID, folderID, desiredOwners, desiredPaths); err != nil {
		return false, fmt.Errorf("releasing stale collection virtual file claims: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		WITH desired(owner_id,file_path) AS (
			SELECT * FROM unnest($3::bigint[],$4::text[])
		)
		DELETE FROM media_files mf
		WHERE mf.content_id=$1
		  AND mf.media_folder_id=$2
		  AND mf.container='virtual'
		  AND mf.probe_source='virtual_collection'
		  AND mf.virtual_owner_installation_id IS NOT NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM desired
		      WHERE desired.owner_id=mf.virtual_owner_installation_id
		        AND desired.file_path=mf.file_path
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims claim
		      WHERE claim.content_id=mf.content_id
		        AND claim.media_folder_id=mf.media_folder_id
		        AND claim.file_path=mf.file_path
		        AND claim.plugin_installation_id=mf.virtual_owner_installation_id
		  )`, item.ContentID, folderID, desiredOwners, desiredPaths); err != nil {
		return false, fmt.Errorf("removing stale collection virtual files: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM virtual_media_source_claims claim
		WHERE claim.content_id=$1
		  AND claim.media_folder_id=$2
		  AND (claim.source_key='collection' OR left(claim.source_key,11)='collection:')
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims file_claim
		      WHERE file_claim.plugin_installation_id=claim.plugin_installation_id
		        AND file_claim.source_key=claim.source_key
		        AND file_claim.content_id=claim.content_id
		        AND file_claim.media_folder_id=claim.media_folder_id
		  )`, item.ContentID, folderID); err != nil {
		return false, fmt.Errorf("removing empty collection virtual source claims: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE media_items
		SET virtual_source = 'collection', virtual_last_seen_at = NOW(), updated_at = NOW()
		WHERE content_id = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      WHERE mf.content_id=media_items.content_id
		        AND mf.container<>'virtual'
		        AND mf.file_path NOT LIKE 'virtual://%'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_source_claims claim
		      WHERE claim.content_id=media_items.content_id
		  )`, item.ContentID); err != nil {
		return false, fmt.Errorf("marking collection virtual item ownership: %w", err)
	}
	// Virtual items are database-only and therefore never pass through the
	// scanner's normal metadata-refresh enqueue path.  Queue core metadata
	// enrichment in the same transaction so a newly materialized item gets
	// artwork, synopsis, ratings, and any missing provider IDs automatically.
	if _, err := tx.Exec(ctx, `
		INSERT INTO metadata_refresh_debt (
			target_type, content_id, priority, reason_mask, next_refresh_at, updated_at
		) VALUES ('item', $1, 150, 8, NOW(), NOW())
		ON CONFLICT (target_type, content_id) DO UPDATE SET
			priority = GREATEST(metadata_refresh_debt.priority, EXCLUDED.priority),
			reason_mask = metadata_refresh_debt.reason_mask | EXCLUDED.reason_mask,
			next_refresh_at = LEAST(metadata_refresh_debt.next_refresh_at, EXCLUDED.next_refresh_at),
			updated_at = NOW()`, item.ContentID); err != nil {
		return false, fmt.Errorf("queueing virtual item metadata refresh: %w", err)
	}
	if mediaType == "series" {
		if err := materializeVirtualPlaybackEpisodesTx(ctx, tx, item.ContentID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit virtual item transaction: %w", err)
	}
	return created, nil
}

func (r *ItemRepository) CleanupUnreferencedCollectionVirtualItems(ctx context.Context, candidateIDs []string) (int64, error) {
	if r == nil || r.pool == nil || len(candidateIDs) == 0 {
		return 0, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin failed collection cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE failed_collection_virtual_folders ON COMMIT DROP AS
		WITH removed AS (
			DELETE FROM media_files mf
			WHERE mf.content_id=ANY($1::text[])
			  AND mf.container='virtual'
			  AND mf.probe_source='virtual_collection'
			  AND NOT EXISTS (
			      SELECT 1 FROM virtual_media_file_source_claims claim
			      WHERE claim.plugin_installation_id=mf.virtual_owner_installation_id
			        AND claim.content_id=mf.content_id
			        AND claim.media_folder_id=mf.media_folder_id
			        AND claim.file_path=mf.file_path
			  )
			RETURNING content_id,media_folder_id
		)
		SELECT DISTINCT content_id,media_folder_id FROM removed`, candidateIDs); err != nil {
		return 0, fmt.Errorf("remove failed collection virtual files: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM media_item_libraries membership
		USING failed_collection_virtual_folders removed
		WHERE membership.content_id=removed.content_id
		  AND membership.media_folder_id=removed.media_folder_id
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files remaining
		      WHERE remaining.content_id=membership.content_id
		        AND remaining.media_folder_id=membership.media_folder_id
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM library_collection_items collection_item
		      JOIN library_collection_libraries collection_library
		        ON collection_library.collection_id=collection_item.collection_id
		      WHERE collection_item.media_item_id=membership.content_id
		        AND collection_library.library_id=membership.media_folder_id
		  )`); err != nil {
		return 0, fmt.Errorf("remove failed collection library links: %w", err)
	}
	rows, err := tx.Query(ctx, `
		DELETE FROM media_items mi
		WHERE mi.content_id=ANY($1::text[])
		  AND (mi.virtual_source='collection' OR left(mi.virtual_source,11)='collection:')
		  AND NOT EXISTS (
		      SELECT 1 FROM library_collection_items lci
		      WHERE lci.media_item_id=mi.content_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      WHERE mf.content_id=mi.content_id
		        AND mf.container<>'virtual'
		        AND mf.file_path NOT LIKE 'virtual://%'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_source_claims claim
		      WHERE claim.content_id=mi.content_id
		  )
		RETURNING mi.content_id`, candidateIDs)
	if err != nil {
		return 0, fmt.Errorf("delete failed collection virtual items: %w", err)
	}
	deletedIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("collect failed collection virtual items: %w", err)
	}
	if err := EnqueueSearchIndexDeletes(ctx, tx, deletedIDs); err != nil {
		return 0, fmt.Errorf("enqueue failed collection search deletes: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit failed collection cleanup: %w", err)
	}
	return int64(len(deletedIDs)), nil
}

// MaterializeVirtualPlaybackEpisodes attaches released episode rows to every
// configured provider/profile placeholder for a collection-created series.
// It performs no provider lookup; stream URLs remain strictly just-in-time.
func (r *ItemRepository) MaterializeVirtualPlaybackEpisodes(ctx context.Context, seriesID string) error {
	if r == nil || r.pool == nil {
		return errors.New("item repository is not configured")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin virtual episode materialization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := materializeVirtualPlaybackEpisodesTx(ctx, tx, seriesID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit virtual episode materialization: %w", err)
	}
	return nil
}

// ReconcileReleasedCollectionVirtualEpisodes materializes newly released
// episode placeholders for a bounded set of collection-created series. It
// only reads catalog metadata and writes provider-neutral virtual handles; it
// never resolves or prewarms a provider URL.
func (r *ItemRepository) ReconcileReleasedCollectionVirtualEpisodes(ctx context.Context, limit int) (int, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("item repository is not configured")
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := r.pool.Query(ctx, `
		WITH bases AS (
			SELECT content_id,count(*) AS base_count
			FROM media_files
			WHERE episode_id IS NULL
			  AND container='virtual'
			  AND probe_source IN ('virtual_collection', 'virtual')
			  AND file_path LIKE 'virtual://series/%'
			  AND virtual_owner_installation_id IS NOT NULL
			GROUP BY content_id
		), episode_file_counts AS (
			SELECT mf.content_id,mf.episode_id,count(*) AS file_count
			FROM media_files mf
			JOIN bases ON bases.content_id=mf.content_id
			WHERE mf.episode_id IS NOT NULL
			  AND mf.container='virtual'
			  AND mf.probe_source IN ('virtual_collection', 'virtual')
			GROUP BY mf.content_id,mf.episode_id
		), needs_reconciliation AS (
			SELECT bases.content_id
			FROM bases
			JOIN episodes ep ON ep.series_id=bases.content_id
			LEFT JOIN episode_file_counts files
			  ON files.content_id=bases.content_id AND files.episode_id=ep.content_id
			WHERE ep.season_number>0
			  AND ep.episode_number>0
			  AND ep.air_date IS NOT NULL
			  AND ep.air_date<=CURRENT_DATE
			  AND COALESCE(files.file_count,0)<bases.base_count
			UNION
			SELECT bases.content_id
			FROM bases
			JOIN episodes ep ON ep.series_id=bases.content_id
			JOIN episode_file_counts files
			  ON files.content_id=bases.content_id AND files.episode_id=ep.content_id
			WHERE ep.air_date IS NULL OR ep.air_date>CURRENT_DATE
		)
		SELECT content_id
		FROM needs_reconciliation
		ORDER BY content_id
		LIMIT $1`, limit)
	if err != nil {
		return 0, fmt.Errorf("list collection series needing episode reconciliation: %w", err)
	}
	seriesIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("collect collection series needing episode reconciliation: %w", err)
	}
	reconciled := 0
	var reconcileErr error
	for _, seriesID := range seriesIDs {
		if err := r.MaterializeVirtualPlaybackEpisodes(ctx, seriesID); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("%s: %w", seriesID, err))
			continue
		}
		reconciled++
	}
	return reconciled, reconcileErr
}

type collectionVirtualSeriesBase struct {
	folderID    int
	ownerID     int
	filePath    string
	resolution  string
	codecVideo  string
	codecAudio  string
	hdr         bool
	editionRaw  string
	probeSource string
}

func materializeVirtualPlaybackEpisodesTx(ctx context.Context, tx pgx.Tx, seriesID string) error {
	rows, err := tx.Query(ctx, `
		SELECT media_folder_id, virtual_owner_installation_id, file_path,
		       COALESCE(resolution,''), COALESCE(codec_video,''),
		       COALESCE(codec_audio,''), COALESCE(hdr,false), COALESCE(edition_raw,''),
		       COALESCE(probe_source,'virtual_collection')
		FROM media_files
		WHERE content_id=$1
		  AND episode_id IS NULL
		  AND container='virtual'
		  AND probe_source IN ('virtual_collection', 'virtual')
		  AND file_path LIKE 'virtual://series/%'
		  AND virtual_owner_installation_id IS NOT NULL
		ORDER BY id`, seriesID)
	if err != nil {
		return fmt.Errorf("list virtual series profiles: %w", err)
	}
	var bases []collectionVirtualSeriesBase
	for rows.Next() {
		var base collectionVirtualSeriesBase
		if err := rows.Scan(
			&base.folderID, &base.ownerID, &base.filePath, &base.resolution,
			&base.codecVideo, &base.codecAudio, &base.hdr, &base.editionRaw, &base.probeSource,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan virtual series profile: %w", err)
		}
		parsed, parseErr := url.Parse(base.filePath)
		if parseErr != nil || parsed.Scheme != "virtual" || parsed.Host != "series" ||
			strings.TrimSpace(parsed.Query().Get("result")) != "" {
			continue
		}
		bases = append(bases, base)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate virtual series profiles: %w", err)
	}
	rows.Close()
	if len(bases) == 0 {
		return nil
	}

	episodeRows, err := tx.Query(ctx, `
		SELECT content_id, season_number, episode_number
		FROM episodes
		WHERE series_id=$1
		  AND season_number > 0
		  AND episode_number > 0
		  AND (air_date IS NULL OR air_date <= CURRENT_DATE)
		ORDER BY season_number, episode_number`, seriesID)
	if err != nil {
		return fmt.Errorf("list released virtual episodes: %w", err)
	}
	type episodeCoordinate struct {
		id      string
		season  int
		episode int
	}
	var episodes []episodeCoordinate
	for episodeRows.Next() {
		var episode episodeCoordinate
		if err := episodeRows.Scan(&episode.id, &episode.season, &episode.episode); err != nil {
			episodeRows.Close()
			return fmt.Errorf("scan released virtual episode: %w", err)
		}
		episodes = append(episodes, episode)
	}
	if err := episodeRows.Err(); err != nil {
		episodeRows.Close()
		return fmt.Errorf("iterate released virtual episodes: %w", err)
	}
	episodeRows.Close()

	if len(episodes) == 0 {
		seasonID := fmt.Sprintf("%s-1", strings.Replace(seriesID, "series-", "season-", 1))
		episodeID := fmt.Sprintf("%s-1-1", strings.Replace(seriesID, "series-", "episode-", 1))
		if _, err := tx.Exec(ctx, `
			INSERT INTO seasons(content_id,series_id,season_number,title,metadata_source)
			VALUES($1,$2,1,'Season 1','provider') ON CONFLICT DO NOTHING`, seasonID, seriesID); err != nil {
			return fmt.Errorf("create default virtual season: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO episodes(content_id,series_id,season_id,season_number,episode_number,title,metadata_source)
			VALUES($1,$2,$3,1,1,'Episode 1','provider') ON CONFLICT DO NOTHING`, episodeID, seriesID, seasonID); err != nil {
			return fmt.Errorf("create default virtual episode: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO episode_libraries(episode_id,media_folder_id)
			SELECT $1, media_folder_id FROM media_files WHERE content_id=$2 AND episode_id IS NULL
			ON CONFLICT DO NOTHING`, episodeID, seriesID); err != nil {
			return fmt.Errorf("link default virtual episode: %w", err)
		}
		episodes = append(episodes, episodeCoordinate{id: episodeID, season: 1, episode: 1})
	}

	expectedPaths := make([]string, 0, len(bases)*len(episodes))
	expectedOwners := make([]int64, 0, len(bases)*len(episodes))
	for _, base := range bases {
		parsed, _ := url.Parse(base.filePath)
		identifier := strings.TrimPrefix(parsed.EscapedPath(), "/")
		for _, episode := range episodes {
			episodeURI := &url.URL{
				Scheme:   "virtual",
				Host:     "series",
				Path:     fmt.Sprintf("/%s/%d/%d", identifier, episode.season, episode.episode),
				RawQuery: parsed.RawQuery,
			}
			path := episodeURI.String()
			expectedPaths = append(expectedPaths, path)
			expectedOwners = append(expectedOwners, int64(base.ownerID))
			ps := base.probeSource
			if ps == "" {
				ps = "virtual_collection"
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO media_files(
					content_id,episode_id,media_folder_id,file_path,file_size,
					resolution,codec_video,codec_audio,hdr,container,edition_raw,
					season_number,episode_number,probe_source,
					virtual_owner_installation_id
				) VALUES(
					$1,$2,$3,$4,0,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),
					$8,'virtual',$9,$10,$11,$12,$13
				)
				ON CONFLICT(file_path,virtual_owner_installation_id,media_folder_id)
					WHERE virtual_owner_installation_id IS NOT NULL
				DO UPDATE SET
					content_id=EXCLUDED.content_id,
					episode_id=EXCLUDED.episode_id,
					media_folder_id=EXCLUDED.media_folder_id,
					resolution=EXCLUDED.resolution,
					codec_video=EXCLUDED.codec_video,
					codec_audio=EXCLUDED.codec_audio,
					hdr=EXCLUDED.hdr,
					edition_raw=EXCLUDED.edition_raw,
					season_number=EXCLUDED.season_number,
					episode_number=EXCLUDED.episode_number,
					probe_source=EXCLUDED.probe_source,
					missing_since=NULL,
					updated_at=NOW()`,
				seriesID, episode.id, base.folderID, path, base.resolution,
				base.codecVideo, base.codecAudio, base.hdr, base.editionRaw,
				episode.season, episode.episode, ps, base.ownerID,
			); err != nil {
				return fmt.Errorf("upsert released virtual episode: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO virtual_media_file_source_claims(
					plugin_installation_id,source_key,content_id,media_folder_id,
					file_path,last_seen_at,updated_at
				)
				SELECT claim.plugin_installation_id,claim.source_key,$1,$2,$3,NOW(),NOW()
				FROM virtual_media_file_source_claims claim
				WHERE claim.plugin_installation_id=$4
				  AND claim.content_id=$1
				  AND claim.media_folder_id=$2
				  AND claim.file_path=$5
				  AND (claim.source_key='collection' OR left(claim.source_key,11)='collection:' OR claim.source_key='request' OR left(claim.source_key,8)='request:' OR claim.source_key='virtual' OR left(claim.source_key,8)='virtual:')
				ON CONFLICT(plugin_installation_id,source_key,content_id,media_folder_id,file_path)
				DO UPDATE SET last_seen_at=EXCLUDED.last_seen_at,updated_at=NOW()`,
				seriesID, base.folderID, path, base.ownerID, base.filePath,
			); err != nil {
				return fmt.Errorf("claim released virtual episode: %w", err)
			}
		}
	}
	if expectedPaths == nil {
		expectedPaths = []string{}
	}
	if expectedOwners == nil {
		expectedOwners = []int64{}
	}
	if _, err := tx.Exec(ctx, `
		WITH expected(owner_id,file_path) AS (
			SELECT * FROM unnest($2::bigint[],$3::text[])
		), stale AS (
			SELECT mf.content_id,mf.media_folder_id,mf.file_path,mf.virtual_owner_installation_id
			FROM media_files mf
			JOIN episodes ep ON ep.content_id=mf.episode_id
			WHERE ep.series_id=$1
			  AND mf.container='virtual'
			  AND mf.probe_source IN ('virtual_collection', 'virtual')
			  AND mf.virtual_owner_installation_id IS NOT NULL
			  AND NOT EXISTS (
			      SELECT 1 FROM expected
			      WHERE expected.owner_id=mf.virtual_owner_installation_id
			        AND expected.file_path=mf.file_path
			  )
		)
		DELETE FROM virtual_media_file_source_claims claim
		USING stale
		WHERE claim.plugin_installation_id=stale.virtual_owner_installation_id
		  AND claim.content_id=stale.content_id
		  AND claim.media_folder_id=stale.media_folder_id
		  AND claim.file_path=stale.file_path
		  AND (claim.source_key='collection' OR left(claim.source_key,11)='collection:' OR claim.source_key='request' OR left(claim.source_key,8)='request:' OR claim.source_key='virtual' OR left(claim.source_key,8)='virtual:')`, seriesID, expectedOwners, expectedPaths); err != nil {
		return fmt.Errorf("remove stale virtual episode claims: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		WITH expected(owner_id,file_path) AS (
			SELECT * FROM unnest($2::bigint[],$3::text[])
		)
		DELETE FROM media_files mf
		USING episodes ep
		WHERE mf.episode_id=ep.content_id
		  AND ep.series_id=$1
		  AND mf.container='virtual'
		  AND mf.probe_source IN ('virtual_collection', 'virtual')
		  AND mf.virtual_owner_installation_id IS NOT NULL
		  AND NOT EXISTS (
			      SELECT 1 FROM expected
			      WHERE expected.owner_id=mf.virtual_owner_installation_id
			        AND expected.file_path=mf.file_path
			  )
		  AND NOT EXISTS (
			      SELECT 1 FROM virtual_media_file_source_claims claim
			      WHERE claim.plugin_installation_id=mf.virtual_owner_installation_id
			        AND claim.content_id=mf.content_id
			        AND claim.media_folder_id=mf.media_folder_id
			        AND claim.file_path=mf.file_path
			  )`, seriesID, expectedOwners, expectedPaths); err != nil {
		return fmt.Errorf("remove stale virtual episodes: %w", err)
	}
	return nil
}

// GetByID retrieves a media item by its content ID.
func (r *ItemRepository) GetByID(ctx context.Context, contentID string) (*models.MediaItem, error) {
	query := `SELECT ` + itemColumns + ` FROM media_items WHERE content_id = $1`
	return scanItem(r.pool.QueryRow(ctx, query, contentID))
}

// GetByIDs retrieves multiple media items by their content IDs.
// Items not found are silently omitted from the result.
func (r *ItemRepository) GetByIDs(ctx context.Context, contentIDs []string) ([]*models.MediaItem, error) {
	if len(contentIDs) == 0 {
		return []*models.MediaItem{}, nil
	}

	query := `SELECT ` + itemColumns + ` FROM media_items WHERE content_id = ANY($1) ORDER BY content_id ASC`
	rows, err := r.pool.Query(ctx, query, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("fetching media items by IDs: %w", err)
	}
	defer rows.Close()

	return scanItems(rows)
}

// GetStatusByIDs returns a map of content_id → status for the requested IDs.
// Missing IDs are absent from the result rather than returned with empty values.
func (r *ItemRepository) GetStatusByIDs(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT content_id, status FROM media_items WHERE content_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("querying media_items statuses: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string, len(ids))
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, fmt.Errorf("scanning status row: %w", err)
		}
		out[id] = status
	}
	return out, rows.Err()
}

// GetByIDsWithAccess fetches multiple media items by content_id, filtered by
// the access policy in a single query. Returns only items the viewer is
// allowed to see — replaces a per-item EnsureAccessible loop alongside the
// existing batch GetByIDs (audit 2026-05-01 §3.3).
//
// Library access uses the shared per-item EXISTS / NOT EXISTS predicates
// (libraryAccessConditions), the same semantics EnsureAccessible and
// EnsureAccessibleIDs enforce.
func (r *ItemRepository) GetByIDsWithAccess(ctx context.Context, contentIDs []string, access AccessFilter) ([]*models.MediaItem, error) {
	if len(contentIDs) == 0 {
		return []*models.MediaItem{}, nil
	}
	if access.AllowedLibraryIDs != nil && len(access.AllowedLibraryIDs) == 0 {
		return []*models.MediaItem{}, nil
	}
	sql, args := r.buildGetByIDsWithAccessSQL(contentIDs, access)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("fetching media items by IDs with access: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

func (r *ItemRepository) buildGetByIDsWithAccessSQL(contentIDs []string, access AccessFilter) (string, []any) {
	sql := `SELECT ` + itemColumns + `
            FROM media_items mi
            WHERE mi.content_id = ANY($1)`
	args := []any{contentIDs}
	argIdx := 2

	var conditions []string
	appendLibraryAccessConditions("mi.content_id", access, &conditions, &args, &argIdx)
	applyAccessFilter("mi", AccessFilter{MaxContentRating: access.MaxContentRating, ExcludedMediaTypes: access.ExcludedMediaTypes}, &conditions, &args, &argIdx)
	for _, c := range conditions {
		sql += "\n            AND " + c
	}

	sql += " ORDER BY mi.content_id ASC"
	return sql, args
}

// GetOriginalLanguage returns the original_language for a media item by content ID.
// Returns empty string if the item is not found or has no original language.
func (r *ItemRepository) GetOriginalLanguage(ctx context.Context, contentID string) (string, error) {
	var lang string
	err := r.pool.QueryRow(ctx,
		`SELECT original_language FROM media_items WHERE content_id = $1`,
		contentID,
	).Scan(&lang)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("fetching original language for %s: %w", contentID, err)
	}
	return lang, nil
}

// GetByExternalID finds a media item matching any of the given external IDs
// and the specified item type. Checks TMDB ID first, then IMDB, then TVDB.
// Returns ErrItemNotFound if no match is found.
func (r *ItemRepository) GetByExternalID(ctx context.Context, tmdbID, imdbID, tvdbID, itemType string) (*models.MediaItem, error) {
	for _, check := range []struct{ col, val string }{
		{"tmdb_id", tmdbID},
		{"imdb_id", imdbID},
		{"tvdb_id", tvdbID},
	} {
		if check.val == "" {
			continue
		}
		query := fmt.Sprintf("SELECT %s FROM media_items WHERE %s = $1", itemColumns, check.col)
		args := []any{check.val}
		if itemType != "" {
			query += " AND type = $2"
			args = append(args, itemType)
		}
		query += `
			ORDER BY
				CASE lower(trim(status))
					WHEN 'matched' THEN 0
					WHEN 'pending' THEN 1
					WHEN 'unmatched' THEN 2
					ELSE 3
				END,
				updated_at DESC,
				content_id ASC
			LIMIT 1`
		item, err := scanItem(r.pool.QueryRow(ctx, query, args...))
		if err == nil {
			return item, nil
		}
		if !errors.Is(err, ErrItemNotFound) {
			return nil, err
		}
	}
	return nil, ErrItemNotFound
}

// ExternalIDBatch holds the external IDs to look up in a single batched
// query. Each slice may be nil/empty independently; the caller does not need
// to pre-pad with placeholders.
type ExternalIDBatch struct {
	TMDBIDs []string
	IMDbIDs []string
	TVDBIDs []string
}

// ExternalIDLookup maps from each external ID to its content_id, allowing the
// caller to dedup and choose a priority order (e.g. TMDB > IMDb > TVDB).
type ExternalIDLookup struct {
	ByTMDB map[string]string
	ByIMDb map[string]string
	ByTVDB map[string]string
}

// GetByExternalIDs fetches media items matching any of the given external IDs
// of the specified type, in a single query. Replaces N×3 GetByExternalID
// calls in MDBList collection sync (audit 2026-05-01 §3.7).
func (r *ItemRepository) GetByExternalIDs(ctx context.Context, batch ExternalIDBatch, itemType string) (*ExternalIDLookup, error) {
	if len(batch.TMDBIDs) == 0 && len(batch.IMDbIDs) == 0 && len(batch.TVDBIDs) == 0 {
		return &ExternalIDLookup{
			ByTMDB: map[string]string{},
			ByIMDb: map[string]string{},
			ByTVDB: map[string]string{},
		}, nil
	}
	sql, args := r.buildGetByExternalIDsSQL(batch, itemType)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("fetching media items by external IDs: %w", err)
	}
	defer rows.Close()

	out := &ExternalIDLookup{
		ByTMDB: map[string]string{},
		ByIMDb: map[string]string{},
		ByTVDB: map[string]string{},
	}
	for rows.Next() {
		var contentID, tmdb, imdb, tvdb string
		if err := rows.Scan(&contentID, &tmdb, &imdb, &tvdb); err != nil {
			return nil, fmt.Errorf("scanning external-ID row: %w", err)
		}
		if tmdb != "" {
			out.ByTMDB[tmdb] = contentID
		}
		if imdb != "" {
			out.ByIMDb[imdb] = contentID
		}
		if tvdb != "" {
			out.ByTVDB[tvdb] = contentID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating external-ID rows: %w", err)
	}
	return out, nil
}

// buildGetByExternalIDsSQL pins the exact SQL shape used by GetByExternalIDs:
// a single statement that ORs across all three external-ID arrays plus a
// type filter. The COALESCE wraps make scanning into string safe even when
// the column is NULL (imdb_id/tmdb_id/tvdb_id are nullable text on
// media_items per migration 001).
func (r *ItemRepository) buildGetByExternalIDsSQL(batch ExternalIDBatch, itemType string) (string, []any) {
	sql := `SELECT content_id, COALESCE(tmdb_id, ''), COALESCE(imdb_id, ''), COALESCE(tvdb_id, '')
            FROM media_items
            WHERE (tmdb_id = ANY($1) OR imdb_id = ANY($2) OR tvdb_id = ANY($3))
              AND type = $4`
	return sql, []any{batch.TMDBIDs, batch.IMDbIDs, batch.TVDBIDs, itemType}
}

// GetByTitleYearType finds a media item by exact title, year, and type match.
// Used for dedup when external IDs are not available.
// Returns ErrItemNotFound if no match.
func (r *ItemRepository) GetByTitleYearType(ctx context.Context, title string, year int, itemType string) (*models.MediaItem, error) {
	query := `SELECT ` + itemColumns + ` FROM media_items WHERE title = $1 AND year = $2 AND type = $3 LIMIT 1`
	return scanItem(r.pool.QueryRow(ctx, query, title, year, itemType))
}

// Delete removes a media item by its content ID and returns S3 image
// directory paths that should be cleaned up by the caller.
func (r *ItemRepository) Delete(ctx context.Context, contentID string) ([]string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin delete tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Collect image paths before deletion.
	imgRows, err := tx.Query(ctx, `
		SELECT poster_path, backdrop_path, logo_path FROM media_items WHERE content_id = $1
		UNION ALL
		SELECT poster_path, '', '' FROM seasons WHERE series_id = $1
		UNION ALL
		SELECT still_path, '', '' FROM episodes WHERE series_id = $1
	`, contentID)
	if err != nil {
		return nil, fmt.Errorf("collecting image paths before delete: %w", err)
	}
	var imageDirs []string
	for imgRows.Next() {
		var p1, p2, p3 string
		if err := imgRows.Scan(&p1, &p2, &p3); err != nil {
			imgRows.Close()
			return nil, fmt.Errorf("scanning image path: %w", err)
		}
		for _, p := range []string{p1, p2, p3} {
			if p != "" && !strings.Contains(p, "://") {
				if dir := imageDeletePrefix(p); dir != "" {
					imageDirs = append(imageDirs, dir)
				}
			}
		}
	}
	imgRows.Close()

	tag, err := tx.Exec(ctx, "DELETE FROM media_items WHERE content_id = $1", contentID)
	if err != nil {
		return nil, fmt.Errorf("deleting media item: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return nil, ErrItemNotFound
	}

	if err := r.searchIndexEvents.EnqueueDelete(ctx, tx, contentID); err != nil {
		return nil, fmt.Errorf("enqueueing catalog search delete: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit delete tx: %w", err)
	}

	return imageDirs, nil
}

// Search performs a full-text search on media items using the GIN tsvector
// index. It returns the matching items, total count of results, and any error.
//
// The data and total count are computed in a single round-trip via a
// COUNT(*) OVER () window function in the final SELECT off the scored CTE
// (audit 2026-05-01 §3.11). This replaces a prior two-query path that
// re-evaluated the tsvector predicates for the count.
func (r *ItemRepository) Search(ctx context.Context, query string, itemTypes []string, limit, offset int, filter AccessFilter) ([]*models.MediaItem, int, error) {
	items, total, _, _, err := r.SearchPage(ctx, query, itemTypes, limit, offset, filter, true)
	return items, total, err
}

func (r *ItemRepository) SearchPage(
	ctx context.Context,
	query string,
	itemTypes []string,
	limit, offset int,
	filter AccessFilter,
	includeTotal bool,
) ([]*models.MediaItem, int, bool, bool, error) {
	if filter.AllowedLibraryIDs != nil && len(filter.AllowedLibraryIDs) == 0 {
		return []*models.MediaItem{}, 0, false, includeTotal, nil
	}

	parsed := parseSearchQuery(query)

	// FTS block (the common path). queryLimit fetches one extra row in cursor
	// mode so execSearchBlock can derive hasMore without a separate count. When
	// the fuzzy fallback below could fire (first page of a fuzzy-eligible
	// query) the probe is additionally floored at fuzzyFallbackThreshold rows:
	// a tiny caller limit (e.g. an autocomplete asking for 2) with a few
	// incidental exact hits would otherwise report hasMore and hide the FTS
	// block's true size, so the sparse test below could never fire. Fetching up
	// to the threshold reveals a genuinely sparse block regardless of limit;
	// the returned page is still trimmed to limit. Where the fallback cannot
	// fire, fetching past limit+1 would be pure waste, so the floor is skipped.
	queryLimit := limit
	if !includeTotal {
		queryLimit = limit + 1
		if offset == 0 && eligibleForFuzzy(parsed) && queryLimit < fuzzyFallbackThreshold {
			queryLimit = fuzzyFallbackThreshold
		}
	}
	sql, countSQL, args := r.buildSearchSQLFromParsed(parsed, itemTypes, queryLimit, offset, filter, includeTotal)
	if sql == "" {
		return []*models.MediaItem{}, 0, false, includeTotal, nil
	}
	ftsItems, ftsTotal, ftsHasMore, ftsUntrimmed, err := r.execSearchBlock(ctx, r.pool, sql, countSQL, args, limit, offset, includeTotal)
	if err != nil {
		return nil, 0, false, includeTotal, err
	}

	// Fuzzy fallback trigger. The trigram (typo-tolerant) arm is deliberately NOT
	// part of the FTS query: OR-ing the pg_trgm % operator into that WHERE forces
	// a lossy bitmap heap recheck that rebuilds the three title tsvectors for
	// every one of the thousands of near-miss rows the % operator surfaces,
	// making *every* search take seconds. Instead we reach for the trigram index
	// only when the FTS result set is fully known and sparse (a likely
	// misspelling or word-boundary miss).
	//
	// Exact mode judges sparsity by the window count (page-independent), so every
	// page of a sparse query agrees and the combined result paginates fully.
	//
	// Cursor mode has no window count, so it uses the pre-trim size of a probe
	// floored at fuzzyFallbackThreshold rows: fewer than the threshold came
	// back ⇒ the whole FTS block is smaller than the threshold ⇒ sparse,
	// independent of the caller's page size. It only trusts this on the first
	// page (offset 0), where searchWithFuzzyFallback serves fuzzy as a terminal
	// augmentation (no phantom next page a bare offset cursor couldn't locate),
	// and only when the whole FTS block fits inside the caller's page: a
	// terminal page must never hide exact matches the plain cursor path would
	// have surfaced via hasMore. Past offset 0 an empty FTS page can't be told
	// from a rich one, so sparse stays false.
	var sparse bool
	if includeTotal {
		sparse = ftsTotal < fuzzyFallbackThreshold
	} else if offset == 0 {
		sparse = len(ftsUntrimmed) < fuzzyFallbackThreshold && len(ftsUntrimmed) <= limit
	}
	if !sparse || !eligibleForFuzzy(parsed) {
		return ftsItems, ftsTotal, ftsHasMore, includeTotal, nil
	}

	// Hand the fallback the FTS block when the rows just fetched provably form
	// the complete block, sparing it an identical second FTS query. Cursor mode
	// always qualifies here (the floored probe returned fewer rows than it
	// asked for); exact mode qualifies on the first page when the window count
	// fit within the page.
	ftsBlock, haveFTSBlock := []*models.MediaItem(nil), false
	if !includeTotal {
		ftsBlock, haveFTSBlock = ftsUntrimmed, true
	} else if offset == 0 && ftsTotal <= len(ftsItems) {
		ftsBlock, haveFTSBlock = ftsItems, true
	}
	return r.searchWithFuzzyFallback(ctx, parsed, itemTypes, limit, offset, filter, includeTotal, ftsBlock, haveFTSBlock)
}

// searchWithFuzzyFallback serves the combined [FTS block][fuzzy block] result
// for a sparse, fuzzy-eligible query. Because fuzzy only fires when the FTS
// block is sparse (< fuzzyFallbackThreshold rows) and the fuzzy contribution is
// capped at fuzzyMaxResults, the ENTIRE combined result is small
// (< fuzzyFallbackThreshold + fuzzyMaxResults rows). We materialize it once and
// slice the requested page in memory. That keeps two-block pagination correct on
// every page — stable total, no cross-page duplicates, no offset arithmetic
// straddling the block boundary — for both exact and cursor callers. The
// FTS-only path in SearchPage is untouched and still streams strictly per page.
//
// When haveFTSBlock is true, ftsBlock is the complete sparse FTS block the
// caller already fetched and no second FTS query is issued; otherwise (exact
// mode past the first page, or a first page smaller than the block) the block
// is re-fetched here at offset 0 — its ids drive fuzzy dedup below.
func (r *ItemRepository) searchWithFuzzyFallback(
	ctx context.Context,
	parsed parsedSearchQuery,
	itemTypes []string,
	limit, offset int,
	filter AccessFilter,
	includeTotal bool,
	ftsBlock []*models.MediaItem,
	haveFTSBlock bool,
) ([]*models.MediaItem, int, bool, bool, error) {
	if !haveFTSBlock {
		ftsSQL, ftsCountSQL, ftsArgs := r.buildSearchSQLFromParsed(parsed, itemTypes, fuzzyFallbackThreshold, 0, filter, true)
		var err error
		ftsBlock, _, _, _, err = r.execSearchBlock(ctx, r.pool, ftsSQL, ftsCountSQL, ftsArgs, fuzzyFallbackThreshold, 0, true)
		if err != nil {
			return nil, 0, false, includeTotal, err
		}
	}

	// Fuzzy block, excluding every FTS-block id so no title appears in both
	// blocks and the combined total is stable per page. Cursor mode serves the
	// combined result as a single terminal page, so it never needs more fuzzy
	// rows than the page has room for; exact mode materializes up to the cap.
	// Either way one extra row is fetched so truncation is detectable without
	// paying a COUNT(*) OVER () window count over the whole trigram match set.
	// When the FTS block found real hits the fuzzy arm demands near-certain
	// corrections (fuzzyAugmentSimilarityFloor) instead of base-threshold noise.
	fuzzyLimit := fuzzyMaxResults
	if !includeTotal && limit-len(ftsBlock) < fuzzyLimit {
		fuzzyLimit = limit - len(ftsBlock)
	}
	minSimilarity := 0.0
	if len(ftsBlock) > 0 {
		minSimilarity = fuzzyAugmentSimilarityFloor
	}
	var fuzzyBlock []*models.MediaItem
	fuzzyTruncated := false
	if fuzzyLimit > 0 {
		fuzzySQL, _, fuzzyArgs := r.buildFuzzySearchFromParsed(parsed, itemTypes, fuzzyLimit+1, 0, filter, false, contentIDsFromMediaItems(ftsBlock), minSimilarity)
		if fuzzySQL != "" {
			var err error
			fuzzyBlock, fuzzyTruncated, err = r.execFuzzyBlock(ctx, fuzzySQL, fuzzyArgs, fuzzyLimit)
			if err != nil {
				return nil, 0, false, includeTotal, err
			}
			if fuzzyTruncated {
				slog.DebugContext(ctx, "fuzzy search fallback truncated to cap",
					"query", parsed.Text, "cap", fuzzyLimit)
			}
		}
	}

	combined := make([]*models.MediaItem, 0, len(ftsBlock)+len(fuzzyBlock))
	combined = append(combined, ftsBlock...)
	combined = append(combined, fuzzyBlock...)
	total := len(combined)

	// Slice the requested page out of the materialized list, clamped so a
	// caller passing a negative offset or an overflowing limit (the exported
	// SearchPage is reachable outside the HTTP parser, which otherwise
	// guarantees offset>=0 and limit>0) gets a sane page instead of a
	// reversed-slice panic.
	lo := min(max(offset, 0), total)
	hi := lo + max(limit, 0)
	if hi < lo || hi > total { // hi < lo: lo+limit overflowed; serve the rest
		hi = total
	}
	page := combined[lo:hi]

	if !includeTotal {
		// Cursor mode reaches here only at offset 0 with the whole FTS block
		// inside the page (see SearchPage gate), so no exact match is hidden;
		// only fuzzy augmentation past the page is cut. Serve as a terminal
		// page: report only what we return and no next page, since a bare
		// offset cursor can't locate the FTS/fuzzy boundary on a follow-up
		// request.
		return page, len(page), false, false, nil
	}
	// When the fuzzy arm hit its cap the true match count is larger than the
	// materialized list, so surface the total as inexact rather than presenting
	// the cap as an exact count.
	return page, total, hi < total, !fuzzyTruncated, nil
}

// execFuzzyBlock runs the fuzzy data query inside a transaction that pins
// pg_trgm.strict_word_similarity_threshold, so the <<% operator's selectivity
// cannot drift with cluster configuration (its 0.6 server default would reject
// ordinary typos outright). The query was built with LIMIT fuzzyLimit+1; the
// extra row only signals truncation and is trimmed from the returned block.
func (r *ItemRepository) execFuzzyBlock(ctx context.Context, dataSQL string, args []any, fuzzyLimit int) ([]*models.MediaItem, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning fuzzy search tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL pg_trgm.strict_word_similarity_threshold = %g", trgmWordSimilarityThreshold)); err != nil {
		return nil, false, fmt.Errorf("pinning trigram word-similarity threshold: %w", err)
	}
	block, _, truncated, _, err := r.execSearchBlock(ctx, tx, dataSQL, "", args, fuzzyLimit, 0, false)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing fuzzy search tx: %w", err)
	}
	return block, truncated, nil
}

// searchQuerier is the subset of pgxpool.Pool / pgx.Tx that execSearchBlock
// needs, so a block can run either directly on the pool or inside a
// transaction (the fuzzy block pins a GUC with SET LOCAL).
type searchQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// execSearchBlock runs one search data query (FTS or fuzzy fallback) and its
// count-fallback sibling, returning that block's page. It is the shared engine
// behind SearchPage's two blocks so both derive hasMore/total identically.
//
// In cursor mode (includeTotal == false) the caller fetches limit+1 rows so
// hasMore is len(items) > limit; the extra row is trimmed here. In exact mode
// the data query carries COUNT(*) OVER () for the total, which emits no rows on
// an empty page (e.g. OFFSET past the last row); only then, and only past
// offset 0, is the count sibling re-run to recover the real total. Both queries
// must end with the trailing limit/offset args so the count sibling can drop
// them.
//
// The fourth return value is the untrimmed row set the data query actually
// returned (before the cursor-mode +1 row is trimmed; in exact mode it equals
// the page). SearchPage uses it in cursor mode to judge FTS-block sparsity
// independently of the caller's page size and to hand the fuzzy fallback the
// complete block without a second FTS query.
func (r *ItemRepository) execSearchBlock(ctx context.Context, q searchQuerier, dataSQL, countSQL string, args []any, limit, offset int, includeTotal bool) ([]*models.MediaItem, int, bool, []*models.MediaItem, error) {
	rows, err := q.Query(ctx, dataSQL, args...)
	if err != nil {
		return nil, 0, false, nil, fmt.Errorf("searching media items: %w", err)
	}
	defer rows.Close()

	if !includeTotal {
		items, err := scanItems(rows)
		if err != nil {
			return nil, 0, false, nil, err
		}
		untrimmed := items
		hasMore := len(items) > limit
		if hasMore {
			items = items[:limit]
		}
		total := offset + len(items)
		if hasMore {
			total++
		}
		return items, total, hasMore, untrimmed, nil
	}

	items, total, err := scanItemsWithTotal(rows)
	if err != nil {
		return nil, 0, false, nil, err
	}
	hasMore := total > offset+len(items)
	if len(items) == 0 && offset > 0 {
		// Drop the trailing limit/offset args from the data query.
		countArgs := args[:len(args)-2]
		if err := q.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
			return nil, 0, false, nil, fmt.Errorf("count fallback for empty search page: %w", err)
		}
		hasMore = total > offset+len(items)
	}
	return items, total, hasMore, items, nil
}

// buildSearchSQL assembles the unified search query, returning the SQL string
// and bound args (or empty string when the input parses to no searchable text).
//
// The query uses two CTEs: `scored` aggregates per-content_id ranking signals
// (title_rank, overview_rank, phrase_rank, plus exact/contiguous/year match
// flags), and `stats` derives a single has_title_match boolean over the
// scored set. The final SELECT CROSS JOINs stats and applies a WHERE that
// keeps every row whose title FTS rank is positive, plus overview-only rows
// when no title match exists in the candidate set AND the overview rank
// clears overviewMatchFloor. This suppresses single-word body-only matches
// for queries like "obsession" without harming queries where the term truly
// only appears in descriptions (those still surface as the fallback bucket).
// COUNT(*) OVER () in the final SELECT means the returned total reflects
// the post-filter row count automatically.
//
// Argument order is intentionally fixed:
//
//	$1               searchText (always)
//	$2               titlePrefixTsQuery (always)
//	itemType placeholders, allowed/disabled libraries, MaxContentRating
//	parsed.ExactTitleHint
//	parsed.Year (or NULL)
//	parsed.Phrase
//	limit, offset
//
// searchTextFromParsed derives the effective search text shared by the FTS and
// fuzzy builders: the parsed Text, or (when the query was only quotes/whitespace
// that parsed to empty Text) a whitespace-collapsed, quote-stripped fallback off
// the raw query. Centralized so the two builders can never search different text.
func searchTextFromParsed(parsed parsedSearchQuery) string {
	if parsed.Text != "" {
		return parsed.Text
	}
	return collapseSearchWhitespace(strings.ReplaceAll(strings.TrimSpace(parsed.Raw), "\"", " "))
}

func (r *ItemRepository) buildSearchSQL(query string, itemTypes []string, limit, offset int, filter AccessFilter) (dataSQL, countSQL string, args []any) {
	return r.buildSearchSQLWithTotal(query, itemTypes, limit, offset, filter, true)
}

func (r *ItemRepository) buildSearchSQLWithTotal(query string, itemTypes []string, limit, offset int, filter AccessFilter, includeTotal bool) (dataSQL, countSQL string, args []any) {
	return r.buildSearchSQLFromParsed(parseSearchQuery(query), itemTypes, limit, offset, filter, includeTotal)
}

// buildSearchSQLFromParsed is the FTS query builder proper; the string-taking
// wrappers above parse first. SearchPage parses once and calls this directly so
// the same parsedSearchQuery feeds the FTS builder, the fuzzy builder, and the
// eligibility gate without re-parsing. The mixed builder unions media_items and
// episode candidates into one ranked page.
func (r *ItemRepository) buildSearchSQLFromParsed(parsed parsedSearchQuery, itemTypes []string, limit, offset int, filter AccessFilter, includeTotal bool) (dataSQL, countSQL string, args []any) {
	return r.buildMixedSearchSQLFromParsed(parsed, itemTypes, limit, offset, filter, includeTotal)
}

// appendSearchScopeFilters appends the scope predicates shared by the FTS search
// (buildSearchSQLWithTotal) and the trigram fuzzy fallback (buildFuzzySearchSQL):
// item type, allowed/disabled libraries, the access filter, and the
// manga-chapter exclusion. It mutates conditions/args/argIdx in place and
// returns the FROM clause (always "media_items mi"; library scoping is enforced
// with independent EXISTS/NOT EXISTS subqueries rather than a JOIN). Both callers
// append these in the same order so a single helper keeps the two queries'
// filtering provably identical.
func appendSearchScopeFilters(itemTypes []string, filter AccessFilter, conditions *[]string, args *[]any, argIdx *int) (fromClause string) {
	fromClause = "media_items mi"

	if len(itemTypes) > 0 {
		placeholders := make([]string, 0, len(itemTypes))
		for _, itemType := range itemTypes {
			if strings.TrimSpace(itemType) == "" {
				continue
			}
			placeholders = append(placeholders, fmt.Sprintf("$%d", *argIdx))
			*args = append(*args, strings.ToLower(strings.TrimSpace(itemType)))
			*argIdx++
		}
		if len(placeholders) > 0 {
			*conditions = append(*conditions, fmt.Sprintf("mi.type IN (%s)", strings.Join(placeholders, ", ")))
		}
	}

	// Library allow/deny via the shared leak-safe helper: an item linked to both a
	// passing and a disabled library must not slip through. The old JOIN +
	// NOT(mil.media_folder_id = ANY(...)) form let the passing membership row
	// satisfy the deny check (audit 2026-05-01 §3.3), so both the FTS search and
	// the fuzzy fallback that share this helper used it. appendLibraryAccessConditions
	// emits independent EXISTS/NOT EXISTS subqueries keyed on mi.content_id and
	// needs no JOIN.
	appendLibraryAccessConditions("mi.content_id", filter, conditions, args, argIdx)

	applyAccessFilter("mi", AccessFilter{MaxContentRating: filter.MaxContentRating, ExcludedMediaTypes: filter.ExcludedMediaTypes}, conditions, args, argIdx)

	// Manga chapters (type='ebook' rows linked into a manga series) are internal
	// sub-units and must never surface as standalone search results.
	*conditions = append(*conditions, MangaChapterExclusionWhere("mi"))
	return fromClause
}

// buildFuzzySearchSQL assembles the trigram fuzzy-fallback query invoked by
// SearchPage only when the FTS query is sparse. Unlike buildSearchSQLWithTotal,
// the sole match predicate is the pg_trgm strict-word-similarity operator
// (<<%) against the indexed title_normalized generated column (migration 105's
// gin_trgm_ops index serves it), and the ranking signals are
// strict_word_similarity()/similarity() on that same column. Crucially it
// never references the title tsvectors, so the low-similarity rows the
// operator can surface never pay a per-row tsvector rebuild — that rebuild
// over the fuzzy candidate set was the entire cause of the multi-second search
// regression. The operator's base selectivity is pinned by the caller
// (execFuzzyBlock sets pg_trgm.strict_word_similarity_threshold via SET LOCAL)
// so it cannot drift with cluster configuration.
//
// Scope note: only title_normalized — a generated column over `title` — is
// fuzzy-matched, because it is the only column with a gin_trgm_ops index
// (migration 105). Typos of original_title or sort_title, which the FTS arm
// covers exactly, are deliberately out of the fuzzy arm's scope; extending it
// would need matching normalized columns + trigram indexes (follow-up).
//
// includeTotal toggles the COUNT(*) OVER () window total the same way
// buildSearchSQLWithTotal does, so the fuzzy block honors the caller's cursor
// vs. exact-count mode. excludeContentIDs drops rows already returned by the FTS
// block so a title matching both is not shown twice. minSimilarity > 0 adds an
// explicit similarity floor above the base threshold — used when the FTS block
// had real hits so augmentation only admits near-certain corrections. Argument
// order: $1 searchText, then the similarity floor (if any), then the shared
// scope placeholders, then the exclusion array, then limit/offset.
func (r *ItemRepository) buildFuzzySearchSQL(query string, itemTypes []string, limit, offset int, filter AccessFilter, includeTotal bool, excludeContentIDs []string, minSimilarity float64) (dataSQL, countSQL string, args []any) {
	return r.buildFuzzySearchFromParsed(parseSearchQuery(query), itemTypes, limit, offset, filter, includeTotal, excludeContentIDs, minSimilarity)
}

// buildFuzzySearchFromParsed is the fuzzy query builder proper; the string-taking
// wrapper above parses first. SearchPage parses once and calls this directly.
func (r *ItemRepository) buildFuzzySearchFromParsed(parsed parsedSearchQuery, itemTypes []string, limit, offset int, filter AccessFilter, includeTotal bool, excludeContentIDs []string, minSimilarity float64) (dataSQL, countSQL string, args []any) {
	searchText := searchTextFromParsed(parsed)
	if searchText == "" {
		return "", "", nil
	}

	args = []any{searchText}
	argIdx := 2
	// Strict word similarity (<<%) rather than full-string similarity (%): a
	// typo of one word must still reach long titles ("avegners" → "Avengers:
	// Endgame"), where full-string similarity is diluted by every extra trigram
	// the rest of the title contributes. <<% scores the query against the best
	// word-boundary extent of the title instead, and at equal thresholds it is
	// a strict superset of % (the whole string is itself a valid extent). The
	// same gin_trgm_ops index serves both operators.
	// The alias arm is a scalar array subquery (InitPlan) for the same reason
	// as the FTS builder's alias arms: `= ANY(param)` keeps the outer OR a
	// BitmapOr over the two gin_trgm_ops indexes, where an `IN (subquery)` arm
	// would force a seq scan evaluating <<% against every media_items row.
	conditions := []string{`(
		public.normalize_search_text($1) <<% mi.title_normalized OR
		mi.content_id = ANY(COALESCE((
			SELECT array_agg(DISTINCT mia.content_id) FROM media_item_aliases mia
			WHERE public.normalize_search_text($1) <<% mia.normalized_title
		), '{}'::text[]))
	)`}
	if minSimilarity > 0 {
		// The floor is whole-title similarity, NOT word similarity: augmenting
		// a query that already has hits must only admit near-identical titles,
		// and word similarity rates embedded prefix words far too high (see
		// fuzzyAugmentSimilarityFloor).
		conditions = append(conditions, fmt.Sprintf(`GREATEST(
			similarity(public.normalize_search_text($1), mi.title_normalized),
			COALESCE((SELECT MAX(similarity(public.normalize_search_text($1), mia.normalized_title)) FROM media_item_aliases mia WHERE mia.content_id = mi.content_id), 0)
		) >= $%d`, argIdx))
		args = append(args, minSimilarity)
		argIdx++
	}

	fromClause := appendSearchScopeFilters(itemTypes, filter, &conditions, &args, &argIdx)

	if len(excludeContentIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("NOT (mi.content_id = ANY($%d))", argIdx))
		args = append(args, excludeContentIDs)
		argIdx++
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	// GROUP BY is required so the MAX(similarity(...)) ranking aggregate is legal,
	// mirroring buildSearchSQLWithTotal; COUNT(*) OVER () then counts distinct
	// content_ids. qualifiedItemColumns aliases the coalesced columns so the outer
	// SELECT can re-reference them; GROUP BY uses the raw refs.
	qualifiedCols := qualifiedItemColumns("mi")
	groupByCols := qualifiedItemColumnRefs("mi")
	// fuzzy_rank orders by how well the query matches SOME word extent of the
	// title; fuzzy_full_rank tie-breaks by whole-title closeness so "The
	// Avengers" sorts above "Avengers: Endgame" for the typo "avegners". Both
	// are computed only over the matched candidate set.
	scoredCTE := fmt.Sprintf(`
		WITH scored AS (
			SELECT
				%s,
				MAX(GREATEST(
					strict_word_similarity(public.normalize_search_text($1), mi.title_normalized),
					COALESCE((SELECT MAX(strict_word_similarity(public.normalize_search_text($1), mia.normalized_title)) FROM media_item_aliases mia WHERE mia.content_id = mi.content_id), 0)
				)) AS fuzzy_rank,
				MAX(GREATEST(
					similarity(public.normalize_search_text($1), mi.title_normalized),
					COALESCE((SELECT MAX(similarity(public.normalize_search_text($1), mia.normalized_title)) FROM media_item_aliases mia WHERE mia.content_id = mi.content_id), 0)
				)) AS fuzzy_full_rank
			FROM %s
			%s
			GROUP BY %s
		)
	`, qualifiedCols, fromClause, whereClause, groupByCols)

	totalColumn := ""
	if includeTotal {
		totalColumn = ", COUNT(*) OVER () AS total_count"
	}
	dataSQL = scoredCTE + fmt.Sprintf(`
		SELECT %s%s
		FROM scored
		ORDER BY fuzzy_rank DESC, fuzzy_full_rank DESC, LOWER(title) ASC, content_id ASC
		LIMIT $%d OFFSET $%d`, itemColumns, totalColumn, argIdx, argIdx+1)
	countSQL = scoredCTE + `SELECT COUNT(*) FROM scored`
	args = append(args, limit, offset)
	return dataSQL, countSQL, args
}

// ListUnmatchedByFolderAndPathPrefix returns content IDs for unmatched-style
// items that are linked to at least one present file within the given folder
// subtree. This intentionally includes ambiguous items so a library scan can
// revisit legacy scanner ambiguities after inference heuristics improve.
func (r *ItemRepository) buildListUnmatchedByFolderAndPathPrefixSQL(folderID int, pathPrefix string, limit int) (string, []any) {
	query := `
		SELECT mi.content_id
		FROM media_items mi
		JOIN media_item_libraries mil
			ON mil.content_id = mi.content_id
		JOIN media_folders folders
			ON folders.id = mil.media_folder_id
		JOIN media_files mf
			ON mf.content_id = mi.content_id
		   AND mf.media_folder_id = mil.media_folder_id
		WHERE mil.media_folder_id = $1
		  AND folders.enabled = true
		  AND mi.status IN ('unmatched', 'pending', 'ambiguous')
		  -- Manga chapters stay status='pending' by design: provider metadata
		  -- lives on the type='manga' series item, so chapters are never
		  -- matchable and must not feed the matcher's retry loop (mirrors the
		  -- exclusion in the ebook enricher's claim query).
		  AND ` + MangaChapterExclusionWhere("mi") + `
		  AND mf.missing_since IS NULL
		  AND (mf.file_path = $2 OR mf.file_path LIKE $3 ESCAPE '\')
		GROUP BY mi.content_id
		ORDER BY MIN(mf.id) ASC, mi.content_id ASC`

	args := []any{folderID, pathPrefix, pathPrefixLike(pathPrefix)}
	if limit > 0 {
		query += ` LIMIT $4`
		args = append(args, limit)
	}
	return query, args
}

func (r *ItemRepository) ListUnmatchedByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, limit int) ([]string, error) {
	query, args := r.buildListUnmatchedByFolderAndPathPrefixSQL(folderID, pathPrefix, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing unmatched items by folder and path prefix: %w", err)
	}
	defer rows.Close()

	var contentIDs []string
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning unmatched item content_id: %w", err)
		}
		contentIDs = append(contentIDs, contentID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating unmatched item rows: %w", err)
	}

	return contentIDs, nil
}

// EnsureAccessible returns ErrItemNotFound when the item falls outside the effective scope.
//
// Library restrictions are enforced with the same independent EXISTS /
// NOT EXISTS predicates as GetByIDsWithAccess (see libraryAccessConditions):
// an item linked to both a passing and a disabled library must not leak
// through the passing link.
func (r *ItemRepository) EnsureAccessible(ctx context.Context, contentID string, filter AccessFilter) error {
	// An empty (non-nil) allowlist means "no libraries allowed".
	if filter.AllowedLibraryIDs != nil && len(filter.AllowedLibraryIDs) == 0 {
		return ErrItemNotFound
	}

	query, args := buildEnsureAccessibleSQL(contentID, filter)
	var found int
	if err := r.pool.QueryRow(ctx, query, args...).Scan(&found); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrItemNotFound
		}
		return fmt.Errorf("checking item access: %w", err)
	}
	return nil
}

func buildEnsureAccessibleSQL(contentID string, filter AccessFilter) (string, []any) {
	var conditions []string
	var args []any
	argIdx := 1

	conditions = append(conditions, fmt.Sprintf("mi.content_id = $%d", argIdx))
	args = append(args, contentID)
	argIdx++

	appendLibraryAccessConditions("mi.content_id", filter, &conditions, &args, &argIdx)
	applyAccessFilter("mi", AccessFilter{MaxContentRating: filter.MaxContentRating, ExcludedMediaTypes: filter.ExcludedMediaTypes}, &conditions, &args, &argIdx)

	return fmt.Sprintf("SELECT 1 FROM media_items mi WHERE %s LIMIT 1", strings.Join(conditions, " AND ")), args
}

// EnsureAccessibleIDs is the batch form of EnsureAccessible: it returns the set
// of content IDs from the input that the viewer may access, applying the exact
// same predicates (per-item library allow/deny via EXISTS / NOT EXISTS, max
// content rating, excluded media types). IDs absent from the returned map are
// not accessible (mirrors EnsureAccessible returning ErrItemNotFound). Used by
// GetItemDetailsByIDs to avoid a per-item EnsureAccessible fan-out.
func (r *ItemRepository) EnsureAccessibleIDs(ctx context.Context, contentIDs []string, filter AccessFilter) (map[string]bool, error) {
	result := make(map[string]bool, len(contentIDs))
	if len(contentIDs) == 0 {
		return result, nil
	}
	// An empty (non-nil) allowlist means "no libraries allowed" — nothing is
	// accessible, exactly as EnsureAccessible returns ErrItemNotFound.
	if filter.AllowedLibraryIDs != nil && len(filter.AllowedLibraryIDs) == 0 {
		return result, nil
	}

	query, args := buildEnsureAccessibleIDsSQL(contentIDs, filter)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("checking items access: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning item access row: %w", err)
		}
		result[id] = true
	}
	return result, rows.Err()
}

func buildEnsureAccessibleIDsSQL(contentIDs []string, filter AccessFilter) (string, []any) {
	var conditions []string
	var args []any
	argIdx := 1

	conditions = append(conditions, fmt.Sprintf("mi.content_id = ANY($%d)", argIdx))
	args = append(args, contentIDs)
	argIdx++

	appendLibraryAccessConditions("mi.content_id", filter, &conditions, &args, &argIdx)
	applyAccessFilter("mi", AccessFilter{MaxContentRating: filter.MaxContentRating, ExcludedMediaTypes: filter.ExcludedMediaTypes}, &conditions, &args, &argIdx)

	return fmt.Sprintf("SELECT mi.content_id FROM media_items mi WHERE %s", strings.Join(conditions, " AND ")), args
}

// GetItemsInLibrary returns a membership map for the provided content IDs within
// a single library. Episode content IDs are matched directly against
// episode_libraries so compat callers can filter mixed item/episode batches.
func (r *ItemRepository) GetItemsInLibrary(ctx context.Context, contentIDs []string, libraryID int) (map[string]bool, error) {
	result := make(map[string]bool, len(contentIDs))
	if len(contentIDs) == 0 {
		return result, nil
	}

	rows, err := r.pool.Query(ctx,
		`SELECT req.content_id
		FROM unnest($2::text[]) AS req(content_id)
		WHERE EXISTS (
			SELECT 1
			FROM media_item_libraries mil
			WHERE mil.media_folder_id = $1
			  AND mil.content_id = req.content_id
		)
		OR EXISTS (
			SELECT 1
			FROM episode_libraries el
			WHERE el.media_folder_id = $1
			  AND el.episode_id = req.content_id
		)`,
		libraryID, contentIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("getting library membership for items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning library membership row: %w", err)
		}
		result[contentID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating library membership rows: %w", err)
	}

	return result, nil
}

// ReplacePeople deletes all item_people rows for the given content_id and inserts new ones.
func (r *ItemRepository) ReplacePeople(ctx context.Context, contentID string, people []models.ItemPerson) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "DELETE FROM item_people WHERE content_id = $1", contentID); err != nil {
		return fmt.Errorf("delete existing people: %w", err)
	}

	if len(people) == 0 {
		if err := r.searchIndexEvents.EnqueueUpsert(ctx, tx, contentID); err != nil {
			return fmt.Errorf("enqueueing catalog search people update: %w", err)
		}
		return tx.Commit(ctx)
	}

	// Deduplicate by (person_id, kind, character) — PostgreSQL's ON CONFLICT
	// cannot handle the same row appearing twice in a single INSERT.
	type dedupKey struct {
		PersonID  int64
		Kind      models.PersonKind
		Character string
	}
	seen := make(map[dedupKey]struct{}, len(people))
	deduped := make([]models.ItemPerson, 0, len(people))
	for _, p := range people {
		key := dedupKey{p.Person.ID, p.Kind, p.Character}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, p)
	}

	var sb strings.Builder
	sb.WriteString("INSERT INTO item_people (id, content_id, person_id, kind, character, sort_order) VALUES ")
	args := make([]interface{}, 0, len(deduped)*6)
	for i, p := range deduped {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * 6
		fmt.Fprintf(&sb, "($%d, $%d, $%d, $%d, $%d, $%d)", base+1, base+2, base+3, base+4, base+5, base+6)

		rowIDStr, err := idgen.NextID()
		if err != nil {
			return fmt.Errorf("generate content-person id: %w", err)
		}
		rowID, _ := strconv.ParseInt(rowIDStr, 10, 64)
		args = append(args, rowID, contentID, p.Person.ID, p.Kind, p.Character, p.SortOrder)
	}
	sb.WriteString(" ON CONFLICT (content_id, person_id, kind, character) DO UPDATE SET sort_order = EXCLUDED.sort_order")

	if _, err := tx.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("insert people: %w", err)
	}

	if err := r.searchIndexEvents.EnqueueUpsert(ctx, tx, contentID); err != nil {
		return fmt.Errorf("enqueueing catalog search people update: %w", err)
	}

	return tx.Commit(ctx)
}

// GetPeople returns all people credited on a media item via the item_people + people JOIN.
func (r *ItemRepository) GetPeople(ctx context.Context, contentID string) ([]models.ItemPerson, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.name, p.sort_name, p.bio, p.birth_date, p.death_date, p.birthplace, p.homepage,
			p.photo_path, p.photo_source_path, p.photo_thumbhash, p.tmdb_id, p.imdb_id, p.tvdb_id, p.plex_guid,
			p.created_at, p.updated_at,
			ip.kind, ip.character, ip.sort_order
		FROM item_people ip
		JOIN people p ON p.id = ip.person_id
		WHERE ip.content_id = $1
		ORDER BY ip.kind, ip.sort_order`, contentID,
	)
	if err != nil {
		return nil, fmt.Errorf("get people for item %s: %w", contentID, err)
	}
	defer rows.Close()

	return scanItemPeople(rows)
}

// UpdateMetadata builds a dynamic UPDATE query for the media_items table,
// setting only the non-nil fields in upd. Always bumps updated_at.
// Returns ErrItemNotFound if no row matches contentID.
func (r *ItemRepository) UpdateMetadata(ctx context.Context, contentID string, upd *MetadataUpdate) error {
	var setClauses []string
	var args []any
	argIdx := 1

	addString := func(col string, val *string) {
		if val != nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, *val)
			argIdx++
		}
	}
	// addNullableString behaves like addString but stores an empty string as SQL
	// NULL (via NULLIF), so clearing a nullable column persists as NULL.
	addNullableString := func(col string, val *string) {
		if val != nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = NULLIF($%d, '')", col, argIdx))
			args = append(args, *val)
			argIdx++
		}
	}
	addInt := func(col string, val *int) {
		if val != nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, *val)
			argIdx++
		}
	}
	addFloat := func(col string, val *float64) {
		if val != nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, *val)
			argIdx++
		}
	}
	addStringArray := func(col string, val *[]string) {
		if val != nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, *val)
			argIdx++
		}
	}
	addIntArray := func(col string, val *[]int) {
		if val != nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, *val)
			argIdx++
		}
	}

	addString("title", upd.Title)
	addString("sort_title", upd.SortTitle)
	addString("original_title", upd.OriginalTitle)
	addString("overview", upd.Overview)
	addString("tagline", upd.Tagline)
	addString("content_rating", upd.ContentRating)
	addInt("year", upd.Year)
	addInt("runtime", upd.Runtime)
	addFloat("rating_imdb", upd.RatingIMDB)
	addFloat("rating_tmdb", upd.RatingTMDB)
	addInt("rating_rt_critic", upd.RatingRTCritic)
	addInt("rating_rt_audience", upd.RatingRTAudience)
	addStringArray("genres", upd.Genres)
	addStringArray("studios", upd.Studios)
	addStringArray("networks", upd.Networks)
	addStringArray("countries", upd.Countries)
	addString("release_date", upd.ReleaseDate)
	addString("first_air_date", upd.FirstAirDate)
	addString("last_air_date", upd.LastAirDate)
	addString("air_time", upd.AirTime)
	addNullableString("air_timezone", upd.AirTimezone)
	addString("status", upd.Status)
	addString("show_status", upd.ShowStatus)
	addString("imdb_id", upd.ImdbID)
	addString("tmdb_id", upd.TmdbID)
	addString("tvdb_id", upd.TvdbID)
	addIntArray("locked_fields", upd.LockedFields)
	addString("poster_path", upd.PosterPath)
	if upd.PosterPath != nil && upd.PosterSourcePath == nil {
		// An explicit poster override invalidates the provider-origin source
		// path captured by image caching; outbound embeds must not keep
		// rendering the replaced provider artwork.
		setClauses = append(setClauses, "poster_source_path = ''")
	}
	addString("poster_source_path", upd.PosterSourcePath)
	addString("poster_thumbhash", upd.PosterThumbhash)
	addString("backdrop_path", upd.BackdropPath)
	if upd.BackdropPath != nil && upd.BackdropSourcePath == nil {
		setClauses = append(setClauses, "backdrop_source_path = ''")
	}
	addString("backdrop_source_path", upd.BackdropSourcePath)
	addString("backdrop_thumbhash", upd.BackdropThumbhash)
	addString("logo_path", upd.LogoPath)
	if upd.LogoPath != nil && upd.LogoSourcePath == nil {
		setClauses = append(setClauses, "logo_source_path = ''")
	}
	addString("logo_source_path", upd.LogoSourcePath)

	setClauses = append(setClauses, "updated_at = NOW()")

	query := fmt.Sprintf("UPDATE media_items SET %s WHERE content_id = $%d",
		strings.Join(setClauses, ", "), argIdx)
	args = append(args, contentID)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin metadata update tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating media item metadata: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	if err := r.searchIndexEvents.EnqueueUpsert(ctx, tx, contentID); err != nil {
		return fmt.Errorf("enqueueing catalog search metadata update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit metadata update tx: %w", err)
	}
	return nil
}

func (r *ItemRepository) UpdateArtworkIfSourceMatches(ctx context.Context, contentID, imageType, sourcePath, cachedPath, thumbhash string) (bool, error) {
	if r == nil || r.pool == nil {
		return false, ErrItemNotFound
	}

	var query string
	var args []any
	switch imageType {
	case "poster":
		query = `
			UPDATE media_items
			SET poster_path = $3,
				poster_source_path = $2,
				poster_thumbhash = $4,
				updated_at = NOW()
			WHERE content_id = $1
			  AND poster_source_path = $2`
		args = []any{contentID, sourcePath, cachedPath, thumbhash}
	case "backdrop":
		query = `
			UPDATE media_items
			SET backdrop_path = $3,
				backdrop_source_path = $2,
				backdrop_thumbhash = $4,
				updated_at = NOW()
			WHERE content_id = $1
			  AND backdrop_source_path = $2`
		args = []any{contentID, sourcePath, cachedPath, thumbhash}
	case "logo":
		query = `
			UPDATE media_items
			SET logo_path = $3,
				logo_source_path = $2,
				updated_at = NOW()
			WHERE content_id = $1
			  AND logo_source_path = $2`
		args = []any{contentID, sourcePath, cachedPath}
	default:
		return false, fmt.Errorf("unsupported media item artwork type %q", imageType)
	}

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("updating media item cached artwork: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// IncrementRefreshFailure records a failed metadata refresh attempt for an
// existing media item.
func (r *ItemRepository) IncrementRefreshFailure(ctx context.Context, contentID string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_items
		SET refresh_failures = refresh_failures + 1
		WHERE content_id = $1`,
		contentID,
	)
	if err != nil {
		return fmt.Errorf("incrementing refresh failure: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

// MediaTMDBRow is a single result row from LookupTMDBIDs, containing the
// fields needed by the pluginhost CatalogPresence adapter.
type MediaTMDBRow struct {
	MediaID   string
	TMDBID    string
	LibraryID string
	Title     string
}

type ExternalIDLookupCandidate struct {
	TMDBID string
	TVDBID string
	IMDbID string
}

type ExternalIDMatchRow struct {
	QueryTMDBID     string
	MediaID         string
	MatchedProvider string
	LibraryID       string
	Title           string
}

// LookupTMDBIDs returns one row per matching media item that has its tmdb_id
// in the supplied list and is linked to at least one enabled library.
// mediaType is "movie" or "series" (silo's internal naming).
// When a media item is linked to multiple libraries the first matched row is
// returned (ordered by library id for determinism).
// The pluginhost.CatalogPresence adapter calls this to answer
// CheckMediaPresence requests from plugins.
func (r *ItemRepository) LookupTMDBIDs(ctx context.Context, mediaType string, tmdbIDs []string) ([]MediaTMDBRow, error) {
	if len(tmdbIDs) == 0 {
		return nil, nil
	}
	candidates := make([]ExternalIDLookupCandidate, 0, len(tmdbIDs))
	for _, id := range tmdbIDs {
		if strings.TrimSpace(id) != "" {
			candidates = append(candidates, ExternalIDLookupCandidate{TMDBID: id})
		}
	}
	rows, err := r.LookupExternalIDs(ctx, mediaType, candidates)
	if err != nil {
		return nil, err
	}
	out := make([]MediaTMDBRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, MediaTMDBRow{
			MediaID:   row.MediaID,
			TMDBID:    row.QueryTMDBID,
			LibraryID: row.LibraryID,
			Title:     row.Title,
		})
	}
	return out, nil
}

func lookupExternalIDsSQL() string {
	return `
		WITH requested(query_tmdb_id, provider, provider_id, ord) AS (
			SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::int[])
		),
		direct_matches AS (
			SELECT r.query_tmdb_id, mi.content_id, r.provider, mil.media_folder_id::text, mi.title, r.ord,
			       CASE r.provider WHEN 'tmdb' THEN 0 WHEN 'tvdb' THEN 1 WHEN 'imdb' THEN 2 ELSE 3 END AS provider_rank
			FROM requested r
			JOIN media_items mi
			  ON mi.type = $5
			 AND (
				(r.provider = 'tmdb' AND mi.tmdb_id <> '' AND mi.tmdb_id = r.provider_id)
				OR (r.provider = 'tvdb' AND mi.tvdb_id <> '' AND mi.tvdb_id = r.provider_id)
				OR (r.provider = 'imdb' AND mi.imdb_id <> '' AND mi.imdb_id = r.provider_id)
			 )
			JOIN media_item_libraries mil ON mil.content_id = mi.content_id
			JOIN media_folders mf ON mf.id = mil.media_folder_id
			WHERE mf.enabled = true
		),
		provider_matches AS (
			SELECT r.query_tmdb_id, mi.content_id, r.provider, mil.media_folder_id::text, mi.title, r.ord,
			       CASE r.provider WHEN 'tmdb' THEN 0 WHEN 'tvdb' THEN 1 WHEN 'imdb' THEN 2 ELSE 3 END AS provider_rank
			FROM requested r
			JOIN media_item_provider_ids mip
			  ON mip.provider = r.provider
			 AND mip.provider_id = r.provider_id
			 AND mip.item_type = $5
			JOIN media_items mi ON mi.content_id = mip.content_id AND mi.type = $5
			JOIN media_item_libraries mil ON mil.content_id = mi.content_id
			JOIN media_folders mf ON mf.id = mil.media_folder_id
			WHERE mf.enabled = true
		)
		SELECT DISTINCT ON (query_tmdb_id)
		       query_tmdb_id, content_id, provider, media_folder_id, title
		FROM (
			SELECT * FROM direct_matches
			UNION ALL
			SELECT * FROM provider_matches
		) matches
		ORDER BY query_tmdb_id, provider_rank ASC, ord ASC, content_id ASC, media_folder_id ASC`
}

func (r *ItemRepository) LookupExternalIDs(
	ctx context.Context,
	mediaType string,
	candidates []ExternalIDLookupCandidate,
) ([]ExternalIDMatchRow, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	queryTMDBIDs := make([]string, 0, len(candidates)*3)
	providers := make([]string, 0, len(candidates)*3)
	providerIDs := make([]string, 0, len(candidates)*3)
	ordinals := make([]int32, 0, len(candidates)*3)

	appendID := func(candidate ExternalIDLookupCandidate, provider, providerID string, ordinal int) {
		providerID = strings.TrimSpace(providerID)
		if providerID == "" {
			return
		}
		queryTMDBIDs = append(queryTMDBIDs, strings.TrimSpace(candidate.TMDBID))
		providers = append(providers, provider)
		providerIDs = append(providerIDs, providerID)
		ordinals = append(ordinals, int32(ordinal))
	}

	for i, candidate := range candidates {
		appendID(candidate, "tmdb", candidate.TMDBID, i)
		appendID(candidate, "tvdb", candidate.TVDBID, i)
		appendID(candidate, "imdb", candidate.IMDbID, i)
	}
	if len(providerIDs) == 0 {
		return nil, nil
	}

	rows, err := r.pool.Query(ctx, lookupExternalIDsSQL(), queryTMDBIDs, providers, providerIDs, ordinals, mediaType)
	if err != nil {
		return nil, fmt.Errorf("lookup external ids: %w", err)
	}
	defer rows.Close()

	out := make([]ExternalIDMatchRow, 0)
	for rows.Next() {
		var row ExternalIDMatchRow
		if err := rows.Scan(&row.QueryTMDBID, &row.MediaID, &row.MatchedProvider, &row.LibraryID, &row.Title); err != nil {
			return nil, fmt.Errorf("scanning external id lookup row: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating external id lookup rows: %w", err)
	}
	return out, nil
}

func pathPrefixLike(pathPrefix string) string {
	return pathscope.PrefixLike(pathPrefix)
}
