package catalogseed

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	pgvector "github.com/pgvector/pgvector-go"
)

type exportTotals struct {
	Libraries  int
	Items      int
	People     int
	Embeddings int
	Seasons    int
	Episodes   int
	Files      int
	Links      int
}

func (t exportTotals) progressTotal() int {
	return t.Libraries + t.Items + t.People + t.Embeddings + t.Seasons + t.Episodes + t.Files + t.Links
}

type exportProgressReporter struct {
	total    int
	current  int
	callback func(ExportProgress)
}

func (r *exportProgressReporter) stage(message string) {
	if r.callback == nil {
		return
	}
	r.callback(ExportProgress{
		Message: message,
		Current: r.current,
		Total:   r.total,
	})
}

func (r *exportProgressReporter) advance(message string) {
	r.current++
	if r.callback == nil {
		return
	}
	if r.current == r.total || r.current%500 == 0 {
		r.callback(ExportProgress{
			Message: message,
			Current: r.current,
			Total:   r.total,
		})
	}
}

func (s *Service) ExportToWriter(ctx context.Context, w io.Writer, opts ExportOptions, progress func(ExportProgress)) (*ExportSummary, error) {
	folders, err := s.exportFolders(ctx, opts.LibraryIDs)
	if err != nil {
		return nil, err
	}
	sortLibraries(folders)

	folderIDs := make([]int, 0, len(folders))
	for _, folder := range folders {
		folderIDs = append(folderIDs, folder.ExportedID)
	}

	schemaVersion, err := s.schemaVersion(ctx)
	if err != nil {
		return nil, err
	}

	totals, err := s.loadExportTotals(ctx, folderIDs)
	if err != nil {
		return nil, err
	}
	totals.Libraries = len(folders)

	reporter := &exportProgressReporter{
		total:    totals.progressTotal(),
		callback: progress,
	}
	reporter.stage("Preparing catalog export")

	summary := &ExportSummary{
		FormatVersion:     CurrentBundleVersion,
		SchemaVersion:     schemaVersion,
		LibrariesExported: len(folders),
	}
	manifest := Manifest{
		FormatVersion: CurrentBundleVersion,
		ExportedAt:    time.Now().UTC(),
		SchemaVersion: schemaVersion,
	}

	zw := gzip.NewWriter(w)
	bw := bufio.NewWriterSize(zw, 1<<20)

	if _, err := bw.WriteString(`{"manifest":`); err != nil {
		_ = zw.Close()
		return nil, fmt.Errorf("writing export manifest prefix: %w", err)
	}
	if err := writeJSONValue(bw, manifest); err != nil {
		_ = zw.Close()
		return nil, fmt.Errorf("writing export manifest: %w", err)
	}

	if err := writeArrayField(bw, "libraries", func(arr *jsonArrayWriter) error {
		reporter.stage("Exporting libraries")
		for _, folder := range folders {
			if err := arr.Write(folder); err != nil {
				return err
			}
			reporter.advance("Exporting libraries")
		}
		return nil
	}); err != nil {
		_ = zw.Close()
		return nil, err
	}

	if err := writeArrayField(bw, "items", func(arr *jsonArrayWriter) error {
		reporter.stage("Exporting items")
		return s.streamItemRecords(ctx, folderIDs, func(record ItemRecord) error {
			if err := arr.Write(record); err != nil {
				return err
			}
			summary.ItemsExported++
			reporter.advance("Exporting items")
			return nil
		})
	}); err != nil {
		_ = zw.Close()
		return nil, err
	}

	if err := writeArrayField(bw, "people", func(arr *jsonArrayWriter) error {
		reporter.stage("Exporting people")
		return s.streamPersonRecords(ctx, folderIDs, func(record PersonRecord) error {
			if err := arr.Write(record); err != nil {
				return err
			}
			summary.PeopleExported++
			reporter.advance("Exporting people")
			return nil
		})
	}); err != nil {
		_ = zw.Close()
		return nil, err
	}

	if err := writeArrayField(bw, "seasons", func(arr *jsonArrayWriter) error {
		reporter.stage("Exporting seasons")
		return s.streamSeasonRecords(ctx, folderIDs, func(record SeasonRecord) error {
			if err := arr.Write(record); err != nil {
				return err
			}
			summary.SeasonsExported++
			reporter.advance("Exporting seasons")
			return nil
		})
	}); err != nil {
		_ = zw.Close()
		return nil, err
	}

	if err := writeArrayField(bw, "episodes", func(arr *jsonArrayWriter) error {
		reporter.stage("Exporting episodes")
		return s.streamEpisodeRecords(ctx, folderIDs, func(record EpisodeRecord) error {
			if err := arr.Write(record); err != nil {
				return err
			}
			summary.EpisodesExported++
			reporter.advance("Exporting episodes")
			return nil
		})
	}); err != nil {
		_ = zw.Close()
		return nil, err
	}

	if err := writeArrayField(bw, "files", func(arr *jsonArrayWriter) error {
		reporter.stage("Exporting files")
		return s.streamFileRecords(ctx, folderIDs, func(record FileRecord) error {
			if err := arr.Write(record); err != nil {
				return err
			}
			summary.FilesExported++
			reporter.advance("Exporting files")
			return nil
		})
	}); err != nil {
		_ = zw.Close()
		return nil, err
	}

	if err := writeArrayField(bw, "library_links", func(arr *jsonArrayWriter) error {
		reporter.stage("Exporting library links")
		return s.streamLibraryLinkRecords(ctx, folderIDs, func(record LibraryLinkRecord) error {
			if err := arr.Write(record); err != nil {
				return err
			}
			summary.LibraryLinksExported++
			reporter.advance("Exporting library links")
			return nil
		})
	}); err != nil {
		return nil, err
	}

	if err := writeArrayField(bw, "embeddings", func(arr *jsonArrayWriter) error {
		reporter.stage("Exporting embeddings")
		return s.streamEmbeddingRecords(ctx, folderIDs, func(record EmbeddingRecord) error {
			if err := arr.Write(record); err != nil {
				return err
			}
			summary.EmbeddingsExported++
			reporter.advance("Exporting embeddings")
			return nil
		})
	}); err != nil {
		_ = zw.Close()
		return nil, err
	}

	if _, err := bw.WriteString(`}`); err != nil {
		_ = zw.Close()
		return nil, fmt.Errorf("writing export suffix: %w", err)
	}
	if err := bw.Flush(); err != nil {
		_ = zw.Close()
		return nil, fmt.Errorf("flushing catalog export: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("closing catalog export gzip stream: %w", err)
	}

	reporter.stage("Catalog export complete")
	return summary, nil
}

func (s *Service) loadExportTotals(ctx context.Context, folderIDs []int) (exportTotals, error) {
	totals := exportTotals{}

	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM media_files WHERE media_folder_id = ANY($1)`,
		folderIDs,
	).Scan(&totals.Files); err != nil {
		return totals, fmt.Errorf("counting export files: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM media_item_libraries WHERE media_folder_id = ANY($1)`,
		folderIDs,
	).Scan(&totals.Links); err != nil {
		return totals, fmt.Errorf("counting export library links: %w", err)
	}
	if err := s.pool.QueryRow(ctx, exportContentIDsCTE+`
		SELECT COUNT(*) FROM exported_content_ids`,
		folderIDs,
	).Scan(&totals.Items); err != nil {
		return totals, fmt.Errorf("counting export items: %w", err)
	}
	if err := s.pool.QueryRow(ctx, exportContentIDsCTE+`
		SELECT COUNT(*)
		FROM item_people ip
		JOIN exported_content_ids ids ON ids.content_id = ip.content_id`,
		folderIDs,
	).Scan(&totals.People); err != nil {
		return totals, fmt.Errorf("counting export people: %w", err)
	}
	if err := s.pool.QueryRow(ctx, exportContentIDsCTE+`
		SELECT COUNT(*)
		FROM media_item_embeddings e
		JOIN exported_content_ids ids ON ids.content_id = e.media_item_id`,
		folderIDs,
	).Scan(&totals.Embeddings); err != nil {
		return totals, fmt.Errorf("counting export embeddings: %w", err)
	}
	if err := s.pool.QueryRow(ctx, exportSeriesIDsCTE+`
		SELECT COUNT(*)
		FROM seasons s
		JOIN exported_series_ids ids ON ids.content_id = s.series_id`,
		folderIDs,
	).Scan(&totals.Seasons); err != nil {
		return totals, fmt.Errorf("counting export seasons: %w", err)
	}
	if err := s.pool.QueryRow(ctx, exportSeriesIDsCTE+`
		SELECT COUNT(*)
		FROM episodes e
		JOIN exported_series_ids ids ON ids.content_id = e.series_id`,
		folderIDs,
	).Scan(&totals.Episodes); err != nil {
		return totals, fmt.Errorf("counting export episodes: %w", err)
	}

	return totals, nil
}

