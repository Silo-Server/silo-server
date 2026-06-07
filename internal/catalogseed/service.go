package catalogseed

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/titleutil"
)

var (
	ErrUnsupportedBundleVersion = errors.New("unsupported catalog seed version")
	ErrInvalidConflictMode      = errors.New("invalid catalog seed conflict mode")
	ErrInvalidBundle            = errors.New("invalid catalog seed bundle")
)

type UnmatchedRootsError struct {
	Roots []string
}

func (e *UnmatchedRootsError) Error() string {
	if len(e.Roots) == 0 {
		return "catalog seed import requires additional path rewrites"
	}
	return fmt.Sprintf("catalog seed import requires additional path rewrites: %s", strings.Join(e.Roots, ", "))
}

type Service struct {
	pool       *pgxpool.Pool
	personRepo *catalog.PersonRepository
	recsRepo   *recommendations.Repo
}

func NewService(pool *pgxpool.Pool, personRepo *catalog.PersonRepository, recsRepo *recommendations.Repo) *Service {
	if personRepo == nil {
		personRepo = catalog.NewPersonRepository(pool)
	}
	if recsRepo == nil {
		recsRepo = recommendations.NewRepo(pool)
	}
	return &Service{pool: pool, personRepo: personRepo, recsRepo: recsRepo}
}

func (s *Service) Export(ctx context.Context, opts ExportOptions) ([]byte, error) {
	var raw bytes.Buffer
	if _, err := s.ExportToWriter(ctx, &raw, opts, nil); err != nil {
		return nil, err
	}
	return raw.Bytes(), nil
}

func (s *Service) Import(ctx context.Context, data []byte, opts ImportOptions) (*ImportResult, error) {
	return s.ImportWithProgress(ctx, data, opts, nil)
}

