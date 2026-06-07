package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pathscope"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors for file repository operations.
var (
	ErrFileNotFound = errors.New("media file not found")
)

// FileRepository provides CRUD operations for the media_files table.
type FileRepository struct {
	pool *pgxpool.Pool
}

type RawMatchBacklogMode string

const (
	RawMatchBacklogGeneric   RawMatchBacklogMode = "generic"
	RawMatchBacklogNonSeries RawMatchBacklogMode = "non_series"
	RawMatchBacklogMixed     RawMatchBacklogMode = "mixed"
)

// Pool returns the underlying connection pool (used by tests).
func (r *FileRepository) Pool() *pgxpool.Pool { return r.pool }

// NewFileRepository creates a new FileRepository backed by the given pool.
func NewFileRepository(pool *pgxpool.Pool) *FileRepository {
	return &FileRepository{pool: pool}
}

// fileColumns is the list of columns returned by all SELECT queries.
const fileColumns = `id, content_id, episode_id, season_number, episode_number,
	media_folder_id, canonical_root_path, observed_root_path, content_group_key, group_key_version,
	base_title, base_year, base_type, identity_confidence, identity_json,
	file_path, file_size, file_modified_at, file_hash,
	codec_video, codec_audio, resolution, audio_channels, hdr, container,
	duration, bitrate, video_tracks, audio_tracks, subtitle_tracks, external_subtitles, chapters,
	chapter_thumbnail_retry_after, chapter_thumbnail_failure_count, chapter_thumbnail_last_error,
	intro_start, intro_end, credits_start, credits_end, recap_start, recap_end, preview_start, preview_end, markers_source, markers_confidence,
	intro_markers_source, intro_markers_provider, intro_markers_confidence, intro_markers_algorithm, intro_markers_detected_at,
	credits_markers_source, credits_markers_provider, credits_markers_confidence, credits_markers_algorithm, credits_markers_detected_at,
	recap_markers_source, recap_markers_provider, recap_markers_confidence, recap_markers_algorithm, recap_markers_detected_at,
	preview_markers_source, preview_markers_provider, preview_markers_confidence, preview_markers_algorithm, preview_markers_detected_at,
	edition_raw, edition_key, edition_confidence, edition_source,
	presentation_kind, presentation_group_key, presentation_part_index, presentation_part_total,
	multi_episode_start, multi_episode_end,
	probe_source, probe_updated_at, match_attempted_at, missing_since, created_at, updated_at`

// mfFileColumns qualifies every column with the "mf" alias for use in JOIN queries
// where unqualified "id" would be ambiguous.
const mfFileColumns = `mf.id, mf.content_id, mf.episode_id, mf.season_number, mf.episode_number,
	mf.media_folder_id, mf.canonical_root_path, mf.observed_root_path, mf.content_group_key, mf.group_key_version,
	mf.base_title, mf.base_year, mf.base_type, mf.identity_confidence, mf.identity_json,
	mf.file_path, mf.file_size, mf.file_modified_at, mf.file_hash,
	mf.codec_video, mf.codec_audio, mf.resolution, mf.audio_channels, mf.hdr, mf.container,
	mf.duration, mf.bitrate, mf.video_tracks, mf.audio_tracks, mf.subtitle_tracks, mf.external_subtitles, mf.chapters,
	mf.chapter_thumbnail_retry_after, mf.chapter_thumbnail_failure_count, mf.chapter_thumbnail_last_error,
	mf.intro_start, mf.intro_end, mf.credits_start, mf.credits_end, mf.recap_start, mf.recap_end, mf.preview_start, mf.preview_end, mf.markers_source, mf.markers_confidence,
	mf.intro_markers_source, mf.intro_markers_provider, mf.intro_markers_confidence, mf.intro_markers_algorithm, mf.intro_markers_detected_at,
	mf.credits_markers_source, mf.credits_markers_provider, mf.credits_markers_confidence, mf.credits_markers_algorithm, mf.credits_markers_detected_at,
	mf.recap_markers_source, mf.recap_markers_provider, mf.recap_markers_confidence, mf.recap_markers_algorithm, mf.recap_markers_detected_at,
	mf.preview_markers_source, mf.preview_markers_provider, mf.preview_markers_confidence, mf.preview_markers_algorithm, mf.preview_markers_detected_at,
	mf.edition_raw, mf.edition_key, mf.edition_confidence, mf.edition_source,
	mf.presentation_kind, mf.presentation_group_key, mf.presentation_part_index, mf.presentation_part_total,
	mf.multi_episode_start, mf.multi_episode_end,
	mf.probe_source, mf.probe_updated_at, mf.match_attempted_at, mf.missing_since, mf.created_at, mf.updated_at`

// scanMediaFile scans a single row into a *models.MediaFile.
func scanMediaFile(row pgx.Row) (*models.MediaFile, error) {
	var f models.MediaFile
	var contentID *string
	var episodeID *string
	var seasonNumber, episodeNumber *int
	var canonicalRootPath *string
	var observedRootPath, contentGroupKey, baseTitle, baseType, identityConfidence *string
	var groupKeyVersion, baseYear *int
	var identityJSON []byte
	var fileModifiedAt *time.Time
	var fileHash *string
	var codecVideo, codecAudio, resolution, container, probeSource *string
	var markersSource, introMarkersSource, introMarkersProvider, introMarkersAlgorithm *string
	var creditsMarkersSource, creditsMarkersProvider, creditsMarkersAlgorithm *string
	var recapMarkersSource, recapMarkersProvider, recapMarkersAlgorithm *string
	var previewMarkersSource, previewMarkersProvider, previewMarkersAlgorithm *string
	var chapterThumbnailLastError *string
	var editionRaw, editionKey, editionSource *string
	var audioChannels *int
	var hdr *bool
	var duration, bitrate *int
	var chapterThumbnailFailureCount *int
	var markersConfidence, introMarkersConfidence, creditsMarkersConfidence *float64
	var recapMarkersConfidence, previewMarkersConfidence *float64
	var introMarkersDetectedAt, creditsMarkersDetectedAt *time.Time
	var recapMarkersDetectedAt, previewMarkersDetectedAt *time.Time
	var editionConfidence *float64
	var presentationPartIndex, presentationPartTotal *int
	var multiEpisodeStart, multiEpisodeEnd *int
	var presentationKind, presentationGroupKey *string
	var chapterThumbnailRetryAfter *time.Time
	var videoTracksJSON, audioTracksJSON, subtitleTracksJSON, externalSubtitlesJSON, chaptersJSON []byte

	err := row.Scan(
		&f.ID,
		&contentID,
		&episodeID,
		&seasonNumber,
		&episodeNumber,
		&f.MediaFolderID,
		&canonicalRootPath,
		&observedRootPath,
		&contentGroupKey,
		&groupKeyVersion,
		&baseTitle,
		&baseYear,
		&baseType,
		&identityConfidence,
		&identityJSON,
		&f.FilePath,
		&f.FileSize,
		&fileModifiedAt,
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
		&chaptersJSON,
		&chapterThumbnailRetryAfter,
		&chapterThumbnailFailureCount,
		&chapterThumbnailLastError,
		&f.IntroStart,
		&f.IntroEnd,
		&f.CreditsStart,
		&f.CreditsEnd,
		&f.RecapStart,
		&f.RecapEnd,
		&f.PreviewStart,
		&f.PreviewEnd,
		&markersSource,
		&markersConfidence,
		&introMarkersSource,
		&introMarkersProvider,
		&introMarkersConfidence,
		&introMarkersAlgorithm,
		&introMarkersDetectedAt,
		&creditsMarkersSource,
		&creditsMarkersProvider,
		&creditsMarkersConfidence,
		&creditsMarkersAlgorithm,
		&creditsMarkersDetectedAt,
		&recapMarkersSource,
		&recapMarkersProvider,
		&recapMarkersConfidence,
		&recapMarkersAlgorithm,
		&recapMarkersDetectedAt,
		&previewMarkersSource,
		&previewMarkersProvider,
		&previewMarkersConfidence,
		&previewMarkersAlgorithm,
		&previewMarkersDetectedAt,
		&editionRaw,
		&editionKey,
		&editionConfidence,
		&editionSource,
		&presentationKind,
		&presentationGroupKey,
		&presentationPartIndex,
		&presentationPartTotal,
		&multiEpisodeStart,
		&multiEpisodeEnd,
		&probeSource,
		&f.ProbeUpdatedAt,
		&f.MatchAttemptedAt,
		&f.MissingSince,
		&f.CreatedAt,
		&f.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrFileNotFound
		}
		return nil, fmt.Errorf("scanning media file: %w", err)
	}

	// Assign nullable fields.
	if contentID != nil {
		f.ContentID = *contentID
	}
	if episodeID != nil {
		f.EpisodeID = *episodeID
	}
	if seasonNumber != nil {
		f.SeasonNumber = *seasonNumber
	}
	if episodeNumber != nil {
		f.EpisodeNumber = *episodeNumber
	}
	if canonicalRootPath != nil {
		f.CanonicalRootPath = *canonicalRootPath
	}
	if observedRootPath != nil {
		f.ObservedRootPath = *observedRootPath
	}
	if contentGroupKey != nil {
		f.ContentGroupKey = *contentGroupKey
	}
	if groupKeyVersion != nil {
		f.GroupKeyVersion = *groupKeyVersion
	}
	if baseTitle != nil {
		f.BaseTitle = *baseTitle
	}
	if baseYear != nil {
		f.BaseYear = *baseYear
	}
	if baseType != nil {
		f.BaseType = *baseType
	}
	if identityConfidence != nil {
		f.IdentityConfidence = *identityConfidence
	}
	if len(identityJSON) > 0 {
		f.IdentityJSON = append([]byte(nil), identityJSON...)
	}
	if fileHash != nil {
		f.FileHash = *fileHash
	}
	if fileModifiedAt != nil {
		f.FileModifiedAt = fileModifiedAt
	}
	if codecVideo != nil {
		f.CodecVideo = *codecVideo
	}
	if codecAudio != nil {
		f.CodecAudio = *codecAudio
	}
	if resolution != nil {
		f.Resolution = *resolution
	}
	if audioChannels != nil {
		f.AudioChannels = *audioChannels
	}
	if hdr != nil {
		f.HDR = *hdr
	}
	if container != nil {
		f.Container = *container
	}
	if duration != nil {
		f.Duration = *duration
	}
	if bitrate != nil {
		f.Bitrate = *bitrate
	}
	if chapterThumbnailRetryAfter != nil {
		f.ChapterThumbnailRetryAfter = chapterThumbnailRetryAfter
	}
	if chapterThumbnailFailureCount != nil {
		f.ChapterThumbnailFailureCount = *chapterThumbnailFailureCount
	}
	if chapterThumbnailLastError != nil {
		f.ChapterThumbnailLastError = *chapterThumbnailLastError
	}
	if probeSource != nil {
		f.ProbeSource = *probeSource
	}
	if editionRaw != nil {
		f.EditionRaw = *editionRaw
	}
	if editionKey != nil {
		f.EditionKey = *editionKey
	}
	f.EditionConfidence = editionConfidence
	if editionSource != nil {
		f.EditionSource = *editionSource
	}
	if presentationKind != nil {
		f.PresentationKind = *presentationKind
	}
	if presentationGroupKey != nil {
		f.PresentationGroupKey = *presentationGroupKey
	}
	if presentationPartIndex != nil {
		f.PresentationPartIndex = *presentationPartIndex
	}
	if presentationPartTotal != nil {
		f.PresentationPartTotal = *presentationPartTotal
	}
	if multiEpisodeStart != nil {
		f.MultiEpisodeStart = *multiEpisodeStart
	}
	if multiEpisodeEnd != nil {
		f.MultiEpisodeEnd = *multiEpisodeEnd
	}
	f.MarkersSource = markersSource
	f.MarkersConfidence = markersConfidence
	f.IntroMarkersSource = introMarkersSource
	f.IntroMarkersProvider = introMarkersProvider
	f.IntroMarkersConfidence = introMarkersConfidence
	f.IntroMarkersAlgorithm = introMarkersAlgorithm
	f.IntroMarkersDetectedAt = introMarkersDetectedAt
	f.CreditsMarkersSource = creditsMarkersSource
	f.CreditsMarkersProvider = creditsMarkersProvider
	f.CreditsMarkersConfidence = creditsMarkersConfidence
	f.CreditsMarkersAlgorithm = creditsMarkersAlgorithm
	f.CreditsMarkersDetectedAt = creditsMarkersDetectedAt
	f.RecapMarkersSource = recapMarkersSource
	f.RecapMarkersProvider = recapMarkersProvider
	f.RecapMarkersConfidence = recapMarkersConfidence
	f.RecapMarkersAlgorithm = recapMarkersAlgorithm
	f.RecapMarkersDetectedAt = recapMarkersDetectedAt
	f.PreviewMarkersSource = previewMarkersSource
	f.PreviewMarkersProvider = previewMarkersProvider
	f.PreviewMarkersConfidence = previewMarkersConfidence
	f.PreviewMarkersAlgorithm = previewMarkersAlgorithm
	f.PreviewMarkersDetectedAt = previewMarkersDetectedAt

	if len(videoTracksJSON) > 0 {
		if err := json.Unmarshal(videoTracksJSON, &f.VideoTracks); err != nil {
			return nil, fmt.Errorf("unmarshaling video_tracks: %w", err)
		}
	}
	if f.VideoTracks == nil {
		f.VideoTracks = []models.VideoTrack{}
	}

	if len(audioTracksJSON) > 0 {
		if err := json.Unmarshal(audioTracksJSON, &f.AudioTracks); err != nil {
			return nil, fmt.Errorf("unmarshaling audio_tracks: %w", err)
		}
	}
	if f.AudioTracks == nil {
		f.AudioTracks = []models.AudioTrack{}
	}

	// Deserialize JSONB fields.
	if len(subtitleTracksJSON) > 0 {
		if err := json.Unmarshal(subtitleTracksJSON, &f.SubtitleTracks); err != nil {
			return nil, fmt.Errorf("unmarshaling subtitle_tracks: %w", err)
		}
	}
	if f.SubtitleTracks == nil {
		f.SubtitleTracks = []models.SubtitleTrack{}
	}

	if len(externalSubtitlesJSON) > 0 {
		if err := json.Unmarshal(externalSubtitlesJSON, &f.ExternalSubtitles); err != nil {
			return nil, fmt.Errorf("unmarshaling external_subtitles: %w", err)
		}
	}
	if f.ExternalSubtitles == nil {
		f.ExternalSubtitles = []models.ExternalSubtitle{}
	}

	if len(chaptersJSON) > 0 {
		if err := json.Unmarshal(chaptersJSON, &f.Chapters); err != nil {
			return nil, fmt.Errorf("unmarshaling chapters: %w", err)
		}
	}

	return &f, nil
}

