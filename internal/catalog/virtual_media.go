package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidVirtualMedia = errors.New("invalid virtual media")

const (
	maxVirtualURIBytes             = 1024
	maxVirtualQueryValueBytes      = 256
	maxVirtualSourceBytes          = 128
	maxVirtualTitleBytes           = 512
	maxVirtualOverviewBytes        = 32 << 10
	maxVirtualArtworkPathBytes     = 2048
	maxVirtualVariantLabelBytes    = 256
	maxVirtualAttributeBytes       = 128
	maxVirtualIdentifierBytes      = 128
	maxVirtualGenres               = 64
	maxVirtualGenreBytes           = 128
	maxVirtualEpisodes             = 4096
	maxVirtualVariantsPerMedia     = 50
	maxVirtualFilesPerRegistration = 4096
	maxVirtualLanguages            = 32
	maxVirtualLanguageBytes        = 35
)

var (
	virtualPathSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	imdbIDPattern             = regexp.MustCompile(`^tt[0-9]{1,16}$`)
	numericProviderIDPattern  = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
)

type VirtualMediaVariant struct {
	VirtualURI        string
	Label             string
	Resolution        string
	CodecVideo        string
	CodecAudio        string
	HDR               string
	Bitrate           int
	RuntimeMinutes    int
	FileSize          int64
	Container         string
	SourceType        string
	AudioLanguages    []string
	SubtitleLanguages []string
	Availability      string
}

type VirtualEpisode struct {
	SeasonNumber, EpisodeNumber            int
	Title, Overview, StillPath, VirtualURI string
	AirDate                                time.Time
	RuntimeMinutes                         int
	Variants                               []VirtualMediaVariant
}

type VirtualMedia struct {
	LibraryID, MediaType, Title          string
	Year                                 int
	IMDbID, TMDBID, TVDBID, Overview     string
	Genres                               []string
	PosterPath, BackdropPath, VirtualURI string
	RuntimeMinutes                       int
	Source                               string
	Episodes                             []VirtualEpisode
	Variants                             []VirtualMediaVariant
}

type VirtualMediaResult struct {
	MediaID          string
	LibraryID        string
	EpisodesUpserted int
}

type VirtualReconcileResult struct {
	ItemsRemoved int
	FilesRemoved int
}

// VirtualMediaRegistrar owns transactional catalog registration for virtual
// sources. Plugins submit stable external identifiers and URIs; they never
// receive database access or depend on Silo's table layout.
type VirtualMediaRegistrar struct{ pool *pgxpool.Pool }

func NewVirtualMediaRegistrar(pool *pgxpool.Pool) *VirtualMediaRegistrar {
	return &VirtualMediaRegistrar{pool: pool}
}

func (r *VirtualMediaRegistrar) Upsert(ctx context.Context, in VirtualMedia) (*VirtualMediaResult, error) {
	return r.UpsertVirtualMedia(ctx, 0, in)
}