func (s *Service) ImportWithProgress(ctx context.Context, data []byte, opts ImportOptions, progress func(ImportProgress)) (*ImportResult, error) {
	if opts.ConflictMode == "" {
		opts.ConflictMode = ConflictModeSkipExisting
	}
	if opts.ConflictMode != ConflictModeSkipExisting && opts.ConflictMode != ConflictModeOverwrite {
		return nil, ErrInvalidConflictMode
	}

	reportProgress := func(message string, current, total int) {
		if progress == nil {
			return
		}
		progress(ImportProgress{
			Message: message,
			Current: current,
			Total:   total,
		})
	}

	reportProgress("Reading catalog bundle", 0, 0)
	bundle, err := decodeBundle(data)
	if err != nil {
		return nil, err
	}
	if bundle.Manifest.FormatVersion != CurrentBundleVersion {
		return nil, ErrUnsupportedBundleVersion
	}
	for i := range bundle.Items {
		bundle.Items[i].OriginalLanguage = lang.Canonical(bundle.Items[i].OriginalLanguage)
		bundle.Items[i].Countries = lang.CanonicalCountries(bundle.Items[i].Countries)
	}

	totalWork := len(bundle.Libraries) + len(bundle.Items) + len(bundle.Embeddings) + len(bundle.Seasons) + len(bundle.Episodes) + len(bundle.Files) + len(bundle.LibraryLinks) + 1
	if totalWork <= 0 {
		totalWork = 1
	}
	currentWork := 0
	reportProgress("Validating catalog bundle", currentWork, totalWork)

	rewrites := normalizeRewrites(opts.PathRewrites)
	libraries, folderPaths, unmatchedRoots, err := prepareLibraries(bundle.Libraries, rewrites)
	if err != nil {
		return nil, err
	}
	if len(unmatchedRoots) > 0 {
		sort.Strings(unmatchedRoots)
		return nil, &UnmatchedRootsError{Roots: unmatchedRoots}
	}

	if err := validateFileRoots(bundle.Files, folderPaths, rewrites); err != nil {
		return nil, err
	}

	reportProgress("Starting catalog import", currentWork, totalWork)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("beginning catalog seed import: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	emptyTarget, err := isEmptyCatalogImportTarget(ctx, tx)
	if err != nil {
		return nil, err
	}

	result := &ImportResult{}
	folderIDMap := make(map[int]int, len(libraries))
	for _, library := range libraries {
		localID, created, matched, importErr := importLibrary(ctx, tx, library, opts.ConflictMode == ConflictModeOverwrite)
		if importErr != nil {
			return nil, importErr
		}
		folderIDMap[library.ExportedID] = localID
		if created {
			result.LibrariesCreated++
		}
		if matched {
			result.LibrariesMatched++
		}
		currentWork++
		reportProgress("Importing libraries", currentWork, totalWork)
	}

	if emptyTarget {
		// Empty-target fast path: use COPY protocol for maximum throughput.
		itemRows := make([][]any, 0, len(bundle.Items))
		for _, item := range bundle.Items {
			studios, networks, countries, keywords := itemRecordStringArrays(item)
			itemRows = append(itemRows, []any{
				item.ContentID, item.Type, item.Title, item.SortTitle, item.OriginalTitle, item.Year, item.Genres,
				item.ContentRating, item.Runtime, item.Overview, item.Tagline,
				item.RatingIMDB, item.RatingTMDB, item.RatingRTCritic, item.RatingRTAudience,
				item.ImdbID, item.TmdbID, item.TvdbID,
				item.PosterPath, item.PosterThumbhash, item.BackdropPath, item.BackdropThumbhash, item.LogoPath,
				item.MetadataS3Path, item.MetadataEtag, item.SeasonCount,
				studios, networks, countries, keywords, item.OriginalLanguage, item.ReleaseDate, item.FirstAirDate, item.LastAirDate, item.AirTime, item.AirTimezone,
				item.MatchedAt, item.LastRefreshed, item.RefreshFailures, item.LockedFields, item.Status,
				item.CreatedAt, item.UpdatedAt,
			})
		}
		if err := copyInsertBatches(ctx, tx, "media_items",
			[]string{
				"content_id", "type", "title", "sort_title", "original_title", "year", "genres",
				"content_rating", "runtime", "overview", "tagline",
				"rating_imdb", "rating_tmdb", "rating_rt_critic", "rating_rt_audience",
				"imdb_id", "tmdb_id", "tvdb_id",
				"poster_path", "poster_thumbhash", "backdrop_path", "backdrop_thumbhash", "logo_path",
				"metadata_s3_path", "metadata_etag", "season_count",
				"studios", "networks", "countries", "keywords", "original_language", "release_date", "first_air_date", "last_air_date", "air_time", "air_timezone",
				"matched_at", "last_refreshed", "refresh_failures", "locked_fields", "status",
				"created_at", "updated_at",
			},
			itemRows, func(processed int) {
				currentWork += processed
				reportProgress("Importing catalog items", currentWork, totalWork)
			}); err != nil {
			return nil, err
		}
		result.ItemsCreated = len(bundle.Items)

		// Build lookup sets for FK filtering of child records.
		itemIDs := make(map[string]struct{}, len(bundle.Items))
		itemStates := make(map[string]bool, len(bundle.Items))
		for _, item := range bundle.Items {
			itemIDs[item.ContentID] = struct{}{}
			itemStates[item.ContentID] = true
		}

		reportProgress("Importing people", currentWork, totalWork)
		if err := s.replacePeople(ctx, tx, bundle.People, itemStates, ConflictModeOverwrite, result); err != nil {
			return nil, err
		}
		if err := s.importEmbeddings(ctx, tx, bundle.Embeddings, true, result, func(processed int) {
			currentWork += processed
			reportProgress("Importing embeddings", currentWork, totalWork)
		}); err != nil {
			return nil, err
		}
		currentWork++
		reportProgress("Importing embeddings", currentWork, totalWork)

		seasonRows := make([][]any, 0, len(bundle.Seasons))
		seasonIDs := make(map[string]struct{}, len(bundle.Seasons))
		for _, season := range bundle.Seasons {
			if _, ok := itemIDs[season.SeriesID]; !ok {
				continue // skip orphan season
			}
			seasonIDs[season.ContentID] = struct{}{}
			seasonRows = append(seasonRows, []any{
				season.ContentID, season.SeriesID, season.SeasonNumber, season.Title, season.Overview, season.AirDate,
				season.PosterPath, season.PosterThumbhash, season.MetadataS3Path, season.MetadataEtag,
				season.CreatedAt, season.UpdatedAt,
			})
		}
		if err := copyInsertBatches(ctx, tx, "seasons",
			[]string{
				"content_id", "series_id", "season_number", "title", "overview", "air_date",
				"poster_path", "poster_thumbhash", "metadata_s3_path", "metadata_etag", "created_at", "updated_at",
			},
			seasonRows, func(processed int) {
				currentWork += processed
				reportProgress("Importing seasons", currentWork, totalWork)
			}); err != nil {
			return nil, err
		}
		result.SeasonsCreated = len(seasonRows)

		episodeRows := make([][]any, 0, len(bundle.Episodes))
		episodeIDs := make(map[string]struct{}, len(bundle.Episodes))
		for _, episode := range bundle.Episodes {
			// Skip episodes whose series or season doesn't exist.
			if _, ok := itemIDs[episode.SeriesID]; !ok {
				continue
			}
			if episode.SeasonID != "" {
				if _, ok := seasonIDs[episode.SeasonID]; !ok {
					continue
				}
			}
			episodeIDs[episode.ContentID] = struct{}{}
			episodeRows = append(episodeRows, []any{
				episode.ContentID, episode.SeriesID, nullableString(episode.SeasonID), episode.SeasonNumber, episode.EpisodeNumber,
				episode.Title, episode.Overview, episode.AirDate, episode.Runtime, episode.RatingIMDB, episode.RatingTMDB,
				episode.StillPath, episode.StillThumbhash, episode.MetadataS3Path, episode.MetadataEtag,
				episode.CreatedAt, episode.UpdatedAt,
			})
		}
		if err := copyInsertBatches(ctx, tx, "episodes",
			[]string{
				"content_id", "series_id", "season_id", "season_number", "episode_number",
				"title", "overview", "air_date", "runtime", "rating_imdb", "rating_tmdb",
				"still_path", "still_thumbhash", "metadata_s3_path", "metadata_etag", "created_at", "updated_at",
			},
			episodeRows, func(processed int) {
				currentWork += processed
				reportProgress("Importing episodes", currentWork, totalWork)
			}); err != nil {
			return nil, err
		}
		result.EpisodesCreated = len(episodeRows)

		rewrittenFiles := make([]FileRecord, 0, len(bundle.Files))
		for _, file := range bundle.Files {
			// Skip files referencing non-existent items or episodes.
			if file.ContentID != "" {
				if _, ok := itemIDs[file.ContentID]; !ok {
					continue
				}
			}
			if file.EpisodeID != "" {
				if _, ok := episodeIDs[file.EpisodeID]; !ok {
					continue
				}
			}
			rewritten, rewriteErr := rewriteFileRecord(file, rewrites, folderPaths)
			if rewriteErr != nil {
				return nil, rewriteErr
			}
			rewritten.MediaFolderID = folderIDMap[file.MediaFolderID]
			rewrittenFiles = append(rewrittenFiles, rewritten)
		}
		fileRows := make([][]any, 0, len(rewrittenFiles))
		for _, file := range rewrittenFiles {
			videoTracksJSON, jsonErr := json.Marshal(file.VideoTracks)
			if jsonErr != nil {
				return nil, fmt.Errorf("marshaling video tracks for %s: %w", file.FilePath, jsonErr)
			}
			audioTracksJSON, jsonErr := json.Marshal(file.AudioTracks)
			if jsonErr != nil {
				return nil, fmt.Errorf("marshaling audio tracks for %s: %w", file.FilePath, jsonErr)
			}
			subtitleTracksJSON, jsonErr := json.Marshal(file.SubtitleTracks)
			if jsonErr != nil {
				return nil, fmt.Errorf("marshaling subtitle tracks for %s: %w", file.FilePath, jsonErr)
			}
			externalSubtitlesJSON, jsonErr := json.Marshal(file.ExternalSubtitles)
			if jsonErr != nil {
				return nil, fmt.Errorf("marshaling external subtitles for %s: %w", file.FilePath, jsonErr)
			}
			fileRows = append(fileRows, []any{
				nullableString(file.ContentID), nullableString(file.EpisodeID), nilIfZero(file.SeasonNumber), nilIfZero(file.EpisodeNumber),
				file.MediaFolderID, file.FilePath, file.FileSize, nullableString(file.FileHash),
				file.CodecVideo, file.CodecAudio, file.Resolution, nilIfZero(file.AudioChannels), file.HDR, file.Container,
				nilIfZero(file.Duration), nilIfZero(file.Bitrate), videoTracksJSON, audioTracksJSON, subtitleTracksJSON, externalSubtitlesJSON,
				file.IntroStart, file.IntroEnd, file.CreditsStart, file.CreditsEnd,
				nullableString(file.ProbeSource), file.ProbeUpdatedAt, file.MissingSince, file.CreatedAt, file.UpdatedAt,
			})
		}
		if err := copyInsertBatches(ctx, tx, "media_files",
			[]string{
				"content_id", "episode_id", "season_number", "episode_number",
				"media_folder_id", "file_path", "file_size", "file_hash",
				"codec_video", "codec_audio", "resolution", "audio_channels", "hdr", "container",
				"duration", "bitrate", "video_tracks", "audio_tracks", "subtitle_tracks", "external_subtitles",
				"intro_start", "intro_end", "credits_start", "credits_end",
				"probe_source", "probe_updated_at", "missing_since", "created_at", "updated_at",
			},
			fileRows, func(processed int) {
				currentWork += processed
				reportProgress("Importing media files", currentWork, totalWork)
			}); err != nil {
			return nil, err
		}
		result.FilesCreated = len(rewrittenFiles)

		linkRows := make([][]any, 0, len(bundle.LibraryLinks))
		for _, link := range bundle.LibraryLinks {
			if _, ok := itemIDs[link.ContentID]; !ok {
				continue
			}
			linkRows = append(linkRows, []any{
				link.ContentID, folderIDMap[link.MediaFolderID], link.FirstSeenAt,
			})
		}
		if err := copyInsertBatches(ctx, tx, "media_item_libraries",
			[]string{"content_id", "media_folder_id", "first_seen_at"},
			linkRows, func(processed int) {
				currentWork += processed
				reportProgress("Linking items to libraries", currentWork, totalWork)
			}); err != nil {
			return nil, err
		}
		result.LinksCreated = len(linkRows)

		reportProgress("Finalizing catalog import", currentWork, totalWork)
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("committing catalog seed import: %w", err)
		}

		reportProgress("Catalog import completed", totalWork, totalWork)
		return result, nil
	}

	// Batch import items (instead of one-by-one).
	itemStates, itemsCreated, itemsUpdated, itemsSkipped, err := batchImportItems(ctx, tx, bundle.Items, opts.ConflictMode, func(processed int) {
		currentWork += processed
		reportProgress("Importing catalog items", currentWork, totalWork)
	})
	if err != nil {
		return nil, err
	}
	result.ItemsCreated = itemsCreated
	result.ItemsUpdated = itemsUpdated
	result.Skipped += itemsSkipped

	reportProgress("Importing people", currentWork, totalWork)
	if err := s.replacePeople(ctx, tx, bundle.People, itemStates, opts.ConflictMode, result); err != nil {
		return nil, fmt.Errorf("replacing people: %w", err)
	}
	if err := s.importEmbeddings(ctx, tx, bundle.Embeddings, false, result, func(processed int) {
		currentWork += processed
		reportProgress("Importing embeddings", currentWork, totalWork)
	}); err != nil {
		return nil, fmt.Errorf("importing embeddings: %w", err)
	}
	currentWork++
	reportProgress("Importing embeddings", currentWork, totalWork)

	// Batch import seasons.
	seasonRows := make([][]any, 0, len(bundle.Seasons))
	for _, season := range bundle.Seasons {
		seasonRows = append(seasonRows, []any{
			season.ContentID, season.SeriesID, season.SeasonNumber, season.Title, season.Overview, season.AirDate,
			season.PosterPath, season.PosterThumbhash, season.MetadataS3Path, season.MetadataEtag,
			season.CreatedAt, season.UpdatedAt,
		})
	}
	seasonCounts, err := executeUpsertBatches(ctx, tx, `
		INSERT INTO seasons (
			content_id, series_id, season_number, title, overview, air_date,
			poster_path, poster_thumbhash, metadata_s3_path, metadata_etag, created_at, updated_at
		) VALUES `,
		seasonRows, 12, nil,
		` ON CONFLICT (content_id) DO NOTHING`,
		`
		ON CONFLICT (content_id) DO UPDATE SET
			series_id = EXCLUDED.series_id,
			season_number = EXCLUDED.season_number,
			title = EXCLUDED.title,
			overview = EXCLUDED.overview,
			air_date = EXCLUDED.air_date,
			poster_path = EXCLUDED.poster_path,
			poster_thumbhash = EXCLUDED.poster_thumbhash,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			updated_at = EXCLUDED.updated_at`,
		opts.ConflictMode, func(processed int) {
			currentWork += processed
			reportProgress("Importing seasons", currentWork, totalWork)
		})
	if err != nil {
		return nil, err
	}
	result.SeasonsCreated = seasonCounts.Created
	result.SeasonsUpdated = seasonCounts.Updated
	result.Skipped += seasonCounts.Skipped

	// Batch import episodes.
	episodeRows := make([][]any, 0, len(bundle.Episodes))
	for _, episode := range bundle.Episodes {
		episodeRows = append(episodeRows, []any{
			episode.ContentID, episode.SeriesID, nullableString(episode.SeasonID), episode.SeasonNumber, episode.EpisodeNumber,
			episode.Title, episode.Overview, episode.AirDate, episode.Runtime, episode.RatingIMDB, episode.RatingTMDB,
			episode.StillPath, episode.StillThumbhash, episode.MetadataS3Path, episode.MetadataEtag,
			episode.CreatedAt, episode.UpdatedAt,
		})
	}
	episodeCounts, err := executeUpsertBatches(ctx, tx, `
		INSERT INTO episodes (
			content_id, series_id, season_id, season_number, episode_number,
			title, overview, air_date, runtime, rating_imdb, rating_tmdb,
			still_path, still_thumbhash, metadata_s3_path, metadata_etag, created_at, updated_at
		) VALUES `,
		episodeRows, 17, nil,
		` ON CONFLICT (content_id) DO NOTHING`,
		`
		ON CONFLICT (content_id) DO UPDATE SET
			series_id = EXCLUDED.series_id,
			season_id = EXCLUDED.season_id,
			season_number = EXCLUDED.season_number,
			episode_number = EXCLUDED.episode_number,
			title = EXCLUDED.title,
			overview = EXCLUDED.overview,
			air_date = EXCLUDED.air_date,
			runtime = EXCLUDED.runtime,
			rating_imdb = EXCLUDED.rating_imdb,
			rating_tmdb = EXCLUDED.rating_tmdb,
			still_path = EXCLUDED.still_path,
			still_thumbhash = EXCLUDED.still_thumbhash,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			updated_at = EXCLUDED.updated_at`,
		opts.ConflictMode, func(processed int) {
			currentWork += processed
			reportProgress("Importing episodes", currentWork, totalWork)
		})
	if err != nil {
		return nil, err
	}
	result.EpisodesCreated = episodeCounts.Created
	result.EpisodesUpdated = episodeCounts.Updated
	result.Skipped += episodeCounts.Skipped

	// Rewrite file paths and batch import files.
	rewrittenFiles := make([]FileRecord, 0, len(bundle.Files))
	for _, file := range bundle.Files {
		rewritten, rewriteErr := rewriteFileRecord(file, rewrites, folderPaths)
		if rewriteErr != nil {
			return nil, rewriteErr
		}
		rewritten.MediaFolderID = folderIDMap[file.MediaFolderID]
		rewrittenFiles = append(rewrittenFiles, rewritten)
	}
	fileRows := make([][]any, 0, len(rewrittenFiles))
	for _, file := range rewrittenFiles {
		videoTracksJSON, jsonErr := json.Marshal(file.VideoTracks)
		if jsonErr != nil {
			return nil, fmt.Errorf("marshaling video tracks for %s: %w", file.FilePath, jsonErr)
		}
		audioTracksJSON, jsonErr := json.Marshal(file.AudioTracks)
		if jsonErr != nil {
			return nil, fmt.Errorf("marshaling audio tracks for %s: %w", file.FilePath, jsonErr)
		}
		subtitleTracksJSON, jsonErr := json.Marshal(file.SubtitleTracks)
		if jsonErr != nil {
			return nil, fmt.Errorf("marshaling subtitle tracks for %s: %w", file.FilePath, jsonErr)
		}
		externalSubtitlesJSON, jsonErr := json.Marshal(file.ExternalSubtitles)
		if jsonErr != nil {
			return nil, fmt.Errorf("marshaling external subtitles for %s: %w", file.FilePath, jsonErr)
		}
		fileRows = append(fileRows, []any{
			nullableString(file.ContentID), nullableString(file.EpisodeID), nilIfZero(file.SeasonNumber), nilIfZero(file.EpisodeNumber),
			file.MediaFolderID, file.FilePath, file.FileSize, nullableString(file.FileHash),
			file.CodecVideo, file.CodecAudio, file.Resolution, nilIfZero(file.AudioChannels), file.HDR, file.Container,
			nilIfZero(file.Duration), nilIfZero(file.Bitrate), string(videoTracksJSON), string(audioTracksJSON), string(subtitleTracksJSON), string(externalSubtitlesJSON),
			file.IntroStart, file.IntroEnd, file.CreditsStart, file.CreditsEnd,
			nullableString(file.ProbeSource), file.ProbeUpdatedAt, file.MissingSince, file.CreatedAt, file.UpdatedAt,
		})
	}
	fileCounts, err := executeUpsertBatches(ctx, tx, `
		INSERT INTO media_files (
			content_id, episode_id, season_number, episode_number,
			media_folder_id, file_path, file_size, file_hash,
			codec_video, codec_audio, resolution, audio_channels, hdr, container,
			duration, bitrate, video_tracks, audio_tracks, subtitle_tracks, external_subtitles,
			intro_start, intro_end, credits_start, credits_end,
			probe_source, probe_updated_at, missing_since, created_at, updated_at
		) VALUES `,
		fileRows, 29,
		map[int]string{16: "::jsonb", 17: "::jsonb", 18: "::jsonb", 19: "::jsonb"},
		` ON CONFLICT (file_path) DO NOTHING`,
		`
		ON CONFLICT (file_path) DO UPDATE SET
			content_id = EXCLUDED.content_id,
			episode_id = EXCLUDED.episode_id,
			season_number = EXCLUDED.season_number,
			episode_number = EXCLUDED.episode_number,
			media_folder_id = EXCLUDED.media_folder_id,
			file_size = EXCLUDED.file_size,
			file_hash = EXCLUDED.file_hash,
			codec_video = EXCLUDED.codec_video,
			codec_audio = EXCLUDED.codec_audio,
			resolution = EXCLUDED.resolution,
			audio_channels = EXCLUDED.audio_channels,
			hdr = EXCLUDED.hdr,
			container = EXCLUDED.container,
			duration = EXCLUDED.duration,
			bitrate = EXCLUDED.bitrate,
			video_tracks = EXCLUDED.video_tracks,
			audio_tracks = EXCLUDED.audio_tracks,
			subtitle_tracks = EXCLUDED.subtitle_tracks,
			external_subtitles = EXCLUDED.external_subtitles,
			intro_start = EXCLUDED.intro_start,
			intro_end = EXCLUDED.intro_end,
			credits_start = EXCLUDED.credits_start,
			credits_end = EXCLUDED.credits_end,
			probe_source = EXCLUDED.probe_source,
			probe_updated_at = EXCLUDED.probe_updated_at,
			missing_since = EXCLUDED.missing_since,
			updated_at = EXCLUDED.updated_at`,
		opts.ConflictMode, func(processed int) {
			currentWork += processed
			reportProgress("Importing media files", currentWork, totalWork)
		})
	if err != nil {
		return nil, err
	}
	result.FilesCreated = fileCounts.Created
	result.FilesUpdated = fileCounts.Updated
	result.Skipped += fileCounts.Skipped

	// Batch import library links.
	linkRows := make([][]any, 0, len(bundle.LibraryLinks))
	for _, link := range bundle.LibraryLinks {
		linkRows = append(linkRows, []any{link.ContentID, folderIDMap[link.MediaFolderID], link.FirstSeenAt})
	}
	linksAffected, err := executeInsertBatchesCounted(ctx, tx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES `,
		linkRows, 3, nil,
		` ON CONFLICT (content_id, media_folder_id) DO NOTHING`,
		func(processed int) {
			currentWork += processed
			reportProgress("Linking items to libraries", currentWork, totalWork)
		})
	if err != nil {
		return nil, err
	}
	result.LinksCreated = int(linksAffected)

	reportProgress("Finalizing catalog import", currentWork, totalWork)
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing catalog seed import: %w", err)
	}

	reportProgress("Catalog import completed", totalWork, totalWork)
	return result, nil
}

func (s *Service) exportFolders(ctx context.Context, libraryIDs []int) ([]LibraryRecord, error) {
	folderRepo := catalog.NewFolderRepository(s.pool)
	var folders []*models.MediaFolder
	if len(libraryIDs) == 0 {
		var err error
		folders, err = folderRepo.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing folders for export: %w", err)
		}
	} else {
		folders = make([]*models.MediaFolder, 0, len(libraryIDs))
		for _, libraryID := range libraryIDs {
			folder, err := folderRepo.GetByID(ctx, libraryID)
			if err != nil {
				return nil, fmt.Errorf("loading folder %d for export: %w", libraryID, err)
			}
			folders = append(folders, folder)
		}
	}

	records := make([]LibraryRecord, 0, len(folders))
	for _, folder := range folders {
		paths := append([]string(nil), folder.Paths...)
		sort.Strings(paths)
		records = append(records, LibraryRecord{
			ExportedID:            folder.ID,
			Paths:                 paths,
			Type:                  folder.Type,
			Name:                  folder.Name,
			Enabled:               folder.Enabled,
			LastScannedAt:         folder.LastScannedAt,
			ScanWarningCode:       folder.ScanWarningCode,
			ScanWarningMessage:    folder.ScanWarningMessage,
			ScanWarningAt:         folder.ScanWarningAt,
			AllowEmptyCleanupOnce: folder.AllowEmptyCleanupOnce,
		})
	}

	return records, nil
}

func (s *Service) schemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.pool.QueryRow(ctx, "SELECT COALESCE(MAX(version_id), 0) FROM public.goose_db_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("loading schema version: %w", err)
	}
	return version, nil
}

func (s *Service) loadSeasons(ctx context.Context, seriesID string) ([]SeasonRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT content_id, series_id, season_number, title, overview,
			air_date, poster_path, poster_thumbhash, metadata_s3_path, metadata_etag,
			created_at, updated_at
		FROM seasons
		WHERE series_id = $1
		ORDER BY season_number ASC`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("querying seasons for %s: %w", seriesID, err)
	}
	defer rows.Close()

	var seasons []SeasonRecord
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
			return nil, fmt.Errorf("scanning season for %s: %w", seriesID, err)
		}
		seasons = append(seasons, record)
	}
	return seasons, rows.Err()
}

