package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

// scanStateFile is the lightweight media_files row shape used by library scans.
// It intentionally excludes large JSON payloads like tracks and chapters.
type scanStateFile struct {
	ID                    int
	ContentID             string
	ExtraID               string
	CanonicalRootPath     string
	ObservedRootPath      string
	ContentGroupKey       string
	GroupKeyVersion       int
	BaseTitle             string
	BaseYear              int
	BaseType              string
	IdentityConfidence    string
	IdentityJSON          []byte
	FilePath              string
	FileSize              int64
	FileModifiedAt        *time.Time
	CodecVideo            string
	CodecAudio            string
	Resolution            string
	Container             string
	Duration              int
	EditionRaw            string
	EditionKey            string
	EditionConfidence     *float64
	EditionSource         string
	PresentationKind      string
	PresentationGroupKey  string
	PresentationPartIndex int
	MultiEpisodeStart     int
	MultiEpisodeEnd       int
	ProbeSource           string
	ProbeUpdatedAt        *time.Time
	MissingSince          *time.Time
	HasVideoTracks        bool
	HasAudioTracks        bool
	HasChapters           bool
	ExternalSubtitlePaths []string
}

const scanStateColumns = `id, content_id, extra_id,
	canonical_root_path, observed_root_path, content_group_key, group_key_version,
	base_title, base_year, base_type, identity_confidence, identity_json,
	file_path, file_size, file_modified_at,
	codec_video, codec_audio, resolution, container, duration,
	edition_raw, edition_key, edition_confidence, edition_source,
	presentation_kind, presentation_group_key, presentation_part_index,
	multi_episode_start, multi_episode_end,
	probe_source, probe_updated_at, missing_since,
	COALESCE(jsonb_typeof(video_tracks) = 'array' AND jsonb_array_length(video_tracks) > 0, FALSE) AS has_video_tracks,
	COALESCE(jsonb_typeof(audio_tracks) = 'array' AND jsonb_array_length(audio_tracks) > 0, FALSE) AS has_audio_tracks,
	chapters IS NOT NULL AS has_chapters,
	COALESCE((
		SELECT jsonb_agg(path)
		FROM (
			SELECT elem->>'path' AS path
			FROM jsonb_array_elements(COALESCE(external_subtitles, '[]'::jsonb)) AS t(elem)
			WHERE btrim(elem->>'path') <> ''
		) subtitle_paths
	), '[]'::jsonb) AS external_subtitle_paths`

func scanScanStateRow(row pgx.Row) (*scanStateFile, error) {
	var state scanStateFile
	var contentID *string
	var extraID *string
	var canonicalRootPath, observedRootPath, contentGroupKey, baseTitle, baseType *string
	var groupKeyVersion, baseYear *int
	var identityConfidence *string
	var identityJSON []byte
	var fileModifiedAt *time.Time
	var codecVideo, codecAudio, resolution, container *string
	var duration *int
	var editionRaw, editionKey, editionSource *string
	var presentationKind, presentationGroupKey *string
	var presentationPartIndex, multiEpisodeStart, multiEpisodeEnd *int
	var probeSource *string
	var externalSubtitlePathsJSON []byte

	if err := row.Scan(
		&state.ID,
		&contentID,
		&extraID,
		&canonicalRootPath,
		&observedRootPath,
		&contentGroupKey,
		&groupKeyVersion,
		&baseTitle,
		&baseYear,
		&baseType,
		&identityConfidence,
		&identityJSON,
		&state.FilePath,
		&state.FileSize,
		&fileModifiedAt,
		&codecVideo,
		&codecAudio,
		&resolution,
		&container,
		&duration,
		&editionRaw,
		&editionKey,
		&state.EditionConfidence,
		&editionSource,
		&presentationKind,
		&presentationGroupKey,
		&presentationPartIndex,
		&multiEpisodeStart,
		&multiEpisodeEnd,
		&probeSource,
		&state.ProbeUpdatedAt,
		&state.MissingSince,
		&state.HasVideoTracks,
		&state.HasAudioTracks,
		&state.HasChapters,
		&externalSubtitlePathsJSON,
	); err != nil {
		return nil, fmt.Errorf("scanning scan state row: %w", err)
	}

	if contentID != nil {
		state.ContentID = *contentID
	}
	if extraID != nil {
		state.ExtraID = *extraID
	}
	if canonicalRootPath != nil {
		state.CanonicalRootPath = *canonicalRootPath
	}
	if observedRootPath != nil {
		state.ObservedRootPath = *observedRootPath
	}
	if contentGroupKey != nil {
		state.ContentGroupKey = *contentGroupKey
	}
	if groupKeyVersion != nil {
		state.GroupKeyVersion = *groupKeyVersion
	}
	if baseTitle != nil {
		state.BaseTitle = *baseTitle
	}
	if baseYear != nil {
		state.BaseYear = *baseYear
	}
	if baseType != nil {
		state.BaseType = *baseType
	}
	if identityConfidence != nil {
		state.IdentityConfidence = *identityConfidence
	}
	if len(identityJSON) > 0 {
		state.IdentityJSON = append([]byte(nil), identityJSON...)
	}
	state.FileModifiedAt = fileModifiedAt
	if codecVideo != nil {
		state.CodecVideo = *codecVideo
	}
	if codecAudio != nil {
		state.CodecAudio = *codecAudio
	}
	if resolution != nil {
		state.Resolution = *resolution
	}
	if container != nil {
		state.Container = *container
	}
	if duration != nil {
		state.Duration = *duration
	}
	if editionRaw != nil {
		state.EditionRaw = *editionRaw
	}
	if editionKey != nil {
		state.EditionKey = *editionKey
	}
	if editionSource != nil {
		state.EditionSource = *editionSource
	}
	if presentationKind != nil {
		state.PresentationKind = *presentationKind
	}
	if presentationGroupKey != nil {
		state.PresentationGroupKey = *presentationGroupKey
	}
	if presentationPartIndex != nil {
		state.PresentationPartIndex = *presentationPartIndex
	}
	if multiEpisodeStart != nil {
		state.MultiEpisodeStart = *multiEpisodeStart
	}
	if multiEpisodeEnd != nil {
		state.MultiEpisodeEnd = *multiEpisodeEnd
	}
	if probeSource != nil {
		state.ProbeSource = *probeSource
	}
	if len(externalSubtitlePathsJSON) > 0 {
		if err := json.Unmarshal(externalSubtitlePathsJSON, &state.ExternalSubtitlePaths); err != nil {
			return nil, fmt.Errorf("unmarshaling external subtitle paths: %w", err)
		}
	}

	return &state, nil
}

