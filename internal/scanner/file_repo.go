package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pathscope"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanbatch"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/stream"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors for file repository operations.
var (
	ErrFileNotFound      = errors.New("media file not found")
	ErrStaleMarkerUpdate = errors.New("marker result superseded by a newer file identity")
	// ErrStaleCopySafetyScan reports that a multi-PPS verdict was computed from
	// a generation of the file the row no longer holds — it was rewritten (or
	// removed) while the scan ran. The verdict is not wrong, it just describes
	// bytes nobody is serving any more, so it must not overwrite the row and
	// must not be pushed at live sessions.
	ErrStaleCopySafetyScan = errors.New("copy-safety verdict superseded by a newer generation of the file")
	// ErrFileMoved reports a link write refused because the row no longer has
	// the path the caller read: a scan moved it, typically retitling it for
	// rematch (see RetitleMovedFile), after the caller's snapshot was taken.
	ErrFileMoved = errors.New("media file moved since it was read")
)

// FileRepository provides CRUD operations for the media_files table.
type FileRepository struct {
	pool *pgxpool.Pool
	// virtualCandidateStoreWindow reports the configured candidate store
	// window. Zero (or nil) keeps the pre-window sweep behavior. Wired lazily
	// from the settings store so an admin change applies without a restart.
	virtualCandidateStoreWindow func() time.Duration
}

type fileQueryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
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

// SetVirtualCandidateStoreWindow wires the candidate store window reader used
// by ReplaceVirtualCandidates to retain a listed candidate inside the window.
// It returns the receiver so callers can chain it onto a freshly built
// repository; a nil callback restores the pre-window sweep behavior.
func (r *FileRepository) SetVirtualCandidateStoreWindow(window func() time.Duration) *FileRepository {
	if r == nil {
		return r
	}
	r.virtualCandidateStoreWindow = window
	return r
}

// fileColumns is the list of columns returned by all SELECT queries.
const fileColumns = `id, content_id, episode_id, extra_id, season_number, episode_number,
	media_folder_id, canonical_root_path, observed_root_path, content_group_key, group_key_version,
	base_title, base_year, base_type, identity_confidence, identity_json,
	file_path, file_size, file_modified_at, file_hash,
	codec_video, codec_audio, resolution, audio_channels, hdr, container,
	duration, bitrate, video_tracks, audio_tracks, subtitle_tracks, external_subtitles, chapters, marker_thumbnails,
	chapter_thumbnail_retry_after, chapter_thumbnail_failure_count, chapter_thumbnail_last_error,
	intro_start, intro_end, credits_start, credits_end, recap_start, recap_end, preview_start, preview_end, marker_segments, markers_source, markers_confidence,
	intro_markers_source, intro_markers_provider, intro_markers_confidence, intro_markers_algorithm, intro_markers_detected_at,
	credits_markers_source, credits_markers_provider, credits_markers_confidence, credits_markers_algorithm, credits_markers_detected_at,
	recap_markers_source, recap_markers_provider, recap_markers_confidence, recap_markers_algorithm, recap_markers_detected_at,
	preview_markers_source, preview_markers_provider, preview_markers_confidence, preview_markers_algorithm, preview_markers_detected_at,
	edition_raw, edition_key, edition_confidence, edition_source,
	release_name, release_group,
	presentation_kind, presentation_group_key, presentation_part_index, presentation_part_total,
	multi_episode_start, multi_episode_end,
	multiple_pps, multiple_pps_scan_size, multiple_pps_scan_mtime,
	probe_source, probe_updated_at, probe_failed_at, probe_version, match_attempted_at, missing_since, failed_at,
	first_seen_scan_run_id, created_at, updated_at,
	virtual_owner_installation_id, last_delivered_at,
	resolved_url, resolved_url_expires_at,
	provider_video_hash, provider_guid, provider_release_name, provider_release_size,
	provider_request_headers`

const overlayFileColumns = `content_id, episode_id, media_folder_id, file_path,
	codec_video, codec_audio, resolution, audio_channels, hdr, container,
	video_tracks, audio_tracks, subtitle_tracks, external_subtitles, edition_key`

// mfOverlayFileColumns is overlayFileColumns qualified with the "mf" alias.
const mfOverlayFileColumns = `mf.content_id, mf.episode_id, mf.media_folder_id, mf.file_path,
	mf.codec_video, mf.codec_audio, mf.resolution, mf.audio_channels, mf.hdr, mf.container,
	mf.video_tracks, mf.audio_tracks, mf.subtitle_tracks, mf.external_subtitles, mf.edition_key`

// mfFileColumns qualifies every column with the "mf" alias for use in JOIN queries
// where unqualified "id" would be ambiguous.
const mfFileColumns = `mf.id, mf.content_id, mf.episode_id, mf.extra_id, mf.season_number, mf.episode_number,
	mf.media_folder_id, mf.canonical_root_path, mf.observed_root_path, mf.content_group_key, mf.group_key_version,
	mf.base_title, mf.base_year, mf.base_type, mf.identity_confidence, mf.identity_json,
	mf.file_path, mf.file_size, mf.file_modified_at, mf.file_hash,
	mf.codec_video, mf.codec_audio, mf.resolution, mf.audio_channels, mf.hdr, mf.container,
	mf.duration, mf.bitrate, mf.video_tracks, mf.audio_tracks, mf.subtitle_tracks, mf.external_subtitles, mf.chapters, mf.marker_thumbnails,
	mf.chapter_thumbnail_retry_after, mf.chapter_thumbnail_failure_count, mf.chapter_thumbnail_last_error,
	mf.intro_start, mf.intro_end, mf.credits_start, mf.credits_end, mf.recap_start, mf.recap_end, mf.preview_start, mf.preview_end, mf.marker_segments, mf.markers_source, mf.markers_confidence,
	mf.intro_markers_source, mf.intro_markers_provider, mf.intro_markers_confidence, mf.intro_markers_algorithm, mf.intro_markers_detected_at,
	mf.credits_markers_source, mf.credits_markers_provider, mf.credits_markers_confidence, mf.credits_markers_algorithm, mf.credits_markers_detected_at,
	mf.recap_markers_source, mf.recap_markers_provider, mf.recap_markers_confidence, mf.recap_markers_algorithm, mf.recap_markers_detected_at,
	mf.preview_markers_source, mf.preview_markers_provider, mf.preview_markers_confidence, mf.preview_markers_algorithm, mf.preview_markers_detected_at,
	mf.edition_raw, mf.edition_key, mf.edition_confidence, mf.edition_source,
	mf.release_name, mf.release_group,
	mf.presentation_kind, mf.presentation_group_key, mf.presentation_part_index, mf.presentation_part_total,
	mf.multi_episode_start, mf.multi_episode_end,
	mf.multiple_pps, mf.multiple_pps_scan_size, mf.multiple_pps_scan_mtime,
	mf.probe_source, mf.probe_updated_at, mf.probe_failed_at, mf.probe_version, mf.match_attempted_at, mf.missing_since, mf.failed_at,
	mf.first_seen_scan_run_id, mf.created_at, mf.updated_at,
	mf.virtual_owner_installation_id, mf.last_delivered_at,
	mf.resolved_url, mf.resolved_url_expires_at,
	mf.provider_video_hash, mf.provider_guid, mf.provider_release_name, mf.provider_release_size,
	mf.provider_request_headers`

