package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pathscope"
	"github.com/Silo-Server/silo-server/internal/requestlock"
)

// Sentinel errors for item repository operations.
var (
	ErrItemNotFound            = errors.New("media item not found")
	ErrCollectionItemNotMember = errors.New("item is not in this collection")
	ErrIncompatibleLibrary     = errors.New("collection has no compatible target library")
	ErrVirtualPlaybackDisabled = errors.New("collection does not have virtual playback enabled")
	ErrProviderUnavailable     = errors.New("virtual playback provider unavailable")
)

// MaterializeResult contains item and file counts resulting from materialization.
type MaterializeResult struct {
	ContentID            string `json:"content_id"`
	MediaType            string `json:"media_type"`
	FilesCreated         int    `json:"files_created"`
	FilesExisting        int    `json:"files_existing"`
	EpisodesMaterialized int    `json:"episodes_materialized"`
}

// ItemRepository provides CRUD operations for the media_items table.
type ItemRepository struct {
	pool              *pgxpool.Pool
	searchIndexEvents *SearchIndexEventRepository
}

type itemExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
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
		  AND (target.target_exists OR target.target_rank>1)`+virtualFileRetentionGuard)
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
//
// Provenance is the explicit origin discriminator: "core" marks variants
// stamped by the in-tree core virtual library (Phase 4 Variants()), "plugin"
// marks plugin-path variants, and "" means legacy/unknown (existing plugin
// rows). The zero value stays fail-open on the legacy path; the NEW core SSRF
// dispatch fails closed on unknown/inconsistent provenance. No DB column backs
// this in Phase 2 — struct-level only.
type VirtualPlaybackVariant struct {
	VirtualURI          string
	Label               string
	Resolution          string
	CodecVideo          string
	CodecAudio          string
	HDR                 string
	OwnerInstallationID int
	VirtualProvenance   models.VirtualProvenance
}

// isPGTransient reports Postgres errors that are safe to retry: deadlocks
// (40P01), serialization failures (40001), and lock timeouts (55P03). The
// purge sweep touches media_files/media_items/claims under concurrent
// virtual reconcile writers, so all three surface here.
func isPGTransient(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	return strings.Contains(msg, "40P01") || strings.Contains(lower, "deadlock detected") ||
		strings.Contains(msg, "40001") || strings.Contains(lower, "could not serialize") ||
		strings.Contains(msg, "55P03") || strings.Contains(lower, "lock timeout")
}

// VirtualPurgeOptions filters an administrative virtual-item purge. Zero
// values mean "no filter".
type VirtualPurgeOptions struct {
	LibraryID      int
	InstallationID int
	DryRun         bool
}

// VirtualPurgeResult reports what one purge pass removed. StateRowsDeleted
// counts per-user consumption rows (watch progress, history, favorites,
// ratings, watchlist, hidden-history bookkeeping), metadata-debt and
// plugin stream-metadata rows, and orphaned episode/season metadata whose
// content no longer exists.
// CollectionsPruned counts dangling collection links removed plus distinct
// recommendation cache rows affected (malformed rows evicted, pre-existing
// empty rows removed, rows pruned). Each affected row counts exactly once
// on the statement that removes or rewrites it; rows the prune empties are
// counted once on update and removed without an additional count.
//
// Dry-run previews FilesDeleted and ItemsDeleted only; the state and
// collection sweeps are commit-time cleanup and are reported as zero in
// dry-run results (not estimated).
type VirtualPurgeResult struct {
	FilesDeleted      int64
	ItemsDeleted      int64
	StateRowsDeleted  int64
	CollectionsPruned int64
	// PurgeRevision is the shared catalog_purge_revision value committed by
	// this purge (0 on dry-run, which changes nothing). Callers carry it on
	// the fan-out event so other replicas can reconcile by revision.
	PurgeRevision int64
}

// PurgeVirtualPlaybackItems removes zero-storage virtual files and any
// catalog items that no longer have a physical or virtual file. Filters are
// optional and make administrative cleanup safe to scope.
func (r *ItemRepository) PurgeVirtualPlaybackItems(ctx context.Context, opts VirtualPurgeOptions) (VirtualPurgeResult, error) {
	var result VirtualPurgeResult
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		result, err = r.purgeVirtualPlaybackItemsOnce(ctx, opts)
		if err == nil || !isPGTransient(err) {
			return result, err
		}
		time.Sleep(time.Duration(200*(attempt+1)) * time.Millisecond)
	}
	return result, err
}

func (r *ItemRepository) purgeVirtualPlaybackItemsOnce(ctx context.Context, opts VirtualPurgeOptions) (VirtualPurgeResult, error) {
	var result VirtualPurgeResult
	if r == nil || r.pool == nil {
		return result, errors.New("item repository is not configured")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin virtual playback purge: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Exclusive purge barrier: writers hold this shared so concurrent
	// upserts, registrations, and materializations proceed in parallel.
	// The purge takes exclusive to block all of them while it deletes
	// media_files (firing the episode_catalog_entries trigger).
	if err := requestlock.LockPurgeBarrierExclusive(ctx, tx); err != nil {
		return result, fmt.Errorf("lock purge barrier: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE purge_virtual_items ON COMMIT DROP AS
		SELECT DISTINCT COALESCE(NULLIF(mf.content_id, ''), ep.series_id) AS content_id
		FROM media_files mf
		LEFT JOIN episodes ep ON ep.content_id = mf.episode_id
		LEFT JOIN media_items mi ON mi.content_id = COALESCE(NULLIF(mf.content_id, ''), ep.series_id)
		WHERE (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%')
		  AND ($1 = 0 OR mf.media_folder_id = $1)
		  AND ($2 = 0 OR mf.virtual_owner_installation_id = $2 OR (mf.virtual_owner_installation_id = 0 AND mi.virtual_owner_installation_id = $2))
		UNION
		SELECT DISTINCT mi.content_id FROM media_items mi
		WHERE mi.virtual_owner_installation_id IS NOT NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      LEFT JOIN episodes ep ON ep.content_id = mf.episode_id
		      WHERE (mf.content_id = mi.content_id OR ep.series_id = mi.content_id)
		        AND (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%')
		  )
		  AND ($1 = 0 OR EXISTS (
		      SELECT 1 FROM media_item_libraries mil
		      WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $1
		  ))
		  AND ($2 = 0 OR mi.virtual_owner_installation_id = $2)
		UNION
		SELECT DISTINCT mi.content_id FROM media_items mi
		WHERE ($2 = 0 OR mi.virtual_owner_installation_id = $2)
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      LEFT JOIN episodes ep ON ep.content_id = mf.episode_id
		      WHERE mf.content_id = mi.content_id OR ep.series_id = mi.content_id
		  )
		  AND ($1 = 0 OR EXISTS (
		      SELECT 1 FROM media_item_libraries mil
		      WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $1
		  ))
		  -- A file-less item is only virtual-purge material when a plugin owns
		  -- it. Items with no owner and no files are local catalog content
		  -- (mid-rescan, metadata-only, or not-yet-scanned) and must survive.
		  AND mi.virtual_owner_installation_id IS NOT NULL`, opts.LibraryID, opts.InstallationID); err != nil {
		return result, fmt.Errorf("identify virtual media items: %w", err)
	}
	// Source claims are independent catalog ownership records, not foreign
	// keys to media_files. Remove the exact claims for files selected by this
	// purge first so retained local items do not keep ghost plugin ownership.
	if _, err := tx.Exec(ctx, `
		DELETE FROM virtual_media_file_source_claims claim
		USING media_files mf
		LEFT JOIN episodes ep ON ep.content_id = mf.episode_id
		LEFT JOIN media_items mi ON mi.content_id = COALESCE(NULLIF(mf.content_id, ''), ep.series_id)
		WHERE claim.content_id = COALESCE(NULLIF(mf.content_id, ''), ep.series_id)
		  AND claim.media_folder_id = mf.media_folder_id
		  AND claim.file_path = mf.file_path
		  AND (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%')
		  AND claim.content_id IN (SELECT content_id FROM purge_virtual_items)
		  AND ($1 = 0 OR mf.media_folder_id = $1)
		  AND ($2 = 0 OR mf.virtual_owner_installation_id = $2
		      OR (mf.virtual_owner_installation_id = 0 AND mi.virtual_owner_installation_id = $2))
		  AND ($2 = 0 OR claim.plugin_installation_id = $2)`,
		opts.LibraryID, opts.InstallationID); err != nil {
		return result, fmt.Errorf("delete virtual file source claims: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM virtual_media_source_claims claim
		WHERE claim.content_id IN (SELECT content_id FROM purge_virtual_items)
		  AND ($1 = 0 OR claim.media_folder_id = $1)
		  AND ($2 = 0 OR claim.plugin_installation_id = $2)
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims file_claim
		      WHERE file_claim.plugin_installation_id = claim.plugin_installation_id
		        AND file_claim.source_key = claim.source_key
		        AND file_claim.content_id = claim.content_id
		        AND file_claim.media_folder_id = claim.media_folder_id
		  )`, opts.LibraryID, opts.InstallationID); err != nil {
		return result, fmt.Errorf("delete empty virtual source claims: %w", err)
	}
	// Acquire the candidate file rows in deterministic PK order first.
	// Postgres DELETE has no ORDER BY, so concurrent deleters would
	// otherwise lock rows in scan order and can deadlock against each
	// other (and against reconcile writers). Locking via an ordered
	// SELECT ... FOR UPDATE makes every deleter take row locks in the
	// same sequence; the DELETE below then re-locks rows already held.
	if _, err := tx.Exec(ctx, `
		SELECT 1 FROM media_files mf
		WHERE (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%')
		  AND ($1 = 0 OR mf.media_folder_id = $1)
		  AND ($2 = 0 OR mf.virtual_owner_installation_id = $2
		      OR (mf.virtual_owner_installation_id = 0 AND EXISTS (
		          SELECT 1 FROM media_items mi
		          LEFT JOIN episodes ep ON ep.content_id = mf.episode_id
		          WHERE (mi.content_id = mf.content_id OR mi.content_id = ep.series_id)
		            AND mi.virtual_owner_installation_id = $2
		      )))
		  AND ($2 = 0 OR NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims remaining
		      WHERE remaining.content_id = COALESCE(NULLIF(mf.content_id, ''), (SELECT ep.series_id FROM episodes ep WHERE ep.content_id = mf.episode_id))
		        AND remaining.media_folder_id = mf.media_folder_id
		        AND remaining.file_path = mf.file_path
		  ))
		ORDER BY mf.id
		FOR UPDATE`, opts.LibraryID, opts.InstallationID); err != nil {
		return result, fmt.Errorf("lock virtual files: %w", err)
	}
	fileResult, err := tx.Exec(ctx, `
		DELETE FROM media_files mf
		WHERE (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%')
		  AND ($1 = 0 OR mf.media_folder_id = $1)
		  AND ($2 = 0 OR mf.virtual_owner_installation_id = $2
		      OR (mf.virtual_owner_installation_id = 0 AND EXISTS (
		          SELECT 1 FROM media_items mi
		          LEFT JOIN episodes ep ON ep.content_id = mf.episode_id
		          WHERE (mi.content_id = mf.content_id OR mi.content_id = ep.series_id)
		            AND mi.virtual_owner_installation_id = $2
		      )))
	  AND ($2 = 0 OR NOT EXISTS (
	      SELECT 1 FROM virtual_media_file_source_claims remaining
	      WHERE remaining.content_id = COALESCE(NULLIF(mf.content_id, ''), (SELECT ep.series_id FROM episodes ep WHERE ep.content_id = mf.episode_id))
	        AND remaining.media_folder_id = mf.media_folder_id
	        AND remaining.file_path = mf.file_path
	  ))`, opts.LibraryID, opts.InstallationID)
	if err != nil {
		return result, fmt.Errorf("delete virtual files: %w", err)
	}
	result.FilesDeleted = fileResult.RowsAffected()

	if _, err := tx.Exec(ctx, `
		WITH chosen AS (
			SELECT DISTINCT ON (claim.content_id)
				claim.content_id,claim.plugin_installation_id,claim.source_key,
				claim.media_folder_id
			FROM virtual_media_source_claims claim
			WHERE claim.content_id IN (SELECT content_id FROM purge_virtual_items)
			  AND NOT EXISTS (
			      SELECT 1 FROM media_files physical
			      LEFT JOIN episodes ep ON ep.content_id = physical.episode_id
			      WHERE (physical.content_id = claim.content_id OR ep.series_id = claim.content_id)
			        AND (physical.container IS NULL OR physical.container <> 'virtual')
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
		return result, fmt.Errorf("reconcile remaining virtual ownership: %w", err)
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
		return result, fmt.Errorf("restore remaining virtual ownership: %w", err)
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
		return result, fmt.Errorf("clear purged virtual ownership: %w", err)
	}

	rows, err := tx.Query(ctx, `
		DELETE FROM media_items mi
		WHERE EXISTS (SELECT 1 FROM purge_virtual_items pvi WHERE pvi.content_id = mi.content_id)
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      LEFT JOIN episodes ep ON ep.content_id = mf.episode_id
		      WHERE mf.content_id = mi.content_id OR ep.series_id = mi.content_id
		  )
		RETURNING mi.content_id`)
	if err != nil {
		return result, fmt.Errorf("delete orphaned virtual media items: %w", err)
	}
	deletedIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return result, fmt.Errorf("collect deleted virtual media IDs: %w", err)
	}
	result.ItemsDeleted = int64(len(deletedIDs))
	if opts.DryRun {
		// Dry-run previews catalog deletions only; the state and collection
		// sweeps below are commit-time cleanup and are intentionally not
		// counted here (they would require read-only COUNT queries to
		// preview). See VirtualPurgeResult.
		return result, nil
	}
	// Remove orphaned library memberships: items that have no files and no
	// virtual owner should not appear in any library's browse results.
	if _, err := tx.Exec(ctx, `
		DELETE FROM media_item_libraries mil
		WHERE mil.content_id IN (SELECT content_id FROM purge_virtual_items)
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      LEFT JOIN episodes ep ON ep.content_id = mf.episode_id
		      WHERE mf.content_id = mil.content_id OR ep.series_id = mil.content_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM media_items mi
		      WHERE mi.content_id = mil.content_id
		        AND mi.virtual_owner_installation_id IS NOT NULL
		        AND mi.virtual_owner_installation_id != 0
		  )`); err != nil {
		return result, fmt.Errorf("clean orphaned library memberships: %w", err)
	}
	if err := EnqueueSearchIndexDeletes(ctx, tx, deletedIDs); err != nil {
		return result, fmt.Errorf("enqueue virtual media search deletes: %w", err)
	}

	// Per-user consumption state references content by bare ID with no
	// foreign key, so removing catalog entities strands Continue Watching,
	// history, favorites, and rating rows pointing at items the server no
	// longer has — exactly why purged titles kept showing up after an admin
	// purge. An explicit purge is not an undo surface, so the dead references
	// go with the content. Curated references (library collections above)
	// deliberately survive. The sweep is orphan-based rather than scoped to
	// this pass's IDs so it also heals residue from earlier partial removals.
	for _, stmt := range []string{
		`DELETE FROM user_watch_progress p WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = p.media_item_id)`,
		`DELETE FROM user_watch_history p WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = p.media_item_id)`,
		`DELETE FROM admin_playback_history p WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = p.media_item_id)`,
		`DELETE FROM user_history_hidden_items p WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = p.media_item_id)`,
		`DELETE FROM user_favorites p WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = p.media_item_id)`,
		`DELETE FROM user_ratings p WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = p.media_item_id)`,
		`DELETE FROM user_watchlist p WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = p.media_item_id)`,
		`DELETE FROM metadata_refresh_debt p WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = p.content_id)`,
	} {
		stateResult, err := tx.Exec(ctx, stmt)
		if err != nil {
			return result, fmt.Errorf("purge dangling playback state rows: %w", err)
		}
		result.StateRowsDeleted += stateResult.RowsAffected()
	}

	pruneResult, err := tx.Exec(ctx, `
		DELETE FROM library_collection_items lci
		WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = lci.media_item_id)`)
	if err != nil {
		return result, fmt.Errorf("clean virtual collection links: %w", err)
	}
	result.CollectionsPruned += pruneResult.RowsAffected()
	// Links in personal (per-user) collections have no foreign key to
	// media_items, so the orphan delete above cannot reach them. A purged
	// title must not linger on a Home collection rail via a dangling
	// personal-collection reference. Links to surviving (physical) items
	// are intentionally kept.
	pruneResult, err = tx.Exec(ctx, `
		DELETE FROM user_personal_collection_items upci
		WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = upci.media_item_id)`)
	if err != nil {
		return result, fmt.Errorf("clean virtual personal collection links: %w", err)
	}
	result.CollectionsPruned += pruneResult.RowsAffected()
	// Recommendation rows cache item IDs as JSONB with no foreign key, and
	// their cleanup is otherwise expiry-only. Drop cache rows whose source
	// item is gone and prune purged IDs out of surviving rows so Home
	// recommendation rails cannot serve ghosts.
	recResult, err := tx.Exec(ctx, `
		DELETE FROM recommendation_cache rc
		WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = rc.source_item_id)`)
	if err != nil {
		return result, fmt.Errorf("clean virtual recommendation sources: %w", err)
	}
	result.CollectionsPruned += recResult.RowsAffected()
	// Evict malformed cache rows (items not a JSON array — NULL, object,
	// scalar) before pruning. jsonb_array_elements errors on non-arrays,
	// which would abort the purge transaction; these derived rows are not
	// worth keeping.
	if len(deletedIDs) > 0 {
		malformedResult, err := tx.Exec(ctx, `
			DELETE FROM recommendation_cache rc
			WHERE jsonb_typeof(rc.items) IS DISTINCT FROM 'array'`)
		if err != nil {
			return result, fmt.Errorf("evict malformed recommendation rows: %w", err)
		}
		result.CollectionsPruned += malformedResult.RowsAffected()
		// Remove pre-existing empty rows first so the prune below only
		// touches rows that reference a purged ID; every row the UPDATE
		// touches is then attributable to this purge.
		preEmptied, err := tx.Exec(ctx, `
			DELETE FROM recommendation_cache rc
			WHERE rc.items = '[]'::jsonb`)
		if err != nil {
			return result, fmt.Errorf("clean pre-existing empty recommendation rows: %w", err)
		}
		result.CollectionsPruned += preEmptied.RowsAffected()
		// Both array expansions substitute an empty array for non-arrays via
		// CASE, so each is intrinsically safe regardless of predicate
		// evaluation order: a concurrent writer inserting a non-array row
		// mid-purge can neither abort the transaction (jsonb_array_elements
		// errors on non-arrays) nor be miscounted. Only rows the UPDATE
		// touches are counted; rows it empties are removed below without an
		// additional count.
		prunedRows, err := tx.Exec(ctx, `
			UPDATE recommendation_cache rc
			SET items = (
				SELECT COALESCE(jsonb_agg(elem), '[]'::jsonb)
				FROM jsonb_array_elements(CASE WHEN jsonb_typeof(rc.items) = 'array' THEN rc.items ELSE '[]'::jsonb END) AS elem
				WHERE elem ->> 'media_item_id' <> ALL($1::text[])
			)
			WHERE EXISTS (
				SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(rc.items) = 'array' THEN rc.items ELSE '[]'::jsonb END) AS elem
				WHERE elem ->> 'media_item_id' = ANY($1::text[])
			)`,
			deletedIDs)
		if err != nil {
			return result, fmt.Errorf("prune virtual recommendation items: %w", err)
		}
		result.CollectionsPruned += prunedRows.RowsAffected()
		if _, err := tx.Exec(ctx, `
			DELETE FROM recommendation_cache rc
			WHERE jsonb_typeof(rc.items) = 'array' AND rc.items = '[]'::jsonb`); err != nil {
			return result, fmt.Errorf("clean emptied recommendation rows: %w", err)
		}
	}
	// Defensive orphan sweep: episodes and seasons normally cascade when
	// their series media_items row is deleted. This heals residue from
	// earlier partial removals. Rows belonging to a retained series (mixed
	// local+virtual, or a library-scoped purge) are never touched: file-less
	// episode metadata can be legitimate.
	for _, stmt := range []string{
		`DELETE FROM episodes ep WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = ep.series_id)`,
		`DELETE FROM seasons s WHERE NOT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = s.series_id)`,
	} {
		orphanResult, err := tx.Exec(ctx, stmt)
		if err != nil {
			return result, fmt.Errorf("purge orphaned virtual episode rows: %w", err)
		}
		result.StateRowsDeleted += orphanResult.RowsAffected()
	}
	// Persist the purge revision transactionally with the deletion so other
	// API replicas can reconcile via the shared revision even if the
	// pub/sub event is lost. The revision is bumped once per committed purge.
	// The new value is returned so callers can carry it on the event payload.
	var newRevision int64
	if err := tx.QueryRow(ctx, `
		UPDATE catalog_purge_revision SET revision = revision + 1, updated_at = NOW() WHERE id = 1 RETURNING revision`).Scan(&newRevision); err != nil {
		return result, fmt.Errorf("bump purge revision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit virtual playback purge: %w", err)
	}
	result.PurgeRevision = newRevision
	return result, nil
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
		if err := requestlock.LockItem(ctx, tx, lockKey); err != nil {
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
		  )`+virtualFileRetentionGuard); err != nil {
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
		  )`+virtualFileRetentionGuard); err != nil {
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
		  AND NOT EXISTS (SELECT 1 FROM virtual_media_source_claims claim WHERE claim.content_id=mi.content_id)`+virtualItemRetentionGuard+`
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

func (r *ItemRepository) CountVirtualPlaybackEpisodeFiles(ctx context.Context, seriesID string) (int, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("item repository is not configured")
	}
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM media_files
		WHERE episode_id IN (SELECT content_id FROM episodes WHERE series_id=$1)
		  AND container='virtual'
		  AND virtual_owner_installation_id IS NOT NULL`, seriesID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count virtual episode files: %w", err)
	}
	return count, nil
}

func (r *ItemRepository) FindCollectionItemsMissingVirtualBase(ctx context.Context, collectionID string, limit int) ([]*models.MediaItem, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("item repository is not configured")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT mi.content_id, mi.type, mi.title, COALESCE(mi.sort_title, ''), COALESCE(mi.year, 0),
		       COALESCE(mi.imdb_id, ''), COALESCE(mi.tmdb_id, ''), COALESCE(mi.tvdb_id, ''), mi.status
		FROM library_collection_items lci
		JOIN media_items mi ON mi.content_id = lci.media_item_id
		JOIN library_collections lc ON lc.id = lci.collection_id
		WHERE lci.collection_id = $1
		  AND mi.type IN ('movie', 'series')
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      WHERE mf.content_id = mi.content_id
		        AND COALESCE(mf.container, '') <> 'virtual'
		        AND mf.file_path NOT LIKE 'virtual://%'
		  )
		  AND (
		      NOT EXISTS (
		          SELECT 1 FROM media_files mf
		          JOIN media_folders f ON f.id = mf.media_folder_id AND f.enabled IS NOT FALSE
		          WHERE mf.content_id = mi.content_id
		            AND mf.episode_id IS NULL
		            AND mf.container = 'virtual'
		            AND mf.virtual_owner_installation_id IS NOT NULL
		            AND (f.id IN (SELECT library_id FROM library_collection_libraries WHERE collection_id = $1) OR f.id = lc.library_id)
		      )
		      OR NOT EXISTS (
		          SELECT 1 FROM virtual_media_source_claims sc
		          WHERE sc.content_id = mi.content_id
		            AND sc.source_key = ('collection:' || $1)
		            AND (sc.media_folder_id IN (SELECT library_id FROM library_collection_libraries WHERE collection_id = $1) OR sc.media_folder_id = lc.library_id)
		      )
		      OR NOT EXISTS (
		          SELECT 1
		          FROM virtual_media_file_source_claims fc
		          JOIN media_files mf ON mf.content_id = fc.content_id
		                               AND mf.media_folder_id = fc.media_folder_id
		                               AND mf.file_path = fc.file_path
		                               AND mf.episode_id IS NULL
		          WHERE fc.content_id = mi.content_id
		            AND fc.source_key = ('collection:' || $1)
		            AND (fc.media_folder_id IN (SELECT library_id FROM library_collection_libraries WHERE collection_id = $1) OR fc.media_folder_id = lc.library_id)
		      )
		      OR (
		          mi.type = 'series'
		          AND EXISTS (
		              SELECT 1 FROM episodes ep
		              WHERE ep.series_id = mi.content_id
		                AND ep.season_number > 0
		                AND ep.episode_number > 0
		                AND verified_episode_release_at(ep.series_id,ep.season_number,ep.episode_number,ep.air_date::timestamp AT TIME ZONE 'UTC') <= statement_timestamp()
		                AND (
		                    NOT EXISTS (
		                        SELECT 1 FROM media_files mf
		                        WHERE mf.episode_id = ep.content_id
		                          AND mf.container = 'virtual'
		                          AND mf.virtual_owner_installation_id IS NOT NULL
		                    )
		                    OR NOT EXISTS (
		                        SELECT 1 FROM virtual_media_file_source_claims fc
		                        WHERE fc.content_id = mi.content_id
		                          AND fc.source_key = ('collection:' || $1)
		                          AND fc.file_path LIKE ('virtual://series/%/' || ep.season_number || '/' || ep.episode_number || '%')
		                    )
		                )
		          )
	      )
	      OR mi.virtual_reconciliation_attempted_at IS NULL
	      OR mi.virtual_reconciliation_attempted_at < NOW() - INTERVAL '5 minutes'
	  )
		ORDER BY COALESCE(mi.virtual_reconciliation_attempted_at, '1970-01-01'::timestamptz) ASC, lci.updated_at ASC, lci.position ASC
		LIMIT $2`, collectionID, limit)
	if err != nil {
		return nil, fmt.Errorf("find collection items missing virtual base: %w", err)
	}
	defer rows.Close()

	var items []*models.MediaItem
	for rows.Next() {
		item := &models.MediaItem{}
		if err := rows.Scan(
			&item.ContentID, &item.Type, &item.Title, &item.SortTitle, &item.Year,
			&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Status,
		); err != nil {
			return nil, fmt.Errorf("scan collection item missing virtual base: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *ItemRepository) ItemNeedsVirtualMaterialization(ctx context.Context, contentID string) (bool, error) {
	if r == nil || r.pool == nil {
		return false, errors.New("item repository is not configured")
	}
	var needs bool
	err := r.pool.QueryRow(ctx, `
		SELECT (
			NOT EXISTS (
				SELECT 1 FROM media_files mf
				WHERE mf.content_id = $1
				  AND COALESCE(mf.container, '') <> 'virtual'
				  AND mf.file_path NOT LIKE 'virtual://%'
			)
			AND (
				NOT EXISTS (
					SELECT 1 FROM media_files mf
					WHERE mf.content_id = $1
					  AND mf.episode_id IS NULL
					  AND mf.container = 'virtual'
					  AND mf.virtual_owner_installation_id IS NOT NULL
				)
				OR (
					EXISTS (
						SELECT 1 FROM media_items mi
						WHERE mi.content_id = $1 AND mi.type = 'series'
					)
					AND EXISTS (
						SELECT 1 FROM episodes ep
						WHERE ep.series_id = $1
						  AND ep.season_number > 0
						  AND ep.episode_number > 0
						  AND verified_episode_release_at(ep.series_id,ep.season_number,ep.episode_number,ep.air_date::timestamp AT TIME ZONE 'UTC') <= statement_timestamp()
						  AND NOT EXISTS (
							SELECT 1 FROM media_files mf
							WHERE mf.episode_id = ep.content_id
							  AND mf.container = 'virtual'
							  AND mf.virtual_owner_installation_id IS NOT NULL
						  )
					)
				)
			)
		)`, contentID).Scan(&needs)
	if err != nil {
		return false, fmt.Errorf("check if item needs virtual materialization: %w", err)
	}
	return needs, nil
}

func (r *ItemRepository) CollectionItemNeedsVirtualMaterialization(ctx context.Context, collectionID, contentID string) (bool, error) {
	if r == nil || r.pool == nil {
		return false, errors.New("item repository is not configured")
	}
	var needs bool
	err := r.pool.QueryRow(ctx, `
		SELECT (
			NOT EXISTS (
				SELECT 1 FROM media_files mf
				WHERE mf.content_id = $1
				  AND COALESCE(mf.container, '') <> 'virtual'
				  AND mf.file_path NOT LIKE 'virtual://%'
			)
			AND (
				NOT EXISTS (
					SELECT 1 FROM media_files mf
					JOIN media_folders f ON f.id = mf.media_folder_id AND f.enabled IS NOT FALSE
					WHERE mf.content_id = $1
					  AND mf.episode_id IS NULL
					  AND mf.container = 'virtual'
					  AND mf.virtual_owner_installation_id IS NOT NULL
					  AND (
					      f.id IN (SELECT library_id FROM library_collection_libraries WHERE collection_id = $2)
					      OR f.id IN (SELECT library_id FROM library_collections WHERE id = $2)
					  )
				)
				OR NOT EXISTS (
					SELECT 1 FROM virtual_media_source_claims sc
					WHERE sc.content_id = $1
					  AND sc.source_key = ('collection:' || $2)
					  AND (
					      sc.media_folder_id IN (SELECT library_id FROM library_collection_libraries WHERE collection_id = $2)
					      OR sc.media_folder_id IN (SELECT library_id FROM library_collections WHERE id = $2)
					  )
				)
				OR NOT EXISTS (
					SELECT 1 FROM virtual_media_file_source_claims fc
					JOIN media_files mf ON mf.content_id = fc.content_id
					                     AND mf.media_folder_id = fc.media_folder_id
					                     AND mf.file_path = fc.file_path
					                     AND mf.episode_id IS NULL
					WHERE fc.content_id = $1
					  AND fc.source_key = ('collection:' || $2)
					  AND (
					      fc.media_folder_id IN (SELECT library_id FROM library_collection_libraries WHERE collection_id = $2)
					      OR fc.media_folder_id IN (SELECT library_id FROM library_collections WHERE id = $2)
					  )
				)
				OR (
					EXISTS (
						SELECT 1 FROM media_items mi
						WHERE mi.content_id = $1 AND mi.type = 'series'
					)
					AND EXISTS (
						SELECT 1 FROM episodes ep
						WHERE ep.series_id = $1
						  AND ep.season_number > 0
						  AND ep.episode_number > 0
						  AND verified_episode_release_at(ep.series_id,ep.season_number,ep.episode_number,ep.air_date::timestamp AT TIME ZONE 'UTC') <= statement_timestamp()
						  AND (
							NOT EXISTS (
								SELECT 1 FROM media_files mf
								JOIN media_folders f ON f.id = mf.media_folder_id AND f.enabled IS NOT FALSE
								WHERE mf.episode_id = ep.content_id
								  AND mf.container = 'virtual'
								  AND mf.virtual_owner_installation_id IS NOT NULL
								  AND (
								      f.id IN (SELECT library_id FROM library_collection_libraries WHERE collection_id = $2)
								      OR f.id IN (SELECT library_id FROM library_collections WHERE id = $2)
								  )
							)
							OR NOT EXISTS (
								SELECT 1 FROM virtual_media_file_source_claims fc
								WHERE fc.content_id = $1
								  AND fc.source_key = ('collection:' || $2)
								  AND (
								      fc.media_folder_id IN (SELECT library_id FROM library_collection_libraries WHERE collection_id = $2)
								      OR fc.media_folder_id IN (SELECT library_id FROM library_collections WHERE id = $2)
								  )
								  AND fc.file_path LIKE ('virtual://series/%/' || ep.season_number || '/' || ep.episode_number || '%')
							)
						  )
					)
				)
			)
		)`, contentID, collectionID).Scan(&needs)
	if err != nil {
		return false, fmt.Errorf("check if collection item needs virtual materialization: %w", err)
	}
	return needs, nil
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

// A catalog search is interactive work. Bound the complete PostgreSQL FTS +
// fuzzy path independently of client cancellation so a pathological query can
// never consume a connection and CPU for minutes after the user has moved on.
const postgresSearchTimeout = 3 * time.Second

// A timed-out fuzzy query leaves its transaction needing a rollback, but the
// request context is already unusable at that point. Give cleanup its own
// short bound so the pool can reuse the connection instead of discarding it;
// this is cleanup-only and cannot extend the search response deadline.
const postgresSearchRollbackTimeout = time.Second

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

// fuzzyCandidatePool is how many trigram candidates, in trigram rank order, the
// fallback scores before admitFuzzyRanks keeps the titles matching every word.
// Whole-query trigram similarity favors short titles, so a long title matching
// every word ranks low: among 664 candidates for "brekng bad" in a
// 190,000-title catalog, "Snow Globe: A Breaking Bad Short" ranked 184th and
// two Breaking Bad documentaries 496th and 497th. The trigram scan dominates the
// query's cost; a larger LIMIT adds only row transfer and in-process scoring.
const fuzzyCandidatePool = 500

// fuzzyRerankAliasesPerOrder bounds the only extra metadata the in-process
// typo reranker may read: per item, this many aliases by similarity and this
// many by word coverage (see loadFuzzyRerankAliases). Candidate rows are
// already capped by fuzzyCandidatePool, so its worst case is 500 items / 4,000
// relevant aliases regardless of total library size.
const fuzzyRerankAliasesPerOrder = 4

const (
	fuzzyRerankMaxQueryTokens = 16
	fuzzyRerankMaxTitleTokens = 64
	fuzzyRerankMaxTokenRunes  = 64
	fuzzyRerankMaxTitleBytes  = 1024

	// maxVirtualEpisodeReconciliationBatch caps the number of episode
	// candidates evaluated per reconciliation pass to bound the lock and
	// override-capture batch size. Episodes beyond the budget wait for the
	// next reconciliation cycle.
	maxVirtualEpisodeReconciliationBatch = 500
)

// trgmWordSimilarityThreshold pins pg_trgm.strict_word_similarity_threshold
// for the fuzzy query via SET LOCAL. The <<% operator's selectivity is
// otherwise governed by a cluster-wide GUC a DBA (or another application) can
// change, silently altering both match quality and scan cost per deployment —
// and its server default (0.5) would reject ordinary one-edit typos
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
// Advisory column names, shared by every select list and scan-order check in
// this package so a rename cannot drift between them.
const (
	advisoryAgeColumn    = "advisory_age"
	advisorySourceColumn = "advisory_source"
)

// Served-path columns of the banner, thumb, clear art and disc slots, shared
// by the item select lists in this package.
const (
	bannerPathColumn   = "banner_path"
	thumbPathColumn    = "thumb_path"
	clearArtPathColumn = "clearart_path"
	discPathColumn     = "disc_path"
)

var itemColumnNames = []string{
	"content_id", "type", "title", "sort_title", "default_metadata_language", "original_title", "year", "genres",
	"content_rating", "runtime", "overview", "tagline",
	"rating_imdb", "rating_tmdb", "rating_rt_critic", "rating_rt_audience",
	"imdb_id", "tmdb_id", "tvdb_id",
	"poster_path", "poster_source_path", "poster_thumbhash", "backdrop_path", "backdrop_source_path", "backdrop_thumbhash", "logo_path", "logo_source_path",
	bannerPathColumn, "banner_source_path", "banner_thumbhash", thumbPathColumn, "thumb_source_path", "thumb_thumbhash",
	clearArtPathColumn, "clearart_source_path", discPathColumn, "disc_source_path",
	"metadata_s3_path", "metadata_etag", "season_count",
	"studios", "networks", "countries", "keywords", "original_language", "release_date::text", "first_air_date", "last_air_date", "air_time", "air_timezone",
	"show_status",
	"matched_at", "last_refreshed", "refresh_failures",
	"episode_metadata_incomplete", "episode_metadata_last_checked_at", "locked_fields", "status", "created_at", "updated_at",
	advisoryAgeColumn, advisorySourceColumn,
	"digital_release_date::text", "physical_release_date::text",
}

// nullableStringItemColumns are media_items columns that may hold NULL but
// scan into plain (non-pointer) string fields on models.MediaItem, so select
// lists coalesce them to ”.
var nullableStringItemColumns = map[string]bool{
	advisorySourceColumn:   true,
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
		&item.BannerPath,
		&item.BannerSourcePath,
		&item.BannerThumbhash,
		&item.ThumbPath,
		&item.ThumbSourcePath,
		&item.ThumbThumbhash,
		&item.ClearArtPath,
		&item.ClearArtSourcePath,
		&item.DiscPath,
		&item.DiscSourcePath,
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
		&item.AdvisoryAge,
		&item.AdvisorySource,
		&item.DigitalReleaseDate,
		&item.PhysicalReleaseDate,
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
		&item.BannerPath,
		&item.BannerSourcePath,
		&item.BannerThumbhash,
		&item.ThumbPath,
		&item.ThumbSourcePath,
		&item.ThumbThumbhash,
		&item.ClearArtPath,
		&item.ClearArtSourcePath,
		&item.DiscPath,
		&item.DiscSourcePath,
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
		&item.AdvisoryAge,
		&item.AdvisorySource,
		&item.DigitalReleaseDate,
		&item.PhysicalReleaseDate,
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
	// Shared purge barrier: allows concurrent writers but excludes them
	// during the purge sweep. Protects against the AB-BA cycle where
	// DELETE fires the episode_catalog_entries trigger while this
	// transaction INSERTs into the same index.
	if err := requestlock.LockPurgeBarrierShared(ctx, tx); err != nil {
		return fmt.Errorf("lock purge barrier: %w", err)
	}

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

// InsertIfAbsent inserts a new media item and leaves an existing row with the
// same content_id untouched. It reports whether this call inserted the row, so
// concurrent creators of a deterministic content_id can tell the winner apart
// without overwriting metadata another writer already stored.
func (r *ItemRepository) InsertIfAbsent(ctx context.Context, item *models.MediaItem) (bool, error) {
	if r.searchIndexEvents.disabledByActiveProvider() {
		return r.writeItem(ctx, r.pool, item, false)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin media item insert tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	inserted, err := r.writeItem(ctx, tx, item, false)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit media item insert tx: %w", err)
	}
	return inserted, nil
}

// SetStatusUnlessMatched sets an item's status and leaves a matched item
// untouched. It reports whether a row changed, and returns ErrItemNotFound when
// no item has contentID. The condition is part of the UPDATE, so a match
// another writer stores at the same time is kept; reading the item and then
// calling Upsert would write the stale copy over it.
func (r *ItemRepository) SetStatusUnlessMatched(ctx context.Context, contentID, status string) (bool, error) {
	if r.searchIndexEvents.disabledByActiveProvider() {
		return r.setStatusUnlessMatched(ctx, r.pool, contentID, status)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin media item status tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	changed, err := r.setStatusUnlessMatched(ctx, tx, contentID, status)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit media item status tx: %w", err)
	}
	return changed, nil
}

// setStatusUnlessMatched's outer SELECT reads the snapshot taken before the
// UPDATE, so it finds an item the UPDATE changed as well as one it skipped.
func (r *ItemRepository) setStatusUnlessMatched(ctx context.Context, querier itemStatusQuerier, contentID, status string) (bool, error) {
	var exists, changed bool
	err := querier.QueryRow(ctx, `
		WITH updated AS (
			UPDATE media_items
			SET status = $2, updated_at = NOW()
			WHERE content_id = $1 AND lower(trim(status)) <> 'matched'
			RETURNING content_id
		)
		SELECT EXISTS (SELECT 1 FROM media_items WHERE content_id = $1), EXISTS (SELECT 1 FROM updated)`, contentID, status).Scan(&exists, &changed)
	if err != nil {
		return false, fmt.Errorf("setting media item status: %w", err)
	}
	if !exists {
		return false, ErrItemNotFound
	}
	if !changed {
		return false, nil
	}
	if err := r.searchIndexEvents.EnqueueUpsert(ctx, querier, contentID); err != nil {
		return false, fmt.Errorf("enqueueing catalog search upsert: %w", err)
	}
	return true, nil
}

type itemStatusQuerier interface {
	itemExecer
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (r *ItemRepository) upsert(ctx context.Context, execer itemExecer, item *models.MediaItem) error {
	_, err := r.writeItem(ctx, execer, item, true)
	return err
}

// itemArtworkAssignments keeps an item's artwork slots that an admin pick
// locked after the writer read the item; see lockedArtworkAssignments.
var itemArtworkAssignments = func() string {
	locked := imagesLockedSQL("media_items.locked_fields")
	slots := [][]string{
		{"poster_path", "poster_source_path", "poster_thumbhash"},
		{"backdrop_path", "backdrop_source_path", "backdrop_thumbhash"},
		{"logo_path", "logo_source_path"},
		{"banner_path", "banner_source_path", "banner_thumbhash"},
		{"thumb_path", "thumb_source_path", "thumb_thumbhash"},
		{"clearart_path", "clearart_source_path"},
		{"disc_path", "disc_source_path"},
	}
	assignments := make([]string, len(slots))
	for i, slot := range slots {
		assignments[i] = lockedArtworkAssignments("media_items", locked, slot...)
	}
	return strings.Join(assignments, ",\n\t\t\t")
}()

// writeItem inserts item and, when update is set, overwrites every mutable
// field of an existing row. It reports whether a row was written. An update
// that leaves the item matched also clears match_ignored_at, since an
// admin's ignore only means something while the item is unmatched.
func (r *ItemRepository) writeItem(ctx context.Context, execer itemExecer, item *models.MediaItem, update bool) (bool, error) {
	if item.ContentID == "" {
		return false, fmt.Errorf("refusing to write media item with empty content_id")
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
			episode_metadata_incomplete, episode_metadata_last_checked_at, status,
			content_rating_age,
			advisory_age, advisory_source,
			banner_path, banner_source_path, banner_thumbhash, thumb_path, thumb_source_path, thumb_thumbhash,
			clearart_path, clearart_source_path, disc_path, disc_source_path,
			digital_release_date, physical_release_date
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
			$45, $46, $47,
			$48,
			$49, $50,
			$51, $52, $53, $54, $55, $56,
			$57, $58, $59, $60,
			$61, $62
		)`
	conflict := `
		ON CONFLICT (content_id) DO NOTHING`
	if update {
		conflict = `
		ON CONFLICT (content_id) DO UPDATE SET
			type = EXCLUDED.type,
			title = EXCLUDED.title,
			sort_title = EXCLUDED.sort_title,
			default_metadata_language = COALESCE(NULLIF(EXCLUDED.default_metadata_language, ''), media_items.default_metadata_language),
			original_title = EXCLUDED.original_title,
			year = EXCLUDED.year,
			genres = EXCLUDED.genres,
			content_rating = EXCLUDED.content_rating,
			-- Audiobook runtime is maintained from active files. Preserve the
			-- current value even when a metadata request read an older one.
			runtime = CASE WHEN media_items.type = 'audiobook'
				THEN media_items.runtime ELSE EXCLUDED.runtime END,
			overview = EXCLUDED.overview,
			tagline = EXCLUDED.tagline,
			rating_imdb = EXCLUDED.rating_imdb,
			rating_tmdb = EXCLUDED.rating_tmdb,
			rating_rt_critic = EXCLUDED.rating_rt_critic,
			rating_rt_audience = EXCLUDED.rating_rt_audience,
			imdb_id = EXCLUDED.imdb_id,
			tmdb_id = EXCLUDED.tmdb_id,
			tvdb_id = EXCLUDED.tvdb_id,
			` + itemArtworkAssignments + `,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			season_count = EXCLUDED.season_count,
			studios = EXCLUDED.studios,
			networks = EXCLUDED.networks,
			countries = EXCLUDED.countries,
			keywords = EXCLUDED.keywords,
			original_language = EXCLUDED.original_language,
			release_date = EXCLUDED.release_date,
			digital_release_date = EXCLUDED.digital_release_date,
			physical_release_date = EXCLUDED.physical_release_date,
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
			match_ignored_at = CASE WHEN lower(trim(EXCLUDED.status)) = 'matched' THEN NULL ELSE media_items.match_ignored_at END,
			content_rating_age = EXCLUDED.content_rating_age,
			advisory_age = EXCLUDED.advisory_age,
			advisory_source = EXCLUDED.advisory_source,
			updated_at = NOW()`
	}

	// The stored age is derived here, never in SQL: access.Normalize is the one
	// ladder, and content_rating stays the verbatim provider string.
	contentRatingAge := access.StoredRating(item.ContentRating)
	advisoryAge, advisorySource := models.AdvisoryColumns(item.Type, item.AdvisoryAge, item.AdvisorySource)

	tag, err := execer.Exec(ctx, query+conflict,
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
		contentRatingAge,
		advisoryAge,
		advisorySource,
		item.BannerPath,
		item.BannerSourcePath,
		item.BannerThumbhash,
		item.ThumbPath,
		item.ThumbSourcePath,
		item.ThumbThumbhash,
		item.ClearArtPath,
		item.ClearArtSourcePath,
		item.DiscPath,
		item.DiscSourcePath,
		item.DigitalReleaseDate,
		item.PhysicalReleaseDate,
	)
	if err != nil {
		return false, fmt.Errorf("writing media item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	if err := r.searchIndexEvents.EnqueueUpsert(ctx, execer, item.ContentID); err != nil {
		return false, fmt.Errorf("enqueueing catalog search upsert: %w", err)
	}

	return true, nil
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
	if err := requestlock.LockItem(ctx, tx, item.ContentID); err != nil {
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
			// Synced collections (TMDB trending, MDBList lists) mix movies and
			// series while often targeting a single library, so an explicit
			// target that cannot host this media type must fall back to any
			// compatible folder — refusing here aborted the whole collection
			// sync on the first mismatched entry. Only when no compatible
			// folder exists anywhere is this genuinely unplaceable.
			if fbErr := tx.QueryRow(ctx, `
				SELECT id FROM media_folders
				WHERE enabled IS NOT FALSE AND type IN ($1, 'mixed')
				ORDER BY sort_order, id
				LIMIT 1`, wantedFolderType).Scan(&folderID); fbErr != nil {
				return false, fmt.Errorf("virtual playback item has no compatible %s library", mediaType)
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
	if len(variants) == 0 {
		return false, errors.New("virtual playback item requires an owning provider installation")
	}
	if ownerInstallationID == 0 {
		// Core-owned variants carry OwnerInstallationID 0: the virtual-library
		// plugin is retired, so persist under the core owner identity. Owner 0
		// is covered by the partial unique index like any plugin owner, and
		// playback dispatches owner<=0 to the core virtual library.
		ownerIDs = append(ownerIDs, 0)
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
		  )`+virtualFileRetentionGuard, item.ContentID, folderID, desiredOwners, desiredPaths); err != nil {
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
		SET virtual_source = 'collection', virtual_last_seen_at = NOW(), virtual_reconciliation_attempted_at = NOW(), updated_at = NOW()
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
	// Episode release locks precede the debt write below (see
	// lockReleaseContentTx): override mutation takes identity locks before
	// its own debt write, so every path orders locks before debt rows.
	if mediaType == "series" {
		if err := materializeVirtualPlaybackEpisodesTx(ctx, tx, item.ContentID); err != nil {
			return false, err
		}
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
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit virtual item transaction: %w", err)
	}
	return created, nil
}

// VirtualMaterializeOptions controls membership and validation rules during virtual materialization.
type VirtualMaterializeOptions struct {
	RequireMembership bool
	accepting         bool
	sourceConfig      json.RawMessage
	releaseSnapshot   []ReleaseOverride
	// preparedExplicitly marks that the caller explicitly evaluated release
	// overrides for this materialization. Nil snapshot + false means the
	// caller never prepared; nil snapshot + true would be invalid. Empty
	// snapshot + true means "prepared, no active override found."
	preparedExplicitly bool
	// deferDebtWrite skips the refresh-debt write so a multi-item caller
	// can emit all debt rows after every item's release locks and
	// eligibility decisions complete (see lockReleaseContentTx ordering).
	deferDebtWrite bool
}

// queueVirtualItemRefreshDebtTx records metadata-enrichment debt for one
// content item inside the caller's transaction.
func queueVirtualItemRefreshDebtTx(ctx context.Context, tx pgx.Tx, contentID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO metadata_refresh_debt (
			target_type, content_id, priority, reason_mask, next_refresh_at, updated_at
		) VALUES ('item', $1, 150, 8, NOW(), NOW())
		ON CONFLICT (target_type, content_id) DO UPDATE SET
			priority = GREATEST(metadata_refresh_debt.priority, EXCLUDED.priority),
			reason_mask = metadata_refresh_debt.reason_mask | EXCLUDED.reason_mask,
			next_refresh_at = LEAST(metadata_refresh_debt.next_refresh_at, EXCLUDED.next_refresh_at),
			updated_at = NOW()`, contentID)
	return err
}

// EnsureVirtualCollectionItemMaterialized ensures a collection-owned item has its
// base virtual files, profile variants, released episode files (for series), and
// collection claims atomically established.
func (r *ItemRepository) EnsureVirtualCollectionItemMaterialized(
	ctx context.Context,
	collectionID string,
	item *models.MediaItem,
	targetLibraryIDs []int,
	variants []VirtualPlaybackVariant,
) (*MaterializeResult, error) {
	return r.EnsureVirtualCollectionItemMaterializedWithOptions(ctx, collectionID, item, targetLibraryIDs, variants, VirtualMaterializeOptions{
		RequireMembership: true,
	})
}

// EnsureVirtualCollectionItemMaterializedWithOptions ensures a collection-owned item has its
// base virtual files, profile variants, released episode files (for series), and
// collection claims atomically established with configurable membership enforcement.
func (r *ItemRepository) EnsureVirtualCollectionItemMaterializedWithOptions(
	ctx context.Context,
	collectionID string,
	item *models.MediaItem,
	targetLibraryIDs []int,
	variants []VirtualPlaybackVariant,
	opts VirtualMaterializeOptions,
) (*MaterializeResult, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("item repository is not configured")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin virtual item transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Shared purge barrier: allows concurrent writers but excludes them
	// during the purge sweep. Taken before the per-item content lock so
	// both sides order the shared lock first.
	if err := requestlock.LockPurgeBarrierShared(ctx, tx); err != nil {
		return nil, fmt.Errorf("lock purge barrier: %w", err)
	}
	opts.RequireMembership = true
	result, err := r.ensureVirtualCollectionItemMaterializedTx(ctx, tx, collectionID, item, targetLibraryIDs, variants, opts)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit virtual item transaction: %w", err)
	}
	return result, nil
}

func (r *ItemRepository) ensureVirtualCollectionItemMaterializedTx(ctx context.Context, tx pgx.Tx, collectionID string, item *models.MediaItem, targetLibraryIDs []int, variants []VirtualPlaybackVariant, opts VirtualMaterializeOptions) (*MaterializeResult, error) {
	if item == nil || strings.TrimSpace(item.ContentID) == "" {
		return nil, errors.New("virtual playback item requires a content ID")
	}
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		return nil, errors.New("collection ID is required")
	}
	mediaType := strings.TrimSpace(item.Type)
	if mediaType != "movie" && mediaType != "series" {
		return nil, fmt.Errorf("unsupported media type %q for virtual materialization", mediaType)
	}
	virtualPath, err := virtualPlaybackItemURI(item)
	if err != nil {
		return nil, err
	}
	// Content lock first (see lockReleaseContentTx): alias writers take it
	// exclusively, so the alias set resolved below cannot change under us.
	if err := lockReleaseContentTx(ctx, tx, item.ContentID, false); err != nil {
		return nil, err
	}
	snapshot := opts.releaseSnapshot
	if opts.accepting {
		// Acceptance executes the decision made when the member was prepared;
		// an override first recorded afterwards does not invalidate it.
		snapshot = recordedReleaseOverrides(snapshot)
	}
	if err := validateReleaseSnapshotTx(ctx, tx, snapshot); err != nil {
		return nil, err
	}

	var (
		colLibraryID    int
		sourceConfigRaw []byte
	)
	if err := tx.QueryRow(ctx, `
		SELECT library_id, source_config
		FROM library_collections
		WHERE id = $1
		FOR SHARE`, collectionID).Scan(&colLibraryID, &sourceConfigRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLibraryCollectionNotFound
		}
		return nil, fmt.Errorf("locking collection: %w", err)
	}

	if len(opts.sourceConfig) > 0 {
		var same bool
		if err := tx.QueryRow(ctx, `SELECT $1::jsonb = $2::jsonb`, sourceConfigRaw, opts.sourceConfig).Scan(&same); err != nil {
			return nil, err
		}
		if !same {
			return nil, ErrCollectionSyncConfigurationChanged
		}
	}
	var sourceCfg struct {
		VirtualPlayback bool `json:"virtual_playback"`
	}
	if err := json.Unmarshal(sourceConfigRaw, &sourceCfg); err != nil || !sourceCfg.VirtualPlayback {
		return nil, ErrVirtualPlaybackDisabled
	}

	rows, err := tx.Query(ctx, `
		SELECT library_id
		FROM library_collection_libraries
		WHERE collection_id = $1
		ORDER BY library_id ASC`, collectionID)
	if err != nil {
		return nil, fmt.Errorf("reading collection target libraries: %w", err)
	}
	var activeLibraryIDs []int
	for rows.Next() {
		var lid int
		if err := rows.Scan(&lid); err != nil {
			rows.Close()
			return nil, err
		}
		activeLibraryIDs = append(activeLibraryIDs, lid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating collection target libraries: %w", err)
	}
	rows.Close()
	if len(activeLibraryIDs) == 0 && colLibraryID > 0 {
		activeLibraryIDs = []int{colLibraryID}
	}
	if len(activeLibraryIDs) == 0 {
		return nil, errors.New("collection has no target libraries configured")
	}

	var isMember bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM library_collection_items
			WHERE collection_id = $1 AND media_item_id = $2
		)`, collectionID, item.ContentID).Scan(&isMember); err != nil {
		return nil, fmt.Errorf("revalidate collection membership: %w", err)
	}
	if opts.RequireMembership && !isMember {
		return nil, ErrCollectionItemNotMember
	}
	if opts.accepting {
		isMember = true
	}

	// Lock 2: Lock canonical item advisory lock to serialize concurrent materializations of the same item.
	if err := requestlock.LockItem(ctx, tx, item.ContentID); err != nil {
		return nil, fmt.Errorf("lock canonical virtual item: %w", err)
	}

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
		LIMIT 1 FOR SHARE`, activeLibraryIDs, wantedFolderType).Scan(&folderID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w for %s", ErrIncompatibleLibrary, mediaType)
		}
		return nil, fmt.Errorf("selecting virtual playback library: %w", err)
	}

	// Lock 3: Row lock media_items to check existing type and preserve catalog invariants.
	// Fresh scalar IDs are loaded here (not trusted from the caller) so a
	// stale prepared item cannot smuggle removed aliases into the gate.
	var existingType, rowTmdbID, rowImdbID string
	err = tx.QueryRow(ctx, `SELECT type, COALESCE(tmdb_id,''), COALESCE(imdb_id,'') FROM media_items WHERE content_id=$1 FOR UPDATE`, item.ContentID).Scan(&existingType, &rowTmdbID, &rowImdbID)
	switch {
	case err == nil:
		if existingType != mediaType {
			return nil, fmt.Errorf("canonical virtual content ID belongs to media type %q", existingType)
		}
	case errors.Is(err, pgx.ErrNoRows):
		if err := r.UpsertTx(ctx, tx, item); err != nil {
			return nil, err
		}
		rowTmdbID, rowImdbID = item.TmdbID, item.ImdbID
	default:
		return nil, fmt.Errorf("checking canonical virtual item: %w", err)
	}

	// Authoritative movie gate: with the item row locked and alias writers
	// held out by the content lock, resolve the current full alias set and
	// require it to match the prepared snapshot. A changed membership is a
	// retryable conflict: removed aliases must not retain authority, and
	// added aliases must go through preparation, not silent union.
	// Explicit preparation is enforced below: any movie with stored release
	// identities must arrive prepared (override evaluation done). New items
	// with no stored aliases are exempt.
	if mediaType == "movie" {
		// The rows locked above are authoritative: a prepared item can carry
		// aliases enrichment has since removed, and those must not retain
		// authority at acceptance.
		movieIDs, err := releaseIdentitiesForContent(ctx, tx, "movie", item.ContentID, "movie", rowTmdbID, "", rowImdbID, 0, 0)
		if err != nil {
			return nil, err
		}
		// Enforce explicit preparation: callers that did not prepare must not reach
		// the override gate with stored identities. New items (no stored aliases)
		// are exempt.
		if !opts.preparedExplicitly && len(movieIDs) > 0 {
			return nil, fmt.Errorf("%w: movie has stored release identities but was not prepared with override evaluation", ErrReleaseOverrideConflict)
		}
		if opts.preparedExplicitly || len(movieIDs) > 0 {
			if len(opts.releaseSnapshot) > 0 && !releaseIdentitySetsEqual(snapshotIdentities(opts.releaseSnapshot), movieIDs) {
				return nil, fmt.Errorf("%w: release alias set changed after preparation", ErrReleaseOverrideConflict)
			}
			if opts.accepting {
				// Collection acceptance executes the decision made when its
				// members were prepared. A concurrent override write may land
				// after that snapshot, but it must not retroactively cancel
				// the acceptance; a changed alias set still conflicts above.
				if allowed, active := decideReleaseOverrides(time.Now().UTC(), opts.releaseSnapshot); active && !allowed {
					return nil, fmt.Errorf("%w: verified release override blocks materialization", ErrProviderUnavailable)
				}
			} else {
				fresh, err := captureReleaseOverridesTx(ctx, tx, movieIDs)
				if err != nil {
					return nil, err
				}
				if err := validateReleaseSnapshotTx(ctx, tx, fresh); err != nil {
					return nil, err
				}
				if allowed, active := decideReleaseOverrides(time.Now().UTC(), fresh); active && !allowed {
					return nil, fmt.Errorf("%w: verified release override blocks materialization", ErrProviderUnavailable)
				}
			}
		}
	}

	if isMember {
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
			VALUES ($1, $2, NOW()) ON CONFLICT DO NOTHING`, item.ContentID, folderID); err != nil {
			return nil, fmt.Errorf("linking virtual item to library: %w", err)
		}
	}

	ownerInstallationID := 0
	seenOwners := make(map[int]struct{}, len(variants))
	var ownerIDs []int64
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
	if len(variants) == 0 {
		return nil, fmt.Errorf("%w: virtual playback item requires an owning provider installation", ErrProviderUnavailable)
	}
	if ownerInstallationID == 0 {
		// Core-owned variants carry OwnerInstallationID 0: the virtual-library
		// plugin is retired, so persist under the core owner identity. Owner 0
		// is covered by the partial unique index like any plugin owner, and
		// playback dispatches owner<=0 to the core virtual library.
		ownerIDs = append(ownerIDs, 0)
	}

	type desiredFile struct {
		filePath   string
		ownerID    int64
		folderID   int
		resolution string
		codecVideo string
		codecAudio string
		hdr        bool
	}
	type desiredKey struct {
		filePath string
		ownerID  int64
		folderID int
	}
	seenDesired := make(map[desiredKey]struct{}, len(ownerIDs)+len(variants))
	var desired []desiredFile
	for _, ownerID := range ownerIDs {
		key := desiredKey{filePath: virtualPath, ownerID: ownerID, folderID: folderID}
		if _, exists := seenDesired[key]; exists {
			continue
		}
		seenDesired[key] = struct{}{}
		desired = append(desired, desiredFile{
			filePath: virtualPath,
			ownerID:  ownerID,
			folderID: folderID,
		})
	}
	for _, variant := range variants {
		path := strings.TrimSpace(variant.VirtualURI)
		if path == "" || path == virtualPath || !strings.HasPrefix(path, "virtual://") {
			continue
		}
		varOwner := int64(variant.OwnerInstallationID)
		if varOwner <= 0 {
			varOwner = int64(ownerInstallationID)
		}
		key := desiredKey{filePath: path, ownerID: varOwner, folderID: folderID}
		if _, exists := seenDesired[key]; exists {
			continue
		}
		seenDesired[key] = struct{}{}
		desired = append(desired, desiredFile{
			filePath:   path,
			ownerID:    varOwner,
			folderID:   folderID,
			resolution: variant.Resolution,
			codecVideo: variant.CodecVideo,
			codecAudio: variant.CodecAudio,
			hdr:        variant.HDR != "",
		})
	}

	filesCreated := 0
	filesExisting := 0
	collectionSourceKey := "collection:" + collectionID

	// Establish collection virtual source claims first (referenced by file claims foreign key).
	// Desired files/claims are created before any stale cleanup so a missing
	// claim is restored in place and existing file IDs are preserved.
	// Existing members stay active; only genuinely new work is staged.
	for _, ownerID := range ownerIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO virtual_media_source_claims(
				plugin_installation_id, source_key, content_id, media_folder_id,
				owns_item_metadata, last_seen_at, staged_until, updated_at
			) VALUES ($1, $2, $3, $4, false, NOW(), NULL, NOW())
			ON CONFLICT(plugin_installation_id, source_key, content_id, media_folder_id)
			DO UPDATE SET last_seen_at=EXCLUDED.last_seen_at, staged_until=EXCLUDED.staged_until, updated_at=NOW()`,
			ownerID, collectionSourceKey, item.ContentID, folderID,
		); err != nil {
			return nil, fmt.Errorf("claiming virtual media source: %w", err)
		}
	}

	for _, df := range desired {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM media_files
				WHERE file_path=$1 AND virtual_owner_installation_id=$2 AND media_folder_id=$3
			)`, df.filePath, df.ownerID, df.folderID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check media file existence: %w", err)
		}
		if exists {
			filesExisting++
		} else {
			filesCreated++
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO media_files (
				content_id, media_folder_id, file_path, file_size, resolution,
				codec_video, codec_audio, hdr, container, probe_source,
				virtual_owner_installation_id, missing_since
			) VALUES (
				$1, $2, $3, 0, NULLIF($4,''), NULLIF($5,''), NULLIF($6,''),
				$7, 'virtual', 'virtual_collection', $8,
				CASE WHEN $9 THEN NULL ELSE NOW() END
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
				missing_since=CASE WHEN $9 THEN NULL ELSE media_files.missing_since END,
				updated_at=NOW()`,
			item.ContentID, df.folderID, df.filePath, df.resolution, df.codecVideo, df.codecAudio, df.hdr, df.ownerID, isMember,
		); err != nil {
			return nil, fmt.Errorf("upserting virtual base file: %w", err)
		}

		// Atomically establish collection file source claims
		if _, err := tx.Exec(ctx, `
			INSERT INTO virtual_media_file_source_claims(
				plugin_installation_id, source_key, content_id, media_folder_id,
				file_path, last_seen_at, staged_until, updated_at
			) VALUES ($1, $2, $3, $4, $5, NOW(), NULL, NOW())
			ON CONFLICT(plugin_installation_id, source_key, content_id, media_folder_id, file_path)
			DO UPDATE SET last_seen_at=EXCLUDED.last_seen_at, staged_until=EXCLUDED.staged_until, updated_at=NOW()`,
			df.ownerID, collectionSourceKey, item.ContentID, df.folderID, df.filePath,
		); err != nil {
			return nil, fmt.Errorf("claiming virtual media file: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		WITH chosen AS (
			SELECT DISTINCT ON (claim.content_id)
				claim.content_id, claim.plugin_installation_id, claim.source_key, claim.media_folder_id
			FROM virtual_media_source_claims claim
			WHERE claim.content_id = $1
			  AND NOT EXISTS (
			      SELECT 1 FROM media_files physical
			      WHERE physical.content_id = claim.content_id
			        AND COALESCE(physical.container, '') <> 'virtual'
			        AND physical.file_path NOT LIKE 'virtual://%'
			  )
			ORDER BY claim.content_id, claim.owns_item_metadata DESC,
			         claim.last_seen_at DESC, claim.plugin_installation_id, claim.source_key
		)
		UPDATE virtual_media_source_claims claim
		SET owns_item_metadata = (
			chosen.plugin_installation_id = claim.plugin_installation_id
			AND chosen.source_key = claim.source_key
			AND chosen.media_folder_id = claim.media_folder_id
		), updated_at = NOW()
		FROM chosen
		WHERE claim.content_id = chosen.content_id`, item.ContentID); err != nil {
		return nil, fmt.Errorf("reconciling virtual metadata ownership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE virtual_media_source_claims claim
		SET owns_item_metadata = false, updated_at = NOW()
		WHERE claim.content_id = $1
		  AND EXISTS (
		      SELECT 1 FROM media_files physical
		      WHERE physical.content_id = claim.content_id
		        AND COALESCE(physical.container, '') <> 'virtual'
		        AND physical.file_path NOT LIKE 'virtual://%'
		  )`, item.ContentID); err != nil {
		return nil, fmt.Errorf("clearing virtual metadata ownership for local item: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE media_items
		SET virtual_owner_installation_id = NULL, virtual_source = '', virtual_last_seen_at = NULL, updated_at = NOW()
		WHERE content_id = $1
		  AND EXISTS (
		      SELECT 1 FROM media_files physical
		      WHERE physical.content_id = media_items.content_id
		        AND COALESCE(physical.container, '') <> 'virtual'
		        AND physical.file_path NOT LIKE 'virtual://%'
		  )`, item.ContentID); err != nil {
		return nil, fmt.Errorf("clearing virtual item ownership for local item: %w", err)
	}

	// Release stale files for this item in this folder that are no longer desired by this collection.
	// Desired rows were already upserted above, so this only removes genuinely
	// obsolete identities and preserves file IDs for repaired claims.
	desiredPaths := make([]string, 0, len(desired))
	desiredOwners := make([]int64, 0, len(desired))
	desiredFolders := make([]int, 0, len(desired))
	for _, df := range desired {
		desiredPaths = append(desiredPaths, df.filePath)
		desiredOwners = append(desiredOwners, df.ownerID)
		desiredFolders = append(desiredFolders, df.folderID)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM virtual_media_source_claims claim
		WHERE claim.content_id = $1
		  AND claim.source_key = $2
		  AND (claim.media_folder_id <> $4 OR NOT EXISTS (
		      SELECT 1
		      FROM unnest($3::bigint[]) AS desired(owner_id)
		      WHERE desired.owner_id = claim.plugin_installation_id
		  ))`, item.ContentID, collectionSourceKey, ownerIDs, folderID); err != nil {
		return nil, fmt.Errorf("releasing stale collection virtual source claims: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		WITH desired(owner_id, file_path, folder_id) AS (
			SELECT * FROM unnest($3::bigint[], $4::text[], $5::int[])
		), stale AS (
			SELECT mf.content_id, mf.media_folder_id, mf.file_path, mf.virtual_owner_installation_id
			FROM media_files mf
			WHERE mf.content_id = $1
			  AND mf.media_folder_id = $2
			  AND mf.episode_id IS NULL
			  AND mf.container = 'virtual'
			  AND mf.probe_source = 'virtual_collection'
			  AND mf.virtual_owner_installation_id IS NOT NULL
			  AND NOT EXISTS (
			      SELECT 1 FROM desired
			      WHERE desired.owner_id = mf.virtual_owner_installation_id
			        AND desired.file_path = mf.file_path
			        AND desired.folder_id = mf.media_folder_id
			  )
		)
		DELETE FROM virtual_media_file_source_claims claim
		USING stale
		WHERE claim.content_id = stale.content_id
		  AND claim.media_folder_id = stale.media_folder_id
		  AND claim.file_path = stale.file_path
		  AND claim.plugin_installation_id = stale.virtual_owner_installation_id
		  AND claim.source_key = $6`,
		item.ContentID, folderID, desiredOwners, desiredPaths, desiredFolders, collectionSourceKey); err != nil {
		return nil, fmt.Errorf("releasing stale collection virtual file claims: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		WITH desired(owner_id, file_path, folder_id) AS (
			SELECT * FROM unnest($3::bigint[], $4::text[], $5::int[])
		)
		DELETE FROM media_files mf
		WHERE mf.content_id = $1
		  AND mf.media_folder_id = $2
		  AND mf.episode_id IS NULL
		  AND mf.container = 'virtual'
		  AND mf.probe_source = 'virtual_collection'
		  AND mf.virtual_owner_installation_id IS NOT NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM desired
		      WHERE desired.owner_id = mf.virtual_owner_installation_id
		        AND desired.file_path = mf.file_path
		        AND desired.folder_id = mf.media_folder_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims claim
		      WHERE claim.content_id = mf.content_id
		        AND claim.media_folder_id = mf.media_folder_id
		        AND claim.file_path = mf.file_path
		        AND claim.plugin_installation_id = mf.virtual_owner_installation_id
		  )`+virtualFileRetentionGuard, item.ContentID, folderID, desiredOwners, desiredPaths, desiredFolders); err != nil {
		return nil, fmt.Errorf("removing stale collection virtual files: %w", err)
	}

	// Reconcile metadata authority with the shared routine so scalar
	// compatibility fields always agree with the elected owning claim.
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE IF NOT EXISTS affected_collection_virtual_items(
			content_id text PRIMARY KEY
		) ON COMMIT DROP`); err != nil {
		return nil, fmt.Errorf("create affected collection virtual items: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO affected_collection_virtual_items(content_id) VALUES($1)
		ON CONFLICT DO NOTHING`, item.ContentID); err != nil {
		return nil, fmt.Errorf("capture affected collection virtual item: %w", err)
	}
	if err := reconcileAffectedVirtualMetadataOwnership(ctx, tx); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE media_items
		SET virtual_reconciliation_attempted_at = NOW(), updated_at = NOW()
		WHERE content_id = $1`, item.ContentID); err != nil {
		return nil, fmt.Errorf("marking collection virtual reconciliation attempt: %w", err)
	}

	// Episode release locks precede the debt write below (see
	// lockReleaseContentTx): override mutation takes identity locks before
	// its own debt write, so every path orders locks before debt rows.
	episodesMaterialized := 0
	if mediaType == "series" {
		epCreated, epExisting, distinctEps, epErr := materializeVirtualPlaybackEpisodesDetailedTx(ctx, tx, item.ContentID, collectionID, isMember)
		if epErr != nil {
			return nil, epErr
		}
		filesCreated += epCreated
		filesExisting += epExisting
		episodesMaterialized = distinctEps
	}

	// Metadata refresh debt. Deferred by multi-item acceptance until every
	// item's release locks and decisions complete (see lockReleaseContentTx).
	if !opts.deferDebtWrite {
		if err := queueVirtualItemRefreshDebtTx(ctx, tx, item.ContentID); err != nil {
			return nil, fmt.Errorf("queueing virtual item metadata refresh: %w", err)
		}
	}
	return &MaterializeResult{
		ContentID:            item.ContentID,
		MediaType:            mediaType,
		FilesCreated:         filesCreated,
		FilesExisting:        filesExisting,
		EpisodesMaterialized: episodesMaterialized,
	}, nil
}

// CleanupUnreferencedCollectionVirtualItems releases only the claims owned by
// collectionID. Keeping the owner in the call prevents one failed sync from
// removing another collection's uncommitted work for the same catalog item.
func (r *ItemRepository) CleanupUnreferencedCollectionVirtualItems(ctx context.Context, collectionID string, candidateIDs []string) (int64, error) {
	if r == nil || r.pool == nil || strings.TrimSpace(collectionID) == "" || len(candidateIDs) == 0 {
		return 0, nil
	}
	collectionSourceKey := "collection:" + strings.TrimSpace(collectionID)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin failed collection cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize with concurrent ReplaceItems/RemoveItem/Delete for this
	// collection so a racing acceptance is either visible (and preserved) or
	// ordered after this cleanup.
	if _, err := tx.Exec(ctx, `SELECT id FROM library_collections WHERE id=$1 FOR UPDATE`, strings.TrimSpace(collectionID)); err != nil {
		return 0, fmt.Errorf("lock collection for failed sync cleanup: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE IF NOT EXISTS affected_collection_virtual_items(
			content_id text PRIMARY KEY
		) ON COMMIT DROP`); err != nil {
		return 0, fmt.Errorf("create failed cleanup ownership set: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO affected_collection_virtual_items(content_id)
		SELECT DISTINCT unnest($1::text[])
		ON CONFLICT DO NOTHING`, candidateIDs); err != nil {
		return 0, fmt.Errorf("capture failed cleanup ownership set: %w", err)
	}
	// Release any collection claims for candidate items that have no surviving membership in that collection
	if _, err := tx.Exec(ctx, `
		DELETE FROM virtual_media_file_source_claims claim
		WHERE claim.content_id = ANY($1::text[])
		  AND claim.source_key = $2
		  AND NOT EXISTS (
		      SELECT 1 FROM library_collection_items lci
		      WHERE lci.media_item_id = claim.content_id
		        AND lci.collection_id = $3
		  )`, candidateIDs, collectionSourceKey, collectionID); err != nil {
		return 0, fmt.Errorf("remove unreferenced collection file claims: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM virtual_media_source_claims claim
		WHERE claim.content_id = ANY($1::text[])
		  AND claim.source_key = $2
		  AND NOT EXISTS (
		      SELECT 1 FROM library_collection_items lci
		      WHERE lci.media_item_id = claim.content_id
		        AND lci.collection_id = $3
		  )`, candidateIDs, collectionSourceKey, collectionID); err != nil {
		return 0, fmt.Errorf("remove unreferenced collection source claims: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE failed_collection_virtual_folders ON COMMIT DROP AS
		WITH removed AS (
			DELETE FROM media_files mf
			WHERE mf.content_id = ANY($1::text[])
			  AND mf.container = 'virtual'
			  AND mf.probe_source = 'virtual_collection'
			  AND NOT EXISTS (
			      SELECT 1 FROM virtual_media_file_source_claims claim
			      WHERE claim.plugin_installation_id = mf.virtual_owner_installation_id
			        AND claim.content_id = mf.content_id
			        AND claim.media_folder_id = mf.media_folder_id
			        AND claim.file_path = mf.file_path
			  )`+virtualFileRetentionGuard+`
			RETURNING content_id, media_folder_id
		)
		SELECT DISTINCT content_id, media_folder_id FROM removed`, candidateIDs); err != nil {
		return 0, fmt.Errorf("remove failed collection virtual files: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM media_item_libraries membership
		USING failed_collection_virtual_folders removed
		WHERE membership.content_id = removed.content_id
		  AND membership.media_folder_id = removed.media_folder_id
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files remaining
		      WHERE remaining.content_id = membership.content_id
		        AND remaining.media_folder_id = membership.media_folder_id
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM library_collection_items collection_item
		      JOIN library_collection_libraries collection_library
		        ON collection_library.collection_id = collection_item.collection_id
		      WHERE collection_item.media_item_id = membership.content_id
		        AND collection_library.library_id = membership.media_folder_id
		  )`); err != nil {
		return 0, fmt.Errorf("remove failed collection library links: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM episode_libraries el
		WHERE el.episode_id IN (SELECT content_id FROM episodes WHERE series_id = ANY($1::text[]))
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      WHERE mf.episode_id = el.episode_id
		        AND mf.media_folder_id = el.media_folder_id
		  )`, candidateIDs); err != nil {
		return 0, fmt.Errorf("clean unreferenced episode libraries: %w", err)
	}
	rows, err := tx.Query(ctx, `
		DELETE FROM media_items mi
		WHERE mi.content_id = ANY($1::text[])
		  AND (mi.virtual_source = 'collection' OR left(mi.virtual_source, 11) = 'collection:')
		  AND NOT EXISTS (
		      SELECT 1 FROM library_collection_items lci
		      WHERE lci.media_item_id = mi.content_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      WHERE mf.content_id = mi.content_id
		        AND COALESCE(mf.container, '') <> 'virtual'
		        AND mf.file_path NOT LIKE 'virtual://%'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_source_claims claim
		      WHERE claim.content_id = mi.content_id
		  )`+virtualItemRetentionGuard+`
		RETURNING mi.content_id`, candidateIDs)
	if err != nil {
		return 0, fmt.Errorf("clean up candidate collection items: %w", err)
	}
	deletedIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("collect candidate collection cleanup ids: %w", err)
	}
	// Reconcile after deletions so the orphan-item predicate above observes
	// the pre-cleanup virtual_source compatibility value.
	if err := reconcileAffectedVirtualMetadataOwnership(ctx, tx); err != nil {
		return 0, err
	}
	if err := EnqueueSearchIndexDeletes(ctx, tx, deletedIDs); err != nil {
		return 0, fmt.Errorf("enqueue candidate collection search deletes: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit failed collection cleanup: %w", err)
	}
	return int64(len(deletedIDs)), nil
}

// CleanupLegacyUnscopedCollectionClaims executes a bounded cleanup of legacy unscoped
// collection claims ('collection') that have no surviving membership evidence in the
// relevant target folder or that have been superseded by scoped claims ('collection:<id>').
// It also cleans up any virtual files or catalog items orphaned by the removal.
func (r *ItemRepository) CleanupLegacyUnscopedCollectionClaims(ctx context.Context, limit int) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("item repository is not configured")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin legacy unscoped claims cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE IF NOT EXISTS affected_collection_virtual_items(
			content_id text PRIMARY KEY
		) ON COMMIT DROP`); err != nil {
		return 0, fmt.Errorf("create legacy cleanup ownership set: %w", err)
	}

	// Stage candidate claims with collection locks to prevent race with acceptance
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE legacy_candidate_claims(
			plugin_installation_id bigint,
			source_key text,
			content_id text,
			media_folder_id int
		) ON COMMIT DROP`); err != nil {
		return 0, fmt.Errorf("create legacy candidate claims: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		CREATE TEMP VIEW eligible_legacy_claims AS
			SELECT sc.plugin_installation_id, sc.source_key, sc.content_id, sc.media_folder_id
			FROM virtual_media_source_claims sc
			WHERE (
			      sc.source_key = 'collection'
			      AND (
			      -- No surviving collection membership targeting this folder
			      NOT EXISTS (
			          SELECT 1
			          FROM library_collection_items lci
			          JOIN library_collections lc ON lc.id = lci.collection_id
			          LEFT JOIN library_collection_libraries lcl ON lcl.collection_id = lc.id
			          WHERE lci.media_item_id = sc.content_id
			            AND (lcl.library_id = sc.media_folder_id OR lc.library_id = sc.media_folder_id)
			      )
			      -- Or superseded by at least one scoped claim in the same folder
			      OR EXISTS (
			          SELECT 1
			          FROM virtual_media_source_claims scoped
			          WHERE scoped.plugin_installation_id = sc.plugin_installation_id
			            AND scoped.content_id = sc.content_id
			            AND scoped.media_folder_id = sc.media_folder_id
			            AND left(scoped.source_key, 11) = 'collection:'
			            AND NOT EXISTS (
			                SELECT 1
			                FROM virtual_media_file_source_claims legacy_file
			                WHERE legacy_file.plugin_installation_id = sc.plugin_installation_id
			                  AND legacy_file.source_key = 'collection'
			                  AND legacy_file.content_id = sc.content_id
			                  AND legacy_file.media_folder_id = sc.media_folder_id
			                  AND NOT EXISTS (
			                      SELECT 1
			                      FROM virtual_media_file_source_claims scoped_file
			                      WHERE scoped_file.plugin_installation_id = legacy_file.plugin_installation_id
			                        AND scoped_file.source_key LIKE 'collection:%'
			                        AND scoped_file.content_id = legacy_file.content_id
			                        AND scoped_file.media_folder_id = legacy_file.media_folder_id
			                        AND scoped_file.file_path = legacy_file.file_path
			                  )
			            )
			      )
			      )
		      OR (
		          left(sc.source_key, 11) = 'collection:'
		          AND sc.staged_until IS NOT NULL
		          AND sc.staged_until < NOW()
		          AND NOT EXISTS (
			              SELECT 1
			              FROM library_collection_items lci
			              WHERE lci.media_item_id = sc.content_id
			                AND ('collection:' || lci.collection_id) = sc.source_key
			          )
			      )
			  )
		`); err != nil {
		return 0, fmt.Errorf("create eligible legacy claims view: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO legacy_candidate_claims
		SELECT * FROM eligible_legacy_claims
		ORDER BY content_id, source_key, plugin_installation_id, media_folder_id
		LIMIT $1`, limit); err != nil {
		return 0, fmt.Errorf("selecting legacy candidate claims: %w", err)
	}

	collectionRows, err := tx.Query(ctx, `
		SELECT id FROM library_collections
		WHERE id IN (
			SELECT substring(source_key FROM 12) FROM legacy_candidate_claims WHERE left(source_key, 11)='collection:'
			UNION
			SELECT collection_id FROM library_collection_items WHERE media_item_id IN (SELECT content_id FROM legacy_candidate_claims)
		)
		ORDER BY id FOR UPDATE`)
	if err != nil {
		return 0, err
	}
	if _, err := pgx.CollectRows(collectionRows, pgx.RowTo[string]); err != nil {
		return 0, err
	}
	itemRows, err := tx.Query(ctx, `SELECT DISTINCT content_id FROM legacy_candidate_claims ORDER BY content_id`)
	if err != nil {
		return 0, err
	}
	itemIDs, err := pgx.CollectRows(itemRows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, id := range itemIDs {
		if err := requestlock.LockItem(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM legacy_candidate_claims candidate
		WHERE NOT EXISTS (
			SELECT 1 FROM eligible_legacy_claims eligible
			WHERE eligible.plugin_installation_id = candidate.plugin_installation_id
			  AND eligible.source_key = candidate.source_key
			  AND eligible.content_id = candidate.content_id
			  AND eligible.media_folder_id = candidate.media_folder_id
		)`); err != nil {
		return 0, fmt.Errorf("revalidate legacy candidate claims: %w", err)
	}
	if _, err := tx.Exec(ctx, `DROP VIEW eligible_legacy_claims`); err != nil {
		return 0, fmt.Errorf("drop eligible legacy claims view: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE deleted_legacy_file_claims(
			plugin_installation_id bigint,
			content_id text,
			media_folder_id int,
			file_path text
		) ON COMMIT DROP`); err != nil {
		return 0, fmt.Errorf("create deleted legacy file claims: %w", err)
	}
	// Remove file claims owned by the candidate parents first (the foreign
	// key runs file->source), capturing their exact identities for the file
	// sweep below. Files with surviving claims from other owners are
	// preserved by the remaining-claim check in that sweep.
	if _, err := tx.Exec(ctx, `
		WITH deleted AS (
			DELETE FROM virtual_media_file_source_claims fc
			USING legacy_candidate_claims c
			WHERE fc.plugin_installation_id = c.plugin_installation_id
			  AND fc.source_key = c.source_key
			  AND fc.content_id = c.content_id
			  AND fc.media_folder_id = c.media_folder_id
			RETURNING fc.plugin_installation_id, fc.content_id, fc.media_folder_id, fc.file_path
		)
		INSERT INTO deleted_legacy_file_claims(plugin_installation_id, content_id, media_folder_id, file_path)
		SELECT plugin_installation_id, content_id, media_folder_id, file_path FROM deleted`); err != nil {
		return 0, fmt.Errorf("deleting legacy file claims: %w", err)
	}
	rows, err := tx.Query(ctx, `
		WITH deleted_claims AS (
			DELETE FROM virtual_media_source_claims sc
			USING legacy_candidate_claims c
			WHERE sc.plugin_installation_id = c.plugin_installation_id
			  AND sc.source_key = c.source_key
			  AND sc.content_id = c.content_id
			  AND sc.media_folder_id = c.media_folder_id
			RETURNING sc.content_id, sc.media_folder_id
		)
		SELECT DISTINCT content_id, media_folder_id FROM deleted_claims`)
	if err != nil {
		return 0, fmt.Errorf("deleting unreferenced legacy unscoped claims: %w", err)
	}
	type affectedFolder struct {
		contentID string
		folderID  int
	}
	var affected []affectedFolder
	for rows.Next() {
		var af affectedFolder
		if err := rows.Scan(&af.contentID, &af.folderID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scanning affected legacy cleanup folder: %w", err)
		}
		affected = append(affected, af)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterating affected legacy cleanup folders: %w", err)
	}
	rows.Close()
	if len(affected) == 0 {
		return 0, nil
	}

	contentIDs := make([]string, 0, len(affected))
	folderIDs := make([]int, 0, len(affected))
	for _, af := range affected {
		contentIDs = append(contentIDs, af.contentID)
		folderIDs = append(folderIDs, af.folderID)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO affected_collection_virtual_items(content_id)
		SELECT DISTINCT unnest($1::text[])
		ON CONFLICT DO NOTHING`, contentIDs); err != nil {
		return 0, fmt.Errorf("capture legacy cleanup ownership set: %w", err)
	}
	if err := reconcileAffectedVirtualMetadataOwnership(ctx, tx); err != nil {
		return 0, err
	}

	// Delete only the exact files whose claims this cleanup removed and which
	// have no surviving claims from any owner. Files merely sharing the same
	// content/folder pair are preserved.
	if _, err := tx.Exec(ctx, `
		DELETE FROM media_files mf
		USING deleted_legacy_file_claims deleted
		WHERE deleted.plugin_installation_id = mf.virtual_owner_installation_id
		  AND deleted.content_id = mf.content_id
		  AND deleted.media_folder_id = mf.media_folder_id
		  AND deleted.file_path = mf.file_path
		  AND mf.container = 'virtual'
		  AND mf.probe_source IN ('virtual_collection', 'virtual')
		  AND mf.virtual_owner_installation_id IS NOT NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims claim
		      WHERE claim.plugin_installation_id = mf.virtual_owner_installation_id
		        AND claim.content_id = mf.content_id
		        AND claim.media_folder_id = mf.media_folder_id
		        AND claim.file_path = mf.file_path
		  )`+virtualFileRetentionGuard); err != nil {
		return 0, fmt.Errorf("cleaning orphaned virtual files after legacy claim cleanup: %w", err)
	}

	// Clean up media_item_libraries if no media files and no collection memberships remain for that folder.
	if _, err := tx.Exec(ctx, `
		DELETE FROM media_item_libraries mil
		WHERE EXISTS (
		      SELECT 1 FROM unnest($1::text[], $2::int[]) AS affected(content_id, folder_id)
		      WHERE affected.content_id = mil.content_id
		        AND affected.folder_id = mil.media_folder_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      WHERE mf.content_id = mil.content_id
		        AND mf.media_folder_id = mil.media_folder_id
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM library_collection_items lci
		      JOIN library_collections lc ON lc.id = lci.collection_id
		      LEFT JOIN library_collection_libraries lcl ON lcl.collection_id = lc.id
		      WHERE lci.media_item_id = mil.content_id
		        AND (lcl.library_id = mil.media_folder_id OR lc.library_id = mil.media_folder_id)
		  )`, contentIDs, folderIDs); err != nil {
		return 0, fmt.Errorf("cleaning orphaned library links after legacy claim cleanup: %w", err)
	}

	// Clean up episode_libraries for affected series
	if _, err := tx.Exec(ctx, `
		DELETE FROM episode_libraries el
		WHERE el.episode_id IN (SELECT content_id FROM episodes WHERE series_id = ANY($1::text[]))
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      WHERE mf.episode_id = el.episode_id
		        AND mf.media_folder_id = el.media_folder_id
		  )`, contentIDs); err != nil {
		return 0, fmt.Errorf("cleaning orphaned episode libraries after legacy claim cleanup: %w", err)
	}

	// Delete catalog rows proven orphaned by this cleanup. Provenance comes
	// from the deleted claims captured in contentIDs, not the rewritten
	// virtual_source compatibility field.
	rows, err = tx.Query(ctx, `
		DELETE FROM media_items mi
		WHERE mi.content_id = ANY($1::text[])
		  AND NOT EXISTS (SELECT 1 FROM library_collection_items lci WHERE lci.media_item_id = mi.content_id)
		  AND NOT EXISTS (SELECT 1 FROM media_files mf WHERE mf.content_id = mi.content_id)
		  AND NOT EXISTS (SELECT 1 FROM virtual_media_source_claims claim WHERE claim.content_id = mi.content_id)`+virtualItemRetentionGuard+`
		RETURNING mi.content_id`,
		contentIDs)
	if err != nil {
		return 0, fmt.Errorf("cleaning unreferenced virtual items after legacy claim cleanup: %w", err)
	}
	deletedIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("collecting legacy cleanup item ids: %w", err)
	}
	if err := EnqueueSearchIndexDeletes(ctx, tx, deletedIDs); err != nil {
		return 0, fmt.Errorf("enqueueing legacy cleanup search deletes: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit legacy unscoped claims cleanup: %w", err)
	}
	return int64(len(affected)), nil
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
			SELECT content_id, media_folder_id, virtual_owner_installation_id, file_path
			FROM media_files
			WHERE episode_id IS NULL
			  AND container='virtual'
			  AND probe_source='virtual_collection'
			  AND file_path LIKE 'virtual://series/%'
			  AND virtual_owner_installation_id IS NOT NULL
			  AND missing_since IS NULL
			  AND EXISTS (
			      SELECT 1
			      FROM virtual_media_file_source_claims collection_claim
			      JOIN library_collection_items collection_member
			        ON collection_member.media_item_id = media_files.content_id
			      WHERE collection_claim.plugin_installation_id = media_files.virtual_owner_installation_id
			        AND collection_claim.content_id = media_files.content_id
			        AND collection_claim.media_folder_id = media_files.media_folder_id
			        AND collection_claim.file_path = media_files.file_path
			        AND (collection_claim.source_key = 'collection' OR collection_claim.source_key LIKE 'collection:%')
			  )
		), needs_reconciliation AS (
			SELECT bases.content_id
			FROM bases
			JOIN episodes ep ON ep.series_id=bases.content_id
			WHERE ep.season_number>0
			  AND ep.episode_number>0
			  AND (
			      CASE 
			          WHEN EXISTS (
			              SELECT 1 FROM verified_release_override_history h
			              JOIN (
			                  SELECT 'tmdb'::text AS provider, tmdb_id AS provider_id FROM media_items WHERE content_id=ep.series_id
			                  UNION SELECT 'tvdb', tvdb_id FROM media_items WHERE content_id=ep.series_id
			                  UNION SELECT 'imdb', imdb_id FROM media_items WHERE content_id=ep.series_id
			                  UNION SELECT provider, provider_id FROM media_item_provider_ids WHERE content_id=ep.series_id AND item_type='series'
			              ) i USING (provider, provider_id)
			              WHERE h.media_type='episode' AND h.season_number=ep.season_number AND h.episode_number=ep.episode_number
			                AND h.release_at IS NOT NULL
			                AND NOT EXISTS (
			                  SELECT 1 FROM verified_release_override_history h2
			                  WHERE h2.media_type=h.media_type
			                    AND h2.provider=h.provider
			                    AND h2.provider_id=h.provider_id
			                    AND h2.season_number=h.season_number
			                    AND h2.episode_number=h.episode_number
			                    AND h2.revision > h.revision
			              )
			              AND h.release_at <= statement_timestamp()
			          ) THEN (
			              verified_episode_release_at(ep.series_id,ep.season_number,ep.episode_number,ep.air_date::timestamp AT TIME ZONE 'UTC') <= statement_timestamp()
			          )
			          ELSE (
			              -- No active override: local possession, then the
			              -- provider/air-date evidence (the documented fallback
			              -- order). A null date has no evidence and stays blocked.
			              ep.metadata_source='local'
			              OR EXISTS(
			                  SELECT 1 FROM media_files proven
			                  WHERE proven.episode_id=ep.content_id
			                    AND proven.container<>'virtual'
			                    AND proven.file_path NOT LIKE 'virtual://%'
			              )
			              OR verified_episode_release_at(ep.series_id,ep.season_number,ep.episode_number,ep.air_date::timestamp AT TIME ZONE 'UTC') <= statement_timestamp()
			          )
			      END
			  )
			  AND (
			      NOT EXISTS (
			      SELECT 1 FROM media_files mf
			      WHERE mf.episode_id=ep.content_id
				  AND mf.media_folder_id=bases.media_folder_id
				  AND mf.virtual_owner_installation_id=bases.virtual_owner_installation_id
				  AND mf.container='virtual'
				  AND mf.probe_source IN ('virtual_collection', 'virtual')
			        AND mf.file_path = split_part(bases.file_path, '?', 1) || '/' || ep.season_number || '/' || ep.episode_number ||
			            CASE WHEN position('?' IN bases.file_path) > 0 THEN '?' || split_part(bases.file_path, '?', 2) ELSE '' END
			      )
			      OR NOT EXISTS (
			      SELECT 1 FROM virtual_media_file_source_claims claim
			      WHERE claim.plugin_installation_id = bases.virtual_owner_installation_id
			        AND claim.content_id = bases.content_id
			        AND claim.media_folder_id = bases.media_folder_id
			        AND claim.file_path = split_part(bases.file_path, '?', 1) || '/' || ep.season_number || '/' || ep.episode_number ||
			            CASE WHEN position('?' IN bases.file_path) > 0 THEN '?' || split_part(bases.file_path, '?', 2) ELSE '' END
			        AND (claim.source_key LIKE 'collection:%' OR claim.source_key IN ('request', 'virtual') OR left(claim.source_key, 8) IN ('request:', 'virtual:'))
			      )
			  )
			UNION
			SELECT mf.content_id
			FROM media_files mf
			JOIN episodes ep ON ep.content_id=mf.episode_id
			WHERE mf.container='virtual'
			  AND mf.probe_source='virtual_collection'
			  AND EXISTS (
			      SELECT 1
			      FROM virtual_media_file_source_claims collection_claim
			      JOIN library_collection_items collection_member
			        ON collection_member.media_item_id = mf.content_id
			      WHERE collection_claim.plugin_installation_id = mf.virtual_owner_installation_id
			        AND collection_claim.content_id = mf.content_id
			        AND collection_claim.media_folder_id = mf.media_folder_id
			        AND collection_claim.file_path = mf.file_path
			        AND (collection_claim.source_key = 'collection' OR collection_claim.source_key LIKE 'collection:%')
			  )
			  AND (
			      (
			          CASE 
			              WHEN EXISTS (
			                  SELECT 1 FROM verified_release_override_history h
			                  JOIN (
			                      SELECT 'tmdb'::text AS provider, tmdb_id AS provider_id FROM media_items WHERE content_id=ep.series_id
			                      UNION SELECT 'tvdb', tvdb_id FROM media_items WHERE content_id=ep.series_id
			                      UNION SELECT 'imdb', imdb_id FROM media_items WHERE content_id=ep.series_id
			                      UNION SELECT provider, provider_id FROM media_item_provider_ids WHERE content_id=ep.series_id AND item_type='series'
			                  ) i USING (provider, provider_id)
			                  WHERE h.media_type='episode' AND h.season_number=ep.season_number AND h.episode_number=ep.episode_number
			                    AND h.release_at IS NOT NULL
			                    AND NOT EXISTS (
			                      SELECT 1 FROM verified_release_override_history h2
			                      WHERE h2.media_type=h.media_type
			                        AND h2.provider=h.provider
			                        AND h2.provider_id=h.provider_id
			                        AND h2.season_number=h.season_number
			                        AND h2.episode_number=h.episode_number
			                        AND h2.revision > h.revision
			                  )
			                  AND h.release_at <= statement_timestamp()
			              ) THEN (
			                  verified_episode_release_at(ep.series_id,ep.season_number,ep.episode_number,ep.air_date::timestamp AT TIME ZONE 'UTC') > statement_timestamp()
			              )
			              ELSE (
			                  -- No active override: an existing virtual file is
			                  -- stale when the episode is neither locally proven
			                  -- nor physically possessed and its provider date is
			                  -- still in the future. An unknown date keeps the
			                  -- file rather than withdrawing on absence.
			                  COALESCE(ep.metadata_source,'')<>'local'
			                  AND NOT EXISTS(
			                      SELECT 1 FROM media_files proven
			                      WHERE proven.episode_id=ep.content_id
			                        AND proven.container<>'virtual'
			                        AND proven.file_path NOT LIKE 'virtual://%'
			                  )
			                  AND verified_episode_release_at(ep.series_id,ep.season_number,ep.episode_number,ep.air_date::timestamp AT TIME ZONE 'UTC') > statement_timestamp()
			              )
			          END
			      )
			      OR ep.season_number<=0
			      OR ep.episode_number<=0
			  )
		)
		SELECT nr.content_id
		FROM needs_reconciliation nr
		JOIN media_items mi ON mi.content_id = nr.content_id
		ORDER BY COALESCE(mi.virtual_episode_reconciliation_attempted_at, '1970-01-01'::timestamptz) ASC, nr.content_id ASC
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
		_ = r.TouchVirtualEpisodeAttempt(ctx, seriesID)
		if err := r.MaterializeVirtualPlaybackEpisodes(ctx, seriesID); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("%s: %w", seriesID, err))
			continue
		}
		reconciled++
	}
	return reconciled, reconcileErr
}

// TouchVirtualItemAttempt records collection-repair progress independently of
// virtual ownership freshness, so claim updates cannot reset retry fairness.
// This is used by reconciliation to record durable failure progress that survives
// collection membership replacements and avoids batch starvation.
func (r *ItemRepository) TouchVirtualItemAttempt(ctx context.Context, contentID string) error {
	if r == nil || r.pool == nil {
		return errors.New("item repository is not configured")
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE media_items
		SET virtual_reconciliation_attempted_at = NOW()
		WHERE content_id = $1`, contentID)
	if err != nil {
		return fmt.Errorf("touch virtual item attempt: %w", err)
	}
	return nil
}

// TouchVirtualEpisodeAttempt records scheduled-episode-reconciliation progress
// on its own cursor so episode work and collection repair never postpone each
// other.
func (r *ItemRepository) TouchVirtualEpisodeAttempt(ctx context.Context, contentID string) error {
	if r == nil || r.pool == nil {
		return errors.New("item repository is not configured")
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE media_items
		SET virtual_episode_reconciliation_attempted_at = NOW()
		WHERE content_id = $1`, contentID)
	if err != nil {
		return fmt.Errorf("touch virtual episode attempt: %w", err)
	}
	return nil
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
	_, _, _, err := materializeVirtualPlaybackEpisodesDetailedTx(ctx, tx, seriesID, "", true)
	return err
}

// materializeVirtualEpisodesEvalHook runs between release-lock acquisition
// and eligibility evaluation in episode materialization. It is nil in
// production; tests use it to interleave concurrent override writes or
// catalog changes deterministically.
var materializeVirtualEpisodesEvalHook func()

func materializeVirtualPlaybackEpisodesDetailedTx(ctx context.Context, tx pgx.Tx, seriesID, collectionID string, linkLibraries bool) (filesCreated, filesExisting, distinctEpisodes int, err error) {
	rows, err := tx.Query(ctx, `
		SELECT mf.media_folder_id, mf.virtual_owner_installation_id, mf.file_path,
		       COALESCE(mf.resolution,''), COALESCE(mf.codec_video,''),
		       COALESCE(mf.codec_audio,''), COALESCE(mf.hdr,false), COALESCE(mf.edition_raw,''),
		       COALESCE(mf.probe_source,'virtual_collection')
		FROM media_files mf
		WHERE mf.content_id=$1
		  AND mf.episode_id IS NULL
		  AND mf.container='virtual'
		  AND mf.probe_source IN ('virtual_collection', 'virtual')
		  AND mf.file_path LIKE 'virtual://series/%'
		  AND mf.virtual_owner_installation_id IS NOT NULL
		  AND (
		      mf.probe_source <> 'virtual_collection'
		      OR mf.missing_since IS NULL
		      OR EXISTS (
		          SELECT 1
		          FROM virtual_media_file_source_claims staged_claim
		          JOIN library_collection_items staged_member
		            ON staged_member.media_item_id = mf.content_id
		          WHERE staged_claim.plugin_installation_id = mf.virtual_owner_installation_id
		            AND staged_claim.content_id = mf.content_id
		            AND staged_claim.media_folder_id = mf.media_folder_id
		            AND staged_claim.file_path = mf.file_path
		            AND staged_claim.source_key = ('collection:' || staged_member.collection_id)
		      )
		      OR ($2 <> '' AND EXISTS (
		          SELECT 1
		          FROM virtual_media_file_source_claims staged_own_claim
		          WHERE staged_own_claim.plugin_installation_id = mf.virtual_owner_installation_id
		            AND staged_own_claim.content_id = mf.content_id
		            AND staged_own_claim.media_folder_id = mf.media_folder_id
		            AND staged_own_claim.file_path = mf.file_path
		            AND staged_own_claim.source_key = ('collection:' || $2)
		      ))
		  )
		  AND (
		      $2 = ''
		      OR EXISTS (
		          SELECT 1 FROM virtual_media_file_source_claims sc
		          WHERE sc.content_id = mf.content_id
		            AND sc.media_folder_id = mf.media_folder_id
		            AND sc.file_path = mf.file_path
		            AND sc.plugin_installation_id = mf.virtual_owner_installation_id
		            AND sc.source_key = ('collection:' || $2)
		      )
		  )
		ORDER BY mf.id`, seriesID, collectionID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list virtual series profiles: %w", err)
	}
	var bases []collectionVirtualSeriesBase
	for rows.Next() {
		var base collectionVirtualSeriesBase
		if err := rows.Scan(
			&base.folderID, &base.ownerID, &base.filePath, &base.resolution,
			&base.codecVideo, &base.codecAudio, &base.hdr, &base.editionRaw, &base.probeSource,
		); err != nil {
			rows.Close()
			return 0, 0, 0, fmt.Errorf("scan virtual series profile: %w", err)
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
		return 0, 0, 0, fmt.Errorf("iterate virtual series profiles: %w", err)
	}
	rows.Close()
	if len(bases) == 0 {
		return 0, 0, 0, nil
	}

	// Content lock first (see lockReleaseContentTx): alias writers take it
	// exclusively, so the candidate set below cannot gain aliases under us.
	if err := lockReleaseContentTx(ctx, tx, seriesID, false); err != nil {
		return 0, 0, 0, err
	}
	type episodeCoordinate struct {
		id      string
		season  int
		episode int
	}
	// Enumerate the stable candidate set with no release filter, lock every
	// candidate's identities, then evaluate. Later reads can only shrink
	// this set: newly eligible episodes wait for their own run instead of
	// entering under unlocked identities.
	var candidates []episodeCoordinate
	candidateRows, err := tx.Query(ctx, `
		SELECT content_id, season_number, episode_number
		FROM episodes
		WHERE series_id=$1 AND season_number > 0 AND episode_number > 0
		ORDER BY season_number, episode_number`, seriesID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list virtual episode candidates: %w", err)
	}
	for candidateRows.Next() {
		var candidate episodeCoordinate
		if err := candidateRows.Scan(&candidate.id, &candidate.season, &candidate.episode); err != nil {
			candidateRows.Close()
			return 0, 0, 0, fmt.Errorf("scan virtual episode candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := candidateRows.Err(); err != nil {
		candidateRows.Close()
		return 0, 0, 0, fmt.Errorf("iterate virtual episode candidates: %w", err)
	}
	candidateRows.Close()
	// Bound the per-pass work: episodes beyond the budget wait for the
	// next reconciliation cycle rather than inflating lock and batch sizes.
	if len(candidates) > maxVirtualEpisodeReconciliationBatch {
		candidates = candidates[:maxVirtualEpisodeReconciliationBatch]
	}
	template, err := releaseIdentitiesForContent(ctx, tx, "episode", seriesID, "series", "", "", "", 1, 1)
	if err != nil {
		return 0, 0, 0, err
	}
	byCoordinate := make(map[episodeCoordinate][]ReleaseIdentity, len(candidates))
	lockIDs := make([]ReleaseIdentity, 0, len(candidates)*len(template))
	for _, candidate := range candidates {
		ids := make([]ReleaseIdentity, 0, len(template))
		for _, t := range template {
			t.SeasonNumber = candidate.season
			t.EpisodeNumber = candidate.episode
			ids = append(ids, t)
			lockIDs = append(lockIDs, t)
		}
		byCoordinate[candidate] = ids
	}
	if err := lockReleaseIdentitiesTx(ctx, tx, lockIDs); err != nil {
		return 0, 0, 0, fmt.Errorf("lock release identities: %w", err)
	}
	if materializeVirtualEpisodesEvalHook != nil {
		materializeVirtualEpisodesEvalHook()
	}
	// Deduplicate lock identities before the override capture batch so
	// each unique identity is queried once rather than once per candidate.
	dedupedLockIDs := dedupeReleaseIdentities(lockIDs)
	// One captured decision per locked identity at a single evaluation
	// time; provider/air-date evidence and local fallback apply only where
	// no active override decides.
	captured, err := captureReleaseOverridesTx(ctx, tx, dedupedLockIDs)
	if err != nil {
		return 0, 0, 0, err
	}
	byIdentity := make(map[ReleaseIdentity]ReleaseOverride, len(captured))
	for _, o := range captured {
		byIdentity[o.ReleaseIdentity] = o
	}
	now := time.Now().UTC()
	verdict := make(map[episodeCoordinate]bool, len(candidates))
	decided := make(map[episodeCoordinate]bool, len(candidates))
	for candidate, ids := range byCoordinate {
		entries := make([]ReleaseOverride, 0, len(ids))
		for _, id := range ids {
			if o, ok := byIdentity[id]; ok {
				entries = append(entries, o)
			}
		}
		if allowed, active := decideReleaseOverrides(now, entries); active {
			verdict[candidate] = allowed
			decided[candidate] = true
		}
	}
	byID := make(map[string]episodeCoordinate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.id] = candidate
	}
	evalRows, err := tx.Query(ctx, `
		SELECT ep.content_id,
			COALESCE(verified_episode_release_at(ep.series_id,ep.season_number,ep.episode_number,ep.air_date::timestamp AT TIME ZONE 'UTC') <= statement_timestamp(),false),
			COALESCE(ep.metadata_source='local'
				OR EXISTS(
					SELECT 1 FROM media_files mf
					WHERE mf.episode_id=ep.content_id
					  AND mf.container<>'virtual'
					  AND mf.file_path NOT LIKE 'virtual://%'
				),false)
		FROM episodes ep
		WHERE ep.series_id=$1 AND ep.season_number > 0 AND ep.episode_number > 0`, seriesID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("evaluate virtual episode releases: %w", err)
	}
	var episodes []episodeCoordinate
	for evalRows.Next() {
		var id string
		var dateReleased, locallyProven bool
		if err := evalRows.Scan(&id, &dateReleased, &locallyProven); err != nil {
			evalRows.Close()
			return 0, 0, 0, fmt.Errorf("scan virtual episode release: %w", err)
		}
		candidate, ok := byID[id]
		if !ok {
			// Not in the locked candidate set (inserted concurrently):
			// leave it for its own run.
			continue
		}
		if decided[candidate] {
			if verdict[candidate] {
				episodes = append(episodes, candidate)
			}
			continue
		}
		if dateReleased || locallyProven {
			episodes = append(episodes, candidate)
		}
	}
	if err := evalRows.Err(); err != nil {
		evalRows.Close()
		return 0, 0, 0, fmt.Errorf("iterate virtual episode releases: %w", err)
	}
	evalRows.Close()
	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].season != episodes[j].season {
			return episodes[i].season < episodes[j].season
		}
		return episodes[i].episode < episodes[j].episode
	})

	if len(candidates) == 0 {
		if err := cleanupStaleVirtualEpisodesTx(ctx, tx, seriesID, collectionID, nil, nil, nil); err != nil {
			return 0, 0, 0, err
		}
		return 0, 0, 0, nil
	}

	// The expected set is the released subset: an episode whose release gate
	// closed (a future override) must lose the virtual file and claims a prior
	// pass created, or a delayed release keeps advertising a placeholder the
	// provider cannot serve yet. The batch bound only limits how many episodes
	// this pass evaluates, not which of them may exist.
	expectedPaths := make([]string, 0, len(bases)*len(episodes))
	expectedOwners := make([]int64, 0, len(bases)*len(episodes))
	expectedFolders := make([]int, 0, len(bases)*len(episodes))

	type episodeFileKey struct {
		path     string
		ownerID  int64
		folderID int
	}
	seenEpisodes := make(map[episodeFileKey]struct{}, len(bases)*len(candidates))

	for _, base := range bases {
		parsed, _ := url.Parse(base.filePath)
		identifier := strings.TrimPrefix(parsed.EscapedPath(), "/")
		for _, candidate := range episodes {
			episodeURI := &url.URL{
				Scheme:   "virtual",
				Host:     "series",
				Path:     fmt.Sprintf("/%s/%d/%d", identifier, candidate.season, candidate.episode),
				RawQuery: parsed.RawQuery,
			}
			path := episodeURI.String()
			key := episodeFileKey{path: path, ownerID: int64(base.ownerID), folderID: base.folderID}
			if _, seen := seenEpisodes[key]; seen {
				continue
			}
			seenEpisodes[key] = struct{}{}
			expectedPaths = append(expectedPaths, path)
			expectedOwners = append(expectedOwners, int64(base.ownerID))
			expectedFolders = append(expectedFolders, base.folderID)
		}
	}

	if len(episodes) == 0 {
		if err := cleanupStaleVirtualEpisodesTx(ctx, tx, seriesID, collectionID, expectedOwners, expectedPaths, expectedFolders); err != nil {
			return 0, 0, 0, err
		}
		return 0, 0, 0, nil
	}

	distinctEpisodes = len(episodes)
	seenMaterialized := make(map[episodeFileKey]struct{}, len(bases)*len(episodes))

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
			key := episodeFileKey{path: path, ownerID: int64(base.ownerID), folderID: base.folderID}
			if _, seen := seenMaterialized[key]; seen {
				continue
			}
			seenMaterialized[key] = struct{}{}
			ps := base.probeSource
			if ps == "" {
				ps = "virtual_collection"
			}

			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM media_files
					WHERE file_path=$1 AND virtual_owner_installation_id=$2 AND media_folder_id=$3
				)`, path, base.ownerID, base.folderID).Scan(&exists); err != nil {
				return 0, 0, 0, fmt.Errorf("check episode file existence: %w", err)
			}
			if exists {
				filesExisting++
			} else {
				filesCreated++
			}

			if _, err := tx.Exec(ctx, `
				INSERT INTO media_files(
					content_id,episode_id,media_folder_id,file_path,file_size,
					resolution,codec_video,codec_audio,hdr,container,edition_raw,
					season_number,episode_number,probe_source,
					virtual_owner_installation_id,missing_since
				) VALUES(
					$1,$2,$3,$4,0,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),
					$8,'virtual',$9,$10,$11,$12,$13,
					CASE WHEN $14 THEN NULL ELSE NOW() END
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
					missing_since=CASE WHEN $14 THEN NULL ELSE media_files.missing_since END,
					updated_at=NOW()`,
				seriesID, episode.id, base.folderID, path, base.resolution,
				base.codecVideo, base.codecAudio, base.hdr, base.editionRaw,
				episode.season, episode.episode, ps, base.ownerID, linkLibraries,
			); err != nil {
				return 0, 0, 0, fmt.Errorf("upsert released virtual episode: %w", err)
			}
			if linkLibraries {
				if _, err := tx.Exec(ctx, `
					INSERT INTO episode_libraries(episode_id, media_folder_id)
					VALUES($1, $2)
					ON CONFLICT DO NOTHING`,
					episode.id, base.folderID,
				); err != nil {
					return 0, 0, 0, fmt.Errorf("link released virtual episode library: %w", err)
				}
			}

			// Inherit actual base file claims
			if _, err := tx.Exec(ctx, `
				INSERT INTO virtual_media_file_source_claims(
					plugin_installation_id, source_key, content_id, media_folder_id,
					file_path, last_seen_at, staged_until, updated_at
				)
				SELECT claim.plugin_installation_id, claim.source_key, claim.content_id, claim.media_folder_id,
				       $1, NOW(), claim.staged_until, NOW()
				FROM virtual_media_file_source_claims claim
				WHERE claim.plugin_installation_id = $2
				  AND claim.content_id = $3
				  AND claim.media_folder_id = $4
				  AND claim.file_path = $5
				ON CONFLICT (plugin_installation_id, source_key, content_id, media_folder_id, file_path)
				DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at, staged_until = EXCLUDED.staged_until, updated_at = NOW()`,
				path, base.ownerID, seriesID, base.folderID, base.filePath,
			); err != nil {
				return 0, 0, 0, fmt.Errorf("claim released virtual episode: %w", err)
			}
		}
	}

	if err := cleanupStaleVirtualEpisodesTx(ctx, tx, seriesID, collectionID, expectedOwners, expectedPaths, expectedFolders); err != nil {
		return 0, 0, 0, err
	}
	return filesCreated, filesExisting, distinctEpisodes, nil
}

func cleanupStaleVirtualEpisodesTx(ctx context.Context, tx pgx.Tx, seriesID, collectionID string, expectedOwners []int64, expectedPaths []string, expectedFolders []int) error {
	if expectedPaths == nil {
		expectedPaths = []string{}
	}
	if expectedOwners == nil {
		expectedOwners = []int64{}
	}
	if expectedFolders == nil {
		expectedFolders = []int{}
	}

	if collectionID != "" {
		sourceKey := "collection:" + collectionID
		if _, err := tx.Exec(ctx, `
			WITH expected(owner_id, file_path, folder_id) AS (
				SELECT * FROM unnest($2::bigint[], $3::text[], $4::int[])
			), stale AS (
				SELECT mf.content_id, mf.media_folder_id, mf.file_path, mf.virtual_owner_installation_id
				FROM media_files mf
				JOIN episodes ep ON ep.content_id = mf.episode_id
				WHERE ep.series_id = $1
				  AND mf.container = 'virtual'
				  AND mf.probe_source = 'virtual_collection'
				  AND mf.virtual_owner_installation_id IS NOT NULL
				  AND NOT EXISTS (
				      SELECT 1 FROM expected
				      WHERE expected.owner_id = mf.virtual_owner_installation_id
				        AND expected.file_path = mf.file_path
				        AND expected.folder_id = mf.media_folder_id
				  )
			)
			DELETE FROM virtual_media_file_source_claims claim
			USING stale
			WHERE claim.plugin_installation_id = stale.virtual_owner_installation_id
			  AND claim.content_id = stale.content_id
			  AND claim.media_folder_id = stale.media_folder_id
			  AND claim.file_path = stale.file_path
			  AND claim.source_key = $5`,
			seriesID, expectedOwners, expectedPaths, expectedFolders, sourceKey); err != nil {
			return fmt.Errorf("remove stale collection episode claims: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			WITH expected(owner_id, file_path, folder_id) AS (
				SELECT * FROM unnest($2::bigint[], $3::text[], $4::int[])
			), stale AS (
				SELECT mf.content_id, mf.media_folder_id, mf.file_path, mf.virtual_owner_installation_id
				FROM media_files mf
				JOIN episodes ep ON ep.content_id = mf.episode_id
				WHERE ep.series_id = $1
				  AND mf.container = 'virtual'
				  AND mf.probe_source = 'virtual_collection'
				  AND mf.virtual_owner_installation_id IS NOT NULL
				  AND NOT EXISTS (
				      SELECT 1 FROM expected
				      WHERE expected.owner_id = mf.virtual_owner_installation_id
				        AND expected.file_path = mf.file_path
				        AND expected.folder_id = mf.media_folder_id
				  )
			)
			DELETE FROM virtual_media_file_source_claims claim
			USING stale
			WHERE claim.plugin_installation_id = stale.virtual_owner_installation_id
			  AND claim.content_id = stale.content_id
			  AND claim.media_folder_id = stale.media_folder_id
			  AND claim.file_path = stale.file_path
			  AND (claim.source_key = 'collection' OR claim.source_key LIKE 'collection:%')`,
			seriesID, expectedOwners, expectedPaths, expectedFolders); err != nil {
			return fmt.Errorf("remove stale virtual episode claims: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		WITH expected(owner_id, file_path, folder_id) AS (
			SELECT * FROM unnest($2::bigint[], $3::text[], $4::int[])
		)
		DELETE FROM media_files mf
		USING episodes ep
		WHERE mf.episode_id = ep.content_id
		  AND ep.series_id = $1
		  AND mf.container = 'virtual'
		  AND mf.probe_source = 'virtual_collection'
		  AND mf.virtual_owner_installation_id IS NOT NULL
		  AND (
		      $5 = ''
		      OR mf.media_folder_id IN (
		          SELECT library_id FROM library_collection_libraries WHERE collection_id = $5
		          UNION
		          SELECT library_id FROM library_collections WHERE id = $5
		      )
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM expected
		      WHERE expected.owner_id = mf.virtual_owner_installation_id
		        AND expected.file_path = mf.file_path
		        AND expected.folder_id = mf.media_folder_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM virtual_media_file_source_claims claim
		      WHERE claim.plugin_installation_id = mf.virtual_owner_installation_id
		        AND claim.content_id = mf.content_id
		        AND claim.media_folder_id = mf.media_folder_id
		        AND claim.file_path = mf.file_path
		  )`+virtualFileRetentionGuard,
		seriesID, expectedOwners, expectedPaths, expectedFolders, collectionID); err != nil {
		return fmt.Errorf("remove stale virtual episodes: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM episode_libraries el
		WHERE el.episode_id IN (SELECT content_id FROM episodes WHERE series_id = $1)
		  AND NOT EXISTS (
		      SELECT 1 FROM media_files mf
		      WHERE mf.episode_id = el.episode_id
		        AND mf.media_folder_id = el.media_folder_id
		  )`, seriesID); err != nil {
		return fmt.Errorf("clean unreferenced episode libraries: %w", err)
	}

	return nil
}

// GetByID retrieves a media item by its content ID.
func (r *ItemRepository) GetByID(ctx context.Context, contentID string) (*models.MediaItem, error) {
	query := `SELECT ` + itemColumns + ` FROM media_items WHERE content_id = $1`
	return scanItem(r.pool.QueryRow(ctx, query, contentID))
}

// GetByIDTx retrieves a media item using the supplied transaction.
func (r *ItemRepository) GetByIDTx(ctx context.Context, tx pgx.Tx, contentID string) (*models.MediaItem, error) {
	query := `SELECT ` + itemColumns + ` FROM media_items WHERE content_id = $1`
	return scanItem(tx.QueryRow(ctx, query, contentID))
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
	return r.getByIDsWithAccess(ctx, contentIDs, access, itemAccessConditions)
}

// GetListedByIDsWithAccess is GetByIDsWithAccess for a listing built from
// stored IDs, such as a collection's member page: it also leaves out items in
// no library, as listedItemAccessConditions does.
func (r *ItemRepository) GetListedByIDsWithAccess(ctx context.Context, contentIDs []string, access AccessFilter) ([]*models.MediaItem, error) {
	return r.getByIDsWithAccess(ctx, contentIDs, access, listedItemAccessConditions)
}

func (r *ItemRepository) getByIDsWithAccess(ctx context.Context, contentIDs []string, access AccessFilter, conditions itemConditions) ([]*models.MediaItem, error) {
	if len(contentIDs) == 0 {
		return []*models.MediaItem{}, nil
	}
	if access.AllowedLibraryIDs != nil && len(access.AllowedLibraryIDs) == 0 {
		return []*models.MediaItem{}, nil
	}
	sql, args := r.buildGetByIDsWithAccessSQL(contentIDs, access, conditions)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("fetching media items by IDs with access: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// itemConditions renders access predicates over media_items mi.
type itemConditions func(access AccessFilter, args *[]any, argIdx *int) []string

func (r *ItemRepository) buildGetByIDsWithAccessSQL(contentIDs []string, access AccessFilter, conditions itemConditions) (string, []any) {
	sql := `SELECT ` + itemColumns + `
            FROM media_items mi
            WHERE mi.content_id = ANY($1)`
	args := []any{contentIDs}
	argIdx := 2

	for _, c := range conditions(access, &args, &argIdx) {
		sql += "\n            AND " + c
	}

	sql += " ORDER BY mi.content_id ASC"
	return sql, args
}

// itemAccessConditions are the predicates over media_items mi that decide
// whether the viewer may see an item addressed by ID: library access, the
// maturity limits, and excluded media types.
func itemAccessConditions(access AccessFilter, args *[]any, argIdx *int) []string {
	var conditions []string
	appendLibraryAccessConditions("mi.content_id", access, &conditions, args, argIdx)
	applyAccessFilter("mi", AccessFilter{MaturityLimits: access.MaturityLimits, ExcludedMediaTypes: access.ExcludedMediaTypes}, &conditions, args, argIdx)
	return conditions
}

// listedItemAccessConditions is itemAccessConditions for listings and their
// counts: with no library restriction it still requires the item to be in a
// library, so an item held only by the scanner's file removal grace is left
// out (see InSomeLibraryWhere).
func listedItemAccessConditions(access AccessFilter, args *[]any, argIdx *int) []string {
	conditions := itemAccessConditions(access, args, argIdx)
	if access.AllowedLibraryIDs == nil && len(access.DisabledLibraryIDs) == 0 {
		conditions = append(conditions, InSomeLibraryWhere("mi.content_id"))
	}
	return conditions
}

// CountVisiblePersonalCollectionMembers counts, per collection, the members of
// the account's hand-picked or imported personal collections that the viewer
// can see, as their catalog view counts them. It ignores smart definitions and
// display filters; CountPersonalCollections handles those collections.
// Collections with no visible members are absent from the result. It reads the
// Postgres user store's membership table, so callers must not use it for
// collections kept elsewhere.
func (r *ItemRepository) CountVisiblePersonalCollectionMembers(ctx context.Context, userID int, collectionIDs []string, access AccessFilter) (map[string]int, error) {
	counts := make(map[string]int, len(collectionIDs))
	if len(collectionIDs) == 0 || (access.AllowedLibraryIDs != nil && len(access.AllowedLibraryIDs) == 0) {
		return counts, nil
	}
	sql, args := buildCountVisiblePersonalCollectionMembersSQL(userID, collectionIDs, access)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("counting visible personal collection members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scanning personal collection member count: %w", err)
		}
		counts[id] = n
	}
	return counts, rows.Err()
}

func buildCountVisiblePersonalCollectionMembersSQL(userID int, collectionIDs []string, access AccessFilter) (string, []any) {
	sql := `SELECT upci.collection_id, COUNT(*)
            FROM user_personal_collection_items upci
            JOIN media_items mi ON mi.content_id = upci.media_item_id
            WHERE upci.user_id = $1 AND upci.collection_id = ANY($2) AND upci.sub_item_id = ''`
	args := []any{userID, collectionIDs}
	argIdx := 3
	for _, c := range listedItemAccessConditions(access, &args, &argIdx) {
		sql += "\n            AND " + c
	}
	// The catalog view never lists manga chapters as items.
	sql += "\n            AND " + MangaChapterExclusionWhere("mi")
	return sql + "\n            GROUP BY upci.collection_id", args
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

// Resolve picks the content_id a source entry names, trying its IDs in the
// itemType's anchor precedence (contentid.AnchorPrecedence): TVDB → TMDB →
// IMDb for a series, TMDB → IMDb → TVDB otherwise. It returns "" when no ID
// matches or the lookup is nil.
func (l *ExternalIDLookup) Resolve(itemType, tvdbID, tmdbID, imdbID string) string {
	if l == nil {
		return ""
	}
	for _, provider := range contentid.AnchorPrecedence(itemType) {
		var id string
		switch provider {
		case contentid.ProviderTVDB:
			id = lookupExternalID(l.ByTVDB, tvdbID)
		case contentid.ProviderTMDB:
			id = lookupExternalID(l.ByTMDB, tmdbID)
		case contentid.ProviderIMDB:
			id = lookupExternalID(l.ByIMDb, imdbID)
		}
		if id != "" {
			return id
		}
	}
	return ""
}

func lookupExternalID(byID map[string]string, id string) string {
	if id == "" {
		return ""
	}
	return byID[id]
}

// externalIDLookupChunk caps how many IDs of each provider one
// GetByExternalIDs statement carries.
const externalIDLookupChunk = 1000

// GetByExternalIDs fetches media items matching any of the given external IDs
// of the specified type. Repeated IDs are looked up once, and each statement
// carries at most externalIDLookupChunk IDs per provider, so a long list costs
// a bounded number of bounded statements. Replaces N×3 GetByExternalID calls
// in MDBList collection sync (audit 2026-05-01 §3.7).
func (r *ItemRepository) GetByExternalIDs(ctx context.Context, batch ExternalIDBatch, itemType string) (*ExternalIDLookup, error) {
	out := &ExternalIDLookup{
		ByTMDB: map[string]string{},
		ByIMDb: map[string]string{},
		ByTVDB: map[string]string{},
	}
	tmdbIDs, imdbIDs, tvdbIDs := uniqueExternalIDs(batch.TMDBIDs), uniqueExternalIDs(batch.IMDbIDs), uniqueExternalIDs(batch.TVDBIDs)
	for start := 0; start < max(len(tmdbIDs), len(imdbIDs), len(tvdbIDs)); start += externalIDLookupChunk {
		chunk := ExternalIDBatch{
			TMDBIDs: externalIDChunk(tmdbIDs, start),
			IMDbIDs: externalIDChunk(imdbIDs, start),
			TVDBIDs: externalIDChunk(tvdbIDs, start),
		}
		if err := r.getByExternalIDsChunk(ctx, chunk, itemType, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// getByExternalIDsChunk adds to out the best title for each ID in batch.
// Rows arrive best first, so a shared ID keeps the title GetByExternalID
// would pick. A row can carry another chunk's ID in a column it did not match
// on; only IDs this chunk asked for are kept, so that ID is decided by its own
// chunk's ordering.
func (r *ItemRepository) getByExternalIDsChunk(ctx context.Context, batch ExternalIDBatch, itemType string, out *ExternalIDLookup) error {
	sql, args := r.buildGetByExternalIDsSQL(batch, itemType)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("fetching media items by external IDs: %w", err)
	}
	defer rows.Close()

	tmdbAsked, imdbAsked, tvdbAsked := externalIDSet(batch.TMDBIDs), externalIDSet(batch.IMDbIDs), externalIDSet(batch.TVDBIDs)
	keep := func(by map[string]string, asked map[string]struct{}, id, contentID string) {
		if _, ok := asked[id]; !ok {
			return
		}
		if _, taken := by[id]; !taken {
			by[id] = contentID
		}
	}
	for rows.Next() {
		var contentID, tmdb, imdb, tvdb string
		if err := rows.Scan(&contentID, &tmdb, &imdb, &tvdb); err != nil {
			return fmt.Errorf("scanning external-ID row: %w", err)
		}
		keep(out.ByTMDB, tmdbAsked, tmdb, contentID)
		keep(out.ByIMDb, imdbAsked, imdb, contentID)
		keep(out.ByTVDB, tvdbAsked, tvdb, contentID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating external-ID rows: %w", err)
	}
	return nil
}

// uniqueExternalIDs drops empty and repeated IDs, keeping first-seen order.
func uniqueExternalIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; id == "" || dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// externalIDChunk returns the externalIDLookupChunk IDs starting at start, or
// nil past the end.
func externalIDChunk(ids []string, start int) []string {
	if start >= len(ids) {
		return nil
	}
	return ids[start:min(start+externalIDLookupChunk, len(ids))]
}

func externalIDSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// buildGetByExternalIDsSQL pins the SQL shape used by GetByExternalIDs: one
// statement that matches any of the three external-ID arrays plus a type
// filter, reading the same identity sources as LookupExternalIDs — the
// denormalized media_items columns and the media_item_provider_ids table. A
// provider-table-only id therefore resolves exactly like a column id.
//
// Every match is an `= ANY($n)` predicate, which Postgres hashes when the
// array is large, so cost is linear in catalog size plus request size at both
// a small list and the 100k-entry source cap. The earlier form joined each
// requested id to the whole catalog with an OR across columns; that is not
// sargable, so the planner nested-looped request × catalog and cross-joined
// the provider table, taking ~100s for 500 ids.
//
// TMDB is matched against both its bare form and its "id-slug" URL form
// ("1931-some-title"). Because split_part(value, '-', 1) is exactly the
// identity NormalizeTMDBID extracts — the digits before the first '-' — the
// slug form is matched by comparing that prefix to the same array; the numeric
// prefix never bleeds across ids, so "19310" and "1931-some" never answer for
// "1931". Request TMDB ids are run through NormalizeTMDBID before binding, so
// both sides read the same way. Exact-match semantics are otherwise unchanged:
// no title/year fallback, and the caller still applies its own library filter.
func (r *ItemRepository) buildGetByExternalIDsSQL(batch ExternalIDBatch, itemType string) (string, []any) {
	// Titles match on their own ID columns and on their provider ID rows; a
	// title can be known to a provider only through the latter. Rows arrive
	// best first: direct columns before provider rows, then the order
	// GetByExternalID prefers.
	sql := `SELECT content_id, tmdb, imdb, tvdb FROM (
                SELECT mi.content_id,
                       CASE WHEN mi.tmdb_id = ANY($1) THEN mi.tmdb_id
                            WHEN mi.tmdb_id LIKE '%-%' AND split_part(mi.tmdb_id, '-', 1) = ANY($1) THEN split_part(mi.tmdb_id, '-', 1)
                            ELSE '' END AS tmdb,
                       CASE WHEN mi.imdb_id = ANY($2) THEN mi.imdb_id ELSE '' END AS imdb,
                       CASE WHEN mi.tvdb_id = ANY($3) THEN mi.tvdb_id ELSE '' END AS tvdb,
                       0 AS source_rank, mi.status, mi.updated_at
                FROM media_items mi
                WHERE mi.type = $4
                  AND (mi.tmdb_id = ANY($1) OR mi.imdb_id = ANY($2) OR mi.tvdb_id = ANY($3)
                       OR (mi.tmdb_id LIKE '%-%' AND split_part(mi.tmdb_id, '-', 1) = ANY($1)))
                UNION ALL
                SELECT mi.content_id,
                       CASE WHEN mip.provider = 'tmdb'
                            THEN CASE WHEN mip.provider_id = ANY($1) THEN mip.provider_id
                                      ELSE split_part(mip.provider_id, '-', 1) END
                            ELSE '' END,
                       CASE WHEN mip.provider = 'imdb' THEN mip.provider_id ELSE '' END,
                       CASE WHEN mip.provider = 'tvdb' THEN mip.provider_id ELSE '' END,
                       1, mi.status, mi.updated_at
                FROM media_item_provider_ids mip
                JOIN media_items mi ON mi.content_id = mip.content_id AND mi.type = $4
                WHERE mip.item_type = $4
                  AND ((mip.provider = 'tmdb' AND (mip.provider_id = ANY($1)
                                                   OR (mip.provider_id LIKE '%-%' AND split_part(mip.provider_id, '-', 1) = ANY($1))))
                    OR (mip.provider = 'imdb' AND mip.provider_id = ANY($2))
                    OR (mip.provider = 'tvdb' AND mip.provider_id = ANY($3)))
            ) matches
            ORDER BY
                source_rank,
                CASE lower(trim(status))
                    WHEN 'matched' THEN 0
                    WHEN 'pending' THEN 1
                    WHEN 'unmatched' THEN 2
                    ELSE 3
                END,
                updated_at DESC,
                content_id ASC`
	tmdbIDs := batch.TMDBIDs
	if len(tmdbIDs) > 0 {
		// Mirror the stored-side normalization on the request side. Callers
		// hold canonical numeric ids, so this is normally a no-op.
		normalized := make([]string, len(tmdbIDs))
		for i, id := range tmdbIDs {
			normalized[i] = NormalizeTMDBID(id)
		}
		tmdbIDs = normalized
	}
	return sql, []any{tmdbIDs, batch.IMDbIDs, batch.TVDBIDs, itemType}
}

// TitleWithoutTMDBID is a catalog title known by IMDb or TVDB ID but not by
// TMDB ID.
type TitleWithoutTMDBID struct {
	ContentID string
	IMDbID    string
	TVDBID    string
}

// TitlesWithoutTMDBID returns up to limit titles of itemType that have no bare
// numeric TMDB ID in their own columns or provider ID rows but do have an IMDb
// or TVDB ID. Only these titles can match a TMDB entry through another
// provider's ID. A TMDB ID stored in TMDB's "id-slug" URL form (see
// sameTMDBID) never equals an entry's ID, so such a title is listed too.
// Titles arrive in GetByExternalID's preference order, so when two of them
// resolve to one TMDB ID the caller can keep the first.
func (r *ItemRepository) TitlesWithoutTMDBID(ctx context.Context, itemType string, limit int) ([]TitleWithoutTMDBID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT mi.content_id,
		       COALESCE(NULLIF(mi.imdb_id, ''), imdb.provider_id, ''),
		       COALESCE(NULLIF(mi.tvdb_id, ''), tvdb.provider_id, '')
		FROM media_items mi
		LEFT JOIN media_item_provider_ids imdb ON imdb.content_id = mi.content_id AND imdb.provider = 'imdb'
		LEFT JOIN media_item_provider_ids tvdb ON tvdb.content_id = mi.content_id AND tvdb.provider = 'tvdb'
		WHERE mi.type = $1
		  AND COALESCE(mi.tmdb_id, '') !~ '^[0-9]+$'
		  AND NOT EXISTS (
		      SELECT 1 FROM media_item_provider_ids tmdb
		      WHERE tmdb.content_id = mi.content_id AND tmdb.provider = 'tmdb'
		        AND tmdb.provider_id ~ '^[0-9]+$'
		  )
		  AND (COALESCE(NULLIF(mi.imdb_id, ''), imdb.provider_id, '') <> ''
		       OR COALESCE(NULLIF(mi.tvdb_id, ''), tvdb.provider_id, '') <> '')
		ORDER BY
		    CASE lower(trim(mi.status))
		        WHEN 'matched' THEN 0
		        WHEN 'pending' THEN 1
		        WHEN 'unmatched' THEN 2
		        ELSE 3
		    END,
		    mi.updated_at DESC,
		    mi.content_id ASC
		LIMIT $2`, itemType, limit)
	if err != nil {
		return nil, fmt.Errorf("listing titles without a TMDB ID: %w", err)
	}
	defer rows.Close()
	var out []TitleWithoutTMDBID
	for rows.Next() {
		var title TitleWithoutTMDBID
		if err := rows.Scan(&title.ContentID, &title.IMDbID, &title.TVDBID); err != nil {
			return nil, fmt.Errorf("scanning title without a TMDB ID: %w", err)
		}
		out = append(out, title)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating titles without a TMDB ID: %w", err)
	}
	return out, nil
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
	defer func() { _ = tx.Rollback(ctx) }()

	// Collect image paths before deletion. The outer COALESCE covers every
	// artwork surface: episodes.still_path is still nullable, and a NULL there
	// surfaces under the union's first column name (poster_path), so scanning
	// the union raw fails the whole delete for a series with an unstilled
	// episode.
	imgRows, err := tx.Query(ctx, `
		SELECT COALESCE(poster_path, ''), COALESCE(backdrop_path, ''), COALESCE(logo_path, '')
		FROM (
			SELECT poster_path, backdrop_path, logo_path FROM media_items WHERE content_id = $1
			UNION ALL
			SELECT banner_path, thumb_path, clearart_path FROM media_items WHERE content_id = $1
			UNION ALL
			SELECT disc_path, '', '' FROM media_items WHERE content_id = $1
			UNION ALL
			SELECT path, '', '' FROM media_item_backdrops WHERE content_id = $1
			UNION ALL
			SELECT poster_path, '', '' FROM seasons WHERE series_id = $1
			UNION ALL
			SELECT still_path, '', '' FROM episodes WHERE series_id = $1
		) artwork
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
	if err := imgRows.Err(); err != nil {
		return nil, fmt.Errorf("collecting image paths before delete: %w", err)
	}

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
	ctx, cancel := context.WithTimeout(ctx, postgresSearchTimeout)
	defer cancel()

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
	// An exact normalized title is already the strongest possible correction.
	// The sparse FTS block is complete here, and it already includes related
	// prefix/title matches, so a second trigram scan can only add latency and
	// noisy near-matches. Misspellings and alias-only hits still take the fuzzy
	// path because their hydrated title does not equal the query.
	if searchBlockHasExactTitle(ftsBlock, parsed.ExactTitleHint) {
		fuzzyLimit = 0
	}
	minSimilarity := 0.0
	if len(ftsBlock) > 0 {
		minSimilarity = fuzzyAugmentSimilarityFloor
	}
	var fuzzyBlock []*models.MediaItem
	fuzzyTruncated := false
	if fuzzyLimit > 0 {
		fuzzySQL, _, fuzzyArgs := r.buildFuzzySearchFromParsed(parsed, itemTypes, fuzzyCandidatePool+1, 0, filter, false, contentIDsFromMediaItems(ftsBlock), minSimilarity)
		if fuzzySQL != "" {
			var err error
			fuzzyBlock, fuzzyTruncated, err = r.execFuzzyBlock(ctx, searchTextFromParsed(parsed), fuzzySQL, fuzzyArgs, fuzzyLimit, len(ftsBlock) > 0)
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

// searchBlockHasExactTitle mirrors exact_title_match for a block's titles,
// including yearSuffixedTitleSQL for non-episode items.
func searchBlockHasExactTitle(items []*models.MediaItem, normalizedTitle string) bool {
	if normalizedTitle == "" {
		return false
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		title := normalizeTitleForComparison(item.Title)
		if title == normalizedTitle ||
			(item.Type != recentTVTypeEpisode && item.Year > 0 && title == normalizedTitle+" "+strconv.Itoa(item.Year)) {
			return true
		}
	}
	return false
}

// execFuzzyBlock runs the fuzzy data query inside a transaction that pins
// pg_trgm.strict_word_similarity_threshold, so the <<% operator's selectivity
// cannot drift with cluster configuration (its 0.5 server default would reject
// ordinary typos outright). The query was built with LIMIT fuzzyCandidatePool+1;
// the extra row only signals truncation. The admitted candidates are trimmed to
// fuzzyLimit. exactHits reports whether the FTS block found any results.
func (r *ItemRepository) execFuzzyBlock(ctx context.Context, searchText, dataSQL string, args []any, fuzzyLimit int, exactHits bool) ([]*models.MediaItem, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning fuzzy search tx: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), postgresSearchRollbackTimeout)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL pg_trgm.strict_word_similarity_threshold = %g", trgmWordSimilarityThreshold)); err != nil {
		return nil, false, fmt.Errorf("pinning trigram word-similarity threshold: %w", err)
	}
	block, _, truncated, _, err := r.execSearchBlock(ctx, tx, dataSQL, "", args, fuzzyCandidatePool, 0, false)
	if err != nil {
		return nil, false, err
	}
	aliases, err := loadFuzzyRerankAliases(ctx, tx, searchText, block)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing fuzzy search tx: %w", err)
	}
	block, err = rerankFuzzyItems(ctx, searchText, block, aliases, exactHits)
	if err != nil {
		return nil, false, err
	}
	if len(block) > fuzzyLimit {
		block, truncated = block[:fuzzyLimit], true
	}
	return block, truncated, nil
}

// loadFuzzyRerankAliases reads only aliases that passed the same indexed
// strict-word predicate as the fuzzy candidate query. ROW_NUMBER applies the
// cap per content ID; an item with pathological alias history cannot inflate
// the in-process relevance workload. Each item gets its most similar aliases,
// which usually score best, and the aliases covering the most query words,
// because admitFuzzyRanks drops an item whose loaded aliases miss a word.
// Coverage sums each word's best strict word similarity, which neither word
// order nor extra words lower. Similarity alone ranked "Harbor" and three
// other short aliases above "Tales from the Lantern Keeper's Distant Northern
// Harbor" for "lantrn harbor".
func loadFuzzyRerankAliases(ctx context.Context, q searchQuerier, searchText string, items []*models.MediaItem) (map[string][]string, error) {
	contentIDs := contentIDsFromMediaItems(items)
	if len(contentIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
		WITH matched AS (
			SELECT
				mia.content_id,
				mia.normalized_title,
				ROW_NUMBER() OVER (
					PARTITION BY mia.content_id
					ORDER BY
						0.65 * similarity($2, mia.normalized_title)
							+ 0.35 * strict_word_similarity($2, mia.normalized_title) DESC,
						mia.normalized_title ASC
				) AS similarity_rank,
				ROW_NUMBER() OVER (
					PARTITION BY mia.content_id
					ORDER BY coverage.score DESC, mia.normalized_title ASC
				) AS coverage_rank
			FROM media_item_aliases mia
			CROSS JOIN LATERAL (
				SELECT sum(strict_word_similarity(word, mia.normalized_title)) AS score
				FROM unnest(string_to_array($2, ' ')) AS word
			) coverage
			WHERE mia.content_id = ANY($1)
			  AND $2 <<% mia.normalized_title
		)
		SELECT content_id, normalized_title
		FROM matched
		WHERE similarity_rank <= $3 OR coverage_rank <= $3`, contentIDs, fuzzyCandidateText(searchText), fuzzyRerankAliasesPerOrder)
	if err != nil {
		return nil, fmt.Errorf("loading bounded fuzzy aliases: %w", err)
	}
	defer rows.Close()

	aliases := make(map[string][]string, len(contentIDs))
	for rows.Next() {
		var contentID, normalizedTitle string
		if err := rows.Scan(&contentID, &normalizedTitle); err != nil {
			return nil, fmt.Errorf("scanning fuzzy alias: %w", err)
		}
		aliases[contentID] = append(aliases[contentID], normalizedTitle)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating fuzzy aliases: %w", err)
	}
	return aliases, nil
}

type fuzzyTitleScore struct {
	exact         bool
	contiguous    bool
	matchedTokens int
	// missingWords counts required query words (see fuzzyRequiredWords) the
	// title does not match within their typo allowance.
	missingWords int
	editDistance int
	extraTokens  int
}

// unmatchedFuzzyTitleScore ranks below every scored title variant.
func unmatchedFuzzyTitleScore() fuzzyTitleScore {
	return fuzzyTitleScore{missingWords: math.MaxInt, editDistance: math.MaxInt, extraTokens: math.MaxInt}
}

type fuzzyRankedItem struct {
	item  *models.MediaItem
	score fuzzyTitleScore
	order int
}

// rerankFuzzyItems orders typo candidates best first and returns only the ones
// admitFuzzyRanks keeps. exactHits reports whether the exact FTS search
// already found results for the query.
func rerankFuzzyItems(ctx context.Context, searchText string, items []*models.MediaItem, aliases map[string][]string, exactHits bool) ([]*models.MediaItem, error) {
	ranks, required, err := rankFuzzyItems(ctx, searchText, items, aliases)
	if err != nil || required == 0 {
		return items, err
	}
	ranks = admitFuzzyRanks(ranks, required, exactHits)
	admitted := make([]*models.MediaItem, len(ranks))
	for i, rank := range ranks {
		admitted[i] = rank.item
	}
	return admitted, nil
}

// rankFuzzyItems is a deliberately tiny relevance layer over PostgreSQL's
// indexed candidate set, not another search index. Its edit limits mirror the
// auto-fuzziness policy used by Bleve (Apache-2.0) and Elasticsearch's
// fuzziness AUTO: 0 edits for <=2 characters, 1 for 3-5, and at most 2 for
// longer tokens. PostgreSQL still decides which rows are candidates; this ranks
// a phrase whose every token is a plausible typo correction above titles that
// share one strongly matching word. required counts the query's required
// words; it is 0 when the query has no words or too many to score, and ranks
// then keep PostgreSQL's order.
func rankFuzzyItems(ctx context.Context, searchText string, items []*models.MediaItem, aliases map[string][]string) (ranks []fuzzyRankedItem, required int, err error) {
	query := normalizeTitleForComparison(searchText)
	queryTokens, ok := boundedFuzzyTokens(query, fuzzyRerankMaxQueryTokens)
	scored := ok && len(queryTokens) > 0
	if scored {
		for _, word := range fuzzyRequiredWords(queryTokens) {
			if word {
				required++
			}
		}
	}

	ranks = make([]fuzzyRankedItem, 0, len(items))
	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		score := fuzzyTitleScore{}
		if scored {
			var variants []string
			if item != nil {
				variants = append(variants, aliases[item.ContentID]...)
			}
			if item != nil && len(item.Title) <= fuzzyRerankMaxTitleBytes {
				variants = append(variants, item.Title)
			}
			score = unmatchedFuzzyTitleScore()
			for _, variant := range variants {
				candidate := scoreFuzzyTitleVariant(query, queryTokens, variant)
				if fuzzyTitleScoreBetter(candidate, score) {
					score = candidate
				}
			}
		}
		ranks = append(ranks, fuzzyRankedItem{item: item, score: score, order: i})
	}

	slices.SortStableFunc(ranks, func(a, b fuzzyRankedItem) int {
		if fuzzyTitleScoreBetter(a.score, b.score) {
			return -1
		}
		if fuzzyTitleScoreBetter(b.score, a.score) {
			return 1
		}
		return 0
	})
	return ranks, required, nil
}

// admitFuzzyRanks keeps the typo candidates a search engine would return:
// titles that match every required query word within its typo allowance, as
// Elasticsearch's AND operator and Meilisearch's "all" strategy require. Only
// when no candidate matches every word and the exact search found nothing does
// it relax to the candidates missing the fewest words, as Typesense's
// drop_tokens_threshold does. Ranking alone left every partial match in the
// results: "the god place" listed The Good Place and then each "The ... Place".
// ranks and required come from rankFuzzyItems.
func admitFuzzyRanks(ranks []fuzzyRankedItem, required int, exactHits bool) []fuzzyRankedItem {
	if len(ranks) == 0 {
		return ranks
	}
	fewest := ranks[0].score.missingWords
	if fewest > 0 && (exactHits || fewest >= required) {
		return ranks[:0]
	}
	end := 0
	for end < len(ranks) && ranks[end].score.missingWords == fewest {
		end++
	}
	return ranks[:end]
}

// fuzzyStopwords take no typo and need not appear in a typo match. "the" is
// one edit from "she" and "tho", and a title may drop a leading article
// ("Quiet Place" for "a quiet place"). They still count as matched words, so a
// title containing them ranks higher. The list is English and short on purpose:
// every entry is a word a title can omit without changing which work it names.
var fuzzyStopwords = func() map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.Fields("a an the of in on at to for by with from") {
		words[word] = true
	}
	return words
}()

// fuzzyCandidateText is the text the trigram candidate search matches: the
// normalized query without stopwords. Stopwords need not appear in a typo
// match, and their trigrams occur in much of a catalog: in 191,307 titles,
// "the godfathr" admitted 51,530 candidates and "godfathr" 456, which kept the
// query near its deadline. When no other word has fuzzyMinTokenLen characters
// ("from up"), the remaining words form too few trigrams to be selective, so
// the whole query is matched as before.
func fuzzyCandidateText(searchText string) string {
	words := strings.Fields(normalizeTitleForComparison(searchText))
	content := make([]string, 0, len(words))
	selective := false
	for _, word := range words {
		if !fuzzyStopwords[word] {
			content = append(content, word)
			selective = selective || utf8.RuneCountInString(word) >= fuzzyMinTokenLen
		}
	}
	if !selective {
		return strings.Join(words, " ")
	}
	return strings.Join(content, " ")
}

func fuzzyTokenMaxEdits(token []rune) int {
	if fuzzyStopwords[string(token)] {
		return 0
	}
	return fuzzyAutoMaxEdits(len(token))
}

// fuzzyRequiredWords marks the query words a typo match must contain: every
// word except stopwords, or every word when the query has only stopwords.
func fuzzyRequiredWords(queryTokens [][]rune) []bool {
	required := make([]bool, len(queryTokens))
	content := false
	for i, token := range queryTokens {
		required[i] = !fuzzyStopwords[string(token)]
		content = content || required[i]
	}
	if !content {
		for i := range required {
			required[i] = true
		}
	}
	return required
}

func scoreFuzzyTitleVariant(query string, queryTokens [][]rune, rawTitle string) fuzzyTitleScore {
	title := normalizeTitleForComparison(rawTitle)
	titleTokens, ok := boundedFuzzyTokens(title, fuzzyRerankMaxTitleTokens)
	if !ok || len(titleTokens) == 0 {
		return unmatchedFuzzyTitleScore()
	}
	// Compare without spaces so "Live Stream From Hell" is an exact match for
	// "Livestream from Hell" and "spiderman" for "Spider-Man".
	compactTitle, compactQuery := compactFuzzyTokens(titleTokens), compactFuzzyTokens(queryTokens)
	score := fuzzyTitleScore{
		exact:       compactTitle == compactQuery,
		contiguous:  strings.Contains(compactTitle, compactQuery),
		extraTokens: absInt(len(titleTokens) - len(queryTokens)),
	}
	// A query word may also match two adjacent title words joined, and two
	// adjacent query words joined may match one title word.
	joinedTitleTokens := joinAdjacentFuzzyTokens(titleTokens)
	titleTerms := slices.Concat(titleTokens, joinedTitleTokens)
	matches := make([]fuzzyTokenMatch, len(queryTokens))
	for i, token := range queryTokens {
		matches[i] = bestFuzzyTokenMatch(token, titleTokens, joinedTitleTokens)
	}
	matched := make([]bool, len(queryTokens))
	for i := 0; i < len(queryTokens); i++ {
		if i+1 < len(queryTokens) && (!matches[i].ok || !matches[i+1].ok) && fuzzyTokensJoinable(queryTokens[i], queryTokens[i+1]) {
			if pair := bestFuzzyPairMatch(queryTokens[i], queryTokens[i+1], titleTerms); pair.ok {
				matched[i], matched[i+1] = true, true
				score.matchedTokens += 2
				score.editDistance += pair.distance
				i++
				continue
			}
		}
		if matches[i].ok {
			matched[i] = true
			score.matchedTokens++
		}
		score.editDistance += matches[i].distance
	}
	if !score.exact && !spellsConsecutiveFuzzyTokens(titleTokens, compactQuery) {
		for i, required := range fuzzyRequiredWords(queryTokens) {
			if required && !matched[i] {
				score.missingWords++
			}
		}
	}
	return score
}

// spellsConsecutiveFuzzyTokens reports whether compactQuery spells a run of
// whole title words typed without spaces: "lordoftherings" for "The Lord of
// the Rings". Word matching joins at most two words, so such a query would
// otherwise miss every word of the title it names. The run must start and end
// on word boundaries, so "heat" does not match inside "Theater".
func spellsConsecutiveFuzzyTokens(titleTokens [][]rune, compactQuery string) bool {
	for start := range titleTokens {
		for end := start + 1; end <= len(titleTokens); end++ {
			run := compactFuzzyTokens(titleTokens[start:end])
			if run == compactQuery {
				return true
			}
			if len(run) >= len(compactQuery) {
				break
			}
		}
	}
	return false
}

type fuzzyTokenMatch struct {
	distance int  // ranking cost; maxEdits+1 when the word does not match
	ok       bool // the word matches within its typo allowance
}

// bestFuzzyTokenMatch matches a query word against single title words and
// adjacent title words joined. A joined match costs one extra edit, so a title
// spelling the word as one ranks first: for "the ofice", "The Office" before
// "The Clink of Ice".
func bestFuzzyTokenMatch(token []rune, words, joined [][]rune) fuzzyTokenMatch {
	maxEdits := fuzzyTokenMaxEdits(token)
	best := fuzzyTokenMatch{distance: maxEdits + 1}
	for penalty, terms := range [][][]rune{words, joined} {
		for _, term := range terms {
			distance := boundedLevenshteinRunes(token, term, maxEdits)
			if distance <= maxEdits && (!best.ok || distance+penalty < best.distance) {
				best = fuzzyTokenMatch{distance: distance + penalty, ok: true}
			}
		}
	}
	return best
}

// bestFuzzyPairMatch matches two adjacent query words against one term split
// into two parts, each part within its own word's typo allowance. Splitting
// keeps a short word from being dropped: "ed wood" does not match "Wood", nor
// "a spider" match "Spider".
func bestFuzzyPairMatch(first, second []rune, terms [][]rune) fuzzyTokenMatch {
	firstEdits, secondEdits := fuzzyTokenMaxEdits(first), fuzzyTokenMaxEdits(second)
	best := firstEdits + secondEdits + 1
	for _, term := range terms {
		if absInt(len(term)-len(first)-len(second)) > firstEdits+secondEdits {
			continue
		}
		for split := max(0, len(first)-firstEdits); split <= min(len(term), len(first)+firstEdits); split++ {
			head := boundedLevenshteinRunes(first, term[:split], firstEdits)
			if head > firstEdits {
				continue
			}
			if tail := boundedLevenshteinRunes(second, term[split:], secondEdits); tail <= secondEdits {
				best = min(best, head+tail)
			}
		}
	}
	return fuzzyTokenMatch{distance: best, ok: best <= firstEdits+secondEdits}
}

func joinAdjacentFuzzyTokens(tokens [][]rune) [][]rune {
	if len(tokens) < 2 {
		return nil
	}
	joined := make([][]rune, 0, len(tokens)-1)
	for i := 0; i+1 < len(tokens); i++ {
		if fuzzyTokensJoinable(tokens[i], tokens[i+1]) {
			joined = append(joined, slices.Concat(tokens[i], tokens[i+1]))
		}
	}
	return joined
}

// compactFuzzyTokens joins tokens without spaces, except between two numbers.
func compactFuzzyTokens(tokens [][]rune) string {
	var b strings.Builder
	for i, token := range tokens {
		if i > 0 && !fuzzyTokensJoinable(tokens[i-1], token) {
			b.WriteByte(' ')
		}
		b.WriteString(string(token))
	}
	return b.String()
}

// fuzzyTokensJoinable reports whether two adjacent words may be read as one.
// Normalization turns number words into digits, so joining "1 2" into "12"
// would make "One Two" an exact match for "Twelve".
func fuzzyTokensJoinable(left, right []rune) bool {
	return !unicode.IsDigit(left[len(left)-1]) || !unicode.IsDigit(right[0])
}

func fuzzyTitleScoreBetter(a, b fuzzyTitleScore) bool {
	if a.exact != b.exact {
		return a.exact
	}
	if a.missingWords != b.missingWords {
		return a.missingWords < b.missingWords
	}
	if a.matchedTokens != b.matchedTokens {
		return a.matchedTokens > b.matchedTokens
	}
	if a.contiguous != b.contiguous {
		return a.contiguous
	}
	if a.editDistance != b.editDistance {
		return a.editDistance < b.editDistance
	}
	return a.extraTokens < b.extraTokens
}

func boundedFuzzyTokens(normalized string, maxTokens int) ([][]rune, bool) {
	fields := strings.Fields(normalized)
	if len(fields) > maxTokens {
		return nil, false
	}
	tokens := make([][]rune, 0, len(fields))
	for _, field := range fields {
		runes := []rune(field)
		if len(runes) > fuzzyRerankMaxTokenRunes {
			return nil, false
		}
		tokens = append(tokens, runes)
	}
	return tokens, true
}

func fuzzyAutoMaxEdits(tokenRunes int) int {
	switch {
	case tokenRunes <= 2:
		return 0
	case tokenRunes <= 5:
		return 1
	default:
		return 2
	}
}

// boundedLevenshteinRunes computes only the small edit bands used by typo
// search. Rows that cannot finish within maxEdits return maxEdits+1 early, so
// CPU and scratch memory are bounded by short title-token lengths.
func boundedLevenshteinRunes(a, b []rune, maxEdits int) int {
	if absInt(len(a)-len(b)) > maxEdits {
		return maxEdits + 1
	}
	if len(a) == 0 {
		if len(b) <= maxEdits {
			return len(b)
		}
		return maxEdits + 1
	}
	if len(b) == 0 {
		if len(a) <= maxEdits {
			return len(a)
		}
		return maxEdits + 1
	}
	if len(a) > len(b) {
		a, b = b, a
	}

	previous := make([]int, len(a)+1)
	current := make([]int, len(a)+1)
	for i := range previous {
		previous[i] = i
	}
	for row, right := range b {
		current[0] = row + 1
		rowMin := current[0]
		for col, left := range a {
			cost := 0
			if left != right {
				cost = 1
			}
			current[col+1] = minInt(
				previous[col+1]+1,
				current[col]+1,
				previous[col]+cost,
			)
			if current[col+1] < rowMin {
				rowMin = current[col+1]
			}
		}
		if rowMin > maxEdits {
			return maxEdits + 1
		}
		previous, current = current, previous
	}
	if previous[len(a)] > maxEdits {
		return maxEdits + 1
	}
	return previous[len(a)]
}

func minInt(values ...int) int {
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
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
	rows, err := q.Query(ctx, dataSQL, searchPlanArgs(args)...)
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
		if err := q.QueryRow(ctx, countSQL, searchPlanArgs(countArgs)...).Scan(&total); err != nil {
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
//	parsed.NormalizedText (leading-short title lookups only)
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
	// the fuzzy fallback that share this helper used it. The helper emits
	// independent EXISTS/NOT EXISTS subqueries keyed on mi.content_id and needs
	// no JOIN, and as a listing it leaves out an item in no library.
	appendListedLibraryAccessConditions("mi.content_id", filter, conditions, args, argIdx)

	applyAccessFilter("mi", AccessFilter{MaturityLimits: filter.MaturityLimits, ExcludedMediaTypes: filter.ExcludedMediaTypes}, conditions, args, argIdx)

	// Manga chapters (type='ebook' rows linked into a manga series) are internal
	// sub-units and must never surface as standalone search results.
	*conditions = append(*conditions, MangaChapterExclusionWhere("mi"))
	return fromClause
}

// buildFuzzySearchSQL assembles the trigram fuzzy-fallback query invoked by
// SearchPage only when the FTS query is sparse. Unlike buildSearchSQLWithTotal,
// the sole match predicate is the pg_trgm strict-word-similarity operator
// (<<%) against the indexed title_normalized generated column (migration 105's
// gin_trgm_ops index serves it), and the ranking signal blends
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
// order: $1 searchText, $2 its fuzzyCandidateText, then the similarity floor
// (if any), then the shared scope placeholders, then the exclusion array, then
// limit/offset.
func (r *ItemRepository) buildFuzzySearchSQL(query string, itemTypes []string, limit, offset int, filter AccessFilter, includeTotal bool, excludeContentIDs []string, minSimilarity float64) (dataSQL, countSQL string, args []any) {
	return r.buildFuzzySearchFromParsed(parseSearchQuery(query), itemTypes, limit, offset, filter, includeTotal, excludeContentIDs, minSimilarity)
}

// buildFuzzySearchFromParsed is the fuzzy query builder proper; the string-taking
// wrapper above parses first. SearchPage parses once and calls this directly.
func (r *ItemRepository) buildFuzzySearchFromParsed(parsed parsedSearchQuery, itemTypes []string, limit, offset int, filter AccessFilter, includeTotal bool, excludeContentIDs []string, minSimilarity float64) (dataSQL, countSQL string, args []any) {
	return r.buildFuzzySearchCursorSQL(parsed, itemTypes, limit, offset, filter, includeTotal, excludeContentIDs, minSimilarity, nil)
}
func (r *ItemRepository) buildFuzzySearchCursorSQL(parsed parsedSearchQuery, itemTypes []string, limit, offset int, filter AccessFilter, includeTotal bool, excludeContentIDs []string, minSimilarity float64, cursor *searchCursorSQL) (dataSQL, countSQL string, args []any) {
	searchText := searchTextFromParsed(parsed)
	if searchText == "" {
		return "", "", nil
	}

	// Candidates and their trigram rank come from the query's content words
	// ($2); the whole-title floor and tie-break compare the full query ($1).
	args = []any{searchText, fuzzyCandidateText(searchText)}
	argIdx := 3
	// Strict word similarity (<<%) rather than full-string similarity (%): a
	// typo of one word must still reach long titles ("avegners" → "Avengers:
	// Endgame"), where full-string similarity is diluted by every extra trigram
	// the rest of the title contributes. <<% scores the query against the best
	// word-boundary extent of the title instead, and at equal thresholds it is
	// a strict superset of % (the whole string is itself a valid extent). The
	// same gin_trgm_ops index serves both operators.
	// Title and alias candidates are independent indexed arms. They are unioned
	// by content_id before media_items is hydrated. Do not correlate alias
	// scoring back to each media row: PostgreSQL can turn that shape into a full
	// alias-index filter for every candidate (observed cost >112k and a real
	// 3-second timeout for "Gane of Throns"). This shape scans each trigram GIN
	// index once and scores only the rows it returned.
	var conditions []string
	if minSimilarity > 0 {
		// The floor is whole-title similarity, NOT word similarity: augmenting
		// a query that already has hits must only admit near-identical titles,
		// and word similarity rates embedded prefix words far too high (see
		// fuzzyAugmentSimilarityFloor).
		conditions = append(conditions, fmt.Sprintf("cs.fuzzy_full_rank >= $%d", argIdx))
		args = append(args, minSimilarity)
		argIdx++
	}

	fromClause := appendSearchScopeFilters(itemTypes, filter, &conditions, &args, &argIdx)
	r.appendSearchCursorDefinition(cursor, false, filter, &conditions, &args, &argIdx)
	if cursor != nil && cursor.err != nil {
		return "", "", nil
	}

	if len(excludeContentIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("NOT (mi.content_id = ANY($%d))", argIdx))
		args = append(args, excludeContentIDs)
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// candidate_scores has exactly one row per content_id, so hydration needs no
	// wide GROUP BY over all media columns. COUNT(*) OVER () therefore counts
	// distinct visible items directly.
	qualifiedCols := qualifiedItemColumns("mi")
	// Candidate recall uses strict word similarity so a typo can still reach a
	// long title. Ranking cannot use that score alone: a single matching word
	// can otherwise outrank the intended phrase (for example "Gane of Throns"
	// used to rank "Justice League: Throne of Atlantis" above "Game of
	// Thrones"). Blend mostly whole-title closeness with enough word similarity
	// to keep long-title typo recovery useful (for example "Hary Poter" still
	// prefers a Harry Potter title over "Miss Potter"). The whole-title score
	// remains the deterministic first tie-break. Both signals are computed only
	// over the indexed candidate set, never the full catalog.
	scoredCTE := fmt.Sprintf(`
		WITH alias_candidates AS MATERIALIZED (
			SELECT
				mia.content_id,
				MAX(
					0.65 * similarity($2, mia.normalized_title)
						+ 0.35 * strict_word_similarity($2, mia.normalized_title)
				) AS fuzzy_rank,
				MAX(similarity(public.normalize_search_text($1), mia.normalized_title)) AS fuzzy_full_rank
			FROM media_item_aliases mia
			WHERE $2 <<%% mia.normalized_title
			GROUP BY mia.content_id
		), candidates AS (
			SELECT
				mi.content_id,
				0.65 * similarity($2, mi.title_normalized)
					+ 0.35 * strict_word_similarity($2, mi.title_normalized) AS fuzzy_rank,
				similarity(public.normalize_search_text($1), mi.title_normalized) AS fuzzy_full_rank
			FROM media_items mi
			WHERE $2 <<%% mi.title_normalized
			UNION ALL
			SELECT content_id, fuzzy_rank, fuzzy_full_rank
			FROM alias_candidates
		), candidate_scores AS (
			SELECT content_id, MAX(fuzzy_rank) AS fuzzy_rank, MAX(fuzzy_full_rank) AS fuzzy_full_rank
			FROM candidates
			GROUP BY content_id
		), scored AS (
			SELECT
				%s,
				cs.fuzzy_rank,
				cs.fuzzy_full_rank
			FROM %s
			JOIN candidate_scores cs ON cs.content_id = mi.content_id
			%s
		)
	`, qualifiedCols, fromClause, whereClause)

	totalColumn := ""
	if includeTotal {
		totalColumn = ", COUNT(*) OVER () AS total_count"
	}
	if cursor != nil {
		totalColumn += ", fuzzy_rank::text, fuzzy_full_rank::text, LOWER(title)::text, content_id::text"
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
// revisit legacy scanner ambiguities after inference heuristics improve. It
// leaves out items an admin ignored: the matcher would skip them anyway, and
// the scan would report each one as still unmatched.
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
		  AND mi.match_ignored_at IS NULL
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
	applyAccessFilter("mi", AccessFilter{MaturityLimits: filter.MaturityLimits, ExcludedMediaTypes: filter.ExcludedMediaTypes}, &conditions, &args, &argIdx)

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
	applyAccessFilter("mi", AccessFilter{MaturityLimits: filter.MaturityLimits, ExcludedMediaTypes: filter.ExcludedMediaTypes}, &conditions, &args, &argIdx)

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
	defer func() { _ = tx.Rollback(ctx) }()

	if err := r.ReplacePeopleTx(ctx, tx, contentID, people); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReplacePeopleTx is ReplacePeople in the caller's transaction.
func (r *ItemRepository) ReplacePeopleTx(ctx context.Context, tx pgx.Tx, contentID string, people []models.ItemPerson) error {
	if _, err := tx.Exec(ctx, "DELETE FROM item_people WHERE content_id = $1", contentID); err != nil {
		return fmt.Errorf("delete existing people: %w", err)
	}
	return r.insertPeopleTx(ctx, tx, contentID, people)
}

// PersonKindsOf returns the distinct credit kinds in people, in first-seen
// order.
func PersonKindsOf(people []models.ItemPerson) []models.PersonKind {
	var kinds []models.PersonKind
	for _, p := range people {
		if !slices.Contains(kinds, p.Kind) {
			kinds = append(kinds, p.Kind)
		}
	}
	return kinds
}

// ReplacePeopleKindsTx replaces only the credits of the given kinds in the
// caller's transaction and leaves every other kind on the item as it is.
func (r *ItemRepository) ReplacePeopleKindsTx(ctx context.Context, tx pgx.Tx, contentID string, kinds []models.PersonKind, people []models.ItemPerson) error {
	if len(kinds) == 0 {
		return nil
	}
	kindValues := make([]int, len(kinds))
	for i, k := range kinds {
		kindValues[i] = int(k)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM item_people WHERE content_id = $1 AND kind = ANY($2)", contentID, kindValues); err != nil {
		return fmt.Errorf("delete existing people: %w", err)
	}
	return r.insertPeopleTx(ctx, tx, contentID, people)
}

// insertPeopleTx inserts credits and queues the item's search-index update.
func (r *ItemRepository) insertPeopleTx(ctx context.Context, tx pgx.Tx, contentID string, people []models.ItemPerson) error {
	if len(people) == 0 {
		if err := r.searchIndexEvents.EnqueueUpsert(ctx, tx, contentID); err != nil {
			return fmt.Errorf("enqueueing catalog search people update: %w", err)
		}
		return nil
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
		key := dedupKey{p.ID, p.Kind, p.Character}
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
		args = append(args, rowID, contentID, p.ID, p.Kind, p.Character, p.SortOrder)
	}
	sb.WriteString(" ON CONFLICT (content_id, person_id, kind, character) DO UPDATE SET sort_order = EXCLUDED.sort_order")

	if _, err := tx.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("insert people: %w", err)
	}

	if err := r.searchIndexEvents.EnqueueUpsert(ctx, tx, contentID); err != nil {
		return fmt.Errorf("enqueueing catalog search people update: %w", err)
	}
	return nil
}

// ClearPeopleTx removes every credit on a media item in the caller's
// transaction, so the change commits or fails with the item write it belongs
// to.
func (r *ItemRepository) ClearPeopleTx(ctx context.Context, tx pgx.Tx, contentID string) error {
	if _, err := tx.Exec(ctx, "DELETE FROM item_people WHERE content_id = $1", contentID); err != nil {
		return fmt.Errorf("delete existing people: %w", err)
	}
	if err := r.searchIndexEvents.EnqueueUpsert(ctx, tx, contentID); err != nil {
		return fmt.Errorf("enqueueing catalog search people update: %w", err)
	}
	return nil
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
// setting only the non-nil fields in upd and NULLing the ones it clears. A
// cleared rating also loses its per-source row. Always bumps updated_at.
// Returns ErrItemNotFound if no row matches contentID.
func (r *ItemRepository) UpdateMetadata(ctx context.Context, contentID string, upd *MetadataUpdate) error {
	tx, err := r.pool.BeginTx(ctx, MetadataCurationTxOptions(ctx))
	if err != nil {
		return fmt.Errorf("begin metadata update tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := lockCuratedProviderIDs(ctx, tx, contentID, upd); err != nil {
		return err
	}
	if err := r.UpdateMetadataTx(ctx, tx, contentID, upd); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit metadata update tx: %w", err)
	}
	return nil
}

// UpdateProvisionalArtwork applies upd, an artwork-only update, to the item
// read describes while that item is still pending, unmatched or ambiguous,
// its images lock is as read had it, and each poster, backdrop or logo slot upd
// writes still has read's source. A match, an Identify, or an admin's artwork
// pick or lock that committed since the read wins: nothing is written and it
// returns false.
func (r *ItemRepository) UpdateProvisionalArtwork(ctx context.Context, read *models.MediaItem, upd *MetadataUpdate) (bool, error) {
	contentID := read.ContentID
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin provisional artwork update tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op once committed

	// A slot upd leaves alone is not compared (NULL expected source).
	expected := func(written *string, source string) *string {
		if written == nil {
			return nil
		}
		return &source
	}
	var one int
	err = tx.QueryRow(ctx, `
		SELECT 1 FROM media_items
		WHERE content_id = $1
		  AND lower(trim(status)) IN ('pending', 'unmatched', 'ambiguous')
		  AND ($2::text IS NULL OR COALESCE(poster_source_path, '') = $2)
		  AND ($3::text IS NULL OR COALESCE(backdrop_source_path, '') = $3)
		  AND ($4::text IS NULL OR COALESCE(logo_source_path, '') = $4)
		  AND COALESCE(`+imagesLockedSQL("locked_fields")+`, false) = $5
		FOR UPDATE`, contentID,
		expected(upd.PosterSourcePath, read.PosterSourcePath),
		expected(upd.BackdropSourcePath, read.BackdropSourcePath),
		expected(upd.LogoSourcePath, read.LogoSourcePath),
		slices.Contains(read.LockedFields, fieldImagesLocked),
	).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("locking provisional item artwork: %w", err)
	}
	if err := r.UpdateMetadataTx(ctx, tx, contentID, upd); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit provisional artwork update tx: %w", err)
	}
	return true, nil
}

// UpdateMetadataTx applies the same metadata update using the caller's
// transaction. It lets workflows that also replace durable identity keep the
// identity, scalar metadata, search-index event, and terminal timestamp in one
// atomic write.
func (r *ItemRepository) UpdateMetadataTx(ctx context.Context, tx pgx.Tx, contentID string, upd *MetadataUpdate) error {
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
	if upd.ContentRating != nil {
		// content_rating_age is derived, so an edit that changes the rating
		// string has to rewrite it in the same statement; leaving the old age
		// behind would keep filtering by the previous certification.
		setClauses = append(setClauses, fmt.Sprintf("content_rating_age = $%d", argIdx))
		args = append(args, access.StoredRating(*upd.ContentRating))
		argIdx++
	}
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
	if upd.Status != nil && strings.EqualFold(strings.TrimSpace(*upd.Status), "matched") {
		// An admin's ignore only means something while the item is unmatched.
		setClauses = append(setClauses, "match_ignored_at = NULL")
	}
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
	setClauses = append(setClauses, upd.nullClauses(itemClearableColumns...)...)

	setClauses = append(setClauses, "updated_at = NOW()")

	query := fmt.Sprintf("UPDATE media_items SET %s WHERE content_id = $%d",
		strings.Join(setClauses, ", "), argIdx)
	args = append(args, contentID)

	// The item's ID columns before the edit, read under the lock the UPDATE
	// takes anyway; the durable-ID sync below skips an ID this edit resends.
	var storedTMDB, storedIMDb, storedTVDB string
	if upd.TmdbID != nil || upd.ImdbID != nil || upd.TvdbID != nil {
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(tmdb_id, ''), COALESCE(imdb_id, ''), COALESCE(tvdb_id, '')
			FROM media_items WHERE content_id = $1 FOR NO KEY UPDATE`, contentID).Scan(&storedTMDB, &storedIMDb, &storedTVDB)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("reading edited item provider ids: %w", err)
		}
	}
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating media item metadata: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	if upd.Runtime != nil || upd.LockedFields != nil {
		// Removing the runtime lock restores the file-derived value even
		// when an unchanged audiobook will be skipped by the next scan.
		if _, err := tx.Exec(ctx, `SELECT refresh_audiobook_runtime(ARRAY[$1]::text[])`, contentID); err != nil {
			return fmt.Errorf("refreshing audiobook runtime: %w", err)
		}
	}
	// The UPDATE above locked the item row before its rating sources, the
	// order every rating source writer follows (see lockRatingSourceItem).
	if sources := upd.clearedRatingSources(); len(sources) > 0 {
		if _, err := tx.Exec(ctx, `
			DELETE FROM media_item_rating_sources
			WHERE content_id = $1 AND source = ANY($2::text[])`,
			contentID, sources); err != nil {
			return fmt.Errorf("deleting cleared rating sources: %w", err)
		}
	}
	for _, edited := range []struct {
		provider string
		value    *string
		stored   string
	}{
		{contentid.ProviderTMDB, upd.TmdbID, storedTMDB},
		{contentid.ProviderTVDB, upd.TvdbID, storedTVDB},
		{contentid.ProviderIMDB, upd.ImdbID, storedIMDb},
	} {
		// Only a changed ID is a choice, as lockCuratedProviderIDs judges it: an
		// edit form resends unchanged IDs, and a curator's scope check skips
		// them, so their durable rows stay as they are.
		if edited.value == nil || strings.EqualFold(strings.TrimSpace(*edited.value), strings.TrimSpace(edited.stored)) {
			continue
		}
		if err := syncEditedProviderIDTx(ctx, tx, contentID, edited.provider, *edited.value); err != nil {
			return err
		}
	}
	if err := r.searchIndexEvents.EnqueueUpsert(ctx, tx, contentID); err != nil {
		return fmt.Errorf("enqueueing catalog search metadata update: %w", err)
	}
	return nil
}

// UpdateStatus updates only the status and updated_at columns of media_items.
// It avoids full-row upserts that could clobber concurrent alias updates.
func (r *ItemRepository) UpdateStatus(ctx context.Context, contentID, status string) error {
	if r == nil || r.pool == nil {
		return ErrItemNotFound
	}
	tag, err := r.pool.Exec(ctx, `UPDATE media_items SET status = $2, updated_at = NOW() WHERE content_id = $1`, contentID, status)
	if err != nil {
		return fmt.Errorf("update item status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

// UpdateEpisodeMetadataState updates only the episode metadata completeness
// columns and updated_at on media_items, preventing full-row upserts from clobbering aliases.
func (r *ItemRepository) UpdateEpisodeMetadataState(ctx context.Context, seriesID string, incomplete bool, lastCheckedAt *time.Time) error {
	if r == nil || r.pool == nil {
		return ErrItemNotFound
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_items 
		SET episode_metadata_incomplete = $2,
		    episode_metadata_last_checked_at = $3,
		    updated_at = NOW()
		WHERE content_id = $1`, seriesID, incomplete, lastCheckedAt)
	if err != nil {
		return fmt.Errorf("update episode metadata state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

// syncEditedProviderIDTx makes the item's durable provider ID for provider
// match an edited ID column. Lookups, AttachTMDBID and a refresh's ID seeding
// read the durable row, so leaving it would keep the replaced or cleared ID
// in use. Callers sync providers in preferredProviderIDOrder, the order
// ReplaceByContentIDTx writes them in, so the two can't wait on each other.
//
// An ID another item of the same type already holds stays on the column only,
// as before this sync existed; the next refresh's conflict recovery decides
// between the two items. The NOT EXISTS check settles that without waiting on
// the holder's row, so two edits that swap IDs don't deadlock. A holder that
// commits after the check fails the insert on the unique key instead; the
// savepoint confines that failure to the durable row, so the edit still saves.
// ReplaceByContentID and the provider-ID drift repair lock the item before its
// rows, so they wait for the edit; the upsert overwrites a row any other
// writer puts back between the delete and the insert.
func syncEditedProviderIDTx(ctx context.Context, tx pgx.Tx, contentID, provider, value string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM media_item_provider_ids
		WHERE content_id = $1 AND lower(provider) = $2`, contentID, provider); err != nil {
		return fmt.Errorf("deleting edited %s provider id: %w", provider, err)
	}
	if value == "" {
		return nil
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storing edited %s provider id: %w", provider, err)
	}
	defer savepoint.Rollback(ctx) //nolint:errcheck // a no-op once the savepoint is released
	_, err = savepoint.Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id, created_at, updated_at)
		SELECT mi.content_id, mi.type, $2, $3, NOW(), NOW()
		FROM media_items mi
		WHERE mi.content_id = $1
		  AND NOT EXISTS (
			SELECT 1 FROM media_item_provider_ids held
			WHERE held.provider = $2 AND held.provider_id = $3
			  AND held.item_type = mi.type AND held.content_id <> mi.content_id
		  )
		ON CONFLICT (content_id, provider) DO UPDATE
		SET item_type = EXCLUDED.item_type,
		    provider_id = EXCLUDED.provider_id,
		    updated_at = NOW()`, contentID, provider, value)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok &&
		pgErr.Code == "23505" && pgErr.ConstraintName == "media_item_provider_ids_provider_provider_id_item_type_key" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("storing edited %s provider id: %w", provider, err)
	}
	if err := savepoint.Commit(ctx); err != nil {
		return fmt.Errorf("storing edited %s provider id: %w", provider, err)
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
	case artworkkey.ImageBanner, artworkkey.ImageThumb:
		// imageType is one of the two literals matched above, so it is safe
		// to splice into the column names.
		query = fmt.Sprintf(`
			UPDATE media_items
			SET %[1]s_path = $3,
				%[1]s_source_path = $2,
				%[1]s_thumbhash = $4,
				updated_at = NOW()
			WHERE content_id = $1
			  AND %[1]s_source_path = $2`, imageType)
		args = []any{contentID, sourcePath, cachedPath, thumbhash}
	case artworkkey.ImageClearArt, artworkkey.ImageDisc:
		query = fmt.Sprintf(`
			UPDATE media_items
			SET %[1]s_path = $3,
				%[1]s_source_path = $2,
				updated_at = NOW()
			WHERE content_id = $1
			  AND %[1]s_source_path = $2`, imageType)
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

// TryClaimTrailersRefresh atomically consumes an item's trailer-refresh
// cooldown slot. The UPDATE is the gate: it writes NOW() only when the stored
// timestamp is NULL or older than the cooldown window, so concurrent callers
// cannot both win.
//
// Either way the returned timestamp is the value now stored in the column. A
// winner needs it to release its own claim later (see
// ReleaseTrailersRefreshClaim); a loser needs it to compute the next-allowed
// time. Losing the gate means either the item is in cooldown or it no longer
// exists, which the follow-up read distinguishes — a missing row yields
// ErrItemNotFound.
//
// The classification spans two statements, so a concurrent release can land
// between them: the UPDATE loses to another request's claim, that request's
// refresh fails and NULLs the column, and the follow-up SELECT then reads a
// free slot. Reporting that as cooldown would be a cooldown with no
// next-allowed time and no refresh actually running, so a NULL read retries
// the claim once — the slot is demonstrably free, and this caller may take it.
//
// Contract for the three outcomes, which callers rely on to avoid emitting an
// undateable cooldown:
//   - (true, ts, nil): claimed; ts is the stored timestamp to release on.
//   - (false, ts, nil): in cooldown until ts plus the window.
//   - (false, nil, nil): lost the gate twice while the slot kept being freed,
//     so another request is claiming it right now. Not a cooldown — the caller
//     should treat it as "an equivalent refresh is already in flight", the same
//     answer it gives when it loses the in-process dedup claim.
func (r *ItemRepository) TryClaimTrailersRefresh(ctx context.Context, contentID string, cooldown time.Duration) (bool, *time.Time, error) {
	const maxAttempts = 2
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var claimedAt time.Time
		err := r.pool.QueryRow(ctx, `
			UPDATE media_items
			SET trailers_refresh_requested_at = NOW()
			WHERE content_id = $1
			  AND (trailers_refresh_requested_at IS NULL
			       OR trailers_refresh_requested_at < NOW() - $2::interval)
			RETURNING trailers_refresh_requested_at`,
			contentID, fmt.Sprintf("%d seconds", int64(cooldown.Seconds())),
		).Scan(&claimedAt)
		switch {
		case err == nil:
			return true, &claimedAt, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return false, nil, fmt.Errorf("claiming trailers refresh: %w", err)
		}

		var requestedAt *time.Time
		err = r.pool.QueryRow(ctx, `
			SELECT trailers_refresh_requested_at
			FROM media_items
			WHERE content_id = $1`,
			contentID,
		).Scan(&requestedAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil, ErrItemNotFound
			}
			return false, nil, fmt.Errorf("reading trailers refresh timestamp: %w", err)
		}
		if requestedAt != nil {
			return false, requestedAt, nil
		}
		// The slot was released underneath us; go around and try to take it.
	}
	// Both attempts lost the gate and both read a freed slot. Rather than
	// report a cooldown we cannot date, treat it as contention lost to whoever
	// is claiming and releasing in a tight loop: no timestamp, no claim.
	return false, nil, nil
}

// ReleaseTrailersRefreshClaim hands an item's trailer-refresh cooldown slot
// back after the refresh it started failed, so the viewer can retry instead of
// waiting out the whole window for work that never happened.
//
// claimedAt is the timestamp TryClaimTrailersRefresh wrote, and the equality
// guard is what makes this safe to run from a detached goroutine: if the window
// has since lapsed and another request claimed the slot, this UPDATE matches no
// row and the newer claim survives untouched. Zero rows affected is therefore a
// normal outcome, not an error.
func (r *ItemRepository) ReleaseTrailersRefreshClaim(ctx context.Context, contentID string, claimedAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE media_items
		SET trailers_refresh_requested_at = NULL
		WHERE content_id = $1
		  AND trailers_refresh_requested_at = $2`,
		contentID, claimedAt,
	)
	if err != nil {
		return fmt.Errorf("releasing trailers refresh claim: %w", err)
	}
	return nil
}

// TrailersRefreshRequestedAt reads the timestamp of the trailer-refresh
// cooldown claim currently held for an item, or nil when the slot is free.
//
// The durable recovery path needs it: when the process that consumed a slot
// dies mid-refresh, the refresh-debt queue re-runs the work in a worker that
// never saw the claim and so has no timestamp to release it on. Reading the
// claim before the refresh starts gives that worker the same exact key the
// original request had, so ReleaseTrailersRefreshClaim's equality guard keeps
// working: a slot re-claimed in the meantime is left alone.
func (r *ItemRepository) TrailersRefreshRequestedAt(ctx context.Context, contentID string) (*time.Time, error) {
	var requestedAt *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT trailers_refresh_requested_at
		FROM media_items
		WHERE content_id = $1`,
		contentID,
	).Scan(&requestedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrItemNotFound
		}
		return nil, fmt.Errorf("reading trailers refresh timestamp: %w", err)
	}
	return requestedAt, nil
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
	// SeasonNumberingSource is the provider whose season and episode numbers
	// the item follows ("tvdb", "tmdb"), or empty when unknown.
	SeasonNumberingSource string
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
			       CASE r.provider WHEN 'tmdb' THEN 0 WHEN 'tvdb' THEN 1 WHEN 'imdb' THEN 2 ELSE 3 END AS provider_rank,
			       COALESCE(mi.tmdb_id, '') AS item_tmdb_id, COALESCE(mi.tvdb_id, '') AS item_tvdb_id,
			       COALESCE(mi.imdb_id, '') AS item_imdb_id
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
			  AND EXISTS (
				SELECT 1 FROM media_files mf_playable
				WHERE (mf_playable.content_id = mi.content_id
				       OR EXISTS (SELECT 1 FROM episodes ep WHERE ep.content_id = mf_playable.episode_id AND ep.series_id = mi.content_id))
				  AND mf_playable.media_folder_id = mil.media_folder_id
				  AND mf_playable.missing_since IS NULL
				  AND (mi.type <> 'series' OR mf_playable.episode_id IS NOT NULL)
			  )
		),
		provider_matches AS (
			SELECT r.query_tmdb_id, mi.content_id, r.provider, mil.media_folder_id::text, mi.title, r.ord,
			       CASE r.provider WHEN 'tmdb' THEN 0 WHEN 'tvdb' THEN 1 WHEN 'imdb' THEN 2 ELSE 3 END AS provider_rank,
			       COALESCE(mi.tmdb_id, '') AS item_tmdb_id, COALESCE(mi.tvdb_id, '') AS item_tvdb_id,
			       COALESCE(mi.imdb_id, '') AS item_imdb_id
			FROM requested r
			JOIN media_item_provider_ids mip
			  ON mip.provider = r.provider
			 AND mip.provider_id = r.provider_id
			 AND mip.item_type = $5
			JOIN media_items mi ON mi.content_id = mip.content_id AND mi.type = $5
			JOIN media_item_libraries mil ON mil.content_id = mi.content_id
			JOIN media_folders mf ON mf.id = mil.media_folder_id
			WHERE mf.enabled = true
			  AND EXISTS (
				SELECT 1 FROM media_files mf_playable
				WHERE (mf_playable.content_id = mi.content_id
				       OR EXISTS (SELECT 1 FROM episodes ep WHERE ep.content_id = mf_playable.episode_id AND ep.series_id = mi.content_id))
				  AND mf_playable.media_folder_id = mil.media_folder_id
				  AND mf_playable.missing_since IS NULL
				  AND (mi.type <> 'series' OR mf_playable.episode_id IS NOT NULL)
			  )
		)
		-- Every match, best first per query_tmdb_id. LookupExternalIDs keeps
		-- the first one that names the requested title.
		SELECT query_tmdb_id, content_id, provider, media_folder_id, title, ord,
		       item_tmdb_id, item_tvdb_id, item_imdb_id
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
	matched := make(map[string]bool)
	for rows.Next() {
		var row ExternalIDMatchRow
		var ord int32
		var itemTMDB, itemTVDB, itemIMDb string
		if err := rows.Scan(&row.QueryTMDBID, &row.MediaID, &row.MatchedProvider, &row.LibraryID, &row.Title,
			&ord, &itemTMDB, &itemTVDB, &itemIMDb); err != nil {
			return nil, fmt.Errorf("scanning external id lookup row: %w", err)
		}
		if matched[row.QueryTMDBID] {
			continue
		}
		// A TVDB or IMDb match stands in for the requested TMDB ID only when
		// the item is not another title by the type's anchor precedence: an
		// item holding a different TMDB ID is another movie, while a series
		// sharing the TVDB ID is the same show whatever its TMDB ID.
		if row.MatchedProvider != contentid.ProviderTMDB {
			candidate := candidates[ord]
			requested := map[string]string{
				contentid.ProviderTMDB: candidate.TMDBID,
				contentid.ProviderTVDB: candidate.TVDBID,
				contentid.ProviderIMDB: candidate.IMDbID,
			}
			item := map[string]string{
				contentid.ProviderTMDB: itemTMDB,
				contentid.ProviderTVDB: itemTVDB,
				contentid.ProviderIMDB: itemIMDb,
			}
			if IdentityProviderIDsContradict(mediaType, requested, item) {
				continue
			}
		}
		matched[row.QueryTMDBID] = true
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

// LibraryIDsForItem returns the libraries a media item or episode belongs to,
// in ID order. It reads the same memberships as GetItemsInLibrary, so every
// library it lists is one where a library-scoped detail page of contentID
// resolves.
func (r *ItemRepository) LibraryIDsForItem(ctx context.Context, contentID string) ([]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT media_folder_id FROM media_item_libraries WHERE content_id = $1
		UNION
		SELECT media_folder_id FROM episode_libraries WHERE episode_id = $1
		ORDER BY media_folder_id`, contentID)
	if err != nil {
		return nil, fmt.Errorf("listing item libraries: %w", err)
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning item library: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
