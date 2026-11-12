package scanner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanbatch"
)

// MovedFileCandidates returns the rows in a folder that recorded the same size
// and OSHash as the file that appeared at filePath, the rows it may have moved
// from, along with any row already at filePath. A scan whose snapshot predates
// another scan's write finds that row here and leaves it alone.
func (r *FileRepository) MovedFileCandidates(ctx context.Context, folderID int, filePath string, fileSize int64, fileHash string) ([]*scanStateFile, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+scanStateColumns+` FROM media_files
		WHERE media_folder_id = $3
		  AND (file_path = $4 OR (file_hash = $1 AND file_size = $2 AND extra_id IS NULL))`,
		fileHash, fileSize, folderID, filePath,
	)
	if err != nil {
		return nil, fmt.Errorf("querying moved file candidates: %w", err)
	}
	return scanScanStateRows(rows)
}

// movedFileFence is the WHERE clause, after "id = $1", that applies a relink
// only while the row is still the one the caller verified: same path, bytes,
// modification time, and links, and not an extra. Its arguments follow
// identityArgs' $1 to $24 and the new path as $25.
const movedFileFence = `
		  AND file_path = $26
		  AND file_hash = $27
		  AND file_size = $28
		  AND file_modified_at IS NOT DISTINCT FROM $29
		  AND content_id IS NOT DISTINCT FROM $30
		  AND episode_id IS NOT DISTINCT FROM $31
		  AND extra_id IS NULL`

// movedFileArgs returns the arguments for a relink write: identityArgs, the
// new path ($25), and the fence ($26 to $31).
func movedFileArgs(moved *scanStateFile, mf models.MediaFile) ([]any, error) {
	externalSubtitlesJSON, err := serializeJSONB(mf.ExternalSubtitles)
	if err != nil {
		return nil, fmt.Errorf("marshaling external_subtitles: %w", err)
	}
	return append(identityArgs(moved.ID, mf, externalSubtitlesJSON),
		mf.FilePath,
		moved.FilePath,
		moved.FileHash,
		moved.FileSize,
		moved.FileModifiedAt,
		nilIfEmpty(moved.ContentID),
		nilIfEmpty(moved.EpisodeID),
	), nil
}

