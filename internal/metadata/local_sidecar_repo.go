package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/pathscope"
)

// localSidecarRecord is the stored fingerprint of one target's local sidecar
// files in one library (table local_sidecar_fingerprints).
type localSidecarRecord struct {
	FolderID   int
	TargetType string // RefreshTargetItem, RefreshTargetSeason or RefreshTargetEpisode
	ContentID  string
	// ItemID is the item the target belongs to: itself, or a season's or
	// episode's series.
	ItemID string
	// Fingerprint hashes the sidecar files the local provider applied.
	Fingerprint string
	// FolderDigest (items only) hashes every sidecar file in the item's
	// folders, as a scan lists them.
	FolderDigest string
	// ProviderIDs (items only) are the IDs the item's NFO declared when it was
	// last checked: the baseline an identity correction is measured against.
	ProviderIDs map[string]string
	// Baseline marks a first-sight record. It is stored only where no record
	// exists yet, so a concurrent check's record is kept and an edit made
	// between the two is still found.
	Baseline bool
	// ifFingerprint, when set, makes the write conditional: it replaces the
	// stored record only while that record's fingerprint is still this one,
	// the fingerprint the check read. A scan's batch, written outside the
	// item lock, then never overwrites a record another check stored since.
	ifFingerprint *string
}

// localSidecarContent is a matched item with present files in one library:
// one of its media files, and the folders local sidecars for it can sit in
// (those holding its files, and their observed roots).
type localSidecarContent struct {
	FolderID  int
	ContentID string
	Type      string
	File      string
	Dirs      []string
}

// localSidecarStore persists local sidecar fingerprints.
type localSidecarStore interface {
	ListScopeContents(ctx context.Context, folderID int, scopePath string) ([]localSidecarContent, error)
	ListContents(ctx context.Context, contentID string) ([]localSidecarContent, error)
	Get(ctx context.Context, folderID int, targetType string, contentIDs []string) (map[string]localSidecarRecord, error)
	Upsert(ctx context.Context, records []localSidecarRecord) error
	AdvanceFolderDigest(ctx context.Context, folderID int, contentID, fingerprint, digest string) error
	SeriesChildren(ctx context.Context, seriesID string) (map[int]string, map[episodeResultKey]string, error)
	LockItem(ctx context.Context, folderID int, contentID string) (unlock func(), locked bool, err error)
	ContentIDOfFile(ctx context.Context, folderID int, path string) (string, error)
}

// localSidecarLockSlots bounds the item locks one process holds or waits for
// at once, as a share of the pool like the chapter thumbnail locks: a lock pins
// a pooled connection while its re-apply runs on others, so the scheduled
// refresh's workers must not take a lock each.
func localSidecarLockSlots(pool *pgxpool.Pool) int {
	return min(4, max(1, int(pool.Config().MaxConns)/4))
}

// localSidecarLockWait bounds how long LockItem waits for a slot and for
// another holder of the item's lock. A merge holds it for milliseconds; a
// re-anchoring refresh can hold it much longer.
const localSidecarLockWait = 30 * time.Second

// LocalSidecarRepository stores local sidecar fingerprints in Postgres.
type LocalSidecarRepository struct {
	pool     *pgxpool.Pool
	slots    chan struct{}
	lockWait time.Duration
	// lockConnConfig, set for a pool of two connections or fewer, makes each
	// item lock take a connection of its own instead of one of the pool's.
	lockConnConfig *pgx.ConnConfig
}

// NewLocalSidecarRepository returns a repository backed by pool, or nil
// without one.
func NewLocalSidecarRepository(pool *pgxpool.Pool) *LocalSidecarRepository {
	if pool == nil {
		return nil
	}
	repo := &LocalSidecarRepository{pool: pool, slots: make(chan struct{}, localSidecarLockSlots(pool)), lockWait: localSidecarLockWait}
	if config := pool.Config(); config.MaxConns <= 2 {
		// A pinned lock connection, beside a chapter thumbnail lock's, could
		// leave such a pool nothing for the re-apply itself.
		repo.lockConnConfig = config.ConnConfig
	}
	return repo
}

const localSidecarContentColumns = `mf.media_folder_id, mf.content_id, mi.type, mf.file_path, COALESCE(mf.observed_root_path, '')`

const localSidecarContentFilter = `mf.missing_since IS NULL AND mf.extra_id IS NULL AND mi.status = 'matched'`

