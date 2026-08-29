package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworksource"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
)

const (
	// Version 2 stopped folding the revalidated fallback/protected flags into
	// the plan fingerprint, so a version-1 checkpoint is replanned instead of
	// failing the drift check it can no longer satisfy.
	artworkPurgeCheckpointVersion = 2
	artworkPurgeBatchSize         = 200
	// artworkPurgeSourceProbeTimeout bounds one validation fetch. Purge is an
	// admin job, so the probes run sequentially; only the per-fetch wait needs
	// a ceiling.
	artworkPurgeSourceProbeTimeout = 30 * time.Second
)

// artworkPurgeSourceResolver turns a provider- or plugin-scheme source path
// into a fetchable URL. It is the same resolver the delivery and
// materialization paths use; without one, such a source cannot be proved
// reachable and its target stays protected.
type artworkPurgeSourceResolver interface {
	ResolveImageURL(ctx context.Context, path string, variant string) string
}

type ArtworkPurgeExecutor struct {
	pool       *pgxpool.Pool
	direct     *DirectLibraryArtworkResolver
	accounting *ArtworkStorageService
	sources    artworkPurgeSourceResolver
	// fetchSource is the verified source fetch, injectable for tests. Nil uses
	// the SSRF-guarded, size-limited, image-validating fetch shared with
	// materialization and resilient delivery.
	fetchSource func(ctx context.Context, rawURL string) error
}

func NewArtworkPurgeExecutor(
	pool *pgxpool.Pool,
	direct *DirectLibraryArtworkResolver,
	accounting *ArtworkStorageService,
) *ArtworkPurgeExecutor {
	if pool == nil || direct == nil || accounting == nil {
		return nil
	}
	return &ArtworkPurgeExecutor{pool: pool, direct: direct, accounting: accounting}
}

// SetSourceResolver wires the plugin/provider image-URL resolver. Purge needs
// it to validate non-HTTP provider sources before it makes the stored revision
// collectible; targets whose scheme needs resolution stay protected while it is
// absent.
func (e *ArtworkPurgeExecutor) SetSourceResolver(sources artworkPurgeSourceResolver) {
	if e != nil {
		e.sources = sources
	}
}

type artworkPurgeTarget struct {
	surfaceName string
	keys        []string
	path        string
	source      string
	fallback    string
	shared      bool
	protected   bool
	bytes       int64
}

