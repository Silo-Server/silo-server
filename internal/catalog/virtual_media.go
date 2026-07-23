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
	VirtualURI     string
	Label          string
	Resolution     string
	CodecVideo     string
	CodecAudio     string
	HDR            string
	Bitrate        int
	RuntimeMinutes int
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
	Episodes                             []VirtualEpisode
	Variants                             []VirtualMediaVariant
}

type VirtualMediaResult struct {
	MediaID          string
	LibraryID        string
	EpisodesUpserted int
}

// VirtualMediaRegistrar owns transactional catalog registration for virtual
// sources. Plugins submit stable external identifiers and URIs; they never
// receive database access or depend on Silo's table layout.
type VirtualMediaRegistrar struct{ pool *pgxpool.Pool }

func NewVirtualMediaRegistrar(pool *pgxpool.Pool) *VirtualMediaRegistrar {
	return &VirtualMediaRegistrar{pool: pool}
}

func (r *VirtualMediaRegistrar) Upsert(ctx context.Context, in VirtualMedia) (*VirtualMediaResult, error) {
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
	status := "unmatched"
	if in.Overview != "" || in.PosterPath != "" {
		status = "matched"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_items (content_id,type,title,year,imdb_id,tmdb_id,tvdb_id,overview,genres,poster_path,backdrop_path,runtime,matched_at,status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,CASE WHEN $13='matched' THEN now() END,$13)
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
		matched_at=COALESCE(EXCLUDED.matched_at,media_items.matched_at),status=CASE WHEN EXCLUDED.status='matched' THEN 'matched' ELSE media_items.status END,updated_at=now()`,
		contentID, in.MediaType, in.Title, in.Year, in.IMDbID, in.TMDBID, in.TVDBID, in.Overview, in.Genres, in.PosterPath, in.BackdropPath, in.RuntimeMinutes, status); err != nil {
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
			if err := upsertVirtualEpisode(ctx, tx, contentID, folderID, ep); err != nil {
				return nil, err
			}
			episodes++
		}
	} else {
		if len(in.Variants) > 0 {
			for _, v := range in.Variants {
				if err := upsertVirtualFileVariant(ctx, tx, contentID, "", folderID, v); err != nil {
					return nil, err
				}
			}
		} else {
			if err := upsertVirtualFile(ctx, tx, contentID, "", folderID, in.VirtualURI, runtimeSeconds(in.RuntimeMinutes)); err != nil {
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

func upsertVirtualEpisode(ctx context.Context, tx pgx.Tx, seriesID string, folderID int, ep VirtualEpisode) error {
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
			if err := upsertVirtualFileVariant(ctx, tx, seriesID, episodeID, folderID, v); err != nil {
				return err
			}
		}
		return nil
	}
	return upsertVirtualFile(ctx, tx, seriesID, episodeID, folderID, ep.VirtualURI, runtimeSeconds(ep.RuntimeMinutes))
}

func upsertVirtualFile(ctx context.Context, tx pgx.Tx, contentID, episodeID string, folderID int, uri string, duration int) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, uri); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO media_files(content_id,episode_id,media_folder_id,file_path,file_size,container,duration,probe_source,probe_updated_at)
		VALUES($1,NULLIF($2,''),$3,$4,0,'virtual',NULLIF($5,0),'virtual',now())
		ON CONFLICT (file_path) DO UPDATE SET
			content_id=EXCLUDED.content_id,
			episode_id=EXCLUDED.episode_id,
			media_folder_id=EXCLUDED.media_folder_id,
			file_size=0,
			container='virtual',
			duration=EXCLUDED.duration,
			probe_source='virtual',
			probe_updated_at=now(),
			missing_since=NULL,
			updated_at=now()`,
		contentID, episodeID, folderID, uri, duration)
	if err != nil {
		return fmt.Errorf("upsert virtual file: %w", err)
	}
	return nil
}

func upsertVirtualFileVariant(ctx context.Context, tx pgx.Tx, contentID, episodeID string, folderID int, v VirtualMediaVariant) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, v.VirtualURI); err != nil {
		return err
	}
	isHDR := false
	if v.HDR != "" {
		isHDR = true
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO media_files(
			content_id,episode_id,media_folder_id,file_path,file_size,container,duration,probe_source,probe_updated_at,
			resolution,codec_video,codec_audio,hdr,bitrate
		) VALUES($1,NULLIF($2,''),$3,$4,0,'virtual',NULLIF($5,0),'virtual',now(),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9,NULLIF($10,0))
		ON CONFLICT (file_path) DO UPDATE SET 
			content_id=EXCLUDED.content_id,
			episode_id=EXCLUDED.episode_id,
			media_folder_id=EXCLUDED.media_folder_id,
			file_size=0,
			container='virtual',
			duration=EXCLUDED.duration,
			probe_source='virtual',
			probe_updated_at=now(),
			missing_since=NULL,
			updated_at=now(),
			resolution=EXCLUDED.resolution,
			codec_video=EXCLUDED.codec_video,
			codec_audio=EXCLUDED.codec_audio,
			hdr=EXCLUDED.hdr,
			bitrate=EXCLUDED.bitrate`,
		contentID, episodeID, folderID, v.VirtualURI, runtimeSeconds(v.RuntimeMinutes), v.Resolution, v.CodecVideo, v.CodecAudio, isHDR, v.Bitrate)
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
