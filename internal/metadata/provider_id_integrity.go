package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

const providerIDRepairDefaultBatchSize = 250

type ProviderIDIntegrityStats struct {
	Scanned                      int
	CleanInserts                 int
	ProvisionalConflictsRepaired int
	MatchedCanonicalizations     int
	SkippedUnresolved            int
	Errors                       int
	RemainingEstimate            int
}

type ProviderIDIntegrityRepairer struct {
	pool *pgxpool.Pool
}

func NewProviderIDIntegrityRepairer(pool *pgxpool.Pool) *ProviderIDIntegrityRepairer {
	return &ProviderIDIntegrityRepairer{pool: pool}
}

func (r *ProviderIDIntegrityRepairer) Run(ctx context.Context, batchSize int) (ProviderIDIntegrityStats, error) {
	var stats ProviderIDIntegrityStats
	if r == nil || r.pool == nil {
		return stats, nil
	}
	if batchSize <= 0 {
		batchSize = providerIDRepairDefaultBatchSize
	}
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		batch, err := r.fetchDriftBatch(ctx, cursor, batchSize)
		if err != nil {
			return stats, err
		}
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			stats.Scanned++
			cursor = row.ContentID
			outcome, err := r.repairDriftRow(ctx, row)
			if err != nil {
				stats.Errors++
				slog.WarnContext(ctx, "metadata: provider-id drift row repair failed", "component", "metadata",
					"content_id", row.ContentID,
					"item_type", row.ItemType,
					"status", row.Status,
					"error", err,
				)
				continue
			}
			switch outcome {
			case "clean_insert":
				stats.CleanInserts++
			case "provisional_conflict":
				stats.ProvisionalConflictsRepaired++
			case "matched_canonicalization":
				stats.MatchedCanonicalizations++
			default:
				stats.SkippedUnresolved++
			}
		}
		if len(batch) < batchSize {
			break
		}
	}
	remaining, err := r.countDrift(ctx)
	if err == nil {
		stats.RemainingEstimate = remaining
	}
	return stats, nil
}

func (r *ProviderIDIntegrityRepairer) fetchDriftBatch(ctx context.Context, cursor string, batchSize int) ([]providerIDDriftRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT mi.content_id,
		       mi.type,
		       mi.status,
		       COALESCE(mi.imdb_id, ''),
		       COALESCE(mi.tmdb_id, ''),
		       COALESCE(mi.tvdb_id, '')
		FROM media_items mi
		WHERE mi.type IN ('movie', 'series')
		  AND mi.content_id > $2
		  AND (COALESCE(mi.imdb_id, '') <> ''
		       OR COALESCE(mi.tmdb_id, '') <> ''
		       OR COALESCE(mi.tvdb_id, '') <> '')
		  AND EXISTS (
		      SELECT 1
		      FROM (
		          VALUES
		              ('tmdb', COALESCE(mi.tmdb_id, '')),
		              ('tvdb', COALESCE(mi.tvdb_id, '')),
		              ('imdb', COALESCE(mi.imdb_id, ''))
		      ) AS legacy(provider, provider_id)
		      WHERE legacy.provider_id <> ''
		        AND NOT EXISTS (
		            SELECT 1
		            FROM media_item_provider_ids pid
		            WHERE pid.content_id = mi.content_id
		              AND pid.item_type = mi.type
		              AND pid.provider = legacy.provider
		              AND pid.provider_id = legacy.provider_id
		        )
		  )
		ORDER BY mi.content_id ASC
		LIMIT $1
	`, batchSize, cursor)
	if err != nil {
		return nil, fmt.Errorf("querying provider-id drift rows: %w", err)
	}
	defer rows.Close()

	var batch []providerIDDriftRow
	for rows.Next() {
		var row providerIDDriftRow
		if err := rows.Scan(&row.ContentID, &row.ItemType, &row.Status, &row.IMDbID, &row.TMDBID, &row.TVDBID); err != nil {
			return nil, fmt.Errorf("scanning provider-id drift row: %w", err)
		}
		batch = append(batch, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating provider-id drift rows: %w", err)
	}
	return batch, nil
}

func (r *ProviderIDIntegrityRepairer) countDrift(ctx context.Context) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM media_items mi
		WHERE mi.type IN ('movie', 'series')
		  AND (COALESCE(mi.imdb_id, '') <> ''
		       OR COALESCE(mi.tmdb_id, '') <> ''
		       OR COALESCE(mi.tvdb_id, '') <> '')
		  AND EXISTS (
		      SELECT 1
		      FROM (
		          VALUES
		              ('tmdb', COALESCE(mi.tmdb_id, '')),
		              ('tvdb', COALESCE(mi.tvdb_id, '')),
		              ('imdb', COALESCE(mi.imdb_id, ''))
		      ) AS legacy(provider, provider_id)
		      WHERE legacy.provider_id <> ''
		        AND NOT EXISTS (
		            SELECT 1
		            FROM media_item_provider_ids pid
		            WHERE pid.content_id = mi.content_id
		              AND pid.item_type = mi.type
		              AND pid.provider = legacy.provider
		              AND pid.provider_id = legacy.provider_id
		        )
		  )
	`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting remaining provider-id drift: %w", err)
	}
	return count, nil
}

type providerIDDriftRow struct {
	ContentID string
	ItemType  string
	Status    string
	IMDbID    string
	TMDBID    string
	TVDBID    string
}