// scanMediaFile scans a single row into a *models.MediaFile.
func scanMediaFile(row pgx.Row) (*models.MediaFile, error) {
	var f models.MediaFile
	var contentID *string
	var episodeID *string
	var extraID *string
	var seasonNumber, episodeNumber *int
	var canonicalRootPath *string
	var observedRootPath, contentGroupKey, baseTitle, baseType, identityConfidence *string
	var groupKeyVersion, baseYear *int
	var identityJSON []byte
	var fileModifiedAt *time.Time
	var fileHash *string
	var codecVideo, codecAudio, resolution, container, probeSource *string
	var probeVersion int
	var markersSource, introMarkersSource, introMarkersProvider, introMarkersAlgorithm *string
	var creditsMarkersSource, creditsMarkersProvider, creditsMarkersAlgorithm *string
	var recapMarkersSource, recapMarkersProvider, recapMarkersAlgorithm *string
	var previewMarkersSource, previewMarkersProvider, previewMarkersAlgorithm *string
	var chapterThumbnailLastError *string
	var editionRaw, editionKey, editionSource *string
	var releaseName, releaseGroup *string
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
	var firstSeenScanRunID *string
	var chapterThumbnailRetryAfter *time.Time
	var videoTracksJSON, audioTracksJSON, subtitleTracksJSON, externalSubtitlesJSON, chaptersJSON []byte
	var virtualOwnerInstallationID *int
	var lastDeliveredAt *time.Time
	var resolvedURL *string
	var resolvedURLExpiresAt *time.Time
	var providerVideoHash, providerGUID, providerReleaseName *string
	var providerReleaseSize *int64
	var providerRequestHeaders []byte

	err := row.Scan(
		&f.ID,
		&contentID,
		&episodeID,
		&extraID,
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
		&f.MarkerThumbnails,
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
		&f.MarkerSegments,
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
		&releaseName,
		&releaseGroup,
		&presentationKind,
		&presentationGroupKey,
		&presentationPartIndex,
		&presentationPartTotal,
		&multiEpisodeStart,
		&multiEpisodeEnd,
		&f.MultiplePPS,
		&f.MultiplePPSScanSize,
		&f.MultiplePPSScanMtime,
		&probeSource,
		&f.ProbeUpdatedAt,
		&f.ProbeFailedAt,
		&probeVersion,
		&f.MatchAttemptedAt,
		&f.MissingSince,
		&f.FailedAt,
		&firstSeenScanRunID,
		&f.CreatedAt,
		&f.UpdatedAt,
		&virtualOwnerInstallationID,
		&lastDeliveredAt,
		&resolvedURL,
		&resolvedURLExpiresAt,
		&providerVideoHash,
		&providerGUID,
		&providerReleaseName,
		&providerReleaseSize,
		&providerRequestHeaders,
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
	if extraID != nil {
		f.ExtraID = *extraID
	}
	if seasonNumber != nil {
		f.SeasonNumber = *seasonNumber
	}
	if episodeNumber != nil {
		f.EpisodeNumber = *episodeNumber
	}
	if virtualOwnerInstallationID != nil {
		f.VirtualOwnerInstallationID = *virtualOwnerInstallationID
		f.VirtualOwnerInstallationSet = true
	}
	if lastDeliveredAt != nil {
		f.LastDeliveredAt = lastDeliveredAt
	}
	if resolvedURL != nil {
		f.ResolvedURL = *resolvedURL
	}
	if resolvedURLExpiresAt != nil {
		f.ResolvedURLExpiresAt = resolvedURLExpiresAt
	}
	if providerVideoHash != nil {
		f.ProviderVideoHash = *providerVideoHash
	}
	if providerGUID != nil {
		f.ProviderGUID = *providerGUID
	}
	if providerReleaseName != nil {
		f.ProviderReleaseName = *providerReleaseName
	}
	if providerReleaseSize != nil {
		f.ProviderReleaseSize = *providerReleaseSize
	}
	if len(providerRequestHeaders) > 0 {
		if err := json.Unmarshal(providerRequestHeaders, &f.ProviderRequestHeaders); err != nil {
			return nil, fmt.Errorf("unmarshaling provider_request_headers: %w", err)
		}
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
	f.ProbeVersion = probeVersion
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
	if releaseName != nil {
		f.ReleaseName = *releaseName
	}
	if releaseGroup != nil {
		f.ReleaseGroup = *releaseGroup
	}
	if presentationKind != nil {
		f.PresentationKind = *presentationKind
	}
	if presentationGroupKey != nil {
		f.PresentationGroupKey = *presentationGroupKey
	}
	if firstSeenScanRunID != nil {
		f.FirstSeenScanRunID = *firstSeenScanRunID
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
		var extraID *string
		var seasonNumber, episodeNumber *int
		var canonicalRootPath *string
		var observedRootPath, contentGroupKey, baseTitle, baseType, identityConfidence *string
		var groupKeyVersion, baseYear *int
		var identityJSON []byte
		var fileModifiedAt *time.Time
		var fileHash *string
		var codecVideo, codecAudio, resolution, container, probeSource *string
		var probeVersion int
		var markersSource, introMarkersSource, introMarkersProvider, introMarkersAlgorithm *string
		var creditsMarkersSource, creditsMarkersProvider, creditsMarkersAlgorithm *string
		var recapMarkersSource, recapMarkersProvider, recapMarkersAlgorithm *string
		var previewMarkersSource, previewMarkersProvider, previewMarkersAlgorithm *string
		var chapterThumbnailLastError *string
		var editionRaw, editionKey, editionSource *string
		var releaseName, releaseGroup *string
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
		var firstSeenScanRunID *string
		var chapterThumbnailRetryAfter *time.Time
		var videoTracksJSON, audioTracksJSON, subtitleTracksJSON, externalSubtitlesJSON, chaptersJSON []byte
		var virtualOwnerInstallationID *int
		var lastDeliveredAt *time.Time
		var resolvedURL *string
		var resolvedURLExpiresAt *time.Time
		var providerVideoHash, providerGUID, providerReleaseName *string
		var providerReleaseSize *int64
		var providerRequestHeaders []byte

		err := rows.Scan(
			&f.ID,
			&contentID,
			&episodeID,
			&extraID,
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
			&f.MarkerThumbnails,
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
			&f.MarkerSegments,
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
			&releaseName,
			&releaseGroup,
			&presentationKind,
			&presentationGroupKey,
			&presentationPartIndex,
			&presentationPartTotal,
			&multiEpisodeStart,
			&multiEpisodeEnd,
			&f.MultiplePPS,
			&f.MultiplePPSScanSize,
			&f.MultiplePPSScanMtime,
			&probeSource,
			&f.ProbeUpdatedAt,
			&f.ProbeFailedAt,
			&probeVersion,
			&f.MatchAttemptedAt,
			&f.MissingSince,
			&f.FailedAt,
			&firstSeenScanRunID,
			&f.CreatedAt,
			&f.UpdatedAt,
			&virtualOwnerInstallationID,
			&lastDeliveredAt,
			&resolvedURL,
			&resolvedURLExpiresAt,
			&providerVideoHash,
			&providerGUID,
			&providerReleaseName,
			&providerReleaseSize,
			&providerRequestHeaders,
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
		if extraID != nil {
			f.ExtraID = *extraID
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
		f.ProbeVersion = probeVersion
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
		if releaseName != nil {
			f.ReleaseName = *releaseName
		}
		if releaseGroup != nil {
			f.ReleaseGroup = *releaseGroup
		}
		if presentationKind != nil {
			f.PresentationKind = *presentationKind
		}
		if presentationGroupKey != nil {
			f.PresentationGroupKey = *presentationGroupKey
		}
		if firstSeenScanRunID != nil {
			f.FirstSeenScanRunID = *firstSeenScanRunID
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
		if virtualOwnerInstallationID != nil {
			f.VirtualOwnerInstallationID = *virtualOwnerInstallationID
			f.VirtualOwnerInstallationSet = true
		}
		if lastDeliveredAt != nil {
			f.LastDeliveredAt = lastDeliveredAt
		}
		if resolvedURL != nil {
			f.ResolvedURL = *resolvedURL
		}
		if resolvedURLExpiresAt != nil {
			f.ResolvedURLExpiresAt = resolvedURLExpiresAt
		}
		if providerVideoHash != nil {
			f.ProviderVideoHash = *providerVideoHash
		}
		if providerGUID != nil {
			f.ProviderGUID = *providerGUID
		}
		if providerReleaseName != nil {
			f.ProviderReleaseName = *providerReleaseName
		}
		if providerReleaseSize != nil {
			f.ProviderReleaseSize = *providerReleaseSize
		}
		if len(providerRequestHeaders) > 0 {
			if err := json.Unmarshal(providerRequestHeaders, &f.ProviderRequestHeaders); err != nil {
				return nil, fmt.Errorf("unmarshaling provider_request_headers: %w", err)
			}
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

func scanOverlayMediaFiles(rows pgx.Rows) ([]*models.MediaFile, error) {
	var files []*models.MediaFile
	for rows.Next() {
		var f models.MediaFile
		var contentID, episodeID, filePath, codecVideo, codecAudio, resolution, container, editionKey *string
		var audioChannels *int
		var hdr *bool
		var videoTracksJSON, audioTracksJSON, subtitleTracksJSON, externalSubtitlesJSON []byte

		if err := rows.Scan(
			&contentID,
			&episodeID,
			&f.MediaFolderID,
			&filePath,
			&codecVideo,
			&codecAudio,
			&resolution,
			&audioChannels,
			&hdr,
			&container,
			&videoTracksJSON,
			&audioTracksJSON,
			&subtitleTracksJSON,
			&externalSubtitlesJSON,
			&editionKey,
		); err != nil {
			return nil, fmt.Errorf("scanning overlay media file: %w", err)
		}

		f.ContentID = stringPtrValue(contentID)
		f.EpisodeID = stringPtrValue(episodeID)
		f.FilePath = stringPtrValue(filePath)
		f.CodecVideo = stringPtrValue(codecVideo)
		f.CodecAudio = stringPtrValue(codecAudio)
		f.Resolution = stringPtrValue(resolution)
		f.Container = stringPtrValue(container)
		f.EditionKey = stringPtrValue(editionKey)
		if audioChannels != nil {
			f.AudioChannels = *audioChannels
		}
		if hdr != nil {
			f.HDR = *hdr
		}

		if len(videoTracksJSON) > 0 {
			if err := json.Unmarshal(videoTracksJSON, &f.VideoTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay video_tracks: %w", err)
			}
		}
		if len(audioTracksJSON) > 0 {
			if err := json.Unmarshal(audioTracksJSON, &f.AudioTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay audio_tracks: %w", err)
			}
		}
		if len(subtitleTracksJSON) > 0 {
			if err := json.Unmarshal(subtitleTracksJSON, &f.SubtitleTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay subtitle_tracks: %w", err)
			}
		}
		if len(externalSubtitlesJSON) > 0 {
			if err := json.Unmarshal(externalSubtitlesJSON, &f.ExternalSubtitles); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay external_subtitles: %w", err)
			}
		}

		files = append(files, &f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating overlay media file rows: %w", err)
	}
	return files, nil
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
// SaveCopySafetyVerdict persists the multi-PPS bitstream scan result so the
// first play after a restart does not re-read the opening seconds of remote
// media. Validity is re-checked against the file's size on load.
func (r *FileRepository) SaveCopySafetyVerdict(ctx context.Context, id int, multi bool, size int64) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE media_files
		SET copy_safety_multi=$2, copy_safety_checked_size=$3, copy_safety_checked_at=NOW()
		WHERE id=$1`, id, multi, size)
	return err
}

// LoadCopySafetyVerdict returns the persisted multi-PPS verdict for id when a
// scan was recorded for the same file size.
func (r *FileRepository) LoadCopySafetyVerdict(ctx context.Context, id int, size int64) (multi bool, ok bool, err error) {
	var (
		multiVal *bool
		sizeVal  *int64
	)
	err = r.pool.QueryRow(ctx, `
		SELECT copy_safety_multi, copy_safety_checked_size
		FROM media_files
		WHERE id=$1 AND copy_safety_multi IS NOT NULL
		  AND copy_safety_checked_size=$2`, id, size).Scan(&multiVal, &sizeVal)
	if err != nil {
		return false, false, err
	}
	if multiVal == nil || sizeVal == nil || *sizeVal != size {
		return false, false, nil
	}
	return *multiVal, true, nil
}

func (r *FileRepository) Upsert(ctx context.Context, mf models.MediaFile) (*models.MediaFile, error) {
	return r.upsertWithQueryer(ctx, r.pool, mf)
}

// UpsertTx inserts or updates a media file inside the caller's transaction.
// Audiobook folder scans use this to commit the item and all of its parts as
// one unit instead of leaving half-indexed books when one file write fails.
func (r *FileRepository) UpsertTx(ctx context.Context, tx pgx.Tx, mf models.MediaFile) (*models.MediaFile, error) {
	if tx == nil {
		return nil, fmt.Errorf("media file upsert: nil transaction")
	}
	return r.upsertWithQueryer(ctx, tx, mf)
}

// UpsertBatchTx queues media-file upserts on one transaction and sends them as
// a single PostgreSQL batch. This keeps multipart audiobook reconciliation
// atomic while reducing one client/server round trip per part.
func (r *FileRepository) UpsertBatchTx(ctx context.Context, tx pgx.Tx, files []models.MediaFile) error {
	if tx == nil {
		return fmt.Errorf("media file batch upsert: nil transaction")
	}
	if len(files) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for i := range files {
		capture := &fileUpsertCapture{}
		if _, err := r.upsertWithQueryer(ctx, capture, files[i]); err != nil {
			return err
		}
		query := capture.query
		if idx := strings.Index(query, "RETURNING "); idx >= 0 {
			query = query[:idx]
		}
		batch.Queue(query, capture.args...)
	}
	results := tx.SendBatch(ctx, batch)
	defer results.Close()
	for range files {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("media file batch upsert: %w", err)
		}
	}
	return nil
}

// LockByPathsTx locks the media_files rows at paths, in id order, within the
// caller's transaction. A paths entry with no row locks nothing.
func (r *FileRepository) LockByPathsTx(ctx context.Context, tx pgx.Tx, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		SELECT 1 FROM media_files
		WHERE file_path = ANY($1)
		ORDER BY id
		FOR NO KEY UPDATE
	`, paths); err != nil {
		return fmt.Errorf("locking media files: %w", err)
	}
	return nil
}

// fileUpsertCapture lets UpsertBatchTx reuse the canonical upsert SQL and
// argument normalization without duplicating its large column list.
type fileUpsertCapture struct {
	query string
	args  []any
}

func (c *fileUpsertCapture) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	c.query = query
	c.args = append([]any(nil), args...)
	return c
}

func (c *fileUpsertCapture) Scan(...any) error { return nil }

func (r *FileRepository) upsertWithQueryer(ctx context.Context, queryer fileQueryer, mf models.MediaFile) (*models.MediaFile, error) {
	if queryer == nil {
		return nil, fmt.Errorf("media file upsert: nil queryer")
	}
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
	var extraID *string
	if mf.ExtraID != "" {
		extraID = &mf.ExtraID
	}
	var fileHash *string
	if mf.FileHash != "" {
		fileHash = &mf.FileHash
	}
	var probeSource *string
	if mf.ProbeSource != "" {
		probeSource = &mf.ProbeSource
	}
	groupKeyVersion, identityConfidence, identityJSON := identityColumnDefaults(mf)

	query := `INSERT INTO media_files (
		content_id, episode_id, extra_id, season_number, episode_number,
		media_folder_id, canonical_root_path, observed_root_path, content_group_key, group_key_version,
		base_title, base_year, base_type, identity_confidence, identity_json,
		file_path, file_size, file_modified_at, file_hash,
		codec_video, codec_audio, resolution, audio_channels, hdr, container,
		duration, bitrate, video_tracks, audio_tracks, subtitle_tracks, external_subtitles, chapters,
		intro_start, intro_end, credits_start, credits_end, markers_source, markers_confidence,
		edition_raw, edition_key, edition_confidence, edition_source,
		release_name, release_group,
		presentation_kind, presentation_group_key, presentation_part_index, presentation_part_total,
		multi_episode_start, multi_episode_end,
		probe_source, probe_updated_at, probe_failed_at, probe_version, missing_since,
		first_seen_scan_run_id, virtual_owner_installation_id
	) VALUES (
		$1, $2, $3, $4, $5,
		$6, $7, $8, $9, $10,
		$11, $12, $13, $14, $15,
		$16, $17, $18, $19,
		$20, $21, $22, $23, $24, $25,
		$26, $27, $28, $29, $30, $31, $32,
		$33, $34, $35, $36, $37, $38,
		$39, $40, $41, $42, $43, $44,
		$45, $46, $47, $48,
		$49, $50,
		$51, $52, $53, $54, $55, $56, $57
	)
	ON CONFLICT (file_path) WHERE virtual_owner_installation_id IS NULL DO UPDATE SET
		content_id = CASE
			WHEN EXCLUDED.extra_id IS NOT NULL THEN NULL
			ELSE COALESCE(EXCLUDED.content_id, media_files.content_id)
		END,
		episode_id = CASE
			WHEN EXCLUDED.extra_id IS NOT NULL THEN NULL
			ELSE COALESCE(EXCLUDED.episode_id, media_files.episode_id)
		END,
		extra_id = EXCLUDED.extra_id,
		season_number = CASE
			WHEN EXCLUDED.extra_id IS NOT NULL THEN NULL
			ELSE COALESCE(EXCLUDED.season_number, media_files.season_number)
		END,
		episode_number = CASE
			WHEN EXCLUDED.extra_id IS NOT NULL THEN NULL
			ELSE COALESCE(EXCLUDED.episode_number, media_files.episode_number)
		END,
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
		release_name = EXCLUDED.release_name,
		release_group = EXCLUDED.release_group,
		presentation_kind = EXCLUDED.presentation_kind,
		presentation_group_key = EXCLUDED.presentation_group_key,
		presentation_part_index = EXCLUDED.presentation_part_index,
		presentation_part_total = EXCLUDED.presentation_part_total,
		multi_episode_start = EXCLUDED.multi_episode_start,
		multi_episode_end = EXCLUDED.multi_episode_end,
		probe_source = EXCLUDED.probe_source,
		probe_updated_at = EXCLUDED.probe_updated_at,
		probe_version = EXCLUDED.probe_version,
		-- A successful probe clears the rejection and a new rejection
		-- replaces it. A write that carries neither (the probe was skipped,
		-- timed out, or could not read the file) keeps the stored rejection
		-- while the bytes it describes are unchanged; changed bytes drop it.
		-- media_files.* here are the row's values before this update.
		probe_failed_at = CASE
			WHEN EXCLUDED.probe_updated_at IS NOT NULL THEN NULL
			WHEN EXCLUDED.probe_failed_at IS NOT NULL THEN EXCLUDED.probe_failed_at
			WHEN media_files.file_size IS NOT DISTINCT FROM EXCLUDED.file_size
				AND date_trunc('microseconds', media_files.file_modified_at)
					IS NOT DISTINCT FROM date_trunc('microseconds', EXCLUDED.file_modified_at)
				THEN media_files.probe_failed_at
			ELSE NULL
		END,
		match_suppressed_at = NULL,
		missing_since = NULL,
		-- A file written onto a retitle's stand-in is a real file now.
		stand_in_for_file_id = NULL,
		updated_at = NOW()
	RETURNING ` + fileColumns

	row := queryer.QueryRow(ctx, query,
		contentID,
		episodeID,
		extraID,
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
		mf.EditionConfidence,
		mf.EditionSource,
		mf.ReleaseName,
		mf.ReleaseGroup,
		mf.PresentationKind,
		mf.PresentationGroupKey,
		nilIfZero(mf.PresentationPartIndex),
		nilIfZero(mf.PresentationPartTotal),
		nilIfZero(mf.MultiEpisodeStart),
		nilIfZero(mf.MultiEpisodeEnd),
		probeSource,
		mf.ProbeUpdatedAt,
		mf.ProbeFailedAt,
		mf.ProbeVersion,
		mf.MissingSince,
		nilIfEmpty(scanbatch.RunID(ctx)),
		virtualOwnerInstallationValue(mf),
	)
	if _, ok := queryer.(*fileUpsertCapture); ok {
		return nil, nil
	}

	return scanMediaFile(row)
}

func virtualOwnerInstallationValue(mf models.MediaFile) any {
	if mf.VirtualOwnerInstallationSet || mf.VirtualOwnerInstallationID > 0 {
		return mf.VirtualOwnerInstallationID
	}
	return nil
}

// VirtualCandidate is provider-neutral technical metadata for one ephemeral
// stream choice. URI must remain a canonical virtual:// handle; provider URLs
// are resolved only when playback opens the selected file.
type VirtualCandidate struct {
	OwnerInstallationID int
	URI                 string
	Label               string
	Resolution          string
	CodecVideo          string
	CodecAudio          string
	HDR                 string
	FileSize            int64
	Bitrate             int
	AudioLanguages      []string
	SubtitleLanguages   []string
	AudioChannels       string
	HasAtmos            bool
	Is10Bit             bool
	IsMultiAudio        bool
	VisualTags          []string
	// ResolvedURL is the candidate's provider stream URL, persisted so a later
	// phase can reuse it. It is purely additive to URI. ResolvedURLExpiresAt
	// is the URL's parsed expiry (nil when unparseable).
	ResolvedURL          string
	ResolvedURLExpiresAt *time.Time
	// ProviderVideoHash, ProviderGUID, ProviderReleaseName and
	// ProviderReleaseSize are the candidate's durable identity, in the same
	// tier order as the resolver's dedup key. ProviderReleaseName is always
	// derived when the candidate has any usable name, so every row is
	// re-matchable by at least name+size.
	ProviderVideoHash   string
	ProviderGUID        string
	ProviderReleaseName string
	ProviderReleaseSize int64
	// ProviderRequestHeaders is the relay-forwardable header set (Referer,
	// Origin, User-Agent) the provider URL needs. It is stored with
	// ResolvedURL and preserved on omission, exactly like the URL.
	ProviderRequestHeaders map[string]string
}

// ReplaceVirtualCandidates atomically replaces the just-in-time candidates
// for one canonical source, episode, profile, and plugin owner. Provider result
// IDs can change between requests; replacement prevents stale rows from
// accumulating forever while retaining other profiles and installations.
func (r *FileRepository) ReplaceVirtualCandidates(ctx context.Context, source *models.MediaFile, candidates []VirtualCandidate) error {
	if r == nil || r.pool == nil {
		return errors.New("file repository is not configured")
	}
	if source == nil || source.ContentID == "" {
		return errors.New("virtual candidate source is required")
	}
	if len(candidates) == 0 {
		// An empty listing is a provider hiccup, not a statement that the title
		// lost every release. It must not rewrite, sweep, or otherwise touch the
		// persisted candidate state: a provider that intermittently returns a
		// zero-count answer would otherwise erase a healthy version list. The
		// provider failure is surfaced by the caller, and the next non-empty
		// listing replaces the rows as usual.
		return nil
	}
	if source.VirtualOwnerInstallationID <= 0 {
		for _, candidate := range candidates {
			if candidate.OwnerInstallationID > 0 {
				source.VirtualOwnerInstallationID = candidate.OwnerInstallationID
				break
			}
		}
		if source.VirtualOwnerInstallationID <= 0 && source.ContentID != "" {
			_ = r.pool.QueryRow(ctx, `SELECT COALESCE(virtual_owner_installation_id, 0) FROM media_items WHERE content_id = $1`, source.ContentID).Scan(&source.VirtualOwnerInstallationID)
		}
		if source.VirtualOwnerInstallationID <= 0 {
			_ = r.pool.QueryRow(ctx, `SELECT id FROM plugin_installations WHERE enabled = true ORDER BY id ASC LIMIT 1`).Scan(&source.VirtualOwnerInstallationID)
		}
	}
	if len(candidates) > 50 {
		candidates = candidates[:50]
	}
	group, ok := virtualCandidateGroup(source.FilePath)
	if !ok {
		return errors.New("virtual candidate source URI is invalid")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin virtual candidate replacement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	keep := make(map[int]struct{}, len(candidates))
	owners := map[int]struct{}{source.VirtualOwnerInstallationID: {}}
	for _, candidate := range candidates {
		ownerInstallationID := candidate.OwnerInstallationID
		if ownerInstallationID <= 0 {
			ownerInstallationID = source.VirtualOwnerInstallationID
		}
		owners[ownerInstallationID] = struct{}{}
		candidateGroup, candidateOK := virtualCandidateGroup(candidate.URI)
		if !candidateOK || candidateGroup != group || !virtualCandidateSelection(candidate.URI) {
			return fmt.Errorf("virtual candidate URI is outside its source group")
		}
		fileSize := candidate.FileSize
		if fileSize < 0 {
			fileSize = 0
		}
		audioTracks, err := json.Marshal(syntheticAudioTracks(candidate))
		if err != nil {
			return fmt.Errorf("marshal virtual candidate audio tracks: %w", err)
		}
		subtitleTracks, err := json.Marshal(languageSubtitleTracks(candidate.SubtitleLanguages))
		if err != nil {
			return fmt.Errorf("marshal virtual candidate subtitle tracks: %w", err)
		}
		videoTracks, err := json.Marshal(syntheticVideoTracks(candidate))
		if err != nil {
			return fmt.Errorf("marshal virtual candidate video tracks: %w", err)
		}
		audioChannels := virtualChannelsCount(candidate.AudioChannels)
		bitrate := candidate.Bitrate
		if bitrate < 0 {
			bitrate = 0
		}
		// The expiry belongs to the URL it was parsed from. A candidate with
		// no URL must not carry an orphan expiry into the row; the update
		// below clears the stored expiry whenever a new URL replaces the old
		// one, so the pair always describes the same URL.
		var resolvedURLExpiresAt *time.Time
		if strings.TrimSpace(candidate.ResolvedURL) != "" {
			resolvedURLExpiresAt = candidate.ResolvedURLExpiresAt
		}
		// Request headers travel with the URL they authenticate. serializeJSONB
		// returns nil for an empty map, so a header-less candidate stores NULL
		// and the re-list preserves any previously stored set.
		providerRequestHeadersJSON, err := serializeJSONB(candidate.ProviderRequestHeaders)
		if err != nil {
			return fmt.Errorf("marshal virtual candidate request headers: %w", err)
		}
		var id int
		err = tx.QueryRow(ctx, `
			-- The provider display label is display-only metadata: it lives in
			-- edition_raw (the version flyout's fallback). Virtual files have no
			-- filename to parse a release name from, so release_name and
			-- release_group stay empty (the column default) on both insert and
			-- conflict update, matching upsertVirtualFileVariant and the cleanup
			-- migration 20260908140000_cleanup_virtual_release_metadata.sql.
			INSERT INTO media_files (
				content_id, episode_id, media_folder_id, file_path, file_size,
				resolution, codec_video, codec_audio, audio_channels, hdr, container, bitrate,
				edition_raw, release_name, release_group, audio_tracks, subtitle_tracks, video_tracks, probe_source,
				probe_updated_at, virtual_owner_installation_id,
				resolved_url, resolved_url_expires_at,
				provider_video_hash, provider_guid, provider_release_name, provider_release_size,
				provider_request_headers
			) VALUES (
				$1, NULLIF($2,''), $3, $4, $5,
				NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), NULLIF($9,0), $10, 'virtual',
				NULLIF($11,0), $12, '', '', $13, $14, $15, 'virtual', NULL, $16,
				NULLIF($17,''), $18,
				NULLIF($19,''), NULLIF($20,''), NULLIF($21,''), NULLIF($22::bigint,0),
				$23
			)
			ON CONFLICT (file_path, virtual_owner_installation_id, media_folder_id)
				WHERE virtual_owner_installation_id IS NOT NULL
			DO UPDATE SET
				content_id=EXCLUDED.content_id,
				episode_id=EXCLUDED.episode_id,
				media_folder_id=EXCLUDED.media_folder_id,
				file_size=EXCLUDED.file_size,
				resolution = CASE
					WHEN media_files.probe_updated_at IS NOT NULL THEN media_files.resolution
					ELSE EXCLUDED.resolution
				END,
				codec_video = CASE
					WHEN media_files.probe_updated_at IS NOT NULL THEN media_files.codec_video
					ELSE EXCLUDED.codec_video
				END,
				codec_audio = CASE
					WHEN media_files.probe_updated_at IS NOT NULL THEN media_files.codec_audio
					ELSE EXCLUDED.codec_audio
				END,
				audio_channels = CASE
					WHEN media_files.probe_updated_at IS NOT NULL THEN media_files.audio_channels
					ELSE EXCLUDED.audio_channels
				END,
				hdr = CASE
					WHEN media_files.probe_updated_at IS NOT NULL THEN media_files.hdr
					ELSE EXCLUDED.hdr
				END,
				container = CASE
					WHEN media_files.probe_updated_at IS NOT NULL THEN media_files.container
					ELSE 'virtual'
				END,
				bitrate = CASE
					WHEN media_files.probe_updated_at IS NOT NULL THEN media_files.bitrate
					ELSE EXCLUDED.bitrate
				END,
				edition_raw=EXCLUDED.edition_raw,
				release_name='',
				release_group='',
				audio_tracks = CASE
					WHEN media_files.probe_updated_at IS NOT NULL THEN media_files.audio_tracks
					ELSE EXCLUDED.audio_tracks
				END,
				subtitle_tracks = CASE
					WHEN media_files.probe_updated_at IS NOT NULL THEN media_files.subtitle_tracks
					ELSE EXCLUDED.subtitle_tracks
				END,
				video_tracks = CASE
					WHEN media_files.probe_updated_at IS NOT NULL THEN media_files.video_tracks
					ELSE EXCLUDED.video_tracks
				END,
				probe_source='virtual',
				-- A re-list that no longer reports a provider URL must not
				-- erase the last resolved one; only a newer successful
				-- resolution overwrites it, and its expiry is replaced with it
				-- so the pair always describes the same URL.
				resolved_url = CASE
					WHEN EXCLUDED.resolved_url IS NOT NULL THEN EXCLUDED.resolved_url
					ELSE media_files.resolved_url
				END,
				resolved_url_expires_at = CASE
					WHEN EXCLUDED.resolved_url IS NOT NULL THEN EXCLUDED.resolved_url_expires_at
					ELSE media_files.resolved_url_expires_at
				END,
				-- Durable identity is preserved when a re-list omits a tier:
				-- a release that once carried a hash keeps it until a newer
				-- listing supplies a replacement.
				provider_video_hash=COALESCE(EXCLUDED.provider_video_hash, media_files.provider_video_hash),
				provider_guid=COALESCE(EXCLUDED.provider_guid, media_files.provider_guid),
				provider_release_name=COALESCE(EXCLUDED.provider_release_name, media_files.provider_release_name),
				provider_release_size=COALESCE(EXCLUDED.provider_release_size, media_files.provider_release_size),
				-- Request headers belong to the stored URL. A re-list that
				-- refreshes the URL replaces the complete header set, including
				-- clearing it when the new URL carries none: a header-less URL
				-- must not inherit the previous URL's credentials. A re-list
				-- that omits the URL preserves the stored set so a
				-- header-authenticated URL is not orphaned.
				provider_request_headers = CASE
					WHEN EXCLUDED.resolved_url IS NOT NULL THEN EXCLUDED.provider_request_headers
					ELSE COALESCE(EXCLUDED.provider_request_headers, media_files.provider_request_headers)
				END,
				-- Registration is not a probe: preserve any existing real probe
				-- timestamp (NULL stays NULL) so the probe repair gate can fill
				-- real track inventory later.
				probe_updated_at = CASE WHEN media_files.probe_updated_at IS NULL THEN NULL ELSE media_files.probe_updated_at END,
				missing_since=NULL,
				-- A re-list is not a recovery. The provider still offering the
				-- release says nothing about the bytes that failed to arrive or
				-- failed to decode, so the persisted verdict survives the
				-- listing. Clearing is the job of MarkVirtualCandidateRecovered
				-- (real delivery), ClearVirtualCandidateFailed (a liveness
				-- success), or an explicit user retry/forced relink. A genuinely
				-- new row is inserted without a failed_at and is unaffected.
				updated_at=NOW()
			RETURNING id`,
			source.ContentID, source.EpisodeID, source.MediaFolderID, candidate.URI,
			fileSize, candidate.Resolution, candidate.CodecVideo,
			candidate.CodecAudio, audioChannels, candidate.HDR != "", bitrate,
			candidate.Label, audioTracks, subtitleTracks, videoTracks,
			ownerInstallationID,
			candidate.ResolvedURL, resolvedURLExpiresAt,
			candidate.ProviderVideoHash, candidate.ProviderGUID,
			candidate.ProviderReleaseName, candidate.ProviderReleaseSize,
			providerRequestHeadersJSON,
		).Scan(&id)
		if err != nil {
			return fmt.Errorf("upsert virtual candidate: %w", err)
		}
		keep[id] = struct{}{}
	}

	ownerIDs := make([]int64, 0, len(owners))
	for ownerID := range owners {
		ownerIDs = append(ownerIDs, int64(ownerID))
	}
	rows, err := tx.Query(ctx, `
		SELECT id, file_path
		FROM media_files
		WHERE content_id=$1
		  AND COALESCE(episode_id,'')=$2
		  AND media_folder_id=$3
		  AND virtual_owner_installation_id=ANY($4::bigint[])
		  AND (container='virtual' OR file_path LIKE 'virtual://%')`,
		source.ContentID, source.EpisodeID, source.MediaFolderID, ownerIDs)
	if err != nil {
		return fmt.Errorf("list existing virtual candidates: %w", err)
	}
	var stale []int
	for rows.Next() {
		var id int
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return fmt.Errorf("scan existing virtual candidate: %w", err)
		}
		existingGroup, valid := virtualCandidateGroup(path)
		if !valid || existingGroup != group || !virtualCandidateSelection(path) {
			continue
		}
		if _, exists := keep[id]; !exists {
			stale = append(stale, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate existing virtual candidates: %w", err)
	}
	rows.Close()
	if len(stale) > 0 && len(keep) > 0 {
		// Candidate store window. A positive window keeps a listed candidate
		// row around even after the provider's re-list drops it, so a replay of
		// the viewer's saved pick still finds the persisted URL instead of
		// being refused or rotated to a sibling. The window is measured from
		// updated_at (refreshed on every re-list upsert and on a stored-URL
		// refresh), and zero disables it, restoring the sweep below unchanged.
		windowSeconds := 0.0
		if r.virtualCandidateStoreWindow != nil {
			if window := r.virtualCandidateStoreWindow(); window > 0 {
				windowSeconds = window.Seconds()
			}
		}
		// Retention: candidate identity is a provider result id that can churn
		// between listings, but a version a user actually played is recorded as
		// user_watch_progress.last_file_id. Keep stale rows that are still
		// someone's last-played file so a known-working version does not vanish
		// from the version list when the provider re-lists with new result ids.
		// A retained row keeps its failed_at. A failed retained row stays out
		// of the auto-pick (a re-list no longer clears the verdict), and the
		// resolver drops SourceFailed candidates before the version-list sink,
		// so a failed release is neither re-persisted nor offered for manual
		// retry through the version list. Only a real delivery
		// (MarkVirtualCandidateRecovered) or a liveness success
		// (ClearVirtualCandidateFailed) clears the verdict; the next listing
		// then re-adds the release if the provider still offers it.
		//
		// A row that actually delivered media bytes (last_delivered_at set) is
		// retained on the same principle even if no progress row points at it:
		// "once worked" is stronger evidence than "recently listed". These
		// retained rows have no cleanup path here; a future retention TTL may
		// prune them (not implemented).
		if _, err := tx.Exec(ctx, `
			DELETE FROM media_files
			WHERE id = ANY($1::bigint[])
			  AND `+virtualCandidateRetentionSQL("$2"), stale, windowSeconds); err != nil {
			return fmt.Errorf("delete stale virtual candidates: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit virtual candidate replacement: %w", err)
	}
	return nil
}

// virtualCandidateRetentionSQL returns the SQL predicate that protects a stale
// or dead virtual-candidate row from deletion. windowArg names the placeholder
// carrying the candidate store window in seconds. The re-list sweep in
// ReplaceVirtualCandidates and the dead-candidate prune in
// PruneDeadVirtualCandidates share it, so the two retention rules can never
// drift apart. The predicate assumes the row under test is media_files.id.
//
// A row is retained when any of these hold:
//   - it delivered media bytes (last_delivered_at not null): "once worked" is
//     stronger evidence than "recently listed";
//   - it is still inside the candidate store window (a positive window keeps a
//     listed candidate trusted for replay even after a re-list drops it);
//   - it is some user's last-played file (user_watch_progress.last_file_id);
//   - a live playback attempt's effective_media_file_id points at it (the
//     FK is ON DELETE CASCADE, so deleting the row would destroy live session
//     state; the FK's key-share lock serializes this against a new insert);
//   - an open ABS-compatible session references it (ON DELETE SET NULL, so
//     deleting the row would silently strip a live session's file identity).
func virtualCandidateRetentionSQL(windowArg string) string {
	return `last_delivered_at IS NULL
			  -- Keep a candidate inside the candidate store window: it is still
			  -- trusted for replay even though this listing dropped it. Zero
			  -- seconds disables the clause (updated_at is never in the future).
			  AND updated_at < NOW() - make_interval(secs => ` + windowArg + `)
			  AND NOT EXISTS (
				SELECT 1 FROM user_watch_progress p
				WHERE p.last_file_id = media_files.id
			  )
			  -- Never delete a row a live playback attempt points at. The
			  -- attempt's effective_media_file_id has an ON DELETE CASCADE
			  -- foreign key, so deleting the row would destroy the attempt and
			  -- session state out from under an active stream. The sweep runs on
			  -- every version-list fetch, so without this guard a long session is
			  -- unprotected for most of its life (last_delivered_at is only
			  -- written after a direct stream finishes). The FK's key-share lock
			  -- serializes this against a concurrent attempt insert, so the
			  -- check cannot race a new attempt. The live window matches the
			  -- planstore's retention: an expired attempt is already cleanup.
			  AND NOT EXISTS (
				SELECT 1 FROM playback_v3_attempts a
				WHERE a.expires_at > NOW()
				  AND a.effective_media_file_id = media_files.id
			  )
			  -- The same protection for the ABS-compatible session table: an
			  -- open session references its media file with ON DELETE SET NULL,
			  -- so deleting the row silently strips a live session's file
			  -- identity.
			  AND NOT EXISTS (
				SELECT 1 FROM abs_playback_sessions s
				WHERE s.media_file_id = media_files.id
				  AND s.closed_at IS NULL
			  )`
}

// virtualCandidateWindowSeconds returns the configured candidate store window
// in seconds, or zero when the window is disabled (nil reader or non-positive
// value). The retention predicate treats zero as "no window protection".
func (r *FileRepository) virtualCandidateWindowSeconds() float64 {
	if r == nil || r.virtualCandidateStoreWindow == nil {
		return 0
	}
	if window := r.virtualCandidateStoreWindow(); window > 0 {
		return window.Seconds()
	}
	return 0
}

// PruneDeadAbsentVirtualCandidates deletes the named virtual candidate rows
// unless the candidate retention rule protects them. ids are candidate rows the
// refresh has already proven both absent from the fresh listing and dead (a
// failed_at verdict or an AltMount failed verdict); this method re-applies the
// same retention predicate the re-list sweep uses, so a last-played, delivered,
// in-window, or live-session row is never deleted even when dead and absent.
// Passing the caller's evidence rather than re-deriving it keeps the "when in
// doubt, keep" rule in one place: a missed prune is a cosmetic leftover, a
// wrong prune is data loss.
//
// Rows that are not virtual candidates (the provider-neutral source row, a
// local file) are ignored. It returns the number of rows actually deleted.
func (r *FileRepository) PruneDeadAbsentVirtualCandidates(ctx context.Context, ids []int) (int, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("file repository is not configured")
	}
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM media_files
		WHERE id = ANY($1::bigint[])
		  AND (container = 'virtual' OR file_path LIKE 'virtual://%')
		  AND `+virtualCandidateRetentionSQL("$2"), ids, r.virtualCandidateWindowSeconds())
	if err != nil {
		return 0, fmt.Errorf("prune dead virtual candidates: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func virtualCandidateGroup(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "virtual" || parsed.Host == "" {
		return "", false
	}
	query := parsed.Query()
	query.Del("result")
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String(), true
}

// ListVirtualCandidatesNeedingRefresh returns the virtual candidate rows whose
// signed stored URL is approaching expiry and still inside the candidate store
// window, oldest expiry first. It feeds the bounded background refresh pass.
//
// Selection is deliberately narrow:
//   - only rows that carry a signaled expiry (resolved_url_expires_at) are
//     eligible: a NULL expiry is the durable unsigned-provider case and must
//     never be re-fetched;
//   - already-expired rows are excluded (the pass only pre-warms a live URL);
//   - rows are confined to the trust window (updated_at inside it), so a row
//     the server no longer trusts is never touched;
//   - a zero window or lead matches nothing, so the pass is inert when the
//     window is disabled.
//
// limit caps one pass; the caller also bounds its own batch.
//
// Cooldown ordering note: the refresher holds its per-row failure cooldown in
// memory (it is deliberately not durable — a restart clears it and the next
// pass retries), so this query cannot exclude cooling-down rows itself. The
// caller filters them after the cap. When every row in the batch is cooling
// down the pass cheaply skips them; rows beyond the cap wait for the next
// pass. The per-row cooldown (an hour) is shorter than the pass's lead window
// (two hours), so a skipped row becomes eligible again before its URL lapses.
func (r *FileRepository) ListVirtualCandidatesNeedingRefresh(ctx context.Context, window, lead time.Duration, limit int) ([]*models.MediaFile, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("file repository is not configured")
	}
	if window <= 0 || lead <= 0 || limit <= 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+fileColumns+`
		FROM media_files
		WHERE (container = 'virtual' OR file_path LIKE 'virtual://%')
		  AND resolved_url IS NOT NULL
		  AND resolved_url_expires_at IS NOT NULL
		  AND resolved_url_expires_at > NOW()
		  AND resolved_url_expires_at <= NOW() + make_interval(secs => $1)
		  AND updated_at >= NOW() - make_interval(secs => $2)
		ORDER BY resolved_url_expires_at ASC
		LIMIT $3`,
		lead.Seconds(), window.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("list virtual candidates needing refresh: %w", err)
	}
	defer rows.Close()
	return scanMediaFiles(rows)
}

// VirtualProviderIdentity is one virtual candidate's durable provider identity,
// in the same tier order the resolver's dedup key uses. It is the payload the
// identity backfill writes onto a legacy row.
type VirtualProviderIdentity struct {
	VideoHash   string
	GUID        string
	ReleaseName string
	ReleaseSize int64
}

// ListVirtualIdentityBackfillRows returns one page of virtual candidate rows
// that never carried provider identity: both provider_video_hash and
// provider_guid are NULL or empty. Rows are ordered by id and keyset-paginated
// on afterID so a resumed backfill picks up where it stopped.
//
// A row that already carries a hash or a GUID is never returned, so the page
// shrinks as the backfill fills rows in and a resumed pass never reworks a row
// that already has identity. A non-virtual row is likewise never returned.
func (r *FileRepository) ListVirtualIdentityBackfillRows(ctx context.Context, afterID, limit int) ([]*models.MediaFile, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("file repository is not configured")
	}
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+fileColumns+`
		FROM media_files
		WHERE (container = 'virtual' OR file_path LIKE 'virtual://%')
		  AND virtual_owner_installation_id IS NOT NULL
		  AND (provider_video_hash IS NULL OR provider_video_hash = '')
		  AND (provider_guid IS NULL OR provider_guid = '')
		  AND id > $1
		ORDER BY id ASC
		LIMIT $2`,
		afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list virtual identity backfill rows: %w", err)
	}
	defer rows.Close()
	return scanMediaFiles(rows)
}

// CountVirtualIdentityBackfillRows counts the virtual rows still missing
// provider identity. It is the backfill's progress total and its one-shot gate:
// zero means there is nothing left to backfill.
func (r *FileRepository) CountVirtualIdentityBackfillRows(ctx context.Context) (int, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("file repository is not configured")
	}
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM media_files
		WHERE (container = 'virtual' OR file_path LIKE 'virtual://%')
		  AND virtual_owner_installation_id IS NOT NULL
		  AND (provider_video_hash IS NULL OR provider_video_hash = '')
		  AND (provider_guid IS NULL OR provider_guid = '')`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count virtual identity backfill rows: %w", err)
	}
	return count, nil
}

// FillVirtualProviderIdentity writes a confidently matched candidate's durable
// identity onto a legacy virtual row, filling only the tiers the row is
// missing. An existing tier is never overwritten: the write is fenced to rows
// that carry neither a hash nor a GUID, and each column keeps its stored value
// via COALESCE. An identity with no usable tier is a no-op, so the caller can
// never blank an existing value or invent identity from nothing.
//
// expectedFilePath is the listing identity the matcher inspected; a row that
// rotated underneath performs no write, mirroring the other virtual-candidate
// CAS writes. It reports whether a row was actually updated.
func (r *FileRepository) FillVirtualProviderIdentity(ctx context.Context, fileID int, expectedFilePath string, identity VirtualProviderIdentity) (bool, error) {
	if r == nil || r.pool == nil {
		return false, errors.New("file repository is not configured")
	}
	if fileID <= 0 || expectedFilePath == "" {
		return false, nil
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_files
		SET provider_video_hash = COALESCE(provider_video_hash, NULLIF($2, '')),
		    provider_guid = COALESCE(provider_guid, NULLIF($3, '')),
		    provider_release_name = COALESCE(provider_release_name, NULLIF($4, '')),
		    provider_release_size = COALESCE(provider_release_size, NULLIF($5::bigint, 0))
		WHERE id = $1
		  AND file_path = $6
		  AND (provider_video_hash IS NULL OR provider_video_hash = '')
		  AND (provider_guid IS NULL OR provider_guid = '')
		  AND (NULLIF($2, '') IS NOT NULL
		       OR NULLIF($3, '') IS NOT NULL
		       OR NULLIF($4, '') IS NOT NULL)`,
		fileID, identity.VideoHash, identity.GUID, identity.ReleaseName, identity.ReleaseSize, expectedFilePath)
	if err != nil {
		return false, fmt.Errorf("fill virtual provider identity: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// VirtualCandidateDeliveryGrace is how long after a virtual candidate's last
// successful delivery a later failure is forgiven (failed_at is not stamped).
// A release that played recently should not be branded dead because the
// provider flapped today: within the window the row stays selectable and the
// auto-pick keeps preferring it for a re-verify. After the window, an absence
// of fresh delivery evidence means a failure stamps normally. The same window
// guards the transport no-bytes marker.
const VirtualCandidateDeliveryGrace = 7 * 24 * time.Hour

// virtualCandidateFailureGracePredicate is the SQL predicate the transport
// failed_at stamp applies: known-good rows inside the delivery grace are
// skipped, while rows that never delivered (or whose last delivery is stale)
// stamp as before. gracePlaceholder is the 1-based index of the query
// placeholder that carries the grace in seconds, so the one rule can be embedded
// in statements whose placeholders differ. The decode-rejection stamp
// deliberately omits this clause: bytes that cannot be decoded are unplayable
// regardless of delivery evidence. The transport no-bytes marker in NewRouter
// delegates here (MarkVirtualCandidateFailed), so this predicate is the single
// definition of the rule.
func virtualCandidateFailureGracePredicate(gracePlaceholder int) string {
	return fmt.Sprintf(`(last_delivered_at IS NULL OR last_delivered_at < NOW() - make_interval(secs => $%d))`, gracePlaceholder)
}

// MarkVirtualCandidateFailed stamps a virtual candidate row as known-bad after
// a transport produced no bytes (corrupted NZB, dead provider URL). The
// auto-pick skips failed candidates. The verdict survives a provider re-list
// (ReplaceVirtualCandidates no longer clears it): only real delivery
// (MarkVirtualCandidateRecovered), a liveness success
// (ClearVirtualCandidateFailed), or an explicit user retry clears it.
//
// A known-good row (last_delivered_at set) inside VirtualCandidateDeliveryGrace
// is not stamped: the failure is treated as a transient flap and the candidate
// stays eligible for the next auto-pick. This is the "prefer a release that
// once delivered" rule, paired with the retention guard in
// ReplaceVirtualCandidates that never deletes known-good rows.
//
// The write is fenced on the candidate identity the caller actually inspected:
// expectedFilePath must still be the row's file_path and observedFailedAt must
// still be the row's failed_at. A concurrent candidate rotation (the row's
// file_path now points at a replacement candidate) or a newer failure stamp
// therefore makes the write a no-op instead of mis-marking the replacement or
// clearing fresh evidence. This mirrors ReplaceVirtualResultPin's
// `WHERE id=$1 AND file_path=$2` guard.
func (r *FileRepository) MarkVirtualCandidateFailed(ctx context.Context, fileID int, expectedFilePath string, observedFailedAt *time.Time) error {
	_, err := r.MarkVirtualCandidateFailedDetailed(ctx, fileID, expectedFilePath, observedFailedAt)
	return err
}

// MarkVirtualCandidateFailedDetailed stamps a virtual candidate row as
// known-bad, returning whether the update actually committed. A nil error
// from a fenced-out write or a delivery-grace skip returns committed=false.
func (r *FileRepository) MarkVirtualCandidateFailedDetailed(ctx context.Context, fileID int, expectedFilePath string, observedFailedAt *time.Time) (bool, error) {
	return r.markVirtualCandidateFailedDetailed(ctx, fileID, expectedFilePath, observedFailedAt, true)
}

// MarkVirtualCandidateDecodeRejected stamps a virtual candidate row as
// known-bad after a decoder rejected the source, bypassing the delivery grace
// that MarkVirtualCandidateFailed applies. A decode verdict is a statement
// about the bytes themselves — they are unplayable by the executor that just
// tried — so a candidate that delivered recently is still branded dead and the
// auto-pick rotates instead of re-selecting the same undecodable release
// forever. Transport failures keep the grace behavior: a provider flap is
// forgiven for a candidate that demonstrably delivered; an undecodable release
// is not.
//
// The identity/failed_at fence is identical to MarkVirtualCandidateFailed, so a
// rotation or a newer stamp is never mis-marked by a stale verdict.
func (r *FileRepository) MarkVirtualCandidateDecodeRejected(ctx context.Context, fileID int, expectedFilePath string, observedFailedAt *time.Time) error {
	_, err := r.markVirtualCandidateFailedDetailed(ctx, fileID, expectedFilePath, observedFailedAt, false)
	return err
}

// markVirtualCandidateFailedDetailed is the shared failed_at stamp. applyGrace selects
// the transport rule: true (MarkVirtualCandidateFailed) skips known-good rows
// inside VirtualCandidateDeliveryGrace, false (MarkVirtualCandidateDecodeRejected)
// stamps regardless of delivery evidence. The fence and the placeholder order
// are identical either way; only the grace clause is conditional.
func (r *FileRepository) markVirtualCandidateFailedDetailed(ctx context.Context, fileID int, expectedFilePath string, observedFailedAt *time.Time, applyGrace bool) (bool, error) {
	if r == nil || r.pool == nil {
		return false, errors.New("file repository is not configured")
	}
	if fileID <= 0 {
		return false, nil
	}
	query := `UPDATE media_files SET failed_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND file_path = $2 AND failed_at IS NOT DISTINCT FROM $3`
	args := []any{fileID, expectedFilePath, observedFailedAt}
	if applyGrace {
		query += `
		  AND ` + virtualCandidateFailureGracePredicate(4)
		args = append(args, VirtualCandidateDeliveryGrace.Seconds())
	}
	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ClearVirtualCandidateFailed clears the failed_at stamp on a virtual candidate
// row after a liveness check resolved its pinned result successfully, so the
// auto-pick considers it again. No-op when the row is not virtual or has
// vanished.
//
// Like MarkVirtualCandidateFailed, the write is fenced on the candidate
// identity the check inspected: expectedFilePath must still be the row's
// file_path and observedFailedAt must still be the row's failed_at, so a
// concurrent rotation or a newer failure stamp is never cleared by a stale
// success.
func (r *FileRepository) ClearVirtualCandidateFailed(ctx context.Context, fileID int, expectedFilePath string, observedFailedAt *time.Time) error {
	if r == nil || r.pool == nil {
		return errors.New("file repository is not configured")
	}
	if fileID <= 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `UPDATE media_files SET failed_at = NULL, updated_at = NOW() WHERE id = $1 AND file_path = $2 AND failed_at IS NOT DISTINCT FROM $3 AND (container = 'virtual' OR file_path LIKE 'virtual://%')`, fileID, expectedFilePath, observedFailedAt)
	return err
}

// MarkVirtualCandidateRecovered records durable delivery evidence for a
// virtual candidate: it clears the failed_at stamp and sets last_delivered_at
// after that candidate actually delivered media bytes to a client. This is the
// transport-delivery counterpart to ClearVirtualCandidateFailed: the caller
// passes the candidate identity the transport served (the row's file_path AT
// DELIVERY TIME, which a session retains even after the row rotates) and the
// failure timestamp observed when the transport started, so the write only
// lands when the row still describes the delivered candidate in the observed
// health state. A rotated row (now describing candidate B) or a newer failure
// stamp on candidate A is never cleared or re-stamped by a late delivery of A.
//
// The two column writes are deliberately one UPDATE: delivery evidence and the
// failed_at clear happen atomically, so a reader never sees a cleared stamp
// without the corresponding delivery timestamp (or vice versa).
func (r *FileRepository) MarkVirtualCandidateRecovered(ctx context.Context, fileID int, deliveredFilePath string, observedFailedAt *time.Time) error {
	if r == nil || r.pool == nil {
		return errors.New("file repository is not configured")
	}
	if fileID <= 0 || strings.TrimSpace(deliveredFilePath) == "" {
		return nil
	}
	_, err := r.pool.Exec(ctx, `UPDATE media_files SET failed_at = NULL, last_delivered_at = NOW(), updated_at = NOW() WHERE id = $1 AND file_path = $2 AND failed_at IS NOT DISTINCT FROM $3 AND (container = 'virtual' OR file_path LIKE 'virtual://%')`, fileID, deliveredFilePath, observedFailedAt)
	return err
}

// recordVirtualAdoptionRefusalSQL atomically increments the consecutive-refusal
// count for one (file, pinned path). The CASE restarts the count when the
// previous mark has expired, so a refusal stream separated by more than the TTL
// does not accumulate; the ON CONFLICT upsert makes the increment safe under
// concurrent replicas.
const recordVirtualAdoptionRefusalSQL = `
	INSERT INTO virtual_adoption_refusals (file_id, expected_path, refusal_count, expires_at)
	VALUES ($1, $2, 1, now() + ($3 * interval '1 second'))
	ON CONFLICT (file_id, expected_path) DO UPDATE
	SET refusal_count = CASE
	        WHEN virtual_adoption_refusals.expires_at > now()
	        THEN virtual_adoption_refusals.refusal_count + 1
	        ELSE 1
	    END,
	    expires_at = now() + ($3 * interval '1 second')
	RETURNING refusal_count
`

// sweepVirtualAdoptionRefusalsSQL deletes a bounded batch of expired marks so
// the table cannot retain refusal state past its window. It is best-effort and
// intentionally bounded: the sweep runs alongside the rare refusal write, not as
// a table-wide scan.
const sweepVirtualAdoptionRefusalsSQL = `
	DELETE FROM virtual_adoption_refusals
	WHERE ctid IN (
	    SELECT ctid FROM virtual_adoption_refusals
	    WHERE expires_at <= now()
	    LIMIT $1
	)
`

// virtualAdoptionRefusalSweepBatch caps how many expired marks one refusal write
// reclaims.
const virtualAdoptionRefusalSweepBatch = 512

// RecordVirtualAdoptionRefusal atomically increments the durable
// consecutive-refusal count for one virtual row and pinned path and returns the
// new count. It is the cross-replica trigger that reaches the bounded-refusal
// decision: a restarted process or a different replica continues the same count
// instead of restarting at zero, so the durable terminal decision is reached
// regardless of which replica served the earlier refusals. An entry older than
// ttl is treated as absent and restarts at one, so a provider that recovers is
// not permanently treated as stale.
//
// It returns the current database error unchanged; callers must treat a store
// error as a recoverable infrastructure failure, never as a refusal.
func (r *FileRepository) RecordVirtualAdoptionRefusal(ctx context.Context, fileID int, expectedPath string, ttl time.Duration) (int, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("file repository is not configured")
	}
	if fileID <= 0 || strings.TrimSpace(expectedPath) == "" || ttl <= 0 {
		return 0, nil
	}
	var count int
	if err := r.pool.QueryRow(ctx, recordVirtualAdoptionRefusalSQL, fileID, expectedPath, ttl.Seconds()).Scan(&count); err != nil {
		return 0, err
	}
	// Bounded, best-effort reclamation of expired marks; a failure here changes
	// nothing about the count just recorded.
	_, _ = r.pool.Exec(ctx, sweepVirtualAdoptionRefusalsSQL, virtualAdoptionRefusalSweepBatch)
	return count, nil
}

// ClearVirtualAdoptionRefusal drops the durable refusal mark for one row and
// pinned path after a write landed, so the bound resets on success. A missing
// row is not an error.
func (r *FileRepository) ClearVirtualAdoptionRefusal(ctx context.Context, fileID int, expectedPath string) error {
	if r == nil || r.pool == nil {
		return errors.New("file repository is not configured")
	}
	if fileID <= 0 || strings.TrimSpace(expectedPath) == "" {
		return nil
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM virtual_adoption_refusals WHERE file_id = $1 AND expected_path = $2`, fileID, expectedPath)
	return err
}

// CountVirtualAdoptionRefusal returns the live durable consecutive-refusal count
// for one row and pinned path, or zero when no live mark exists. It never
// increments, so a caller can decide whether the bound was reached without
// itself advancing the count.
func (r *FileRepository) CountVirtualAdoptionRefusal(ctx context.Context, fileID int, expectedPath string) (int, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("file repository is not configured")
	}
	if fileID <= 0 || strings.TrimSpace(expectedPath) == "" {
		return 0, nil
	}
	var count int
	err := r.pool.QueryRow(ctx, `SELECT refusal_count FROM virtual_adoption_refusals WHERE file_id = $1 AND expected_path = $2 AND expires_at > now()`, fileID, expectedPath).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return count, nil
}

// RecordVirtualAdoptionRefusalTx is RecordVirtualAdoptionRefusal bound to the
// caller's transaction, so the increment commits atomically with the evidence
// write or terminal decision it belongs to. The atomic upsert and TTL semantics
// are identical to the pool variant; the expired-row sweep is deliberately not
// run here (it would acquire a second pooled connection while the transaction is
// open). The caller runs SweepVirtualAdoptionRefusals best-effort after commit.
func (r *FileRepository) RecordVirtualAdoptionRefusalTx(ctx context.Context, tx pgx.Tx, fileID int, expectedPath string, ttl time.Duration) (int, error) {
	if r == nil || tx == nil {
		return 0, errors.New("file repository is not configured")
	}
	if fileID <= 0 || strings.TrimSpace(expectedPath) == "" || ttl <= 0 {
		return 0, nil
	}
	var count int
	if err := tx.QueryRow(ctx, recordVirtualAdoptionRefusalSQL, fileID, expectedPath, ttl.Seconds()).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// lockVirtualAdoptionRowSQL takes the media_files row lock the locked refusal
// recorder serializes on. It is the same lock the transactional evidence saver
// takes first, so every replica reconciles one row in the same order
// (media_files before virtual_adoption_refusals) and cannot deadlock.
const lockVirtualAdoptionRowSQL = `SELECT id FROM media_files WHERE id = $1 FOR UPDATE`

// RecordVirtualAdoptionRefusalLocked records one consecutive refusal for a
// virtual row and pinned path under the media_files row lock, so a refusal
// increment taken outside the evidence transaction (the same-release sibling
// rotation trigger) serializes on the same row-first order as the evidence write
// and its clear. It reports the live count read under that lock, so a bound
// decision cannot rest on a count a concurrent evidence write or clear has
// already invalidated. A missing row is a no-op: there is nothing to count
// against, and a refusal row for a vanished file would only fail its foreign
// key. The expired-row sweep runs best-effort after commit.
func (r *FileRepository) RecordVirtualAdoptionRefusalLocked(ctx context.Context, fileID int, expectedPath string, ttl time.Duration) (int, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("file repository is not configured")
	}
	if fileID <= 0 || strings.TrimSpace(expectedPath) == "" || ttl <= 0 {
		return 0, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin adoption refusal transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	var lockedID int
	lockErr := tx.QueryRow(ctx, lockVirtualAdoptionRowSQL, fileID).Scan(&lockedID)
	if errors.Is(lockErr, pgx.ErrNoRows) {
		return 0, nil
	}
	if lockErr != nil {
		return 0, fmt.Errorf("lock virtual row for adoption refusal: %w", lockErr)
	}

	count, err := r.RecordVirtualAdoptionRefusalTx(ctx, tx, fileID, expectedPath, ttl)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit adoption refusal transaction: %w", err)
	}
	committed = true
	_, _ = r.pool.Exec(ctx, sweepVirtualAdoptionRefusalsSQL, virtualAdoptionRefusalSweepBatch)
	return count, nil
}

// ClearVirtualAdoptionRefusalTx is ClearVirtualAdoptionRefusal bound to the
// caller's transaction, so a successful evidence write and the exact-pin clear
// commit together. A cancel or database interruption can therefore never leave a
// cleared write behind a still-live threshold, or a live threshold behind a
// committed write.
func (r *FileRepository) ClearVirtualAdoptionRefusalTx(ctx context.Context, tx pgx.Tx, fileID int, expectedPath string) error {
	if r == nil || tx == nil {
		return errors.New("file repository is not configured")
	}
	if fileID <= 0 || strings.TrimSpace(expectedPath) == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `DELETE FROM virtual_adoption_refusals WHERE file_id = $1 AND expected_path = $2`, fileID, expectedPath)
	return err
}

// SweepVirtualAdoptionRefusals runs the bounded, best-effort reclamation of
// expired refusal marks. The transactional record path hands the sweep to the
// caller's post-commit step so the sweep never holds a second connection inside
// the transaction; a failure changes nothing about any recorded count.
func (r *FileRepository) SweepVirtualAdoptionRefusals(ctx context.Context) error {
	if r == nil || r.pool == nil {
		return errors.New("file repository is not configured")
	}
	_, err := r.pool.Exec(ctx, sweepVirtualAdoptionRefusalsSQL, virtualAdoptionRefusalSweepBatch)
	return err
}

// virtualAdoptionTerminalRefusalSQL stamps the durable terminal refusal for a
// row whose pinned adoption the bounded decision has exhausted. It is fenced on
// the candidate identity AND the evidence revision the decision observed: the
// row is only stamped while it still describes the exact candidate and still
// carries the updated_at/probe_updated_at the decision was taken from. A newer
// evidence write that advanced the row therefore wins over a stale terminal
// instead of being condemned by it. The transport delivery grace is applied like
// MarkVirtualCandidateFailed, so a release that recently delivered is not
// terminally fenced by refusals alone.
var virtualAdoptionTerminalRefusalSQL = `
	UPDATE media_files SET failed_at = NOW(), updated_at = NOW()
	WHERE id = $1
	  AND file_path = $2
	  AND failed_at IS NOT DISTINCT FROM $3
	  AND updated_at = $4
	  AND probe_updated_at IS NOT DISTINCT FROM $5
	  AND (container = 'virtual' OR file_path LIKE 'virtual://%')
	  AND ` + virtualCandidateFailureGracePredicate(6) + `
	RETURNING id
`

// markVirtualAdoptionTerminalRefusal is the shared terminal stamp behind the
// pool and transaction entry points. q is the pool or the caller's transaction;
// it reports whether a marker was installed. A stale revision (a newer evidence
// write committed since the decision) makes the stamp a no-op rather than a
// stale terminal.
func (r *FileRepository) markVirtualAdoptionTerminalRefusal(ctx context.Context, q fileQueryer, fileID int, expectedFilePath string, observedFailedAt *time.Time, observedUpdatedAt time.Time, observedProbeUpdatedAt *time.Time) (bool, error) {
	if r == nil || q == nil {
		return false, errors.New("file repository is not configured")
	}
	if fileID <= 0 || strings.TrimSpace(expectedFilePath) == "" {
		return false, nil
	}
	var id int
	err := q.QueryRow(ctx, virtualAdoptionTerminalRefusalSQL,
		fileID, expectedFilePath, observedFailedAt, observedUpdatedAt, observedProbeUpdatedAt,
		VirtualCandidateDeliveryGrace.Seconds()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MarkVirtualAdoptionTerminalRefusal installs the revision-fenced terminal
// refusal outside a transaction. It is the non-transactional fast path a caller
// uses when it can pre-empt the write (the args builder); the authoritative
// decision runs inside the evidence transaction.
func (r *FileRepository) MarkVirtualAdoptionTerminalRefusal(ctx context.Context, fileID int, expectedFilePath string, observedFailedAt *time.Time, observedUpdatedAt time.Time, observedProbeUpdatedAt *time.Time) (bool, error) {
	return r.markVirtualAdoptionTerminalRefusal(ctx, r.pool, fileID, expectedFilePath, observedFailedAt, observedUpdatedAt, observedProbeUpdatedAt)
}

// MarkVirtualAdoptionTerminalRefusalTx is MarkVirtualAdoptionTerminalRefusal
// bound to the caller's transaction, so the terminal verdict commits with the
// refusal count that reached the bound and is serialized on the same row lock as
// the evidence write.
func (r *FileRepository) MarkVirtualAdoptionTerminalRefusalTx(ctx context.Context, tx pgx.Tx, fileID int, expectedFilePath string, observedFailedAt *time.Time, observedUpdatedAt time.Time, observedProbeUpdatedAt *time.Time) (bool, error) {
	return r.markVirtualAdoptionTerminalRefusal(ctx, tx, fileID, expectedFilePath, observedFailedAt, observedUpdatedAt, observedProbeUpdatedAt)
}

func virtualCandidateSelection(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return strings.TrimSpace(parsed.Query().Get("result")) != ""
}

const (
	virtualResolution2160p = "2160p"
	videoRangeHDR          = "HDR"
)

func virtualResolutionDimensions(resolution string) (int, int) {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "4320p", "8k":
		return 7680, 4320
	case virtualResolution2160p, "4k", "uhd":
		return 3840, 2160
	case "1080p", "1080i":
		return 1920, 1080
	case "720p":
		return 1280, 720
	case "480p", "sd":
		return 720, 480
	default:
		return 0, 0
	}
}

func virtualChannelsCount(ch string) int {
	ch = strings.TrimSpace(ch)
	switch {
	case strings.HasPrefix(ch, "7.1"):
		return 8
	case strings.HasPrefix(ch, "5.1"):
		return 6
	case strings.HasPrefix(ch, "2.0") || ch == "2":
		return 2
	case strings.HasPrefix(ch, "1.0") || ch == "1":
		return 1
	default:
		return 0
	}
}

func extractDeclaredDVProfile(label string) int {
	lower := strings.ToLower(label)
	// "dv"/"dovi" followed by digit: "dv8" -> 8, "dovi7" -> 7. A trailing
	// digit boundary prevents "dv80" from matching profile 8.
	for _, p := range []struct {
		prefix  string
		profile int
	}{
		{"dv8", 8},
		{"dv7", 7},
		{"dv5", 5},
		{"dovi8", 8},
		{"dovi7", 7},
		{"dovi5", 5},
	} {
		if idx := strings.Index(lower, p.prefix); idx >= 0 {
			next := lower[idx+len(p.prefix):]
			if next == "" || next[0] < '0' || next[0] > '9' {
				return p.profile
			}
		}
	}
	// "profile 8"/"profile.8" with a digit boundary: "profile 80" must not
	// match profile 8 (a "." separator like "Profile.8.HDR10" is fine).
	for _, p := range []struct {
		prefix  string
		profile int
	}{
		{"profile 8", 8},
		{"profile.8", 8},
		{"profile 7", 7},
		{"profile.7", 7},
		{"profile 5", 5},
		{"profile.5", 5},
	} {
		if idx := strings.Index(lower, p.prefix); idx >= 0 {
			next := lower[idx+len(p.prefix):]
			if next == "" || !isASCIIDigitVirtual(next[0]) {
				return p.profile
			}
		}
	}
	return 0
}

func isASCIIDigitVirtual(b byte) bool { return b >= '0' && b <= '9' }

func syntheticVideoTracks(candidate VirtualCandidate) []models.VideoTrack {
	if candidate.Resolution == "" && candidate.CodecVideo == "" && candidate.HDR == "" && len(candidate.VisualTags) == 0 {
		return []models.VideoTrack{}
	}

	videoCodec := strings.TrimSpace(candidate.CodecVideo)
	width, height := virtualResolutionDimensions(candidate.Resolution)
	bitDepth := 0
	if candidate.Is10Bit {
		bitDepth = 10
	}

	hasDV := false
	hasHDR10Plus := false
	hasHDR10 := false
	hasHLG := false
	hasGenericHDR := false

	for _, tag := range candidate.VisualTags {
		switch strings.ToLower(tag) {
		case "dv":
			hasDV = true
		case "hdr10+":
			hasHDR10Plus = true
		case "hdr10":
			hasHDR10 = true
		case "hlg":
			hasHLG = true
		case "hdr":
			hasGenericHDR = true
		}
	}
	if !hasDV && strings.EqualFold(candidate.HDR, "dv") {
		hasDV = true
	}
	if !hasHDR10Plus && strings.EqualFold(candidate.HDR, "hdr10+") {
		hasHDR10Plus = true
	}
	if !hasHDR10 && strings.EqualFold(candidate.HDR, "hdr10") {
		hasHDR10 = true
	}
	if !hasHLG && strings.EqualFold(candidate.HDR, "hlg") {
		hasHLG = true
	}
	if !hasGenericHDR && strings.EqualFold(candidate.HDR, "hdr") {
		hasGenericHDR = true
	}
	// A Dolby Vision release tag cannot be true of a codec whose bitstream has
	// no way to carry a DV RPU (see playback.CodecCarriesDolbyVisionV3). An
	// "H.264 DV" listing is a mislabel; writing DV onto it would make the
	// planner tone-map an SDR file instead of serving it directly. An unknown
	// codec keeps the declaration.
	if hasDV && videoCodec != "" && !playback.CodecCarriesDolbyVisionV3(videoCodec) {
		hasDV = false
	}

	videoRange := "SDR"
	videoRangeType := "SDR"
	dvProfile := 0
	dolbyVision := ""

	if hasDV {
		videoRange = "DolbyVision"
		dolbyVision = "Dolby Vision"
		dvProfile = extractDeclaredDVProfile(candidate.Label)
		if hasHDR10Plus {
			videoRangeType = "DOVIWithHDR10Plus"
		} else if hasHDR10 {
			videoRangeType = "DOVIWithHDR10"
		} else if hasHLG {
			videoRangeType = "DOVIWithHLG"
		} else {
			videoRangeType = "DOVI"
		}
	} else if hasHDR10Plus {
		videoRange = videoRangeHDR
		videoRangeType = "HDR10Plus"
	} else if hasHDR10 {
		videoRange = videoRangeHDR
		videoRangeType = "HDR10"
	} else if hasHLG {
		videoRange = videoRangeHDR
		videoRangeType = "HLG"
	} else if hasGenericHDR {
		videoRange = videoRangeHDR
		videoRangeType = videoRangeHDR
	}

	bitrate := candidate.Bitrate
	if bitrate <= 0 {
		bitrate = 0
	}
	frameRate := ""

	vt := models.VideoTrack{
		Codec:          videoCodec,
		Width:          width,
		Height:         height,
		BitDepth:       bitDepth,
		FrameRate:      frameRate,
		Bitrate:        bitrate,
		VideoRange:     videoRange,
		VideoRangeType: videoRangeType,
		DVProfile:      dvProfile,
		DolbyVision:    dolbyVision,
		HDR10Plus:      hasHDR10Plus,
	}
	return []models.VideoTrack{vt}
}

func syntheticAudioTracks(candidate VirtualCandidate) []models.AudioTrack {
	codec := strings.TrimSpace(candidate.CodecAudio)
	channels := virtualChannelsCount(candidate.AudioChannels)

	languages := candidate.AudioLanguages
	if len(languages) == 0 {
		if codec != "" || candidate.Resolution != "" {
			t := models.AudioTrack{
				Index:    -1,
				Codec:    codec,
				Channels: channels,
				Default:  true,
			}
			if candidate.HasAtmos {
				t.Title = "Dolby Atmos"
			}
			return []models.AudioTrack{t}
		}
		return []models.AudioTrack{}
	}

	result := make([]models.AudioTrack, 0, len(languages))
	for i, lang := range languages {
		lang = strings.TrimSpace(lang)
		if lang == "" {
			continue
		}
		t := models.AudioTrack{
			Index:    -1,
			Language: lang,
			Codec:    codec,
			Channels: channels,
			Default:  i == 0,
		}
		if candidate.IsMultiAudio {
			t.Languages = languages
		}
		if candidate.HasAtmos {
			t.Title = "Dolby Atmos"
		}
		result = append(result, t)
	}
	return result
}

func languageAudioTracks(languages []string) []models.AudioTrack {
	result := make([]models.AudioTrack, 0, len(languages))
	for _, language := range languages {
		if language = strings.TrimSpace(language); language != "" {
			result = append(result, models.AudioTrack{Language: language})
		}
	}
	return result
}

func languageSubtitleTracks(languages []string) []models.SubtitleTrack {
	// Collapse language aliases onto one entry per base language so a re-list
	// cannot persist two placeholder rows for the same language (for example
	// "EN-US" and "ENG" from a release name).
	deduped := stream.DedupeLanguageAliases(languages)
	result := make([]models.SubtitleTrack, 0, len(deduped))
	for _, language := range deduped {
		result = append(result, models.SubtitleTrack{Language: language})
	}
	return result
}

// identityColumnDefaults normalizes the identity/grouping zero values the way
// every media_files write must persist them. Upsert and UpdateIdentity both go
// through it so the full and metadata-only scan paths converge on identical
// stored values.
func identityColumnDefaults(mf models.MediaFile) (groupKeyVersion int, identityConfidence string, identityJSON []byte) {
	groupKeyVersion = mf.GroupKeyVersion
	if groupKeyVersion == 0 {
		groupKeyVersion = 1
	}
	identityConfidence = mf.IdentityConfidence
	if identityConfidence == "" {
		identityConfidence = "low"
	}
	identityJSON = mf.IdentityJSON
	if len(identityJSON) == 0 {
		identityJSON = []byte("{}")
	}
	return groupKeyVersion, identityConfidence, identityJSON
}

// UpdateIdentity rewrites only the derived root/group/identity and
// edition/presentation columns of an existing media_files row, returning the
// row id. Probe data, file bytes/mtime/hash, subtitles, chapters, markers, and
// content/episode/extra linkage are left untouched. It backs the scanner's
// metadata-only update path: an identity or content-group-key reclassification
// must persist the new grouping without re-running (or disturbing) ffprobe.
// Column handling mirrors Upsert's ON CONFLICT assignments for the same
// columns so the two paths converge on identical values; like any scan write,
// it clears match suppression so the fresh identity re-enters the match
// backlog. Only the id is returned — this runs once per file during
// library-wide grouping migrations, and returning the full row would drag the
// track/chapter JSONB payloads along for millions of rows. Returns
// ErrFileNotFound when the row no longer exists.
func (r *FileRepository) UpdateIdentity(ctx context.Context, mf models.MediaFile) (int, error) {
	return r.updateIdentity(ctx, mf, nil)
}

// UpdateIdentityAndExternalSubtitles applies sidecar and identity changes
// without replacing probe-derived columns after a failed repair attempt.
func (r *FileRepository) UpdateIdentityAndExternalSubtitles(ctx context.Context, mf models.MediaFile) (int, error) {
	externalSubtitlesJSON, err := serializeJSONB(mf.ExternalSubtitles)
	if err != nil {
		return 0, fmt.Errorf("marshaling external_subtitles: %w", err)
	}
	return r.updateIdentity(ctx, mf, externalSubtitlesJSON)
}

func (r *FileRepository) updateIdentity(ctx context.Context, mf models.MediaFile, externalSubtitlesJSON []byte) (int, error) {
	query := `UPDATE media_files SET ` + identityAssignments + `,
		match_suppressed_at = NULL,
		updated_at = NOW()
	WHERE file_path = $1
	RETURNING id`

	var id int
	err := r.pool.QueryRow(ctx, query, identityArgs(mf.FilePath, mf, externalSubtitlesJSON)...).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrFileNotFound
		}
		return 0, fmt.Errorf("updating media file identity: %w", err)
	}
	return id, nil
}

// identityAssignments sets the path-derived identity columns, and the
// sidecar list when one is given, from arguments $2 to $24 of identityArgs.
// A path that names no season or episode keeps the stored numbers, which a
// match may have filled in.
const identityAssignments = identityGroupColumns + `
		season_number = COALESCE($12, season_number),
		episode_number = COALESCE($13, episode_number),
		` + identityVariantColumns

// identityGroupColumns and identityVariantColumns are the assignments of
// identityAssignments around the season and episode numbers.
const identityGroupColumns = `media_folder_id = $2,
		canonical_root_path = $3,
		observed_root_path = $4,
		content_group_key = $5,
		group_key_version = $6,
		base_title = $7,
		base_year = $8,
		base_type = $9,
		identity_confidence = $10,
		identity_json = $11,`

const identityVariantColumns = `edition_raw = $14,
		edition_key = $15,
		edition_confidence = $16,
		edition_source = $17,
		presentation_kind = $18,
		presentation_group_key = $19,
		presentation_part_index = $20,
		presentation_part_total = $21,
		multi_episode_start = $22,
		multi_episode_end = $23,
		external_subtitles = COALESCE($24, external_subtitles)`

// identityArgs returns the arguments for identityAssignments, with key, the
// row the caller's WHERE clause selects, as $1.
func identityArgs(key any, mf models.MediaFile, externalSubtitlesJSON []byte) []any {
	groupKeyVersion, identityConfidence, identityJSON := identityColumnDefaults(mf)
	return []any{
		key,
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
		nilIfZero(mf.SeasonNumber),
		nilIfZero(mf.EpisodeNumber),
		mf.EditionRaw,
		mf.EditionKey,
		mf.EditionConfidence,
		mf.EditionSource,
		mf.PresentationKind,
		mf.PresentationGroupKey,
		nilIfZero(mf.PresentationPartIndex),
		nilIfZero(mf.PresentationPartTotal),
		nilIfZero(mf.MultiEpisodeStart),
		nilIfZero(mf.MultiEpisodeEnd),
		externalSubtitlesJSON,
	}
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

	// Commit through the session holding the chapter lock. If that session
	// dies during extraction, its former owner cannot save after takeover.
	row := r.chapterStateWriter(ctx, fileID).QueryRow(ctx, `
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
	tag, err := r.chapterStateWriter(ctx, fileID).Exec(ctx, `
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

// MarkProbeFailed records that ffprobe rejected a file whose row is otherwise
// left untouched. Only rows with no successful probe are marked, so a probe
// that succeeded concurrently, or valid metadata from an earlier probe, is never
// overridden, and a row already marked is not rewritten. The next successful
// probe clears the mark through Upsert.
//
// probedSize and probedMtime describe the bytes ffprobe rejected, and the row
// is marked only while it still carries them. A scan that replaced the file
// and wrote the new revision without a probe result before this update landed
// would otherwise have the old rejection blamed on the replacement. The mtime
// comparison is normalized to microseconds, as in UpdateMultiplePPS.
func (r *FileRepository) MarkProbeFailed(ctx context.Context, fileID int, probedSize int64, probedMtime *time.Time) error {
	var normalizedMtime *time.Time
	if probedMtime != nil {
		normalized := models.NormalizeFileModifiedAt(*probedMtime)
		normalizedMtime = &normalized
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE media_files
		SET probe_failed_at = NOW(),
		    updated_at = NOW()
		WHERE id = $1
		  AND probe_updated_at IS NULL
		  AND probe_failed_at IS NULL
		  AND file_size = $2
		  AND date_trunc('microseconds', file_modified_at) IS NOT DISTINCT FROM $3::timestamptz`,
		fileID,
		probedSize,
		normalizedMtime,
	); err != nil {
		return fmt.Errorf("recording probe failure: %w", err)
	}
	return nil
}

// segmentState tracks the mutable per-segment fields used by UpsertMarkers.
// Each segment kind (intro, credits, recap, preview) has an independent state
// that the apply step mutates if the priority check allows the write.
type segmentState struct {
	ranges     []models.MarkerSegment
	start      *float64
	end        *float64
	source     *string
	provider   *string
	confidence *float64
	algorithm  *string
	detectedAt *time.Time
}

func applySegmentRanges(
	state *segmentState, legacySharedSource *string, source string, provider *string,
	confidence *float64, algorithm string, patchStart, patchEnd *float64,
	ranges []models.MarkerSegment, duration float64, segmentName string, mutationAt time.Time,
) (bool, error) {
	if len(ranges) == 0 {
		if patchStart == nil && patchEnd == nil {
			return false, nil
		}
		start, end := state.start, state.end
		if patchStart != nil {
			start = patchStart
		}
		if patchEnd != nil {
			end = patchEnd
		}
		if start == nil || end == nil {
			return false, nil
		}
		ranges = []models.MarkerSegment{{Kind: segmentName, StartSeconds: *start, EndSeconds: *end}}
	} else {
		ranges = slices.Clone(ranges)
	}
	for _, segment := range ranges {
		if !segment.Valid() || segment.Kind != segmentName {
			return false, fmt.Errorf("invalid %s marker range %.3f-%.3f", segmentName, segment.StartSeconds, segment.EndSeconds)
		}
		if duration > 0 && segment.EndSeconds > duration+1 {
			return false, fmt.Errorf("%s marker end %.3f exceeds duration %.3f", segmentName, segment.EndSeconds, duration)
		}
	}
	sort.SliceStable(ranges, func(i, j int) bool { return ranges[i].StartSeconds < ranges[j].StartSeconds })
	incoming := markers.SegmentPayload{Start: &ranges[0].StartSeconds, End: &ranges[0].EndSeconds, Ranges: ranges,
		Source: source, Provider: provider, Confidence: confidence, Algorithm: algorithm}
	if !markers.CanWriteMarkerUpdate(existingSegmentPayload(*state, legacySharedSource), incoming) {
		return false, nil
	}
	next := segmentState{start: incoming.Start, end: incoming.End, ranges: ranges, source: &source,
		provider: provider, confidence: confidence, algorithm: &algorithm, detectedAt: &mutationAt}
	current := *state
	if len(current.ranges) == 0 && current.start != nil && current.end != nil {
		current.ranges = []models.MarkerSegment{{Kind: segmentName, StartSeconds: *current.start, EndSeconds: *current.end}}
	}
	if segmentEqual(current, next) {
		return false, nil
	}
	*state = next
	return true, nil
}

// applyAbsentSegment writes a provider's absent answer as a tombstone, its
// provenance without bounds, when source precedence lets it replace state.
func applyAbsentSegment(
	state *segmentState, legacySharedSource *string, source string, provider *string,
	confidence *float64, algorithm string, mutationAt time.Time,
) bool {
	incoming := markers.SegmentPayload{Source: source, Provider: provider, Confidence: confidence, Algorithm: algorithm}
	if !markers.CanWriteMarkerUpdate(existingSegmentPayload(*state, legacySharedSource), incoming) {
		return false
	}
	next := segmentState{source: &source, provider: provider, confidence: confidence, algorithm: &algorithm, detectedAt: &mutationAt}
	if segmentEqual(*state, next) {
		return false
	}
	*state = next
	return true
}

// existingSegmentPayload returns state for precedence checks. A segment written
// before per-segment provenance carries only the file's shared source.
func existingSegmentPayload(state segmentState, legacySharedSource *string) markers.SegmentPayload {
	effectiveSource := state.source
	if effectiveSource == nil && state.start != nil && state.end != nil {
		effectiveSource = legacySharedSource
	}
	existing := markers.SegmentPayload{Start: state.start, End: state.end, Ranges: state.ranges, Provider: state.provider, Confidence: state.confidence}
	if effectiveSource != nil {
		existing.Source = *effectiveSource
	}
	if state.algorithm != nil {
		existing.Algorithm = *state.algorithm
	}
	return existing
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
	return slices.Equal(a.ranges, b.ranges) && ptrFloatEqual(a.start, b.start) &&
		ptrFloatEqual(a.end, b.end) &&
		ptrStringEqual(a.source, b.source) &&
		ptrStringEqual(a.provider, b.provider) &&
		ptrFloatEqual(a.confidence, b.confidence) &&
		ptrStringEqual(a.algorithm, b.algorithm)
}

// UpsertMarkers updates only marker fields while enforcing source priority.
func (r *FileRepository) UpsertMarkers(ctx context.Context, fileID int, update MarkerUpdate) (bool, error) {
	if update.MarkersSource == "" && len(update.RefreshedProviders) == 0 {
		return false, fmt.Errorf("marker source is required")
	}
	return r.UpsertAndClearMarkers(ctx, fileID, update, nil)
}

// WithdrawScannerMarker clears a file's intro or credits segment while it
// still holds the scanner result algorithm wrote, and reports whether it
// cleared it. Local analysis uses it to take back a result its current rules
// no longer produce. A marker another source or detector has written since
// stays. expected, when set, guards the file identity like
// MarkerUpdate.ExpectedFile.
func (r *FileRepository) WithdrawScannerMarker(ctx context.Context, fileID int, segment, algorithm string, expected *models.MediaFile) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin marker withdrawal transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	state, err := loadMarkerMutationState(ctx, tx, fileID)
	if err != nil {
		return false, err
	}
	if expected != nil && models.MarkerFileIdentity(expected) != models.MarkerFileIdentity(&state.file) {
		return false, ErrStaleMarkerUpdate
	}
	flags, err := markerClearFlags([]string{segment})
	if err != nil {
		return false, err
	}
	var target *segmentState
	switch {
	case flags.intro:
		target = &state.intro
	case flags.credits:
		target = &state.credits
	default:
		return false, fmt.Errorf("marker segment %q cannot be withdrawn", segment)
	}
	// A segment written before per-segment provenance carries only the
	// file's shared source.
	source := target.source
	if source == nil || strings.TrimSpace(*source) == "" {
		source = state.existingSource
	}
	if source == nil || strings.TrimSpace(*source) != models.MarkerSourceScanner ||
		target.algorithm == nil || *target.algorithm != algorithm || !clearSegmentState(target) {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit marker withdrawal transaction: %w", err)
		}
		return false, nil
	}
	wrote, err := writeMarkerMutationState(ctx, tx, fileID, state)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit marker withdrawal transaction: %w", err)
	}
	return wrote, nil
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
	file               models.MediaFile
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
	if hasUpdate && update.MarkersSource == "" && len(update.RefreshedProviders) == 0 {
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
	defer func() { _ = tx.Rollback(ctx) }()

	state, err := loadMarkerMutationState(ctx, tx, fileID)
	if err != nil {
		return false, err
	}
	if update != nil && update.ExpectedFile != nil && models.MarkerFileIdentity(update.ExpectedFile) != models.MarkerFileIdentity(&state.file) {
		return false, ErrStaleMarkerUpdate
	}
	before := state
	mutationAt := time.Now().UTC()
	if update != nil && !update.DetectedAt.IsZero() {
		mutationAt = update.DetectedAt.UTC()
	}
	var applyUpdate *MarkerUpdate
	if hasUpdate {
		applyUpdate = update
	}
	changed, err := applyMarkerMutation(applyUpdate, clearFlags, &state, mutationAt)
	if err != nil {
		return false, err
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
	var ranges []models.MarkerSegment
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
		        preview_markers_detected_at,
                marker_segments, id, COALESCE(file_hash, ''), COALESCE(file_size, 0), file_modified_at,
                COALESCE(content_id, ''), COALESCE(episode_id, ''), COALESCE(extra_id, ''),
                COALESCE(season_number, 0), COALESCE(episode_number, 0)
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
		&ranges, &state.file.ID, &state.file.FileHash, &state.file.FileSize, &state.file.FileModifiedAt,
		&state.file.ContentID, &state.file.EpisodeID, &state.file.ExtraID, &state.file.SeasonNumber, &state.file.EpisodeNumber,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return markerMutationState{}, ErrFileNotFound
		}
		return markerMutationState{}, fmt.Errorf("load existing marker source: %w", err)
	}
	state.file.Duration = int(state.duration)
	for _, segment := range models.EffectiveMarkerSegments(&models.MediaFile{
		MarkerSegments: ranges, IntroStart: state.intro.start, IntroEnd: state.intro.end,
		CreditsStart: state.credits.start, CreditsEnd: state.credits.end,
		RecapStart: state.recap.start, RecapEnd: state.recap.end,
		PreviewStart: state.preview.start, PreviewEnd: state.preview.end,
	}) {
		switch segment.Kind {
		case "intro":
			state.intro.ranges = append(state.intro.ranges, segment)
		case "credits":
			state.credits.ranges = append(state.credits.ranges, segment)
		case "recap":
			state.recap.ranges = append(state.recap.ranges, segment)
		case "preview":
			state.preview.ranges = append(state.preview.ranges, segment)
		}
	}
	return state, nil
}

func applyMarkerUpdateToMutationState(update *MarkerUpdate, state *markerMutationState, mutationAt time.Time) (markerSegmentFlags, error) {
	var applied markerSegmentFlags
	ranges := make(map[string][]models.MarkerSegment, 4)
	for _, segment := range update.Segments {
		if !segment.Valid() {
			return applied, fmt.Errorf("invalid marker segment %q", segment.Kind)
		}
		ranges[segment.Kind] = append(ranges[segment.Kind], segment)
	}
	for _, patch := range []struct {
		kind       string
		state      *segmentState
		start, end *float64
		provenance *SegmentProvenance
		changed    *bool
	}{
		{"intro", &state.intro, update.IntroStart, update.IntroEnd, update.IntroProvenance, &applied.intro},
		{"credits", &state.credits, update.CreditsStart, update.CreditsEnd, update.CreditsProvenance, &applied.credits},
		{"recap", &state.recap, update.RecapStart, update.RecapEnd, update.RecapProvenance, &applied.recap},
		{"preview", &state.preview, update.PreviewStart, update.PreviewEnd, update.PreviewProvenance, &applied.preview},
	} {
		if patch.provenance != nil && patch.provenance.Absent {
			source, provider, confidence, algorithm := resolveSegmentProvenance(*update, patch.provenance)
			*patch.changed = applyAbsentSegment(patch.state, state.existingSource, source, provider, confidence, algorithm, mutationAt)
			continue
		}
		start, end, kindRanges := patch.start, patch.end, ranges[patch.kind]
		if patch.kind == models.MarkerSegmentCredits {
			// The intro has already been patched, so this checks against the
			// intro the file is left with.
			start, end, kindRanges = placedCredits(state, start, end, kindRanges)
		}
		source, provider, confidence, algorithm := resolveSegmentProvenance(*update, patch.provenance)
		changed, err := applySegmentRanges(patch.state, state.existingSource, source, provider, confidence, algorithm,
			start, end, kindRanges, state.duration, patch.kind, mutationAt)
		if err != nil {
			return markerSegmentFlags{}, err
		}
		*patch.changed = changed
		existingSource := patch.state.source
		if existingSource == nil {
			existingSource = state.existingSource
		}
		if start == nil && end == nil && len(kindRanges) == 0 && patch.state.provider != nil &&
			(existingSource == nil || *existingSource != models.MarkerSourceManual) &&
			slices.Contains(update.RefreshedProviders, *patch.state.provider) {
			*patch.changed = clearSegmentState(patch.state)
		}
	}
	return applied, nil
}

// applyMarkerMutation applies the manual clears in clearFlags, then update
// (nil for a clear-only request), to state, and reports which kinds changed.
// Clears go first so the credits in update are checked against the intro the
// request leaves the file with.
func applyMarkerMutation(update *MarkerUpdate, clearFlags markerSegmentFlags, state *markerMutationState, mutationAt time.Time) (markerSegmentFlags, error) {
	changed := markerSegmentFlags{}
	if clearFlags.intro {
		changed.intro = clearManualSegmentState(&state.intro, mutationAt)
	}
	if clearFlags.credits {
		changed.credits = clearManualSegmentState(&state.credits, mutationAt)
	}
	if clearFlags.recap {
		changed.recap = clearManualSegmentState(&state.recap, mutationAt)
	}
	if clearFlags.preview {
		changed.preview = clearManualSegmentState(&state.preview, mutationAt)
	}
	if update != nil {
		applied, err := applyMarkerUpdateToMutationState(update, state, mutationAt)
		if err != nil {
			return markerSegmentFlags{}, err
		}
		changed.intro = changed.intro || applied.intro
		changed.credits = changed.credits || applied.credits
		changed.recap = changed.recap || applied.recap
		changed.preview = changed.preview || applied.preview
	}
	return changed, nil
}

// placedCredits drops the credits in an update that fail the placement every
// marker source shares (markers.CheckCreditsPlacement), from its ranges and
// from its singular bounds. Dropping them before the write lets a refresh that
// returns only misplaced credits clear the provider's stored ones like any
// other withdrawn range.
func placedCredits(state *markerMutationState, start, end *float64, ranges []models.MarkerSegment) (*float64, *float64, []models.MarkerSegment) {
	limits := markers.CreditsLimitsForFile(&state.file)
	kept := limits.KeepPlaced(ranges, state.duration, state.intro.end)
	if start != nil || end != nil {
		s, e := state.credits.start, state.credits.end
		if start != nil {
			s = start
		}
		if end != nil {
			e = end
		}
		if s != nil && e != nil && limits.CheckCreditsPlacement(*s, *e, state.duration, state.intro.end) != nil {
			start, end = nil, nil
		}
	}
	return start, end, kept
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
			marker_segments = $32::jsonb,
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
		mutationMarkerSegments(state),
	)
	if err != nil {
		return false, fmt.Errorf("updating media markers: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, ErrFileNotFound
	}
	return true, nil
}

func mutationMarkerSegments(state markerMutationState) []models.MarkerSegment {
	segments := make([]models.MarkerSegment, 0)
	for _, segment := range []segmentState{state.intro, state.credits, state.recap, state.preview} {
		segments = append(segments, segment.ranges...)
	}
	sort.SliceStable(segments, func(i, j int) bool { return segments[i].StartSeconds < segments[j].StartSeconds })
	return segments
}

func clearSegmentState(state *segmentState) bool {
	if segmentEqual(*state, segmentState{}) {
		return false
	}
	state.ranges = nil
	state.start = nil
	state.end = nil
	state.source = nil
	state.provider = nil
	state.confidence = nil
	state.algorithm = nil
	state.detectedAt = nil
	return true
}

func clearManualSegmentState(state *segmentState, mutationAt time.Time) bool {
	source, algorithm, confidence := models.MarkerSourceManual, markers.ManualAlgorithm, 1.0
	next := segmentState{source: &source, algorithm: &algorithm, confidence: &confidence, detectedAt: &mutationAt}
	if segmentEqual(*state, next) {
		return false
	}
	*state = next
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

// stripVirtualResultParam returns the virtual URI with the pinned ?result=
// selection removed, preserving the scheme/host/path and every other query
// parameter. The result parameter is removed wherever it appears in the query
// string (leading, trailing, or between other parameters). A URI whose only
// query parameter was result collapses to the bare path with no trailing '?'.
// The input is returned unchanged when it carries no result parameter or
// cannot be parsed.
func stripVirtualResultParam(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := parsed.Query()
	if strings.TrimSpace(q.Get("result")) == "" {
		return raw
	}
	q.Del("result")
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// virtualPinDB is the narrow database surface the virtual pin writes need.
// *pgxpool.Pool implements it; tests substitute a scripted fake so the
// collision paths can be exercised without a live Postgres.
type virtualPinDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// virtualCandidateEvidenceResetSQL clears the probe, delivery and resolved-URL
// evidence a candidate produced and reports the live owner of the target path.
// It deliberately does not touch file_path: a row that lost a path-ownership
// race keeps its dead pin but must not keep a stored URL or evidence that
// describe the candidate that failed.
//
// The owner lookup and the clear are one statement so no repair can commit
// between them: a repair that rotates this row and writes fresh evidence makes
// the revision fence (file_path and updated_at observed before the failed CAS)
// match no row, and the update count stays zero. The caller must treat that as
// a concurrent modification, never as a clean success, so the new evidence is
// never erased. The owner predicate is: the exact target path, the same
// virtual owner installation and media folder, the same content and episode,
// a different row id, and no missing_since stamp.
//
// Placeholders: $1 losing row id, $2 target path, $3 observed file_path,
// $4 observed updated_at.
const virtualCandidateEvidenceResetSQL = `
	WITH owner AS (
		SELECT owner.id, owner.file_path
		FROM media_files current
		JOIN media_files owner
		  ON owner.file_path = $2
		 AND owner.virtual_owner_installation_id IS NOT DISTINCT FROM current.virtual_owner_installation_id
		 AND owner.media_folder_id IS NOT DISTINCT FROM current.media_folder_id
		 AND owner.content_id = current.content_id
		 AND owner.episode_id IS NOT DISTINCT FROM current.episode_id
		 AND owner.id <> current.id
		 AND owner.missing_since IS NULL
		WHERE current.id = $1
		LIMIT 1
	),
	cleared AS (
		UPDATE media_files
		SET probe_updated_at = NULL,
			probe_source = NULL,
			last_delivered_at = NULL,
			resolution = NULL,
			codec_video = NULL,
			codec_audio = NULL,
			audio_channels = NULL,
			container = 'virtual',
			hdr = false,
			bitrate = NULL,
			video_tracks = '[]'::jsonb,
			audio_tracks = '[]'::jsonb,
			subtitle_tracks = '[]'::jsonb,
			resolved_url = NULL,
			resolved_url_expires_at = NULL,
			-- Advance the revision so a stale evidence writer that snapshotted the
			-- pre-reset row cannot CAS its dead evidence back in after the clear.
			updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
		WHERE id = $1
		  AND file_path = $3
		  AND updated_at = $4
		  AND EXISTS (SELECT 1 FROM owner)
		RETURNING id
	)
	SELECT owner.id, owner.file_path, (SELECT count(*) FROM cleared)::int
	FROM owner`

// virtualPinUpdate reports where a pin replacement landed. Replaced is true
// when the requested target is represented in the catalog: either this row
// took the path, or a sibling owner row already held it. Owner is set to that
// sibling when the target path was already owned.
type virtualPinUpdate struct {
	Replaced bool
	Owner    *models.MediaFile
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// errVirtualPinRevisionSuperseded reports that a collision cleanup matched no
// row because the losing row was rotated after the failed CAS. A newer repair
// owns the row now, so its path and evidence must be left alone.
var errVirtualPinRevisionSuperseded = errors.New("virtual pin row changed after the failed update")

// virtualPinRevision is the losing row's state as observed before a pin CAS.
// The collision fallback fences its evidence reset on this revision: only the
// row that lost the CAS may be cleared, and only while it still holds the
// state the caller observed.
type virtualPinRevision struct {
	filePath  string
	updatedAt time.Time
}

// readVirtualPinRevision snapshots the row a pin write is about to CAS. It must
// run before the write: a read after a 23505 could observe a newer repair that
// another session already committed, and clearing against that revision would
// erase the healthy evidence the repair just wrote.
func readVirtualPinRevision(ctx context.Context, db virtualPinDB, fileID int) (virtualPinRevision, bool, error) {
	var rev virtualPinRevision
	err := db.QueryRow(ctx, `
		SELECT file_path, updated_at FROM media_files WHERE id = $1`, fileID).
		Scan(&rev.filePath, &rev.updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return virtualPinRevision{}, false, nil
	}
	if err != nil {
		return virtualPinRevision{}, false, fmt.Errorf("read virtual pin revision: %w", err)
	}
	return rev, true, nil
}

// adoptVirtualPinOwner handles a target path that a sibling already owns. The
// winning candidate is already pinned on that row, so the replacement must not
// be attempted again (the unique index would reject it forever). This row's
// candidate evidence is cleared by id without rewriting file_path: its stored
// URL and probe facts described the candidate that just failed, and keeping
// them would let the durable-resume fast path serve those dead bytes.
//
// The clear is revision-fenced to observed, the state read before the CAS that
// lost. A concurrent repair that rotated this row and committed fresh evidence
// makes the fence miss; that is reported as errVirtualPinRevisionSuperseded
// instead of success so the repair survives.
func adoptVirtualPinOwner(ctx context.Context, db virtualPinDB, fileID int, targetPath string, observed virtualPinRevision) (*models.MediaFile, error) {
	var ownerID int
	var ownerPath string
	var cleared int
	err := db.QueryRow(ctx, virtualCandidateEvidenceResetSQL,
		fileID, targetPath, observed.filePath, observed.updatedAt).
		Scan(&ownerID, &ownerPath, &cleared)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("resolve path owner for %q: no live owner row", targetPath)
	}
	if err != nil {
		return nil, fmt.Errorf("clear stale candidate evidence for %q: %w", targetPath, err)
	}
	if cleared == 0 {
		return nil, fmt.Errorf("%w: %q no longer matches the observed path %q at %s",
			errVirtualPinRevisionSuperseded, targetPath, observed.filePath, observed.updatedAt.Format(time.RFC3339Nano))
	}
	return &models.MediaFile{ID: ownerID, FilePath: ownerPath}, nil
}

// unpinVirtualResult strips the pinned ?result= selection from a virtual
// file's path, preserving every other query parameter. It is a no-op when the
// row vanished, no longer matches expectedPath (CAS), or carries no result
// parameter. expectedPath "" skips the CAS check.
//
// The path is read and then CAS'd in one statement rather than held under a
// row lock: a concurrent rotation makes the CAS match no row, which is already
// the intended no-op. Avoiding a transaction also lets a 23505 be explained by
// an owner lookup instead of aborting the transaction that produced it.
//
// A 23505 here means the neutral no-pin path is also owned by a sibling row
// (a provider re-list materialized it concurrently). The unpinned state already
// exists on that owner, so this row's dead candidate evidence is cleared by id
// and the operation reports success instead of leaving a stale pin behind. The
// clear is fenced on the path and updated_at observed before the failed CAS; a
// concurrent repair of this row is reported as an error and left untouched.
func (r *FileRepository) unpinVirtualResult(ctx context.Context, fileID int, expectedPath string) error {
	return unpinVirtualResultWithDB(ctx, r.pool, fileID, expectedPath)
}

func unpinVirtualResultWithDB(ctx context.Context, db virtualPinDB, fileID int, expectedPath string) error {
	observed, found, err := readVirtualPinRevision(ctx, db, fileID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	currentPath := observed.filePath
	if expectedPath != "" && currentPath != expectedPath {
		return nil
	}
	neutralPath := stripVirtualResultParam(currentPath)
	if neutralPath == currentPath {
		return nil
	}
	_, err = db.Exec(ctx, `
		UPDATE media_files
		SET file_path = $1
		WHERE id = $2 AND file_path = $3`, neutralPath, fileID, currentPath)
	if err != nil {
		if isUniqueViolation(err) {
			if _, adoptErr := adoptVirtualPinOwner(ctx, db, fileID, neutralPath, observed); adoptErr != nil {
				return fmt.Errorf("update path: %w (adopt path owner: %w)", err, adoptErr)
			}
			return nil
		}
		return fmt.Errorf("update path: %w", err)
	}
	return nil
}

// ClearVirtualResultPin strips the pinned ?result= selection from a virtual
// file's path so the next playback re-lists live provider candidates instead
// of resolving a cached link that just failed (expired or 5xx-ing debrid
// links otherwise brick the title: the pin survives every failover). No-op
// when the row has no pin or vanished concurrently.
func (r *FileRepository) ClearVirtualResultPin(ctx context.Context, fileID int) error {
	if err := r.unpinVirtualResult(ctx, fileID, ""); err != nil {
		return fmt.Errorf("clear virtual result pin for file %d: %w", fileID, err)
	}
	return nil
}

// ReplaceVirtualResultPin conditionally updates a virtual file's path if it still
// matches expectedPath. This prevents concurrent playback sessions or stale attempts
// from clobbering a newer or already-updated file path.
//
// Collision semantics: the unique index media_files_virtual_file_owner_key
// (file_path, virtual_owner_installation_id, media_folder_id) means a replacement
// path can only be written to this row if no sibling row already owns that tuple.
// A unique violation (SQLSTATE 23505) therefore means the winning candidate is a
// separate live row that a concurrent scan/refresh already re-listed. Repointing
// this row would duplicate it and an unpin can collide the same way when the
// neutral path is owned too, so the existing owner row is adopted: the target is
// already pinned there, this row's stale evidence is cleared by id without
// rewriting file_path, and the update reports success. The clear is fenced on the
// losing row's path and updated_at observed before the CAS; if another session
// repaired this row after the collision, the update reports an error and leaves
// the newer path and evidence in place. A collision with no resolvable owner is
// a genuine failure and is returned as an error.
func (r *FileRepository) ReplaceVirtualResultPin(ctx context.Context, fileID int, expectedPath, replacementPath string) (bool, error) {
	res, err := replaceVirtualResultPinWithDB(ctx, r.pool, fileID, expectedPath, replacementPath)
	if err != nil {
		return false, fmt.Errorf("replace virtual result pin for file %d: %w", fileID, err)
	}
	return res.Replaced, nil
}

func replaceVirtualResultPinWithDB(ctx context.Context, db virtualPinDB, fileID int, expectedPath, replacementPath string) (virtualPinUpdate, error) {
	observed, found, err := readVirtualPinRevision(ctx, db, fileID)
	if err != nil {
		return virtualPinUpdate{}, err
	}
	if !found {
		return virtualPinUpdate{}, nil
	}
	tag, err := db.Exec(ctx, `
		UPDATE media_files
		SET file_path = $1,
			-- The replacement is a different provider candidate. Probe and
			-- delivery evidence belongs to the candidate that produced it, so
			-- clear it: otherwise the replacement looks probed and recently
			-- delivered (optimistic-start eligibility) without either.
			probe_updated_at = NULL,
			probe_source = NULL,
			last_delivered_at = NULL,
			resolution = NULL,
			codec_video = NULL,
			codec_audio = NULL,
			audio_channels = NULL,
			container = 'virtual',
			hdr = false,
			bitrate = NULL,
			video_tracks = '[]'::jsonb,
			audio_tracks = '[]'::jsonb,
			subtitle_tracks = '[]'::jsonb,
			-- The stored URL and its expiry describe the old candidate's bytes.
			-- Clearing them with the rest of the evidence means no URL survives
			-- pointing at a path this row no longer describes, and the
			-- durable-resume fast path cannot fire for the replacement until it
			-- has been resolved and probed itself.
			resolved_url = NULL,
			resolved_url_expires_at = NULL
		WHERE id = $2 AND file_path = $3`, replacementPath, fileID, expectedPath)
	if err != nil {
		if isUniqueViolation(err) {
			owner, adoptErr := adoptVirtualPinOwner(ctx, db, fileID, replacementPath, observed)
			if adoptErr != nil {
				return virtualPinUpdate{}, fmt.Errorf("replacement path is owned: %w (after %w)", adoptErr, err)
			}
			// The losing path remains on this row, but no dead URL or probe
			// evidence survives. The live candidate is represented by owner.
			return virtualPinUpdate{Replaced: true, Owner: owner}, nil
		}
		return virtualPinUpdate{}, err
	}
	return virtualPinUpdate{Replaced: tag.RowsAffected() > 0}, nil
}

// PlayableContentID returns the catalog item a media file plays as: its
// episode, otherwise its content item, otherwise its local extra. It returns
// ErrFileNotFound when no file has the id, and "" for an unlinked file.
func (r *FileRepository) PlayableContentID(ctx context.Context, id int) (string, error) {
	var contentID string
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(episode_id, ''), NULLIF(content_id, ''), NULLIF(extra_id, ''), '')
		FROM media_files
		WHERE id = $1
	`, id).Scan(&contentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrFileNotFound
	}
	if err != nil {
		return "", fmt.Errorf("querying playable content id for media file %d: %w", id, err)
	}
	return contentID, nil
}

// GetByIDs retrieves media files by primary key.
func (r *FileRepository) GetByIDs(ctx context.Context, ids []int) ([]*models.MediaFile, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+fileColumns+`
		FROM media_files
		WHERE id = ANY($1::bigint[])
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

// virtualNeutralLikePrefix builds a left-anchored LIKE pattern covering every
// stored path for a neutral virtual key. The neutral key's query string is
// dropped: a provider candidate URI may carry result= in any parameter
// position, so only the scheme/host/path is guaranteed to prefix it. The
// prefix is escaped with the backslash escape character declared by the caller
// so a literal %, _ or \ in the path cannot widen the match.
func virtualNeutralLikePrefix(neutralPath string) string {
	base := neutralPath
	if i := strings.IndexByte(base, '?'); i >= 0 {
		base = base[:i]
	}
	var b strings.Builder
	b.Grow(len(base) + 1)
	for i := 0; i < len(base); i++ {
		switch c := base[i]; c {
		case '\\', '%', '_':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('%')
	return b.String()
}

// GetVirtualCandidateByNeutralPath retrieves a virtual candidate while ignoring
// the provider's rotating result selection. The stored path is matched with a
// left-anchored LIKE so the lookup is sargable; the exact neutral key is then
// confirmed in Go because a bare neutral key is also a prefix of sibling URIs
// (for example a longer content id under the same scheme/host/path).
func (r *FileRepository) GetVirtualCandidateByNeutralPath(ctx context.Context, neutralPath, contentID, episodeID string, ownerInstallationID int) (*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + `
		FROM media_files
		WHERE virtual_owner_installation_id = $1
		  AND content_id = $2
		  AND COALESCE(episode_id, '') = COALESCE($3, '')
		  AND file_path LIKE $4 ESCAPE '\'
		  AND missing_since IS NULL
		ORDER BY id`
	rows, err := r.pool.Query(ctx, query, ownerInstallationID, contentID, episodeID, virtualNeutralLikePrefix(neutralPath))
	if err != nil {
		return nil, fmt.Errorf("querying virtual candidate by neutral path: %w", err)
	}
	defer rows.Close()
	files, err := scanMediaFiles(rows)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if stripVirtualResultParam(file.FilePath) == neutralPath {
			return file, nil
		}
	}
	return nil, ErrFileNotFound
}

// IsActivePath reports whether path is the exact logical path of a media file
// that is still active in the catalog. Scanner paths are authoritative here:
// they deliberately preserve readable symlinks instead of replacing them with
// their physical targets.
func (r *FileRepository) IsActivePath(ctx context.Context, path string) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM media_files
			WHERE file_path = $1
			  AND missing_since IS NULL
		)
	`, path).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("checking active media file path: %w", err)
	}
	return active, nil
}

// IsActiveFile reports whether media file id is still active in the catalog
// at exactly path. Scanner paths are authoritative here, as for IsActivePath.
// The core checks a transcode node's source grant with it: a node may open a
// granted source only while this holds.
func (r *FileRepository) IsActiveFile(ctx context.Context, id int, path string) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM media_files
			WHERE id = $1
			  AND file_path = $2
			  AND missing_since IS NULL
		)
	`, id, path).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("checking active media file %d: %w", id, err)
	}
	return active, nil
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
		WHERE (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
			WHERE (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
			WHERE (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
			WHERE (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
			WHERE (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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

// IsMatchSuppressed reports whether a raw unmatched file has been canceled
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
		  AND (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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

// CountUnmatchedMatchBacklogByFolders counts raw matcher work for multiple
// libraries in one query. Libraries without eligible files are omitted.
func (r *FileRepository) CountUnmatchedMatchBacklogByFolders(ctx context.Context, folderIDs []int, mode RawMatchBacklogMode) (map[int]int, error) {
	counts := make(map[int]int, len(folderIDs))
	if len(folderIDs) == 0 {
		return counts, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT mf.media_folder_id, COUNT(*)
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		WHERE mf.media_folder_id = ANY($1)
		  AND (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
		GROUP BY mf.media_folder_id
	`, folderIDs, string(normalizeRawMatchBacklogMode(mode)))
	if err != nil {
		return nil, fmt.Errorf("counting unmatched match backlog by folders: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var folderID, count int
		if err := rows.Scan(&folderID, &count); err != nil {
			return nil, fmt.Errorf("scanning unmatched match backlog counts: %w", err)
		}
		counts[folderID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating unmatched match backlog counts: %w", err)
	}
	return counts, nil
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
		  AND (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
		  AND (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
		  AND (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
		  AND (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
			  AND (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
			  AND (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
			  AND (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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
			  AND (mf.content_id IS NULL OR mf.content_id = '') AND mf.extra_id IS NULL
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

// MarkMissing sets the missing_since timestamp for the given media file while
// it is still recorded at filePath. A row that moved to another path since the
// caller read it (a rename relinked it) is left alone and reported as
// ErrFileNotFound, like a row deleted meanwhile.
func (r *FileRepository) MarkMissing(ctx context.Context, id int, filePath string, since time.Time) error {
	tag, err := r.pool.Exec(ctx,
		"UPDATE media_files SET missing_since = $1, updated_at = NOW() WHERE id = $2 AND file_path = $3",
		since, id, filePath,
	)
	if err != nil {
		return fmt.Errorf("marking file missing: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFileNotFound
	}
	return nil
}

// MarkMissingWrittenBefore is MarkMissing for a row last written before
// before. A row another scan wrote since then is left alone and reported as
// ErrFileNotFound, so a scan cannot hide a file a later scan just indexed.
func (r *FileRepository) MarkMissingWrittenBefore(ctx context.Context, id int, filePath string, since, before time.Time) error {
	tag, err := r.pool.Exec(ctx,
		"UPDATE media_files SET missing_since = $1, updated_at = NOW() WHERE id = $2 AND file_path = $3 AND updated_at < $4",
		since, id, filePath, before,
	)
	if err != nil {
		return fmt.Errorf("marking file missing: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFileNotFound
	}
	return nil
}

// DeleteMissingByFolder deletes media files in the given folder that have been
// marked missing for longer than the grace period. Missing files are already
// hidden from clients; the grace only delays deleting the row so a file that
// reappears within the window restores without re-probing or re-matching.
// A zero grace deletes all missing-marked rows immediately.
//
// Rows of an item that still exists without any library membership are kept
// whatever their age: membership reconciliation runs first and deletes every
// orphan it may, so such an item is held (catalog WithRemovalGrace) or
// protected, and its orphan check on a later pass needs these rows to find it.
//
// Rows whose file_path lies at or under one of protectedRoots are never
// deleted, no matter how long they have been missing: an unreachable library
// root (dead drive, lost mount) is temporarily offline, not removed, so its
// catalog state must survive until the root is reachable again. Passing no
// protected roots preserves the historical folder-wide sweep exactly.
// Returns the number of rows deleted.
func (r *FileRepository) DeleteMissingByFolder(ctx context.Context, folderID int, gracePeriod time.Duration, protectedRoots []string) (int, error) {
	cutoff := time.Now().UTC().Add(-gracePeriod)
	// Virtual plugin-backed files are not present on the local filesystem by
	// design. They must never be treated as missing physical files by scanner
	// cleanup, otherwise a scan/restart disables playback for every virtual item.
	query := `WITH deleted AS (
		DELETE FROM media_files mf
		WHERE mf.media_folder_id = $1
		  AND mf.missing_since IS NOT NULL
		  AND mf.missing_since < $2
		  AND (mf.container IS NULL OR mf.container <> 'virtual')
		  AND mf.file_path NOT LIKE 'virtual://%'
		  AND NOT EXISTS (
			SELECT 1 FROM media_items mi
			WHERE mi.content_id = mf.content_id
			  AND NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id)
		  )`
	args := []any{folderID, cutoff}
	if clauses, clauseArgs := rootCoverageClauses(protectedRoots, len(args)+1); len(clauses) > 0 {
		query += " AND NOT (" + strings.Join(clauses, " OR ") + ")"
		args = append(args, clauseArgs...)
	}
	query += `
		RETURNING mf.id, mf.extra_id
	)` + catalog.ClearEmptiedExtraStillsCTE + `
	SELECT count(*) FROM deleted`
	var deleted int
	if err := r.pool.QueryRow(ctx, query, args...).Scan(&deleted); err != nil {
		return 0, fmt.Errorf("deleting missing files for folder %d: %w", folderID, err)
	}
	return deleted, nil
}

// listRootsWithCatalogedFilesSQL backs ListRootsWithCatalogedFiles. The
// descendants are matched by bytewise range, in an EXISTS of their own, so
// each probe is an index range scan on (media_folder_id, file_path). A LIKE
// pattern taken from the unnested row, or an OR with the exact match, cannot
// become an index condition, and a root with no rows would then read every
// file of the folder.
const listRootsWithCatalogedFilesSQL = `
	SELECT r.root
	FROM unnest($2::text[], $3::text[], $4::text[]) WITH ORDINALITY AS r(root, lower_bound, upper_bound, ord)
	WHERE EXISTS (
		SELECT 1 FROM media_files mf
		WHERE mf.media_folder_id = $1
		  AND mf.file_path ~>=~ r.lower_bound AND mf.file_path ~<~ r.upper_bound
	) OR EXISTS (
		SELECT 1 FROM media_files mf
		WHERE mf.media_folder_id = $1 AND mf.file_path = r.root
	)
	ORDER BY r.ord
`

// ListRootsWithCatalogedFiles returns the subset of roots (in input order)
// that still have any media_files rows at or under them in the folder,
// whether those rows are present or already marked missing.
//
// This is the proactive counterpart to ListRootsWithOnlyMissingFiles. That
// query requires a root to have NO live rows left, which means it can only
// recognize a lost mount after a scan has already marked its files missing —
// i.e. after the damage is done. For deciding whether to mark in the first
// place, the question is simply "does the catalog believe anything lives
// here", because an empty-but-reachable directory that still owns cataloged
// files is the signature of a dropped mount exposing its bare mountpoint.
//
// A genuinely emptied root also matches, which is intended: emptying a root
// is confirmed through the operator's one-time cleanup allowance rather than
// inferred from a single scan.
func (r *FileRepository) ListRootsWithCatalogedFiles(ctx context.Context, folderID int, roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	lowers := make([]string, len(roots))
	uppers := make([]string, len(roots))
	for i, root := range roots {
		lowers[i], uppers[i] = pathscope.DescendantRange(root)
	}
	rows, err := r.pool.Query(ctx, listRootsWithCatalogedFilesSQL, folderID, roots, lowers, uppers)
	if err != nil {
		return nil, fmt.Errorf("querying roots with cataloged files: %w", err)
	}
	defer rows.Close()

	occupied := make([]string, 0)
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, fmt.Errorf("scanning root with cataloged files: %w", err)
		}
		occupied = append(occupied, root)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating roots with cataloged files: %w", err)
	}
	return occupied, nil
}

// ListRootsWithOnlyMissingFiles returns the subset of roots (in input order)
// that still have media_files rows at or under them in the folder but none
// that are present (missing_since IS NULL). A reachable root in this state is
// "suspect empty": it is the on-disk signature of a mount that dropped out
// while leaving an empty, stat-able mountpoint directory, which a
// reachability probe cannot distinguish from an intentionally emptied root.
func (r *FileRepository) ListRootsWithOnlyMissingFiles(ctx context.Context, folderID int, roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	patterns := make([]string, len(roots))
	for i, root := range roots {
		patterns[i] = pathscope.PrefixLike(root)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT r.root
		FROM unnest($2::text[], $3::text[]) WITH ORDINALITY AS r(root, pattern, ord)
		WHERE EXISTS (
			SELECT 1 FROM media_files mf
			WHERE mf.media_folder_id = $1
			  AND (mf.file_path = r.root OR mf.file_path LIKE r.pattern ESCAPE '\')
		)
		AND NOT EXISTS (
			SELECT 1 FROM media_files mf
			WHERE mf.media_folder_id = $1
			  AND (mf.file_path = r.root OR mf.file_path LIKE r.pattern ESCAPE '\')
			  AND mf.missing_since IS NULL
		)
		ORDER BY r.ord
	`, folderID, roots, patterns)
	if err != nil {
		return nil, fmt.Errorf("querying roots with only missing files: %w", err)
	}
	defer rows.Close()

	suspect := make([]string, 0)
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, fmt.Errorf("scanning suspect-empty root: %w", err)
		}
		suspect = append(suspect, root)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating suspect-empty roots: %w", err)
	}
	return suspect, nil
}

// DeleteByIDs removes specific media file rows by primary key, clearing the
// image of any local extra left without a file
// (catalog.ClearEmptiedExtraStillsCTE).
func (r *FileRepository) DeleteByIDs(ctx context.Context, ids []int) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	var deleted int
	err := r.pool.QueryRow(ctx, `WITH deleted AS (
		DELETE FROM media_files WHERE id = ANY($1) RETURNING id, extra_id
	)`+catalog.ClearEmptiedExtraStillsCTE+`
	SELECT count(*) FROM deleted`,
		ids,
	).Scan(&deleted)
	if err != nil {
		return 0, fmt.Errorf("deleting media files by id: %w", err)
	}
	return deleted, nil
}

// ListIDsOutsideRoots returns file row ids for a folder whose paths are no
// longer covered by any configured root.
func (r *FileRepository) ListIDsOutsideRoots(ctx context.Context, folderID int, roots []string) ([]int, error) {
	if len(roots) == 0 {
		rows, err := r.pool.Query(ctx, `SELECT id FROM media_files WHERE media_folder_id = $1 AND (container IS NULL OR container <> 'virtual') AND file_path NOT LIKE 'virtual://%'`, folderID)
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
	coveredClauses, coveredArgs := rootCoverageClauses(roots, 2)
	args = append(args, coveredArgs...)

	query := `SELECT id FROM media_files WHERE media_folder_id = $1 AND (container IS NULL OR container <> 'virtual') AND file_path NOT LIKE 'virtual://%' AND NOT (` + strings.Join(coveredClauses, " OR ") + `)`
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

// GetByFolderAndPathPrefix returns all files for a folder that live under a
// subtree path.
func (r *FileRepository) GetByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string) ([]*models.MediaFile, error) {
	query, args := folderPathPrefixQuery(fileColumns, folderID, pathPrefix)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying files by folder and path prefix: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// folderPathPrefixQuery selects columns for the files of a folder at or under
// pathPrefix. The range bounds let the (media_folder_id, file_path
// text_pattern_ops) index narrow the subtree even under a generic plan, which
// a parameterized LIKE cannot do.
func folderPathPrefixQuery(columns string, folderID int, pathPrefix string) (string, []any) {
	clauses, args := pathscope.RangeCoverageClauses("file_path", []string{pathPrefix}, 2)
	query := `SELECT ` + columns + ` FROM media_files
		WHERE media_folder_id = $1 AND (` + strings.Join(clauses, " OR ") + `)
		ORDER BY file_path ASC`
	return query, append([]any{folderID}, args...)
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
// excluding files that are missing. This preserves the long-standing id order
// used by the general catalog and playback paths.
func (r *FileRepository) GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE content_id = $1 AND missing_since IS NULL
		ORDER BY (container = 'virtual') ASC, id ASC`
	rows, err := r.pool.Query(ctx, query, contentID)
	if err != nil {
		return nil, fmt.Errorf("querying files by content_id: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// GetByContentIDPresentation is the audiobook presentation ordering variant.
// Multipart books use the scanner-assigned part index. Rows that predate the
// column have no index and SQL cannot apply naturalPathLess to them, so those
// legacy rows are re-sorted in Go after the query; part10.m4b must follow
// part2.m4b for them too. Keeping this separate avoids changing ordering
// assumptions in movie and series playback.
func (r *FileRepository) GetByContentIDPresentation(ctx context.Context, contentID string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE content_id = $1 AND missing_since IS NULL
		ORDER BY COALESCE(presentation_part_index, 2147483647), file_path ASC, id ASC`
	rows, err := r.pool.Query(ctx, query, contentID)
	if err != nil {
		return nil, fmt.Errorf("querying presentation files by content_id: %w", err)
	}
	defer rows.Close()
	files, err := scanMediaFiles(rows)
	if err != nil {
		return nil, err
	}
	sortPresentationFiles(files)
	return files, nil
}

// sortPresentationFiles orders multipart rows for one content ID: indexed rows
// by part index, then legacy rows without one by natural file path. The SQL
// ordering already places the null-index group last, so this only fixes the
// intra-group lexical order (part10 before part2) that SQL cannot express.
func sortPresentationFiles(files []*models.MediaFile) {
	sort.SliceStable(files, func(i, j int) bool {
		a, b := files[i], files[j]
		if a == nil || b == nil {
			return b == nil && a != nil
		}
		aIndexed := a.PresentationPartIndex > 0
		bIndexed := b.PresentationPartIndex > 0
		switch {
		case aIndexed && bIndexed:
			return a.PresentationPartIndex < b.PresentationPartIndex
		case aIndexed != bIndexed:
			return aIndexed
		}
		if a.FilePath != b.FilePath {
			return naturalPathLess(a.FilePath, b.FilePath)
		}
		return a.ID < b.ID
	})
}

// FirstDurationsByContentIDs returns the probed duration (seconds) of the
// first live file backing each content id, using the same "first file with
// duration > 0, ordered by id" rule as the v1 API's contentDurationSeconds.
// Ids with no live probed file are absent from the map. Resolution is
// intentionally not access-scoped; callers have already filtered the items.
func (r *FileRepository) FirstDurationsByContentIDs(ctx context.Context, contentIDs []string) (map[string]int, error) {
	result := make(map[string]int)
	if len(contentIDs) == 0 {
		return result, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (content_id) content_id, duration
		FROM media_files
		WHERE content_id = ANY($1)
		  AND episode_id IS NULL
		  AND missing_since IS NULL
		  AND duration > 0
		ORDER BY content_id, id ASC`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("querying first durations by content ids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var contentID string
		var duration int
		if err := rows.Scan(&contentID, &duration); err != nil {
			return nil, fmt.Errorf("scanning first duration by content id: %w", err)
		}
		result[contentID] = duration
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating first durations by content ids: %w", err)
	}
	return result, nil
}

// GetByExtraID returns the live files backing a local extra
// (media_extras.content_id). Extras files carry no content_id/episode_id, so
// this is their only ownership lookup.
func (r *FileRepository) GetByExtraID(ctx context.Context, extraID string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE extra_id = $1 AND missing_since IS NULL
		ORDER BY id ASC`
	rows, err := r.pool.Query(ctx, query, extraID)
	if err != nil {
		return nil, fmt.Errorf("querying files by extra_id: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// FindParentContentIDForStem finds the owning content id of a primary file in
// dir whose filename stem matches exactly ("Movie A" matches "Movie A.mkv").
// Used to bind suffix-classified extras ("Movie A-trailer.mkv") in flat
// multi-item directories.
func (r *FileRepository) FindParentContentIDForStem(ctx context.Context, folderID int, dir, stem string) (string, error) {
	pattern := pathscope.EscapeLike(filepath.Join(dir, stem)) + ".%"
	var parentID *string
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(e.series_id, mf.content_id)
		FROM media_files mf
		LEFT JOIN episodes e ON e.content_id = mf.episode_id
		WHERE mf.media_folder_id = $1
		  AND mf.file_path LIKE $2 ESCAPE '\'
		  AND mf.extra_id IS NULL
		  AND (mf.content_id IS NOT NULL OR mf.episode_id IS NOT NULL)
		ORDER BY mf.id ASC
		LIMIT 1`, folderID, pattern).Scan(&parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("finding parent by stem: %w", err)
	}
	if parentID == nil {
		return "", nil
	}
	return *parentID, nil
}

// FindUnambiguousParentContentIDForDir returns the single content id owning
// the primary files under dir, or "" when the directory holds no matched
// content or more than one distinct item (ambiguous — caller defers). Rows
// marked missing still count: dropping them could leave a sibling as the sole
// owner and bind the extra to the wrong item. Rows at excludePaths are
// ignored: they are extras still carrying a primary link from before they
// were classified.
func (r *FileRepository) FindUnambiguousParentContentIDForDir(ctx context.Context, folderID int, dir string, excludePaths []string) (string, error) {
	if excludePaths == nil {
		excludePaths = []string{}
	}
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT COALESCE(e.series_id, mf.content_id) AS parent_id
		FROM media_files mf
		LEFT JOIN episodes e ON e.content_id = mf.episode_id
		WHERE mf.media_folder_id = $1
		  AND mf.file_path LIKE $2 ESCAPE '\'
		  AND mf.file_path <> ALL($3::text[])
		  AND mf.extra_id IS NULL
		  AND (mf.content_id IS NOT NULL OR mf.episode_id IS NOT NULL)
		LIMIT 2`, folderID, pathPrefixLike(dir), excludePaths)
	if err != nil {
		return "", fmt.Errorf("finding parent by dir: %w", err)
	}
	defer rows.Close()

	parents := make([]string, 0, 2)
	for rows.Next() {
		var parentID *string
		if err := rows.Scan(&parentID); err != nil {
			return "", fmt.Errorf("scanning parent id: %w", err)
		}
		if parentID != nil && *parentID != "" {
			parents = append(parents, *parentID)
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterating parent ids: %w", err)
	}
	if len(parents) != 1 {
		return "", nil
	}
	return parents[0], nil
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

// ListOverlayFilesByContentIDs returns a lightweight media-file projection for
// building overlay summaries, grouped by content ID.
func (r *FileRepository) ListOverlayFilesByContentIDs(ctx context.Context, contentIDs []string) (map[string][]*models.MediaFile, error) {
	grouped := make(map[string][]*models.MediaFile, len(contentIDs))
	if len(contentIDs) == 0 {
		return grouped, nil
	}

	query := `SELECT ` + overlayFileColumns + ` FROM media_files
		WHERE content_id = ANY($1) AND missing_since IS NULL
		ORDER BY content_id ASC, id ASC`
	rows, err := r.pool.Query(ctx, query, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("querying overlay files by content_ids: %w", err)
	}
	defer rows.Close()

	files, err := scanOverlayMediaFiles(rows)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.ContentID == "" {
			continue
		}
		grouped[file.ContentID] = append(grouped[file.ContentID], file)
	}
	return grouped, nil
}

// servedEpisodeRows reads the served episode ID that prefixes each row of a
// query joined to catalog.ServedEpisodeFilesSQL, in row order. A later episode
// of a multi-episode file differs from the file's own episode_id.
type servedEpisodeRows struct {
	pgx.Rows
	episodeIDs []string
}

func (r *servedEpisodeRows) Scan(dest ...any) error {
	var episodeID string
	if err := r.Rows.Scan(append([]any{&episodeID}, dest...)...); err != nil {
		return err
	}
	r.episodeIDs = append(r.episodeIDs, episodeID)
	return nil
}

// ListByEpisodeIDs returns media files grouped by episode ID for the given
// episode IDs, excluding files that are marked missing. A multi-episode file
// is listed under every requested episode it spans.
//
// A canonical episode row can be fileless when episode_aliases points at a
// duplicate layout row that owns the files. For a requested episode, the files
// of its alias rows are returned under the requested canonical key after the
// direct files. Returned MediaFile.EpisodeID stays the stored alias ID; only
// the result map key is canonical. Keeping both sets is deliberate: the
// playability query can admit a canonical episode through either a direct file
// or an alias file, so the file listing must offer the same candidate.
// Direct files come first, then alias files, each in file ID order, so the
// canonical episode's own files lead regardless of the alias rows' IDs.
func (r *FileRepository) ListByEpisodeIDs(ctx context.Context, episodeIDs []string) (map[string][]*models.MediaFile, error) {
	direct, err := r.listFilesByEpisodeIDs(ctx, episodeIDs)
	if err != nil {
		return nil, err
	}
	if len(episodeIDs) == 0 {
		return direct, nil
	}

	seen := make(map[string]struct{}, len(episodeIDs))
	canonicalIDs := make([]string, 0, len(episodeIDs))
	for _, episodeID := range episodeIDs {
		if _, ok := seen[episodeID]; ok {
			continue
		}
		seen[episodeID] = struct{}{}
		canonicalIDs = append(canonicalIDs, episodeID)
	}

	fallback, err := r.listAliasFilesByCanonicalEpisodeIDs(ctx, canonicalIDs)
	if err != nil {
		return nil, err
	}
	for canonicalID, files := range fallback {
		direct[canonicalID] = mergeEpisodeFiles(direct[canonicalID], files)
	}
	return direct, nil
}

// mergeEpisodeFiles lists the canonical episode's own files before the files
// of its alias rows, preserving the ID order each query returned. Both input
// slices are already ordered by file ID, so the result is deterministic without
// re-sorting across the two sets. A file that appears in both is kept once, at
// its direct position.
func mergeEpisodeFiles(direct, alias []*models.MediaFile) []*models.MediaFile {
	if len(alias) == 0 {
		return direct
	}
	seen := make(map[int]struct{}, len(direct)+len(alias))
	merged := make([]*models.MediaFile, 0, len(direct)+len(alias))
	for _, files := range [][]*models.MediaFile{direct, alias} {
		for _, file := range files {
			if file == nil {
				continue
			}
			if _, ok := seen[file.ID]; ok {
				continue
			}
			seen[file.ID] = struct{}{}
			merged = append(merged, file)
		}
	}
	return merged
}

// listFilesByEpisodeIDs is the direct media_files lookup behind
// ListByEpisodeIDs. It groups by the served episode ID, so a multi-episode
// file is listed under every requested episode it spans.
func (r *FileRepository) listFilesByEpisodeIDs(ctx context.Context, episodeIDs []string) (map[string][]*models.MediaFile, error) {
	grouped := make(map[string][]*models.MediaFile, len(episodeIDs))
	if len(episodeIDs) == 0 {
		return grouped, nil
	}

	query := `SELECT served.episode_id, ` + mfFileColumns + `
		FROM (` + catalog.ServedEpisodeFilesSQL + `) served(episode_id, file_id)
		JOIN media_files mf ON mf.id = served.file_id
		ORDER BY served.episode_id ASC,
		         CASE WHEN mf.episode_id = served.episode_id THEN 0 ELSE 1 END ASC,
		         mf.id ASC`
	rows, err := r.pool.Query(ctx, query, episodeIDs)
	if err != nil {
		return nil, fmt.Errorf("querying files by episode_ids: %w", err)
	}
	defer rows.Close()

	served := &servedEpisodeRows{Rows: rows}
	files, err := scanMediaFiles(served)
	if err != nil {
		return nil, err
	}
	for i, file := range files {
		grouped[served.episodeIDs[i]] = append(grouped[served.episodeIDs[i]], file)
	}
	return grouped, nil
}

func (r *FileRepository) ListEpisodeListFilesByEpisodeIDs(ctx context.Context, ids []string) (map[string][]*models.MediaFile, error) {
	return r.listFilesByEpisodeIDs(ctx, ids)
}

// listAliasFilesByCanonicalEpisodeIDs resolves canonical episode IDs to the
// files owned by their alias rows. It returns alias files independently of
// direct files, ordered by file ID. ListByEpisodeIDs appends them after the
// canonical episode's direct files.
func (r *FileRepository) listAliasFilesByCanonicalEpisodeIDs(ctx context.Context, canonicalEpisodeIDs []string) (map[string][]*models.MediaFile, error) {
	grouped := make(map[string][]*models.MediaFile, len(canonicalEpisodeIDs))
	mappingRows, err := r.pool.Query(ctx, `
		SELECT canonical_episode_id, alias_episode_id
		FROM episode_aliases
		WHERE canonical_episode_id = ANY($1)
		  AND alias_episode_id IS NOT NULL
		ORDER BY canonical_episode_id ASC, alias_episode_id ASC`, canonicalEpisodeIDs)
	if err != nil {
		return nil, fmt.Errorf("querying episode aliases: %w", err)
	}
	aliasToCanonical := make(map[string]string)
	aliasIDs := make([]string, 0, len(canonicalEpisodeIDs))
	for mappingRows.Next() {
		var canonicalID, aliasID string
		if err := mappingRows.Scan(&canonicalID, &aliasID); err != nil {
			mappingRows.Close()
			return nil, fmt.Errorf("scanning episode alias: %w", err)
		}
		if _, ok := aliasToCanonical[aliasID]; ok {
			continue
		}
		aliasToCanonical[aliasID] = canonicalID
		aliasIDs = append(aliasIDs, aliasID)
	}
	if err := mappingRows.Err(); err != nil {
		mappingRows.Close()
		return nil, fmt.Errorf("iterating episode aliases: %w", err)
	}
	mappingRows.Close()
	if len(aliasIDs) == 0 {
		return grouped, nil
	}

	filesByAlias, err := r.listFilesByEpisodeIDs(ctx, aliasIDs)
	if err != nil {
		return nil, err
	}
	for aliasID, files := range filesByAlias {
		canonicalID := aliasToCanonical[aliasID]
		if canonicalID == "" {
			continue
		}
		grouped[canonicalID] = append(grouped[canonicalID], files...)
	}
	for canonicalID := range grouped {
		sort.Slice(grouped[canonicalID], func(i, j int) bool {
			return grouped[canonicalID][i].ID < grouped[canonicalID][j].ID
		})
	}
	return grouped, nil
}

// ListOverlayFilesByEpisodeIDs returns a lightweight media-file projection for
// building overlay summaries, grouped by episode ID.
func (r *FileRepository) ListOverlayFilesByEpisodeIDs(ctx context.Context, episodeIDs []string) (map[string][]*models.MediaFile, error) {
	grouped := make(map[string][]*models.MediaFile, len(episodeIDs))
	if len(episodeIDs) == 0 {
		return grouped, nil
	}

	query := `SELECT served.episode_id, ` + mfOverlayFileColumns + `
		FROM (` + catalog.ServedEpisodeFilesSQL + `) served(episode_id, file_id)
		JOIN media_files mf ON mf.id = served.file_id
		ORDER BY served.episode_id ASC, mf.id ASC`
	rows, err := r.pool.Query(ctx, query, episodeIDs)
	if err != nil {
		return nil, fmt.Errorf("querying overlay files by episode_ids: %w", err)
	}
	defer rows.Close()

	served := &servedEpisodeRows{Rows: rows}
	files, err := scanOverlayMediaFiles(served)
	if err != nil {
		return nil, err
	}
	for i, file := range files {
		grouped[served.episodeIDs[i]] = append(grouped[served.episodeIDs[i]], file)
	}
	return grouped, nil
}

// UpdateContentID sets the content_id on a media file, linking it to a matched
// media item. The matcher calls it with the path it read the file at, and the
// write applies only while the row still has that path: a scan that moved the
// row to another title meanwhile (RetitleMovedFile) cleared its links for a
// rematch, and a decision made from the old path must not link it back. A
// refused write returns ErrFileMoved; a missing row returns ErrFileNotFound.
func (r *FileRepository) UpdateContentID(ctx context.Context, fileID int, filePath, contentID string) error {
	tag, err := r.pool.Exec(ctx,
		"UPDATE media_files SET content_id = $1, updated_at = NOW() WHERE id = $2 AND file_path = $3",
		contentID, fileID, filePath)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return r.unwrittenFileError(ctx, fileID)
	}
	return nil
}

// UnlinkContentID undoes a link the caller made with UpdateContentID: it
// clears content_id only while the row still carries contentID, wherever the
// row is now, since a scan may have moved it with its links meanwhile
// (RelinkMovedFile). A row that another writer linked elsewhere, or that
// lost the link already, is left alone; unlinked then reports false.
func (r *FileRepository) UnlinkContentID(ctx context.Context, fileID int, contentID string) (unlinked bool, err error) {
	tag, err := r.pool.Exec(ctx,
		"UPDATE media_files SET content_id = NULL, updated_at = NOW() WHERE id = $1 AND content_id = $2",
		fileID, contentID)
	if err != nil {
		return false, fmt.Errorf("unlinking media file %d from %s: %w", fileID, contentID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// unwrittenFileError explains a path-fenced write that matched no row:
// ErrFileNotFound when the row is gone, ErrFileMoved when it has another path.
func (r *FileRepository) unwrittenFileError(ctx context.Context, fileID int) error {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM media_files WHERE id = $1)`, fileID).Scan(&exists); err != nil {
		return fmt.Errorf("checking media file %d: %w", fileID, err)
	}
	if !exists {
		return ErrFileNotFound
	}
	return ErrFileMoved
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
		  AND (content_id IS NULL OR content_id = '') AND extra_id IS NULL
		  AND (file_path = $3 OR file_path LIKE $4 ESCAPE '\')
	`, contentID, folderID, pathPrefix, pathPrefixLike(pathPrefix))
	if err != nil {
		return 0, fmt.Errorf("updating content_id by path prefix: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// UpdateContentIDByObservedRootPath assigns one content item to all present
// files under the same observed root path in a media folder. Files an admin
// split pinned with a file-scope identity override keep their item. It returns
// the number of files relinked and the distinct content IDs they were linked
// to before, so the caller can reconcile memberships those items may have lost.
func (r *FileRepository) UpdateContentIDByObservedRootPath(ctx context.Context, folderID int, observedRootPath, contentID string) (int, []string, error) {
	// The self-join reads each row as it was before the update.
	var updated int
	var replaced []string
	if err := r.pool.QueryRow(ctx, `
		WITH relinked AS (
			UPDATE media_files mf
			SET content_id = $1, updated_at = NOW()
			FROM media_files previous
			WHERE previous.id = mf.id
			  AND mf.media_folder_id = $2
			  AND mf.observed_root_path = $3
			  AND mf.missing_since IS NULL
			  AND mf.extra_id IS NULL
			  AND (mf.content_id IS NULL OR mf.content_id <> $1)
			  AND NOT EXISTS (
				SELECT 1
				FROM media_identity_overrides o
				WHERE o.media_folder_id = mf.media_folder_id
				  AND o.scope = 'file'
				  AND o.file_path = mf.file_path
			  )
			RETURNING previous.content_id
		)
		SELECT COUNT(*)::int,
		       COALESCE(array_agg(DISTINCT content_id) FILTER (WHERE content_id IS NOT NULL AND content_id <> ''), ARRAY[]::text[])
		FROM relinked
	`, contentID, folderID, observedRootPath).Scan(&updated, &replaced); err != nil {
		return 0, nil, fmt.Errorf("updating content_id by observed root path: %w", err)
	}
	return updated, replaced, nil
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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin clearing media file content links transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var cleared int
	var oldEpisodeIDs []string
	var affectedSeriesIDs []string
	err = tx.QueryRow(ctx, `
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
		)
		SELECT COUNT(*)::int,
		       COALESCE(
			       array_agg(DISTINCT p.old_episode_id) FILTER (WHERE p.old_episode_id IS NOT NULL),
			       ARRAY[]::text[]
		       ),
		       COALESCE(
			       array_agg(DISTINCT e.series_id) FILTER (WHERE e.series_id IS NOT NULL),
			       ARRAY[]::text[]
		       )
		FROM cleared c
		JOIN previous p ON p.id = c.id
		LEFT JOIN episodes e ON e.content_id = p.old_episode_id
	`, folderID, pathPrefix, pathPrefixLike(pathPrefix)).Scan(&cleared, &oldEpisodeIDs, &affectedSeriesIDs)
	if err != nil {
		return 0, fmt.Errorf("clearing media file content links by path prefix: %w", err)
	}
	if len(oldEpisodeIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			DELETE FROM episode_libraries el
			WHERE el.media_folder_id = $1
			  AND el.episode_id IN `+catalog.SpanReachSQL("$2::text[]")+`
			  AND `+catalog.NoServingEpisodeFileSQL+`
		`, folderID, oldEpisodeIDs); err != nil {
			return 0, fmt.Errorf("deleting stale episode library links by path prefix: %w", err)
		}
		if err := catalog.RecomputeSeriesEpisodeMembershipDenorms(ctx, tx, affectedSeriesIDs); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit clearing media file content links transaction: %w", err)
	}
	return cleared, nil
}

// UpdateEpisodeLink sets the episode linkage fields on a media file. Like
// UpdateContentID it applies only while the row still has filePath, the path
// the caller read it at, and returns ErrFileMoved otherwise: the episode was
// chosen from that path's numbers within the series the file was linked to.
func (r *FileRepository) UpdateEpisodeLink(ctx context.Context, fileID int, filePath, episodeID string, seasonNum, episodeNum int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin episode link transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var folderID int
	var oldEpisodeID *string
	var currentPath string
	if err := tx.QueryRow(ctx, `
		SELECT media_folder_id, episode_id, file_path
		FROM media_files
		WHERE id = $1
		FOR UPDATE
	`, fileID).Scan(&folderID, &oldEpisodeID, &currentPath); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("loading existing episode link: %w", err)
	}
	if currentPath != filePath {
		return ErrFileMoved
	}

	// The INSERT is the top-level statement: a data-modifying CTE can only be
	// referenced by later query parts if it has a RETURNING clause, so a
	// trailing "SELECT COUNT(*) FROM inserted" is invalid PostgreSQL and made
	// this statement error on every call.
	if _, err := tx.Exec(ctx, `
		WITH updated AS (
			UPDATE media_files
			SET episode_id = $1,
				season_number = $2,
				episode_number = $3,
				updated_at = NOW()
			WHERE id = $4
			RETURNING episode_id, media_folder_id, created_at, missing_since, first_seen_scan_run_id
		)
		INSERT INTO episode_libraries (
			episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id
		)
		SELECT episode_id, media_folder_id, created_at, first_seen_scan_run_id
		FROM updated
		WHERE episode_id IS NOT NULL
		  AND missing_since IS NULL
		ON CONFLICT (episode_id, media_folder_id) DO NOTHING
	`, episodeID, seasonNum, episodeNum, fileID); err != nil {
		return fmt.Errorf("updating episode link: %w", err)
	}

	affectedEpisodeIDs := []string{episodeID}
	if oldEpisodeID != nil && *oldEpisodeID != episodeID {
		affectedEpisodeIDs = append(affectedEpisodeIDs, *oldEpisodeID)
		if _, err := tx.Exec(ctx, `
			DELETE FROM episode_libraries el
			WHERE el.media_folder_id = $1
			  AND el.episode_id IN `+catalog.SpanReachSQL("ARRAY[$2]::text[]")+`
			  AND `+catalog.NoServingEpisodeFileSQL+`
		`, folderID, *oldEpisodeID); err != nil {
			return fmt.Errorf("deleting old episode library link: %w", err)
		}
	}

	var affectedSeriesIDs []string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(array_agg(DISTINCT series_id), ARRAY[]::text[])
		FROM episodes
		WHERE content_id = ANY($1::text[])
	`, affectedEpisodeIDs).Scan(&affectedSeriesIDs); err != nil {
		return fmt.Errorf("collecting affected episode series: %w", err)
	}
	spannedSeriesIDs, err := catalog.InsertSpannedEpisodeMemberships(ctx, tx, "mf.id = $1", fileID)
	if err != nil {
		return err
	}
	affectedSeriesIDs = append(affectedSeriesIDs, spannedSeriesIDs...)
	if err := catalog.RecomputeSeriesEpisodeMembershipDenorms(ctx, tx, affectedSeriesIDs); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit episode link transaction: %w", err)
	}
	return nil
}

// BulkLinkEpisodesBySeries links all already-numbered files for a series to
// matching episode rows in one statement. Files that still lack persisted
// season/episode hints remain unlinked for the slower fallback path.
func (r *FileRepository) BulkLinkEpisodesBySeries(ctx context.Context, seriesContentID string) (int, error) {
	var linked int
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		// Lock the candidates in id order first, the order curated rebind,
		// rename and split lock a series' files; the UPDATE alone locks them
		// in plan order and can deadlock with those.
		var addedLinks bool
		if err := tx.QueryRow(ctx, `
		WITH locked AS (
			SELECT id FROM media_files
			WHERE content_id = $1
			  AND episode_id IS NULL
			  AND missing_since IS NULL
			  AND season_number IS NOT NULL
			  AND episode_number IS NOT NULL
			ORDER BY id
			FOR UPDATE
		),
		updated AS (
			UPDATE media_files mf
			SET episode_id = e.content_id,
				season_number = e.season_number,
				episode_number = e.episode_number,
				updated_at = NOW()
			FROM episodes e
			WHERE mf.id IN (SELECT id FROM locked)
			  AND mf.content_id = $1
			  AND mf.episode_id IS NULL
			  AND mf.missing_since IS NULL
			  AND mf.season_number IS NOT NULL
			  AND mf.episode_number IS NOT NULL
			  AND e.series_id = $1
			  AND mf.season_number = e.season_number
			  AND mf.episode_number = e.episode_number
			RETURNING mf.id, mf.episode_id, mf.media_folder_id, mf.created_at, mf.first_seen_scan_run_id
		),
		inserted AS (
			INSERT INTO episode_libraries (
				episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id
			)
			SELECT episode_id,
			       media_folder_id,
			       MIN(created_at),
			       (array_agg(first_seen_scan_run_id ORDER BY created_at ASC, id ASC))[1]
			FROM updated
			GROUP BY episode_id, media_folder_id
			ON CONFLICT (episode_id, media_folder_id) DO NOTHING
			RETURNING episode_id
		)
		SELECT (SELECT COUNT(*) FROM updated), EXISTS (SELECT 1 FROM inserted)
	`, seriesContentID).Scan(&linked, &addedLinks); err != nil {
			return err
		}
		spannedSeriesIDs, err := catalog.InsertSpannedEpisodeMemberships(ctx, tx, "mf.content_id = $1", seriesContentID)
		if err != nil {
			return err
		}
		if !addedLinks && len(spannedSeriesIDs) == 0 {
			return nil
		}
		// Genuinely new links move the series' membership denorms ("Latest
		// Episodes", issue #202; "Latest Episode Air Date", issue #2741).
		// Recompute them in a later statement: one that waited on a
		// concurrent air-date correction's series lock would still apply
		// this statement's older episode dates.
		return catalog.RecomputeSeriesEpisodeMembershipDenorms(ctx, tx, []string{seriesContentID})
	})
	if err != nil {
		return 0, fmt.Errorf("bulk-linking series files to episodes: %w", err)
	}
	return linked, nil
}

// FindContentIDByRootPath finds an existing linked content item for files under
// the same recognized root path within a media folder. If preferredType is
// set, matches of that type sort first.
func (r *FileRepository) FindContentIDByRootPath(ctx context.Context, folderID int, rootPath, preferredType string) (string, error) {
	return findContentIDByRootPath(ctx, r.pool, folderID, rootPath, preferredType)
}

// FindContentIDByRootPathTx is FindContentIDByRootPath within the caller's
// transaction.
func (r *FileRepository) FindContentIDByRootPathTx(ctx context.Context, tx pgx.Tx, folderID int, rootPath, preferredType string) (string, error) {
	return findContentIDByRootPath(ctx, tx, folderID, rootPath, preferredType)
}

func findContentIDByRootPath(ctx context.Context, queryer fileQueryer, folderID int, rootPath, preferredType string) (string, error) {
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
	err := queryer.QueryRow(ctx, query, args...).Scan(&contentID)
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
	return r.findContentIDByObservedRootPath(ctx, folderID, observedRootPath, preferredType, 0)
}

// FindOtherContentIDByObservedRootPath is FindContentIDByObservedRootPath
// over every file but excludeFileID, the lookup matching makes for a file
// that is not linked yet.
func (r *FileRepository) FindOtherContentIDByObservedRootPath(ctx context.Context, folderID int, observedRootPath, preferredType string, excludeFileID int) (string, error) {
	return r.findContentIDByObservedRootPath(ctx, folderID, observedRootPath, preferredType, excludeFileID)
}

func (r *FileRepository) findContentIDByObservedRootPath(ctx context.Context, folderID int, observedRootPath, preferredType string, excludeFileID int) (string, error) {
	query := `SELECT mf.content_id
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND mf.observed_root_path = $2
		  AND mf.content_id IS NOT NULL
		  AND mf.missing_since IS NULL
		  AND mf.id <> $3
		ORDER BY `

	args := []any{folderID, observedRootPath, excludeFileID}
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

// GetByEpisodeID returns all media files serving the given episode ID: linked
// to it, or a multi-episode file spanning it.
func (r *FileRepository) GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error) {
	files, err := r.listFilesByEpisodeID(ctx, episodeID)
	if err != nil {
		return nil, err
	}
	aliasFiles, err := r.listAliasFilesByCanonicalEpisodeID(ctx, episodeID)
	if err != nil {
		return nil, err
	}
	return mergeEpisodeFiles(files, aliasFiles), nil
}

// listFilesByEpisodeID is the direct media_files lookup behind
// GetByEpisodeID.
func (r *FileRepository) listFilesByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE id IN (SELECT served.file_id FROM (` + catalog.ServedEpisodeFilesSQL + `) served(episode_id, file_id))
		ORDER BY CASE WHEN episode_id = ANY($1::text[]) THEN 0 ELSE 1 END ASC, id ASC`
	rows, err := r.pool.Query(ctx, query, []string{episodeID})
	if err != nil {
		return nil, fmt.Errorf("querying files by episode_id: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// listAliasFilesByCanonicalEpisodeID returns the present files owned by the
// alias rows of one canonical episode, ordered by file ID.
func (r *FileRepository) listAliasFilesByCanonicalEpisodeID(ctx context.Context, canonicalEpisodeID string) ([]*models.MediaFile, error) {
	query := `SELECT ` + fileColumns + ` FROM media_files
		WHERE episode_id IN (
			SELECT alias_episode_id
			FROM episode_aliases
			WHERE canonical_episode_id = $1
			  AND alias_episode_id IS NOT NULL
		)
		  AND missing_since IS NULL
		ORDER BY id ASC`
	rows, err := r.pool.Query(ctx, query, canonicalEpisodeID)
	if err != nil {
		return nil, fmt.Errorf("querying alias files by canonical episode_id: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// FirstDurationsByEpisodeIDs returns the probed duration (seconds) of the
// first live file backing each episode id, using the same "first file with
// duration > 0, ordered by id" rule as the v1 API's contentDurationSeconds.
// Ids with no live probed file are absent from the map. Resolution is
// intentionally not access-scoped; callers have already filtered the items.
func (r *FileRepository) FirstDurationsByEpisodeIDs(ctx context.Context, episodeIDs []string) (map[string]int, error) {
	result := make(map[string]int)
	if len(episodeIDs) == 0 {
		return result, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (episode_id) episode_id, duration
		FROM media_files
		WHERE episode_id = ANY($1)
		  AND missing_since IS NULL
		  AND duration > 0
		ORDER BY episode_id, id ASC`, episodeIDs)
	if err != nil {
		return nil, fmt.Errorf("querying first durations by episode ids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var episodeID string
		var duration int
		if err := rows.Scan(&episodeID, &duration); err != nil {
			return nil, fmt.Errorf("scanning first duration by episode id: %w", err)
		}
		result[episodeID] = duration
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating first durations by episode ids: %w", err)
	}
	return result, nil
}

// ListMissingChapterThumbnails returns present media files in enabled,
// opted-in libraries that either have no chapter probe data yet or still have
// chapters missing thumbnail assets. A thumbnail whose path does not end in
// currentSuffix was made at another width and counts as missing.
func (r *FileRepository) ListMissingChapterThumbnails(ctx context.Context, limit int, currentSuffix string) ([]*models.MediaFile, error) {
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
            -- Count only markers models.EffectiveMarkerThumbnails can preview;
            -- others never write state and would be listed every sweep.
            OR (EXISTS (
                    SELECT 1 FROM (
                        SELECT segment->>'kind' AS kind, (segment->>'start_seconds')::float8 AS start_seconds, (segment->>'end_seconds')::float8 AS end_seconds
                        FROM jsonb_array_elements(mf.marker_segments) AS segment
                        UNION ALL VALUES ('intro', mf.intro_start, mf.intro_end), ('credits', mf.credits_start, mf.credits_end),
                            ('recap', mf.recap_start, mf.recap_end), ('preview', mf.preview_start, mf.preview_end)
                    ) AS marker
                    WHERE marker.kind IN ('intro', 'credits', 'recap', 'preview')
                      AND mf.duration > 0 AND marker.start_seconds >= 0 AND marker.start_seconds < mf.duration + 1
                      AND marker.end_seconds > marker.start_seconds AND marker.end_seconds <= mf.duration + 1
                )
                AND (jsonb_array_length(mf.marker_thumbnails) = 0 OR EXISTS (
                    SELECT 1 FROM jsonb_array_elements(mf.marker_thumbnails) AS marker
                    WHERE right(COALESCE(marker->>'thumbnail_path', ''), length($2)) <> $2
                    AND (COALESCE(marker->>'thumbnail_retry_after', '') = '' OR (marker->>'thumbnail_retry_after')::timestamptz <= NOW())
                )))
            OR (
				jsonb_typeof(mf.chapters) = 'array'
				AND jsonb_array_length(mf.chapters) > 0
				AND EXISTS (
					SELECT 1
					FROM jsonb_array_elements(mf.chapters) AS chapter
					WHERE (
						COALESCE(chapter->>'thumbnail_path', '') = ''
						OR right(chapter->>'thumbnail_path', length($2)) <> $2
					  )
					  AND (
						COALESCE(chapter->>'thumbnail_retry_after', '') = ''
						OR (chapter->>'thumbnail_retry_after')::timestamptz <= NOW()
					  )
				)
			)
		  )
		ORDER BY mf.probe_updated_at ASC NULLS FIRST, mf.id ASC
		LIMIT $1`
	rows, err := r.pool.Query(ctx, query, limit, currentSuffix)
	if err != nil {
		return nil, fmt.Errorf("querying files missing chapter thumbnails: %w", err)
	}
	defer rows.Close()

	return scanMediaFiles(rows)
}

// chapterHDRFileSQL matches files whose chapter frames need HDR tone
// mapping, as tonemap.NeedsToneMap decides: flagged HDR or a Dolby Vision
// video track.
const chapterHDRFileSQL = `(mf.hdr OR EXISTS (
	SELECT 1 FROM jsonb_array_elements(
		CASE WHEN jsonb_typeof(mf.video_tracks) = 'array' THEN mf.video_tracks ELSE '[]'::jsonb END
	) AS track
	WHERE btrim(COALESCE(track->>'dolby_vision', '')) <> ''))`

// ListChapterThumbnailsAtOtherWidths pages chapters without the current image
// width by file ID. A zero retry time means complete; otherwise the final page
// returns the earliest time any stale image can become retryable. Missing
// images stay pending until an in-flight first extraction reaches this width.
// skipHDR leaves out files that need tone mapping, which chapter extraction
// skips while the HDR policy is disabled.
func (r *FileRepository) ListChapterThumbnailsAtOtherWidths(ctx context.Context, limit int, currentSuffix string, afterID int, skipHDR bool) ([]*models.MediaFile, time.Time, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+mfFileColumns+` FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		WHERE mf.id > $3
		  AND mf.missing_since IS NULL
		  AND folders.enabled = true
		  AND folders.chapter_thumbnails_enabled = true
		  AND (mf.chapter_thumbnail_retry_after IS NULL OR mf.chapter_thumbnail_retry_after <= NOW())
		  AND NOT ($4::boolean AND `+chapterHDRFileSQL+`)
		  AND EXISTS (
			SELECT 1 FROM jsonb_array_elements(
				(CASE WHEN jsonb_typeof(mf.chapters) = 'array' THEN mf.chapters ELSE '[]'::jsonb END) || mf.marker_thumbnails
			) AS chapter
			WHERE right(COALESCE(chapter->>'thumbnail_path', ''), length($2)) <> $2
			  AND (COALESCE(chapter->>'thumbnail_retry_after', '') = ''
			       OR (chapter->>'thumbnail_retry_after')::timestamptz <= NOW())
		  )
		ORDER BY mf.id
		LIMIT $1`, limit, currentSuffix, afterID, skipHDR)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("querying chapter thumbnails at other widths: %w", err)
	}
	files, err := scanMediaFiles(rows)
	rows.Close()
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(files) > 0 {
		// The page already proves work is eligible now. Do not expand the
		// catalog again to compute cooldowns while these requests are pending.
		return files, time.Now(), nil
	}
	// File and chapter cooldowns both apply, so each stale image becomes
	// eligible at their later deadline. The earliest such deadline schedules
	// the next scan without repeatedly expanding JSON during the cooldown.
	var nextRetry *time.Time
	err = r.pool.QueryRow(ctx, `SELECT min(GREATEST(
		COALESCE(mf.chapter_thumbnail_retry_after, NOW()),
		COALESCE(NULLIF(chapter->>'thumbnail_retry_after', '')::timestamptz, NOW())
	))
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		CROSS JOIN LATERAL jsonb_array_elements(
			(CASE WHEN jsonb_typeof(mf.chapters) = 'array' THEN mf.chapters ELSE '[]'::jsonb END) || mf.marker_thumbnails
		) AS chapter
		WHERE mf.missing_since IS NULL
		  AND folders.enabled = true
		  AND folders.chapter_thumbnails_enabled = true
		  AND NOT ($2::boolean AND `+chapterHDRFileSQL+`)
		  AND right(COALESCE(chapter->>'thumbnail_path', ''), length($1)) <> $1`, currentSuffix, skipHDR).Scan(&nextRetry)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("checking remaining chapter thumbnail widths: %w", err)
	}
	if nextRetry == nil {
		return files, time.Time{}, nil
	}
	return files, *nextRetry, nil
}

// ChapterThumbnailLibraryKey fingerprints only the eligible library IDs. Once
// a width backfill completes, polling this small table avoids expanding every
// media file's chapter JSON while still noticing library enable and opt-in edits.
func (r *FileRepository) ChapterThumbnailLibraryKey(ctx context.Context) (string, error) {
	var key string
	err := r.pool.QueryRow(ctx, `SELECT md5(COALESCE(string_agg(id::text, ',' ORDER BY id), ''))
		FROM media_folders WHERE enabled AND chapter_thumbnails_enabled`).Scan(&key)
	if err != nil {
		return "", fmt.Errorf("checking chapter thumbnail library eligibility: %w", err)
	}
	return key, nil
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

// rootCoverageClauses builds one SQL predicate per root matching file_path
// rows that live at or under that root; see pathscope.CoverageClauses.
func rootCoverageClauses(roots []string, firstArg int) ([]string, []any) {
	return pathscope.CoverageClauses("file_path", roots, firstArg)
}

// UpdateMultiplePPS records the H.264 multi-PPS copy-safety verdict together
// with the size and mtime it was computed from, so a later read can tell
// whether the file has been rewritten since. A nil scanMtime records a verdict
// for a row that has no file mtime; reading it back validates on size alone.
//
// It deliberately does not go through Upsert: that path also clears
// match_suppressed_at and missing_since, which a copy-safety scan has no
// business touching.
//
// The write is conditional on the row still holding the generation that was
// scanned. A scan reads the opening seconds of a file over storage that can be
// slow, so an old-generation scan finishing late would otherwise stamp its
// verdict — and its stale size and mtime — over the replacement generation's,
// re-validating a verdict for bytes that are gone and condemning (or clearing
// the condemnation of) a file nobody scanned. A superseded write reports
// ErrStaleCopySafetyScan rather than succeeding silently, because the caller
// must also refrain from notifying live sessions on the strength of it.
//
// Both sides of the mtime predicate are normalized to microseconds, exactly as
// MediaFile.PersistedVideoCopyVerdict normalizes them when it reads the verdict
// back: Postgres stores timestamptz at microsecond resolution while a
// filesystem mtime carries nanoseconds, so comparing the raw values would make
// every write for a row whose mtime came from a stat call fail.
func (r *FileRepository) UpdateMultiplePPS(ctx context.Context, fileID int, multiplePPS bool, scanSize int64, scanMtime *time.Time) error {
	var normalizedMtime *time.Time
	if scanMtime != nil {
		normalized := models.NormalizeFileModifiedAt(*scanMtime)
		normalizedMtime = &normalized
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_files
		SET multiple_pps = $2,
		    multiple_pps_scan_size = $3,
		    multiple_pps_scan_mtime = $4,
		    updated_at = NOW()
		WHERE id = $1
		  AND file_size = $3
		  AND date_trunc('microseconds', file_modified_at) IS NOT DISTINCT FROM $4::timestamptz`,
		fileID,
		multiplePPS,
		scanSize,
		normalizedMtime,
	)
	if err != nil {
		return fmt.Errorf("updating multiple pps verdict: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// The row is gone, or it no longer carries the size and mtime that were
		// scanned. Both mean the same thing to every caller: this verdict does
		// not describe the file as it stands.
		return ErrStaleCopySafetyScan
	}
	return nil
}