func (e *ArtworkPurgeExecutor) Execute(
	ctx context.Context,
	req ArtworkPurgeRequest,
	checkpoint *ArtworkPurgeCheckpoint,
	save func(ArtworkPurgeCheckpoint) error,
	progress func(current, total int, message string),
) (*ArtworkPurgeResult, error) {
	if e == nil || e.pool == nil {
		return nil, fmt.Errorf("artwork purge is not configured")
	}
	if err := (&req).Validate(); err != nil {
		return nil, err
	}
	result := &ArtworkPurgeResult{DryRun: req.DryRun}
	result.UntrackedUserArtworkProtected = req.Scope.Server && e.accounting.untrackedUserArtwork
	checkpointResult := func(cp ArtworkPurgeCheckpoint) *ArtworkPurgeResult {
		completed := purgeResultFromCheckpoint(req, cp)
		completed.UntrackedUserArtworkProtected = result.UntrackedUserArtworkProtected
		return completed
	}
	switch req.Mode {
	case ArtworkPurgeModeEdgeOnly:
		return result, nil
	case ArtworkPurgeModeSafeMaterialized:
		// Continue below.
	default:
		return nil, fmt.Errorf("artwork purge: unsupported mode %q", req.Mode)
	}
	cp := ArtworkPurgeCheckpoint{Version: artworkPurgeCheckpointVersion}
	if checkpoint != nil && checkpoint.Version == artworkPurgeCheckpointVersion {
		cp = *checkpoint
	}
	if cp.Finished {
		return checkpointResult(cp), nil
	}
	var targets []artworkPurgeTarget
	if len(cp.Targets) == 0 {
		tx, err := e.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return nil, err
		}
		targets, err = e.plan(ctx, tx, req)
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if err != nil {
			return nil, err
		}
		if err := e.revalidateTargets(ctx, targets); err != nil {
			return nil, err
		}
		metrics := calculatePurgePlanMetrics(targets)
		cp.PlanFingerprint = purgePlanFingerprint(req, targets)
		cp.Targets = checkpointTargets(targets)
		cp.SharedRetained, cp.ProtectedSkipped = metrics.sharedRevisions, metrics.protectedRevisions
		cp.PendingBytes, cp.ReclaimableBytes = metrics.pendingBytes, metrics.reclaimableBytes
		if req.DryRun {
			cp.Transitioned, cp.QueuedRevisions = metrics.transitionedReferences, metrics.queuedRevisions
			cp.Finished = true
			cp.Phase = "dry_run_complete"
		}
		if save != nil {
			if err := save(cp); err != nil {
				return nil, err
			}
		}
		if req.DryRun {
			return checkpointResult(cp), nil
		}
	} else {
		var err error
		targets, err = e.resumeTargets(ctx, req, &cp)
		if err != nil {
			return nil, err
		}
	}
	queued := make(map[string]struct{}, len(cp.QueuedPaths))
	for _, path := range cp.QueuedPaths {
		queued[path] = struct{}{}
	}
	for start := cp.BatchIndex; start < len(targets); start += artworkPurgeBatchSize {
		end := min(start+artworkPurgeBatchSize, len(targets))
		tx, err := e.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		for _, target := range targets[start:end] {
			if target.shared || target.protected {
				continue
			}
			changed, err := updatePurgeTarget(ctx, tx, target)
			if err != nil {
				_ = tx.Rollback(ctx)
				return nil, err
			}
			if !changed {
				changed, err = purgeTargetAtFallback(ctx, tx, target)
				if err != nil {
					_ = tx.Rollback(ctx)
					return nil, err
				}
			}
			if changed {
				cp.Transitioned++
				queued[target.path] = struct{}{}
			} else {
				cp.DriftedReferences++
			}
		}
		paths := make([]string, 0, len(queued))
		for path := range queued {
			paths = append(paths, path)
		}
		if len(paths) > 0 {
			_, err = tx.Exec(ctx, `UPDATE artwork_revision_gc_candidates SET total_physical_bytes = GREATEST(total_physical_bytes, sizes.bytes), updated_at = NOW() FROM (SELECT unnest($1::text[]) path, unnest($2::bigint[]) bytes) sizes WHERE original_path = sizes.path`, paths, purgePathBytes(paths, targets))
		}
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if err != nil {
			return nil, err
		}
		cp.BatchIndex = end
		cp.QueuedPaths = paths
		cp.QueuedRevisions = int64(len(paths))
		cp.Phase = "transitioning"
		if save != nil {
			if err := save(cp); err != nil {
				return nil, err
			}
		}
		if progress != nil {
			progress(end, len(targets), fmt.Sprintf("Transitioned artwork %d/%d", end, len(targets)))
		}
	}
	deadline := time.Now().UTC().Add(24 * time.Hour)
	cp.GraceDeadline = &deadline
	cp.Finished = true
	cp.Phase = "complete"
	if save != nil {
		if err := save(cp); err != nil {
			return nil, err
		}
	}
	completed := checkpointResult(cp)
	return completed, nil
}

func (e *ArtworkPurgeExecutor) plan(ctx context.Context, tx pgx.Tx, req ArtworkPurgeRequest) ([]artworkPurgeTarget, error) {
	var targets []artworkPurgeTarget
	for _, surface := range artworkSweepSurfaces() {
		rows, err := loadPurgeSurface(ctx, tx, surface, req.Scope)
		if err != nil {
			return nil, err
		}
		targets = append(targets, rows...)
	}
	if req.Scope.Server {
		rows, err := tx.Query(ctx, `
			SELECT user_id::text, id::text, substr(avatar, 8)
			FROM user_profiles
			WHERE avatar LIKE 'upload:%'
			ORDER BY user_id, id`)
		if err != nil {
			return nil, fmt.Errorf("artwork purge: plan profile avatars: %w", err)
		}
		for rows.Next() {
			var userID, profileID, path string
			if err := rows.Scan(&userID, &profileID, &path); err != nil {
				rows.Close()
				return nil, fmt.Errorf("artwork purge: scan profile avatar: %w", err)
			}
			targets = append(targets, artworkPurgeTarget{
				surfaceName: "profile avatars",
				keys:        []string{userID, profileID},
				path:        path,
				protected:   true,
			})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("artwork purge: profile avatar rows: %w", err)
		}
		rows.Close()
	}
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.path)
	}
	bytesByPath, err := inventoryBytesForPaths(ctx, tx, paths)
	if err != nil {
		return nil, err
	}
	shared := map[string]bool{}
	if req.Scope.LibraryID != nil && len(paths) > 0 {
		shared, err = sharedArtworkPaths(ctx, tx, int64(*req.Scope.LibraryID), paths)
		if err != nil {
			return nil, err
		}
	}
	for i := range targets {
		targets[i].bytes = bytesByPath[targets[i].path]
		targets[i].shared = shared[targets[i].path]
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].surfaceName != targets[j].surfaceName {
			return targets[i].surfaceName < targets[j].surfaceName
		}
		return strings.Join(targets[i].keys, "\x00") < strings.Join(targets[j].keys, "\x00")
	})
	return targets, nil
}

