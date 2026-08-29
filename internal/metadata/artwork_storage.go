package metadata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

const (
	artworkInventoryCheckpointVersion = 1
	artworkInventoryBatchSize         = 50
	artworkManifestReadLimit          = 2 * 1024 * 1024
)

type ArtworkInventoryStore interface {
	Open(ctx context.Context, key string) (*artworkstore.Object, error)
	Stat(ctx context.Context, key string) (artworkstore.ObjectInfo, error)
	Probe(ctx context.Context) error
	ListPage(ctx context.Context, prefix, cursor string, limit int) ([]artworkstore.ObjectInfo, string, bool, error)
}

type ArtworkStorageService struct {
	pool                 *pgxpool.Pool
	store                ArtworkInventoryStore
	backend              string
	generation           func() string
	limiter              *rate.Limiter
	untrackedUserArtwork bool
	rebuilder            *artworkstore.Handle
}

// SetRebuilder enables the explicit local-store rebuild admin action. The
// inventory service owns the durable recovery-state update that follows the
// storage handle's atomic pin/generation replacement.
func (s *ArtworkStorageService) SetRebuilder(handle *artworkstore.Handle) {
	if s != nil {
		s.rebuilder = handle
	}
}

// RebuildEmpty recreates an empty local store, persists the recovery
// generation, and returns the immediately observable admin state.
//
// The recovery intent is written *before* the store handle rotates its marker
// and pin. The rotation is durable the moment it lands, so persisting the
// intent afterwards leaves a crash window in which the store is empty on a new
// generation while the accounting row still says healthy — a state in which
// nothing ever re-enters bulk recovery. Recording the intent first turns that
// window into the harmless opposite: an empty_rebuilding row whose generation
// has not caught up yet, which RunRecovery treats as a rebuild to resume.
//
// The write runs inside the handle's rebuild, after the request is validated:
// a rejected rebuild (S3 backend, non-empty root) must return without durable
// side effects, or RunRecovery would later adopt the stranded intent and force
// a healthy populated store into bulk recovery.
func (s *ArtworkStorageService) RebuildEmpty(ctx context.Context) (ArtworkStorageAccounting, error) {
	if s == nil || s.pool == nil || s.rebuilder == nil {
		return ArtworkStorageAccounting{}, artworkstore.ErrRebuildUnsupported
	}
	recordIntent := func(ctx context.Context) error {
		if _, err := s.pool.Exec(ctx, `UPDATE artwork_storage_accounting_state SET
			store_health = 'empty_rebuilding', health_changed_at = NOW(), rebuild_generation = $1,
			rebuild_surface_name = '', rebuild_enqueued_at = NULL WHERE singleton`, s.rebuilder.GenerationID()); err != nil {
			return fmt.Errorf("record artwork rebuild intent: %w", err)
		}
		return nil
	}
	if err := s.rebuilder.RebuildEmpty(ctx, recordIntent); err != nil {
		return ArtworkStorageAccounting{}, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE artwork_storage_accounting_state SET
		store_health = 'empty_rebuilding', rebuild_generation = $1,
		rebuild_surface_name = '', rebuild_enqueued_at = NULL WHERE singleton`, s.rebuilder.GenerationID()); err != nil {
		return ArtworkStorageAccounting{}, fmt.Errorf("persist artwork rebuild state: %w", err)
	}
	return s.Accounting(ctx)
}

func NewArtworkStorageService(pool *pgxpool.Pool, store ArtworkInventoryStore, backend string, generation func() string, untrackedUserArtwork ...bool) *ArtworkStorageService {
	if pool == nil || store == nil {
		return nil
	}
	service := &ArtworkStorageService{
		pool:       pool,
		store:      store,
		backend:    strings.TrimSpace(backend),
		generation: generation,
	}
	if len(untrackedUserArtwork) > 0 {
		service.untrackedUserArtwork = untrackedUserArtwork[0]
	}
	if strings.EqualFold(strings.TrimSpace(backend), artworkstore.BackendS3) {
		// Inventory reconciliation is maintenance work, not request-path work.
		// Keep it below ordinary delivery/materialization traffic on remote
		// stores and make cancellation observable through Wait.
		service.limiter = rate.NewLimiter(rate.Limit(20), 5)
	}
	return service
}

func (s *ArtworkStorageService) storeGeneration() string {
	if s == nil || s.generation == nil {
		return strings.TrimSpace(s.backend) + ":"
	}
	return strings.TrimSpace(s.backend) + ":" + strings.TrimSpace(s.generation())
}

type ArtworkInventoryCheckpoint struct {
	Version             int    `json:"version"`
	Cursor              string `json:"cursor,omitempty"`
	KnownRevisions      int64  `json:"known_revisions"`
	MissingObjects      int64  `json:"missing_objects"`
	Failures            int64  `json:"failures"`
	StoreCursor         string `json:"store_cursor,omitempty"`
	OrphanObjects       int64  `json:"orphan_objects"`
	IndexBytes          int64  `json:"index_bytes"`
	IndexObjects        int64  `json:"index_objects"`
	BrandingBytes       int64  `json:"branding_bytes"`
	BrandingObjects     int64  `json:"branding_objects"`
	LegacyUploadBytes   int64  `json:"legacy_upload_bytes"`
	LegacyUploadObjects int64  `json:"legacy_upload_objects"`
	ImportCursor        string `json:"import_cursor,omitempty"`
	ImportedSeeds       int64  `json:"imported_seeds"`
	AdoptedSeeds        int64  `json:"adopted_seeds"`
	RetainedSeeds       int64  `json:"retained_unverifiable_seeds"`
	ImportSkipped       int64  `json:"import_skipped"`
	ImportFinished      bool   `json:"import_finished"`
	Finished            bool   `json:"finished"`
}

type ArtworkInventoryRefreshResult struct {
	SnapshotAt       time.Time `json:"snapshot_at"`
	Complete         bool      `json:"complete"`
	KnownRevisions   int64     `json:"known_revisions"`
	MissingObjects   int64     `json:"missing_objects"`
	Failures         int64     `json:"failures"`
	MissingRevisions int64     `json:"missing_revisions"`
	OrphanObjects    int64     `json:"orphan_objects"`
}

type artworkInventoryReference struct {
	path       string
	imageType  string
	sourcePath string
}

func (s *ArtworkStorageService) Refresh(
	ctx context.Context,
	checkpoint *ArtworkInventoryCheckpoint,
	save func(ArtworkInventoryCheckpoint) error,
	progress func(current, total int, message string),
) (ArtworkInventoryRefreshResult, error) {
	if s == nil || s.pool == nil || s.store == nil {
		return ArtworkInventoryRefreshResult{}, fmt.Errorf("artwork storage refresh is not configured")
	}
	if err := s.store.Probe(ctx); err != nil {
		return ArtworkInventoryRefreshResult{}, fmt.Errorf("artwork inventory: probe store: %w", err)
	}
	cp := ArtworkInventoryCheckpoint{Version: artworkInventoryCheckpointVersion}
	if checkpoint != nil && checkpoint.Version == artworkInventoryCheckpointVersion {
		cp = *checkpoint
	}
	if cp.Finished {
		return s.refreshResult(ctx, cp)
	}

	for {
		references, err := s.nextInventoryReferences(ctx, cp.Cursor, artworkInventoryBatchSize)
		if err != nil {
			return ArtworkInventoryRefreshResult{}, err
		}
		if len(references) == 0 {
			break
		}
		for _, reference := range references {
			objects, complete, missing, err := s.inspectRevision(ctx, reference.path, reference.imageType)
			if err != nil {
				cp.Failures++
				cp.Cursor = reference.path
				continue
			}
			cp.MissingObjects += int64(missing)
			if err := s.upsertInventory(ctx, reference, objects, complete); err != nil {
				return ArtworkInventoryRefreshResult{}, err
			}
			cp.KnownRevisions++
			cp.Cursor = reference.path
		}
		if progress != nil {
			progress(int(cp.KnownRevisions), 0, fmt.Sprintf("Verified %d artwork revisions", cp.KnownRevisions))
		}
		if save != nil {
			if err := save(cp); err != nil {
				return ArtworkInventoryRefreshResult{}, fmt.Errorf("artwork inventory: save checkpoint: %w", err)
			}
		}
	}
	if err := s.discoverOrphans(ctx, &cp, save, progress); err != nil {
		return ArtworkInventoryRefreshResult{}, err
	}
	if err := s.adoptReferencedSeeds(ctx); err != nil {
		return ArtworkInventoryRefreshResult{}, err
	}

	cp.Finished = true
	result, err := s.refreshResult(ctx, cp)
	if err != nil {
		return ArtworkInventoryRefreshResult{}, err
	}
	_, err = s.pool.Exec(ctx, artworkAccountingPublishSQL, result.SnapshotAt, result.Complete, result.KnownRevisions, result.MissingRevisions,
		result.MissingObjects, result.OrphanObjects, result.Failures, s.untrackedUserArtwork,
		map[bool]string{true: "user artwork is stored outside PostgreSQL inventory", false: ""}[s.untrackedUserArtwork],
		cp.IndexBytes, cp.IndexObjects, cp.BrandingBytes, cp.BrandingObjects, cp.LegacyUploadBytes, cp.LegacyUploadObjects)
	if err != nil {
		return ArtworkInventoryRefreshResult{}, fmt.Errorf("artwork inventory: publish snapshot: %w", err)
	}
	if save != nil {
		if err := save(cp); err != nil {
			return ArtworkInventoryRefreshResult{}, fmt.Errorf("artwork inventory: save final checkpoint: %w", err)
		}
	}
	if progress != nil {
		progress(int(cp.KnownRevisions), int(cp.KnownRevisions), "Artwork storage accounting refreshed")
	}
	artworkmetrics.Inventory(result.SnapshotAt, result.MissingRevisions, result.MissingObjects, result.OrphanObjects)
	return result, nil
}

func (s *ArtworkStorageService) adoptReferencedSeeds(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates seed
		SET source_class = 'unknown', seed_imported_at = NULL, seed_expires_at = NULL,
			next_attempt_at = NULL, locked_at = NULL, locked_by = '', updated_at = NOW()
		WHERE seed.source_class = 'seed' AND seed.seed_expires_at IS NOT NULL AND seed.tombstoned_at IS NULL
			AND EXISTS (SELECT 1 FROM (`+artworkInventoryReferenceSQL()+`) refs WHERE refs.path = seed.original_path)`)
	if err != nil {
		return fmt.Errorf("artwork inventory: adopt referenced seed: %w", err)
	}
	return nil
}