func scanScanStateRows(rows pgx.Rows) ([]*scanStateFile, error) {
	defer rows.Close()

	files := make([]*scanStateFile, 0)
	for rows.Next() {
		file, err := scanScanStateRow(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating scan state rows: %w", err)
	}
	return files, nil
}

// GetScanStateByFolder returns the lightweight scan-state rows for a folder.
func (r *FileRepository) GetScanStateByFolder(ctx context.Context, folderID int) ([]*scanStateFile, error) {
	query := `SELECT ` + scanStateColumns + ` FROM media_files WHERE media_folder_id = $1 ORDER BY file_path ASC`
	rows, err := r.pool.Query(ctx, query, folderID)
	if err != nil {
		return nil, fmt.Errorf("querying scan state by folder: %w", err)
	}
	return scanScanStateRows(rows)
}

// GetScanStateByFolderAndPathPrefix returns lightweight scan-state rows for a
// folder subtree.
func (r *FileRepository) GetScanStateByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string) ([]*scanStateFile, error) {
	query := `SELECT ` + scanStateColumns + ` FROM media_files
		WHERE media_folder_id = $1
		  AND (file_path = $2 OR file_path LIKE $3 ESCAPE '\')
		ORDER BY file_path ASC`
	rows, err := r.pool.Query(ctx, query, folderID, pathPrefix, pathPrefixLike(pathPrefix))
	if err != nil {
		return nil, fmt.Errorf("querying scan state by folder and path prefix: %w", err)
	}
	return scanScanStateRows(rows)
}

func scanStateFromMediaFile(file *models.MediaFile) *scanStateFile {
	if file == nil {
		return nil
	}
	return &scanStateFile{
		ID:                    file.ID,
		ContentID:             file.ContentID,
		ExtraID:               file.ExtraID,
		CanonicalRootPath:     file.CanonicalRootPath,
		ObservedRootPath:      file.ObservedRootPath,
		ContentGroupKey:       file.ContentGroupKey,
		GroupKeyVersion:       file.GroupKeyVersion,
		BaseTitle:             file.BaseTitle,
		BaseYear:              file.BaseYear,
		BaseType:              file.BaseType,
		IdentityConfidence:    file.IdentityConfidence,
		IdentityJSON:          append([]byte(nil), file.IdentityJSON...),
		FilePath:              file.FilePath,
		FileSize:              file.FileSize,
		FileModifiedAt:        file.FileModifiedAt,
		CodecVideo:            file.CodecVideo,
		CodecAudio:            file.CodecAudio,
		Resolution:            file.Resolution,
		Container:             file.Container,
		Duration:              file.Duration,
		EditionRaw:            file.EditionRaw,
		EditionKey:            file.EditionKey,
		EditionConfidence:     file.EditionConfidence,
		EditionSource:         file.EditionSource,
		PresentationKind:      file.PresentationKind,
		PresentationGroupKey:  file.PresentationGroupKey,
		PresentationPartIndex: file.PresentationPartIndex,
		MultiEpisodeStart:     file.MultiEpisodeStart,
		MultiEpisodeEnd:       file.MultiEpisodeEnd,
		ProbeSource:           file.ProbeSource,
		ProbeUpdatedAt:        file.ProbeUpdatedAt,
		MissingSince:          file.MissingSince,
		HasVideoTracks:        len(file.VideoTracks) > 0,
		HasAudioTracks:        len(file.AudioTracks) > 0,
		HasChapters:           file.Chapters != nil,
		ExternalSubtitlePaths: externalSubtitlePaths(file.ExternalSubtitles),
	}
}

func externalSubtitlePaths(subs []models.ExternalSubtitle) []string {
	if len(subs) == 0 {
		return nil
	}
	paths := make([]string, 0, len(subs))
	for _, sub := range subs {
		if sub.Path != "" {
			paths = append(paths, sub.Path)
		}
	}
	return paths
}