// ListScopeContents returns the matched items with a file under scopePath in
// a library, each with the folders of all its files in that library.
func (r *LocalSidecarRepository) ListScopeContents(ctx context.Context, folderID int, scopePath string) ([]localSidecarContent, error) {
	clauses, args := pathscope.RangeCoverageClauses("file_path", []string{scopePath}, 2)
	query := `WITH scoped AS (
			SELECT DISTINCT content_id FROM media_files
			WHERE media_folder_id = $1 AND content_id IS NOT NULL AND (` + strings.Join(clauses, " OR ") + `)
		)
		SELECT ` + localSidecarContentColumns + `
		FROM media_files mf
		JOIN scoped ON scoped.content_id = mf.content_id
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1 AND ` + localSidecarContentFilter + `
		ORDER BY mf.content_id, mf.file_path`
	return r.listContents(ctx, query, append([]any{folderID}, args...)...)
}

// ListContents returns a matched item once per library holding its files,
// with the folders of its files there.
func (r *LocalSidecarRepository) ListContents(ctx context.Context, contentID string) ([]localSidecarContent, error) {
	query := `SELECT ` + localSidecarContentColumns + `
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.content_id = $1 AND ` + localSidecarContentFilter + `
		ORDER BY mf.media_folder_id, mf.file_path`
	return r.listContents(ctx, query, contentID)
}

func (r *LocalSidecarRepository) listContents(ctx context.Context, query string, args ...any) ([]localSidecarContent, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing local sidecar contents: %w", err)
	}
	defer rows.Close()
	var out []localSidecarContent
	for rows.Next() {
		var folderID int
		var contentID, itemType, filePath, observedRoot string
		if err := rows.Scan(&folderID, &contentID, &itemType, &filePath, &observedRoot); err != nil {
			return nil, fmt.Errorf("scanning local sidecar content: %w", err)
		}
		if n := len(out); n == 0 || out[n-1].ContentID != contentID || out[n-1].FolderID != folderID {
			out = append(out, localSidecarContent{FolderID: folderID, ContentID: contentID, Type: itemType, File: filePath})
		}
		content := &out[len(out)-1]
		content.Dirs = append(content.Dirs, filepath.Dir(filePath), observedRoot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing local sidecar contents: %w", err)
	}
	for i := range out {
		out[i].Dirs = compactUniqueFilePaths(out[i].Dirs)
	}
	return out, nil
}

// Get returns a library's stored records of one target type, keyed by
// content ID.
func (r *LocalSidecarRepository) Get(ctx context.Context, folderID int, targetType string, contentIDs []string) (map[string]localSidecarRecord, error) {
	out := make(map[string]localSidecarRecord, len(contentIDs))
	if len(contentIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, item_id, fingerprint, folder_digest, provider_ids
		FROM local_sidecar_fingerprints
		WHERE content_id = ANY($1::text[]) AND target_type = $2 AND media_folder_id = $3`, contentIDs, targetType, folderID)
	if err != nil {
		return nil, fmt.Errorf("loading local sidecar fingerprints: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		record := localSidecarRecord{FolderID: folderID, TargetType: targetType}
		var providerIDs []byte
		if err := rows.Scan(&record.ContentID, &record.ItemID, &record.Fingerprint, &record.FolderDigest, &providerIDs); err != nil {
			return nil, fmt.Errorf("scanning local sidecar fingerprint: %w", err)
		}
		if err := json.Unmarshal(providerIDs, &record.ProviderIDs); err != nil {
			return nil, fmt.Errorf("decoding local sidecar provider ids: %w", err)
		}
		out[record.ContentID] = record
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("loading local sidecar fingerprints: %w", err)
	}
	return out, nil
}

// Upsert stores records, replacing any stored for the same targets. A
// baseline record replaces none, and a conditional one (ifFingerprint) only a
// record whose fingerprint still matches. A record whose item was deleted
// meanwhile is dropped.
func (r *LocalSidecarRepository) Upsert(ctx context.Context, records []localSidecarRecord) error {
	var baselines, conditional, updates []localSidecarRecord
	for _, record := range records {
		switch {
		case record.Baseline:
			baselines = append(baselines, record)
		case record.ifFingerprint != nil:
			conditional = append(conditional, record)
		default:
			updates = append(updates, record)
		}
	}
	if len(records) == 0 {
		return nil
	}
	// One transaction: a batch never lands half stored, such as a series'
	// record without its seasons' and episodes'.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storing local sidecar fingerprints: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := storeRecords(ctx, tx, baselines, `DO NOTHING`); err != nil {
		return err
	}
	if err := storeRecordsIfUnchanged(ctx, tx, conditional); err != nil {
		return err
	}
	if err := storeRecords(ctx, tx, updates, `DO UPDATE SET
			item_id = EXCLUDED.item_id,
			fingerprint = EXCLUDED.fingerprint,
			folder_digest = EXCLUDED.folder_digest,
			provider_ids = EXCLUDED.provider_ids,
			checked_at = EXCLUDED.checked_at`); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("storing local sidecar fingerprints: %w", err)
	}
	return nil
}

// AdvanceFolderDigest stores an item record's folder digest while its
// fingerprint is still fingerprint; a record another check stored since is
// left as it is.
func (r *LocalSidecarRepository) AdvanceFolderDigest(ctx context.Context, folderID int, contentID, fingerprint, digest string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE local_sidecar_fingerprints SET folder_digest = $4, checked_at = now()
		WHERE content_id = $2 AND target_type = 'item' AND media_folder_id = $1 AND fingerprint = $3`,
		folderID, contentID, fingerprint, digest)
	if err != nil {
		return fmt.Errorf("storing local sidecar folder digest: %w", err)
	}
	return nil
}

