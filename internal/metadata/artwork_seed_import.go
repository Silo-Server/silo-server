package metadata

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

// seedAdoptionGrace leaves copied, unreferenced seeds available for adoption
// before the normal scheduled artwork-revision GC and on-demand purge job may
// reclaim them.
const seedAdoptionGrace = 30 * 24 * time.Hour

type ArtworkSeedImportResult struct {
	ImportedSeeds        int64     `json:"imported_seeds"`
	AdoptedLive          int64     `json:"adopted_live"`
	RetainedUnverifiable int64     `json:"retained_unverifiable"`
	Skipped              int64     `json:"skipped"`
	FinishedAt           time.Time `json:"finished_at"`
}

type seedImportPageCounts struct {
	imported int64
	adopted  int64
	retained int64
	skipped  int64
}

// ImportPortable verifies copied portable revisions and registers them in the
// existing lifecycle inventory. Only manifest keys are candidates: a copied
// directory is intentionally invisible until its completeness marker exists.
func (s *ArtworkStorageService) ImportPortable(
	ctx context.Context,
	checkpoint *ArtworkInventoryCheckpoint,
	save func(ArtworkInventoryCheckpoint) error,
	progress func(current, total int, message string),
) (ArtworkSeedImportResult, error) {
	if s == nil || s.pool == nil || s.store == nil {
		return ArtworkSeedImportResult{}, fmt.Errorf("artwork seed import is not configured")
	}
	cp := ArtworkInventoryCheckpoint{Version: artworkInventoryCheckpointVersion}
	if checkpoint != nil && checkpoint.Version == artworkInventoryCheckpointVersion {
		cp = *checkpoint
	}
	if cp.ImportFinished {
		return seedImportResult(cp), nil
	}
	for {
		if err := s.waitRateLimit(ctx); err != nil {
			return ArtworkSeedImportResult{}, err
		}
		previous := cp.ImportCursor
		objects, next, done, err := s.store.ListPage(ctx, artworkkey.PortableObjectsPrefix+"/", previous, 250)
		if err != nil {
			return ArtworkSeedImportResult{}, fmt.Errorf("artwork seed import: list portable tree: %w", err)
		}
		if err := validateArtworkListCursor("artwork seed import: portable tree", previous, next, done); err != nil {
			return ArtworkSeedImportResult{}, err
		}
		var page seedImportPageCounts
		for _, object := range objects {
			info, ok := artworkkey.ParsePortableKey(object.Key)
			if !ok || !info.IsManifest {
				continue
			}
			manifest, manifestJSON, inventory, originalKey, err := s.verifyPortableRevision(ctx, info.Directory)
			if err != nil {
				page.skipped++
				continue
			}
			live, err := s.artworkPathReferenced(ctx, originalKey)
			if err != nil {
				return ArtworkSeedImportResult{}, err
			}
			retainUnverifiable := s.untrackedUserArtwork && isUntrackedUserArtworkImageType(manifest.ImageType)
			if err := s.registerImportedRevision(ctx, originalKey, manifest.ImageType, inventory, live, retainUnverifiable); err != nil {
				return ArtworkSeedImportResult{}, err
			}
			_ = manifestJSON // retained by verification; inventory records its exact size.
			if live {
				page.adopted++
			} else if retainUnverifiable {
				page.retained++
			} else {
				page.imported++
			}
		}
		commitSeedImportPage(&cp, next, page)
		if progress != nil {
			progress(int(cp.ImportedSeeds+cp.AdoptedSeeds+cp.RetainedSeeds), 0, fmt.Sprintf("Imported %d portable artwork seeds", cp.ImportedSeeds))
		}
		if save != nil {
			if err := save(cp); err != nil {
				return ArtworkSeedImportResult{}, err
			}
		}
		if done {
			break
		}
	}
	cp.ImportFinished = true
	if _, err := s.pool.Exec(ctx, `UPDATE artwork_storage_accounting_state SET last_seed_import_at = NOW(), updated_at = NOW() WHERE singleton`); err != nil {
		return ArtworkSeedImportResult{}, err
	}
	if save != nil {
		if err := save(cp); err != nil {
			return ArtworkSeedImportResult{}, err
		}
	}
	artworkmetrics.Seed("imported", cp.ImportedSeeds)
	artworkmetrics.Seed("adopted", cp.AdoptedSeeds)
	artworkmetrics.Seed("retained_unverifiable", cp.RetainedSeeds)
	artworkmetrics.Seed("skipped", cp.ImportSkipped)
	return seedImportResult(cp), nil
}

func seedImportResult(cp ArtworkInventoryCheckpoint) ArtworkSeedImportResult {
	return ArtworkSeedImportResult{
		ImportedSeeds: cp.ImportedSeeds, AdoptedLive: cp.AdoptedSeeds,
		RetainedUnverifiable: cp.RetainedSeeds, Skipped: cp.ImportSkipped, FinishedAt: time.Now().UTC(),
	}
}

func commitSeedImportPage(cp *ArtworkInventoryCheckpoint, next string, page seedImportPageCounts) {
	cp.ImportedSeeds += page.imported
	cp.AdoptedSeeds += page.adopted
	cp.RetainedSeeds += page.retained
	cp.ImportSkipped += page.skipped
	cp.ImportCursor = next
}