const artworkAccountingPublishSQL = `
	UPDATE artwork_storage_accounting_state
	SET snapshot_at = $1,
		inventory_complete = $2,
		known_revisions = $3,
		missing_revisions = $4,
		missing_objects = $5,
		orphan_objects = $6,
		failure_count = $7,
		coverage_limited = $8,
		coverage_limit_reason = $9,
		adoption_index_bytes = $10,
		adoption_index_objects = $11,
		branding_bytes = $12,
		branding_objects = $13,
		legacy_upload_bytes = $14,
		legacy_upload_objects = $15,
		last_error = CASE WHEN $7::bigint > 0 THEN 'inventory refresh had failures' ELSE '' END,
		updated_at = NOW()
	WHERE singleton`

// artworkMissingObjectsSQL counts the object slots the registry itself still
// records as absent for the live store generation. A revision is written with
// one zero-sized entry per key the store did not answer for (see
// statArtworkKeys), so this is the same evidence the published object counts
// use, read at finalization instead of accumulated while walking.
const artworkMissingObjectsSQL = `
	SELECT COALESCE(sum((SELECT count(*) FROM unnest(object_sizes_bytes) size WHERE size <= 0)), 0)
	FROM artwork_revision_gc_candidates
	WHERE tombstoned_at IS NULL AND NOT inventory_complete AND store_generation = $1`