// scanMediaFiles scans multiple rows into a []*models.MediaFile slice.
func scanMediaFiles(rows pgx.Rows) ([]*models.MediaFile, error) {
	var files []*models.MediaFile
	for rows.Next() {
		var f models.MediaFile
		var contentID *string
		var episodeID *string
		var seasonNumber, episodeNumber *int
		var canonicalRootPath *string
		var observedRootPath, contentGroupKey, baseTitle, baseType, identityConfidence *string
		var groupKeyVersion, baseYear *int
		var identityJSON []byte
		var fileModifiedAt *time.Time
		var fileHash *string
		var codecVideo, codecAudio, resolution, container, probeSource *string
		var markersSource, introMarkersSource, introMarkersProvider, introMarkersAlgorithm *string
		var creditsMarkersSource, creditsMarkersProvider, creditsMarkersAlgorithm *string
		var recapMarkersSource, recapMarkersProvider, recapMarkersAlgorithm *string
		var previewMarkersSource, previewMarkersProvider, previewMarkersAlgorithm *string
		var chapterThumbnailLastError *string
		var editionRaw, editionKey, editionSource *string
		var audioChannels *int
		var hdr *bool
		var duration, bitrate *int
		var chapterThumbnailFailureCount *int
		var markersConfidence, introMarkersConfidence, creditsMarkersConfidence *float64
		var recapMarkersConfidence, previewMarkersConfidence *float64
		var introMarkersDetectedAt, creditsMarkersDetectedAt *time.Time
		var recapMarkersDetectedAt, previewMarkersDetectedAt *time.Time
		var editionConfidence *float64
		var presentationPartIndex, presentationPartTotal *int
		var multiEpisodeStart, multiEpisodeEnd *int
		var presentationKind, presentationGroupKey *string
		var chapterThumbnailRetryAfter *time.Time
		var videoTracksJSON, audioTracksJSON, subtitleTracksJSON, externalSubtitlesJSON, chaptersJSON []byte

		err := rows.Scan(
			&f.ID,
			&contentID,
			&episodeID,
			&seasonNumber,
			&episodeNumber,
			&f.MediaFolderID,
			&canonicalRootPath,
			&observedRootPath,
			&contentGroupKey,
			&groupKeyVersion,
			&baseTitle,
			&baseYear,
			&baseType,
			&identityConfidence,
			&identityJSON,
			&f.FilePath,
			&f.FileSize,
			&fileModifiedAt,
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
			&chaptersJSON,
			&chapterThumbnailRetryAfter,
			&chapterThumbnailFailureCount,
			&chapterThumbnailLastError,
			&f.IntroStart,
			&f.IntroEnd,
			&f.CreditsStart,
			&f.CreditsEnd,
			&f.RecapStart,
			&f.RecapEnd,
			&f.PreviewStart,
			&f.PreviewEnd,
			&markersSource,
			&markersConfidence,
			&introMarkersSource,
			&introMarkersProvider,
			&introMarkersConfidence,
			&introMarkersAlgorithm,
			&introMarkersDetectedAt,
			&creditsMarkersSource,
			&creditsMarkersProvider,
			&creditsMarkersConfidence,
			&creditsMarkersAlgorithm,
			&creditsMarkersDetectedAt,
			&recapMarkersSource,
			&recapMarkersProvider,
			&recapMarkersConfidence,
			&recapMarkersAlgorithm,
			&recapMarkersDetectedAt,
			&previewMarkersSource,
			&previewMarkersProvider,
			&previewMarkersConfidence,
			&previewMarkersAlgorithm,
			&previewMarkersDetectedAt,
			&editionRaw,
			&editionKey,
			&editionConfidence,
			&editionSource,
			&presentationKind,
			&presentationGroupKey,
			&presentationPartIndex,
			&presentationPartTotal,
			&multiEpisodeStart,
			&multiEpisodeEnd,
			&probeSource,
			&f.ProbeUpdatedAt,
			&f.MatchAttemptedAt,
			&f.MissingSince,
			&f.CreatedAt,
			&f.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning media file row: %w", err)
		}

		if contentID != nil {
			f.ContentID = *contentID
		}
		if episodeID != nil {
			f.EpisodeID = *episodeID
		}
		if seasonNumber != nil {
			f.SeasonNumber = *seasonNumber
		}
		if episodeNumber != nil {
			f.EpisodeNumber = *episodeNumber
		}
		if canonicalRootPath != nil {
			f.CanonicalRootPath = *canonicalRootPath
		}
		if observedRootPath != nil {
			f.ObservedRootPath = *observedRootPath
		}
		if contentGroupKey != nil {
			f.ContentGroupKey = *contentGroupKey
		}
		if groupKeyVersion != nil {
			f.GroupKeyVersion = *groupKeyVersion
		}
		if baseTitle != nil {
			f.BaseTitle = *baseTitle
		}
		if baseYear != nil {
			f.BaseYear = *baseYear
		}
		if baseType != nil {
			f.BaseType = *baseType
		}
		if identityConfidence != nil {
			f.IdentityConfidence = *identityConfidence
		}
		if len(identityJSON) > 0 {
			f.IdentityJSON = append([]byte(nil), identityJSON...)
		}
		if fileHash != nil {
			f.FileHash = *fileHash
		}
		if fileModifiedAt != nil {
			f.FileModifiedAt = fileModifiedAt
		}
		if codecVideo != nil {
			f.CodecVideo = *codecVideo
		}
		if codecAudio != nil {
			f.CodecAudio = *codecAudio
		}
		if resolution != nil {
			f.Resolution = *resolution
		}
		if audioChannels != nil {
			f.AudioChannels = *audioChannels
		}
		if hdr != nil {
			f.HDR = *hdr
		}
		if container != nil {
			f.Container = *container
		}
		if duration != nil {
			f.Duration = *duration
		}
		if bitrate != nil {
			f.Bitrate = *bitrate
		}
		if chapterThumbnailRetryAfter != nil {
			f.ChapterThumbnailRetryAfter = chapterThumbnailRetryAfter
		}
		if chapterThumbnailFailureCount != nil {
			f.ChapterThumbnailFailureCount = *chapterThumbnailFailureCount
		}
		if chapterThumbnailLastError != nil {
			f.ChapterThumbnailLastError = *chapterThumbnailLastError
		}
		if probeSource != nil {
			f.ProbeSource = *probeSource
		}
		if editionRaw != nil {
			f.EditionRaw = *editionRaw
		}
		if editionKey != nil {
			f.EditionKey = *editionKey
		}
		f.EditionConfidence = editionConfidence
		if editionSource != nil {
			f.EditionSource = *editionSource
		}
		if presentationKind != nil {
			f.PresentationKind = *presentationKind
		}
		if presentationGroupKey != nil {
			f.PresentationGroupKey = *presentationGroupKey
		}
		if presentationPartIndex != nil {
			f.PresentationPartIndex = *presentationPartIndex
		}
		if presentationPartTotal != nil {
			f.PresentationPartTotal = *presentationPartTotal
		}
		if multiEpisodeStart != nil {
			f.MultiEpisodeStart = *multiEpisodeStart
		}
		if multiEpisodeEnd != nil {
			f.MultiEpisodeEnd = *multiEpisodeEnd
		}
		f.MarkersSource = markersSource
		f.MarkersConfidence = markersConfidence
		f.IntroMarkersSource = introMarkersSource
		f.IntroMarkersProvider = introMarkersProvider
		f.IntroMarkersConfidence = introMarkersConfidence
		f.IntroMarkersAlgorithm = introMarkersAlgorithm
		f.IntroMarkersDetectedAt = introMarkersDetectedAt
		f.CreditsMarkersSource = creditsMarkersSource
		f.CreditsMarkersProvider = creditsMarkersProvider
		f.CreditsMarkersConfidence = creditsMarkersConfidence
		f.CreditsMarkersAlgorithm = creditsMarkersAlgorithm
		f.CreditsMarkersDetectedAt = creditsMarkersDetectedAt
		f.RecapMarkersSource = recapMarkersSource
		f.RecapMarkersProvider = recapMarkersProvider
		f.RecapMarkersConfidence = recapMarkersConfidence
		f.RecapMarkersAlgorithm = recapMarkersAlgorithm
		f.RecapMarkersDetectedAt = recapMarkersDetectedAt
		f.PreviewMarkersSource = previewMarkersSource
		f.PreviewMarkersProvider = previewMarkersProvider
		f.PreviewMarkersConfidence = previewMarkersConfidence
		f.PreviewMarkersAlgorithm = previewMarkersAlgorithm
		f.PreviewMarkersDetectedAt = previewMarkersDetectedAt

		if len(videoTracksJSON) > 0 {
			if err := json.Unmarshal(videoTracksJSON, &f.VideoTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling video_tracks: %w", err)
			}
		}
		if f.VideoTracks == nil {
			f.VideoTracks = []models.VideoTrack{}
		}

		if len(audioTracksJSON) > 0 {
			if err := json.Unmarshal(audioTracksJSON, &f.AudioTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling audio_tracks: %w", err)
			}
		}
		if f.AudioTracks == nil {
			f.AudioTracks = []models.AudioTrack{}
		}

		// Deserialize JSONB fields.
		if len(subtitleTracksJSON) > 0 {
			if err := json.Unmarshal(subtitleTracksJSON, &f.SubtitleTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling subtitle_tracks: %w", err)
			}
		}
		if f.SubtitleTracks == nil {
			f.SubtitleTracks = []models.SubtitleTrack{}
		}

		if len(externalSubtitlesJSON) > 0 {
			if err := json.Unmarshal(externalSubtitlesJSON, &f.ExternalSubtitles); err != nil {
				return nil, fmt.Errorf("unmarshaling external_subtitles: %w", err)
			}
		}
		if f.ExternalSubtitles == nil {
			f.ExternalSubtitles = []models.ExternalSubtitle{}
		}

		if len(chaptersJSON) > 0 {
			if err := json.Unmarshal(chaptersJSON, &f.Chapters); err != nil {
				return nil, fmt.Errorf("unmarshaling chapters: %w", err)
			}
		}

		files = append(files, &f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating media file rows: %w", err)
	}
	return files, nil
}

// serializeJSONB marshals a value to JSON bytes, returning nil for empty slices.
func serializeJSONB(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	// Treat "null" as nil to store NULL in the JSONB column.
	if string(data) == "null" {
		return nil, nil
	}
	return data, nil
}

// Upsert inserts or updates a media file by file_path (ON CONFLICT DO UPDATE).
// Returns the resulting row.
func (r *FileRepository) Upsert(ctx context.Context, mf models.MediaFile) (*models.MediaFile, error) {
	subtitleTracksJSON, err := serializeJSONB(mf.SubtitleTracks)
	if err != nil {
		return nil, fmt.Errorf("marshaling subtitle_tracks: %w", err)
	}

	externalSubtitlesJSON, err := serializeJSONB(mf.ExternalSubtitles)
	if err != nil {
		return nil, fmt.Errorf("marshaling external_subtitles: %w", err)
	}
	videoTracksJSON, err := serializeJSONB(mf.VideoTracks)
	if err != nil {
		return nil, fmt.Errorf("marshaling video_tracks: %w", err)
	}
	audioTracksJSON, err := serializeJSONB(mf.AudioTracks)
	if err != nil {
		return nil, fmt.Errorf("marshaling audio_tracks: %w", err)
	}
	chaptersJSON, err := serializeJSONB(mf.Chapters)
	if err != nil {
		return nil, fmt.Errorf("marshaling chapters: %w", err)
	}

	// Convert empty strings to nil for nullable text columns.
	var contentID *string
	if mf.ContentID != "" {
		contentID = &mf.ContentID
	}
	var episodeID *string
	if mf.EpisodeID != "" {
		episodeID = &mf.EpisodeID
	}
	var fileHash *string
	if mf.FileHash != "" {
		fileHash = &mf.FileHash
	}
	var probeSource *string
	if mf.ProbeSource != "" {
		probeSource = &mf.ProbeSource
	}
	var editionConfidence *float64
	if mf.EditionConfidence != nil {
		editionConfidence = mf.EditionConfidence
	}
	groupKeyVersion := mf.GroupKeyVersion
	if groupKeyVersion == 0 {
		groupKeyVersion = 1
	}
	identityConfidence := mf.IdentityConfidence
	if identityConfidence == "" {
		identityConfidence = "low"
	}
	identityJSON := mf.IdentityJSON
	if len(identityJSON) == 0 {
		identityJSON = []byte("{}")
	}

	query := `INSERT INTO media_files (
		content_id, episode_id, season_number, episode_number,
		media_folder_id, canonical_root_path, observed_root_path, content_group_key, group_key_version,
		base_title, base_year, base_type, identity_confidence, identity_json,
		file_path, file_size, file_modified_at, file_hash,
		codec_video, codec_audio, resolution, audio_channels, hdr, container,
		duration, bitrate, video_tracks, audio_tracks, subtitle_tracks, external_subtitles, chapters,
		intro_start, intro_end, credits_start, credits_end, markers_source, markers_confidence,
		edition_raw, edition_key, edition_confidence, edition_source,
		presentation_kind, presentation_group_key, presentation_part_index, presentation_part_total,
		multi_episode_start, multi_episode_end,
		probe_source, probe_updated_at, missing_since
	) VALUES (
		$1, $2, $3, $4,
		$5, $6, $7, $8, $9,
		$10, $11, $12, $13, $14,
		$15, $16, $17, $18,
		$19, $20, $21, $22, $23, $24,
		$25, $26, $27, $28, $29, $30, $31,
		$32, $33, $34, $35, $36, $37,
		$38, $39, $40, $41,
		$42, $43, $44, $45,
		$46, $47,
		$48, $49, $50
	)
	ON CONFLICT (file_path) DO UPDATE SET
		content_id = COALESCE(EXCLUDED.content_id, media_files.content_id),
		episode_id = COALESCE(EXCLUDED.episode_id, media_files.episode_id),
		season_number = COALESCE(EXCLUDED.season_number, media_files.season_number),
		episode_number = COALESCE(EXCLUDED.episode_number, media_files.episode_number),
		media_folder_id = EXCLUDED.media_folder_id,
		canonical_root_path = EXCLUDED.canonical_root_path,
		observed_root_path = EXCLUDED.observed_root_path,
		content_group_key = EXCLUDED.content_group_key,
		group_key_version = EXCLUDED.group_key_version,
		base_title = EXCLUDED.base_title,
		base_year = EXCLUDED.base_year,
		base_type = EXCLUDED.base_type,
		identity_confidence = EXCLUDED.identity_confidence,
		identity_json = EXCLUDED.identity_json,
		file_size = EXCLUDED.file_size,
		file_modified_at = EXCLUDED.file_modified_at,
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
		chapters = EXCLUDED.chapters,
		edition_raw = EXCLUDED.edition_raw,
		edition_key = EXCLUDED.edition_key,
		edition_confidence = EXCLUDED.edition_confidence,
		edition_source = EXCLUDED.edition_source,
		presentation_kind = EXCLUDED.presentation_kind,
		presentation_group_key = EXCLUDED.presentation_group_key,
		presentation_part_index = EXCLUDED.presentation_part_index,
		presentation_part_total = EXCLUDED.presentation_part_total,
		multi_episode_start = EXCLUDED.multi_episode_start,
		multi_episode_end = EXCLUDED.multi_episode_end,
		probe_source = EXCLUDED.probe_source,
		probe_updated_at = EXCLUDED.probe_updated_at,
		match_suppressed_at = NULL,
		missing_since = NULL,
		updated_at = NOW()
	RETURNING ` + fileColumns

	row := r.pool.QueryRow(ctx, query,
		contentID,
		episodeID,
		nilIfZero(mf.SeasonNumber),
		nilIfZero(mf.EpisodeNumber),
		mf.MediaFolderID,
		mf.CanonicalRootPath,
		mf.ObservedRootPath,
		mf.ContentGroupKey,
		groupKeyVersion,
		mf.BaseTitle,
		mf.BaseYear,
		mf.BaseType,
		identityConfidence,
		identityJSON,
		mf.FilePath,
		mf.FileSize,
		mf.FileModifiedAt,
		fileHash,
		nilIfEmpty(mf.CodecVideo),
		nilIfEmpty(mf.CodecAudio),
		nilIfEmpty(mf.Resolution),
		nilIfZero(mf.AudioChannels),
		mf.HDR,
		nilIfEmpty(mf.Container),
		nilIfZero(mf.Duration),
		nilIfZero(mf.Bitrate),
		videoTracksJSON,
		audioTracksJSON,
		subtitleTracksJSON,
		externalSubtitlesJSON,
		chaptersJSON,
		mf.IntroStart,
		mf.IntroEnd,
		mf.CreditsStart,
		mf.CreditsEnd,
		mf.MarkersSource,
		mf.MarkersConfidence,
		mf.EditionRaw,
		mf.EditionKey,
		editionConfidence,
		mf.EditionSource,
		mf.PresentationKind,
		mf.PresentationGroupKey,
		nilIfZero(mf.PresentationPartIndex),
		nilIfZero(mf.PresentationPartTotal),
		nilIfZero(mf.MultiEpisodeStart),
		nilIfZero(mf.MultiEpisodeEnd),
		probeSource,
		mf.ProbeUpdatedAt,
		mf.MissingSince,
	)

	return scanMediaFile(row)
}

type ChapterThumbnailFailureState struct {
	Apply        bool
	RetryAfter   *time.Time
	FailureCount int
	LastError    string
}

func (r *FileRepository) UpdateChapterThumbnailState(
	ctx context.Context,
	fileID int,
	chapters []models.MediaChapter,
	fileFailure *ChapterThumbnailFailureState,
) (*models.MediaFile, error) {
	chaptersJSON, err := serializeJSONB(chapters)
	if err != nil {
		return nil, fmt.Errorf("marshaling chapters: %w", err)
	}

	var retryAfter *time.Time
	var failureCount *int
	var lastError *string
	applyFailure := false
	if fileFailure != nil {
		applyFailure = fileFailure.Apply
		retryAfter = fileFailure.RetryAfter
		failureCount = &fileFailure.FailureCount
		if fileFailure.LastError != "" {
			lastError = &fileFailure.LastError
		}
	}

	row := r.pool.QueryRow(ctx, `
		UPDATE media_files
		SET chapters = $2,
		    chapter_thumbnail_retry_after = CASE WHEN $3 THEN $4 ELSE chapter_thumbnail_retry_after END,
		    chapter_thumbnail_failure_count = CASE
		        WHEN $3 THEN COALESCE($5, chapter_thumbnail_failure_count)
		        ELSE chapter_thumbnail_failure_count
		    END,
		    chapter_thumbnail_last_error = CASE WHEN $3 THEN $6 ELSE chapter_thumbnail_last_error END,
		    updated_at = NOW()
		WHERE id = $1
		RETURNING `+fileColumns,
		fileID,
		chaptersJSON,
		applyFailure,
		retryAfter,
		failureCount,
		lastError,
	)

	return scanMediaFile(row)
}

func (r *FileRepository) SetChapterThumbnailFailure(
	ctx context.Context,
	fileID int,
	retryAfter time.Time,
	failureCount int,
	lastError string,
) error {
	var lastErrorPtr *string
	if lastError != "" {
		lastErrorPtr = &lastError
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_files
		SET chapter_thumbnail_retry_after = $2,
		    chapter_thumbnail_failure_count = $3,
		    chapter_thumbnail_last_error = $4,
		    updated_at = NOW()
		WHERE id = $1`,
		fileID,
		retryAfter,
		failureCount,
		lastErrorPtr,
	)
	if err != nil {
		return fmt.Errorf("updating chapter thumbnail failure state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFileNotFound
	}
	return nil
}

// segmentState tracks the mutable per-segment fields used by UpsertMarkers.
// Each segment kind (intro, credits, recap, preview) has an independent state
// that the apply step mutates if the priority check allows the write.
type segmentState struct {
	start      *float64
	end        *float64
	source     *string
	provider   *string
	confidence *float64
	algorithm  *string
	detectedAt *time.Time
}

// applySegmentPatch merges the patched start/end into the segment state, then
// gates the write on the shared priority check. Returns true if the state was
// mutated. The legacy `markers_source` field is consulted as a fallback when
// the segment-specific source is nil but the segment already has a range.
func applySegmentPatch(
	state *segmentState,
	legacySharedSource *string,
	source string,
	provider *string,
	confidence *float64,
	algorithm string,
	patchStart, patchEnd *float64,
	duration float64,
	segmentName string,
	mutationAt time.Time,
) (bool, error) {
	if patchStart == nil && patchEnd == nil {
		return false, nil
	}

	nextStart := state.start
	nextEnd := state.end
	if patchStart != nil {
		nextStart = patchStart
	}
	if patchEnd != nil {
		nextEnd = patchEnd
	}
	if nextStart == nil || nextEnd == nil {
		return false, nil
	}
	if *nextStart < 0 || *nextEnd <= *nextStart {
		return false, fmt.Errorf("invalid %s marker range %.3f-%.3f", segmentName, *nextStart, *nextEnd)
	}
	if duration > 0 && *nextEnd > duration+1 {
		return false, fmt.Errorf("%s marker end %.3f exceeds duration %.3f", segmentName, *nextEnd, duration)
	}

	effectiveSource := state.source
	if effectiveSource == nil && state.start != nil && state.end != nil {
		effectiveSource = legacySharedSource
	}
	if !markers.CanWriteMarker(effectiveSource, state.confidence, source, confidence) {
		return false, nil
	}

	src := source
	algo := algorithm
	nextState := segmentState{
		start:      nextStart,
		end:        nextEnd,
		source:     &src,
		provider:   provider,
		confidence: confidence,
		algorithm:  &algo,
		detectedAt: &mutationAt,
	}
	if segmentEqual(*state, nextState) {
		return false, nil
	}
	state.start = nextStart
	state.end = nextEnd
	state.source = &src
	state.provider = provider
	state.confidence = confidence
	state.algorithm = &algo
	state.detectedAt = &mutationAt
	return true, nil
}

// resolveSegmentProvenance returns the source/provider/confidence/algorithm to
// write for a segment: the per-segment override when present, otherwise the
// update's shared Markers* values. The algorithm always falls back to
// external:<source> so writes carry an algorithm tag.
func resolveSegmentProvenance(update MarkerUpdate, override *SegmentProvenance) (source string, provider *string, confidence *float64, algorithm string) {
	source = update.MarkersSource
	provider = update.MarkersProvider
	confidence = update.MarkersConfidence
	algorithm = update.MarkersAlgorithm
	if override != nil {
		if override.Source != "" {
			source = override.Source
		}
		provider = override.Provider
		confidence = override.Confidence
		if override.Algorithm != "" {
			algorithm = override.Algorithm
		}
	}
	if algorithm == "" {
		algorithm = "external:" + source
	}
	return source, provider, confidence, algorithm
}

// segmentEqual reports whether two segment states are semantically equivalent.
// detected_at is intentionally ignored so writing the same marker value does
// not refresh provenance timestamps or create audit noise.
func segmentEqual(a, b segmentState) bool {
	return ptrFloatEqual(a.start, b.start) &&
		ptrFloatEqual(a.end, b.end) &&
		ptrStringEqual(a.source, b.source) &&
		ptrStringEqual(a.provider, b.provider) &&
		ptrFloatEqual(a.confidence, b.confidence) &&
		ptrStringEqual(a.algorithm, b.algorithm)
}

// UpsertMarkers updates only marker fields while enforcing source priority.
func (r *FileRepository) UpsertMarkers(ctx context.Context, fileID int, update MarkerUpdate) (bool, error) {
	if update.MarkersSource == "" {
		return false, fmt.Errorf("marker source is required")
	}
	return r.UpsertAndClearMarkers(ctx, fileID, update, nil)
}

// ClearMarkers nulls the given segment kinds (intro|credits|recap|preview) for
// a file, including their provenance columns. Used by the admin manual-marker
// API to remove a marker so detection/online fetch can repopulate it. Returns
// whether a row was updated.
func (r *FileRepository) ClearMarkers(ctx context.Context, fileID int, segments []string) (bool, error) {
	return r.upsertAndClearMarkers(ctx, fileID, nil, segments)
}

// UpsertAndClearMarkers applies manual marker sets and clears in one row-locking
// transaction so mixed PUT bodies cannot partially persist.
func (r *FileRepository) UpsertAndClearMarkers(ctx context.Context, fileID int, update MarkerUpdate, clearSegments []string) (bool, error) {
	return r.upsertAndClearMarkers(ctx, fileID, &update, clearSegments)
}

func (r *FileRepository) ListMarkerEditAudit(ctx context.Context, fileIDs []int, limit int) ([]MarkerEditAuditRow, error) {
	if len(fileIDs) == 0 {
		return []MarkerEditAuditRow{}, nil
	}
	return r.listMarkerEditAudit(ctx, "WHERE a.media_file_id = ANY ($1)", []any{fileIDs}, limit)
}

func (r *FileRepository) ListAllMarkerEditAudit(ctx context.Context, limit int) ([]MarkerEditAuditRow, error) {
	return r.listMarkerEditAudit(ctx, "", nil, limit)
}

func (r *FileRepository) listMarkerEditAudit(ctx context.Context, whereClause string, args []any, limit int) ([]MarkerEditAuditRow, error) {
	limit = normalizeMarkerAuditLimit(limit)
	args = append(args, limit)
	limitPlaceholder := len(args)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT a.id,
		       a.media_file_id,
		       NULLIF(CASE WHEN mf.episode_id <> '' THEN mf.episode_id ELSE mf.content_id END, '') AS item_id,
		       NULLIF(COALESCE(CASE WHEN mf.episode_id <> '' THEN 'episode' ELSE mi.type END, mf.base_type), '') AS item_type,
		       NULLIF(COALESCE(e.title, mi.title, mf.base_title), '') AS media_title,
		       NULLIF(mf.file_path, '') AS file_path,
		       a.segment_kind,
		       a.action,
		       a.before_marker,
		       a.after_marker,
		       a.user_id,
		       u.username,
		       a.impersonator_user_id,
		       iu.username,
		       a.api_key_id,
		       a.request_id,
		       a.client_ip::text,
		       a.user_agent,
		       a.created_at
		FROM marker_edit_audit a
		LEFT JOIN media_files mf ON mf.id = a.media_file_id
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		LEFT JOIN episodes e ON e.content_id = mf.episode_id
		LEFT JOIN users u ON u.id = a.user_id
		LEFT JOIN users iu ON iu.id = a.impersonator_user_id
		%s
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $%d`, whereClause, limitPlaceholder), args...)
	if err != nil {
		return nil, fmt.Errorf("list marker edit audit: %w", err)
	}
	defer rows.Close()

	out := make([]MarkerEditAuditRow, 0, limit)
	for rows.Next() {
		var row MarkerEditAuditRow
		var beforeJSON []byte
		var afterJSON []byte
		if err := rows.Scan(
			&row.ID,
			&row.MediaFileID,
			&row.ItemID,
			&row.ItemType,
			&row.MediaTitle,
			&row.FilePath,
			&row.SegmentKind,
			&row.Action,
			&beforeJSON,
			&afterJSON,
			&row.UserID,
			&row.Username,
			&row.ImpersonatorUserID,
			&row.ImpersonatorUsername,
			&row.APIKeyID,
			&row.RequestID,
			&row.ClientIP,
			&row.UserAgent,
			&row.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan marker edit audit row: %w", err)
		}
		before, err := unmarshalMarkerAuditSegment(beforeJSON)
		if err != nil {
			return nil, err
		}
		after, err := unmarshalMarkerAuditSegment(afterJSON)
		if err != nil {
			return nil, err
		}
		row.Before = before
		row.After = after
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate marker edit audit rows: %w", err)
	}
	return out, nil
}