// RelinkMovedFile moves the row of a file that left moved.FilePath to
// mf.FilePath, marks it present, and rewrites its path-derived identity and
// sidecars from mf. Probe data, chapters, markers, and every row keyed to the
// file's id stay as they are, and so do its links. Marker thumbnails stay too:
// their identities hash the path, so they are rewritten for the new one (see
// relinkedMarkerThumbnails). It reports false when the row changed meanwhile
// or another row holds the new path; the caller then treats the file as new.
func (r *FileRepository) RelinkMovedFile(ctx context.Context, moved *scanStateFile, mf models.MediaFile) (bool, error) {
	args, err := movedFileArgs(moved, mf)
	if err != nil {
		return false, err
	}
	stored, relinked, err := r.relinkedMarkerThumbnails(ctx, moved.ID, mf.FilePath)
	if errors.Is(err, ErrFileNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	args = append(args, stored, relinked)
	// A thumbnail write between the read above and this update leaves images
	// whose identities hash the old path; they are dropped, as a path change
	// dropped every one before.
	tag, err := r.pool.Exec(ctx, `UPDATE media_files SET `+identityAssignments+`,
		file_path = $25,
		missing_since = NULL,
		marker_thumbnails = CASE WHEN marker_thumbnails = $32::jsonb THEN $33::jsonb ELSE '[]'::jsonb END,
		updated_at = NOW()
	WHERE id = $1`+movedFileFence, args...)
	if err != nil {
		return false, relinkWriteError(err)
	}
	return tag.RowsAffected() == 1, nil
}

// RetitleMovedFile moves the row of a file that left moved.FilePath to
// mf.FilePath when the new path names another title. Like RelinkMovedFile it
// keeps everything derived from the file's bytes and keyed to its id, but the
// row leaves its title and is matched afresh exactly like a new file: content
// and episode links are dropped, season and episode numbers come from the new
// path alone, match suppression and the last match attempt are cleared, and
// the row takes this scan as its first sighting (created_at and
// first_seen_scan_run_id, which episode memberships date from), so the new
// title does not inherit the old one's added date. Marker images are dropped,
// since their identities hash the old path; the marker trigger drops markers
// that were not set by hand, since they belong to the title.
//
// The title the file leaves sees what it sees when a file goes missing: a
// stand-in row stays at the old path with the old links, root, group,
// numbers, and first sighting, its missing_since (now, or the earlier time the
// row was marked missing), and any held first-seen dates. Removal grace, the
// added date, and the trash sweep then treat the old title the same way as
// before rows were reused, and a rematch that finds the same title restores
// it as it was. The stand-in names the row in stand_in_for_file_id and records
// no OSHash, so it is never taken for a moved file; if the file comes back,
// ReclaimStandIn gives the row its path back.
//
// Offline downloads of the old title move to the stand-in with it: they were
// authorized for that title, so a download is never served for a file that
// now belongs to another one. The stand-in is missing, so its originals are
// refused as a missing file's are, and they are deleted with the stand-in
// after the grace, as before rows were reused. A row that had neither links
// nor such downloads leaves nothing behind.
//
// The update is fenced like RelinkMovedFile's and runs in one statement with
// the insert.
func (r *FileRepository) RetitleMovedFile(ctx context.Context, moved *scanStateFile, mf models.MediaFile) (bool, error) {
	var links titleLinks
	if runID := scanbatch.RunID(ctx); runID != "" {
		links.firstSeenScanRunID = &runID
	}
	return moveRowToTitle(ctx, r.pool, moved, mf, links)
}

// ReclaimStandIn gives a retitled row its old path back when its file returns
// there: the stand-in standInID at mf.FilePath is deleted and the row takes
// the path with the stand-in's links, numbers, edition and presentation,
// first sighting, and held first-seen dates, so the old title gets its file
// back as it was, import edition included, and the stand-in's downloads
// return to the row. If the row was linked to another title meanwhile, that
// title and its downloads get a stand-in at the row's current path, as on any
// retitle. Everything runs in one transaction and applies only while the
// stand-in still stands in for this row at that path and the row passes
// RelinkMovedFile's fence.
//
// standInGone reports that no such stand-in was left to delete (the trash
// sweep took it, or a file was written onto it), so the caller can relink the
// row the ordinary way. Otherwise a false result means the row changed under
// the fence.
func (r *FileRepository) ReclaimStandIn(ctx context.Context, standInID int, moved *scanStateFile, mf models.MediaFile) (reclaimed, standInGone bool, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, false, fmt.Errorf("beginning stand-in reclaim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var links titleLinks
	var heldItem, heldEpisode *time.Time
	var heldEpisodeRun *string
	var partIndex, partTotal, episodeStart, episodeEnd *int
	// The stand-in's downloads come back to the row before the delete would
	// cascade them away; nothing commits unless the stand-in is deleted and
	// the row moves.
	if _, err := tx.Exec(ctx, `
		UPDATE downloads SET media_file_id = $2, updated_at = NOW()
		WHERE media_file_id = $1`, standInID, moved.ID); err != nil {
		return false, false, fmt.Errorf("returning stand-in downloads: %w", err)
	}
	err = tx.QueryRow(ctx, `
		DELETE FROM media_files
		WHERE id = $1 AND stand_in_for_file_id = $2 AND file_path = $3 AND missing_since IS NOT NULL
		RETURNING content_id, episode_id, season_number, episode_number, created_at, first_seen_scan_run_id,
			held_item_first_seen_at, held_episode_first_seen_at, held_episode_first_seen_scan_run_id,
			edition_raw, edition_key, edition_confidence, edition_source,
			presentation_kind, presentation_group_key, presentation_part_index, presentation_part_total,
			multi_episode_start, multi_episode_end`,
		standInID, moved.ID, mf.FilePath,
	).Scan(&links.contentID, &links.episodeID, &links.seasonNumber, &links.episodeNumber, &links.createdAt,
		&links.firstSeenScanRunID, &heldItem, &heldEpisode, &heldEpisodeRun,
		&mf.EditionRaw, &mf.EditionKey, &mf.EditionConfidence, &mf.EditionSource,
		&mf.PresentationKind, &mf.PresentationGroupKey, &partIndex, &partTotal, &episodeStart, &episodeEnd)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, true, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("removing stand-in: %w", err)
	}
	mf.PresentationPartIndex, mf.PresentationPartTotal = derefInt(partIndex), derefInt(partTotal)
	mf.MultiEpisodeStart, mf.MultiEpisodeEnd = derefInt(episodeStart), derefInt(episodeEnd)
	relinked, err := moveRowToTitle(ctx, tx, moved, mf, links)
	if err != nil || !relinked {
		return false, false, err
	}
	// The relink trigger drops held dates when the links change, so they are
	// restored once the row carries the stand-in's links.
	if heldItem != nil || heldEpisode != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE media_files
			SET held_item_first_seen_at = $2, held_episode_first_seen_at = $3, held_episode_first_seen_scan_run_id = $4
			WHERE id = $1`, moved.ID, heldItem, heldEpisode, heldEpisodeRun); err != nil {
			return false, false, fmt.Errorf("restoring held first-seen dates: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, false, relinkWriteError(err)
	}
	return true, false, nil
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// titleLinks are the links and first sighting a row takes when it moves to
// another title. Nil links leave the row unlinked, nil numbers take the new
// path's, and a nil created_at means now.
type titleLinks struct {
	contentID, episodeID        *string
	seasonNumber, episodeNumber *int
	createdAt                   *time.Time
	firstSeenScanRunID          *string
}

// moveRowToTitle is the fenced statement behind RetitleMovedFile and
// ReclaimStandIn: it moves the row to mf.FilePath with links, and leaves a
// stand-in holding the links it had at its old path unless it had none or
// keeps the same ones.
func moveRowToTitle(ctx context.Context, q fileQueryer, moved *scanStateFile, mf models.MediaFile, links titleLinks) (bool, error) {
	args, err := movedFileArgs(moved, mf)
	if err != nil {
		return false, err
	}
	args = append(args, links.contentID, links.episodeID, links.seasonNumber, links.episodeNumber, links.createdAt, links.firstSeenScanRunID)
	var count int
	err = q.QueryRow(ctx, `
	WITH previous AS (
		SELECT * FROM media_files WHERE id = $1`+movedFileFence+`
		FOR UPDATE
	),
	moved AS (
		UPDATE media_files SET `+identityGroupColumns+`
			season_number = COALESCE($34::int, $12::int),
			episode_number = COALESCE($35::int, $13::int),
			`+identityVariantColumns+`,
			file_path = $25,
			missing_since = NULL,
			content_id = $32::text,
			episode_id = $33::text,
			created_at = COALESCE($36::timestamptz, NOW()),
			first_seen_scan_run_id = $37::text,
			marker_thumbnails = '[]'::jsonb,
			match_suppressed_at = NULL,
			match_attempted_at = NULL,
			stand_in_for_file_id = NULL,
			updated_at = NOW()
		WHERE id = (SELECT id FROM previous)
		RETURNING id
	),
	left_behind AS (
		INSERT INTO media_files (
			media_folder_id, file_path, file_size, file_modified_at,
			content_id, episode_id, season_number, episode_number,
			canonical_root_path, observed_root_path, content_group_key, group_key_version,
			base_title, base_year, base_type, identity_confidence, identity_json,
			edition_raw, edition_key, edition_confidence, edition_source,
			presentation_kind, presentation_group_key, presentation_part_index, presentation_part_total,
			multi_episode_start, multi_episode_end,
			created_at, first_seen_scan_run_id, missing_since, stand_in_for_file_id,
			held_item_first_seen_at, held_episode_first_seen_at, held_episode_first_seen_scan_run_id
		)
		SELECT
			p.media_folder_id, p.file_path, p.file_size, p.file_modified_at,
			p.content_id, p.episode_id, p.season_number, p.episode_number,
			p.canonical_root_path, p.observed_root_path, p.content_group_key, p.group_key_version,
			p.base_title, p.base_year, p.base_type, p.identity_confidence, p.identity_json,
			p.edition_raw, p.edition_key, p.edition_confidence, p.edition_source,
			p.presentation_kind, p.presentation_group_key, p.presentation_part_index, p.presentation_part_total,
			p.multi_episode_start, p.multi_episode_end,
			p.created_at, p.first_seen_scan_run_id, COALESCE(p.missing_since, NOW()), p.id,
			p.held_item_first_seen_at, p.held_episode_first_seen_at, p.held_episode_first_seen_scan_run_id
		FROM previous p
		WHERE EXISTS (SELECT 1 FROM moved)
		  AND (p.content_id, p.episode_id) IS DISTINCT FROM ($32::text, $33::text)
		  AND (p.content_id IS NOT NULL OR p.episode_id IS NOT NULL OR EXISTS (
			SELECT 1 FROM downloads d
			WHERE d.media_file_id = p.id AND (d.content_id, d.episode_id) IS DISTINCT FROM ($32::text, $33::text)
		  ))
		RETURNING id
	),
	-- A download was authorized for the title it names, so it stays with that
	-- title: downloads of another title than the row's new one move to the
	-- stand-in and share its fate, as they did with the missing row before
	-- rows were reused.
	parked_downloads AS (
		UPDATE downloads d SET media_file_id = (SELECT id FROM left_behind), updated_at = NOW()
		WHERE d.media_file_id = $1
		  AND EXISTS (SELECT 1 FROM left_behind)
		  AND (d.content_id, d.episode_id) IS DISTINCT FROM ($32::text, $33::text)
		RETURNING d.id
	)
	SELECT count(*)::int FROM moved`, args...).Scan(&count)
	if err != nil {
		return false, relinkWriteError(err)
	}
	return count == 1, nil
}

// StandInFor returns the row a missing stand-in at fileID stands in for, or 0
// when fileID is no stand-in. exists is false when no row has fileID any more.
func (r *FileRepository) StandInFor(ctx context.Context, fileID int) (standInFor int, exists bool, err error) {
	var id *int
	var missing bool
	err = r.pool.QueryRow(ctx, `
		SELECT stand_in_for_file_id, missing_since IS NOT NULL FROM media_files WHERE id = $1`, fileID,
	).Scan(&id, &missing)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("reading stand-in: %w", err)
	}
	if id == nil || !missing {
		return 0, true, nil
	}
	return *id, true, nil
}

// ItemIdentity returns the identity provider IDs matching compares a file's
// tags with for a catalog item (its stored provider ID rows, with its TMDB,
// TVDB and IMDb columns filling any it lacks), whether the item is matched,
// and whether it exists.
func (r *FileRepository) ItemIdentity(ctx context.Context, contentID string) (ids map[string]string, matched, found bool, err error) {
	var tmdb, imdb, tvdb, status string
	var rows map[string]string
	err = r.pool.QueryRow(ctx, `
		SELECT mi.tmdb_id, mi.imdb_id, mi.tvdb_id, mi.status,
			COALESCE((
				SELECT jsonb_object_agg(lower(p.provider), p.provider_id)
				FROM media_item_provider_ids p
				WHERE p.content_id = mi.content_id
				  AND lower(p.provider) IN ('tmdb', 'tvdb', 'imdb')
				  AND btrim(p.provider_id) <> ''
			), '{}'::jsonb)
		FROM media_items mi WHERE mi.content_id = $1`, contentID).Scan(&tmdb, &imdb, &tvdb, &status, &rows)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, fmt.Errorf("reading item identity: %w", err)
	}
	ids = map[string]string{contentid.ProviderTMDB: tmdb, contentid.ProviderIMDB: imdb, contentid.ProviderTVDB: tvdb}
	for provider, value := range rows {
		ids[provider] = value
	}
	return ids, strings.EqualFold(strings.TrimSpace(status), "matched"), true, nil
}

// ClaimedContentID returns the matched title that confirmed ownership gives a
// file with this content group or content root, the first two steps of
// matching: the group claim, then the root claim. It returns "" when neither
// names a matched title.
func (r *FileRepository) ClaimedContentID(ctx context.Context, folderID, groupKeyVersion int, contentGroupKey, canonicalRootPath string) (string, error) {
	var contentID string
	err := r.pool.QueryRow(ctx, `
		SELECT claim.content_id FROM (
			SELECT 1 AS rank, mig.content_id
			FROM media_item_groups mig
			WHERE mig.media_folder_id = $1 AND mig.group_key_version = $2 AND mig.content_group_key = $3 AND $3 <> ''
			UNION ALL
			SELECT 2, mir.content_id
			FROM media_item_roots mir
			WHERE mir.media_folder_id = $1 AND mir.canonical_root_path = $4 AND $4 <> ''
		) claim
		JOIN media_items mi ON mi.content_id = claim.content_id AND lower(btrim(mi.status)) = 'matched'
		ORDER BY claim.rank
		LIMIT 1`, folderID, groupKeyVersion, contentGroupKey, canonicalRootPath).Scan(&contentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading content claims: %w", err)
	}
	return contentID, nil
}

// relinkWriteError reports a relink that lost the new path to another row as
// no relink, and wraps anything else.
func relinkWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil
	}
	return fmt.Errorf("relinking moved media file: %w", err)
}

// relinkedMarkerThumbnails returns a row's stored marker thumbnail state and
// that state for the row at newPath. An image's identity hashes the file's
// identity, its path, and the marker range, so after a rename every stored
// image would read as stale although the frames are unchanged. Each image of a
// current marker range gets the identity it has under the new path; the
// uploaded object keeps its key. Images no current range explains (ranges an
// on-demand provider lookup knew, or leftovers) are dropped, and their objects
// go to blob GC through the trigger. The stored value is read first so the
// caller's update can fence on it.
func (r *FileRepository) relinkedMarkerThumbnails(ctx context.Context, fileID int, newPath string) (stored, relinked []byte, err error) {
	err = r.pool.QueryRow(ctx, `SELECT marker_thumbnails FROM media_files WHERE id = $1`, fileID).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrFileNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading marker thumbnails: %w", err)
	}
	if string(stored) == "[]" {
		return stored, stored, nil
	}
	file, err := r.GetByID(ctx, fileID)
	if err != nil {
		return nil, nil, err
	}
	relinked, err = markerThumbnailStateJSON(markerThumbnailsAtPath(file, newPath))
	if err != nil {
		return nil, nil, err
	}
	return stored, relinked, nil
}

// markerThumbnailsAtPath returns file's marker thumbnails with the identities
// they have once the file sits at newPath, keeping only images of the file's
// current marker ranges.
func markerThumbnailsAtPath(file *models.MediaFile, newPath string) []models.MarkerThumbnail {
	moved := *file
	moved.FilePath = newPath
	next := make(map[string]string)
	for _, segment := range models.EffectiveMarkerSegments(file) {
		next[models.MarkerThumbnailIdentity(file, segment)] = models.MarkerThumbnailIdentity(&moved, segment)
	}
	out := make([]models.MarkerThumbnail, 0, len(file.MarkerThumbnails))
	for _, thumbnail := range file.MarkerThumbnails {
		if identity, ok := next[thumbnail.Identity]; ok {
			thumbnail.Identity = identity
			out = append(out, thumbnail)
		}
	}
	return out
}