func (s *Service) loadEpisodes(ctx context.Context, seriesID string) ([]EpisodeRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT content_id, series_id, season_id, season_number, episode_number,
			title, overview, air_date, runtime, rating_imdb, rating_tmdb,
			still_path, still_thumbhash, metadata_s3_path, metadata_etag,
			created_at, updated_at
		FROM episodes
		WHERE series_id = $1
		ORDER BY season_number ASC, episode_number ASC`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("querying episodes for %s: %w", seriesID, err)
	}
	defer rows.Close()

	var episodes []EpisodeRecord
	for rows.Next() {
		var record EpisodeRecord
		var seasonID *string
		if err := rows.Scan(
			&record.ContentID,
			&record.SeriesID,
			&seasonID,
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
			return nil, fmt.Errorf("scanning episode for %s: %w", seriesID, err)
		}
		if seasonID != nil {
			record.SeasonID = *seasonID
		}
		episodes = append(episodes, record)
	}
	return episodes, rows.Err()
}

func itemToRecord(item *models.MediaItem) ItemRecord {
	if strings.TrimSpace(item.SortTitle) == "" {
		if derived := titleutil.DeriveDefaultSortTitle(item.Title); derived != "" {
			item.SortTitle = derived
		}
	}
	return ItemRecord{
		ContentID:         item.ContentID,
		Type:              item.Type,
		Title:             item.Title,
		SortTitle:         item.SortTitle,
		OriginalTitle:     item.OriginalTitle,
		Year:              item.Year,
		Genres:            cloneStrings(item.Genres),
		ContentRating:     item.ContentRating,
		Runtime:           item.Runtime,
		Overview:          item.Overview,
		Tagline:           item.Tagline,
		RatingIMDB:        item.RatingIMDB,
		RatingTMDB:        item.RatingTMDB,
		RatingRTCritic:    item.RatingRTCritic,
		RatingRTAudience:  item.RatingRTAudience,
		ImdbID:            item.ImdbID,
		TmdbID:            item.TmdbID,
		TvdbID:            item.TvdbID,
		PosterPath:        item.PosterPath,
		PosterThumbhash:   item.PosterThumbhash,
		BackdropPath:      item.BackdropPath,
		BackdropThumbhash: item.BackdropThumbhash,
		LogoPath:          item.LogoPath,
		MetadataS3Path:    item.MetadataS3Path,
		MetadataEtag:      item.MetadataEtag,
		SeasonCount:       item.SeasonCount,
		Studios:           cloneStrings(item.Studios),
		Networks:          cloneStrings(item.Networks),
		Countries:         cloneStrings(item.Countries),
		ReleaseDate:       item.ReleaseDate,
		FirstAirDate:      item.FirstAirDate,
		LastAirDate:       item.LastAirDate,
		MatchedAt:         item.MatchedAt,
		LastRefreshed:     item.LastRefreshed,
		RefreshFailures:   item.RefreshFailures,
		LockedFields:      cloneInts(item.LockedFields),
		Status:            item.Status,
		CreatedAt:         item.CreatedAt,
		UpdatedAt:         item.UpdatedAt,
	}
}

func fileToRecord(file *models.MediaFile) FileRecord {
	return FileRecord{
		ContentID:         file.ContentID,
		EpisodeID:         file.EpisodeID,
		SeasonNumber:      file.SeasonNumber,
		EpisodeNumber:     file.EpisodeNumber,
		MediaFolderID:     file.MediaFolderID,
		FilePath:          file.FilePath,
		FileSize:          file.FileSize,
		FileHash:          file.FileHash,
		CodecVideo:        file.CodecVideo,
		CodecAudio:        file.CodecAudio,
		Resolution:        file.Resolution,
		AudioChannels:     file.AudioChannels,
		HDR:               file.HDR,
		Container:         file.Container,
		Duration:          file.Duration,
		Bitrate:           file.Bitrate,
		VideoTracks:       toVideoTrackRecords(file.VideoTracks),
		AudioTracks:       toAudioTrackRecords(file.AudioTracks),
		SubtitleTracks:    toSubtitleTrackRecords(file.SubtitleTracks),
		ExternalSubtitles: toExternalSubtitleRecords(file.ExternalSubtitles),
		IntroStart:        file.IntroStart,
		IntroEnd:          file.IntroEnd,
		CreditsStart:      file.CreditsStart,
		CreditsEnd:        file.CreditsEnd,
		ProbeSource:       file.ProbeSource,
		ProbeUpdatedAt:    file.ProbeUpdatedAt,
		MissingSince:      file.MissingSince,
		CreatedAt:         file.CreatedAt,
		UpdatedAt:         file.UpdatedAt,
	}
}

func encodeBundle(bundle Bundle) ([]byte, error) {
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	if err := json.NewEncoder(gz).Encode(bundle); err != nil {
		_ = gz.Close()
		return nil, fmt.Errorf("encoding catalog seed bundle: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("closing catalog seed bundle: %w", err)
	}
	return raw.Bytes(), nil
}

func decodeBundle(data []byte) (*Bundle, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: opening catalog seed bundle: %v", ErrInvalidBundle, err)
	}
	defer gz.Close()

	payload, err := io.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("%w: reading catalog seed bundle: %v", ErrInvalidBundle, err)
	}

	var bundle Bundle
	if err := json.Unmarshal(payload, &bundle); err != nil {
		return nil, fmt.Errorf("%w: decoding catalog seed bundle: %v", ErrInvalidBundle, err)
	}
	return &bundle, nil
}

func prepareLibraries(records []LibraryRecord, rewrites []PathRewrite) ([]LibraryRecord, map[int][]string, []string, error) {
	libraries := make([]LibraryRecord, 0, len(records))
	folderPaths := make(map[int][]string, len(records))
	var unmatched []string

	for _, record := range records {
		finalPaths := make([]string, 0, len(record.Paths))
		for _, path := range record.Paths {
			rewritten, _ := rewritePath(path, rewrites)
			rewritten = normalizePath(rewritten)
			if !filepath.IsAbs(rewritten) {
				return nil, nil, nil, fmt.Errorf("catalog seed path %q is not absolute after rewrite", rewritten)
			}
			finalPaths = append(finalPaths, rewritten)
			if _, err := os.Stat(rewritten); err != nil {
				if os.IsNotExist(err) {
					unmatched = append(unmatched, rewritten)
					continue
				}
				return nil, nil, nil, fmt.Errorf("checking library path %q: %w", rewritten, err)
			}
		}
		sort.Strings(finalPaths)
		record.Paths = dedupeStrings(finalPaths)
		folderPaths[record.ExportedID] = append([]string(nil), record.Paths...)
		libraries = append(libraries, record)
	}

	return libraries, folderPaths, dedupeStrings(unmatched), nil
}

func validateFileRoots(files []FileRecord, folderPaths map[int][]string, rewrites []PathRewrite) error {
	var unmatched []string
	for _, file := range files {
		rewritten, matched := rewritePath(file.FilePath, rewrites)
		rewritten = normalizePath(rewritten)
		paths := folderPaths[file.MediaFolderID]
		ok := false
		for _, root := range paths {
			if hasPathPrefix(rewritten, root) {
				ok = true
				break
			}
		}
		if !ok {
			if matched {
				unmatched = append(unmatched, rewritten)
			} else {
				unmatched = append(unmatched, file.FilePath)
			}
		}
	}
	if len(unmatched) > 0 {
		sort.Strings(unmatched)
		return &UnmatchedRootsError{Roots: dedupeStrings(unmatched)}
	}
	return nil
}

func rewriteFileRecord(file FileRecord, rewrites []PathRewrite, folderPaths map[int][]string) (FileRecord, error) {
	rewrittenPath, _ := rewritePath(file.FilePath, rewrites)
	file.FilePath = normalizePath(rewrittenPath)
	if !filepath.IsAbs(file.FilePath) {
		return FileRecord{}, fmt.Errorf("catalog seed file path %q is not absolute after rewrite", file.FilePath)
	}
	for i := range file.ExternalSubtitles {
		if file.ExternalSubtitles[i].Path == "" {
			continue
		}
		subtitlePath, _ := rewritePath(file.ExternalSubtitles[i].Path, rewrites)
		file.ExternalSubtitles[i].Path = normalizePath(subtitlePath)
		if !filepath.IsAbs(file.ExternalSubtitles[i].Path) {
			return FileRecord{}, fmt.Errorf("catalog seed subtitle path %q is not absolute after rewrite", file.ExternalSubtitles[i].Path)
		}
	}

	paths := folderPaths[file.MediaFolderID]
	for _, root := range paths {
		if hasPathPrefix(file.FilePath, root) {
			return file, nil
		}
	}

	return FileRecord{}, &UnmatchedRootsError{Roots: []string{file.FilePath}}
}

func importLibrary(ctx context.Context, tx pgx.Tx, library LibraryRecord, overwrite bool) (localID int, created bool, matched bool, err error) {
	existingIDs, err := lookupFolderIDsByPaths(ctx, tx, library.Paths)
	if err != nil {
		return 0, false, false, err
	}
	switch len(existingIDs) {
	case 0:
		err = tx.QueryRow(ctx, `
			INSERT INTO media_folders (
				type, name, enabled, last_scanned_at,
				scan_warning_code, scan_warning_message, scan_warning_at, allow_empty_cleanup_once
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id`,
			library.Type,
			library.Name,
			library.Enabled,
			library.LastScannedAt,
			library.ScanWarningCode,
			library.ScanWarningMessage,
			library.ScanWarningAt,
			library.AllowEmptyCleanupOnce,
		).Scan(&localID)
		if err != nil {
			return 0, false, false, fmt.Errorf("creating imported library %q: %w", library.Name, err)
		}
		for _, path := range library.Paths {
			if _, err := tx.Exec(ctx,
				`INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`,
				localID, path,
			); err != nil {
				return 0, false, false, fmt.Errorf("creating imported library path %q: %w", path, err)
			}
		}
		return localID, true, false, nil
	case 1:
		localID = existingIDs[0]
		if overwrite {
			if _, err := tx.Exec(ctx, `
				UPDATE media_folders
				SET type = $1,
					name = $2,
					enabled = $3,
					last_scanned_at = $4,
					scan_warning_code = $5,
					scan_warning_message = $6,
					scan_warning_at = $7,
					allow_empty_cleanup_once = $8
				WHERE id = $9`,
				library.Type,
				library.Name,
				library.Enabled,
				library.LastScannedAt,
				library.ScanWarningCode,
				library.ScanWarningMessage,
				library.ScanWarningAt,
				library.AllowEmptyCleanupOnce,
				localID,
			); err != nil {
				return 0, false, false, fmt.Errorf("updating imported library %d: %w", localID, err)
			}
		}
		for _, path := range library.Paths {
			if _, err := tx.Exec(ctx,
				`INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2) ON CONFLICT (path) DO NOTHING`,
				localID, path,
			); err != nil {
				return 0, false, false, fmt.Errorf("upserting library path %q: %w", path, err)
			}
		}
		return localID, false, true, nil
	default:
		return 0, false, false, fmt.Errorf("catalog seed paths for library %q map to multiple local libraries", library.Name)
	}
}

const catalogImportBatchSize = 1000

type importPersonState struct {
	ID             int64
	Name           string
	LowerName      string
	TmdbID         string
	ImdbID         string
	TvdbID         string
	PlexGUID       string
	PhotoPath      string
	PhotoThumbhash string
	IsNew          bool
	NeedsUpdate    bool
}

func isEmptyCatalogImportTarget(ctx context.Context, tx pgx.Tx) (bool, error) {
	var empty bool
	err := tx.QueryRow(ctx, `
		SELECT
			NOT EXISTS (SELECT 1 FROM media_folders) AND
			NOT EXISTS (SELECT 1 FROM media_items) AND
			NOT EXISTS (SELECT 1 FROM item_people) AND
			NOT EXISTS (SELECT 1 FROM seasons) AND
			NOT EXISTS (SELECT 1 FROM episodes) AND
			NOT EXISTS (SELECT 1 FROM media_files) AND
			NOT EXISTS (SELECT 1 FROM media_item_libraries)`).
		Scan(&empty)
	if err != nil {
		return false, fmt.Errorf("checking empty catalog import target: %w", err)
	}
	return empty, nil
}

func bulkInsertItems(ctx context.Context, tx pgx.Tx, items []ItemRecord, onBatch func(processed int)) error {
	rows := make([][]any, 0, len(items))
	for _, item := range items {
		rows = append(rows, []any{
			item.ContentID, item.Type, item.Title, item.SortTitle, item.OriginalTitle, item.Year, item.Genres,
			item.ContentRating, item.Runtime, item.Overview, item.Tagline,
			item.RatingIMDB, item.RatingTMDB, item.RatingRTCritic, item.RatingRTAudience,
			item.ImdbID, item.TmdbID, item.TvdbID,
			item.PosterPath, item.PosterThumbhash, item.BackdropPath, item.BackdropThumbhash, item.LogoPath,
			item.MetadataS3Path, item.MetadataEtag, item.SeasonCount,
			item.Studios, item.Networks, item.Countries, item.Keywords, item.OriginalLanguage, item.ReleaseDate, item.FirstAirDate, item.LastAirDate, item.AirTime, item.AirTimezone,
			item.MatchedAt, item.LastRefreshed, item.RefreshFailures, item.LockedFields, item.Status,
			item.CreatedAt, item.UpdatedAt,
		})
	}

	return executeInsertBatches(ctx, tx, `
		INSERT INTO media_items (
			content_id, type, title, sort_title, original_title, year, genres,
			content_rating, runtime, overview, tagline,
			rating_imdb, rating_tmdb, rating_rt_critic, rating_rt_audience,
			imdb_id, tmdb_id, tvdb_id,
			poster_path, poster_thumbhash, backdrop_path, backdrop_thumbhash, logo_path,
			metadata_s3_path, metadata_etag, season_count,
			studios, networks, countries, keywords, original_language, release_date, first_air_date, last_air_date, air_time, air_timezone,
			matched_at, last_refreshed, refresh_failures, locked_fields, status,
			created_at, updated_at
		) VALUES `,
		rows,
		43,
		nil,
		"",
		onBatch,
	)
}

func (s *Service) replacePeople(ctx context.Context, tx pgx.Tx, people []PersonRecord, itemStates map[string]bool, mode ConflictMode, result *ImportResult) error {
	shouldReplace := make(map[string]struct{})
	if itemStates == nil {
		// Empty-catalog fast path: replace everything.
		for _, p := range people {
			shouldReplace[p.ContentID] = struct{}{}
		}
	} else {
		for contentID, changed := range itemStates {
			if mode == ConflictModeOverwrite || changed {
				shouldReplace[contentID] = struct{}{}
			}
		}
	}

	if len(shouldReplace) == 0 {
		return nil
	}

	contentIDs := mapKeys(shouldReplace)
	sort.Strings(contentIDs)
	if err := clearImportedPeople(ctx, tx, contentIDs); err != nil {
		return err
	}

	filtered := make([]PersonRecord, 0, len(people))
	tmdbIDs := make([]string, 0, len(people))
	imdbIDs := make([]string, 0, len(people))
	nameKeys := make([]string, 0, len(people))
	for _, rec := range people {
		if _, ok := shouldReplace[rec.ContentID]; !ok {
			continue
		}
		filtered = append(filtered, rec)
		if rec.TmdbID != "" {
			tmdbIDs = append(tmdbIDs, rec.TmdbID)
		}
		if rec.ImdbID != "" {
			imdbIDs = append(imdbIDs, rec.ImdbID)
		}
		if rec.Name != "" {
			nameKeys = append(nameKeys, strings.ToLower(rec.Name))
		}
	}

	if len(filtered) == 0 {
		result.CreditsReplaced += len(contentIDs)
		return nil
	}

	byTMDB, byIMDb, byName, err := loadExistingImportPeople(ctx, tx, tmdbIDs, imdbIDs, nameKeys)
	if err != nil {
		return err
	}

	newPeople := make([]*importPersonState, 0)
	updatedPeople := make(map[int64]*importPersonState)

	type itemPersonKey struct {
		contentID string
		personID  int64
		kind      int
		character string
	}
	seenItemPeople := make(map[itemPersonKey]struct{}, len(filtered))
	itemPeopleRows := make([][]any, 0, len(filtered))
	for _, rec := range filtered {
		person, resolveErr := resolveImportPerson(rec, byTMDB, byIMDb, byName, &newPeople, updatedPeople)
		if resolveErr != nil {
			return fmt.Errorf("resolving person %q for %s: %w", rec.Name, rec.ContentID, resolveErr)
		}

		// PostgreSQL's ON CONFLICT cannot handle the same conflict-target tuple
		// appearing twice in one INSERT. Deduplicate after person resolution so
		// that collisions caused by person-merging are also caught.
		key := itemPersonKey{rec.ContentID, person.ID, rec.Kind, rec.Character}
		if _, exists := seenItemPeople[key]; exists {
			continue
		}
		seenItemPeople[key] = struct{}{}

		itemPersonID, idErr := nextInt64ID()
		if idErr != nil {
			return fmt.Errorf("generating item_people id for %s: %w", rec.ContentID, idErr)
		}
		itemPeopleRows = append(itemPeopleRows, []any{
			itemPersonID,
			rec.ContentID,
			person.ID,
			rec.Kind,
			rec.Character,
			rec.SortOrder,
		})
	}

	if err := bulkInsertPeople(ctx, tx, newPeople); err != nil {
		return err
	}
	if err := bulkUpdatePeople(ctx, tx, updatedPeople); err != nil {
		return err
	}
	if err := bulkInsertItemPeople(ctx, tx, itemPeopleRows); err != nil {
		return err
	}

	result.CreditsReplaced += len(contentIDs)
	return nil
}

// embeddingImportBatchSize is larger than the general batch size because
// embeddings only have 4 columns (4 × 5000 = 20,000 params, well within the 65,535 limit).
const embeddingImportBatchSize = 5000

func (s *Service) importEmbeddings(ctx context.Context, tx pgx.Tx, embeddings []EmbeddingRecord, emptyTarget bool, result *ImportResult, onBatch func(int)) error {
	if len(embeddings) == 0 {
		return nil
	}

	// For empty targets, drop the HNSW index before bulk loading and recreate after.
	// Maintaining an HNSW graph incrementally during inserts is far slower than building
	// it once on the complete dataset.
	if emptyTarget {
		if _, err := tx.Exec(ctx, `DROP INDEX IF EXISTS idx_media_item_embeddings_hnsw`); err != nil {
			return fmt.Errorf("dropping embedding index for bulk load: %w", err)
		}
	}

	rows := make([][]any, 0, len(embeddings))
	for _, rec := range embeddings {
		padded := rec.Embedding
		if len(padded) > recommendations.CanonicalEmbeddingDimensions {
			return fmt.Errorf(
				"embedding vector length %d exceeds canonical dimension %d for media item %s",
				len(padded),
				recommendations.CanonicalEmbeddingDimensions,
				rec.MediaItemID,
			)
		}
		if len(padded) < recommendations.CanonicalEmbeddingDimensions {
			p := make([]float32, recommendations.CanonicalEmbeddingDimensions)
			copy(p, padded)
			padded = p
		}
		rows = append(rows, []any{
			rec.MediaItemID,
			pgvector.NewVector(padded),
			rec.Model,
			rec.CanonicalText,
		})
	}

	batchCallback := func(processed int) {
		result.EmbeddingsImported += processed
		if onBatch != nil {
			onBatch(processed)
		}
	}

	var suffix string
	if emptyTarget {
		suffix = ""
	} else {
		suffix = `
		ON CONFLICT (media_item_id) DO UPDATE
			SET embedding      = EXCLUDED.embedding,
			    model          = EXCLUDED.model,
			    canonical_text = EXCLUDED.canonical_text,
			    updated_at     = NOW()`
	}

	for start := 0; start < len(rows); start += embeddingImportBatchSize {
		end := min(start+embeddingImportBatchSize, len(rows))

		var builder strings.Builder
		builder.WriteString(`
		INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text)
		VALUES `)
		args := make([]any, 0, (end-start)*4)
		for rowIndex := start; rowIndex < end; rowIndex++ {
			if rowIndex > start {
				builder.WriteString(", ")
			}
			builder.WriteString(buildValuesClause(len(args)+1, 4, nil))
			args = append(args, rows[rowIndex]...)
		}
		builder.WriteString(suffix)

		if _, err := tx.Exec(ctx, builder.String(), args...); err != nil {
			return fmt.Errorf("inserting embeddings batch: %w", err)
		}
		batchCallback(end - start)
	}

	// Recreate the HNSW index after bulk load.
	if emptyTarget {
		if _, err := tx.Exec(ctx, fmt.Sprintf(
			`CREATE INDEX idx_media_item_embeddings_hnsw ON media_item_embeddings USING hnsw ((embedding::halfvec(%d)) halfvec_cosine_ops)`,
			recommendations.CanonicalEmbeddingDimensions,
		)); err != nil {
			return fmt.Errorf("recreating embedding index after bulk load: %w", err)
		}
	}

	return nil
}

func bulkInsertSeasons(ctx context.Context, tx pgx.Tx, seasons []SeasonRecord, onBatch func(processed int)) error {
	rows := make([][]any, 0, len(seasons))
	for _, season := range seasons {
		rows = append(rows, []any{
			season.ContentID, season.SeriesID, season.SeasonNumber, season.Title, season.Overview, season.AirDate,
			season.PosterPath, season.PosterThumbhash, season.MetadataS3Path, season.MetadataEtag,
			season.CreatedAt, season.UpdatedAt,
		})
	}

	return executeInsertBatches(ctx, tx, `
		INSERT INTO seasons (
			content_id, series_id, season_number, title, overview, air_date,
			poster_path, poster_thumbhash, metadata_s3_path, metadata_etag, created_at, updated_at
		) VALUES `,
		rows,
		12,
		nil,
		"",
		onBatch,
	)
}

func bulkInsertEpisodes(ctx context.Context, tx pgx.Tx, episodes []EpisodeRecord, onBatch func(processed int)) error {
	rows := make([][]any, 0, len(episodes))
	for _, episode := range episodes {
		rows = append(rows, []any{
			episode.ContentID, episode.SeriesID, nullableString(episode.SeasonID), episode.SeasonNumber, episode.EpisodeNumber,
			episode.Title, episode.Overview, episode.AirDate, episode.Runtime, episode.RatingIMDB, episode.RatingTMDB,
			episode.StillPath, episode.StillThumbhash, episode.MetadataS3Path, episode.MetadataEtag,
			episode.CreatedAt, episode.UpdatedAt,
		})
	}

	return executeInsertBatches(ctx, tx, `
		INSERT INTO episodes (
			content_id, series_id, season_id, season_number, episode_number,
			title, overview, air_date, runtime, rating_imdb, rating_tmdb,
			still_path, still_thumbhash, metadata_s3_path, metadata_etag, created_at, updated_at
		) VALUES `,
		rows,
		17,
		nil,
		"",
		onBatch,
	)
}

func bulkInsertFiles(ctx context.Context, tx pgx.Tx, files []FileRecord, onBatch func(processed int)) error {
	rows := make([][]any, 0, len(files))
	for _, file := range files {
		videoTracksJSON, err := json.Marshal(file.VideoTracks)
		if err != nil {
			return fmt.Errorf("marshaling video tracks for %s: %w", file.FilePath, err)
		}
		audioTracksJSON, err := json.Marshal(file.AudioTracks)
		if err != nil {
			return fmt.Errorf("marshaling audio tracks for %s: %w", file.FilePath, err)
		}
		subtitleTracksJSON, err := json.Marshal(file.SubtitleTracks)
		if err != nil {
			return fmt.Errorf("marshaling subtitle tracks for %s: %w", file.FilePath, err)
		}
		externalSubtitlesJSON, err := json.Marshal(file.ExternalSubtitles)
		if err != nil {
			return fmt.Errorf("marshaling external subtitles for %s: %w", file.FilePath, err)
		}
		rows = append(rows, []any{
			nullableString(file.ContentID), nullableString(file.EpisodeID), nilIfZero(file.SeasonNumber), nilIfZero(file.EpisodeNumber),
			file.MediaFolderID, file.FilePath, file.FileSize, nullableString(file.FileHash),
			file.CodecVideo, file.CodecAudio, file.Resolution, nilIfZero(file.AudioChannels), file.HDR, file.Container,
			nilIfZero(file.Duration), nilIfZero(file.Bitrate), string(videoTracksJSON), string(audioTracksJSON), string(subtitleTracksJSON), string(externalSubtitlesJSON),
			file.IntroStart, file.IntroEnd, file.CreditsStart, file.CreditsEnd,
			nullableString(file.ProbeSource), file.ProbeUpdatedAt, file.MissingSince, file.CreatedAt, file.UpdatedAt,
		})
	}

	return executeInsertBatches(ctx, tx, `
		INSERT INTO media_files (
			content_id, episode_id, season_number, episode_number,
			media_folder_id, file_path, file_size, file_hash,
			codec_video, codec_audio, resolution, audio_channels, hdr, container,
			duration, bitrate, video_tracks, audio_tracks, subtitle_tracks, external_subtitles,
			intro_start, intro_end, credits_start, credits_end,
			probe_source, probe_updated_at, missing_since, created_at, updated_at
		) VALUES `,
		rows,
		29,
		map[int]string{
			16: "::jsonb",
			17: "::jsonb",
			18: "::jsonb",
			19: "::jsonb",
		},
		"",
		onBatch,
	)
}

func bulkInsertLibraryLinks(ctx context.Context, tx pgx.Tx, links []LibraryLinkRecord, onBatch func(processed int)) error {
	rows := make([][]any, 0, len(links))
	for _, link := range links {
		rows = append(rows, []any{link.ContentID, link.MediaFolderID, link.FirstSeenAt})
	}

	return executeInsertBatches(ctx, tx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES `,
		rows,
		3,
		nil,
		"",
		onBatch,
	)
}

