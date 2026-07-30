package catalog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidVirtualMedia = errors.New("invalid virtual media")

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
	if err := validateVirtualMedia(in); err != nil {
		return nil, err
	}
	folderID, _ := strconv.Atoi(in.LibraryID)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin virtual media transaction: %w", err)
	}
	defer tx.Rollback(ctx)
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
	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = "plugin"
	}
	status := "unmatched"
	if in.Overview != "" || in.PosterPath != "" {
		status = "matched"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_items (content_id,type,title,year,imdb_id,tmdb_id,tvdb_id,overview,genres,poster_path,backdrop_path,runtime,matched_at,status,virtual_owner_installation_id,virtual_source,virtual_last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,CASE WHEN $13='matched' THEN now() END,$13,$14,$15,now())
		ON CONFLICT (content_id) DO UPDATE SET type=EXCLUDED.type,title=EXCLUDED.title,
		year=CASE WHEN EXCLUDED.year > 0 THEN EXCLUDED.year ELSE media_items.year END,
		imdb_id=CASE WHEN EXCLUDED.imdb_id <> '' THEN EXCLUDED.imdb_id ELSE media_items.imdb_id END,
		tmdb_id=CASE WHEN EXCLUDED.tmdb_id <> '' THEN EXCLUDED.tmdb_id ELSE media_items.tmdb_id END,
		tvdb_id=CASE WHEN EXCLUDED.tvdb_id <> '' THEN EXCLUDED.tvdb_id ELSE media_items.tvdb_id END,
		overview=CASE WHEN EXCLUDED.overview <> '' THEN EXCLUDED.overview ELSE media_items.overview END,
		genres=CASE WHEN cardinality(EXCLUDED.genres)>0 THEN EXCLUDED.genres ELSE media_items.genres END,
		poster_path=CASE WHEN EXCLUDED.poster_path <> '' THEN EXCLUDED.poster_path ELSE media_items.poster_path END,
		backdrop_path=CASE WHEN EXCLUDED.backdrop_path <> '' THEN EXCLUDED.backdrop_path ELSE media_items.backdrop_path END,
		runtime=CASE WHEN EXCLUDED.runtime > 0 THEN EXCLUDED.runtime ELSE media_items.runtime END,
		matched_at=COALESCE(EXCLUDED.matched_at,media_items.matched_at),status=CASE WHEN EXCLUDED.status='matched' THEN 'matched' ELSE media_items.status END,
		virtual_owner_installation_id=EXCLUDED.virtual_owner_installation_id,virtual_source=EXCLUDED.virtual_source,virtual_last_seen_at=now(),updated_at=now()`,
		contentID, in.MediaType, in.Title, in.Year, in.IMDbID, in.TMDBID, in.TVDBID, in.Overview, in.Genres, in.PosterPath, in.BackdropPath, in.RuntimeMinutes, status, installationID, source); err != nil {
		return nil, fmt.Errorf("upsert virtual media: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, contentID, folderID); err != nil {
		return nil, fmt.Errorf("link virtual media: %w", err)
	}
	episodes := 0
	if in.MediaType == "series" {
		for _, ep := range in.Episodes {
			if ep.SeasonNumber <= 0 || ep.EpisodeNumber <= 0 || (strings.TrimSpace(ep.VirtualURI) == "" && len(ep.Variants) == 0) {
				continue
			}
			if err := upsertVirtualEpisode(ctx, tx, contentID, folderID, installationID, ep); err != nil {
				return nil, err
			}
			episodes++
		}
	} else {
		if len(in.Variants) > 0 {
			for _, v := range in.Variants {
				if err := upsertVirtualFileVariant(ctx, tx, contentID, "", folderID, installationID, v); err != nil {
					return nil, err
				}
			}
		} else {
			if err := upsertVirtualFile(ctx, tx, contentID, "", folderID, installationID, in.VirtualURI, runtimeSeconds(in.RuntimeMinutes)); err != nil {
				return nil, err
			}
		}
	}
	if err := EnqueueSearchIndexUpsert(ctx, tx, contentID); err != nil {
		return nil, fmt.Errorf("enqueue virtual media search update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit virtual media transaction: %w", err)
	}
	return &VirtualMediaResult{MediaID: contentID, LibraryID: in.LibraryID, EpisodesUpserted: episodes}, nil
}

// ReconcileVirtualMedia removes stale virtual media owned by one plugin source.
// Physical files and collection-linked items are preserved.
func (r *VirtualMediaRegistrar) ReconcileVirtualMedia(ctx context.Context, installationID int, source string, keepIDs []string, libraryIDs []int) (VirtualReconcileResult, error) {
	var result VirtualReconcileResult
	if r == nil || r.pool == nil {
		return result, errors.New("virtual catalog is unavailable")
	}
	source = strings.TrimSpace(source)
	if installationID <= 0 || source == "" {
		return result, errors.New("installation and source are required")
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
	fileTag, err := tx.Exec(ctx, `
		DELETE FROM media_files mf
		USING media_items mi
		WHERE mf.content_id = mi.content_id
		  AND (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%')
		  AND mi.virtual_owner_installation_id = $1
		  AND (mf.virtual_owner_installation_id = $1 OR (mf.virtual_owner_installation_id = 0 AND mi.virtual_owner_installation_id = $1))
		  AND mi.virtual_source = $2
		  AND NOT (mi.content_id = ANY($3::text[]))
		  AND (cardinality($4::int[]) = 0 OR EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($4::int[])
		  ))`, installationID, source, keepIDs, libraryIDs)
	if err != nil {
		return result, fmt.Errorf("delete stale virtual files: %w", err)
	}
	result.FilesRemoved = int(fileTag.RowsAffected())
	rows, err := tx.Query(ctx, `
		DELETE FROM media_items mi
		WHERE mi.virtual_owner_installation_id = $1
		  AND mi.virtual_source = $2
		  AND NOT (mi.content_id = ANY($3::text[]))
		  AND (cardinality($4::int[]) = 0 OR EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($4::int[])
		  ))
		  AND NOT EXISTS (SELECT 1 FROM media_files mf WHERE mf.content_id = mi.content_id)
		  AND NOT EXISTS (SELECT 1 FROM library_collection_items lci WHERE lci.media_item_id = mi.content_id)
		RETURNING mi.content_id`, installationID, source, keepIDs, libraryIDs)
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
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.LibraryID) == "" {
		return fmt.Errorf("%w: title and library_id are required", ErrInvalidVirtualMedia)
	}
	if _, err := strconv.Atoi(in.LibraryID); err != nil {
		return fmt.Errorf("%w: library_id must be numeric", ErrInvalidVirtualMedia)
	}
	if in.VirtualURI == "" && len(in.Variants) == 0 {
		return fmt.Errorf("%w: VirtualURI or Variants are required", ErrInvalidVirtualMedia)
	}
	if in.VirtualURI != "" && !strings.HasPrefix(in.VirtualURI, "aiostreams://") && !strings.HasPrefix(in.VirtualURI, "virtual://") {
		return fmt.Errorf("%w: unsupported virtual URI", ErrInvalidVirtualMedia)
	}
	for _, v := range in.Variants {
		if !strings.HasPrefix(v.VirtualURI, "aiostreams://") && !strings.HasPrefix(v.VirtualURI, "virtual://") {
			return fmt.Errorf("%w: unsupported virtual URI in variant", ErrInvalidVirtualMedia)
		}
	}
	if in.TMDBID == "" && in.TVDBID == "" && in.IMDbID == "" {
		return fmt.Errorf("%w: an external identifier is required", ErrInvalidVirtualMedia)
	}
	if in.RuntimeMinutes < 0 || in.RuntimeMinutes > 24*60 {
		return fmt.Errorf("%w: runtime_minutes is out of range", ErrInvalidVirtualMedia)
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
	return in.MediaType + "-imdb-" + in.IMDbID
}

func virtualLibraryCompatible(folderType, mediaType string) bool {
	return folderType == "mixed" || (mediaType == "movie" && folderType == "movies") || (mediaType == "series" && folderType == "series")
}

func upsertVirtualEpisode(ctx context.Context, tx pgx.Tx, seriesID string, folderID, installationID int, ep VirtualEpisode) error {
	seasonID := fmt.Sprintf("%s-%d", strings.Replace(seriesID, "series-", "season-", 1), ep.SeasonNumber)
	episodeID := fmt.Sprintf("%s-%d-%d", strings.Replace(seriesID, "series-", "episode-", 1), ep.SeasonNumber, ep.EpisodeNumber)
	if _, err := tx.Exec(ctx, `INSERT INTO seasons(content_id,series_id,season_number,title,air_date,metadata_source) VALUES($1,$2,$3,$4,$5,'provider') ON CONFLICT(series_id,season_number) DO UPDATE SET title=EXCLUDED.title,air_date=COALESCE(EXCLUDED.air_date,seasons.air_date),updated_at=now()`, seasonID, seriesID, ep.SeasonNumber, fmt.Sprintf("Season %d", ep.SeasonNumber), nullTime(ep.AirDate)); err != nil {
		return fmt.Errorf("upsert virtual season: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO episodes(content_id,series_id,season_id,season_number,episode_number,title,overview,air_date,runtime,still_path,metadata_source) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,NULLIF($9,0),NULLIF($10,''),'provider') ON CONFLICT(series_id,season_number,episode_number) DO UPDATE SET season_id=EXCLUDED.season_id,title=EXCLUDED.title,overview=COALESCE(EXCLUDED.overview,episodes.overview),air_date=COALESCE(EXCLUDED.air_date,episodes.air_date),runtime=COALESCE(EXCLUDED.runtime,episodes.runtime),still_path=COALESCE(EXCLUDED.still_path,episodes.still_path),updated_at=now()`, episodeID, seriesID, seasonID, ep.SeasonNumber, ep.EpisodeNumber, ep.Title, ep.Overview, nullTime(ep.AirDate), ep.RuntimeMinutes, ep.StillPath); err != nil {
		return fmt.Errorf("upsert virtual episode: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, episodeID, folderID); err != nil {
		return fmt.Errorf("link virtual episode: %w", err)
	}
	if len(ep.Variants) > 0 {
		for _, v := range ep.Variants {
			if err := upsertVirtualFileVariant(ctx, tx, seriesID, episodeID, folderID, installationID, v); err != nil {
				return err
			}
		}
		return nil
	}
	return upsertVirtualFile(ctx, tx, seriesID, episodeID, folderID, installationID, ep.VirtualURI, runtimeSeconds(ep.RuntimeMinutes))
}