func (s *ArtworkStorageService) refreshResult(ctx context.Context, cp ArtworkInventoryCheckpoint) (ArtworkInventoryRefreshResult, error) {
	var liveMissing, lifecycleMissing, missingObjects int64
	query := `SELECT count(*) FROM (` + artworkInventoryReferenceSQL() + `) refs
		LEFT JOIN artwork_revision_gc_candidates inventory ON inventory.original_path = refs.path
		WHERE inventory.id IS NULL OR NOT inventory.inventory_complete OR inventory.store_generation <> $1`
	if err := s.pool.QueryRow(ctx, query, s.storeGeneration()).Scan(&liveMissing); err != nil {
		return ArtworkInventoryRefreshResult{}, fmt.Errorf("artwork inventory: count incomplete references: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM artwork_revision_gc_candidates
		WHERE tombstoned_at IS NULL AND (NOT inventory_complete OR store_generation <> $1)`, s.storeGeneration()).Scan(&lifecycleMissing); err != nil {
		return ArtworkInventoryRefreshResult{}, fmt.Errorf("artwork inventory: count incomplete lifecycle rows: %w", err)
	}
	// Missing objects come from the registry, not from cp.MissingObjects. A
	// resumed refresh restores the pre-interruption count verbatim and then
	// resumes strictly past its cursor, so those revisions are never looked at
	// again: repairs that landed in between would stay invisible and the stale
	// count would hold Complete false until somebody ran a refresh from
	// scratch. Recomputing here is authoritative for both the fresh and the
	// resumed run, and for the already-finished checkpoint replay above.
	//
	// cp.Failures stays an accumulated observation on purpose: a revision this
	// run could not inspect leaves no registry trace (its previous inventory
	// row may still be complete and current), so only the counter records that
	// the pass was incomplete. It is scoped to one refresh run — a new refresh
	// starts from an empty checkpoint — so it cannot outlive the failure.
	if err := s.pool.QueryRow(ctx, artworkMissingObjectsSQL, s.storeGeneration()).Scan(&missingObjects); err != nil {
		return ArtworkInventoryRefreshResult{}, fmt.Errorf("artwork inventory: count missing objects: %w", err)
	}
	return ArtworkInventoryRefreshResult{
		SnapshotAt:       time.Now().UTC(),
		Complete:         cp.Failures == 0 && missingObjects == 0 && liveMissing == 0 && lifecycleMissing == 0,
		KnownRevisions:   cp.KnownRevisions,
		MissingObjects:   missingObjects,
		Failures:         cp.Failures,
		MissingRevisions: liveMissing,
		OrphanObjects:    cp.OrphanObjects,
	}, nil
}

func (s *ArtworkStorageService) discoverOrphans(ctx context.Context, cp *ArtworkInventoryCheckpoint, save func(ArtworkInventoryCheckpoint) error, progress func(int, int, string)) error {
	for {
		if err := s.waitRateLimit(ctx); err != nil {
			return err
		}
		previous := cp.StoreCursor
		objects, next, done, err := s.store.ListPage(ctx, "", previous, 500)
		if err != nil {
			return fmt.Errorf("artwork inventory: list store: %w", err)
		}
		if err := validateArtworkListCursor("artwork inventory: store", previous, next, done); err != nil {
			return err
		}
		if len(objects) > 0 {
			keys := make([]string, 0, len(objects))
			for i := range objects {
				if artworkkey.IsAdoptionIndexKey(objects[i].Key) {
					cp.IndexObjects++
					cp.IndexBytes += objects[i].SizeBytes
					continue
				}
				if artworkkey.IsBrandingKey(objects[i].Key) {
					cp.BrandingObjects++
					cp.BrandingBytes += objects[i].SizeBytes
					continue
				}
				if artworkkey.IsLegacyUploadKey(objects[i].Key) {
					cp.LegacyUploadObjects++
					cp.LegacyUploadBytes += objects[i].SizeBytes
					continue
				}
				if artworkkey.IsStoredArtworkKey(objects[i].Key) {
					keys = append(keys, objects[i].Key)
				}
			}
			var known int64
			if len(keys) > 0 {
				if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT key) FROM artwork_revision_gc_candidates c CROSS JOIN LATERAL unnest(c.object_keys) key WHERE c.tombstoned_at IS NULL AND key = ANY($1)`, keys).Scan(&known); err != nil {
					return err
				}
			}
			cp.OrphanObjects += int64(len(keys)) - known
		}
		cp.StoreCursor = next
		if progress != nil {
			progress(int(cp.KnownRevisions), 0, fmt.Sprintf("Verified inventory; found %d orphan objects", cp.OrphanObjects))
		}
		if save != nil {
			if err := save(*cp); err != nil {
				return err
			}
		}
		if done {
			return nil
		}
	}
}

func validateArtworkListCursor(operation, previous, next string, done bool) error {
	if !done && (next == "" || next == previous) {
		return fmt.Errorf("%s listing did not advance", operation)
	}
	return nil
}