func executeInsertBatches(ctx context.Context, tx pgx.Tx, prefix string, rows [][]any, columnsPerRow int, casts map[int]string, suffix string, onBatch func(processed int)) error {
	_, err := executeInsertBatchesCounted(ctx, tx, prefix, rows, columnsPerRow, casts, suffix, onBatch)
	return err
}

// executeInsertBatchesCounted runs batched inserts and returns total rows affected.
// For ON CONFLICT DO NOTHING, RowsAffected = rows actually inserted.
// For ON CONFLICT DO UPDATE, RowsAffected = all rows (inserts + updates).
func executeInsertBatchesCounted(ctx context.Context, tx pgx.Tx, prefix string, rows [][]any, columnsPerRow int, casts map[int]string, suffix string, onBatch func(processed int)) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	var total int64
	for start := 0; start < len(rows); start += catalogImportBatchSize {
		end := min(start+catalogImportBatchSize, len(rows))

		var builder strings.Builder
		builder.WriteString(prefix)
		args := make([]any, 0, (end-start)*columnsPerRow)
		for rowIndex := start; rowIndex < end; rowIndex++ {
			if rowIndex > start {
				builder.WriteString(", ")
			}
			builder.WriteString(buildValuesClause(len(args)+1, columnsPerRow, casts))
			args = append(args, rows[rowIndex]...)
		}
		builder.WriteString(suffix)

		tag, err := tx.Exec(ctx, builder.String(), args...)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if onBatch != nil {
			onBatch(end - start)
		}
	}

	return total, nil
}