type canonicalCandidate struct {
	ContentID    string
	Status       string
	CreatedAt    time.Time
	ActiveFiles  int
	LibraryCount int
}

type providerIDEntry struct {
	Provider   string
	ProviderID string
}

type providerIDOwner struct {
	ContentID string
	Status    string
}

func (r *ProviderIDIntegrityRepairer) repairDriftRow(ctx context.Context, row providerIDDriftRow) (string, error) {
	entries := providerEntriesFromDrift(row)
	if len(entries) == 0 {
		return "unresolved", nil
	}
	owners, err := r.findProviderIDOwners(ctx, row.ContentID, row.ItemType, entries)
	if err != nil {
		return "", err
	}
	if len(owners) == 0 {
		if err := r.insertProviderIDsForDriftRow(ctx, row, entries); err != nil {
			return "", err
		}
		return "clean_insert", nil
	}
	if len(owners) != 1 {
		return "unresolved", nil
	}
	var owner providerIDOwner
	for _, candidate := range owners {
		owner = candidate
	}
	switch {
	case isProvisionalOwnershipStatus(owner.Status) && isConfirmedOwnershipStatus(row.Status):
		if err := r.moveProvisionalProviderClaim(ctx, owner.ContentID, row, entries); err != nil {
			return "", err
		}
		return "provisional_conflict", nil
	case isConfirmedOwnershipStatus(owner.Status) && isProvisionalOwnershipStatus(row.Status):
		if _, err := canonicalizeProviderIDDuplicateInto(ctx, r.pool, row.ContentID, owner.ContentID, false); err != nil {
			return "", err
		}
		return "matched_canonicalization", nil
	case isConfirmedOwnershipStatus(owner.Status) && isConfirmedOwnershipStatus(row.Status):
		if _, err := canonicalizeProviderIDDuplicate(ctx, r.pool, row.ContentID, owner.ContentID, true); err != nil {
			return "", err
		}
		return "matched_canonicalization", nil
	default:
		return "unresolved", nil
	}
}

func providerEntriesFromDrift(row providerIDDriftRow) []providerIDEntry {
	entries := make([]providerIDEntry, 0, 3)
	if strings.TrimSpace(row.TMDBID) != "" {
		entries = append(entries, providerIDEntry{Provider: "tmdb", ProviderID: strings.TrimSpace(row.TMDBID)})
	}
	if strings.TrimSpace(row.TVDBID) != "" {
		entries = append(entries, providerIDEntry{Provider: "tvdb", ProviderID: strings.TrimSpace(row.TVDBID)})
	}
	if strings.TrimSpace(row.IMDbID) != "" {
		entries = append(entries, providerIDEntry{Provider: "imdb", ProviderID: strings.TrimSpace(row.IMDbID)})
	}
	return entries
}

func (r *ProviderIDIntegrityRepairer) findProviderIDOwners(
	ctx context.Context,
	contentID string,
	itemType string,
	entries []providerIDEntry,
) (map[string]providerIDOwner, error) {
	owners := make(map[string]providerIDOwner)
	for _, entry := range entries {
		var owner providerIDOwner
		err := r.pool.QueryRow(ctx, `
			SELECT pid.content_id, mi.status
			FROM media_item_provider_ids pid
			JOIN media_items mi ON mi.content_id = pid.content_id
			WHERE pid.provider = $1
			  AND pid.provider_id = $2
			  AND pid.item_type = $3
			  AND pid.content_id <> $4
			LIMIT 1
		`, entry.Provider, entry.ProviderID, itemType, contentID).Scan(&owner.ContentID, &owner.Status)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, fmt.Errorf("finding owner for %s=%s: %w", entry.Provider, entry.ProviderID, err)
		}
		owners[owner.ContentID] = owner
	}
	return owners, nil
}

func (r *ProviderIDIntegrityRepairer) insertProviderIDsForDriftRow(ctx context.Context, row providerIDDriftRow, entries []providerIDEntry) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin clean provider-id repair: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM media_item_provider_ids WHERE content_id = $1`, row.ContentID); err != nil {
		return fmt.Errorf("clear existing provider ids for %s: %w", row.ContentID, err)
	}
	for _, entry := range entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NOW(), NOW())
		`, row.ContentID, row.ItemType, entry.Provider, entry.ProviderID); err != nil {
			return fmt.Errorf("insert repaired provider id %s for %s: %w", entry.Provider, row.ContentID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit clean provider-id repair: %w", err)
	}
	return nil
}