// UpsertVirtualMedia is the plugin-host entry point. The installation ID is
// persisted as ownership metadata so reconciliation cannot remove another
// plugin's virtual catalog entries.
func (r *VirtualMediaRegistrar) UpsertVirtualMedia(ctx context.Context, installationID int, in VirtualMedia) (*VirtualMediaResult, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("virtual catalog is unavailable")
	}
	if in.MediaType == "series" {
		in = r.normalizeSeriesVirtualMedia(ctx, in)
	}
	if err := validateVirtualMedia(in); err != nil {
		return nil, err
	}
	folderID, _ := strconv.Atoi(in.LibraryID)
	source := normalizedVirtualSource(in.Source)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin virtual media transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockVirtualMediaSource(ctx, tx, installationID, source); err != nil {
		return nil, err
	}
	var folderType string
	if err := tx.QueryRow(ctx, `SELECT type FROM media_folders WHERE id=$1 AND enabled IS NOT FALSE`, folderID).Scan(&folderType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: destination library does not exist or is disabled", ErrInvalidVirtualMedia)
		}
		return nil, fmt.Errorf("load destination library: %w", err)
	}
	if !virtualLibraryCompatible(folderType, in.MediaType) {
		return nil, fmt.Errorf("%w: %s cannot be added to %s library", ErrInvalidVirtualMedia, in.MediaType, folderType)
	}
	contentID := virtualContentID(in)
	ownsItemMetadata, err := upsertVirtualMediaItem(ctx, tx, installationID, source, contentID, in)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, contentID, folderID); err != nil {
		return nil, fmt.Errorf("link virtual media: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO virtual_media_source_claims(
			plugin_installation_id, source_key, content_id, media_folder_id, owns_item_metadata, last_seen_at
		) VALUES($1,$2,$3,$4,$5,NOW())
		ON CONFLICT(plugin_installation_id,source_key,content_id,media_folder_id) DO UPDATE SET
			owns_item_metadata=EXCLUDED.owns_item_metadata,
			last_seen_at=NOW(),
			updated_at=NOW()`, installationID, source, contentID, folderID, ownsItemMetadata); err != nil {
		return nil, fmt.Errorf("claim virtual media source: %w", err)
	}

	filePaths := make([]string, 0, virtualMediaFileCount(in))
	episodes := 0
	if in.MediaType == "series" {
		for _, ep := range in.Episodes {
			if err := upsertVirtualEpisode(ctx, tx, contentID, folderID, installationID, ownsItemMetadata, ep); err != nil {
				return nil, err
			}
			if len(ep.Variants) > 0 {
				for _, variant := range ep.Variants {
					filePaths = append(filePaths, variant.VirtualURI)
				}
			} else {
				filePaths = append(filePaths, ep.VirtualURI)
			}
			episodes++
		}
	} else {
		if len(in.Variants) > 0 {
			for _, v := range in.Variants {
				if err := upsertVirtualFileVariant(ctx, tx, contentID, "", folderID, installationID, v); err != nil {
					return nil, err
				}
				filePaths = append(filePaths, v.VirtualURI)
			}
		} else {
			if err := upsertVirtualFile(ctx, tx, contentID, "", folderID, installationID, in.VirtualURI, runtimeSeconds(in.RuntimeMinutes)); err != nil {
				return nil, err
			}
			filePaths = append(filePaths, in.VirtualURI)
		}
	}
	if err := syncVirtualFileSourceClaims(ctx, tx, installationID, source, contentID, folderID, filePaths); err != nil {
		return nil, err
	}
	if err := EnqueueSearchIndexUpsert(ctx, tx, contentID); err != nil {
		return nil, fmt.Errorf("enqueue virtual media search update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit virtual media transaction: %w", err)
	}
	return &VirtualMediaResult{MediaID: contentID, LibraryID: in.LibraryID, EpisodesUpserted: episodes}, nil
}

func upsertVirtualMediaItem(ctx context.Context, tx pgx.Tx, installationID int, source, contentID string, in VirtualMedia) (bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, contentID); err != nil {
		return false, fmt.Errorf("lock virtual media identity: %w", err)
	}
	status := "unmatched"
	if in.Overview != "" || in.PosterPath != "" {
		status = "matched"
	}
	var insertedID string
	err := tx.QueryRow(ctx, `
		INSERT INTO media_items(
			content_id,type,title,year,imdb_id,tmdb_id,tvdb_id,overview,genres,poster_path,backdrop_path,
			runtime,matched_at,status,virtual_owner_installation_id,virtual_source,virtual_last_seen_at
		) VALUES(
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
			CASE WHEN $13='matched' THEN NOW() END,$13,NULLIF($14,0),$15,NOW()
		)
		ON CONFLICT(content_id) DO NOTHING
		RETURNING content_id`,
		contentID, in.MediaType, in.Title, in.Year, in.IMDbID, in.TMDBID, in.TVDBID,
		in.Overview, in.Genres, in.PosterPath, in.BackdropPath, in.RuntimeMinutes, status, installationID, source,
	).Scan(&insertedID)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("insert virtual media: %w", err)
	}

	var (
		existingType       string
		existingOwner      *int64
		existingSource     string
		hasOwnClaim        bool
		hasOtherOwnerClaim bool
		hasPhysicalFile    bool
	)
	if err := tx.QueryRow(ctx, `
		SELECT mi.type, mi.virtual_owner_installation_id, mi.virtual_source,
			EXISTS(
				SELECT 1 FROM virtual_media_source_claims vmsc
				WHERE vmsc.content_id=mi.content_id
				  AND vmsc.plugin_installation_id=$2
				  AND vmsc.source_key=$3
			),
			EXISTS(
				SELECT 1 FROM virtual_media_source_claims vmsc
				WHERE vmsc.content_id=mi.content_id
				  AND vmsc.owns_item_metadata
				  AND (vmsc.plugin_installation_id<>$2 OR vmsc.source_key<>$3)
			),
			EXISTS(
				SELECT 1 FROM media_files mf
				WHERE mf.content_id=mi.content_id
				  AND mf.container<>'virtual'
				  AND mf.file_path NOT LIKE 'virtual://%'
			)
		FROM media_items mi
		WHERE mi.content_id=$1
		FOR UPDATE`, contentID, installationID, source).Scan(
		&existingType, &existingOwner, &existingSource, &hasOwnClaim, &hasOtherOwnerClaim, &hasPhysicalFile,
	); err != nil {
		return false, fmt.Errorf("load existing virtual media identity: %w", err)
	}
	if existingType != in.MediaType {
		return false, fmt.Errorf("%w: canonical content ID already belongs to media type %q", ErrInvalidVirtualMedia, existingType)
	}

	if hasPhysicalFile {
		if _, err := tx.Exec(ctx, `
			UPDATE virtual_media_source_claims
			SET owns_item_metadata=false,updated_at=NOW()
			WHERE content_id=$1 AND owns_item_metadata`, contentID); err != nil {
			return false, fmt.Errorf("release virtual metadata ownership to local media: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE media_items
			SET virtual_owner_installation_id=NULL,virtual_source='',virtual_last_seen_at=NULL,updated_at=NOW()
			WHERE content_id=$1`, contentID); err != nil {
			return false, fmt.Errorf("clear virtual metadata ownership from local media: %w", err)
		}
		return false, nil
	}

	currentScalarOwner := existingOwner != nil && int(*existingOwner) == installationID && existingSource == source
	if installationID == 0 {
		currentScalarOwner = existingOwner == nil && existingSource == source && hasOwnClaim
	}
	ownsItemMetadata := currentScalarOwner || (existingOwner == nil && !hasOtherOwnerClaim)
	if !ownsItemMetadata {
		// Existing local items and items registered by another plugin remain the
		// authoritative catalog record. This registration owns only its virtual
		// source association and files, preserving local+virtual coexistence.
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE media_items SET
			title=$2,
			year=CASE WHEN $3>0 THEN $3 ELSE year END,
			imdb_id=CASE WHEN $4<>'' THEN $4 ELSE imdb_id END,
			tmdb_id=CASE WHEN $5<>'' THEN $5 ELSE tmdb_id END,
			tvdb_id=CASE WHEN $6<>'' THEN $6 ELSE tvdb_id END,
			overview=CASE WHEN $7<>'' THEN $7 ELSE overview END,
			genres=CASE WHEN cardinality($8::text[])>0 THEN $8 ELSE genres END,
			poster_path=CASE WHEN $9<>'' THEN $9 ELSE poster_path END,
			backdrop_path=CASE WHEN $10<>'' THEN $10 ELSE backdrop_path END,
			runtime=CASE WHEN $11>0 THEN $11 ELSE runtime END,
			matched_at=CASE WHEN $12='matched' THEN COALESCE(matched_at,NOW()) ELSE matched_at END,
			status=CASE WHEN $12='matched' THEN 'matched' ELSE status END,
			virtual_owner_installation_id=NULLIF($13,0),
			virtual_source=$14,
			virtual_last_seen_at=NOW(),
			updated_at=NOW()
		WHERE content_id=$1`,
		contentID, in.Title, in.Year, in.IMDbID, in.TMDBID, in.TVDBID, in.Overview,
		in.Genres, in.PosterPath, in.BackdropPath, in.RuntimeMinutes, status, installationID, source,
	); err != nil {
		return false, fmt.Errorf("update owned virtual media: %w", err)
	}
	return true, nil
}

func virtualMediaFileCount(in VirtualMedia) int {
	if in.MediaType == "movie" {
		if len(in.Variants) > 0 {
			return len(in.Variants)
		}
		return 1
	}
	total := 0
	for _, episode := range in.Episodes {
		if len(episode.Variants) > 0 {
			total += len(episode.Variants)
		} else {
			total++
		}
	}
	return total
}

func syncVirtualFileSourceClaims(ctx context.Context, tx pgx.Tx, installationID int, source, contentID string, folderID int, filePaths []string) error {
	if filePaths == nil {
		filePaths = []string{}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO virtual_media_file_source_claims(
			plugin_installation_id,source_key,content_id,media_folder_id,file_path,last_seen_at
		)
		SELECT $1,$2,$3,$4,path,NOW()
		FROM unnest($5::text[]) AS path
		ON CONFLICT(plugin_installation_id,source_key,content_id,media_folder_id,file_path) DO UPDATE SET
			last_seen_at=NOW(),
			updated_at=NOW()`, installationID, source, contentID, folderID, filePaths); err != nil {
		return fmt.Errorf("claim virtual media files: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		WITH stale AS (
			DELETE FROM virtual_media_file_source_claims
			WHERE plugin_installation_id=$1
			  AND source_key=$2
			  AND content_id=$3
			  AND media_folder_id=$4
			  AND NOT (file_path=ANY($5::text[]))
			RETURNING file_path
		)
		DELETE FROM media_files mf
		USING stale
		WHERE mf.content_id=$3
		  AND mf.media_folder_id=$4
		  AND mf.file_path=stale.file_path
		  AND mf.virtual_owner_installation_id=$1
		  AND NOT EXISTS(
			SELECT 1 FROM virtual_media_file_source_claims vmfsc
			WHERE vmfsc.plugin_installation_id=$1
			  AND vmfsc.content_id=$3
			  AND vmfsc.media_folder_id=$4
			  AND vmfsc.file_path=stale.file_path
			  AND vmfsc.source_key<>$2
		  )`, installationID, source, contentID, folderID, filePaths); err != nil {
		return fmt.Errorf("remove stale virtual media files: %w", err)
	}
	return nil
}

// ReconcileVirtualMedia removes stale virtual media owned by one plugin source.
// Physical files and collection-linked items are preserved.
func (r *VirtualMediaRegistrar) ReconcileVirtualMedia(ctx context.Context, installationID int, source string, keepIDs []string, libraryIDs []int) (VirtualReconcileResult, error) {
	var result VirtualReconcileResult
	if r == nil || r.pool == nil {
		return result, errors.New("virtual catalog is unavailable")
	}
	if installationID <= 0 || source == "" {
		return result, errors.New("installation and source are required")
	}
	if err := validateVirtualText("source", source, maxVirtualSourceBytes, true, false); err != nil {
		return result, err
	}
	// pgx encodes a nil slice as SQL NULL. `x <> ALL(NULL)`/`NOT (x =
	// ANY(NULL))` is NULL, not true, which would make reconciliation silently
	// retain every stale row when a plugin omits keep_media_ids or libraries.
	// Pass concrete empty arrays so the predicates have the intended semantics.
	keepIDs = normalizeVirtualKeepIDs(keepIDs)
	libraryIDs = normalizeVirtualLibraryIDs(libraryIDs)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin virtual reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockVirtualMediaSource(ctx, tx, installationID, source); err != nil {
		return result, err
	}
	// Safety guard: an empty keep list with existing claims means the plugin
	// lost its monitored state (restart, file corruption, provider outage).
	// Wiping everything would remove all virtual URIs for users, so refuse.
	if len(keepIDs) == 0 {
		var existingCount int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM virtual_media_source_claims
			WHERE plugin_installation_id=$1 AND source_key=$2`,
			installationID, source).Scan(&existingCount); err != nil {
			return result, fmt.Errorf("virtual reconciliation guard check: %w", err)
		}
		if existingCount > 0 {
			return result, fmt.Errorf("virtual reconciliation refused: plugin sent empty keep list but %d existing claims exist for source %q — the plugin may have lost its monitored state", existingCount, source)
		}
	}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE stale_virtual_source_claims ON COMMIT DROP AS
		SELECT plugin_installation_id,source_key,content_id,media_folder_id
		FROM virtual_media_source_claims
		WHERE plugin_installation_id=$1
		  AND source_key=$2
		  AND NOT (content_id=ANY($3::text[]))
		  AND (cardinality($4::int[])=0 OR media_folder_id=ANY($4::int[]))`,
		installationID, source, keepIDs, libraryIDs); err != nil {
		return result, fmt.Errorf("identify stale virtual source claims: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE stale_virtual_file_claims ON COMMIT DROP AS
		SELECT vmfsc.plugin_installation_id,vmfsc.content_id,vmfsc.media_folder_id,vmfsc.file_path
		FROM virtual_media_file_source_claims vmfsc
		JOIN stale_virtual_source_claims stale
		  ON stale.plugin_installation_id=vmfsc.plugin_installation_id
		 AND stale.source_key=vmfsc.source_key
		 AND stale.content_id=vmfsc.content_id
		 AND stale.media_folder_id=vmfsc.media_folder_id`); err != nil {
		return result, fmt.Errorf("identify stale virtual file claims: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM virtual_media_source_claims vmsc
		USING stale_virtual_source_claims stale
		WHERE vmsc.plugin_installation_id=stale.plugin_installation_id
		  AND vmsc.source_key=stale.source_key
		  AND vmsc.content_id=stale.content_id
		  AND vmsc.media_folder_id=stale.media_folder_id`); err != nil {
		return result, fmt.Errorf("delete stale virtual source claims: %w", err)
	}
	fileTag, err := tx.Exec(ctx, `
		DELETE FROM media_files mf
		USING stale_virtual_file_claims stale
		WHERE mf.content_id=stale.content_id
		  AND mf.media_folder_id=stale.media_folder_id
		  AND mf.file_path=stale.file_path
		  AND mf.virtual_owner_installation_id=stale.plugin_installation_id
		  AND NOT EXISTS(
			SELECT 1 FROM virtual_media_file_source_claims remaining
			WHERE remaining.plugin_installation_id=stale.plugin_installation_id
			  AND remaining.content_id=stale.content_id
			  AND remaining.media_folder_id=stale.media_folder_id
			  AND remaining.file_path=stale.file_path
		  )`)
	if err != nil {
		return result, fmt.Errorf("delete unclaimed virtual files: %w", err)
	}
	result.FilesRemoved = int(fileTag.RowsAffected())
	// Re-elect exactly one remaining metadata owner for each virtual-only item.
	// Preserve the current owner when it survives; otherwise promote the newest
	// source deterministically. Physical media always remains authoritative.
	if _, err := tx.Exec(ctx, `
		WITH chosen AS (
			SELECT DISTINCT ON (remaining.content_id)
				remaining.plugin_installation_id,remaining.source_key,
				remaining.content_id,remaining.media_folder_id
			FROM virtual_media_source_claims remaining
			WHERE remaining.content_id IN(SELECT content_id FROM stale_virtual_source_claims)
			  AND NOT EXISTS (
				SELECT 1 FROM media_files physical
				WHERE physical.content_id=remaining.content_id
				  AND physical.container<>'virtual'
				  AND physical.file_path NOT LIKE 'virtual://%'
			  )
			ORDER BY remaining.content_id,remaining.owns_item_metadata DESC,
			         remaining.last_seen_at DESC,
			         remaining.plugin_installation_id,remaining.source_key,remaining.media_folder_id
		)
		UPDATE virtual_media_source_claims remaining
		SET owns_item_metadata=(
			chosen.plugin_installation_id IS NOT NULL
			AND remaining.plugin_installation_id=chosen.plugin_installation_id
			AND remaining.source_key=chosen.source_key
			AND remaining.media_folder_id=chosen.media_folder_id
		),updated_at=NOW()
		FROM (SELECT DISTINCT content_id FROM stale_virtual_source_claims) affected
		LEFT JOIN chosen ON chosen.content_id=affected.content_id
		WHERE remaining.content_id=affected.content_id`); err != nil {
		return result, fmt.Errorf("promote remaining virtual metadata ownership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE media_items mi SET
			virtual_owner_installation_id=owner.plugin_installation_id,
			virtual_source=COALESCE(owner.source_key,''),
			virtual_last_seen_at=(
				SELECT MAX(remaining.last_seen_at)
				FROM virtual_media_source_claims remaining
				WHERE remaining.content_id=mi.content_id
			),
			updated_at=NOW()
		FROM (
			SELECT affected.content_id,
			       chosen.plugin_installation_id,chosen.source_key
			FROM (SELECT DISTINCT content_id FROM stale_virtual_source_claims) affected
			LEFT JOIN LATERAL (
				SELECT remaining.plugin_installation_id,remaining.source_key
				FROM virtual_media_source_claims remaining
				WHERE remaining.content_id=affected.content_id
				  AND remaining.owns_item_metadata
				ORDER BY remaining.last_seen_at DESC,remaining.plugin_installation_id,remaining.source_key
				LIMIT 1
			) chosen ON true
		) owner
		WHERE mi.content_id=owner.content_id`); err != nil {
		return result, fmt.Errorf("refresh compatibility virtual ownership: %w", err)
	}
	rows, err := tx.Query(ctx, `
		DELETE FROM media_items mi
		WHERE mi.content_id IN(SELECT content_id FROM stale_virtual_source_claims)
		  AND NOT EXISTS (SELECT 1 FROM media_files mf WHERE mf.content_id = mi.content_id)
		  AND NOT EXISTS (SELECT 1 FROM library_collection_items lci WHERE lci.media_item_id = mi.content_id)
		  AND NOT EXISTS (SELECT 1 FROM virtual_media_source_claims vmsc WHERE vmsc.content_id=mi.content_id)
		RETURNING mi.content_id`)
	if err != nil {
		return result, fmt.Errorf("delete stale virtual media: %w", err)
	}
	deletedIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return result, fmt.Errorf("collect stale virtual media IDs: %w", err)
	}
	if err := EnqueueSearchIndexDeletes(ctx, tx, deletedIDs); err != nil {
		return result, fmt.Errorf("enqueue stale virtual media search deletes: %w", err)
	}
	result.ItemsRemoved = len(deletedIDs)
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit virtual reconciliation: %w", err)
	}
	return result, nil
}

func lockVirtualMediaSource(ctx context.Context, tx pgx.Tx, installationID int, source string) error {
	lockKey := fmt.Sprintf("silo:virtual-media-source:%d:%s", installationID, source)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return fmt.Errorf("lock virtual media source: %w", err)
	}
	return nil
}

func normalizeVirtualKeepIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

func normalizeVirtualLibraryIDs(ids []int) []int {
	if ids == nil {
		return []int{}
	}
	return ids
}

func validateVirtualMedia(in VirtualMedia) error {
	if in.MediaType != "movie" && in.MediaType != "series" {
		return fmt.Errorf("%w: media_type must be movie or series", ErrInvalidVirtualMedia)
	}
	if err := validateVirtualText("title", in.Title, maxVirtualTitleBytes, true, false); err != nil {
		return err
	}
	folderID, err := strconv.Atoi(in.LibraryID)
	if err != nil || folderID <= 0 || strconv.Itoa(folderID) != in.LibraryID {
		return fmt.Errorf("%w: library_id must be a canonical positive integer", ErrInvalidVirtualMedia)
	}
	if in.Source != "" && strings.TrimSpace(in.Source) != in.Source {
		return fmt.Errorf("%w: source must not have surrounding whitespace", ErrInvalidVirtualMedia)
	}
	if err := validateVirtualText("source", normalizedVirtualSource(in.Source), maxVirtualSourceBytes, true, false); err != nil {
		return err
	}
	if in.Year < 0 || in.Year > 9999 {
		return fmt.Errorf("%w: year is out of range", ErrInvalidVirtualMedia)
	}
	if err := validateVirtualText("overview", in.Overview, maxVirtualOverviewBytes, false, true); err != nil {
		return err
	}
	if err := validateVirtualText("poster_path", in.PosterPath, maxVirtualArtworkPathBytes, false, false); err != nil {
		return err
	}
	if err := validateVirtualText("backdrop_path", in.BackdropPath, maxVirtualArtworkPathBytes, false, false); err != nil {
		return err
	}
	if len(in.Genres) > maxVirtualGenres {
		return fmt.Errorf("%w: genres exceeds %d entries", ErrInvalidVirtualMedia, maxVirtualGenres)
	}
	for i, genre := range in.Genres {
		if err := validateVirtualText(fmt.Sprintf("genres[%d]", i), genre, maxVirtualGenreBytes, true, false); err != nil {
			return err
		}
	}
	if err := validateVirtualIdentifiers(in); err != nil {
		return err
	}
	if in.TMDBID == "" && in.TVDBID == "" && in.IMDbID == "" {
		return fmt.Errorf("%w: an external identifier is required", ErrInvalidVirtualMedia)
	}
	if in.RuntimeMinutes < 0 || in.RuntimeMinutes > 24*60 {
		return fmt.Errorf("%w: runtime_minutes is out of range", ErrInvalidVirtualMedia)
	}
	if len(in.Variants) > maxVirtualVariantsPerMedia {
		return fmt.Errorf("%w: variants exceeds %d entries", ErrInvalidVirtualMedia, maxVirtualVariantsPerMedia)
	}
	if len(in.Episodes) > maxVirtualEpisodes {
		return fmt.Errorf("%w: episodes exceeds %d entries", ErrInvalidVirtualMedia, maxVirtualEpisodes)
	}
	if in.MediaType == "movie" && in.VirtualURI == "" && len(in.Variants) == 0 {
		return fmt.Errorf("%w: movie VirtualURI or Variants are required", ErrInvalidVirtualMedia)
	}
	if in.MediaType == "series" && (in.VirtualURI != "" || len(in.Variants) > 0) {
		return fmt.Errorf("%w: series playback sources must be attached to episodes", ErrInvalidVirtualMedia)
	}
	if in.MediaType == "series" && len(in.Episodes) == 0 {
		return fmt.Errorf("%w: series Episodes are required", ErrInvalidVirtualMedia)
	}

	seenURIs := make(map[string]struct{})
	fileCount := 0
	if in.VirtualURI != "" {
		if err := validateCanonicalVirtualURI(in.VirtualURI, in.MediaType, 0, 0); err != nil {
			return fmt.Errorf("%w: VirtualURI: %w", ErrInvalidVirtualMedia, err)
		}
		seenURIs[in.VirtualURI] = struct{}{}
	}
	for i, variant := range in.Variants {
		if err := validateVirtualVariant(variant, in.MediaType, 0, 0); err != nil {
			return fmt.Errorf("%w: variants[%d]: %w", ErrInvalidVirtualMedia, i, err)
		}
		if _, duplicate := seenURIs[variant.VirtualURI]; duplicate {
			return fmt.Errorf("%w: duplicate virtual URI %q", ErrInvalidVirtualMedia, variant.VirtualURI)
		}
		seenURIs[variant.VirtualURI] = struct{}{}
		fileCount++
	}
	if in.MediaType == "movie" && len(in.Variants) == 0 {
		fileCount++
	}
	for i, episode := range in.Episodes {
		if in.MediaType != "series" {
			return fmt.Errorf("%w: episodes are only valid for series", ErrInvalidVirtualMedia)
		}
		if err := validateVirtualEpisode(episode, i, seenURIs); err != nil {
			return err
		}
		if len(episode.Variants) > 0 {
			fileCount += len(episode.Variants)
		} else {
			fileCount++
		}
	}
	if fileCount > maxVirtualFilesPerRegistration {
		return fmt.Errorf("%w: registration exceeds %d virtual files", ErrInvalidVirtualMedia, maxVirtualFilesPerRegistration)
	}
	return nil
}

func normalizedVirtualSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "plugin"
	}
	return source
}

func validateVirtualIdentifiers(in VirtualMedia) error {
	for name, value := range map[string]string{
		"imdb_id": in.IMDbID,
		"tmdb_id": in.TMDBID,
		"tvdb_id": in.TVDBID,
	} {
		if value == "" {
			continue
		}
		if len(value) > maxVirtualIdentifierBytes || strings.TrimSpace(value) != value {
			return fmt.Errorf("%w: %s is invalid", ErrInvalidVirtualMedia, name)
		}
		if name == "imdb_id" {
			if !imdbIDPattern.MatchString(value) {
				return fmt.Errorf("%w: imdb_id must be a canonical tt identifier", ErrInvalidVirtualMedia)
			}
			continue
		}
		if !numericProviderIDPattern.MatchString(value) {
			return fmt.Errorf("%w: %s must be a canonical positive integer", ErrInvalidVirtualMedia, name)
		}
	}
	return nil
}

func validateVirtualEpisode(ep VirtualEpisode, index int, seenURIs map[string]struct{}) error {
	if ep.SeasonNumber <= 0 || ep.EpisodeNumber <= 0 {
		return fmt.Errorf("%w: episodes[%d] must have positive season and episode numbers", ErrInvalidVirtualMedia, index)
	}
	if ep.SeasonNumber > 100000 || ep.EpisodeNumber > 100000 {
		return fmt.Errorf("%w: episodes[%d] season or episode number is out of range", ErrInvalidVirtualMedia, index)
	}
	if err := validateVirtualText(fmt.Sprintf("episodes[%d].title", index), ep.Title, maxVirtualTitleBytes, false, false); err != nil {
		return err
	}
	if err := validateVirtualText(fmt.Sprintf("episodes[%d].overview", index), ep.Overview, maxVirtualOverviewBytes, false, true); err != nil {
		return err
	}
	if err := validateVirtualText(fmt.Sprintf("episodes[%d].still_path", index), ep.StillPath, maxVirtualArtworkPathBytes, false, false); err != nil {
		return err
	}
	if ep.RuntimeMinutes < 0 || ep.RuntimeMinutes > 24*60 {
		return fmt.Errorf("%w: episodes[%d].runtime_minutes is out of range", ErrInvalidVirtualMedia, index)
	}
	if ep.VirtualURI == "" && len(ep.Variants) == 0 {
		return fmt.Errorf("%w: episodes[%d] requires VirtualURI or Variants", ErrInvalidVirtualMedia, index)
	}
	if len(ep.Variants) > maxVirtualVariantsPerMedia {
		return fmt.Errorf("%w: episodes[%d].variants exceeds %d entries", ErrInvalidVirtualMedia, index, maxVirtualVariantsPerMedia)
	}
	if ep.VirtualURI != "" {
		if err := validateCanonicalVirtualURI(ep.VirtualURI, "series", ep.SeasonNumber, ep.EpisodeNumber); err != nil {
			return fmt.Errorf("%w: episodes[%d].VirtualURI: %w", ErrInvalidVirtualMedia, index, err)
		}
		if _, duplicate := seenURIs[ep.VirtualURI]; duplicate {
			return fmt.Errorf("%w: duplicate virtual URI %q", ErrInvalidVirtualMedia, ep.VirtualURI)
		}
		seenURIs[ep.VirtualURI] = struct{}{}
	}
	for variantIndex, variant := range ep.Variants {
		if err := validateVirtualVariant(variant, "series", ep.SeasonNumber, ep.EpisodeNumber); err != nil {
			return fmt.Errorf("%w: episodes[%d].variants[%d]: %w", ErrInvalidVirtualMedia, index, variantIndex, err)
		}
		if _, duplicate := seenURIs[variant.VirtualURI]; duplicate {
			return fmt.Errorf("%w: duplicate virtual URI %q", ErrInvalidVirtualMedia, variant.VirtualURI)
		}
		seenURIs[variant.VirtualURI] = struct{}{}
	}
	return nil
}

func validateVirtualVariant(variant VirtualMediaVariant, mediaType string, season, episode int) error {
	if err := validateCanonicalVirtualURI(variant.VirtualURI, mediaType, season, episode); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"label":        variant.Label,
		"resolution":   variant.Resolution,
		"codec_video":  variant.CodecVideo,
		"codec_audio":  variant.CodecAudio,
		"hdr":          variant.HDR,
		"container":    variant.Container,
		"source_type":  variant.SourceType,
		"availability": variant.Availability,
	} {
		limit := maxVirtualAttributeBytes
		if name == "label" {
			limit = maxVirtualVariantLabelBytes
		}
		if err := validateVirtualText(name, value, limit, false, false); err != nil {
			return err
		}
	}
	if variant.RuntimeMinutes < 0 || variant.RuntimeMinutes > 24*60 {
		return errors.New("runtime_minutes is out of range")
	}
	if variant.Bitrate < 0 || variant.Bitrate > 1_000_000_000 {
		return errors.New("bitrate is out of range")
	}
	if variant.FileSize < 0 {
		return errors.New("file_size cannot be negative")
	}
	if err := validateVirtualLanguages("audio_languages", variant.AudioLanguages); err != nil {
		return err
	}
	return validateVirtualLanguages("subtitle_languages", variant.SubtitleLanguages)
}

func validateVirtualLanguages(field string, languages []string) error {
	if len(languages) > maxVirtualLanguages {
		return fmt.Errorf("%s exceeds %d entries", field, maxVirtualLanguages)
	}
	for i, language := range languages {
		if err := validateVirtualText(fmt.Sprintf("%s[%d]", field, i), language, maxVirtualLanguageBytes, true, false); err != nil {
			return err
		}
	}
	return nil
}

func validateVirtualText(field, value string, maxBytes int, required, allowFormattingControls bool) error {
	if !utf8.ValidString(value) || len(value) > maxBytes || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%w: %s is invalid or exceeds %d bytes", ErrInvalidVirtualMedia, field, maxBytes)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s must not have surrounding whitespace", ErrInvalidVirtualMedia, field)
	}
	if required && value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidVirtualMedia, field)
	}
	for _, r := range value {
		if unicode.IsControl(r) && (!allowFormattingControls || (r != '\n' && r != '\r' && r != '\t')) {
			return fmt.Errorf("%w: %s contains control characters", ErrInvalidVirtualMedia, field)
		}
	}
	return nil
}

func validateCanonicalVirtualURI(raw, mediaType string, season, episode int) error {
	if raw == "" || len(raw) > maxVirtualURIBytes || strings.TrimSpace(raw) != raw || !utf8.ValidString(raw) {
		return fmt.Errorf("URI is empty, malformed, or exceeds %d bytes", maxVirtualURIBytes)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse URI: %w", err)
	}
	if parsed.Scheme != "virtual" || parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("URI must use the canonical virtual:// scheme without userinfo or fragment")
	}
	if parsed.Host != mediaType || parsed.Hostname() != mediaType || parsed.Port() != "" {
		return fmt.Errorf("URI host must be %q", mediaType)
	}
	if parsed.RawPath != "" || strings.Contains(parsed.EscapedPath(), "%") || parsed.ForceQuery {
		return errors.New("URI path must use unescaped canonical segments")
	}
	segments := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	expectedSegments := 1
	if season > 0 || episode > 0 {
		expectedSegments = 3
	}
	idOffset := 0
	if len(segments) == expectedSegments+1 {
		provider := segments[0]
		if provider != "imdb" && provider != "tmdb" && provider != "tvdb" {
			return errors.New("URI path contains an unsupported provider namespace")
		}
		if mediaType == "movie" && provider == "tvdb" {
			return errors.New("movie URI cannot use a TVDB namespace")
		}
		idOffset = 1
	}
	if len(segments) != expectedSegments+idOffset {
		return fmt.Errorf("URI path must contain %d canonical identifier segment(s)", expectedSegments)
	}
	for _, segment := range segments {
		if !virtualPathSegmentPattern.MatchString(segment) {
			return errors.New("URI path contains a non-canonical segment")
		}
	}
	identifier := segments[idOffset]
	if idOffset == 1 {
		switch segments[0] {
		case "imdb":
			if !imdbIDPattern.MatchString(identifier) {
				return errors.New("URI contains an invalid IMDb ID")
			}
		case "tmdb", "tvdb":
			if !numericProviderIDPattern.MatchString(identifier) {
				return errors.New("URI contains an invalid numeric provider ID")
			}
		}
	} else if !imdbIDPattern.MatchString(identifier) && !numericProviderIDPattern.MatchString(identifier) {
		return errors.New("URI contains an invalid external identifier")
	}
	if expectedSegments == 3 {
		if segments[idOffset+1] != strconv.Itoa(season) || segments[idOffset+2] != strconv.Itoa(episode) {
			return errors.New("URI season and episode must match the episode payload")
		}
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return fmt.Errorf("parse URI query: %w", err)
	}
	if parsed.RawQuery != query.Encode() {
		return errors.New("URI query must use canonical encoding and key order")
	}
	for key, values := range query {
		if key != "profile" && key != "result" && key != "results" {
			return fmt.Errorf("URI query parameter %q is not supported", key)
		}
		if len(values) != 1 || values[0] == "" || len(values[0]) > maxVirtualQueryValueBytes {
			return fmt.Errorf("URI query parameter %q must have one bounded value", key)
		}
		if err := validateVirtualText("URI query "+key, values[0], maxVirtualQueryValueBytes, true, false); err != nil {
			return err
		}
	}
	if _, result := query["result"]; result {
		if _, allResults := query["results"]; allResults {
			return errors.New("URI cannot contain both result and results")
		}
	}
	if values, ok := query["results"]; ok && values[0] != "all" {
		return errors.New(`URI results value must be "all"`)
	}
	return nil
}

func runtimeSeconds(minutes int) int {
	if minutes <= 0 {
		return 0
	}
	return minutes * 60
}

func virtualContentID(in VirtualMedia) string {
	if in.MediaType == "series" && in.TVDBID != "" {
		return "series-tvdb-" + in.TVDBID
	}
	if in.TMDBID != "" {
		return in.MediaType + "-tmdb-" + in.TMDBID
	}
	if in.IMDbID != "" {
		return in.MediaType + "-imdb-" + in.IMDbID
	}
	// No external ID available: build a deterministic fallback so distinct
	// items do not silently collide on the same content_id.
	h := sha256.Sum256([]byte(in.Source + "|" + in.MediaType + "|" + in.Title + "|" + strconv.Itoa(in.Year) + "|" + in.VirtualURI))
	return in.MediaType + "-hash-" + hex.EncodeToString(h[:12])
}

func virtualLibraryCompatible(folderType, mediaType string) bool {
	return folderType == "mixed" || (mediaType == "movie" && folderType == "movies") || (mediaType == "series" && folderType == "series")
}

func upsertVirtualEpisode(ctx context.Context, tx pgx.Tx, seriesID string, folderID, installationID int, ownsItemMetadata bool, ep VirtualEpisode) error {
	seasonID := fmt.Sprintf("%s-%d", strings.Replace(seriesID, "series-", "season-", 1), ep.SeasonNumber)
	episodeID := fmt.Sprintf("%s-%d-%d", strings.Replace(seriesID, "series-", "episode-", 1), ep.SeasonNumber, ep.EpisodeNumber)
	var persistedSeasonID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO seasons(content_id,series_id,season_number,title,air_date,metadata_source)
		VALUES($1,$2,$3,$4,$5,'provider')
		ON CONFLICT(series_id,season_number) DO UPDATE SET
			title=CASE WHEN $6 THEN EXCLUDED.title ELSE seasons.title END,
			air_date=CASE WHEN $6 THEN COALESCE(EXCLUDED.air_date,seasons.air_date) ELSE seasons.air_date END,
			updated_at=CASE WHEN $6 THEN NOW() ELSE seasons.updated_at END
		RETURNING content_id`,
		seasonID, seriesID, ep.SeasonNumber, fmt.Sprintf("Season %d", ep.SeasonNumber), nullTime(ep.AirDate), ownsItemMetadata,
	).Scan(&persistedSeasonID); err != nil {
		return fmt.Errorf("upsert virtual season: %w", err)
	}
	var persistedEpisodeID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO episodes(
			content_id,series_id,season_id,season_number,episode_number,title,
			overview,air_date,runtime,still_path,metadata_source
		) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,NULLIF($9,0),NULLIF($10,''),'provider')
		ON CONFLICT(series_id,season_number,episode_number) DO UPDATE SET
			season_id=CASE WHEN $11 THEN EXCLUDED.season_id ELSE episodes.season_id END,
			title=CASE WHEN $11 AND EXCLUDED.title<>'' THEN EXCLUDED.title ELSE episodes.title END,
			overview=CASE WHEN $11 THEN COALESCE(EXCLUDED.overview,episodes.overview) ELSE episodes.overview END,
			air_date=CASE WHEN $11 THEN COALESCE(EXCLUDED.air_date,episodes.air_date) ELSE episodes.air_date END,
			runtime=CASE WHEN $11 THEN COALESCE(EXCLUDED.runtime,episodes.runtime) ELSE episodes.runtime END,
			still_path=CASE WHEN $11 THEN COALESCE(EXCLUDED.still_path,episodes.still_path) ELSE episodes.still_path END,
			updated_at=CASE WHEN $11 THEN NOW() ELSE episodes.updated_at END
		RETURNING content_id`,
		episodeID, seriesID, persistedSeasonID, ep.SeasonNumber, ep.EpisodeNumber,
		ep.Title, ep.Overview, nullTime(ep.AirDate), ep.RuntimeMinutes, ep.StillPath, ownsItemMetadata,
	).Scan(&persistedEpisodeID); err != nil {
		return fmt.Errorf("upsert virtual episode: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, persistedEpisodeID, folderID); err != nil {
		return fmt.Errorf("link virtual episode: %w", err)
	}
	if len(ep.Variants) > 0 {
		for _, v := range ep.Variants {
			if err := upsertVirtualFileVariant(ctx, tx, seriesID, persistedEpisodeID, folderID, installationID, v); err != nil {
				return err
			}
		}
		return nil
	}
	return upsertVirtualFile(ctx, tx, seriesID, persistedEpisodeID, folderID, installationID, ep.VirtualURI, runtimeSeconds(ep.RuntimeMinutes))
}

func upsertVirtualFile(ctx context.Context, tx pgx.Tx, contentID, episodeID string, folderID, installationID int, uri string, duration int) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, uri); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO media_files(content_id,episode_id,media_folder_id,file_path,file_size,container,duration,probe_source,probe_updated_at,virtual_owner_installation_id)
		VALUES($1,NULLIF($2,''),$3,$4,0,'virtual',NULLIF($5,0),'virtual',now(),$6)
		ON CONFLICT (file_path, virtual_owner_installation_id, media_folder_id) WHERE virtual_owner_installation_id IS NOT NULL DO UPDATE SET
			content_id=EXCLUDED.content_id,
			episode_id=EXCLUDED.episode_id,
			media_folder_id=EXCLUDED.media_folder_id,
			file_size=0,
			container='virtual',
			duration=EXCLUDED.duration,
			probe_source=CASE
				WHEN media_files.probe_source='virtual_collection' THEN media_files.probe_source
				ELSE 'virtual'
			END,
			probe_updated_at=now(),
			virtual_owner_installation_id=EXCLUDED.virtual_owner_installation_id,
			missing_since=NULL,
			updated_at=now()`,
		contentID, episodeID, folderID, uri, duration, installationID)
	if err != nil {
		return fmt.Errorf("upsert virtual file: %w", err)
	}
	return nil
}

func upsertVirtualFileVariant(ctx context.Context, tx pgx.Tx, contentID, episodeID string, folderID, installationID int, v VirtualMediaVariant) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, v.VirtualURI); err != nil {
		return err
	}
	isHDR := v.HDR != ""
	fileSize := v.FileSize
	if fileSize < 0 {
		fileSize = 0
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO media_files(
			content_id,episode_id,media_folder_id,file_path,file_size,container,duration,probe_source,probe_updated_at,
			resolution,codec_video,codec_audio,hdr,bitrate,edition_raw,audio_tracks,subtitle_tracks,virtual_owner_installation_id
		) VALUES($1,NULLIF($2,''),$3,$4,$12,$13,NULLIF($5,0),'virtual',now(),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9,NULLIF($10,0),$11,COALESCE((SELECT jsonb_agg(jsonb_build_object('language', x)) FROM unnest($14::text[]) x),'[]'::jsonb),COALESCE((SELECT jsonb_agg(jsonb_build_object('language', x)) FROM unnest($15::text[]) x),'[]'::jsonb),$16)
		ON CONFLICT (file_path, virtual_owner_installation_id, media_folder_id) WHERE virtual_owner_installation_id IS NOT NULL DO UPDATE SET
			content_id=EXCLUDED.content_id,
			episode_id=EXCLUDED.episode_id,
			media_folder_id=EXCLUDED.media_folder_id,
			file_size=EXCLUDED.file_size,
			container=EXCLUDED.container,
			duration=EXCLUDED.duration,
			probe_source=CASE
				WHEN media_files.probe_source='virtual_collection' THEN media_files.probe_source
				ELSE 'virtual'
			END,
			probe_updated_at=now(),
			missing_since=NULL,
			updated_at=now(),
			resolution=EXCLUDED.resolution,
			codec_video=EXCLUDED.codec_video,
			codec_audio=EXCLUDED.codec_audio,
			hdr=EXCLUDED.hdr,
			bitrate=EXCLUDED.bitrate,
			edition_raw=EXCLUDED.edition_raw,
			audio_tracks=EXCLUDED.audio_tracks,
			subtitle_tracks=EXCLUDED.subtitle_tracks`,
		contentID, episodeID, folderID, v.VirtualURI, runtimeSeconds(v.RuntimeMinutes), v.Resolution, v.CodecVideo, v.CodecAudio, isHDR, v.Bitrate, v.Label, fileSize, "virtual", v.AudioLanguages, v.SubtitleLanguages, installationID)
	if err != nil {
		return fmt.Errorf("upsert virtual file variant: %w", err)
	}
	return nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (r *VirtualMediaRegistrar) normalizeSeriesVirtualMedia(ctx context.Context, in VirtualMedia) VirtualMedia {
	if in.MediaType != "series" {
		return in
	}
	hasTopLevelURI := in.VirtualURI != ""
	hasTopLevelVariants := len(in.Variants) > 0

	if !hasTopLevelURI && !hasTopLevelVariants && len(in.Episodes) > 0 {
		return in
	}

	contentID := virtualContentID(in)

	if len(in.Episodes) == 0 && r != nil && r.pool != nil {
		rows, err := r.pool.Query(ctx, `
			SELECT season_number, episode_number, title, COALESCE(overview,''), air_date, COALESCE(still_path,'')
			FROM episodes
			WHERE series_id=$1 AND season_number > 0 AND episode_number > 0
			ORDER BY season_number, episode_number`, contentID)
		if err == nil {
			for rows.Next() {
				var ep VirtualEpisode
				var airDate *time.Time
				if err := rows.Scan(&ep.SeasonNumber, &ep.EpisodeNumber, &ep.Title, &ep.Overview, &airDate, &ep.StillPath); err == nil {
					if airDate != nil {
						ep.AirDate = *airDate
					}
					in.Episodes = append(in.Episodes, ep)
				}
			}
			rows.Close()
		}
	}

	if len(in.Episodes) == 0 {
		in.Episodes = []VirtualEpisode{{
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Title:         in.Title,
		}}
	}

	baseURI := in.VirtualURI
	if baseURI == "" && hasTopLevelVariants {
		baseURI = in.Variants[0].VirtualURI
	}
	if baseURI == "" {
		baseURI = virtualPlaybackItemURIFromIDs("series", in.IMDbID, in.TMDBID, in.TVDBID)
	}

	for i := range in.Episodes {
		ep := &in.Episodes[i]
		if len(ep.Variants) == 0 {
			if hasTopLevelVariants {
				epVariants := make([]VirtualMediaVariant, 0, len(in.Variants))
				for _, v := range in.Variants {
					epV := v
					epV.VirtualURI = buildEpisodeURI(v.VirtualURI, ep.SeasonNumber, ep.EpisodeNumber)
					epVariants = append(epVariants, epV)
				}
				ep.Variants = epVariants
				ep.VirtualURI = ""
			} else if ep.VirtualURI == "" && baseURI != "" {
				ep.VirtualURI = buildEpisodeURI(baseURI, ep.SeasonNumber, ep.EpisodeNumber)
			}
		}
	}

	in.VirtualURI = ""
	in.Variants = nil

	return in
}

func virtualPlaybackItemURIFromIDs(mediaType, imdbID, tmdbID, tvdbID string) string {
	if imdbID != "" && imdbIDPattern.MatchString(imdbID) {
		return "virtual://" + mediaType + "/" + imdbID
	}
	if tvdbID != "" && numericProviderIDPattern.MatchString(tvdbID) {
		return "virtual://" + mediaType + "/tvdb/" + tvdbID
	}
	if tmdbID != "" && numericProviderIDPattern.MatchString(tmdbID) {
		return "virtual://" + mediaType + "/tmdb/" + tmdbID
	}
	return ""
}

func buildEpisodeURI(seriesURI string, season, episode int) string {
	parsed, err := url.Parse(seriesURI)
	if err != nil || parsed.Scheme != "virtual" || parsed.Host != "series" {
		return seriesURI
	}
	segments := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(segments) == 0 || (len(segments) == 1 && segments[0] == "") {
		return seriesURI
	}
	idOffset := 0
	if segments[0] == "imdb" || segments[0] == "tmdb" || segments[0] == "tvdb" {
		idOffset = 1
	}
	if len(segments) >= 3+idOffset {
		return seriesURI
	}
	basePath := strings.TrimPrefix(parsed.Path, "/")
	episodePath := fmt.Sprintf("/%s/%d/%d", basePath, season, episode)
	epURL := &url.URL{
		Scheme:   "virtual",
		Host:     "series",
		Path:     episodePath,
		RawQuery: parsed.RawQuery,
	}
	return epURL.String()
}