func normalizeMarkerAuditLimit(limit int) int {
	if limit <= 0 {
		return 25
	}
	if limit > 100 {
		return 100
	}
	return limit
}

type markerSegmentFlags struct {
	intro   bool
	credits bool
	recap   bool
	preview bool
}

func (f markerSegmentFlags) any() bool {
	return f.intro || f.credits || f.recap || f.preview
}

type markerMutationState struct {
	duration           float64
	existingSource     *string
	existingConfidence *float64
	intro              segmentState
	credits            segmentState
	recap              segmentState
	preview            segmentState
}

func (r *FileRepository) upsertAndClearMarkers(ctx context.Context, fileID int, update *MarkerUpdate, clearSegments []string) (bool, error) {
	hasUpdate := update != nil && update.HasAnySegment()
	if hasUpdate && update.MarkersSource == "" {
		return false, fmt.Errorf("marker source is required")
	}
	clearFlags, err := markerClearFlags(clearSegments)
	if err != nil {
		return false, err
	}
	if !hasUpdate && !clearFlags.any() {
		return false, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin marker mutation transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	state, err := loadMarkerMutationState(ctx, tx, fileID)
	if err != nil {
		return false, err
	}
	before := state
	mutationAt := time.Now().UTC()
	changed := markerSegmentFlags{}

	if hasUpdate {
		applied, err := applyMarkerUpdateToMutationState(update, &state, mutationAt)
		if err != nil {
			return false, err
		}
		changed.intro = changed.intro || applied.intro
		changed.credits = changed.credits || applied.credits
		changed.recap = changed.recap || applied.recap
		changed.preview = changed.preview || applied.preview
	}
	if clearFlags.intro {
		changed.intro = clearSegmentState(&state.intro) || changed.intro
	}
	if clearFlags.credits {
		changed.credits = clearSegmentState(&state.credits) || changed.credits
	}
	if clearFlags.recap {
		changed.recap = clearSegmentState(&state.recap) || changed.recap
	}
	if clearFlags.preview {
		changed.preview = clearSegmentState(&state.preview) || changed.preview
	}
	if !changed.any() {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit marker no-op transaction: %w", err)
		}
		return false, nil
	}

	wrote, err := writeMarkerMutationState(ctx, tx, fileID, state)
	if err != nil {
		return false, err
	}
	if wrote {
		if audit, ok := MarkerAuditContextFromContext(ctx); ok {
			if err := insertMarkerEditAuditRows(ctx, tx, fileID, before, state, changed, audit, mutationAt); err != nil {
				return false, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit marker mutation transaction: %w", err)
	}
	return wrote, nil
}

func markerClearFlags(segments []string) (markerSegmentFlags, error) {
	var flags markerSegmentFlags
	for _, seg := range segments {
		switch seg {
		case "intro":
			flags.intro = true
		case "credits":
			flags.credits = true
		case "recap":
			flags.recap = true
		case "preview":
			flags.preview = true
		default:
			return markerSegmentFlags{}, fmt.Errorf("invalid marker segment %q", seg)
		}
	}
	return flags, nil
}

func loadMarkerMutationState(ctx context.Context, tx pgx.Tx, fileID int) (markerMutationState, error) {
	var state markerMutationState
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(duration, 0),
		        markers_source,
		        markers_confidence,
		        intro_start,
		        intro_end,
		        intro_markers_source,
		        intro_markers_provider,
		        intro_markers_confidence,
		        intro_markers_algorithm,
		        intro_markers_detected_at,
		        credits_start,
		        credits_end,
		        credits_markers_source,
		        credits_markers_provider,
		        credits_markers_confidence,
		        credits_markers_algorithm,
		        credits_markers_detected_at,
		        recap_start,
		        recap_end,
		        recap_markers_source,
		        recap_markers_provider,
		        recap_markers_confidence,
		        recap_markers_algorithm,
		        recap_markers_detected_at,
		        preview_start,
		        preview_end,
		        preview_markers_source,
		        preview_markers_provider,
		        preview_markers_confidence,
		        preview_markers_algorithm,
		        preview_markers_detected_at
		 FROM media_files WHERE id = $1 FOR UPDATE`,
		fileID,
	).Scan(
		&state.duration,
		&state.existingSource,
		&state.existingConfidence,
		&state.intro.start, &state.intro.end, &state.intro.source, &state.intro.provider, &state.intro.confidence, &state.intro.algorithm, &state.intro.detectedAt,
		&state.credits.start, &state.credits.end, &state.credits.source, &state.credits.provider, &state.credits.confidence, &state.credits.algorithm, &state.credits.detectedAt,
		&state.recap.start, &state.recap.end, &state.recap.source, &state.recap.provider, &state.recap.confidence, &state.recap.algorithm, &state.recap.detectedAt,
		&state.preview.start, &state.preview.end, &state.preview.source, &state.preview.provider, &state.preview.confidence, &state.preview.algorithm, &state.preview.detectedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return markerMutationState{}, ErrFileNotFound
		}
		return markerMutationState{}, fmt.Errorf("load existing marker source: %w", err)
	}
	return state, nil
}

func applyMarkerUpdateToMutationState(update *MarkerUpdate, state *markerMutationState, mutationAt time.Time) (markerSegmentFlags, error) {
	var applied markerSegmentFlags
	introSrc, introProv, introConf, introAlgo := resolveSegmentProvenance(*update, update.IntroProvenance)
	var err error
	applied.intro, err = applySegmentPatch(&state.intro, state.existingSource, introSrc, introProv, introConf, introAlgo, update.IntroStart, update.IntroEnd, state.duration, "intro", mutationAt)
	if err != nil {
		return markerSegmentFlags{}, err
	}
	creditsSrc, creditsProv, creditsConf, creditsAlgo := resolveSegmentProvenance(*update, update.CreditsProvenance)
	applied.credits, err = applySegmentPatch(&state.credits, state.existingSource, creditsSrc, creditsProv, creditsConf, creditsAlgo, update.CreditsStart, update.CreditsEnd, state.duration, "credits", mutationAt)
	if err != nil {
		return markerSegmentFlags{}, err
	}
	recapSrc, recapProv, recapConf, recapAlgo := resolveSegmentProvenance(*update, update.RecapProvenance)
	applied.recap, err = applySegmentPatch(&state.recap, state.existingSource, recapSrc, recapProv, recapConf, recapAlgo, update.RecapStart, update.RecapEnd, state.duration, "recap", mutationAt)
	if err != nil {
		return markerSegmentFlags{}, err
	}
	previewSrc, previewProv, previewConf, previewAlgo := resolveSegmentProvenance(*update, update.PreviewProvenance)
	applied.preview, err = applySegmentPatch(&state.preview, state.existingSource, previewSrc, previewProv, previewConf, previewAlgo, update.PreviewStart, update.PreviewEnd, state.duration, "preview", mutationAt)
	if err != nil {
		return markerSegmentFlags{}, err
	}
	return applied, nil
}

func writeMarkerMutationState(
	ctx context.Context,
	tx pgx.Tx,
	fileID int,
	state markerMutationState,
) (bool, error) {
	nextSource, nextConfidence := recomputeSharedMarkerAttribution(
		state.existingSource,
		state.existingConfidence,
		state.intro,
		state.credits,
		state.recap,
		state.preview,
	)
	tag, err := tx.Exec(ctx, `
		UPDATE media_files
		SET intro_start = $2::double precision,
			intro_end = $3::double precision,
			credits_start = $4::double precision,
			credits_end = $5::double precision,
			recap_start = $6::double precision,
			recap_end = $7::double precision,
			preview_start = $8::double precision,
			preview_end = $9::double precision,
			markers_source = $10::text,
			markers_confidence = $11::double precision,
			intro_markers_source = $12::text,
			intro_markers_provider = $13::text,
			intro_markers_confidence = $14::double precision,
			intro_markers_algorithm = $15::text,
			intro_markers_detected_at = $16::timestamptz,
			credits_markers_source = $17::text,
			credits_markers_provider = $18::text,
			credits_markers_confidence = $19::double precision,
			credits_markers_algorithm = $20::text,
			credits_markers_detected_at = $21::timestamptz,
			recap_markers_source = $22::text,
			recap_markers_provider = $23::text,
			recap_markers_confidence = $24::double precision,
			recap_markers_algorithm = $25::text,
			recap_markers_detected_at = $26::timestamptz,
			preview_markers_source = $27::text,
			preview_markers_provider = $28::text,
			preview_markers_confidence = $29::double precision,
			preview_markers_algorithm = $30::text,
			preview_markers_detected_at = $31::timestamptz,
			updated_at = NOW()
		WHERE id = $1
	`,
		fileID,
		state.intro.start, state.intro.end,
		state.credits.start, state.credits.end,
		state.recap.start, state.recap.end,
		state.preview.start, state.preview.end,
		nextSource, nextConfidence,
		state.intro.source, state.intro.provider, state.intro.confidence, state.intro.algorithm, state.intro.detectedAt,
		state.credits.source, state.credits.provider, state.credits.confidence, state.credits.algorithm, state.credits.detectedAt,
		state.recap.source, state.recap.provider, state.recap.confidence, state.recap.algorithm, state.recap.detectedAt,
		state.preview.source, state.preview.provider, state.preview.confidence, state.preview.algorithm, state.preview.detectedAt,
	)
	if err != nil {
		return false, fmt.Errorf("updating media markers: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, ErrFileNotFound
	}
	return true, nil
}

func clearSegmentState(state *segmentState) bool {
	if segmentEqual(*state, segmentState{}) {
		return false
	}
	state.start = nil
	state.end = nil
	state.source = nil
	state.provider = nil
	state.confidence = nil
	state.algorithm = nil
	state.detectedAt = nil
	return true
}

func insertMarkerEditAuditRows(
	ctx context.Context,
	tx pgx.Tx,
	fileID int,
	before markerMutationState,
	after markerMutationState,
	changed markerSegmentFlags,
	audit MarkerAuditContext,
	mutationAt time.Time,
) error {
	type changedSegment struct {
		kind   string
		before segmentState
		after  segmentState
	}
	segments := make([]changedSegment, 0, 4)
	if changed.intro {
		segments = append(segments, changedSegment{"intro", before.intro, after.intro})
	}
	if changed.credits {
		segments = append(segments, changedSegment{"credits", before.credits, after.credits})
	}
	if changed.recap {
		segments = append(segments, changedSegment{"recap", before.recap, after.recap})
	}
	if changed.preview {
		segments = append(segments, changedSegment{"preview", before.preview, after.preview})
	}

	for _, segment := range segments {
		beforeJSON, err := marshalMarkerAuditSegment(markerAuditSegmentForState(segment.before))
		if err != nil {
			return err
		}
		afterJSON, err := marshalMarkerAuditSegment(markerAuditSegmentForState(segment.after))
		if err != nil {
			return err
		}
		action := "set"
		if len(afterJSON) == 0 {
			action = "clear"
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO marker_edit_audit (
				media_file_id,
				segment_kind,
				action,
				before_marker,
				after_marker,
				user_id,
				impersonator_user_id,
				api_key_id,
				request_id,
				client_ip,
				user_agent,
				created_at
			)
			VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7, $8, $9, $10::inet, $11, $12)`,
			fileID,
			segment.kind,
			action,
			nullableJSON(beforeJSON),
			nullableJSON(afterJSON),
			audit.UserID,
			audit.ImpersonatorUserID,
			audit.APIKeyID,
			nullableString(audit.RequestID),
			nullableString(audit.ClientIP),
			nullableString(audit.UserAgent),
			mutationAt,
		)
		if err != nil {
			return fmt.Errorf("insert marker edit audit row: %w", err)
		}
	}
	return nil
}