type batchUpsertCounts struct {
	Created int
	Updated int
	Skipped int
}

// executeUpsertBatches runs batched inserts with ON CONFLICT handling and returns
// accurate created/updated/skipped counts. For skip_existing mode it uses Exec
// with RowsAffected; for overwrite mode it uses RETURNING (xmax = 0) to
// distinguish inserts from updates.
func executeUpsertBatches(ctx context.Context, tx pgx.Tx, prefix string, rows [][]any, columnsPerRow int, casts map[int]string, skipSuffix, upsertSuffix string, mode ConflictMode, onBatch func(processed int)) (batchUpsertCounts, error) {
	var counts batchUpsertCounts
	if len(rows) == 0 {
		return counts, nil
	}

	if mode == ConflictModeSkipExisting {
		affected, err := executeInsertBatchesCounted(ctx, tx, prefix, rows, columnsPerRow, casts, skipSuffix, onBatch)
		if err != nil {
			return counts, err
		}
		counts.Created = int(affected)
		counts.Skipped = len(rows) - counts.Created
		return counts, nil
	}

	// Overwrite mode: append RETURNING (xmax = 0) to distinguish inserts from updates.
	fullSuffix := upsertSuffix + ` RETURNING (xmax = 0)`
	for start := 0; start < len(rows); start += catalogImportBatchSize {
		end := min(start+catalogImportBatchSize, len(rows))

		var builder strings.Builder
		builder.WriteString(prefix)
		args := make([]any, 0, (end-start)*columnsPerRow)
		for rowIndex := start; rowIndex < end; rowIndex++ {
			if rowIndex > start {
				builder.WriteString(", ")
			}
			builder.WriteString(buildValuesClause(len(args)+1, columnsPerRow, casts))
			args = append(args, rows[rowIndex]...)
		}
		builder.WriteString(fullSuffix)

		queryRows, queryErr := tx.Query(ctx, builder.String(), args...)
		if queryErr != nil {
			return counts, queryErr
		}
		for queryRows.Next() {
			var isNew bool
			if scanErr := queryRows.Scan(&isNew); scanErr != nil {
				queryRows.Close()
				return counts, scanErr
			}
			if isNew {
				counts.Created++
			} else {
				counts.Updated++
			}
		}
		queryRows.Close()
		if err := queryRows.Err(); err != nil {
			return counts, err
		}

		if onBatch != nil {
			onBatch(end - start)
		}
	}

	return counts, nil
}

const copyBatchSize = 10000

// copyInsertBatches uses PostgreSQL's COPY protocol for fast bulk loading.
// Much faster than multi-row INSERT as it bypasses query parsing/planning.
// Only suitable for empty tables (no ON CONFLICT support).
func copyInsertBatches(ctx context.Context, tx pgx.Tx, table string, columns []string, rows [][]any, onBatch func(int)) error {
	if len(rows) == 0 {
		return nil
	}

	for start := 0; start < len(rows); start += copyBatchSize {
		end := min(start+copyBatchSize, len(rows))
		_, err := tx.CopyFrom(ctx,
			pgx.Identifier{table},
			columns,
			pgx.CopyFromRows(rows[start:end]),
		)
		if err != nil {
			return fmt.Errorf("copy into %s: %w", table, err)
		}
		if onBatch != nil {
			onBatch(end - start)
		}
	}
	return nil
}