// recordColumns are records laid out as the arrays the batch statements
// unnest.
type recordColumns struct {
	folderIDs                                                                  []int32
	targetTypes, contentIDs, itemIDs, fingerprints, folderDigests, providerIDs []string
	ifFingerprints                                                             []string
}

func columnsOf(records []localSidecarRecord) (recordColumns, error) {
	n := len(records)
	c := recordColumns{
		folderIDs:   make([]int32, n),
		targetTypes: make([]string, n), contentIDs: make([]string, n), itemIDs: make([]string, n),
		fingerprints: make([]string, n), folderDigests: make([]string, n), providerIDs: make([]string, n),
		ifFingerprints: make([]string, n),
	}
	for i, record := range records {
		ids := record.ProviderIDs
		if ids == nil {
			ids = map[string]string{}
		}
		encoded, err := json.Marshal(ids)
		if err != nil {
			return recordColumns{}, fmt.Errorf("encoding local sidecar provider ids: %w", err)
		}
		c.folderIDs[i] = int32(record.FolderID) //nolint:gosec // media_folders.id is an integer column
		c.targetTypes[i] = record.TargetType
		c.contentIDs[i] = record.ContentID
		c.itemIDs[i] = record.ItemID
		c.fingerprints[i] = record.Fingerprint
		c.folderDigests[i] = record.FolderDigest
		c.providerIDs[i] = string(encoded)
		if record.ifFingerprint != nil {
			c.ifFingerprints[i] = *record.ifFingerprint
		}
	}
	return c, nil
}

// storeRecords inserts records, resolving a conflict with onConflict.
func storeRecords(ctx context.Context, tx pgx.Tx, records []localSidecarRecord, onConflict string) error {
	if len(records) == 0 {
		return nil
	}
	c, err := columnsOf(records)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO local_sidecar_fingerprints (media_folder_id, target_type, content_id, item_id, fingerprint, folder_digest, provider_ids, checked_at)
		SELECT t.media_folder_id, t.target_type, t.content_id, t.item_id, t.fingerprint, t.folder_digest, t.provider_ids::jsonb, now()
		FROM unnest($1::int[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[])
			AS t(media_folder_id, target_type, content_id, item_id, fingerprint, folder_digest, provider_ids)
		JOIN media_items mi ON mi.content_id = t.item_id
		ON CONFLICT (content_id, target_type, media_folder_id) `+onConflict,
		c.folderIDs, c.targetTypes, c.contentIDs, c.itemIDs, c.fingerprints, c.folderDigests, c.providerIDs)
	if err != nil {
		return fmt.Errorf("storing local sidecar fingerprints: %w", err)
	}
	return nil
}

// storeRecordsIfUnchanged replaces each record's stored row while that row's
// fingerprint is still the record's ifFingerprint and it still belongs to the
// record's live item. A row that changed, moved to another item (a rename
// cascades into item_id) or went away since is left as it is.
func storeRecordsIfUnchanged(ctx context.Context, tx pgx.Tx, records []localSidecarRecord) error {
	if len(records) == 0 {
		return nil
	}
	c, err := columnsOf(records)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE local_sidecar_fingerprints AS l SET
			fingerprint = t.fingerprint,
			folder_digest = t.folder_digest,
			provider_ids = t.provider_ids::jsonb,
			checked_at = now()
		FROM unnest($1::int[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[])
			AS t(media_folder_id, target_type, content_id, item_id, fingerprint, folder_digest, provider_ids, if_fingerprint)
		JOIN media_items mi ON mi.content_id = t.item_id
		WHERE l.media_folder_id = t.media_folder_id AND l.target_type = t.target_type
		  AND l.content_id = t.content_id AND l.item_id = t.item_id
		  AND l.fingerprint = t.if_fingerprint`,
		c.folderIDs, c.targetTypes, c.contentIDs, c.itemIDs, c.fingerprints, c.folderDigests, c.providerIDs, c.ifFingerprints)
	if err != nil {
		return fmt.Errorf("storing local sidecar fingerprints: %w", err)
	}
	return nil
}