func (s *ArtworkStorageService) nextInventoryReferences(ctx context.Context, cursor string, limit int) ([]artworkInventoryReference, error) {
	rows, err := s.pool.Query(ctx, artworkInventoryEnumerationSQL(), cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("artwork inventory: enumerate references: %w", err)
	}
	defer rows.Close()
	var references []artworkInventoryReference
	for rows.Next() {
		var reference artworkInventoryReference
		if err := rows.Scan(&reference.path, &reference.imageType, &reference.sourcePath); err != nil {
			return nil, fmt.Errorf("artwork inventory: scan reference: %w", err)
		}
		references = append(references, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("artwork inventory: references: %w", err)
	}
	return references, nil
}

func artworkInventoryEnumerationSQL() string {
	return `SELECT path, max(image_type), max(source_path)
		FROM (` + artworkInventoryReferenceSQL() + `
			UNION ALL
			SELECT original_path AS path, image_type, '' AS source_path
			FROM artwork_revision_gc_candidates WHERE tombstoned_at IS NULL
		) refs
		WHERE path > $1
		GROUP BY path
		ORDER BY path
		LIMIT $2`
}

func (s *ArtworkStorageService) inspectRevision(ctx context.Context, originalPath, imageType string) ([]artworkstore.ObjectInfo, bool, int, error) {
	keys := artworkkey.ObjectKeys(originalPath, imageType)
	if info, ok := artworkkey.ParsePortableKey(originalPath); ok {
		if err := s.waitRateLimit(ctx); err != nil {
			return nil, false, 0, err
		}
		object, err := s.store.Open(ctx, info.Directory+"/"+artworkkey.ManifestName)
		if err != nil {
			if errors.Is(err, artworkstore.ErrNotFound) {
				return statArtworkKeys(ctx, s.store, keys, s.limiter)
			}
			return nil, false, 0, fmt.Errorf("artwork inventory: open manifest %s: %w", originalPath, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(object.Body, artworkManifestReadLimit+1))
		closeErr := object.Close()
		if readErr != nil {
			return nil, false, 0, fmt.Errorf("artwork inventory: read manifest %s: %w", originalPath, readErr)
		}
		if closeErr != nil {
			return nil, false, 0, fmt.Errorf("artwork inventory: close manifest %s: %w", originalPath, closeErr)
		}
		if len(data) > artworkManifestReadLimit {
			return nil, false, 0, fmt.Errorf("artwork inventory: manifest %s exceeds limit", originalPath)
		}
		manifest, err := artworkkey.ParseManifest(data)
		if err != nil {
			return nil, false, 0, fmt.Errorf("artwork inventory: parse manifest %s: %w", originalPath, err)
		}
		if manifest.Directory() != info.Directory {
			return nil, false, 0, fmt.Errorf("artwork inventory: manifest directory mismatch for %s", originalPath)
		}
		keys = manifest.ObjectKeys()
	}
	return statArtworkKeys(ctx, s.store, keys, s.limiter)
}

func statArtworkKeys(ctx context.Context, store ArtworkInventoryStore, keys []string, limiter *rate.Limiter) ([]artworkstore.ObjectInfo, bool, int, error) {
	objects := make([]artworkstore.ObjectInfo, 0, len(keys))
	missing := 0
	for _, key := range keys {
		if limiter != nil {
			if err := limiter.Wait(ctx); err != nil {
				return nil, false, missing, err
			}
		}
		info, err := store.Stat(ctx, key)
		if errors.Is(err, artworkstore.ErrNotFound) {
			objects = append(objects, artworkstore.ObjectInfo{Key: key})
			missing++
			continue
		}
		if err != nil {
			return nil, false, missing, fmt.Errorf("artwork inventory: stat %s: %w", key, err)
		}
		info.Key = key
		objects = append(objects, info)
	}
	return objects, len(keys) > 0 && missing == 0, missing, nil
}

func (s *ArtworkStorageService) waitRateLimit(ctx context.Context) error {
	if s == nil || s.limiter == nil {
		return nil
	}
	return s.limiter.Wait(ctx)
}

func (s *ArtworkStorageService) upsertInventory(ctx context.Context, reference artworkInventoryReference, objects []artworkstore.ObjectInfo, complete bool) error {
	keys := make([]string, 0, len(objects))
	sizes := make([]int64, 0, len(objects))
	contentTypes := make([]string, 0, len(objects))
	var total int64
	for _, object := range objects {
		keys = append(keys, object.Key)
		sizes = append(sizes, object.SizeBytes)
		contentTypes = append(contentTypes, object.MediaType)
		total += object.SizeBytes
	}
	imageType := artworkkey.ImageTypeFromKey(reference.path)
	if imageType == "" {
		imageType = reference.imageType
	}
	_, err := s.pool.Exec(ctx, artworkInventoryUpsertSQL, reference.path, imageType, keys, sizes, contentTypes, total,
		artworkSourceClassFromReference(reference.sourcePath), s.storeGeneration(), complete)
	if err != nil {
		return fmt.Errorf("artwork inventory: persist %s: %w", reference.path, err)
	}
	return nil
}

const artworkInventoryUpsertSQL = `
	INSERT INTO artwork_revision_gc_candidates (
		original_path, image_type, object_keys, object_sizes_bytes, object_content_types,
		total_physical_bytes, source_class, store_generation, inventory_complete,
		not_before, next_attempt_at, last_reference_check_at, last_verified_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NULL, NOW(), NOW())
	ON CONFLICT (original_path) DO UPDATE SET
		image_type = EXCLUDED.image_type,
		object_keys = EXCLUDED.object_keys,
		object_sizes_bytes = EXCLUDED.object_sizes_bytes,
		object_content_types = EXCLUDED.object_content_types,
		total_physical_bytes = EXCLUDED.total_physical_bytes,
		source_class = CASE
			WHEN artwork_revision_gc_candidates.seed_imported_at IS NOT NULL
				AND artwork_revision_gc_candidates.seed_expires_at IS NULL THEN 'seed'
			WHEN EXCLUDED.source_class = 'unknown' THEN artwork_revision_gc_candidates.source_class
			ELSE EXCLUDED.source_class
		END,
		store_generation = EXCLUDED.store_generation,
		inventory_complete = EXCLUDED.inventory_complete,
		missing_at = CASE WHEN EXCLUDED.inventory_complete THEN NULL ELSE artwork_revision_gc_candidates.missing_at END,
		repair_state = CASE WHEN EXCLUDED.inventory_complete THEN '' ELSE artwork_revision_gc_candidates.repair_state END,
		repair_queued_at = CASE WHEN EXCLUDED.inventory_complete THEN NULL ELSE artwork_revision_gc_candidates.repair_queued_at END,
		protected_loss_at = CASE WHEN EXCLUDED.inventory_complete THEN NULL ELSE artwork_revision_gc_candidates.protected_loss_at END,
		last_verified_at = NOW()`

func artworkSourceClassFromReference(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	switch {
	case strings.HasPrefix(source, "file://"):
		return "library_sidecar"
	case strings.HasPrefix(source, "embedded://"):
		return "embedded"
	case strings.HasPrefix(source, "generated://"):
		return "generated"
	case strings.HasPrefix(source, "plugin://"):
		return "plugin"
	case strings.Contains(source, "://"):
		return artworkSourceClassProvider
	case strings.HasPrefix(source, "/"):
		return "bundled"
	default:
		return artworkSourceClassUnknown
	}
}

func artworkInventoryReferenceSQL() string {
	parts := make([]string, 0, len(artworkSweepSurfaces())+1)
	for _, surface := range artworkSweepSurfaces() {
		source := "''"
		if surface.sourceCol != "" {
			source = "COALESCE(" + surface.sourceCol + ", '')"
		}
		parts = append(parts, fmt.Sprintf(
			"SELECT %s AS path, '%s' AS image_type, %s AS source_path FROM %s WHERE %s",
			surface.pathCol, surface.imageType, source, surface.table, surface.cachedPredicate(),
		))
	}
	parts = append(parts, fmt.Sprintf(
		"SELECT %s AS path, 'avatar' AS image_type, '' AS source_path FROM %s WHERE %s",
		profileAvatarReferenceSurface().pathExpr,
		profileAvatarReferenceSurface().table,
		profileAvatarReferenceSurface().filter,
	))
	return strings.Join(parts, " UNION ALL ")
}

type ArtworkStorageAccounting struct {
	SnapshotAt           *time.Time                 `json:"snapshot_at,omitempty"`
	Backend              string                     `json:"backend"`
	Complete             bool                       `json:"complete"`
	KnownBytes           int64                      `json:"known_bytes"`
	Total                ArtworkStorageTotal        `json:"total"`
	Libraries            []ArtworkLibraryAccounting `json:"libraries"`
	ServerScoped         ArtworkServerAccounting    `json:"server_scoped"`
	InventoryDrift       ArtworkInventoryDrift      `json:"inventory_drift"`
	UntrackedUserArtwork bool                       `json:"untracked_user_artwork"`
	CoverageLimited      bool                       `json:"coverage_limited"`
	CoverageLimitReason  string                     `json:"coverage_limit_reason,omitempty"`
	FailureCount         int64                      `json:"failure_count"`
	Seed                 ArtworkSeedAccounting      `json:"seed"`
	AdoptionIndexBytes   int64                      `json:"adoption_index_bytes"`
	AdoptionIndexObjects int64                      `json:"adoption_index_objects"`
	BrandingBytes        int64                      `json:"branding_bytes"`
	BrandingObjects      int64                      `json:"branding_objects"`
	LegacyUploadBytes    int64                      `json:"legacy_upload_bytes"`
	LegacyUploadObjects  int64                      `json:"legacy_upload_objects"`
	ResolvedPath         string                     `json:"resolved_path,omitempty"`
	StoreHealth          string                     `json:"store_health"`
	StoreHealthChangedAt *time.Time                 `json:"store_health_changed_at,omitempty"`
	FreeSpaceBytes       *int64                     `json:"free_space_bytes,omitempty"`
	TopologyWarnings     []string                   `json:"unsupported_topology_warnings,omitempty"`
}

type ArtworkStorageTotal struct {
	PhysicalBytes          int64 `json:"physical_bytes"`
	PendingGCBytes         int64 `json:"pending_gc_bytes"`
	MissingBytes           int64 `json:"missing_bytes"`
	RepairPendingBytes     int64 `json:"repair_pending_bytes"`
	ProtectedBytes         int64 `json:"protected_bytes"`
	ReclaimableBytes       int64 `json:"reclaimable_bytes"`
	ObjectCount            int64 `json:"object_count"`
	RevisionCount          int64 `json:"revision_count"`
	MissingRevisionCount   int64 `json:"missing_revision_count"`
	RepairingRevisionCount int64 `json:"repairing_revision_count"`
	ProtectedLossCount     int64 `json:"protected_loss_count"`
}

type ArtworkSeedAccounting struct {
	Bytes                         int64      `json:"bytes"`
	ExpiredBytes                  int64      `json:"expired_bytes"`
	Revisions                     int64      `json:"revisions"`
	RetainedUnverifiableBytes     int64      `json:"retained_unverifiable_bytes"`
	RetainedUnverifiableRevisions int64      `json:"retained_unverifiable_revisions"`
	LastImportAt                  *time.Time `json:"last_import_at,omitempty"`
}

type ArtworkLibraryAccounting struct {
	LibraryID             int64            `json:"library_id"`
	ReferencedBytes       int64            `json:"referenced_bytes"`
	ExclusiveBytes        int64            `json:"exclusive_bytes"`
	SharedBytes           int64            `json:"shared_bytes"`
	ReclaimableBytes      int64            `json:"reclaimable_bytes"`
	ReconstructibleBytes  int64            `json:"reconstructible_bytes"`
	ProtectedBytes        int64            `json:"protected_bytes"`
	ObjectCount           int64            `json:"object_count"`
	RevisionCount         int64            `json:"revision_count"`
	MaterializedRevisions int64            `json:"materialized_revisions"`
	MissingBytes          int64            `json:"missing_bytes"`
	RepairPendingBytes    int64            `json:"repair_pending_bytes"`
	ProtectedLossBytes    int64            `json:"protected_loss_bytes"`
	MissingRevisions      int64            `json:"missing_revisions"`
	RepairingRevisions    int64            `json:"repairing_revisions"`
	ProtectedLosses       int64            `json:"protected_losses"`
	SourceClasses         map[string]int64 `json:"source_classes"`
}

type ArtworkServerAccounting struct {
	ReferencedBytes int64 `json:"referenced_bytes"`
	ObjectCount     int64 `json:"object_count"`
	RevisionCount   int64 `json:"revision_count"`
}

type ArtworkInventoryDrift struct {
	MissingRevisions int64 `json:"missing_revisions"`
	MissingObjects   int64 `json:"missing_objects"`
	OrphanObjects    int64 `json:"orphan_objects"`
}

func (s *ArtworkStorageService) Accounting(ctx context.Context) (ArtworkStorageAccounting, error) {
	if s == nil || s.pool == nil {
		return ArtworkStorageAccounting{}, fmt.Errorf("artwork storage accounting is not configured")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ArtworkStorageAccounting{}, fmt.Errorf("artwork accounting: begin snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result := ArtworkStorageAccounting{
		Backend: s.backend, StoreHealth: string(artworkstore.HealthHealthy), UntrackedUserArtwork: s.untrackedUserArtwork,
		Libraries: make([]ArtworkLibraryAccounting, 0),
	}
	if health, ok := s.store.(interface {
		Health() (artworkstore.HealthState, time.Time)
	}); ok {
		state, changedAt := health.Health()
		result.StoreHealth = string(state)
		if !changedAt.IsZero() {
			changedAt = changedAt.UTC()
			result.StoreHealthChangedAt = &changedAt
		}
	}
	if s.backend == artworkstore.BackendLocal {
		result.TopologyWarnings = []string{"Local artwork storage requires one API node or an identically mounted shared POSIX root on every API node."}
	}
	if rooted, ok := s.store.(interface{ Root() string }); ok {
		result.ResolvedPath = rooted.Root()
	}
	if capacity, ok := s.store.(artworkstore.CapacityProvider); ok {
		if free, err := capacity.FreeSpaceBytes(ctx); err == nil {
			result.FreeSpaceBytes = &free
		} else if !errors.Is(err, artworkstore.ErrNotFound) {
			result.StoreHealth = healthAfterCapacityProbeFailure(result.StoreHealth)
		}
	}
	if err := tx.QueryRow(ctx, artworkAccountingStateSQL).Scan(
		&result.SnapshotAt, &result.Complete, &result.InventoryDrift.MissingRevisions,
		&result.InventoryDrift.MissingObjects, &result.InventoryDrift.OrphanObjects,
		&result.CoverageLimited, &result.CoverageLimitReason, &result.FailureCount,
		&result.AdoptionIndexBytes, &result.AdoptionIndexObjects,
		&result.BrandingBytes, &result.BrandingObjects,
		&result.LegacyUploadBytes, &result.LegacyUploadObjects,
		&result.Seed.LastImportAt,
	); err != nil {
		return result, fmt.Errorf("artwork accounting: load snapshot: %w", err)
	}
	var lifecycleIncomplete bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM artwork_revision_gc_candidates
			WHERE tombstoned_at IS NULL AND (NOT inventory_complete OR store_generation <> $1)
		)`, s.storeGeneration()).Scan(&lifecycleIncomplete); err != nil {
		return result, fmt.Errorf("artwork accounting: check inventory completeness: %w", err)
	}
	if lifecycleIncomplete {
		result.Complete = false
	}
	if err := tx.QueryRow(ctx, artworkStorageTotalSQL()).Scan(
		&result.Total.PhysicalBytes, &result.Total.PendingGCBytes, &result.Total.ProtectedBytes,
		&result.Total.ReclaimableBytes, &result.Total.ObjectCount, &result.Total.RevisionCount,
		&result.Seed.Bytes, &result.Seed.ExpiredBytes, &result.Seed.Revisions,
		&result.Seed.RetainedUnverifiableBytes, &result.Seed.RetainedUnverifiableRevisions,
		&result.Total.MissingBytes, &result.Total.RepairPendingBytes,
		&result.Total.MissingRevisionCount, &result.Total.RepairingRevisionCount, &result.Total.ProtectedLossCount,
	); err != nil {
		return result, fmt.Errorf("artwork accounting: total: %w", err)
	}
	result.Total.PhysicalBytes += result.AdoptionIndexBytes
	result.Total.ObjectCount += result.AdoptionIndexObjects
	result.Total.PhysicalBytes += result.BrandingBytes + result.LegacyUploadBytes
	result.Total.ObjectCount += result.BrandingObjects + result.LegacyUploadObjects
	result.KnownBytes = result.Total.PhysicalBytes
	artworkmetrics.SeedExpiredBytes(result.Seed.ExpiredBytes)
	artworkmetrics.RepairPending(result.Total.RepairingRevisionCount, result.Total.ProtectedLossCount)

	rows, err := tx.Query(ctx, artworkLibraryAccountingSQL())
	if err != nil {
		return result, fmt.Errorf("artwork accounting: libraries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var library ArtworkLibraryAccounting
		if err := rows.Scan(
			&library.LibraryID, &library.ReferencedBytes, &library.ExclusiveBytes, &library.SharedBytes,
			&library.ReclaimableBytes, &library.ReconstructibleBytes, &library.ProtectedBytes,
			&library.ObjectCount, &library.RevisionCount, &library.MaterializedRevisions,
			&library.MissingBytes, &library.RepairPendingBytes, &library.ProtectedLossBytes,
			&library.MissingRevisions, &library.RepairingRevisions, &library.ProtectedLosses,
		); err != nil {
			return result, fmt.Errorf("artwork accounting: scan library: %w", err)
		}
		library.SourceClasses = make(map[string]int64)
		result.Libraries = append(result.Libraries, library)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("artwork accounting: library rows: %w", err)
	}
	rows.Close()

	classRows, err := tx.Query(ctx, artworkLibrarySourceClassSQL())
	if err != nil {
		return result, fmt.Errorf("artwork accounting: source classes: %w", err)
	}
	defer classRows.Close()
	byID := make(map[int64]*ArtworkLibraryAccounting, len(result.Libraries))
	for i := range result.Libraries {
		byID[result.Libraries[i].LibraryID] = &result.Libraries[i]
	}
	for classRows.Next() {
		var libraryID int64
		var sourceClass string
		var bytes int64
		if err := classRows.Scan(&libraryID, &sourceClass, &bytes); err != nil {
			return result, fmt.Errorf("artwork accounting: scan source class: %w", err)
		}
		if library := byID[libraryID]; library != nil {
			library.SourceClasses[sourceClass] = bytes
		}
	}
	if err := classRows.Err(); err != nil {
		return result, fmt.Errorf("artwork accounting: source class rows: %w", err)
	}
	classRows.Close()

	if err := tx.QueryRow(ctx, artworkServerScopedAccountingSQL()).Scan(
		&result.ServerScoped.ReferencedBytes, &result.ServerScoped.ObjectCount, &result.ServerScoped.RevisionCount,
	); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, fmt.Errorf("artwork accounting: server scoped: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("artwork accounting: commit snapshot: %w", err)
	}
	return result, nil
}

func healthAfterCapacityProbeFailure(current string) string {
	switch artworkstore.HealthState(current) {
	case artworkstore.HealthHealthy, artworkstore.HealthDegraded:
		return string(artworkstore.HealthDegraded)
	default:
		return current
	}
}

const artworkAccountingStateSQL = `
	SELECT snapshot_at, inventory_complete, missing_revisions, missing_objects, orphan_objects,
		coverage_limited, coverage_limit_reason, failure_count,
		adoption_index_bytes, adoption_index_objects,
		branding_bytes, branding_objects, legacy_upload_bytes, legacy_upload_objects,
		last_seed_import_at
	FROM artwork_storage_accounting_state WHERE singleton`

func artworkReconstructibleSQL(sourceExpr string) string {
	return fmt.Sprintf(`(
		lower(%[1]s) LIKE 'file://%%'
		OR %[1]s LIKE '/%%'
		OR (coalesce(%[1]s, '') LIKE '%%://%%' AND lower(%[1]s) NOT LIKE ALL (%[2]s))
	)`, sourceExpr, nonProviderImageSchemesSQL)
}

func artworkLibraryReferencesSQL() string {
	reconstructible := func(source string) string { return artworkReconstructibleSQL(source) }
	surface := func(name string) artworkSweepSurface {
		value, ok := artworkSweepSurfaceByName(name)
		if !ok {
			panic("missing artwork sweep surface " + name)
		}
		return value
	}
	return fmt.Sprintf(`
		SELECT mil.media_folder_id AS library_id, mi.poster_path AS path, %s AS reconstructible FROM media_items mi JOIN media_item_libraries mil ON mil.content_id = mi.content_id WHERE %s
		UNION ALL SELECT mil.media_folder_id, mi.backdrop_path, %s FROM media_items mi JOIN media_item_libraries mil ON mil.content_id = mi.content_id WHERE %s
		UNION ALL SELECT mil.media_folder_id, mi.logo_path, %s FROM media_items mi JOIN media_item_libraries mil ON mil.content_id = mi.content_id WHERE %s
		UNION ALL SELECT mil.media_folder_id, loc.poster_path, %s FROM media_item_localizations loc JOIN media_item_libraries mil ON mil.content_id = loc.content_id WHERE %s
		UNION ALL SELECT mil.media_folder_id, loc.backdrop_path, %s FROM media_item_localizations loc JOIN media_item_libraries mil ON mil.content_id = loc.content_id WHERE %s
		UNION ALL SELECT mil.media_folder_id, loc.logo_path, %s FROM media_item_localizations loc JOIN media_item_libraries mil ON mil.content_id = loc.content_id WHERE %s
		UNION ALL SELECT mil.media_folder_id, se.poster_path, %s FROM seasons se JOIN media_item_libraries mil ON mil.content_id = se.series_id WHERE %s
		UNION ALL SELECT mil.media_folder_id, loc.poster_path, %s FROM season_localizations loc JOIN seasons se ON se.content_id = loc.season_content_id JOIN media_item_libraries mil ON mil.content_id = se.series_id WHERE %s
		UNION ALL SELECT mil.media_folder_id, ep.still_path, %s FROM episodes ep JOIN media_item_libraries mil ON mil.content_id = ep.series_id WHERE %s
		UNION ALL SELECT mil.media_folder_id, p.photo_path, %s FROM people p JOIN item_people ip ON ip.person_id = p.id JOIN media_item_libraries mil ON mil.content_id = ip.content_id WHERE %s
		UNION ALL SELECT mf.id, mf.poster_path, FALSE FROM media_folders mf WHERE %s
		UNION ALL SELECT lc.library_id, lc.poster_url, FALSE FROM library_collections lc WHERE %s
		UNION ALL SELECT lc.library_id, lc.backdrop_url, FALSE FROM library_collections lc WHERE %s`,
		reconstructible("mi.poster_source_path"), surface(artworkSurfaceItemPosters).cachedPredicate(),
		reconstructible("mi.backdrop_source_path"), surface(artworkSurfaceItemBackdrops).cachedPredicate(),
		reconstructible("mi.logo_source_path"), surface(artworkSurfaceItemLogos).cachedPredicate(),
		reconstructible("loc.poster_source_path"), strings.ReplaceAll(surface(artworkSurfaceLocalizedItemPosters).cachedPredicate(), "poster_path", "loc.poster_path"),
		reconstructible("loc.backdrop_source_path"), strings.ReplaceAll(surface(artworkSurfaceLocalizedItemBackdrops).cachedPredicate(), "backdrop_path", "loc.backdrop_path"),
		reconstructible("loc.logo_source_path"), strings.ReplaceAll(surface(artworkSurfaceLocalizedItemLogos).cachedPredicate(), "logo_path", "loc.logo_path"),
		reconstructible("se.poster_source_path"), strings.ReplaceAll(surface(artworkSurfaceSeasonPosters).cachedPredicate(), "poster_path", "se.poster_path"),
		reconstructible("loc.poster_source_path"), strings.ReplaceAll(surface(artworkSurfaceLocalizedSeasonPosters).cachedPredicate(), "poster_path", "loc.poster_path"),
		reconstructible("ep.still_source_path"), strings.ReplaceAll(surface(artworkSurfaceEpisodeStills).cachedPredicate(), "still_path", "ep.still_path"),
		reconstructible("p.photo_source_path"), strings.ReplaceAll(surface(artworkSurfacePersonPhotos).cachedPredicate(), "photo_path", "p.photo_path"),
		strings.ReplaceAll(surface(artworkSurfaceLibraryPosters).cachedPredicate(), "poster_path", "mf.poster_path"),
		strings.ReplaceAll(surface(artworkSurfaceCollectionPosters).cachedPredicate(), "poster_url", "lc.poster_url"),
		strings.ReplaceAll(surface(artworkSurfaceCollectionBackdrops).cachedPredicate(), "backdrop_url", "lc.backdrop_url"),
	)
}

func artworkStorageAccountingCTE() string {
	return `WITH raw_refs AS (` + artworkLibraryReferencesSQL() + `),
		library_refs AS (
			SELECT library_id, path, bool_and(reconstructible) AS reconstructible
			FROM raw_refs GROUP BY library_id, path
		), server_refs AS (` + artworkServerReferencesSQL() + `), revision_scope AS (
			SELECT lr.path, count(*) AS library_count,
				EXISTS (SELECT 1 FROM server_refs sr WHERE sr.path = lr.path) AS server_shared
			FROM library_refs lr GROUP BY lr.path
		)`
}

func artworkServerReferencesSQL() string {
	personal, ok := artworkSweepSurfaceByName(artworkSurfaceUserCollectionPosters)
	if !ok {
		panic("missing artwork sweep surface user collection posters")
	}
	avatar := profileAvatarReferenceSurface()
	return fmt.Sprintf(`SELECT %s AS path FROM %s WHERE %s
		UNION ALL SELECT %s AS path FROM %s WHERE %s`,
		personal.pathCol, personal.table, personal.cachedPredicate(),
		avatar.pathExpr, avatar.table, avatar.filter,
	)
}

func artworkStorageTotalSQL() string {
	return `WITH all_refs AS (` + artworkInventoryReferenceSQL() + `), protected_paths AS (
		SELECT path FROM all_refs GROUP BY path
		HAVING NOT bool_and(` + artworkReconstructibleSQL("source_path") + `)
	) SELECT
		COALESCE(sum(total_physical_bytes) FILTER (WHERE lifecycle_state IN ('parked', 'pending_gc', 'deleting')), 0),
		COALESCE(sum(total_physical_bytes) FILTER (WHERE lifecycle_state IN ('pending_gc', 'deleting')), 0),
		COALESCE(sum(total_physical_bytes) FILTER (WHERE original_path IN (SELECT path FROM protected_paths) AND lifecycle_state IN ('parked', 'pending_gc', 'deleting')), 0),
		COALESCE(sum(total_physical_bytes) FILTER (WHERE
			(source_class = 'seed' AND seed_expires_at <= NOW() AND lifecycle_state IN ('parked', 'pending_gc', 'deleting'))
			OR (lifecycle_state IN ('pending_gc', 'deleting') AND original_path NOT IN (SELECT path FROM protected_paths))), 0),
		COALESCE(sum((SELECT count(*) FROM unnest(object_sizes_bytes) size WHERE size > 0)) FILTER (WHERE lifecycle_state IN ('parked', 'pending_gc', 'deleting')), 0),
		count(*) FILTER (WHERE lifecycle_state IN ('parked', 'pending_gc', 'deleting')),
		COALESCE(sum(total_physical_bytes) FILTER (WHERE source_class = 'seed' AND lifecycle_state IN ('parked', 'pending_gc', 'deleting')), 0),
		COALESCE(sum(total_physical_bytes) FILTER (WHERE source_class = 'seed' AND seed_expires_at <= NOW() AND lifecycle_state IN ('parked', 'pending_gc', 'deleting')), 0),
		count(*) FILTER (WHERE source_class = 'seed' AND lifecycle_state IN ('parked', 'pending_gc', 'deleting')),
		COALESCE(sum(total_physical_bytes) FILTER (WHERE source_class = 'seed' AND seed_imported_at IS NOT NULL AND seed_expires_at IS NULL AND lifecycle_state IN ('parked', 'pending_gc', 'deleting')), 0),
		count(*) FILTER (WHERE source_class = 'seed' AND seed_imported_at IS NOT NULL AND seed_expires_at IS NULL AND lifecycle_state IN ('parked', 'pending_gc', 'deleting'))
		, COALESCE(sum(total_physical_bytes) FILTER (WHERE missing_at IS NOT NULL AND lifecycle_state IN ('parked', 'pending_gc', 'deleting')), 0)
		, COALESCE(sum(total_physical_bytes) FILTER (WHERE repair_state IN ('queued', 'repairing') AND lifecycle_state IN ('parked', 'pending_gc', 'deleting')), 0)
		, count(*) FILTER (WHERE missing_at IS NOT NULL AND lifecycle_state IN ('parked', 'pending_gc', 'deleting'))
		, count(*) FILTER (WHERE repair_state IN ('queued', 'repairing') AND lifecycle_state IN ('parked', 'pending_gc', 'deleting'))
		, count(*) FILTER (WHERE protected_loss_at IS NOT NULL AND lifecycle_state IN ('parked', 'pending_gc', 'deleting'))
		FROM artwork_revision_gc_candidates`
}

func artworkLibraryAccountingSQL() string {
	return artworkStorageAccountingCTE() + ` SELECT
		lr.library_id,
		COALESCE(sum(i.total_physical_bytes), 0) AS referenced_bytes,
		COALESCE(sum(i.total_physical_bytes) FILTER (WHERE rs.library_count = 1 AND NOT rs.server_shared), 0) AS exclusive_bytes,
		COALESCE(sum(i.total_physical_bytes) FILTER (WHERE rs.library_count > 1 OR rs.server_shared), 0) AS shared_bytes,
		COALESCE(sum(i.total_physical_bytes) FILTER (WHERE rs.library_count = 1 AND NOT rs.server_shared AND lr.reconstructible), 0) AS reclaimable_bytes,
		COALESCE(sum(i.total_physical_bytes) FILTER (WHERE lr.reconstructible), 0) AS reconstructible_bytes,
		COALESCE(sum(i.total_physical_bytes) FILTER (WHERE NOT lr.reconstructible), 0) AS protected_bytes,
		COALESCE(sum((SELECT count(*) FROM unnest(i.object_sizes_bytes) size WHERE size > 0)), 0) AS object_count,
		count(i.id) AS revision_count,
		count(*) FILTER (WHERE i.inventory_complete) AS materialized_revisions,
		COALESCE(sum(i.total_physical_bytes) FILTER (WHERE i.missing_at IS NOT NULL), 0) AS missing_bytes,
		COALESCE(sum(i.total_physical_bytes) FILTER (WHERE i.repair_state IN ('queued', 'repairing')), 0) AS repair_pending_bytes,
		COALESCE(sum(i.total_physical_bytes) FILTER (WHERE i.protected_loss_at IS NOT NULL), 0) AS protected_loss_bytes,
		count(i.id) FILTER (WHERE i.missing_at IS NOT NULL) AS missing_revisions,
		count(i.id) FILTER (WHERE i.repair_state IN ('queued', 'repairing')) AS repairing_revisions,
		count(i.id) FILTER (WHERE i.protected_loss_at IS NOT NULL) AS protected_losses
	FROM library_refs lr
	JOIN revision_scope rs ON rs.path = lr.path
	LEFT JOIN artwork_revision_gc_candidates i ON i.original_path = lr.path AND i.lifecycle_state IN ('parked', 'pending_gc', 'deleting')
	GROUP BY lr.library_id ORDER BY lr.library_id`
}

func artworkLibrarySourceClassSQL() string {
	return artworkStorageAccountingCTE() + ` SELECT lr.library_id, COALESCE(i.source_class, 'unknown'), COALESCE(sum(i.total_physical_bytes), 0)
		FROM library_refs lr LEFT JOIN artwork_revision_gc_candidates i ON i.original_path = lr.path AND i.lifecycle_state IN ('parked', 'pending_gc', 'deleting')
		GROUP BY lr.library_id, COALESCE(i.source_class, 'unknown') ORDER BY lr.library_id`
}

func artworkServerScopedAccountingSQL() string {
	return `WITH server_paths AS (SELECT DISTINCT path FROM (` + artworkServerReferencesSQL() + `) refs)
	SELECT COALESCE(sum(i.total_physical_bytes), 0), COALESCE(sum((SELECT count(*) FROM unnest(i.object_sizes_bytes) size WHERE size > 0)), 0), count(i.id)
	FROM server_paths sp LEFT JOIN artwork_revision_gc_candidates i ON i.original_path = sp.path AND i.lifecycle_state IN ('parked', 'pending_gc', 'deleting')`
}