const exportContentIDsCTE = `
	WITH exported_content_ids AS (
		SELECT DISTINCT content_id
		FROM media_item_libraries
		WHERE media_folder_id = ANY($1)
		UNION
		SELECT DISTINCT content_id
		FROM media_files
		WHERE media_folder_id = ANY($1)
		  AND content_id IS NOT NULL
		  AND content_id <> ''
	)
`

const exportSeriesIDsCTE = `
	WITH exported_content_ids AS (
		SELECT DISTINCT content_id
		FROM media_item_libraries
		WHERE media_folder_id = ANY($1)
		UNION
		SELECT DISTINCT content_id
		FROM media_files
		WHERE media_folder_id = ANY($1)
		  AND content_id IS NOT NULL
		  AND content_id <> ''
	),
	exported_series_ids AS (
		SELECT mi.content_id
		FROM media_items mi
		JOIN exported_content_ids ids ON ids.content_id = mi.content_id
		WHERE mi.type = 'series'
	)
`

func (s *Service) streamItemRecords(ctx context.Context, folderIDs []int, fn func(ItemRecord) error) error {
	rows, err := s.pool.Query(ctx, exportContentIDsCTE+`
		SELECT
			mi.content_id,
			mi.type,
			COALESCE(mi.title, ''),
			COALESCE(mi.sort_title, ''),
			COALESCE(mi.original_title, ''),
			COALESCE(mi.year, 0),
			COALESCE(mi.genres, ARRAY[]::text[]),
			COALESCE(mi.content_rating, ''),
			COALESCE(mi.runtime, 0),
			COALESCE(mi.overview, ''),
			COALESCE(mi.tagline, ''),
			mi.rating_imdb,
			mi.rating_tmdb,
			mi.rating_rt_critic,
			mi.rating_rt_audience,
			COALESCE(mi.imdb_id, ''),
			COALESCE(mi.tmdb_id, ''),
			COALESCE(mi.tvdb_id, ''),
			COALESCE(mi.poster_path, ''),
			COALESCE(mi.poster_thumbhash, ''),
			COALESCE(mi.backdrop_path, ''),
			COALESCE(mi.backdrop_thumbhash, ''),
			COALESCE(mi.logo_path, ''),
			COALESCE(mi.metadata_s3_path, ''),
			COALESCE(mi.metadata_etag, ''),
			mi.season_count,
			COALESCE(mi.studios, ARRAY[]::text[]),
			COALESCE(mi.networks, ARRAY[]::text[]),
			COALESCE(mi.countries, ARRAY[]::text[]),
			COALESCE(mi.keywords, ARRAY[]::text[]),
			COALESCE(mi.original_language, ''),
			mi.release_date::text,
			mi.first_air_date,
			mi.last_air_date,
			mi.air_time,
			mi.air_timezone,
			mi.matched_at,
			mi.last_refreshed,
			COALESCE(mi.refresh_failures, 0),
			COALESCE(mi.locked_fields, ARRAY[]::integer[]),
			COALESCE(mi.status, ''),
			mi.created_at,
			mi.updated_at
		FROM media_items mi
		JOIN exported_content_ids ids ON ids.content_id = mi.content_id
		ORDER BY mi.content_id ASC`,
		folderIDs,
	)
	if err != nil {
		return fmt.Errorf("querying export items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var record ItemRecord
		if err := rows.Scan(
			&record.ContentID,
			&record.Type,
			&record.Title,
			&record.SortTitle,
			&record.OriginalTitle,
			&record.Year,
			&record.Genres,
			&record.ContentRating,
			&record.Runtime,
			&record.Overview,
			&record.Tagline,
			&record.RatingIMDB,
			&record.RatingTMDB,
			&record.RatingRTCritic,
			&record.RatingRTAudience,
			&record.ImdbID,
			&record.TmdbID,
			&record.TvdbID,
			&record.PosterPath,
			&record.PosterThumbhash,
			&record.BackdropPath,
			&record.BackdropThumbhash,
			&record.LogoPath,
			&record.MetadataS3Path,
			&record.MetadataEtag,
			&record.SeasonCount,
			&record.Studios,
			&record.Networks,
			&record.Countries,
			&record.Keywords,
			&record.OriginalLanguage,
			&record.ReleaseDate,
			&record.FirstAirDate,
			&record.LastAirDate,
			&record.AirTime,
			&record.AirTimezone,
			&record.MatchedAt,
			&record.LastRefreshed,
			&record.RefreshFailures,
			&record.LockedFields,
			&record.Status,
			&record.CreatedAt,
			&record.UpdatedAt,
		); err != nil {
			return fmt.Errorf("scanning export item row: %w", err)
		}
		if err := fn(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Service) streamPersonRecords(ctx context.Context, folderIDs []int, fn func(PersonRecord) error) error {
	rows, err := s.pool.Query(ctx, exportContentIDsCTE+`
		SELECT
			ip.content_id,
			p.id,
			COALESCE(p.name, ''),
			ip.kind,
			COALESCE(ip.character, ''),
			ip.sort_order,
			COALESCE(p.tmdb_id, ''),
			COALESCE(p.imdb_id, ''),
			COALESCE(p.tvdb_id, ''),
			COALESCE(p.plex_guid, ''),
			COALESCE(p.photo_path, ''),
			COALESCE(p.photo_thumbhash, '')
		FROM item_people ip
		JOIN people p ON p.id = ip.person_id
		JOIN exported_content_ids ids ON ids.content_id = ip.content_id
		ORDER BY ip.content_id ASC, ip.sort_order ASC`,
		folderIDs,
	)
	if err != nil {
		return fmt.Errorf("querying export people: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var record PersonRecord
		if err := rows.Scan(
			&record.ContentID,
			&record.PersonID,
			&record.Name,
			&record.Kind,
			&record.Character,
			&record.SortOrder,
			&record.TmdbID,
			&record.ImdbID,
			&record.TvdbID,
			&record.PlexGUID,
			&record.PhotoPath,
			&record.PhotoThumbhash,
		); err != nil {
			return fmt.Errorf("scanning export person row: %w", err)
		}
		if err := fn(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Service) streamEmbeddingRecords(ctx context.Context, folderIDs []int, fn func(EmbeddingRecord) error) error {
	rows, err := s.pool.Query(ctx, exportContentIDsCTE+`
		SELECT
			e.media_item_id,
			e.embedding,
			e.model,
			COALESCE(e.canonical_text, '')
		FROM media_item_embeddings e
		JOIN exported_content_ids ids ON ids.content_id = e.media_item_id`,
		folderIDs,
	)
	if err != nil {
		return fmt.Errorf("querying export embeddings: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var record EmbeddingRecord
		var v pgvector.Vector
		if err := rows.Scan(
			&record.MediaItemID,
			&v,
			&record.Model,
			&record.CanonicalText,
		); err != nil {
			return fmt.Errorf("scanning export embedding row: %w", err)
		}
		record.Embedding = v.Slice()
		if err := fn(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Service) streamSeasonRecords(ctx context.Context, folderIDs []int, fn func(SeasonRecord) error) error {
	rows, err := s.pool.Query(ctx, exportSeriesIDsCTE+`
		SELECT
			s.content_id,
			s.series_id,
			s.season_number,
			COALESCE(s.title, ''),
			COALESCE(s.overview, ''),
			s.air_date,
			COALESCE(s.poster_path, ''),
			COALESCE(s.poster_thumbhash, ''),
			COALESCE(s.metadata_s3_path, ''),
			COALESCE(s.metadata_etag, ''),
			s.created_at,
			s.updated_at
		FROM seasons s
		JOIN exported_series_ids ids ON ids.content_id = s.series_id
		ORDER BY s.series_id ASC, s.season_number ASC`,
		folderIDs,
	)
	if err != nil {
		return fmt.Errorf("querying export seasons: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var record SeasonRecord
		if err := rows.Scan(
			&record.ContentID,
			&record.SeriesID,
			&record.SeasonNumber,
			&record.Title,
			&record.Overview,
			&record.AirDate,
			&record.PosterPath,
			&record.PosterThumbhash,
			&record.MetadataS3Path,
			&record.MetadataEtag,
			&record.CreatedAt,
			&record.UpdatedAt,
		); err != nil {
			return fmt.Errorf("scanning export season row: %w", err)
		}
		if err := fn(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Service) streamEpisodeRecords(ctx context.Context, folderIDs []int, fn func(EpisodeRecord) error) error {
	rows, err := s.pool.Query(ctx, exportSeriesIDsCTE+`
		SELECT
			e.content_id,
			e.series_id,
			COALESCE(e.season_id, ''),
			e.season_number,
			e.episode_number,
			COALESCE(e.title, ''),
			COALESCE(e.overview, ''),
			e.air_date,
			COALESCE(e.runtime, 0),
			e.rating_imdb,
			e.rating_tmdb,
			COALESCE(e.still_path, ''),
			COALESCE(e.still_thumbhash, ''),
			COALESCE(e.metadata_s3_path, ''),
			COALESCE(e.metadata_etag, ''),
			e.created_at,
			e.updated_at
		FROM episodes e
		JOIN exported_series_ids ids ON ids.content_id = e.series_id
		ORDER BY e.series_id ASC, e.season_number ASC, e.episode_number ASC`,
		folderIDs,
	)
	if err != nil {
		return fmt.Errorf("querying export episodes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var record EpisodeRecord
		if err := rows.Scan(
			&record.ContentID,
			&record.SeriesID,
			&record.SeasonID,
			&record.SeasonNumber,
			&record.EpisodeNumber,
			&record.Title,
			&record.Overview,
			&record.AirDate,
			&record.Runtime,
			&record.RatingIMDB,
			&record.RatingTMDB,
			&record.StillPath,
			&record.StillThumbhash,
			&record.MetadataS3Path,
			&record.MetadataEtag,
			&record.CreatedAt,
			&record.UpdatedAt,
		); err != nil {
			return fmt.Errorf("scanning export episode row: %w", err)
		}
		if err := fn(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Service) streamFileRecords(ctx context.Context, folderIDs []int, fn func(FileRecord) error) error {
	rows, err := s.pool.Query(ctx, `
		SELECT
			content_id,
			episode_id,
			season_number,
			episode_number,
			media_folder_id,
			file_path,
			file_size,
			file_hash,
			codec_video,
			codec_audio,
			resolution,
			audio_channels,
			hdr,
			container,
			duration,
			bitrate,
			video_tracks,
			audio_tracks,
			subtitle_tracks,
			external_subtitles,
			intro_start,
			intro_end,
			credits_start,
			credits_end,
			probe_source,
			probe_updated_at,
			missing_since,
			created_at,
			updated_at
		FROM media_files
		WHERE media_folder_id = ANY($1)
		ORDER BY file_path ASC`,
		folderIDs,
	)
	if err != nil {
		return fmt.Errorf("querying export files: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		record, err := scanFileRecordRow(rows)
		if err != nil {
			return err
		}
		if err := fn(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Service) streamLibraryLinkRecords(ctx context.Context, folderIDs []int, fn func(LibraryLinkRecord) error) error {
	rows, err := s.pool.Query(ctx, `
		SELECT content_id, media_folder_id, first_seen_at
		FROM media_item_libraries
		WHERE media_folder_id = ANY($1)
		ORDER BY media_folder_id ASC, content_id ASC`,
		folderIDs,
	)
	if err != nil {
		return fmt.Errorf("querying export library links: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var record LibraryLinkRecord
		if err := rows.Scan(&record.ContentID, &record.MediaFolderID, &record.FirstSeenAt); err != nil {
			return fmt.Errorf("scanning export library link row: %w", err)
		}
		if err := fn(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func scanFileRecordRow(row interface {
	Scan(dest ...any) error
}) (FileRecord, error) {
	var record FileRecord
	var contentID *string
	var episodeID *string
	var seasonNumber *int
	var episodeNumber *int
	var fileHash *string
	var codecVideo *string
	var codecAudio *string
	var resolution *string
	var audioChannels *int
	var hdr *bool
	var container *string
	var duration *int
	var bitrate *int
	var probeSource *string
	var videoTracksJSON []byte
	var audioTracksJSON []byte
	var subtitleTracksJSON []byte
	var externalSubtitlesJSON []byte

	if err := row.Scan(
		&contentID,
		&episodeID,
		&seasonNumber,
		&episodeNumber,
		&record.MediaFolderID,
		&record.FilePath,
		&record.FileSize,
		&fileHash,
		&codecVideo,
		&codecAudio,
		&resolution,
		&audioChannels,
		&hdr,
		&container,
		&duration,
		&bitrate,
		&videoTracksJSON,
		&audioTracksJSON,
		&subtitleTracksJSON,
		&externalSubtitlesJSON,
		&record.IntroStart,
		&record.IntroEnd,
		&record.CreditsStart,
		&record.CreditsEnd,
		&probeSource,
		&record.ProbeUpdatedAt,
		&record.MissingSince,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		return FileRecord{}, fmt.Errorf("scanning export file row: %w", err)
	}

	if contentID != nil {
		record.ContentID = *contentID
	}
	if episodeID != nil {
		record.EpisodeID = *episodeID
	}
	if seasonNumber != nil {
		record.SeasonNumber = *seasonNumber
	}
	if episodeNumber != nil {
		record.EpisodeNumber = *episodeNumber
	}
	if fileHash != nil {
		record.FileHash = *fileHash
	}
	if codecVideo != nil {
		record.CodecVideo = *codecVideo
	}
	if codecAudio != nil {
		record.CodecAudio = *codecAudio
	}
	if resolution != nil {
		record.Resolution = *resolution
	}
	if audioChannels != nil {
		record.AudioChannels = *audioChannels
	}
	if hdr != nil {
		record.HDR = *hdr
	}
	if container != nil {
		record.Container = *container
	}
	if duration != nil {
		record.Duration = *duration
	}
	if bitrate != nil {
		record.Bitrate = *bitrate
	}
	if probeSource != nil {
		record.ProbeSource = *probeSource
	}

	if len(videoTracksJSON) > 0 {
		if err := json.Unmarshal(videoTracksJSON, &record.VideoTracks); err != nil {
			return FileRecord{}, fmt.Errorf("unmarshaling export video_tracks: %w", err)
		}
	}
	if record.VideoTracks == nil {
		record.VideoTracks = []VideoTrackRecord{}
	}

	if len(audioTracksJSON) > 0 {
		if err := json.Unmarshal(audioTracksJSON, &record.AudioTracks); err != nil {
			return FileRecord{}, fmt.Errorf("unmarshaling export audio_tracks: %w", err)
		}
	}
	if record.AudioTracks == nil {
		record.AudioTracks = []AudioTrackRecord{}
	}

	if len(subtitleTracksJSON) > 0 {
		if err := json.Unmarshal(subtitleTracksJSON, &record.SubtitleTracks); err != nil {
			return FileRecord{}, fmt.Errorf("unmarshaling export subtitle_tracks: %w", err)
		}
	}
	if record.SubtitleTracks == nil {
		record.SubtitleTracks = []SubtitleTrackRecord{}
	}

	if len(externalSubtitlesJSON) > 0 {
		if err := json.Unmarshal(externalSubtitlesJSON, &record.ExternalSubtitles); err != nil {
			return FileRecord{}, fmt.Errorf("unmarshaling export external_subtitles: %w", err)
		}
	}
	if record.ExternalSubtitles == nil {
		record.ExternalSubtitles = []ExternalSubtitleRecord{}
	}

	return record, nil
}

type jsonArrayWriter struct {
	w     *bufio.Writer
	first bool
}

func writeArrayField(w *bufio.Writer, name string, fn func(*jsonArrayWriter) error) error {
	if _, err := w.WriteString(`,"` + name + `":[`); err != nil {
		return fmt.Errorf("writing export field %q: %w", name, err)
	}
	arr := &jsonArrayWriter{w: w, first: true}
	if err := fn(arr); err != nil {
		return err
	}
	if _, err := w.WriteString(`]`); err != nil {
		return fmt.Errorf("closing export field %q: %w", name, err)
	}
	return nil
}

func (w *jsonArrayWriter) Write(value any) error {
	if !w.first {
		if _, err := w.w.WriteString(`,`); err != nil {
			return fmt.Errorf("writing export array separator: %w", err)
		}
	}
	if err := writeJSONValue(w.w, value); err != nil {
		return err
	}
	w.first = false
	return nil
}

func writeJSONValue(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshaling export value: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("writing export value: %w", err)
	}
	return nil
}