func markerAuditSegmentForState(state segmentState) *MarkerAuditSegment {
	if state.start == nil || state.end == nil {
		return nil
	}
	return &MarkerAuditSegment{
		Start:      cloneFloat(state.start),
		End:        cloneFloat(state.end),
		Source:     cloneString(state.source),
		Provider:   cloneString(state.provider),
		Confidence: cloneFloat(state.confidence),
		Algorithm:  cloneString(state.algorithm),
		DetectedAt: cloneTime(state.detectedAt),
	}
}

func marshalMarkerAuditSegment(segment *MarkerAuditSegment) ([]byte, error) {
	if segment == nil {
		return nil, nil
	}
	data, err := json.Marshal(segment)
	if err != nil {
		return nil, fmt.Errorf("marshal marker audit segment: %w", err)
	}
	return data, nil
}

func nullableJSON(data []byte) any {
	if len(data) == 0 {
		return nil
	}
	return data
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func recomputeSharedMarkerAttribution(
	legacySource *string,
	legacyConfidence *float64,
	states ...segmentState,
) (*string, *float64) {
	var (
		bestSource     *string
		bestConfidence *float64
		bestPriority   int
		found          bool
	)
	for _, state := range states {
		if state.start == nil || state.end == nil {
			continue
		}
		source := state.source
		confidence := state.confidence
		if source == nil {
			source = legacySource
			if confidence == nil {
				confidence = legacyConfidence
			}
		}
		if source == nil || *source == "" {
			continue
		}
		priority := models.MarkerSourcePriority(*source)
		if !found || priority > bestPriority || (priority == bestPriority && confidenceGreater(confidence, bestConfidence)) {
			sourceCopy := *source
			bestSource = &sourceCopy
			bestConfidence = cloneFloat(confidence)
			bestPriority = priority
			found = true
		}
	}
	if !found {
		return nil, nil
	}
	return bestSource, bestConfidence
}

func confidenceGreater(a, b *float64) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	return *a > *b
}

func cloneFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func cloneString(v *string) *string {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func cloneTime(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func unmarshalMarkerAuditSegment(data []byte) (*MarkerAuditSegment, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var segment MarkerAuditSegment
	if err := json.Unmarshal(data, &segment); err != nil {
		return nil, fmt.Errorf("unmarshal marker audit segment: %w", err)
	}
	return &segment, nil
}

func ptrFloatEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func ptrStringEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// GetByID retrieves a media file by its primary key.
func (r *FileRepository) GetByID(ctx context.Context, id int) (*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files WHERE id = $1`
	return scanMediaFile(r.pool.QueryRow(ctx, query, id))
}

// GetByIDs retrieves media files by primary key.
func (r *FileRepository) GetByIDs(ctx context.Context, ids []int) ([]*models.MediaFile, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+fileColumns+`
		FROM media_files
		WHERE id = ANY($1::int[])
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("querying media files by ids: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// GetByPath retrieves a media file by its file path.
func (r *FileRepository) GetByPath(ctx context.Context, path string) (*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files WHERE file_path = $1`
	return scanMediaFile(r.pool.QueryRow(ctx, query, path))
}

// FindActiveByPath retrieves a present media file by folder and exact file path.
func (r *FileRepository) FindActiveByPath(ctx context.Context, folderID int, filePath string) (*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE media_folder_id = $1
		  AND file_path = $2
		  AND missing_since IS NULL`
	file, err := scanMediaFile(r.pool.QueryRow(ctx, query, folderID, filePath))
	if errors.Is(err, ErrFileNotFound) {
		return nil, nil
	}
	return file, err
}

// GetByHash retrieves a media file by its file hash.
func (r *FileRepository) GetByHash(ctx context.Context, hash string) (*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files WHERE file_hash = $1 LIMIT 1`
	return scanMediaFile(r.pool.QueryRow(ctx, query, hash))
}

// GetUnmatched returns media files where content_id is absent and the file
// is still present on disk (missing_since IS NULL). Results are capped at
// limit. Files are ordered so never-attempted files are processed first,
// then by ascending ID for deterministic batching.
func (r *FileRepository) GetUnmatched(ctx context.Context, limit int) ([]*models.MediaFile, error) {
	query := `SELECT ` + mfFileColumns + ` FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		WHERE (mf.content_id IS NULL OR mf.content_id = '')
		  AND mf.missing_since IS NULL
		  AND mf.match_suppressed_at IS NULL
		  AND folders.enabled = true
		ORDER BY mf.match_attempted_at ASC NULLS FIRST, mf.id ASC
		LIMIT $1`
	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("querying unmatched files: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// ClaimUnmatched atomically selects a batch of unmatched files and stamps the
// claim time so concurrent matcher loops do not process the same rows.
func (r *FileRepository) ClaimUnmatched(ctx context.Context, limit int) ([]*models.MediaFile, error) {
	if limit <= 0 {
		limit = 500
	}

	rows, err := r.pool.Query(ctx, `
		WITH locked AS (
			SELECT
				mf.id,
				mf.media_folder_id,
				mf.group_key_version,
				mf.content_group_key,
				mf.match_attempted_at,
				CASE
					WHEN lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows')
						AND mf.content_group_key <> ''
					THEN true
					ELSE false
				END AS is_series_group
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			WHERE (mf.content_id IS NULL OR mf.content_id = '')
			  AND mf.missing_since IS NULL
			  AND mf.match_suppressed_at IS NULL
			  AND folders.enabled = true
			ORDER BY mf.match_attempted_at ASC NULLS FIRST, mf.id ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		),
		representatives AS (
			SELECT DISTINCT ON (
				locked.media_folder_id,
				CASE WHEN locked.is_series_group THEN locked.group_key_version ELSE 0 END,
				CASE WHEN locked.is_series_group THEN locked.content_group_key ELSE locked.id::text END
			)
				locked.id,
				locked.media_folder_id,
				locked.group_key_version,
				locked.content_group_key,
				locked.is_series_group
			FROM locked
			ORDER BY
				locked.media_folder_id,
				CASE WHEN locked.is_series_group THEN locked.group_key_version ELSE 0 END,
				CASE WHEN locked.is_series_group THEN locked.content_group_key ELSE locked.id::text END,
				locked.match_attempted_at ASC NULLS FIRST,
				locked.id ASC
			LIMIT $2
		),
		touched AS (
			UPDATE media_files mf
			SET match_attempted_at = NOW()
			WHERE (mf.content_id IS NULL OR mf.content_id = '')
			  AND mf.missing_since IS NULL
			  AND mf.match_suppressed_at IS NULL
			  AND EXISTS (
				SELECT 1
				FROM representatives rep
				WHERE (rep.is_series_group
					AND mf.media_folder_id = rep.media_folder_id
					AND mf.group_key_version = rep.group_key_version
					AND mf.content_group_key = rep.content_group_key)
				   OR (NOT rep.is_series_group AND mf.id = rep.id)
			  )
			RETURNING mf.id
		)
		SELECT `+mfFileColumns+`
		FROM media_files mf
		JOIN representatives rep ON rep.id = mf.id
		ORDER BY mf.id ASC
	`, claimRepresentativeWindow(limit), limit)
	if err != nil {
		return nil, fmt.Errorf("claiming unmatched files: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// ClaimUnmatchedNonSeries atomically selects unmatched files for non-TV
// libraries only. This is used when series libraries are routed through the
// native group-backed queue.
func (r *FileRepository) ClaimUnmatchedNonSeries(ctx context.Context, limit int) ([]*models.MediaFile, error) {
	if limit <= 0 {
		limit = 500
	}

	rows, err := r.pool.Query(ctx, `
		WITH locked AS (
			SELECT mf.id
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			WHERE (mf.content_id IS NULL OR mf.content_id = '')
			  AND mf.missing_since IS NULL
			  AND mf.match_suppressed_at IS NULL
			  AND folders.enabled = true
			  AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows')
			ORDER BY mf.match_attempted_at ASC NULLS FIRST, mf.id ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		),
		touched AS (
			UPDATE media_files mf
			SET match_attempted_at = NOW()
			WHERE EXISTS (
				SELECT 1
				FROM locked
				WHERE locked.id = mf.id
			)
			RETURNING mf.id
		)
		SELECT `+mfFileColumns+`
		FROM media_files mf
		JOIN locked ON locked.id = mf.id
		ORDER BY mf.id ASC
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming unmatched non-series files: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// ClaimUnmatchedMixed atomically selects unmatched files for mixed libraries
// only, excluding movie and TV libraries that are routed through dedicated
// durable queues.
func (r *FileRepository) ClaimUnmatchedMixed(ctx context.Context, limit int) ([]*models.MediaFile, error) {
	if limit <= 0 {
		limit = 500
	}

	rows, err := r.pool.Query(ctx, `
		WITH locked AS (
			SELECT mf.id
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			WHERE (mf.content_id IS NULL OR mf.content_id = '')
			  AND mf.missing_since IS NULL
			  AND mf.match_suppressed_at IS NULL
			  AND folders.enabled = true
			  AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows', 'movie', 'movies')
			  AND lower(trim(COALESCE(mf.base_type, ''))) NOT IN ('series', 'movie')
			ORDER BY mf.match_attempted_at ASC NULLS FIRST, mf.id ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		),
		touched AS (
			UPDATE media_files mf
			SET match_attempted_at = NOW()
			WHERE EXISTS (
				SELECT 1
				FROM locked
				WHERE locked.id = mf.id
			)
			RETURNING mf.id
		)
		SELECT `+mfFileColumns+`
		FROM media_files mf
		JOIN locked ON locked.id = mf.id
		ORDER BY mf.id ASC
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming unmatched mixed files: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// MarkMatchAttempted records that the match worker processed a file.
func (r *FileRepository) MarkMatchAttempted(ctx context.Context, fileID int) error {
	_, err := r.pool.Exec(ctx,
		"UPDATE media_files SET match_attempted_at = NOW() WHERE id = $1",
		fileID)
	return err
}

// IsMatchSuppressed reports whether a raw unmatched file has been cancelled
// from background matching.
func (r *FileRepository) IsMatchSuppressed(ctx context.Context, fileID int) (bool, error) {
	var suppressed bool
	err := r.pool.QueryRow(ctx, `
		SELECT match_suppressed_at IS NOT NULL
		FROM media_files
		WHERE id = $1
	`, fileID).Scan(&suppressed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("checking match suppression: %w", err)
	}
	return suppressed, nil
}

// CountUnmatchedMatchBacklogByFolder counts raw unmatched files that the
// background matcher can still claim for a library.
func (r *FileRepository) CountUnmatchedMatchBacklogByFolder(ctx context.Context, folderID int, mode RawMatchBacklogMode) (int, error) {
	var total int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		WHERE mf.media_folder_id = $1
		  AND (mf.content_id IS NULL OR mf.content_id = '')
		  AND mf.missing_since IS NULL
		  AND mf.match_suppressed_at IS NULL
		  AND folders.enabled = true
		  AND (
			$2 = 'generic'
			OR ($2 = 'non_series' AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows'))
			OR (
				$2 = 'mixed'
				AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows', 'movie', 'movies')
				AND lower(trim(COALESCE(mf.base_type, ''))) NOT IN ('series', 'movie')
			)
		  )
	`, folderID, string(normalizeRawMatchBacklogMode(mode))).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("counting unmatched match backlog: %w", err)
	}
	return total, nil
}

// ListUnmatchedMatchBacklogByFolder lists raw unmatched files that are still
// eligible for the background matcher.
func (r *FileRepository) ListUnmatchedMatchBacklogByFolder(ctx context.Context, folderID int, mode RawMatchBacklogMode, limit int, offset int) ([]*models.MediaFile, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	normalizedMode := normalizeRawMatchBacklogMode(mode)
	total, err := r.CountUnmatchedMatchBacklogByFolder(ctx, folderID, normalizedMode)
	if err != nil {
		return nil, 0, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+mfFileColumns+`
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		WHERE mf.media_folder_id = $1
		  AND (mf.content_id IS NULL OR mf.content_id = '')
		  AND mf.missing_since IS NULL
		  AND mf.match_suppressed_at IS NULL
		  AND folders.enabled = true
		  AND (
			$2 = 'generic'
			OR ($2 = 'non_series' AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows'))
			OR (
				$2 = 'mixed'
				AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows', 'movie', 'movies')
				AND lower(trim(COALESCE(mf.base_type, ''))) NOT IN ('series', 'movie')
			)
		  )
		ORDER BY mf.match_attempted_at ASC NULLS FIRST, mf.id ASC
		LIMIT $3 OFFSET $4
	`, folderID, string(normalizedMode), limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("listing unmatched match backlog: %w", err)
	}
	defer rows.Close()

	files, err := scanMediaFiles(rows)
	if err != nil {
		return nil, 0, err
	}
	return files, total, nil
}

// SuppressUnmatchedMatchBacklogByFolder prevents raw unmatched files in a
// library from being claimed by the background matcher until they are retried
// or seen by a new scan.
func (r *FileRepository) SuppressUnmatchedMatchBacklogByFolder(ctx context.Context, folderID int, mode RawMatchBacklogMode) (int, error) {
	normalizedMode := string(normalizeRawMatchBacklogMode(mode))
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_files mf
		SET match_suppressed_at = NOW(), updated_at = NOW()
		FROM media_folders folders
		WHERE folders.id = mf.media_folder_id
		  AND mf.media_folder_id = $1
		  AND (mf.content_id IS NULL OR mf.content_id = '')
		  AND mf.missing_since IS NULL
		  AND mf.match_suppressed_at IS NULL
		  AND folders.enabled = true
		  AND (
			$2 = 'generic'
			OR ($2 = 'non_series' AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows'))
			OR (
				$2 = 'mixed'
				AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows', 'movie', 'movies')
				AND lower(trim(COALESCE(mf.base_type, ''))) NOT IN ('series', 'movie')
			)
		  )
	`, folderID, normalizedMode)
	if err != nil {
		return 0, fmt.Errorf("suppressing unmatched match backlog: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// RetryUnmatchedMatchBacklogByFolder re-enables raw unmatched files in a
// library and moves them to the front of the background matcher order.
func (r *FileRepository) RetryUnmatchedMatchBacklogByFolder(ctx context.Context, folderID int, mode RawMatchBacklogMode) (int, error) {
	normalizedMode := string(normalizeRawMatchBacklogMode(mode))
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_files mf
		SET match_suppressed_at = NULL,
		    match_attempted_at = NULL,
		    updated_at = NOW()
		FROM media_folders folders
		WHERE folders.id = mf.media_folder_id
		  AND mf.media_folder_id = $1
		  AND (mf.content_id IS NULL OR mf.content_id = '')
		  AND mf.missing_since IS NULL
		  AND folders.enabled = true
		  AND (
			$2 = 'generic'
			OR ($2 = 'non_series' AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows'))
			OR (
				$2 = 'mixed'
				AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows', 'movie', 'movies')
				AND lower(trim(COALESCE(mf.base_type, ''))) NOT IN ('series', 'movie')
			)
		  )
	`, folderID, normalizedMode)
	if err != nil {
		return 0, fmt.Errorf("retrying unmatched match backlog: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func normalizeRawMatchBacklogMode(mode RawMatchBacklogMode) RawMatchBacklogMode {
	switch mode {
	case RawMatchBacklogNonSeries, RawMatchBacklogMixed:
		return mode
	default:
		return RawMatchBacklogGeneric
	}
}

// GetUnmatchedByFolderAndPathPrefix returns unmatched files for a single media
// folder restricted to a subtree path.
func (r *FileRepository) GetUnmatchedByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, limit int) ([]*models.MediaFile, error) {
	query := `SELECT ` + mfFileColumns + ` FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		WHERE mf.media_folder_id = $1
		  AND (mf.content_id IS NULL OR mf.content_id = '')
		  AND mf.missing_since IS NULL
		  AND mf.match_suppressed_at IS NULL
		  AND folders.enabled = true
		  AND (mf.file_path = $2 OR mf.file_path LIKE $3 ESCAPE '\')
		ORDER BY mf.id ASC`
	args := []any{folderID, pathPrefix, pathPrefixLike(pathPrefix)}
	if limit > 0 {
		query += ` LIMIT $4`
		args = append(args, limit)
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying unmatched files by path prefix: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// ClaimUnmatchedByFolderAndPathPrefix atomically claims unmatched files in a
// subtree. When attemptBefore is non-zero, rows already claimed during the same
// ingest run are excluded so a hard-failing file is attempted at most once.
func (r *FileRepository) ClaimUnmatchedByFolderAndPathPrefix(
	ctx context.Context,
	folderID int,
	pathPrefix string,
	limit int,
	attemptBefore time.Time,
) ([]*models.MediaFile, error) {
	if limit <= 0 {
		limit = 500
	}

	var (
		builder strings.Builder
		args    = []any{folderID, pathPrefix, pathPrefixLike(pathPrefix), claimRepresentativeWindow(limit), limit}
	)
	builder.WriteString(`
		WITH locked AS (
			SELECT
				mf.id,
				mf.media_folder_id,
				mf.group_key_version,
				mf.content_group_key,
				mf.match_attempted_at,
				CASE
					WHEN lower(trim(folders.type)) IN ('series', 'tv', 'show', 'tvshows')
						AND mf.content_group_key <> ''
					THEN true
					ELSE false
				END AS is_series_group
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			WHERE mf.media_folder_id = $1
			  AND (mf.content_id IS NULL OR mf.content_id = '')
			  AND mf.missing_since IS NULL
			  AND mf.match_suppressed_at IS NULL
			  AND folders.enabled = true
			  AND (mf.file_path = $2 OR mf.file_path LIKE $3 ESCAPE '\')
	`)
	if !attemptBefore.IsZero() {
		args = append(args, attemptBefore)
		builder.WriteString(`
			  AND (mf.match_attempted_at IS NULL OR mf.match_attempted_at < $6)
		`)
	}
	builder.WriteString(`
			ORDER BY mf.match_attempted_at ASC NULLS FIRST, mf.id ASC
			LIMIT $4
			FOR UPDATE SKIP LOCKED
		),
		representatives AS (
			SELECT DISTINCT ON (
				locked.media_folder_id,
				CASE WHEN locked.is_series_group THEN locked.group_key_version ELSE 0 END,
				CASE WHEN locked.is_series_group THEN locked.content_group_key ELSE locked.id::text END
			)
				locked.id,
				locked.media_folder_id,
				locked.group_key_version,
				locked.content_group_key,
				locked.is_series_group
			FROM locked
			ORDER BY
				locked.media_folder_id,
				CASE WHEN locked.is_series_group THEN locked.group_key_version ELSE 0 END,
				CASE WHEN locked.is_series_group THEN locked.content_group_key ELSE locked.id::text END,
				locked.match_attempted_at ASC NULLS FIRST,
				locked.id ASC
			LIMIT $5
		),
		touched AS (
			UPDATE media_files mf
			SET match_attempted_at = NOW()
			WHERE mf.media_folder_id = $1
			  AND (mf.content_id IS NULL OR mf.content_id = '')
			  AND mf.missing_since IS NULL
			  AND mf.match_suppressed_at IS NULL
			  AND EXISTS (
				SELECT 1
				FROM representatives rep
				WHERE (rep.is_series_group
					AND mf.media_folder_id = rep.media_folder_id
					AND mf.group_key_version = rep.group_key_version
					AND mf.content_group_key = rep.content_group_key)
				   OR (NOT rep.is_series_group AND mf.id = rep.id)
			  )
			RETURNING mf.id
		)
		SELECT `)
	builder.WriteString(mfFileColumns)
	builder.WriteString(`
		FROM media_files mf
		JOIN representatives rep ON rep.id = mf.id
		ORDER BY mf.id ASC`)

	rows, err := r.pool.Query(ctx, builder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("claiming unmatched files by path prefix: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// ClaimUnmatchedNonSeriesByFolderAndPathPrefix atomically claims unmatched
// files in a subtree for non-TV libraries only.
func (r *FileRepository) ClaimUnmatchedNonSeriesByFolderAndPathPrefix(
	ctx context.Context,
	folderID int,
	pathPrefix string,
	limit int,
	attemptBefore time.Time,
) ([]*models.MediaFile, error) {
	if limit <= 0 {
		limit = 500
	}

	var (
		builder strings.Builder
		args    = []any{folderID, pathPrefix, pathPrefixLike(pathPrefix), limit}
	)
	builder.WriteString(`
		WITH locked AS (
			SELECT mf.id
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			WHERE mf.media_folder_id = $1
			  AND (mf.content_id IS NULL OR mf.content_id = '')
			  AND mf.missing_since IS NULL
			  AND mf.match_suppressed_at IS NULL
			  AND folders.enabled = true
			  AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows')
			  AND (mf.file_path = $2 OR mf.file_path LIKE $3 ESCAPE '\')
	`)
	if !attemptBefore.IsZero() {
		args = append(args, attemptBefore)
		builder.WriteString(`
			  AND (mf.match_attempted_at IS NULL OR mf.match_attempted_at < $5)
		`)
	}
	builder.WriteString(`
			ORDER BY mf.match_attempted_at ASC NULLS FIRST, mf.id ASC
			LIMIT $4
			FOR UPDATE SKIP LOCKED
		),
		touched AS (
			UPDATE media_files mf
			SET match_attempted_at = NOW()
			WHERE EXISTS (
				SELECT 1
				FROM locked
				WHERE locked.id = mf.id
			)
			RETURNING mf.id
		)
		SELECT `)
	builder.WriteString(mfFileColumns)
	builder.WriteString(`
		FROM media_files mf
		JOIN locked ON locked.id = mf.id
		ORDER BY mf.id ASC`)

	rows, err := r.pool.Query(ctx, builder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("claiming unmatched non-series files by path prefix: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// ClaimUnmatchedMixedByFolderAndPathPrefix atomically claims unmatched files
// in a subtree for mixed libraries only.
func (r *FileRepository) ClaimUnmatchedMixedByFolderAndPathPrefix(
	ctx context.Context,
	folderID int,
	pathPrefix string,
	limit int,
	attemptBefore time.Time,
) ([]*models.MediaFile, error) {
	if limit <= 0 {
		limit = 500
	}

	var (
		builder strings.Builder
		args    = []any{folderID, pathPrefix, pathPrefixLike(pathPrefix), limit}
	)
	builder.WriteString(`
		WITH locked AS (
			SELECT mf.id
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			WHERE mf.media_folder_id = $1
			  AND (mf.content_id IS NULL OR mf.content_id = '')
			  AND mf.missing_since IS NULL
			  AND mf.match_suppressed_at IS NULL
			  AND folders.enabled = true
			  AND lower(trim(folders.type)) NOT IN ('series', 'tv', 'show', 'tvshows', 'movie', 'movies')
			  AND lower(trim(COALESCE(mf.base_type, ''))) NOT IN ('series', 'movie')
			  AND (mf.file_path = $2 OR mf.file_path LIKE $3 ESCAPE '\')
	`)
	if !attemptBefore.IsZero() {
		args = append(args, attemptBefore)
		builder.WriteString(`
			  AND (mf.match_attempted_at IS NULL OR mf.match_attempted_at < $5)
		`)
	}
	builder.WriteString(`
			ORDER BY mf.match_attempted_at ASC NULLS FIRST, mf.id ASC
			LIMIT $4
			FOR UPDATE SKIP LOCKED
		),
		touched AS (
			UPDATE media_files mf
			SET match_attempted_at = NOW()
			WHERE EXISTS (
				SELECT 1
				FROM locked
				WHERE locked.id = mf.id
			)
			RETURNING mf.id
		)
		SELECT `)
	builder.WriteString(mfFileColumns)
	builder.WriteString(`
		FROM media_files mf
		JOIN locked ON locked.id = mf.id
		ORDER BY mf.id ASC`)

	rows, err := r.pool.Query(ctx, builder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("claiming unmatched mixed files by path prefix: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

func claimRepresentativeWindow(limit int) int {
	if limit <= 0 {
		return 512
	}
	window := limit * 32
	if window < 512 {
		return 512
	}
	return window
}

// MarkMissing sets the missing_since timestamp for the given media file.
func (r *FileRepository) MarkMissing(ctx context.Context, id int, since time.Time) error {
	tag, err := r.pool.Exec(ctx,
		"UPDATE media_files SET missing_since = $1, updated_at = NOW() WHERE id = $2",
		since, id,
	)
	if err != nil {
		return fmt.Errorf("marking file missing: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFileNotFound
	}
	return nil
}

// DeleteMissing deletes media files that have been missing longer than the grace period.
// Returns the number of rows deleted.
func (r *FileRepository) DeleteMissing(ctx context.Context, gracePeriod time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-gracePeriod)
	tag, err := r.pool.Exec(ctx,
		"DELETE FROM media_files WHERE missing_since IS NOT NULL AND missing_since < $1",
		cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("deleting missing files: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// DeleteMissingByFolder deletes all media files marked as missing in the given folder.
// Returns the number of rows deleted.
func (r *FileRepository) DeleteMissingByFolder(ctx context.Context, folderID int) (int, error) {
	tag, err := r.pool.Exec(ctx,
		"DELETE FROM media_files WHERE media_folder_id = $1 AND missing_since IS NOT NULL",
		folderID,
	)
	if err != nil {
		return 0, fmt.Errorf("deleting missing files for folder %d: %w", folderID, err)
	}
	return int(tag.RowsAffected()), nil
}

// DeleteByIDs removes specific media file rows by primary key.
func (r *FileRepository) DeleteByIDs(ctx context.Context, ids []int) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	tag, err := r.pool.Exec(ctx,
		"DELETE FROM media_files WHERE id = ANY($1)",
		ids,
	)
	if err != nil {
		return 0, fmt.Errorf("deleting media files by id: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ListIDsOutsideRoots returns file row ids for a folder whose paths are no
// longer covered by any configured root.
func (r *FileRepository) ListIDsOutsideRoots(ctx context.Context, folderID int, roots []string) ([]int, error) {
	if len(roots) == 0 {
		rows, err := r.pool.Query(ctx, `SELECT id FROM media_files WHERE media_folder_id = $1`, folderID)
		if err != nil {
			return nil, fmt.Errorf("querying file ids outside roots: %w", err)
		}
		defer rows.Close()

		ids := make([]int, 0)
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("scanning file id outside roots: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterating file ids outside roots: %w", err)
		}
		return ids, nil
	}

	args := make([]any, 0, 1+len(roots)*2)
	args = append(args, folderID)
	coveredClauses := make([]string, 0, len(roots))
	for i, root := range roots {
		pathArg := 2 + (i * 2)
		likeArg := pathArg + 1
		coveredClauses = append(coveredClauses, fmt.Sprintf("(file_path = $%d OR file_path LIKE $%d ESCAPE '\\')", pathArg, likeArg))
		args = append(args, root, pathPrefixLike(root))
	}

	query := `SELECT id FROM media_files WHERE media_folder_id = $1 AND NOT (` + strings.Join(coveredClauses, " OR ") + `)`
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying file ids outside roots: %w", err)
	}
	defer rows.Close()

	ids := make([]int, 0)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning file id outside roots: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating file ids outside roots: %w", err)
	}
	return ids, nil
}

// GetByFolder returns all media files belonging to the specified folder.
func (r *FileRepository) GetByFolder(ctx context.Context, folderID int) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files WHERE media_folder_id = $1 ORDER BY file_path ASC`
	rows, err := r.pool.Query(ctx, query, folderID)
	if err != nil {
		return nil, fmt.Errorf("querying files by folder: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// ListActiveByFolderAndType returns present media files for a folder and base type.
func (r *FileRepository) ListActiveByFolderAndType(ctx context.Context, folderID int, baseType string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE media_folder_id = $1
		  AND base_type = $2
		  AND missing_since IS NULL
		ORDER BY file_path ASC`
	rows, err := r.pool.Query(ctx, query, folderID, baseType)
	if err != nil {
		return nil, fmt.Errorf("querying active files by folder and type: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// GetByFolderAndPathPrefix returns all files for a folder that live under a
// subtree path.
func (r *FileRepository) GetByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE media_folder_id = $1
		  AND (file_path = $2 OR file_path LIKE $3 ESCAPE '\')
		ORDER BY file_path ASC`
	rows, err := r.pool.Query(ctx, query, folderID, pathPrefix, pathPrefixLike(pathPrefix))
	if err != nil {
		return nil, fmt.Errorf("querying files by folder and path prefix: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// ListByGroupKey returns all present media files in a logical content group.
func (r *FileRepository) ListByGroupKey(ctx context.Context, folderID int, groupKeyVersion int, contentGroupKey string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE media_folder_id = $1
		  AND group_key_version = $2
		  AND content_group_key = $3
		  AND missing_since IS NULL
		ORDER BY file_path ASC`
	rows, err := r.pool.Query(ctx, query, folderID, groupKeyVersion, contentGroupKey)
	if err != nil {
		return nil, fmt.Errorf("querying files by content group: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// ListByObservedRootPath returns all present media files sharing one observed
// root path inside a media folder.
func (r *FileRepository) ListByObservedRootPath(ctx context.Context, folderID int, observedRootPath string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE media_folder_id = $1
		  AND observed_root_path = $2
		  AND missing_since IS NULL
		ORDER BY file_path ASC`
	rows, err := r.pool.Query(ctx, query, folderID, observedRootPath)
	if err != nil {
		return nil, fmt.Errorf("querying files by observed root path: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// GetByContentID returns all media files linked to the given content ID,
// ordered by resolution (highest first), excluding files that are missing.
func (r *FileRepository) GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE content_id = $1 AND missing_since IS NULL
		ORDER BY id ASC`
	rows, err := r.pool.Query(ctx, query, contentID)
	if err != nil {
		return nil, fmt.Errorf("querying files by content_id: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// ListByContentIDs returns media files grouped by content ID for the given
// content IDs, excluding files that are marked missing.
func (r *FileRepository) ListByContentIDs(ctx context.Context, contentIDs []string) (map[string][]*models.MediaFile, error) {
	grouped := make(map[string][]*models.MediaFile, len(contentIDs))
	if len(contentIDs) == 0 {
		return grouped, nil
	}

	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE content_id = ANY($1) AND missing_since IS NULL
		ORDER BY content_id ASC, id ASC`
	rows, err := r.pool.Query(ctx, query, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("querying files by content_ids: %w", err)
	}
	defer rows.Close()

	files, err := scanMediaFiles(rows)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		grouped[file.ContentID] = append(grouped[file.ContentID], file)
	}
	return grouped, nil
}

// ListByEpisodeIDs returns media files grouped by episode ID for the given
// episode IDs, excluding files that are marked missing.
func (r *FileRepository) ListByEpisodeIDs(ctx context.Context, episodeIDs []string) (map[string][]*models.MediaFile, error) {
	grouped := make(map[string][]*models.MediaFile, len(episodeIDs))
	if len(episodeIDs) == 0 {
		return grouped, nil
	}

	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE episode_id = ANY($1) AND missing_since IS NULL
		ORDER BY episode_id ASC, id ASC`
	rows, err := r.pool.Query(ctx, query, episodeIDs)
	if err != nil {
		return nil, fmt.Errorf("querying files by episode_ids: %w", err)
	}
	defer rows.Close()

	files, err := scanMediaFiles(rows)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		grouped[file.EpisodeID] = append(grouped[file.EpisodeID], file)
	}
	return grouped, nil
}

// UpdateContentID sets the content_id on a media file, linking it to a matched
// media item. This is called by the matcher after a successful resolution.
func (r *FileRepository) UpdateContentID(ctx context.Context, fileID int, contentID string) error {
	_, err := r.pool.Exec(ctx,
		"UPDATE media_files SET content_id = $1, updated_at = NOW() WHERE id = $2",
		contentID, fileID)
	return err
}

// ReplaceContentID reassigns all files linked to one content item to another.
func (r *FileRepository) ReplaceContentID(ctx context.Context, oldContentID, newContentID string) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_files
		SET content_id = $1, updated_at = NOW()
		WHERE content_id = $2
	`, newContentID, oldContentID)
	if err != nil {
		return 0, fmt.Errorf("replacing content_id on files: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// UpdateContentIDByPathPrefix sets content_id on all present (non-missing)
// media files in a folder whose path starts with the given prefix. It returns
// the number of rows affected. This is used by the backfill script to bulk-link
// files under a canonical root to their owning content item.
func (r *FileRepository) UpdateContentIDByPathPrefix(ctx context.Context, folderID int, pathPrefix, contentID string) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_files
		SET content_id = $1, updated_at = NOW()
		WHERE media_folder_id = $2
		  AND missing_since IS NULL
		  AND (content_id IS NULL OR content_id = '')
		  AND (file_path = $3 OR file_path LIKE $4 ESCAPE '\')
	`, contentID, folderID, pathPrefix, pathPrefixLike(pathPrefix))
	if err != nil {
		return 0, fmt.Errorf("updating content_id by path prefix: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// UpdateContentIDByObservedRootPath assigns one content item to all present
// files under the same observed root path in a media folder.
func (r *FileRepository) UpdateContentIDByObservedRootPath(ctx context.Context, folderID int, observedRootPath, contentID string) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_files
		SET content_id = $1, updated_at = NOW()
		WHERE media_folder_id = $2
		  AND observed_root_path = $3
		  AND missing_since IS NULL
		  AND (content_id IS NULL OR content_id <> $1)
	`, contentID, folderID, observedRootPath)
	if err != nil {
		return 0, fmt.Errorf("updating content_id by observed root path: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ClearContentID removes any matched media item linkage from a file row.
func (r *FileRepository) ClearContentID(ctx context.Context, fileID int) error {
	_, err := r.pool.Exec(ctx,
		"UPDATE media_files SET content_id = NULL, updated_at = NOW() WHERE id = $1",
		fileID)
	return err
}

// ClearContentLinksByPathPrefix removes content and episode link fields for
// present files beneath a specific root path in one media folder.
func (r *FileRepository) ClearContentLinksByPathPrefix(ctx context.Context, folderID int, pathPrefix string) (int, error) {
	var cleared int
	err := r.pool.QueryRow(ctx, `
		WITH previous AS (
			SELECT id, media_folder_id, episode_id AS old_episode_id
			FROM media_files
			WHERE media_folder_id = $1
			  AND missing_since IS NULL
			  AND (file_path = $2 OR file_path LIKE $3 ESCAPE '\')
			  AND (
				content_id IS NOT NULL OR
				episode_id IS NOT NULL OR
				season_number IS NOT NULL OR
				episode_number IS NOT NULL
			  )
		),
		cleared AS (
			UPDATE media_files
			SET content_id = NULL,
				episode_id = NULL,
				season_number = NULL,
				episode_number = NULL,
				updated_at = NOW()
			WHERE id IN (SELECT id FROM previous)
			RETURNING id
		),
		deleted AS (
			DELETE FROM episode_libraries el
			USING (
				SELECT DISTINCT media_folder_id, old_episode_id AS episode_id
				FROM previous
				WHERE old_episode_id IS NOT NULL
			) touched
			WHERE el.media_folder_id = touched.media_folder_id
			  AND el.episode_id = touched.episode_id
			  AND NOT EXISTS (
				SELECT 1
				FROM media_files mf
				WHERE mf.media_folder_id = el.media_folder_id
				  AND mf.episode_id = el.episode_id
				  AND mf.missing_since IS NULL
			  )
		)
		SELECT COUNT(*) FROM cleared
	`, folderID, pathPrefix, pathPrefixLike(pathPrefix)).Scan(&cleared)
	if err != nil {
		return 0, fmt.Errorf("clearing media file content links by path prefix: %w", err)
	}
	return cleared, nil
}

// UpdateEpisodeLink sets the episode linkage fields on a media file.
func (r *FileRepository) UpdateEpisodeLink(ctx context.Context, fileID int, episodeID string, seasonNum, episodeNum int) error {
	_, err := r.pool.Exec(ctx, `
		WITH previous AS (
			SELECT media_folder_id, episode_id AS old_episode_id
			FROM media_files
			WHERE id = $4
		),
		updated AS (
			UPDATE media_files
			SET episode_id = $1,
				season_number = $2,
				episode_number = $3,
				updated_at = NOW()
			WHERE id = $4
			RETURNING episode_id, media_folder_id, created_at, missing_since
		),
		inserted AS (
			INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at)
			SELECT episode_id, media_folder_id, created_at
			FROM updated
			WHERE episode_id IS NOT NULL
			  AND missing_since IS NULL
			ON CONFLICT (episode_id, media_folder_id) DO NOTHING
		)
		DELETE FROM episode_libraries el
		USING previous p
		WHERE p.old_episode_id IS NOT NULL
		  AND p.old_episode_id <> $1
		  AND el.media_folder_id = p.media_folder_id
		  AND el.episode_id = p.old_episode_id
		  AND NOT EXISTS (
			SELECT 1
			FROM media_files mf
			WHERE mf.media_folder_id = el.media_folder_id
			  AND mf.episode_id = el.episode_id
			  AND mf.missing_since IS NULL
		  )
	`, episodeID, seasonNum, episodeNum, fileID)
	return err
}

// BulkLinkEpisodesBySeries links all already-numbered files for a series to
// matching episode rows in one statement. Files that still lack persisted
// season/episode hints remain unlinked for the slower fallback path.
func (r *FileRepository) BulkLinkEpisodesBySeries(ctx context.Context, seriesContentID string) (int, error) {
	var linked int
	err := r.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE media_files mf
			SET episode_id = e.content_id,
				season_number = e.season_number,
				episode_number = e.episode_number,
				updated_at = NOW()
			FROM episodes e
			WHERE mf.content_id = $1
			  AND mf.episode_id IS NULL
			  AND mf.missing_since IS NULL
			  AND mf.season_number IS NOT NULL
			  AND mf.episode_number IS NOT NULL
			  AND e.series_id = $1
			  AND mf.season_number = e.season_number
			  AND mf.episode_number = e.episode_number
			RETURNING mf.episode_id, mf.media_folder_id, mf.created_at
		),
		inserted AS (
			INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at)
			SELECT episode_id, media_folder_id, MIN(created_at)
			FROM updated
			GROUP BY episode_id, media_folder_id
			ON CONFLICT (episode_id, media_folder_id) DO NOTHING
		)
		SELECT COUNT(*) FROM updated
	`, seriesContentID).Scan(&linked)
	if err != nil {
		return 0, fmt.Errorf("bulk-linking series files to episodes: %w", err)
	}
	return linked, nil
}

// FindContentIDByRootPath finds an existing linked content item for files under
// the same recognized root path within a media folder. If preferredType is
// set, matches of that type sort first.
func (r *FileRepository) FindContentIDByRootPath(ctx context.Context, folderID int, rootPath, preferredType string) (string, error) {
	query := `SELECT mf.content_id
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND mf.content_id IS NOT NULL
		  AND mf.missing_since IS NULL
		  AND (
			mf.canonical_root_path = $2 OR
			(strpos(mf.file_path, $2 || '/') = 1 AND (mf.canonical_root_path IS NULL OR mf.canonical_root_path = ''))
		  )
		ORDER BY `

	args := []any{folderID, rootPath}
	if preferredType != "" {
		query += `CASE WHEN mi.type = $3 THEN 0 ELSE 1 END, `
		args = append(args, preferredType)
	}
	query += `CASE WHEN lower(trim(mi.status)) = 'matched' THEN 0 ELSE 1 END,
		mf.id ASC
		LIMIT 1`

	var contentID string
	err := r.pool.QueryRow(ctx, query, args...).Scan(&contentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("querying content_id by root path: %w", err)
	}
	return contentID, nil
}

// FindContentIDByObservedRootPath finds an existing linked content item for
// files under the same observed root path within a media folder.
func (r *FileRepository) FindContentIDByObservedRootPath(ctx context.Context, folderID int, observedRootPath, preferredType string) (string, error) {
	query := `SELECT mf.content_id
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND mf.observed_root_path = $2
		  AND mf.content_id IS NOT NULL
		  AND mf.missing_since IS NULL
		ORDER BY `

	args := []any{folderID, observedRootPath}
	if preferredType != "" {
		query += `CASE WHEN mi.type = $3 THEN 0 ELSE 1 END, `
		args = append(args, preferredType)
	}
	query += `CASE WHEN lower(trim(mi.status)) = 'matched' THEN 0 ELSE 1 END,
		mf.id ASC LIMIT 1`

	var contentID string
	err := r.pool.QueryRow(ctx, query, args...).Scan(&contentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("querying content_id by observed root path: %w", err)
	}
	return contentID, nil
}

// FindContentIDByGroupKey finds an existing linked content item for files in
// the same logical content group within a media folder.
func (r *FileRepository) FindContentIDByGroupKey(
	ctx context.Context,
	folderID int,
	groupKeyVersion int,
	contentGroupKey string,
	preferredType string,
) (string, error) {
	query := `SELECT mf.content_id
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND mf.group_key_version = $2
		  AND mf.content_group_key = $3
		  AND mf.content_id IS NOT NULL
		  AND mf.missing_since IS NULL
		ORDER BY `

	args := []any{folderID, groupKeyVersion, contentGroupKey}
	if preferredType != "" {
		query += `CASE WHEN mi.type = $4 THEN 0 ELSE 1 END, `
		args = append(args, preferredType)
	}
	query += `CASE WHEN lower(trim(mi.status)) = 'matched' THEN 0 ELSE 1 END,
		mf.id ASC LIMIT 1`

	var contentID string
	err := r.pool.QueryRow(ctx, query, args...).Scan(&contentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("querying content_id by group key: %w", err)
	}
	return contentID, nil
}

// ListBySeriesUnlinked returns media files that have a content_id set but no
// episode_id. These files belong to a series but haven't been linked to a
// specific episode yet.
func (r *FileRepository) ListBySeriesUnlinked(ctx context.Context, seriesContentID string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE content_id = $1 AND episode_id IS NULL AND missing_since IS NULL
		ORDER BY file_path ASC`
	rows, err := r.pool.Query(ctx, query, seriesContentID)
	if err != nil {
		return nil, fmt.Errorf("querying unlinked series files: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// GetByEpisodeID returns all media files linked to the given episode ID.
func (r *FileRepository) GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE episode_id = $1 AND missing_since IS NULL
		ORDER BY id ASC`
	rows, err := r.pool.Query(ctx, query, episodeID)
	if err != nil {
		return nil, fmt.Errorf("querying files by episode_id: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// ListMissingChapterThumbnails returns present media files in enabled,
// opted-in libraries that either have no chapter probe data yet or still have
// chapters missing thumbnail assets.
func (r *FileRepository) ListMissingChapterThumbnails(ctx context.Context, limit int) ([]*models.MediaFile, error) {
	query := `SELECT ` + mfFileColumns + ` FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		WHERE mf.missing_since IS NULL
		  AND folders.enabled = true
		  AND folders.chapter_thumbnails_enabled = true
		  AND (
			mf.chapter_thumbnail_retry_after IS NULL
			OR mf.chapter_thumbnail_retry_after <= NOW()
		  )
		  AND (
			mf.chapters IS NULL
			OR (
				jsonb_typeof(mf.chapters) = 'array'
				AND jsonb_array_length(mf.chapters) > 0
				AND EXISTS (
					SELECT 1
					FROM jsonb_array_elements(mf.chapters) AS chapter
					WHERE COALESCE(chapter->>'thumbnail_path', '') = ''
					  AND (
						COALESCE(chapter->>'thumbnail_retry_after', '') = ''
						OR (chapter->>'thumbnail_retry_after')::timestamptz <= NOW()
					  )
				)
			)
		  )
		ORDER BY mf.probe_updated_at ASC NULLS FIRST, mf.id ASC
		LIMIT $1`
	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("querying files missing chapter thumbnails: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// nilIfEmpty returns nil if the string is empty, otherwise a pointer to it.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nilIfZero returns nil if the int is zero, otherwise a pointer to it.
func nilIfZero(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func pathPrefixLike(pathPrefix string) string {
	return pathscope.PrefixLike(pathPrefix)
}