func (r *ProviderIDIntegrityRepairer) moveProvisionalProviderClaim(ctx context.Context, ownerContentID string, row providerIDDriftRow, entries []providerIDEntry) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin provisional provider-id repair: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var ownerStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM media_items WHERE content_id = $1 FOR UPDATE`, ownerContentID).Scan(&ownerStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("loading status for provisional owner %s: %w", ownerContentID, err)
	}
	if isConfirmedOwnershipStatus(ownerStatus) {
		return fmt.Errorf("refusing to move provider IDs from %s: status is now %q", ownerContentID, ownerStatus)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM media_item_provider_ids WHERE content_id = $1`, ownerContentID); err != nil {
		return fmt.Errorf("clear provisional provider ids for %s: %w", ownerContentID, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM media_item_provider_ids WHERE content_id = $1`, row.ContentID); err != nil {
		return fmt.Errorf("clear existing provider ids for %s: %w", row.ContentID, err)
	}
	for _, entry := range entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NOW(), NOW())
		`, row.ContentID, row.ItemType, entry.Provider, entry.ProviderID); err != nil {
			return fmt.Errorf("insert provider id %s for %s after provisional repair: %w", entry.Provider, row.ContentID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit provisional provider-id repair: %w", err)
	}
	return nil
}

func (s *MetadataService) canonicalizeProviderIDDuplicate(
	ctx context.Context,
	leftContentID string,
	rightContentID string,
	allowMatchedSource bool,
) (string, error) {
	if s == nil || s.dbPool == nil {
		return "", nil
	}
	return canonicalizeProviderIDDuplicate(ctx, s.dbPool, leftContentID, rightContentID, allowMatchedSource)
}

// clearProvisionalProviderIDsLocked removes provider ID rows for an item only
// after re-confirming, under a row-level lock, that the item is still in a
// provisional ownership status. Without the lock, a parallel goroutine could
// promote the item to matched between the caller's status check and our
// delete, silently wiping a confirmed item's provider IDs.
func clearProvisionalProviderIDsLocked(ctx context.Context, pool *pgxpool.Pool, contentID string) error {
	contentID = strings.TrimSpace(contentID)
	if contentID == "" {
		return fmt.Errorf("content_id is required")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin provisional provider-id clear: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM media_items WHERE content_id = $1 FOR UPDATE`, contentID).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("loading status for provisional provider-id clear of %s: %w", contentID, err)
	}
	if isConfirmedOwnershipStatus(status) {
		return fmt.Errorf("refusing to clear provider IDs of %s: status is now %q", contentID, status)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM media_item_provider_ids WHERE content_id = $1`, contentID); err != nil {
		return fmt.Errorf("clearing provisional provider IDs from %s: %w", contentID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit provisional provider-id clear: %w", err)
	}
	return nil
}

func canonicalizeProviderIDDuplicate(
	ctx context.Context,
	pool *pgxpool.Pool,
	leftContentID string,
	rightContentID string,
	allowMatchedSource bool,
) (string, error) {
	leftContentID = strings.TrimSpace(leftContentID)
	rightContentID = strings.TrimSpace(rightContentID)
	if leftContentID == "" || rightContentID == "" || leftContentID == rightContentID {
		return leftContentID, nil
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", fmt.Errorf("begin provider-id canonicalization: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	canonical, source, err := chooseCanonicalProviderOwner(ctx, tx, leftContentID, rightContentID)
	if err != nil {
		return "", err
	}
	if !allowMatchedSource && isConfirmedOwnershipStatus(source.Status) {
		return "", fmt.Errorf("refusing to canonicalize matched source %s without allowMatchedSource", source.ContentID)
	}
	if isConfirmedOwnershipStatus(canonical.Status) && isConfirmedOwnershipStatus(source.Status) && bothCandidatesHaveContent(canonical, source) {
		return "", fmt.Errorf("refusing to merge matched items %s and %s: both have files or library memberships (likely separate editions)", canonical.ContentID, source.ContentID)
	}
	if err := canonicalizeMediaItemReferencesTx(ctx, tx, source.ContentID, canonical.ContentID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, source.ContentID); err != nil {
		return "", fmt.Errorf("delete duplicate media item %s: %w", source.ContentID, err)
	}
	if err := catalog.EnqueueSearchIndexRename(ctx, tx, source.ContentID, canonical.ContentID); err != nil {
		return "", fmt.Errorf("enqueue catalog search provider-id canonicalization %s -> %s: %w", source.ContentID, canonical.ContentID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit provider-id canonicalization: %w", err)
	}
	return canonical.ContentID, nil
}

func canonicalizeProviderIDDuplicateInto(
	ctx context.Context,
	pool *pgxpool.Pool,
	sourceID string,
	canonicalID string,
	allowMatchedSource bool,
) (string, error) {
	sourceID = strings.TrimSpace(sourceID)
	canonicalID = strings.TrimSpace(canonicalID)
	if sourceID == "" || canonicalID == "" {
		return "", fmt.Errorf("source and canonical content IDs are required")
	}
	if sourceID == canonicalID {
		return canonicalID, nil
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", fmt.Errorf("begin provider-id canonicalization: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	candidates := make(map[string]canonicalCandidate, 2)
	rows, err := tx.Query(ctx, `
		SELECT mi.content_id,
		       mi.status,
		       mi.created_at,
		       (SELECT COUNT(*) FROM media_files mf WHERE mf.content_id = mi.content_id AND mf.missing_since IS NULL) AS active_files,
		       (SELECT COUNT(*) FROM media_item_libraries mil WHERE mil.content_id = mi.content_id) AS library_count
		FROM media_items mi
		WHERE mi.content_id = ANY($1::text[])
		ORDER BY mi.content_id
		FOR UPDATE
	`, []string{sourceID, canonicalID})
	if err != nil {
		return "", fmt.Errorf("locking duplicate provider owners: %w", err)
	}
	for rows.Next() {
		var candidate canonicalCandidate
		if err := rows.Scan(&candidate.ContentID, &candidate.Status, &candidate.CreatedAt, &candidate.ActiveFiles, &candidate.LibraryCount); err != nil {
			rows.Close()
			return "", fmt.Errorf("scanning duplicate provider owner lock: %w", err)
		}
		candidates[candidate.ContentID] = candidate
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", fmt.Errorf("iterating duplicate provider owner locks: %w", err)
	}
	rows.Close()
	source, sourceOK := candidates[sourceID]
	canonical, canonicalOK := candidates[canonicalID]
	if !sourceOK || !canonicalOK {
		return "", fmt.Errorf("expected two duplicate provider owners, got %d", len(candidates))
	}
	if !allowMatchedSource && isConfirmedOwnershipStatus(source.Status) {
		return "", fmt.Errorf("refusing to canonicalize matched source %s without allowMatchedSource", sourceID)
	}
	// Scanner-created provisional rows can already own local files/library
	// memberships. If the matched owner also has content, merging here would
	// collapse separate editions before the source is confirmed.
	if bothCandidatesHaveContent(canonical, source) {
		return "", fmt.Errorf("refusing to merge items %s and %s: both have files or library memberships (likely separate editions)", canonical.ContentID, source.ContentID)
	}
	if err := canonicalizeMediaItemReferencesTx(ctx, tx, sourceID, canonicalID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, sourceID); err != nil {
		return "", fmt.Errorf("delete duplicate media item %s: %w", sourceID, err)
	}
	if err := catalog.EnqueueSearchIndexRename(ctx, tx, sourceID, canonicalID); err != nil {
		return "", fmt.Errorf("enqueue catalog search provider-id canonicalization %s -> %s: %w", sourceID, canonicalID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit provider-id canonicalization: %w", err)
	}
	return canonicalID, nil
}

func chooseCanonicalProviderOwner(ctx context.Context, tx pgx.Tx, leftContentID, rightContentID string) (canonical canonicalCandidate, source canonicalCandidate, err error) {
	rows, err := tx.Query(ctx, `
		SELECT mi.content_id,
		       mi.status,
		       mi.created_at,
		       (SELECT COUNT(*) FROM media_files mf WHERE mf.content_id = mi.content_id AND mf.missing_since IS NULL) AS active_files,
		       (SELECT COUNT(*) FROM media_item_libraries mil WHERE mil.content_id = mi.content_id) AS library_count
		FROM media_items mi
		WHERE mi.content_id = ANY($1::text[])
		ORDER BY mi.content_id
		FOR UPDATE
	`, []string{leftContentID, rightContentID})
	if err != nil {
		return canonicalCandidate{}, canonicalCandidate{}, fmt.Errorf("loading duplicate provider owners: %w", err)
	}
	defer rows.Close()

	candidates := make([]canonicalCandidate, 0, 2)
	for rows.Next() {
		var c canonicalCandidate
		if err := rows.Scan(&c.ContentID, &c.Status, &c.CreatedAt, &c.ActiveFiles, &c.LibraryCount); err != nil {
			return canonicalCandidate{}, canonicalCandidate{}, fmt.Errorf("scanning duplicate provider owner: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return canonicalCandidate{}, canonicalCandidate{}, fmt.Errorf("iterating duplicate provider owners: %w", err)
	}
	if len(candidates) != 2 {
		return canonicalCandidate{}, canonicalCandidate{}, fmt.Errorf("expected two duplicate provider owners, got %d", len(candidates))
	}

	best := candidates[0]
	other := candidates[1]
	if betterCanonicalCandidate(other, best) {
		best, other = other, best
	}
	return best, other, nil
}

// bothCandidatesHaveContent reports whether both items appear to hold real
// user-curated content (active files or library memberships). When that is
// true for two matched items, they are likely intentional separate entries
// (e.g. director's vs. theatrical cut sharing an external ID) and we refuse
// to silently destroy one by merging.
func bothCandidatesHaveContent(left, right canonicalCandidate) bool {
	leftHasContent := left.ActiveFiles > 0 || left.LibraryCount > 0
	rightHasContent := right.ActiveFiles > 0 || right.LibraryCount > 0
	return leftHasContent && rightHasContent
}

func betterCanonicalCandidate(left, right canonicalCandidate) bool {
	if (left.ActiveFiles > 0) != (right.ActiveFiles > 0) {
		return left.ActiveFiles > 0
	}
	if left.LibraryCount != right.LibraryCount {
		return left.LibraryCount > right.LibraryCount
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.ContentID < right.ContentID
}

// mediaItemMergeStep is one statement in the ordered media-item merge sequence
// run by canonicalizeMediaItemReferencesTx. Every step references $1 (the
// source content ID); steps that move data onto the canonical row also
// reference $2 (the canonical content ID).
type mediaItemMergeStep struct {
	name string
	sql  string
}

// mergeStepPlaceholderRe matches positional query placeholders ($1, $2, …).
var mergeStepPlaceholderRe = regexp.MustCompile(`\$(\d+)`)

// maxPlaceholder returns the highest positional placeholder number referenced
// in sql (0 if none). It reads the full placeholder number, so "$20" is 20 and
// is never confused with "$2" — unlike a substring check.
func maxPlaceholder(sql string) int {
	highest := 0
	for _, m := range mergeStepPlaceholderRe.FindAllStringSubmatch(sql, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > highest {
			highest = n
		}
	}
	return highest
}

// mergeStepArgs returns the positional arguments for a merge step, sized to the
// placeholders the SQL actually binds. Passing too many args (e.g. an unused
// $2) makes pgx reject the Exec with "mismatched param and argument count"
// under the default QueryExecModeCacheStatement, aborting the whole merge
// transaction. Every merge step binds $1 (source) and optionally $2
// (canonical); TestMergeStepPlaceholdersAreBounded enforces that contract, so
// the default branch is unreachable for the defined steps and fails loud if a
// future step violates it.
func mergeStepArgs(sql, sourceID, canonicalID string) []any {
	switch n := maxPlaceholder(sql); n {
	case 1:
		return []any{sourceID}
	case 2:
		return []any{sourceID, canonicalID}
	default:
		panic(fmt.Sprintf("merge step binds unsupported placeholder count %d: %q", n, sql))
	}
}

var mediaItemMergeSteps = []mediaItemMergeStep{
	{"merge provider ids", `
			INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id, created_at, updated_at)
			SELECT $2, item_type, provider, provider_id, created_at, NOW()
			FROM media_item_provider_ids
			WHERE content_id = $1
			ON CONFLICT DO NOTHING`},
	{"drop source provider ids", `DELETE FROM media_item_provider_ids WHERE content_id = $1`},
	{"merge legacy provider columns", `
			UPDATE media_items dest
			SET imdb_id = COALESCE(NULLIF(dest.imdb_id, ''), src.imdb_id),
			    tmdb_id = COALESCE(NULLIF(dest.tmdb_id, ''), src.tmdb_id),
			    tvdb_id = COALESCE(NULLIF(dest.tvdb_id, ''), src.tvdb_id),
			    updated_at = NOW()
			FROM media_items src
			WHERE dest.content_id = $2 AND src.content_id = $1`},
	{"move media files", `UPDATE media_files SET content_id = $2, updated_at = NOW() WHERE content_id = $1`},
	{"move seasons", `UPDATE seasons SET series_id = $2, updated_at = NOW() WHERE series_id = $1`},
	{"move episodes", `UPDATE episodes SET series_id = $2, updated_at = NOW() WHERE series_id = $1`},
	{"merge library memberships", `
			INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
			SELECT $2, media_folder_id, MIN(first_seen_at)
			FROM media_item_libraries
			WHERE content_id = $1
			GROUP BY media_folder_id
			ON CONFLICT (content_id, media_folder_id) DO UPDATE
			SET first_seen_at = LEAST(media_item_libraries.first_seen_at, EXCLUDED.first_seen_at)`},
	{"delete source library memberships", `DELETE FROM media_item_libraries WHERE content_id = $1`},
	{"move root claims", `UPDATE media_item_roots SET content_id = $2, last_seen_at = NOW() WHERE content_id = $1`},
	{"move group claims", `UPDATE media_item_groups SET content_id = $2, last_seen_at = NOW() WHERE content_id = $1`},
	{"merge collection items", `
			INSERT INTO library_collection_items (collection_id, media_item_id, position, source_rank, created_at, updated_at)
			SELECT collection_id, $2, MIN(position), MIN(source_rank), MIN(created_at), NOW()
			FROM library_collection_items
			WHERE media_item_id = $1
			GROUP BY collection_id
			ON CONFLICT (collection_id, media_item_id) DO UPDATE
			SET position = LEAST(library_collection_items.position, EXCLUDED.position),
			    source_rank = LEAST(library_collection_items.source_rank, EXCLUDED.source_rank),
			    updated_at = NOW()`},
	{"delete source collection items", `DELETE FROM library_collection_items WHERE media_item_id = $1`},
	{"merge personal collection items", `
			INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id, position, added_at)
			SELECT user_id, collection_id, $2, MIN(position), MIN(added_at)
			FROM user_personal_collection_items
			WHERE media_item_id = $1
			GROUP BY user_id, collection_id
			ON CONFLICT (user_id, collection_id, media_item_id) DO UPDATE
			SET position = LEAST(user_personal_collection_items.position, EXCLUDED.position),
			    added_at = LEAST(user_personal_collection_items.added_at, EXCLUDED.added_at)`},
	{"delete source personal collection items", `DELETE FROM user_personal_collection_items WHERE media_item_id = $1`},
	{"merge favorites", `
			INSERT INTO user_favorites (user_id, profile_id, media_item_id, added_at)
			SELECT user_id, profile_id, $2, MIN(added_at)
			FROM user_favorites
			WHERE media_item_id = $1
			GROUP BY user_id, profile_id
			ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE
			SET added_at = LEAST(user_favorites.added_at, EXCLUDED.added_at)`},
	{"delete source favorites", `DELETE FROM user_favorites WHERE media_item_id = $1`},
	{"merge watchlist", `
			INSERT INTO user_watchlist (user_id, profile_id, media_item_id, added_at)
			SELECT user_id, profile_id, $2, MIN(added_at)
			FROM user_watchlist
			WHERE media_item_id = $1
			GROUP BY user_id, profile_id
			ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE
			SET added_at = LEAST(user_watchlist.added_at, EXCLUDED.added_at)`},
	{"delete source watchlist", `DELETE FROM user_watchlist WHERE media_item_id = $1`},
	{"merge ratings", `
			INSERT INTO user_ratings (user_id, profile_id, media_item_id, rating, rated_at)
			SELECT DISTINCT ON (user_id, profile_id) user_id, profile_id, $2, rating, rated_at
			FROM user_ratings
			WHERE media_item_id = $1
			ORDER BY user_id, profile_id, rated_at DESC
			ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE
			SET rating = CASE WHEN EXCLUDED.rated_at >= user_ratings.rated_at THEN EXCLUDED.rating ELSE user_ratings.rating END,
			    rated_at = GREATEST(user_ratings.rated_at, EXCLUDED.rated_at)`},
	{"delete source ratings", `DELETE FROM user_ratings WHERE media_item_id = $1`},
	{"merge progress", `
			INSERT INTO user_watch_progress (
				user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed,
				updated_at, last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
			)
			SELECT user_id, profile_id, $2, position_seconds, duration_seconds, completed,
			       updated_at, last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
			FROM user_watch_progress
			WHERE media_item_id = $1
			ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE
			SET position_seconds = CASE WHEN EXCLUDED.updated_at >= user_watch_progress.updated_at THEN EXCLUDED.position_seconds ELSE user_watch_progress.position_seconds END,
			    duration_seconds = GREATEST(user_watch_progress.duration_seconds, EXCLUDED.duration_seconds),
			    completed = user_watch_progress.completed OR EXCLUDED.completed,
			    updated_at = GREATEST(user_watch_progress.updated_at, EXCLUDED.updated_at),
			    last_file_id = CASE WHEN EXCLUDED.updated_at >= user_watch_progress.updated_at THEN EXCLUDED.last_file_id ELSE user_watch_progress.last_file_id END,
			    last_resolution = CASE WHEN EXCLUDED.updated_at >= user_watch_progress.updated_at THEN EXCLUDED.last_resolution ELSE user_watch_progress.last_resolution END,
			    last_hdr = CASE WHEN EXCLUDED.updated_at >= user_watch_progress.updated_at THEN EXCLUDED.last_hdr ELSE user_watch_progress.last_hdr END,
			    last_codec_video = CASE WHEN EXCLUDED.updated_at >= user_watch_progress.updated_at THEN EXCLUDED.last_codec_video ELSE user_watch_progress.last_codec_video END,
			    last_edition_key = CASE WHEN EXCLUDED.updated_at >= user_watch_progress.updated_at THEN EXCLUDED.last_edition_key ELSE user_watch_progress.last_edition_key END`},
	{"delete source progress", `DELETE FROM user_watch_progress WHERE media_item_id = $1`},
	{"merge ebook reader progress", `
			INSERT INTO ebook_reader_progress (
				user_id, profile_id, content_id, file_id, location, progress, updated_at
			)
			SELECT user_id, profile_id, $2, file_id, location, progress, updated_at
			FROM ebook_reader_progress
			WHERE content_id = $1
			ON CONFLICT (user_id, profile_id, content_id) DO UPDATE
			SET file_id = CASE WHEN EXCLUDED.updated_at >= ebook_reader_progress.updated_at THEN EXCLUDED.file_id ELSE ebook_reader_progress.file_id END,
			    location = CASE WHEN EXCLUDED.updated_at >= ebook_reader_progress.updated_at THEN EXCLUDED.location ELSE ebook_reader_progress.location END,
			    progress = CASE WHEN EXCLUDED.updated_at >= ebook_reader_progress.updated_at THEN EXCLUDED.progress ELSE ebook_reader_progress.progress END,
			    updated_at = GREATEST(ebook_reader_progress.updated_at, EXCLUDED.updated_at)`},
	{"delete source ebook reader progress", `DELETE FROM ebook_reader_progress WHERE content_id = $1`},
	{"move history", `UPDATE user_watch_history SET media_item_id = $2 WHERE media_item_id = $1`},
	{"merge hidden history", `
			INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before, updated_at)
			SELECT user_id, profile_id, $2, MAX(hidden_before), MAX(updated_at)
			FROM user_history_hidden_items
			WHERE media_item_id = $1
			GROUP BY user_id, profile_id
			ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE
			SET hidden_before = GREATEST(user_history_hidden_items.hidden_before, EXCLUDED.hidden_before),
			    updated_at = GREATEST(user_history_hidden_items.updated_at, EXCLUDED.updated_at)`},
	{"delete source hidden history", `DELETE FROM user_history_hidden_items WHERE media_item_id = $1`},
	{"delete duplicate home item dismissals", `
			DELETE FROM user_home_item_dismissals src
			USING user_home_item_dismissals dest
			WHERE src.media_item_id = $1
			  AND dest.media_item_id = $2
			  AND dest.user_id = src.user_id
			  AND dest.profile_id = src.profile_id
			  AND dest.surface = src.surface`},
	{"move home item dismissals", `UPDATE user_home_item_dismissals SET media_item_id = $2 WHERE media_item_id = $1`},
	{"remap home item dismissals series", `UPDATE user_home_item_dismissals SET series_id = $2 WHERE series_id = $1`},
	{"delete duplicate audio preferences", `
			DELETE FROM user_audio_preferences src
			USING user_audio_preferences dest
			WHERE src.series_id = $1
			  AND dest.series_id = $2
			  AND dest.user_id = src.user_id
			  AND dest.profile_id = src.profile_id`},
	{"move audio preferences", `UPDATE user_audio_preferences SET series_id = $2 WHERE series_id = $1`},
	{"delete duplicate subtitle preferences", `
			DELETE FROM user_subtitle_preferences src
			USING user_subtitle_preferences dest
			WHERE src.series_id = $1
			  AND dest.series_id = $2
			  AND dest.user_id = src.user_id
			  AND dest.profile_id = src.profile_id`},
	{"move subtitle preferences", `UPDATE user_subtitle_preferences SET series_id = $2 WHERE series_id = $1`},
	{"delete duplicate series playback preferences", `
			DELETE FROM user_series_playback_preferences src
			USING user_series_playback_preferences dest
			WHERE src.series_id = $1
			  AND dest.series_id = $2
			  AND dest.user_id = src.user_id
			  AND dest.profile_id = src.profile_id`},
	{"move series playback preferences", `UPDATE user_series_playback_preferences SET series_id = $2 WHERE series_id = $1`},
	{"merge plex sync item bindings timestamps", `
			UPDATE plex_sync_item_bindings dest
			SET last_seen_at = GREATEST(dest.last_seen_at, src.last_seen_at),
			    updated_at = NOW()
			FROM plex_sync_item_bindings src
			WHERE src.media_item_id = $1
			  AND dest.connection_id = src.connection_id
			  AND (dest.media_item_id = $2 OR dest.plex_rating_key = src.plex_rating_key)
			  AND dest.media_item_id <> src.media_item_id`},
	{"delete duplicate plex sync item bindings", `
			DELETE FROM plex_sync_item_bindings src
			USING plex_sync_item_bindings dest
			WHERE src.media_item_id = $1
			  AND dest.connection_id = src.connection_id
			  AND (dest.media_item_id = $2 OR dest.plex_rating_key = src.plex_rating_key)
			  AND dest.media_item_id <> src.media_item_id`},
	{"move remaining plex sync item bindings", `UPDATE plex_sync_item_bindings SET media_item_id = $2, updated_at = NOW() WHERE media_item_id = $1`},
	{"delete duplicate plex sync item state", `
			DELETE FROM plex_sync_item_state src
			USING plex_sync_item_state dest
			WHERE src.media_item_id = $1
			  AND dest.media_item_id = $2
			  AND dest.mapping_id = src.mapping_id`},
	{"move plex sync item state", `UPDATE plex_sync_item_state SET media_item_id = $2, updated_at = NOW() WHERE media_item_id = $1`},
	{"move webhook sync item state", `UPDATE webhook_sync_item_state SET media_item_id = $2, updated_at = NOW() WHERE media_item_id = $1`},
	{"move watch together rooms selected", `UPDATE watch_together_rooms SET selected_content_id = $2 WHERE selected_content_id = $1`},
	{"move watch together suggestions", `UPDATE watch_together_suggestions SET content_id = $2 WHERE content_id = $1`},
	{"move admin playback history", `UPDATE playback_history_admin SET media_item_id = $2 WHERE media_item_id = $1`},
	{"move user downloads", `UPDATE user_downloads SET media_item_id = $2 WHERE media_item_id = $1`},
	{"move downloads", `UPDATE downloads SET content_id = $2, updated_at = NOW() WHERE content_id = $1`},
	{"merge embeddings", `
			INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text, created_at, updated_at)
			SELECT $2, embedding, model, canonical_text, created_at, updated_at
			FROM media_item_embeddings
			WHERE media_item_id = $1
			ON CONFLICT (media_item_id) DO NOTHING`},
	{"delete source embeddings", `DELETE FROM media_item_embeddings WHERE media_item_id = $1`},
	{"merge item localizations", `
			INSERT INTO media_item_localizations (
				content_id, language, title, sort_title, overview, tagline, poster_path, poster_thumbhash,
				backdrop_path, backdrop_thumbhash, logo_path, created_at, updated_at
			)
			SELECT $2, language, title, sort_title, overview, tagline, poster_path, poster_thumbhash,
			       backdrop_path, backdrop_thumbhash, logo_path, created_at, NOW()
			FROM media_item_localizations
			WHERE content_id = $1
			ON CONFLICT (content_id, language) DO NOTHING`},
	{"delete source localizations", `DELETE FROM media_item_localizations WHERE content_id = $1`},
	{"dedupe people", `
			DELETE FROM item_people src
			USING item_people dest
			WHERE src.content_id = $1
			  AND dest.content_id = $2
			  AND dest.person_id = src.person_id
			  AND dest.kind = src.kind
			  AND dest.character = src.character`},
	{"move people", `UPDATE item_people SET content_id = $2 WHERE content_id = $1`},
	{"merge refresh debt", `
			INSERT INTO metadata_refresh_debt (
				target_type, content_id, priority, reason_mask, next_refresh_at,
				claimed_at, lease_expires_at, last_attempt_at, last_success_at,
				attempt_count, last_error, updated_at
			)
			SELECT target_type, $2, priority, reason_mask, next_refresh_at,
			       claimed_at, lease_expires_at, last_attempt_at, last_success_at,
			       attempt_count, last_error, updated_at
			FROM metadata_refresh_debt
			WHERE target_type = 'item' AND content_id = $1
			ON CONFLICT (target_type, content_id) DO UPDATE
			SET priority = GREATEST(metadata_refresh_debt.priority, EXCLUDED.priority),
			    reason_mask = metadata_refresh_debt.reason_mask | EXCLUDED.reason_mask,
			    next_refresh_at = LEAST(metadata_refresh_debt.next_refresh_at, EXCLUDED.next_refresh_at),
			    attempt_count = GREATEST(metadata_refresh_debt.attempt_count, EXCLUDED.attempt_count),
			    last_error = COALESCE(NULLIF(metadata_refresh_debt.last_error, ''), EXCLUDED.last_error),
			    updated_at = NOW()`},
	{"delete source refresh debt", `DELETE FROM metadata_refresh_debt WHERE target_type = 'item' AND content_id = $1`},
	{"merge stale media ids", `
			INSERT INTO stale_media_ids (content_id, provider, provider_id, first_seen_at, last_seen_at)
			SELECT $2, provider, provider_id, first_seen_at, last_seen_at
			FROM stale_media_ids
			WHERE content_id = $1
			ON CONFLICT (content_id, provider) DO UPDATE
			SET provider_id = COALESCE(NULLIF(stale_media_ids.provider_id, ''), EXCLUDED.provider_id),
			    first_seen_at = LEAST(stale_media_ids.first_seen_at, EXCLUDED.first_seen_at),
			    last_seen_at = GREATEST(stale_media_ids.last_seen_at, EXCLUDED.last_seen_at)`},
	{"delete source stale media ids", `DELETE FROM stale_media_ids WHERE content_id = $1`},
	{"move watch provider history exports", `UPDATE watch_provider_history_exports SET media_item_id = $2, updated_at = NOW() WHERE media_item_id = $1`},
	{"move watch provider scrobble sessions", `UPDATE watch_provider_scrobble_sessions SET media_item_id = $2, updated_at = NOW() WHERE media_item_id = $1`},
	{"merge watch provider list items by provider key", `
			UPDATE watch_provider_list_items dest
			SET remote_present = dest.remote_present OR src.remote_present,
			    local_present = dest.local_present OR src.local_present,
			    last_seen_remote_at = GREATEST(dest.last_seen_remote_at, src.last_seen_remote_at),
			    last_seen_local_at = GREATEST(dest.last_seen_local_at, src.last_seen_local_at),
			    last_exported_at = GREATEST(dest.last_exported_at, src.last_exported_at),
			    last_error = COALESCE(NULLIF(dest.last_error, ''), src.last_error),
			    updated_at = NOW()
			FROM watch_provider_list_items src
			WHERE src.media_item_id = $1
			  AND dest.media_item_id = $2
			  AND src.connection_id = dest.connection_id
			  AND src.list_kind = dest.list_kind
			  AND src.provider_item_key <> ''
			  AND src.provider_item_key = dest.provider_item_key`},
	{"merge watch provider list items by connection", `
			UPDATE watch_provider_list_items dest
			SET provider_item_key = CASE
			        WHEN dest.provider_item_key = '' THEN src.provider_item_key
			        ELSE dest.provider_item_key
			    END,
			    kind = COALESCE(NULLIF(dest.kind, ''), src.kind),
			    title = COALESCE(NULLIF(dest.title, ''), src.title),
			    year = CASE WHEN dest.year = 0 THEN src.year ELSE dest.year END,
			    remote_present = dest.remote_present OR src.remote_present,
			    local_present = dest.local_present OR src.local_present,
			    last_seen_remote_at = GREATEST(dest.last_seen_remote_at, src.last_seen_remote_at),
			    last_seen_local_at = GREATEST(dest.last_seen_local_at, src.last_seen_local_at),
			    last_exported_at = GREATEST(dest.last_exported_at, src.last_exported_at),
			    last_error = COALESCE(NULLIF(dest.last_error, ''), src.last_error),
			    updated_at = NOW()
			FROM watch_provider_list_items src
			WHERE src.media_item_id = $1
			  AND dest.media_item_id = $2
			  AND src.connection_id = dest.connection_id
			  AND src.list_kind = dest.list_kind`},
	{"delete duplicate watch provider list items", `
			DELETE FROM watch_provider_list_items src
			USING watch_provider_list_items dest
			WHERE src.media_item_id = $1
			  AND dest.media_item_id = $2
			  AND src.connection_id = dest.connection_id
			  AND src.list_kind = dest.list_kind
			  AND (src.provider_item_key = dest.provider_item_key OR dest.media_item_id = $2)`},
	{"move remaining watch provider list items", `UPDATE watch_provider_list_items SET media_item_id = $2, updated_at = NOW() WHERE media_item_id = $1`},
}

func canonicalizeMediaItemReferencesTx(ctx context.Context, tx pgx.Tx, sourceID, canonicalID string) error {
	if sourceID == "" || canonicalID == "" || sourceID == canonicalID {
		return nil
	}
	if err := ensureSeriesCanMove(ctx, tx, sourceID, canonicalID); err != nil {
		return err
	}
	for _, step := range mediaItemMergeSteps {
		if _, err := tx.Exec(ctx, step.sql, mergeStepArgs(step.sql, sourceID, canonicalID)...); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}

func ensureSeriesCanMove(ctx context.Context, tx pgx.Tx, sourceID, canonicalID string) error {
	var sourceType string
	if err := tx.QueryRow(ctx, `SELECT type FROM media_items WHERE content_id = $1`, sourceID).Scan(&sourceType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("source media item %s not found", sourceID)
		}
		return fmt.Errorf("loading source item type: %w", err)
	}
	if sourceType != "series" {
		return nil
	}
	var conflictCount int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM seasons src
		JOIN seasons dest
		  ON dest.series_id = $2
		 AND dest.season_number = src.season_number
		WHERE src.series_id = $1
	`, sourceID, canonicalID).Scan(&conflictCount); err != nil {
		return fmt.Errorf("checking duplicate season conflicts: %w", err)
	}
	if conflictCount > 0 {
		return fmt.Errorf("series %s has %d season conflicts with canonical series %s", sourceID, conflictCount, canonicalID)
	}
	return nil
}