func buildValuesClause(base, columns int, casts map[int]string) string {
	parts := make([]string, 0, columns)
	for i := range columns {
		part := fmt.Sprintf("$%d", base+i)
		if cast, ok := casts[i]; ok {
			part += cast
		}
		parts = append(parts, part)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func loadExistingImportPeople(ctx context.Context, tx pgx.Tx, tmdbIDs, imdbIDs, nameKeys []string) (map[string]*importPersonState, map[string]*importPersonState, map[string]*importPersonState, error) {
	byTMDB := make(map[string]*importPersonState)
	byIMDb := make(map[string]*importPersonState)
	byName := make(map[string]*importPersonState)
	statesByID := make(map[int64]*importPersonState)

	register := func(state importPersonState) {
		canonical := statesByID[state.ID]
		if canonical == nil {
			copied := state
			canonical = &copied
			statesByID[state.ID] = canonical
		}
		if canonical.Name == "" {
			canonical.Name = state.Name
			canonical.LowerName = state.LowerName
		}
		if canonical.TmdbID == "" {
			canonical.TmdbID = state.TmdbID
		}
		if canonical.ImdbID == "" {
			canonical.ImdbID = state.ImdbID
		}
		if canonical.TvdbID == "" {
			canonical.TvdbID = state.TvdbID
		}
		if canonical.PlexGUID == "" {
			canonical.PlexGUID = state.PlexGUID
		}
		if canonical.PhotoPath == "" {
			canonical.PhotoPath = state.PhotoPath
		}
		if canonical.PhotoThumbhash == "" {
			canonical.PhotoThumbhash = state.PhotoThumbhash
		}
		registerImportPerson(canonical, byTMDB, byIMDb, byName)
	}

	if err := queryImportPeople(ctx, tx, `
		SELECT id, name, LOWER(name), tmdb_id, imdb_id, tvdb_id, plex_guid, photo_path, photo_thumbhash
		FROM people
		WHERE tmdb_id = ANY($1)`,
		dedupeStrings(tmdbIDs), register,
	); err != nil {
		return nil, nil, nil, err
	}
	if err := queryImportPeople(ctx, tx, `
		SELECT id, name, LOWER(name), tmdb_id, imdb_id, tvdb_id, plex_guid, photo_path, photo_thumbhash
		FROM people
		WHERE imdb_id = ANY($1)`,
		dedupeStrings(imdbIDs), register,
	); err != nil {
		return nil, nil, nil, err
	}
	if err := queryImportPeople(ctx, tx, `
		SELECT DISTINCT ON (LOWER(name))
			id, name, LOWER(name), tmdb_id, imdb_id, tvdb_id, plex_guid, photo_path, photo_thumbhash
		FROM people
		WHERE LOWER(name) = ANY($1)
		ORDER BY LOWER(name), id`,
		dedupeStrings(nameKeys), register,
	); err != nil {
		return nil, nil, nil, err
	}

	return byTMDB, byIMDb, byName, nil
}

func queryImportPeople(ctx context.Context, tx pgx.Tx, query string, keys []string, add func(importPersonState)) error {
	if len(keys) == 0 {
		return nil
	}

	rows, err := tx.Query(ctx, query, keys)
	if err != nil {
		return fmt.Errorf("loading imported people matches: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var state importPersonState
		if err := rows.Scan(
			&state.ID,
			&state.Name,
			&state.LowerName,
			&state.TmdbID,
			&state.ImdbID,
			&state.TvdbID,
			&state.PlexGUID,
			&state.PhotoPath,
			&state.PhotoThumbhash,
		); err != nil {
			return fmt.Errorf("scanning imported person match: %w", err)
		}
		add(state)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating imported person matches: %w", err)
	}
	return nil
}

func resolveImportPerson(rec PersonRecord, byTMDB, byIMDb, byName map[string]*importPersonState, newPeople *[]*importPersonState, updatedPeople map[int64]*importPersonState) (*importPersonState, error) {
	nameKey := strings.ToLower(rec.Name)
	person := findImportPerson(rec, nameKey, byTMDB, byIMDb, byName)
	if person == nil {
		personID, err := nextInt64ID()
		if err != nil {
			return nil, err
		}
		person = &importPersonState{
			ID:             personID,
			Name:           rec.Name,
			LowerName:      nameKey,
			TmdbID:         rec.TmdbID,
			ImdbID:         rec.ImdbID,
			TvdbID:         rec.TvdbID,
			PlexGUID:       rec.PlexGUID,
			PhotoPath:      rec.PhotoPath,
			PhotoThumbhash: rec.PhotoThumbhash,
			IsNew:          true,
		}
		*newPeople = append(*newPeople, person)
		registerImportPerson(person, byTMDB, byIMDb, byName)
		return person, nil
	}

	enrichImportPerson(person, rec)
	registerImportPerson(person, byTMDB, byIMDb, byName)
	if person.NeedsUpdate {
		updatedPeople[person.ID] = person
	}
	return person, nil
}

func findImportPerson(rec PersonRecord, nameKey string, byTMDB, byIMDb, byName map[string]*importPersonState) *importPersonState {
	if rec.TmdbID != "" {
		if person := byTMDB[rec.TmdbID]; person != nil {
			return person
		}
	}
	if rec.ImdbID != "" {
		if person := byIMDb[rec.ImdbID]; person != nil {
			return person
		}
	}
	if nameKey != "" {
		if person := byName[nameKey]; person != nil {
			return person
		}
	}
	return nil
}

func enrichImportPerson(person *importPersonState, rec PersonRecord) {
	if person.TmdbID == "" && rec.TmdbID != "" {
		person.TmdbID = rec.TmdbID
		person.NeedsUpdate = person.NeedsUpdate || !person.IsNew
	}
	if person.ImdbID == "" && rec.ImdbID != "" {
		person.ImdbID = rec.ImdbID
		person.NeedsUpdate = person.NeedsUpdate || !person.IsNew
	}
	if person.TvdbID == "" && rec.TvdbID != "" {
		person.TvdbID = rec.TvdbID
		person.NeedsUpdate = person.NeedsUpdate || !person.IsNew
	}
	if person.PlexGUID == "" && rec.PlexGUID != "" {
		person.PlexGUID = rec.PlexGUID
		person.NeedsUpdate = person.NeedsUpdate || !person.IsNew
	}
	if person.PhotoPath == "" && rec.PhotoPath != "" {
		person.PhotoPath = rec.PhotoPath
		person.NeedsUpdate = person.NeedsUpdate || !person.IsNew
	}
	if person.PhotoThumbhash == "" && rec.PhotoThumbhash != "" {
		person.PhotoThumbhash = rec.PhotoThumbhash
		person.NeedsUpdate = person.NeedsUpdate || !person.IsNew
	}
}

func registerImportPerson(person *importPersonState, byTMDB, byIMDb, byName map[string]*importPersonState) {
	if person.TmdbID != "" {
		byTMDB[person.TmdbID] = person
	}
	if person.ImdbID != "" {
		byIMDb[person.ImdbID] = person
	}
	if person.LowerName != "" {
		byName[person.LowerName] = person
	}
}

func clearImportedPeople(ctx context.Context, tx pgx.Tx, contentIDs []string) error {
	if len(contentIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM item_people WHERE content_id = ANY($1)`, contentIDs); err != nil {
		return fmt.Errorf("clearing imported people: %w", err)
	}
	return nil
}

func bulkInsertPeople(ctx context.Context, tx pgx.Tx, people []*importPersonState) error {
	rows := make([][]any, 0, len(people))
	for _, person := range people {
		rows = append(rows, []any{
			person.ID,
			person.Name,
			"",
			"",
			nil,
			nil,
			"",
			"",
			person.PhotoPath,
			person.PhotoThumbhash,
			person.TmdbID,
			person.ImdbID,
			person.TvdbID,
			person.PlexGUID,
		})
	}

	if err := executeInsertBatches(ctx, tx, `
		INSERT INTO people (
			id, name, sort_name, bio, birth_date, death_date, birthplace, homepage,
			photo_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid
		) VALUES `,
		rows,
		14,
		nil,
		"",
		nil,
	); err != nil {
		return fmt.Errorf("inserting imported people: %w", err)
	}
	return nil
}

func bulkUpdatePeople(ctx context.Context, tx pgx.Tx, people map[int64]*importPersonState) error {
	if len(people) == 0 {
		return nil
	}

	ids := make([]int64, 0, len(people))
	for id := range people {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	rows := make([][]any, 0, len(ids))
	for _, id := range ids {
		person := people[id]
		rows = append(rows, []any{
			person.ID,
			person.TmdbID,
			person.ImdbID,
			person.TvdbID,
			person.PlexGUID,
			person.PhotoPath,
			person.PhotoThumbhash,
		})
	}

	if err := executeInsertBatches(ctx, tx, `
		UPDATE people AS p
		SET tmdb_id = v.tmdb_id,
			imdb_id = v.imdb_id,
			tvdb_id = v.tvdb_id,
			plex_guid = v.plex_guid,
			photo_path = v.photo_path,
			photo_thumbhash = v.photo_thumbhash,
			updated_at = NOW()
		FROM (VALUES `,
		rows,
		7,
		nil,
		`) AS v(id, tmdb_id, imdb_id, tvdb_id, plex_guid, photo_path, photo_thumbhash)
		WHERE p.id = v.id`,
		nil,
	); err != nil {
		return fmt.Errorf("updating imported people: %w", err)
	}
	return nil
}

func bulkInsertItemPeople(ctx context.Context, tx pgx.Tx, rows [][]any) error {
	if err := executeInsertBatches(ctx, tx, `
		INSERT INTO item_people (id, content_id, person_id, kind, character, sort_order)
		VALUES `,
		rows,
		6,
		nil,
		`
		ON CONFLICT (content_id, person_id, kind, character) DO UPDATE
			SET sort_order = EXCLUDED.sort_order`,
		nil,
	); err != nil {
		return fmt.Errorf("inserting imported item_people: %w", err)
	}
	return nil
}

func nextInt64ID() (int64, error) {
	idStr, err := idgen.NextID()
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func lookupFolderIDsByPaths(ctx context.Context, tx pgx.Tx, paths []string) ([]int, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT media_folder_id FROM media_folder_paths WHERE path = ANY($1)`, paths)
	if err != nil {
		return nil, fmt.Errorf("loading existing folder paths: %w", err)
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning existing folder id: %w", err)
		}
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids, rows.Err()
}

// batchImportItems inserts items in multi-row batches with ON CONFLICT handling.
// Returns a map of content_id → changed (true if created or updated) for people import,
// plus aggregate created/updated/skipped counts.
func batchImportItems(ctx context.Context, tx pgx.Tx, items []ItemRecord, mode ConflictMode, onBatch func(processed int)) (itemStates map[string]bool, created, updated, skipped int, err error) {
	itemStates = make(map[string]bool, len(items))
	if len(items) == 0 {
		return itemStates, 0, 0, 0, nil
	}

	rows := make([][]any, 0, len(items))
	contentIDs := make([]string, 0, len(items))
	for _, item := range items {
		studios, networks, countries, keywords := itemRecordStringArrays(item)
		contentIDs = append(contentIDs, item.ContentID)
		rows = append(rows, []any{
			item.ContentID, item.Type, item.Title, item.SortTitle, item.OriginalTitle, item.Year, item.Genres,
			item.ContentRating, item.Runtime, item.Overview, item.Tagline,
			item.RatingIMDB, item.RatingTMDB, item.RatingRTCritic, item.RatingRTAudience,
			item.ImdbID, item.TmdbID, item.TvdbID,
			item.PosterPath, item.PosterThumbhash, item.BackdropPath, item.BackdropThumbhash, item.LogoPath,
			item.MetadataS3Path, item.MetadataEtag, item.SeasonCount,
			studios, networks, countries, keywords, item.OriginalLanguage, item.ReleaseDate, item.FirstAirDate, item.LastAirDate, item.AirTime, item.AirTimezone,
			item.MatchedAt, item.LastRefreshed, item.RefreshFailures, item.LockedFields, item.Status,
			item.CreatedAt, item.UpdatedAt,
		})
	}

	const colCount = 43
	prefix := `
		INSERT INTO media_items (
			content_id, type, title, sort_title, original_title, year, genres,
			content_rating, runtime, overview, tagline,
			rating_imdb, rating_tmdb, rating_rt_critic, rating_rt_audience,
			imdb_id, tmdb_id, tvdb_id,
			poster_path, poster_thumbhash, backdrop_path, backdrop_thumbhash, logo_path,
			metadata_s3_path, metadata_etag, season_count,
			studios, networks, countries, keywords, original_language, release_date, first_air_date, last_air_date, air_time, air_timezone,
			matched_at, last_refreshed, refresh_failures, locked_fields, status,
			created_at, updated_at
		) VALUES `

	var suffix string
	if mode == ConflictModeSkipExisting {
		suffix = ` ON CONFLICT (content_id) DO NOTHING RETURNING content_id`
	} else {
		suffix = `
		ON CONFLICT (content_id) DO UPDATE SET
			type = EXCLUDED.type,
			title = EXCLUDED.title,
			sort_title = EXCLUDED.sort_title,
			original_title = EXCLUDED.original_title,
			year = EXCLUDED.year,
			genres = EXCLUDED.genres,
			content_rating = EXCLUDED.content_rating,
			runtime = EXCLUDED.runtime,
			overview = EXCLUDED.overview,
			tagline = EXCLUDED.tagline,
			rating_imdb = EXCLUDED.rating_imdb,
			rating_tmdb = EXCLUDED.rating_tmdb,
			rating_rt_critic = EXCLUDED.rating_rt_critic,
			rating_rt_audience = EXCLUDED.rating_rt_audience,
			imdb_id = EXCLUDED.imdb_id,
			tmdb_id = EXCLUDED.tmdb_id,
			tvdb_id = EXCLUDED.tvdb_id,
			poster_path = EXCLUDED.poster_path,
			poster_thumbhash = EXCLUDED.poster_thumbhash,
			backdrop_path = EXCLUDED.backdrop_path,
			backdrop_thumbhash = EXCLUDED.backdrop_thumbhash,
			logo_path = EXCLUDED.logo_path,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			season_count = EXCLUDED.season_count,
			studios = EXCLUDED.studios,
			networks = EXCLUDED.networks,
			countries = EXCLUDED.countries,
			keywords = EXCLUDED.keywords,
			original_language = EXCLUDED.original_language,
			release_date = EXCLUDED.release_date,
			first_air_date = EXCLUDED.first_air_date,
			last_air_date = EXCLUDED.last_air_date,
			air_time = EXCLUDED.air_time,
			air_timezone = EXCLUDED.air_timezone,
			matched_at = EXCLUDED.matched_at,
			last_refreshed = EXCLUDED.last_refreshed,
			refresh_failures = EXCLUDED.refresh_failures,
			locked_fields = EXCLUDED.locked_fields,
			status = EXCLUDED.status,
			updated_at = EXCLUDED.updated_at
		RETURNING content_id, (xmax = 0)`
	}

	for start := 0; start < len(rows); start += catalogImportBatchSize {
		end := min(start+catalogImportBatchSize, len(rows))

		var builder strings.Builder
		builder.WriteString(prefix)
		args := make([]any, 0, (end-start)*colCount)
		for rowIndex := start; rowIndex < end; rowIndex++ {
			if rowIndex > start {
				builder.WriteString(", ")
			}
			builder.WriteString(buildValuesClause(len(args)+1, colCount, nil))
			args = append(args, rows[rowIndex]...)
		}
		builder.WriteString(suffix)

		queryRows, queryErr := tx.Query(ctx, builder.String(), args...)
		if queryErr != nil {
			return nil, 0, 0, 0, fmt.Errorf("batch importing items: %w", queryErr)
		}

		if mode == ConflictModeSkipExisting {
			// RETURNING content_id — only returned for actually-inserted rows.
			createdSet := make(map[string]struct{})
			for queryRows.Next() {
				var contentID string
				if scanErr := queryRows.Scan(&contentID); scanErr != nil {
					queryRows.Close()
					return nil, 0, 0, 0, fmt.Errorf("scanning imported item id: %w", scanErr)
				}
				createdSet[contentID] = struct{}{}
			}
			queryRows.Close()
			if queryErr = queryRows.Err(); queryErr != nil {
				return nil, 0, 0, 0, fmt.Errorf("iterating imported items: %w", queryErr)
			}
			for _, cid := range contentIDs[start:end] {
				if _, ok := createdSet[cid]; ok {
					itemStates[cid] = true
					created++
				} else {
					skipped++
				}
			}
		} else {
			// RETURNING content_id, (xmax = 0) — returns for every row.
			for queryRows.Next() {
				var contentID string
				var isNew bool
				if scanErr := queryRows.Scan(&contentID, &isNew); scanErr != nil {
					queryRows.Close()
					return nil, 0, 0, 0, fmt.Errorf("scanning upserted item: %w", scanErr)
				}
				itemStates[contentID] = true
				if isNew {
					created++
				} else {
					updated++
				}
			}
			queryRows.Close()
			if queryErr = queryRows.Err(); queryErr != nil {
				return nil, 0, 0, 0, fmt.Errorf("iterating upserted items: %w", queryErr)
			}
		}

		if onBatch != nil {
			onBatch(end - start)
		}
	}

	return itemStates, created, updated, skipped, nil
}

func importItem(ctx context.Context, tx pgx.Tx, item ItemRecord, mode ConflictMode) (created bool, updated bool, skipped bool, err error) {
	if mode == ConflictModeSkipExisting {
		tag, execErr := tx.Exec(ctx, `
			INSERT INTO media_items (
				content_id, type, title, sort_title, original_title, year, genres,
				content_rating, runtime, overview, tagline,
				rating_imdb, rating_tmdb, rating_rt_critic, rating_rt_audience,
				imdb_id, tmdb_id, tvdb_id,
				poster_path, poster_thumbhash, backdrop_path, backdrop_thumbhash, logo_path,
				metadata_s3_path, metadata_etag, season_count,
				studios, networks, countries, keywords, original_language, release_date, first_air_date, last_air_date, air_time, air_timezone,
				matched_at, last_refreshed, refresh_failures, locked_fields, status,
				created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11,
				$12, $13, $14, $15,
				$16, $17, $18,
				$19, $20, $21, $22, $23,
				$24, $25, $26,
				$27, $28, $29, $30, $31, $32, $33, $34, $35, $36,
				$37, $38, $39, $40, $41,
				$42, $43
			)
			ON CONFLICT (content_id) DO NOTHING`,
			item.ContentID, item.Type, item.Title, item.SortTitle, item.OriginalTitle, item.Year, item.Genres,
			item.ContentRating, item.Runtime, item.Overview, item.Tagline,
			item.RatingIMDB, item.RatingTMDB, item.RatingRTCritic, item.RatingRTAudience,
			item.ImdbID, item.TmdbID, item.TvdbID,
			item.PosterPath, item.PosterThumbhash, item.BackdropPath, item.BackdropThumbhash, item.LogoPath,
			item.MetadataS3Path, item.MetadataEtag, item.SeasonCount,
			item.Studios, item.Networks, item.Countries, item.Keywords, item.OriginalLanguage, item.ReleaseDate, item.FirstAirDate, item.LastAirDate, item.AirTime, item.AirTimezone,
			item.MatchedAt, item.LastRefreshed, item.RefreshFailures, item.LockedFields, item.Status,
			item.CreatedAt, item.UpdatedAt,
		)
		if execErr != nil {
			return false, false, false, fmt.Errorf("importing item %s: %w", item.ContentID, execErr)
		}
		return tag.RowsAffected() == 1, false, tag.RowsAffected() == 0, nil
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO media_items (
			content_id, type, title, sort_title, original_title, year, genres,
			content_rating, runtime, overview, tagline,
			rating_imdb, rating_tmdb, rating_rt_critic, rating_rt_audience,
			imdb_id, tmdb_id, tvdb_id,
			poster_path, poster_thumbhash, backdrop_path, backdrop_thumbhash, logo_path,
			metadata_s3_path, metadata_etag, season_count,
			studios, networks, countries, keywords, original_language, release_date, first_air_date, last_air_date, air_time, air_timezone,
			matched_at, last_refreshed, refresh_failures, locked_fields, status,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14, $15,
			$16, $17, $18,
			$19, $20, $21, $22, $23,
			$24, $25, $26,
			$27, $28, $29, $30, $31, $32, $33, $34, $35, $36,
			$37, $38, $39, $40, $41,
			$42, $43
		)
		ON CONFLICT (content_id) DO UPDATE SET
			type = EXCLUDED.type,
			title = EXCLUDED.title,
			sort_title = EXCLUDED.sort_title,
			original_title = EXCLUDED.original_title,
			year = EXCLUDED.year,
			genres = EXCLUDED.genres,
			content_rating = EXCLUDED.content_rating,
			runtime = EXCLUDED.runtime,
			overview = EXCLUDED.overview,
			tagline = EXCLUDED.tagline,
			rating_imdb = EXCLUDED.rating_imdb,
			rating_tmdb = EXCLUDED.rating_tmdb,
			rating_rt_critic = EXCLUDED.rating_rt_critic,
			rating_rt_audience = EXCLUDED.rating_rt_audience,
			imdb_id = EXCLUDED.imdb_id,
			tmdb_id = EXCLUDED.tmdb_id,
			tvdb_id = EXCLUDED.tvdb_id,
			poster_path = EXCLUDED.poster_path,
			poster_thumbhash = EXCLUDED.poster_thumbhash,
			backdrop_path = EXCLUDED.backdrop_path,
			backdrop_thumbhash = EXCLUDED.backdrop_thumbhash,
			logo_path = EXCLUDED.logo_path,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			season_count = EXCLUDED.season_count,
			studios = EXCLUDED.studios,
			networks = EXCLUDED.networks,
			countries = EXCLUDED.countries,
			keywords = EXCLUDED.keywords,
			original_language = EXCLUDED.original_language,
			release_date = EXCLUDED.release_date,
			first_air_date = EXCLUDED.first_air_date,
			last_air_date = EXCLUDED.last_air_date,
			air_time = EXCLUDED.air_time,
			air_timezone = EXCLUDED.air_timezone,
			matched_at = EXCLUDED.matched_at,
			last_refreshed = EXCLUDED.last_refreshed,
			refresh_failures = EXCLUDED.refresh_failures,
			locked_fields = EXCLUDED.locked_fields,
			status = EXCLUDED.status,
			updated_at = EXCLUDED.updated_at
		RETURNING (xmax = 0)`,
		item.ContentID, item.Type, item.Title, item.SortTitle, item.OriginalTitle, item.Year, item.Genres,
		item.ContentRating, item.Runtime, item.Overview, item.Tagline,
		item.RatingIMDB, item.RatingTMDB, item.RatingRTCritic, item.RatingRTAudience,
		item.ImdbID, item.TmdbID, item.TvdbID,
		item.PosterPath, item.PosterThumbhash, item.BackdropPath, item.BackdropThumbhash, item.LogoPath,
		item.MetadataS3Path, item.MetadataEtag, item.SeasonCount,
		item.Studios, item.Networks, item.Countries, item.Keywords, item.OriginalLanguage, item.ReleaseDate, item.FirstAirDate, item.LastAirDate, item.AirTime, item.AirTimezone,
		item.MatchedAt, item.LastRefreshed, item.RefreshFailures, item.LockedFields, item.Status,
		item.CreatedAt, item.UpdatedAt,
	).Scan(&created)
	if err != nil {
		return false, false, false, fmt.Errorf("upserting item %s: %w", item.ContentID, err)
	}
	return created, !created, false, nil
}

func importSeason(ctx context.Context, tx pgx.Tx, season SeasonRecord, mode ConflictMode) (created bool, updated bool, skipped bool, err error) {
	if mode == ConflictModeSkipExisting {
		tag, execErr := tx.Exec(ctx, `
			INSERT INTO seasons (
				content_id, series_id, season_number, title, overview, air_date,
				poster_path, poster_thumbhash, metadata_s3_path, metadata_etag, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (content_id) DO NOTHING`,
			season.ContentID, season.SeriesID, season.SeasonNumber, season.Title, season.Overview, season.AirDate,
			season.PosterPath, season.PosterThumbhash, season.MetadataS3Path, season.MetadataEtag,
			season.CreatedAt, season.UpdatedAt,
		)
		if execErr != nil {
			return false, false, false, fmt.Errorf("importing season %s: %w", season.ContentID, execErr)
		}
		return tag.RowsAffected() == 1, false, tag.RowsAffected() == 0, nil
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO seasons (
			content_id, series_id, season_number, title, overview, air_date,
			poster_path, poster_thumbhash, metadata_s3_path, metadata_etag, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (content_id) DO UPDATE SET
			series_id = EXCLUDED.series_id,
			season_number = EXCLUDED.season_number,
			title = EXCLUDED.title,
			overview = EXCLUDED.overview,
			air_date = EXCLUDED.air_date,
			poster_path = EXCLUDED.poster_path,
			poster_thumbhash = EXCLUDED.poster_thumbhash,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			updated_at = EXCLUDED.updated_at
		RETURNING (xmax = 0)`,
		season.ContentID, season.SeriesID, season.SeasonNumber, season.Title, season.Overview, season.AirDate,
		season.PosterPath, season.PosterThumbhash, season.MetadataS3Path, season.MetadataEtag,
		season.CreatedAt, season.UpdatedAt,
	).Scan(&created)
	if err != nil {
		return false, false, false, fmt.Errorf("upserting season %s: %w", season.ContentID, err)
	}
	return created, !created, false, nil
}

func importEpisode(ctx context.Context, tx pgx.Tx, episode EpisodeRecord, mode ConflictMode) (created bool, updated bool, skipped bool, err error) {
	seasonID := nullableString(episode.SeasonID)
	if mode == ConflictModeSkipExisting {
		tag, execErr := tx.Exec(ctx, `
			INSERT INTO episodes (
				content_id, series_id, season_id, season_number, episode_number,
				title, overview, air_date, runtime, rating_imdb, rating_tmdb,
				still_path, still_thumbhash, metadata_s3_path, metadata_etag, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10, $11,
				$12, $13, $14, $15, $16, $17
			)
			ON CONFLICT (content_id) DO NOTHING`,
			episode.ContentID, episode.SeriesID, seasonID, episode.SeasonNumber, episode.EpisodeNumber,
			episode.Title, episode.Overview, episode.AirDate, episode.Runtime, episode.RatingIMDB, episode.RatingTMDB,
			episode.StillPath, episode.StillThumbhash, episode.MetadataS3Path, episode.MetadataEtag,
			episode.CreatedAt, episode.UpdatedAt,
		)
		if execErr != nil {
			return false, false, false, fmt.Errorf("importing episode %s: %w", episode.ContentID, execErr)
		}
		return tag.RowsAffected() == 1, false, tag.RowsAffected() == 0, nil
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO episodes (
			content_id, series_id, season_id, season_number, episode_number,
			title, overview, air_date, runtime, rating_imdb, rating_tmdb,
			still_path, still_thumbhash, metadata_s3_path, metadata_etag, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16, $17
		)
		ON CONFLICT (content_id) DO UPDATE SET
			series_id = EXCLUDED.series_id,
			season_id = EXCLUDED.season_id,
			season_number = EXCLUDED.season_number,
			episode_number = EXCLUDED.episode_number,
			title = EXCLUDED.title,
			overview = EXCLUDED.overview,
			air_date = EXCLUDED.air_date,
			runtime = EXCLUDED.runtime,
			rating_imdb = EXCLUDED.rating_imdb,
			rating_tmdb = EXCLUDED.rating_tmdb,
			still_path = EXCLUDED.still_path,
			still_thumbhash = EXCLUDED.still_thumbhash,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			updated_at = EXCLUDED.updated_at
		RETURNING (xmax = 0)`,
		episode.ContentID, episode.SeriesID, seasonID, episode.SeasonNumber, episode.EpisodeNumber,
		episode.Title, episode.Overview, episode.AirDate, episode.Runtime, episode.RatingIMDB, episode.RatingTMDB,
		episode.StillPath, episode.StillThumbhash, episode.MetadataS3Path, episode.MetadataEtag,
		episode.CreatedAt, episode.UpdatedAt,
	).Scan(&created)
	if err != nil {
		return false, false, false, fmt.Errorf("upserting episode %s: %w", episode.ContentID, err)
	}
	return created, !created, false, nil
}

func importFile(ctx context.Context, tx pgx.Tx, file FileRecord, mode ConflictMode) (created bool, updated bool, skipped bool, err error) {
	videoTracksJSON, err := json.Marshal(file.VideoTracks)
	if err != nil {
		return false, false, false, fmt.Errorf("marshaling video tracks for %s: %w", file.FilePath, err)
	}
	audioTracksJSON, err := json.Marshal(file.AudioTracks)
	if err != nil {
		return false, false, false, fmt.Errorf("marshaling audio tracks for %s: %w", file.FilePath, err)
	}
	subtitleTracksJSON, err := json.Marshal(file.SubtitleTracks)
	if err != nil {
		return false, false, false, fmt.Errorf("marshaling subtitle tracks for %s: %w", file.FilePath, err)
	}
	externalSubtitlesJSON, err := json.Marshal(file.ExternalSubtitles)
	if err != nil {
		return false, false, false, fmt.Errorf("marshaling external subtitles for %s: %w", file.FilePath, err)
	}

	contentID := nullableString(file.ContentID)
	episodeID := nullableString(file.EpisodeID)
	fileHash := nullableString(file.FileHash)
	probeSource := nullableString(file.ProbeSource)
	if mode == ConflictModeSkipExisting {
		tag, execErr := tx.Exec(ctx, `
			INSERT INTO media_files (
				content_id, episode_id, season_number, episode_number,
				media_folder_id, file_path, file_size, file_hash,
				codec_video, codec_audio, resolution, audio_channels, hdr, container,
				duration, bitrate, video_tracks, audio_tracks, subtitle_tracks, external_subtitles,
				intro_start, intro_end, credits_start, credits_end,
				probe_source, probe_updated_at, missing_since, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, $7, $8,
				$9, $10, $11, $12, $13, $14,
				$15, $16, $17, $18, $19, $20,
				$21, $22, $23, $24,
				$25, $26, $27, $28, $29
			)
			ON CONFLICT (file_path) DO NOTHING`,
			contentID, episodeID, nilIfZero(file.SeasonNumber), nilIfZero(file.EpisodeNumber),
			file.MediaFolderID, file.FilePath, file.FileSize, fileHash,
			file.CodecVideo, file.CodecAudio, file.Resolution, nilIfZero(file.AudioChannels), file.HDR, file.Container,
			nilIfZero(file.Duration), nilIfZero(file.Bitrate), videoTracksJSON, audioTracksJSON, subtitleTracksJSON, externalSubtitlesJSON,
			file.IntroStart, file.IntroEnd, file.CreditsStart, file.CreditsEnd,
			probeSource, file.ProbeUpdatedAt, file.MissingSince, file.CreatedAt, file.UpdatedAt,
		)
		if execErr != nil {
			return false, false, false, fmt.Errorf("importing file %s: %w", file.FilePath, execErr)
		}
		return tag.RowsAffected() == 1, false, tag.RowsAffected() == 0, nil
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO media_files (
			content_id, episode_id, season_number, episode_number,
			media_folder_id, file_path, file_size, file_hash,
			codec_video, codec_audio, resolution, audio_channels, hdr, container,
			duration, bitrate, video_tracks, audio_tracks, subtitle_tracks, external_subtitles,
			intro_start, intro_end, credits_start, credits_end,
			probe_source, probe_updated_at, missing_since, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19, $20,
			$21, $22, $23, $24,
			$25, $26, $27, $28, $29
		)
		ON CONFLICT (file_path) DO UPDATE SET
			content_id = EXCLUDED.content_id,
			episode_id = EXCLUDED.episode_id,
			season_number = EXCLUDED.season_number,
			episode_number = EXCLUDED.episode_number,
			media_folder_id = EXCLUDED.media_folder_id,
			file_size = EXCLUDED.file_size,
			file_hash = EXCLUDED.file_hash,
			codec_video = EXCLUDED.codec_video,
			codec_audio = EXCLUDED.codec_audio,
			resolution = EXCLUDED.resolution,
			audio_channels = EXCLUDED.audio_channels,
			hdr = EXCLUDED.hdr,
			container = EXCLUDED.container,
			duration = EXCLUDED.duration,
			bitrate = EXCLUDED.bitrate,
			video_tracks = EXCLUDED.video_tracks,
			audio_tracks = EXCLUDED.audio_tracks,
			subtitle_tracks = EXCLUDED.subtitle_tracks,
			external_subtitles = EXCLUDED.external_subtitles,
			intro_start = EXCLUDED.intro_start,
			intro_end = EXCLUDED.intro_end,
			credits_start = EXCLUDED.credits_start,
			credits_end = EXCLUDED.credits_end,
			probe_source = EXCLUDED.probe_source,
			probe_updated_at = EXCLUDED.probe_updated_at,
			missing_since = EXCLUDED.missing_since,
			updated_at = EXCLUDED.updated_at
		RETURNING (xmax = 0)`,
		contentID, episodeID, nilIfZero(file.SeasonNumber), nilIfZero(file.EpisodeNumber),
		file.MediaFolderID, file.FilePath, file.FileSize, fileHash,
		file.CodecVideo, file.CodecAudio, file.Resolution, nilIfZero(file.AudioChannels), file.HDR, file.Container,
		nilIfZero(file.Duration), nilIfZero(file.Bitrate), videoTracksJSON, audioTracksJSON, subtitleTracksJSON, externalSubtitlesJSON,
		file.IntroStart, file.IntroEnd, file.CreditsStart, file.CreditsEnd,
		probeSource, file.ProbeUpdatedAt, file.MissingSince, file.CreatedAt, file.UpdatedAt,
	).Scan(&created)
	if err != nil {
		return false, false, false, fmt.Errorf("upserting file %s: %w", file.FilePath, err)
	}
	return created, !created, false, nil
}

func importLibraryLink(ctx context.Context, tx pgx.Tx, link LibraryLinkRecord) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (content_id, media_folder_id) DO NOTHING`,
		link.ContentID, link.MediaFolderID, link.FirstSeenAt,
	)
	if err != nil {
		return false, fmt.Errorf("importing library link %s:%d: %w", link.ContentID, link.MediaFolderID, err)
	}
	return tag.RowsAffected() == 1, nil
}

func normalizeRewrites(rewrites []PathRewrite) []PathRewrite {
	out := make([]PathRewrite, 0, len(rewrites))
	for _, rewrite := range rewrites {
		from := normalizePath(rewrite.From)
		to := normalizePath(rewrite.To)
		if from == "" || to == "" {
			continue
		}
		out = append(out, PathRewrite{From: from, To: to})
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].From) > len(out[j].From) })
	return out
}

func rewritePath(path string, rewrites []PathRewrite) (string, bool) {
	normalized := normalizePath(path)
	for _, rewrite := range rewrites {
		if !hasPathPrefix(normalized, rewrite.From) {
			continue
		}
		if normalized == rewrite.From {
			return rewrite.To, true
		}
		suffix := strings.TrimPrefix(normalized, rewrite.From)
		return normalizePath(rewrite.To + suffix), true
	}
	return normalized, false
}

func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func hasPathPrefix(path, prefix string) bool {
	path = normalizePath(path)
	prefix = normalizePath(prefix)
	if path == prefix {
		return true
	}
	if prefix == string(os.PathSeparator) {
		return strings.HasPrefix(path, prefix)
	}
	return strings.HasPrefix(path, prefix+string(os.PathSeparator))
}

func sortLibraries(records []LibraryRecord) {
	sort.Slice(records, func(i, j int) bool { return records[i].ExportedID < records[j].ExportedID })
}

func cloneStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string(nil), values...)
}

func itemRecordStringArrays(item ItemRecord) ([]string, []string, []string, []string) {
	return nonNilStrings(item.Studios),
		nonNilStrings(item.Networks),
		nonNilStrings(item.Countries),
		nonNilStrings(item.Keywords)
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func cloneInts(values []int) []int {
	if values == nil {
		return []int{}
	}
	return append([]int(nil), values...)
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func mapKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func nilIfZero(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func toVideoTrackRecords(tracks []models.VideoTrack) []VideoTrackRecord {
	out := make([]VideoTrackRecord, 0, len(tracks))
	for _, track := range tracks {
		out = append(out, VideoTrackRecord{
			Title:           track.Title,
			Codec:           track.Codec,
			DolbyVision:     track.DolbyVision,
			Profile:         track.Profile,
			Level:           track.Level,
			Width:           track.Width,
			Height:          track.Height,
			AspectRatio:     track.AspectRatio,
			Interlaced:      track.Interlaced,
			FrameRate:       track.FrameRate,
			Bitrate:         track.Bitrate,
			VideoRange:      track.VideoRange,
			ColorPrimaries:  track.ColorPrimaries,
			ColorSpace:      track.ColorSpace,
			ColorTransfer:   track.ColorTransfer,
			BitDepth:        track.BitDepth,
			PixelFormat:     track.PixelFormat,
			ReferenceFrames: track.ReferenceFrames,
		})
	}
	return out
}

func toAudioTrackRecords(tracks []models.AudioTrack) []AudioTrackRecord {
	out := make([]AudioTrackRecord, 0, len(tracks))
	for _, track := range tracks {
		out = append(out, AudioTrackRecord{
			Title:         track.Title,
			EmbeddedTitle: track.EmbeddedTitle,
			Language:      track.Language,
			Codec:         track.Codec,
			Layout:        track.Layout,
			Channels:      track.Channels,
			Bitrate:       track.Bitrate,
			SampleRate:    track.SampleRate,
			BitDepth:      track.BitDepth,
			Default:       track.Default,
		})
	}
	return out
}

func toSubtitleTrackRecords(tracks []models.SubtitleTrack) []SubtitleTrackRecord {
	out := make([]SubtitleTrackRecord, 0, len(tracks))
	for _, track := range tracks {
		out = append(out, SubtitleTrackRecord{
			Index:           track.Index,
			Language:        track.Language,
			Codec:           track.Codec,
			Title:           track.Title,
			EmbeddedTitle:   track.EmbeddedTitle,
			Resolution:      track.Resolution,
			Forced:          track.Forced,
			Default:         track.Default,
			HearingImpaired: track.HearingImpaired,
			External:        track.External,
			FileName:        track.FileName,
		})
	}
	return out
}

func toExternalSubtitleRecords(tracks []models.ExternalSubtitle) []ExternalSubtitleRecord {
	out := make([]ExternalSubtitleRecord, 0, len(tracks))
	for _, track := range tracks {
		out = append(out, ExternalSubtitleRecord{
			Path:            track.Path,
			Language:        track.Language,
			Format:          track.Format,
			Title:           track.Title,
			EmbeddedTitle:   track.EmbeddedTitle,
			Resolution:      track.Resolution,
			Forced:          track.Forced,
			Default:         track.Default,
			HearingImpaired: track.HearingImpaired,
		})
	}
	return out
}