func upsertVirtualFile(ctx context.Context, tx pgx.Tx, contentID, episodeID string, folderID, installationID int, uri string, duration int) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, uri); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO media_files(content_id,episode_id,media_folder_id,file_path,file_size,container,duration,probe_source,probe_updated_at,virtual_owner_installation_id)
		VALUES($1,NULLIF($2,''),$3,$4,0,'virtual',NULLIF($5,0),'virtual',now(),$6)
		ON CONFLICT (file_path, virtual_owner_installation_id) WHERE virtual_owner_installation_id IS NOT NULL DO UPDATE SET
			content_id=EXCLUDED.content_id,
			episode_id=EXCLUDED.episode_id,
			media_folder_id=EXCLUDED.media_folder_id,
			file_size=0,
			container='virtual',
			duration=EXCLUDED.duration,
			probe_source='virtual',
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
	isHDR := false
	if v.HDR != "" {
		isHDR = true
	}
	fileSize := v.FileSize
	if fileSize < 0 {
		fileSize = 0
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO media_files(
			content_id,episode_id,media_folder_id,file_path,file_size,container,duration,probe_source,probe_updated_at,
			resolution,codec_video,codec_audio,hdr,bitrate,edition_raw,audio_tracks,subtitle_tracks,virtual_owner_installation_id
		) VALUES($1,NULLIF($2,''),$3,$4,$12,$13,NULLIF($5,0),'virtual',now(),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9,NULLIF($10,0),NULLIF($11,''),COALESCE((SELECT jsonb_agg(jsonb_build_object('language', x)) FROM unnest($14::text[]) x),'[]'::jsonb),COALESCE((SELECT jsonb_agg(jsonb_build_object('language', x)) FROM unnest($15::text[]) x),'[]'::jsonb),$16)
		ON CONFLICT (file_path, virtual_owner_installation_id) WHERE virtual_owner_installation_id IS NOT NULL DO UPDATE SET
			content_id=EXCLUDED.content_id,
			episode_id=EXCLUDED.episode_id,
			media_folder_id=EXCLUDED.media_folder_id,
			file_size=EXCLUDED.file_size,
			container=EXCLUDED.container,
			duration=EXCLUDED.duration,
			probe_source='virtual',
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