func isUntrackedUserArtworkImageType(imageType string) bool {
	switch strings.ToLower(strings.TrimSpace(imageType)) {
	case artworkkey.ImageTypeAvatar, artworkkey.ImageTypeCollectionPoster, artworkkey.ImageTypeCollectionBackdrop:
		return true
	default:
		return false
	}
}

func (s *ArtworkStorageService) verifyPortableRevision(ctx context.Context, directory string) (artworkkey.Manifest, []byte, []artworkstore.ObjectInfo, string, error) {
	reader := func(ctx context.Context, key string) (io.ReadCloser, error) {
		object, err := s.store.Open(ctx, key)
		if err != nil {
			return nil, err
		}
		return object.Body, nil
	}
	manifest, err := artworkkey.ReadManifest(ctx, directory, reader)
	if err != nil {
		return artworkkey.Manifest{}, nil, nil, "", err
	}
	manifestObject, err := s.store.Open(ctx, directory+"/"+artworkkey.ManifestName)
	if err != nil {
		return artworkkey.Manifest{}, nil, nil, "", err
	}
	manifestJSON, err := io.ReadAll(io.LimitReader(manifestObject.Body, artworkManifestReadLimit+1))
	_ = manifestObject.Close()
	if err != nil || len(manifestJSON) > artworkManifestReadLimit {
		return artworkkey.Manifest{}, nil, nil, "", fmt.Errorf("artwork seed import: invalid manifest bytes")
	}
	objects, complete, _, err := statArtworkKeys(ctx, s.store, manifest.ObjectKeys(), s.limiter)
	if err != nil || !complete {
		return artworkkey.Manifest{}, nil, nil, "", fmt.Errorf("artwork seed import: incomplete revision")
	}
	var originalKey string
	for _, variant := range manifest.Variants {
		if variant.Name == artworkkey.OriginalVariant {
			originalKey = directory + "/" + variant.Filename
			break
		}
	}
	return manifest, manifestJSON, objects, originalKey, nil
}

func (s *ArtworkStorageService) artworkPathReferenced(ctx context.Context, originalPath string) (bool, error) {
	var referenced bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM (`+artworkInventoryReferenceSQL()+`) refs WHERE path = $1)`, originalPath).Scan(&referenced)
	return referenced, err
}

func (s *ArtworkStorageService) registerImportedRevision(ctx context.Context, originalPath, imageType string, objects []artworkstore.ObjectInfo, live, retainUnverifiable bool) error {
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
	expires := time.Now().UTC().Add(seedAdoptionGrace)
	_, err := s.pool.Exec(ctx, artworkSeedImportUpsertSQL,
		originalPath, imageType, keys, sizes, contentTypes, total, live, s.storeGeneration(), expires, retainUnverifiable)
	return err
}

const artworkSeedImportUpsertSQL = `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, image_type, object_keys, object_sizes_bytes, object_content_types,
			total_physical_bytes, source_class, store_generation, inventory_complete,
			not_before, next_attempt_at, seed_imported_at, seed_expires_at, last_verified_at
		) VALUES ($1,$2,$3,$4,$5,$6,CASE WHEN $7 THEN 'unknown' ELSE 'seed' END,$8,TRUE,
			CASE WHEN $7 OR $10 THEN NOW() ELSE $9 END, CASE WHEN $7 OR $10 THEN NULL ELSE $9 END,
			CASE WHEN $7 THEN NULL ELSE NOW() END, CASE WHEN $7 OR $10 THEN NULL ELSE $9 END, NOW())
		ON CONFLICT (original_path) DO UPDATE SET
			image_type = EXCLUDED.image_type, object_keys = EXCLUDED.object_keys,
			object_sizes_bytes = EXCLUDED.object_sizes_bytes, object_content_types = EXCLUDED.object_content_types,
			total_physical_bytes = EXCLUDED.total_physical_bytes, store_generation = EXCLUDED.store_generation,
			inventory_complete = TRUE, last_verified_at = NOW(),
			source_class = CASE
				WHEN artwork_revision_gc_candidates.seed_imported_at IS NOT NULL
					AND artwork_revision_gc_candidates.seed_expires_at IS NULL THEN 'seed'
				WHEN $7 AND artwork_revision_gc_candidates.source_class = 'seed' THEN 'unknown'
				WHEN artwork_revision_gc_candidates.source_class = 'seed' THEN 'seed'
				ELSE artwork_revision_gc_candidates.source_class END,
			seed_imported_at = CASE
				WHEN artwork_revision_gc_candidates.seed_imported_at IS NOT NULL
					AND artwork_revision_gc_candidates.seed_expires_at IS NULL
					THEN artwork_revision_gc_candidates.seed_imported_at
				WHEN $7 THEN NULL ELSE COALESCE(artwork_revision_gc_candidates.seed_imported_at, NOW()) END,
			seed_expires_at = CASE WHEN $7 OR $10 THEN NULL ELSE COALESCE(artwork_revision_gc_candidates.seed_expires_at, $9) END,
			deleted_at = NULL, deletion_started_at = NULL, tombstoned_at = NULL,
			not_before = CASE WHEN $7 OR $10 THEN GREATEST(artwork_revision_gc_candidates.not_before, NOW()) ELSE COALESCE(artwork_revision_gc_candidates.seed_expires_at, $9) END,
			next_attempt_at = CASE WHEN $7 OR $10 THEN NULL ELSE COALESCE(artwork_revision_gc_candidates.seed_expires_at, $9) END,
			attempt_count = 0, locked_at = NULL, locked_by = '', last_error = '', updated_at = NOW()`