// ContentIDOfFile returns the content ID a library's media file belongs to,
// or "" when the file is not cataloged or has none.
func (r *LocalSidecarRepository) ContentIDOfFile(ctx context.Context, folderID int, path string) (string, error) {
	var contentID *string
	err := r.pool.QueryRow(ctx, `
		SELECT content_id FROM media_files
		WHERE media_folder_id = $1 AND file_path = $2`, folderID, path).Scan(&contentID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("loading content id of %s: %w", path, err)
	case contentID == nil:
		return "", nil
	}
	return *contentID, nil
}

// SeriesChildren returns a series' season content IDs by season number and
// its episode content IDs by season and episode number.
func (r *LocalSidecarRepository) SeriesChildren(ctx context.Context, seriesID string) (map[int]string, map[episodeResultKey]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, season_number, NULL::int FROM seasons WHERE series_id = $1
		UNION ALL
		SELECT content_id, season_number, episode_number FROM episodes WHERE series_id = $1`, seriesID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing series children: %w", err)
	}
	defer rows.Close()
	seasons := make(map[int]string)
	episodes := make(map[episodeResultKey]string)
	for rows.Next() {
		var contentID string
		var seasonNumber int
		var episodeNumber *int
		if err := rows.Scan(&contentID, &seasonNumber, &episodeNumber); err != nil {
			return nil, nil, fmt.Errorf("scanning series child: %w", err)
		}
		if episodeNumber == nil {
			seasons[seasonNumber] = contentID
		} else {
			episodes[episodeResultKey{seasonNumber, *episodeNumber}] = contentID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("listing series children: %w", err)
	}
	return seasons, episodes, nil
}

// LockItem takes a cluster-wide lock on one item's local sidecar state in a
// library, held until unlock is called. A scan and the scheduled refresh, on
// any node, then re-apply an item and store its fingerprints one at a time, so
// a stored fingerprint never describes newer files than the item shows.
//
// It waits up to lockWait in all, for a slot, a connection and another
// holder, so an edit saved while one re-applies the item is applied right
// after it. locked is false when the wait runs out; the next check then looks
// again.
func (r *LocalSidecarRepository) LockItem(ctx context.Context, folderID int, contentID string) (func(), bool, error) {
	deadline := time.Now().Add(r.lockWait)
	waitCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	timedOut := func(err error) bool { return ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) }
	select {
	case r.slots <- struct{}{}:
	case <-waitCtx.Done():
		if timedOut(waitCtx.Err()) {
			return nil, false, nil
		}
		return nil, false, ctx.Err()
	}
	releaseSlot := func() { <-r.slots }
	key := localSidecarLockKey(folderID, contentID)

	if r.lockConnConfig == nil {
		lock, locked, err := pglock.Acquire(ctx, r.pool, key, time.Until(deadline))
		if err != nil || !locked {
			releaseSlot()
			if err != nil {
				return nil, false, fmt.Errorf("locking local sidecar state: %w", err)
			}
			return nil, false, nil
		}
		return func() {
			_ = lock.Release(ctx)
			releaseSlot()
		}, true, nil
	}

	// A small pool cannot spare a connection for the lock: the re-apply needs
	// them. The lock takes a connection of its own instead, one per process (a
	// single slot), as the chapter thumbnail locks do for one-connection pools.
	conn, err := pgx.ConnectConfig(waitCtx, r.lockConnConfig.Copy())
	if err != nil {
		releaseSlot()
		if timedOut(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("connecting for local sidecar lock: %w", err)
	}
	closeConn := func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Unlock before closing: the server drops a closed session's locks
		// only once its backend exits.
		_, _ = conn.Exec(closeCtx, `SELECT pg_advisory_unlock_all()`)
		_ = conn.Close(closeCtx)
		releaseSlot()
	}
	locked, err := pglock.LockWithin(ctx, conn, key, time.Until(deadline))
	if err != nil || !locked {
		closeConn()
		if err != nil {
			return nil, false, fmt.Errorf("locking local sidecar state: %w", err)
		}
		return nil, false, nil
	}
	return closeConn, true, nil
}

// localSidecarLockKey is the advisory lock key of one item's local sidecar
// state in a library.
func localSidecarLockKey(folderID int, contentID string) int64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "local_sidecar:%d:%s", folderID, contentID)
	return int64(h.Sum64()) //nolint:gosec // a hash; wrapping into the sign bit is fine for a lock key
}