// revalidateTargets re-proves every fallback a target would be transitioned
// to. Purge points the catalog at the fallback and lets the stored revision
// become unreferenced, so revision GC collects it: a fallback that is not
// actually retrievable turns a reclaim into permanent loss. A file:// sidecar
// is read, and a remote source is fetched through the same verified fetch
// materialization and resilient delivery use — a scheme check alone would
// accept a dead provider URL. Anything that cannot be proved is marked
// protected, which keeps the reference and the bytes exactly where they are.
func (e *ArtworkPurgeExecutor) revalidateTargets(ctx context.Context, targets []artworkPurgeTarget) error {
	// Targets commonly share a source (localizations of one item, versions of
	// one file). One verdict per distinct source keeps a large plan from
	// re-fetching the same bytes hundreds of times.
	verdicts := make(map[string]error)
	for i := range targets {
		target := &targets[i]
		if target.shared {
			continue
		}
		// Verdicts are recomputed, never merged: a resumed target carries the
		// fallback recorded at planning time, and keeping it alongside a fresh
		// protected verdict would leave a dead reference in the checkpoint.
		target.fallback, target.protected = "", false
		source := strings.TrimSpace(target.source)
		switch {
		case strings.HasPrefix(strings.ToLower(source), "file://"):
			artwork, err := e.direct.ReadSource(ctx, target.surfaceName, target.keys, source)
			if err != nil {
				target.protected = true
				continue
			}
			reference, err := artworkurl.EncodeLibraryReference(artworkurl.LibraryIdentity{Surface: target.surfaceName, Keys: target.keys, Fingerprint: artwork.Fingerprint})
			if err != nil {
				return err
			}
			target.fallback = reference
		case reconstructibleRemoteArtworkSource(source):
			verdict, seen := verdicts[source]
			if !seen {
				verdict = e.probeRemoteArtworkSource(ctx, source)
				verdicts[source] = verdict
			}
			if verdict != nil {
				if !seen {
					slog.WarnContext(ctx, "artwork purge: source is not retrievable; keeping the stored revision",
						"surface", target.surfaceName, "source", source, "error", verdict)
				}
				target.protected = true
				continue
			}
			target.fallback = source
		case strings.HasPrefix(source, "/"):
			// App-relative bundled asset served by the frontend: there is no
			// object to fetch and nothing that can rot behind it.
			target.fallback = source
		default:
			target.protected = true
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// probeRemoteArtworkSource proves one remote source still yields a usable
// image. Provider- and plugin-scheme sources are resolved to a URL first, the
// same way materialization and fallback delivery resolve them.
func (e *ArtworkPurgeExecutor) probeRemoteArtworkSource(ctx context.Context, source string) error {
	resolved := source
	lower := strings.ToLower(source)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		if e.sources == nil {
			return errors.New("no image resolver is configured for source scheme")
		}
		resolved = strings.TrimSpace(e.sources.ResolveImageURL(ctx, source, "original"))
		if resolved == "" {
			return errors.New("source resolved to an empty URL")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, artworkPurgeSourceProbeTimeout)
	defer cancel()
	if e.fetchSource != nil {
		return e.fetchSource(ctx, resolved)
	}
	_, err := artworksource.FetchVerified(ctx, resolved)
	return err
}

// resumeTargets restores a checkpoint's plan and re-proves the part of it that
// has not been applied yet. A sidecar can be deleted or a provider URL can die
// between the checkpoint and the resume, so the remaining transitions are
// gated on a fresh verdict rather than on the one recorded at planning time.
func (e *ArtworkPurgeExecutor) resumeTargets(ctx context.Context, req ArtworkPurgeRequest, cp *ArtworkPurgeCheckpoint) ([]artworkPurgeTarget, error) {
	targets := restoreCheckpointTargets(cp.Targets)
	if got := purgePlanFingerprint(req, targets); got != cp.PlanFingerprint {
		return nil, fmt.Errorf("artwork purge: checkpoint plan fingerprint mismatch")
	}
	start := min(max(cp.BatchIndex, 0), len(targets))
	if err := e.revalidateTargets(ctx, targets[start:]); err != nil {
		return nil, err
	}
	metrics := calculatePurgePlanMetrics(targets)
	cp.Targets = checkpointTargets(targets)
	cp.SharedRetained, cp.ProtectedSkipped = metrics.sharedRevisions, metrics.protectedRevisions
	cp.PendingBytes, cp.ReclaimableBytes = metrics.pendingBytes, metrics.reclaimableBytes
	return targets, nil
}

func checkpointTargets(targets []artworkPurgeTarget) []ArtworkPurgeCheckpointTarget {
	result := make([]ArtworkPurgeCheckpointTarget, len(targets))
	for i, target := range targets {
		result[i] = ArtworkPurgeCheckpointTarget{SurfaceName: target.surfaceName, Keys: append([]string(nil), target.keys...), Path: target.path, Source: target.source, Fallback: target.fallback, Shared: target.shared, Protected: target.protected, Bytes: target.bytes}
	}
	return result
}

func restoreCheckpointTargets(targets []ArtworkPurgeCheckpointTarget) []artworkPurgeTarget {
	result := make([]artworkPurgeTarget, len(targets))
	for i, target := range targets {
		result[i] = artworkPurgeTarget{surfaceName: target.SurfaceName, keys: append([]string(nil), target.Keys...), path: target.Path, source: target.Source, fallback: target.Fallback, shared: target.Shared, protected: target.Protected, bytes: target.Bytes}
	}
	return result
}

func purgeResultFromCheckpoint(req ArtworkPurgeRequest, cp ArtworkPurgeCheckpoint) *ArtworkPurgeResult {
	return &ArtworkPurgeResult{DryRun: req.DryRun, Transitioned: cp.Transitioned, QueuedRevisions: cp.QueuedRevisions, PendingBytes: cp.PendingBytes, ReclaimableBytes: cp.ReclaimableBytes, SharedRetained: cp.SharedRetained, ProtectedSkipped: cp.ProtectedSkipped, DriftedReferences: cp.DriftedReferences, GraceDeadline: cp.GraceDeadline}
}

func purgePathBytes(paths []string, targets []artworkPurgeTarget) []int64 {
	byPath := map[string]int64{}
	for _, target := range targets {
		byPath[target.path] = target.bytes
	}
	result := make([]int64, len(paths))
	for i, path := range paths {
		result[i] = byPath[path]
	}
	return result
}

func reconstructibleRemoteArtworkSource(source string) bool {
	lower := strings.ToLower(strings.TrimSpace(source))
	if !strings.Contains(lower, "://") {
		return false
	}
	for _, scheme := range nonReconstructibleArtworkSchemes {
		if strings.HasPrefix(lower, scheme+"://") {
			return false
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(source))
	return err == nil && parsed.Scheme != "" && (parsed.Host != "" || parsed.Opaque != "" || parsed.Path != "")
}

func loadPurgeSurface(ctx context.Context, tx pgx.Tx, surface artworkSweepSurface, scope ArtworkPurgeScope) ([]artworkPurgeTarget, error) {
	alias := "target"
	keys := surface.keySelectExpressions()
	for i := range keys {
		keys[i] = strings.Replace(keys[i], surface.keyCols[i].column, alias+"."+surface.keyCols[i].column, 1)
	}
	source := "''"
	if surface.sourceCol != "" {
		source = "COALESCE(" + alias + "." + surface.sourceCol + ", '')"
	}
	where := strings.ReplaceAll(surface.cachedPredicate(), surface.pathCol, alias+"."+surface.pathCol)
	args := []any{}
	if !scope.Server {
		args = append(args, *scope.LibraryID)
		ownership, err := purgeSurfaceOwnershipPredicate(surface.name, alias)
		if err != nil {
			return nil, err
		}
		where += " AND " + ownership
	}
	query := fmt.Sprintf(`SELECT %s, COALESCE(%s.%s, ''), %s FROM %s %s WHERE %s ORDER BY %s`,
		strings.Join(keys, ", "), alias, surface.pathCol, source, surface.table, alias, where, strings.Join(keys, ", "))
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("artwork purge: plan %s: %w", surface.name, err)
	}
	defer rows.Close()
	var targets []artworkPurgeTarget
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("artwork purge: scan %s: %w", surface.name, err)
		}
		keyValues := make([]string, len(surface.keyCols))
		for i := range keyValues {
			keyValues[i] = fmt.Sprint(values[i])
		}
		targets = append(targets, artworkPurgeTarget{
			surfaceName: surface.name,
			keys:        keyValues,
			path:        fmt.Sprint(values[len(keyValues)]),
			source:      fmt.Sprint(values[len(keyValues)+1]),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("artwork purge: %s rows: %w", surface.name, err)
	}
	return targets, nil
}

func purgeSurfaceOwnershipPredicate(name, alias string) (string, error) {
	switch name {
	case artworkSurfaceItemPosters, artworkSurfaceItemBackdrops, artworkSurfaceItemLogos, artworkSurfaceLocalizedItemPosters, artworkSurfaceLocalizedItemBackdrops, artworkSurfaceLocalizedItemLogos:
		return fmt.Sprintf("EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s.content_id AND mil.media_folder_id = $1)", alias), nil
	case artworkSurfaceSeasonPosters:
		return fmt.Sprintf("EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s.series_id AND mil.media_folder_id = $1)", alias), nil
	case artworkSurfaceLocalizedSeasonPosters:
		return fmt.Sprintf("EXISTS (SELECT 1 FROM seasons se JOIN media_item_libraries mil ON mil.content_id = se.series_id WHERE se.content_id = %s.season_content_id AND mil.media_folder_id = $1)", alias), nil
	case artworkSurfaceEpisodeStills:
		return fmt.Sprintf("EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s.series_id AND mil.media_folder_id = $1)", alias), nil
	case artworkSurfacePersonPhotos:
		return fmt.Sprintf("EXISTS (SELECT 1 FROM item_people ip JOIN media_item_libraries mil ON mil.content_id = ip.content_id WHERE ip.person_id = %s.id AND mil.media_folder_id = $1)", alias), nil
	case artworkSurfaceCollectionPosters, artworkSurfaceCollectionBackdrops:
		return alias + ".library_id = $1", nil
	case artworkSurfaceUserCollectionPosters:
		return artworkNoRemoteSource, nil
	case artworkSurfaceLibraryPosters:
		return alias + ".id = $1", nil
	default:
		return "", fmt.Errorf("artwork purge: unknown surface %q", name)
	}
}

func inventoryBytesForPaths(ctx context.Context, tx pgx.Tx, paths []string) (map[string]int64, error) {
	result := make(map[string]int64)
	if len(paths) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT original_path, total_physical_bytes FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, paths)
	if err != nil {
		return nil, fmt.Errorf("artwork purge: inventory bytes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		var bytes int64
		if err := rows.Scan(&path, &bytes); err != nil {
			return nil, err
		}
		result[path] = bytes
	}
	return result, rows.Err()
}

func sharedArtworkPaths(ctx context.Context, tx pgx.Tx, libraryID int64, paths []string) (map[string]bool, error) {
	result := make(map[string]bool)
	query := `WITH refs AS (` + artworkLibraryReferencesSQL() + `), server_refs AS (` + artworkServerReferencesSQL() + `)
		SELECT DISTINCT path FROM refs WHERE library_id <> $1 AND path = ANY($2)
		UNION SELECT DISTINCT path FROM server_refs WHERE path = ANY($2)`
	rows, err := tx.Query(ctx, query, libraryID, paths)
	if err != nil {
		return nil, fmt.Errorf("artwork purge: shared references: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		result[path] = true
	}
	return result, rows.Err()
}

func updatePurgeTarget(ctx context.Context, tx pgx.Tx, target artworkPurgeTarget) (bool, error) {
	surface, found := artworkSweepSurfaceByName(target.surfaceName)
	if !found {
		return false, fmt.Errorf("artwork purge: unknown surface %q", target.surfaceName)
	}
	set := surface.pathCol + " = $1"
	if surface.thumbhashCol != "" {
		set += ", " + surface.thumbhashCol + " = ''"
	}
	if !surface.noUpdatedAt {
		set += ", updated_at = NOW()"
	}
	where := make([]string, 0, len(surface.keyCols)+2)
	args := []any{target.fallback, target.path, target.source}
	for i, key := range surface.keyCols {
		where = append(where, fmt.Sprintf("%s = $%d", key.column, i+4))
		parsed, err := key.parse(target.keys[i])
		if err != nil {
			return false, err
		}
		args = append(args, parsed)
	}
	where = append(where, surface.pathCol+" = $2")
	if surface.sourceCol != "" {
		where = append(where, surface.sourceCol+" = $3")
	}
	query := fmt.Sprintf("UPDATE %s SET %s WHERE %s", surface.table, set, strings.Join(where, " AND "))
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("artwork purge: transition %s: %w", surface.name, err)
	}
	return tag.RowsAffected() > 0, nil
}

func purgeTargetAtFallback(ctx context.Context, tx pgx.Tx, target artworkPurgeTarget) (bool, error) {
	surface, found := artworkSweepSurfaceByName(target.surfaceName)
	if !found {
		return false, fmt.Errorf("unknown surface %q", target.surfaceName)
	}
	where := make([]string, len(surface.keyCols))
	args := []any{target.fallback}
	for i, key := range surface.keyCols {
		where[i] = fmt.Sprintf("%s = $%d", key.column, i+2)
		value, err := key.parse(target.keys[i])
		if err != nil {
			return false, err
		}
		args = append(args, value)
	}
	var exists bool
	err := tx.QueryRow(ctx, fmt.Sprintf("SELECT EXISTS (SELECT 1 FROM %s WHERE %s = $1 AND %s)", surface.table, surface.pathCol, strings.Join(where, " AND ")), args...).Scan(&exists)
	return exists, err
}

// purgePlanFingerprint covers what the plan read out of the catalog and the
// inventory, so a resume still detects rows that changed since planning. The
// fallback and protected fields are deliberately excluded: they are verdicts
// revalidation recomputes on every resume, and folding them in would make a
// source that died mid-purge look like catalog drift instead of the protected
// target it must become.
func purgePlanFingerprint(req ArtworkPurgeRequest, targets []artworkPurgeTarget) string {
	h := sha256.New()
	scope := "server"
	if req.Scope.LibraryID != nil {
		scope = fmt.Sprintf("library:%d", *req.Scope.LibraryID)
	}
	_, _ = fmt.Fprintf(h, "%s:%s\n", scope, req.Mode)
	for _, target := range targets {
		_, _ = fmt.Fprintf(h, "%s:%s:%s:%s:%t:%d\n", target.surfaceName, strings.Join(target.keys, "\x00"), target.path, target.source, target.shared, target.bytes)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type purgeRevisionPlan struct {
	bytes       int64
	shared      bool
	protected   bool
	transitions int64
}

type purgePlanMetrics struct {
	transitionedReferences int64
	queuedRevisions        int64
	sharedRevisions        int64
	protectedRevisions     int64
	pendingBytes           int64
	reclaimableBytes       int64
}

func calculatePurgePlanMetrics(targets []artworkPurgeTarget) purgePlanMetrics {
	byPath := make(map[string]*purgeRevisionPlan)
	for _, target := range targets {
		revision := byPath[target.path]
		if revision == nil {
			revision = &purgeRevisionPlan{bytes: target.bytes}
			byPath[target.path] = revision
		}
		revision.shared = revision.shared || target.shared
		revision.protected = revision.protected || target.protected
		if !target.shared && !target.protected {
			revision.transitions++
		}
	}
	var metrics purgePlanMetrics
	for _, revision := range byPath {
		metrics.transitionedReferences += revision.transitions
		if revision.shared {
			metrics.sharedRevisions++
			continue
		}
		if revision.protected {
			metrics.protectedRevisions++
		}
		if revision.transitions == 0 {
			continue
		}
		metrics.queuedRevisions++
		metrics.pendingBytes += revision.bytes
		if !revision.protected {
			metrics.reclaimableBytes += revision.bytes
		}
	}
	return metrics
}
