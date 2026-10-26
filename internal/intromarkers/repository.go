package intromarkers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type Repository struct {
	pool      *pgxpool.Pool
	artifacts *mediaartifact.Store
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, artifacts: mediaartifact.NewStore(pool)}
}

// ErrMarkerItemNotFound reports an item that is neither an episode nor a
// movie, the items local marker analysis works on.
var ErrMarkerItemNotFound = errors.New("item is not an episode or a movie")

// Item kinds local marker analysis works on.
const (
	MarkerItemEpisode = "episode"
	MarkerItemMovie   = "movie"
)

// MarkerItemEligibility is whether an item's files can be analyzed locally.
type MarkerItemEligibility struct {
	ItemID string
	// Kind is MarkerItemEpisode or MarkerItemMovie. Movies get credits only.
	Kind                  string
	HasMediaFiles         bool
	IntroDetectionEnabled bool
}

const baseCandidateSelect = baseCandidateSelectFrom + baseCandidateWhere

// baseCandidateSelectFrom and baseCandidateWhere are split so a query can add
// joins between them.
const baseCandidateSelectFrom = `
	SELECT mf.id,
	       mf.episode_id,
	       e.season_id,` + candidateFileColumns + `
	FROM media_files mf
	JOIN media_folders folders ON folders.id = mf.media_folder_id
	JOIN episodes e ON e.content_id = mf.episode_id`

// candidateFileColumns are the candidate columns after the file ID, episode
// ID, and season ID, in scanCandidates order.
const candidateFileColumns = `
	       mf.media_folder_id,
	       mf.file_path,
	       COALESCE(mf.file_hash, ''),
	       COALESCE(mf.file_size, 0),
	       COALESCE(mf.duration, 0),
	       COALESCE(mf.presentation_group_key, ''),
	       COALESCE(mf.edition_key, ''),
	       mf.chapters,
	       mf.audio_tracks,
	       mf.subtitle_tracks,
	       mf.external_subtitles,
	       mf.intro_start,
	       mf.intro_end,
	       mf.intro_markers_source,
	       mf.intro_markers_confidence,
	       mf.intro_markers_algorithm,
	       mf.credits_start,
	       mf.credits_end,
	       mf.credits_markers_source,
	       mf.credits_markers_confidence,
	       mf.credits_markers_algorithm,
	       mf.preview_start,
	       mf.markers_source,
	       COALESCE(mf.content_id, ''),
	       COALESCE(mf.extra_id, ''),
	       COALESCE(mf.season_number, 0),
	       COALESCE(mf.episode_number, 0),
	       mf.file_modified_at,
	       COALESCE(mf.codec_video, ''),
	       COALESCE(mf.codec_audio, ''),
	       mf.video_tracks->0`

const baseCandidateWhere = `
	WHERE mf.episode_id IS NOT NULL
	  AND COALESCE(e.season_id, '') <> ''
	  AND folders.enabled = true
	  AND folders.intro_detection_enabled = true
	  AND folders.type IN ('series', 'mixed')
	  AND mf.missing_since IS NULL
	  AND COALESCE(mf.duration, 0) >= 300
	  AND COALESCE(mf.multi_episode_start, 0) = 0
	  AND COALESCE(mf.multi_episode_end, 0) = 0
	  AND COALESCE(mf.presentation_part_total, 1) <= 1`

func (r *Repository) ListEligibleCandidates(ctx context.Context) ([]Candidate, error) {
	rows, err := r.pool.Query(ctx, baseCandidateSelect+`
		ORDER BY mf.media_folder_id, e.season_id, mf.episode_id, mf.id`)
	if err != nil {
		return nil, fmt.Errorf("listing intro marker candidates: %w", err)
	}
	return scanCandidates(rows)
}

func (r *Repository) ListCandidatesForEpisode(ctx context.Context, episodeID string) ([]Candidate, error) {
	rows, err := r.pool.Query(ctx, baseCandidateSelect+`
		  AND mf.episode_id = $1
		ORDER BY mf.media_folder_id, e.season_id, mf.episode_id, mf.id`, episodeID)
	if err != nil {
		return nil, fmt.Errorf("listing intro marker candidates for episode %s: %w", episodeID, err)
	}
	return scanCandidates(rows)
}

func (r *Repository) ListCandidatesForGroup(ctx context.Context, mediaFolderID int, seasonID, analysisGroupKey string) ([]Candidate, error) {
	rows, err := r.pool.Query(ctx, baseCandidateSelect+`
		  AND mf.media_folder_id = $1
		  AND e.season_id = $2
		ORDER BY mf.media_folder_id, e.season_id, mf.episode_id, mf.id`, mediaFolderID, seasonID)
	if err != nil {
		return nil, fmt.Errorf("listing intro marker candidates for season group %s: %w", analysisGroupKey, err)
	}
	candidates, err := scanCandidates(rows)
	if err != nil {
		return nil, err
	}
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if candidate.AnalysisGroupKey() == analysisGroupKey {
			filtered = append(filtered, candidate)
		}
	}
	return filtered, nil
}

// ListChapterSilenceBackfillCandidates skips a file while its recorded attempt
// still matches the file identity, its chapters, the refined marker range, and
// the silence settings: indefinitely after a clean no-improvement result, and
// until retry_after after a failure recorded by this server. A failure recorded
// by another server does not defer this one, since the cause may be local to
// that server. Files never attempted come first, so retries cannot crowd them
// out of the per-run budget.
//
// The attempt lookup is a LEFT JOIN so it runs as a per-file primary-key probe
// inside the parallel scan. The planner estimates the candidate filter at a
// handful of rows; as NOT EXISTS it either chose an anti-join that rescans the
// attempts table once per candidate or lost the parallel scan.
func (r *Repository) ListChapterSilenceBackfillCandidates(ctx context.Context, limit int, cfg Config, node string) ([]Candidate, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, baseCandidateSelectFrom+`
		LEFT JOIN intro_silence_refinement_attempts attempts ON attempts.media_file_id = mf.id`+
		baseCandidateWhere+`
		  AND mf.intro_start IS NOT NULL
		  AND mf.intro_end IS NOT NULL
		  AND mf.intro_markers_source = $1
		  AND mf.intro_markers_algorithm = ANY($2::text[])
		  AND NOT COALESCE(
		      attempts.config_hash = $3
		      AND attempts.file_hash = COALESCE(mf.file_hash, '')
		      AND attempts.file_size = COALESCE(mf.file_size, 0)
		      AND attempts.duration_seconds = COALESCE(mf.duration, 0)
		      AND attempts.chapters_hash = encode(sha256(convert_to(COALESCE(mf.chapters::text, ''), 'UTF8')), 'hex')
		      AND attempts.intro_start = mf.intro_start
		      AND attempts.intro_end = mf.intro_end
		      AND (attempts.status = $4 OR (attempts.retry_after > NOW() AND attempts.recorded_by = $6)),
		      false)
		ORDER BY attempts.attempted_at NULLS FIRST,
		  mf.intro_markers_detected_at NULLS FIRST,
		  mf.id
		LIMIT $5`,
		models.MarkerSourceScanner,
		[]string{ChapterAlgorithm, legacyChapterSilenceAlgorithm},
		cfg.SilenceConfigHash(),
		silenceAttemptNoImprovement,
		limit,
		node,
	)
	if err != nil {
		return nil, fmt.Errorf("listing intro marker silence backfill candidates: %w", err)
	}
	return scanCandidates(rows)
}

func (r *Repository) LoadSilenceRefinementAttempt(ctx context.Context, fileID int) (*SilenceRefinementAttempt, error) {
	var attempt SilenceRefinementAttempt
	err := r.pool.QueryRow(ctx, `
		SELECT media_file_id,
		       config_hash,
		       file_hash,
		       file_size,
		       duration_seconds,
		       chapters_hash,
		       intro_start,
		       intro_end,
		       status,
		       recorded_by,
		       failure_count,
		       COALESCE(last_error, ''),
		       attempted_at,
		       retry_after
		FROM intro_silence_refinement_attempts
		WHERE media_file_id = $1`, fileID).Scan(
		&attempt.MediaFileID,
		&attempt.ConfigHash,
		&attempt.FileHash,
		&attempt.FileSize,
		&attempt.DurationSeconds,
		&attempt.ChaptersHash,
		&attempt.IntroStart,
		&attempt.IntroEnd,
		&attempt.Status,
		&attempt.RecordedBy,
		&attempt.FailureCount,
		&attempt.LastError,
		&attempt.AttemptedAt,
		&attempt.RetryAfter,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("loading intro silence refinement attempt: %w", err)
	}
	return &attempt, nil
}

func (r *Repository) UpsertSilenceRefinementAttempt(ctx context.Context, attempt SilenceRefinementAttempt) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO intro_silence_refinement_attempts (
		    media_file_id,
		    config_hash,
		    file_hash,
		    file_size,
		    duration_seconds,
		    chapters_hash,
		    intro_start,
		    intro_end,
		    status,
		    recorded_by,
		    failure_count,
		    last_error,
		    attempted_at,
		    retry_after
		) VALUES (
		    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, ''), $13, $14
		)
		ON CONFLICT (media_file_id) DO UPDATE SET
		    config_hash = EXCLUDED.config_hash,
		    file_hash = EXCLUDED.file_hash,
		    file_size = EXCLUDED.file_size,
		    duration_seconds = EXCLUDED.duration_seconds,
		    chapters_hash = EXCLUDED.chapters_hash,
		    intro_start = EXCLUDED.intro_start,
		    intro_end = EXCLUDED.intro_end,
		    status = EXCLUDED.status,
		    recorded_by = EXCLUDED.recorded_by,
		    failure_count = EXCLUDED.failure_count,
		    last_error = EXCLUDED.last_error,
		    attempted_at = EXCLUDED.attempted_at,
		    retry_after = EXCLUDED.retry_after`,
		attempt.MediaFileID,
		attempt.ConfigHash,
		attempt.FileHash,
		attempt.FileSize,
		attempt.DurationSeconds,
		attempt.ChaptersHash,
		attempt.IntroStart,
		attempt.IntroEnd,
		attempt.Status,
		attempt.RecordedBy,
		attempt.FailureCount,
		attempt.LastError,
		attempt.AttemptedAt,
		attempt.RetryAfter,
	)
	if err != nil {
		return fmt.Errorf("upserting intro silence refinement attempt: %w", err)
	}
	return nil
}

// MarkerItemEligibility reports whether local marker analysis may run for an
// episode or a movie: the item exists, has files, and at least one of them
// is in an enabled library of a kind the analysis covers with marker
// detection on.
func (r *Repository) MarkerItemEligibility(ctx context.Context, itemID string) (*MarkerItemEligibility, error) {
	var isEpisode bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM episodes WHERE content_id = $1)`, itemID).Scan(&isEpisode); err != nil {
		return nil, fmt.Errorf("checking episode existence for marker detection: %w", err)
	}
	if isEpisode {
		return r.itemEligibility(ctx, itemID, MarkerItemEpisode, `
			SELECT COUNT(*),
			       COUNT(*) FILTER (
			           WHERE folders.enabled = true
			             AND folders.intro_detection_enabled = true
			             AND folders.type IN ('series', 'mixed')
			       )
			FROM media_files mf
			JOIN media_folders folders ON folders.id = mf.media_folder_id
			WHERE mf.episode_id = $1
			  AND mf.missing_since IS NULL`)
	}
	var isMovie bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM media_items WHERE content_id = $1 AND type = 'movie')`, itemID).Scan(&isMovie); err != nil {
		return nil, fmt.Errorf("checking movie existence for marker detection: %w", err)
	}
	if !isMovie {
		return nil, ErrMarkerItemNotFound
	}
	return r.itemEligibility(ctx, itemID, MarkerItemMovie, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (
		           WHERE folders.enabled = true
		             AND folders.intro_detection_enabled = true
		             AND folders.type IN ('movies', 'mixed')
		       )
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		WHERE mf.content_id = $1
		  AND mf.episode_id IS NULL
		  AND COALESCE(mf.extra_id, '') = ''
		  AND mf.missing_since IS NULL`)
}

// itemEligibility counts an item's files and those in libraries with marker
// detection on, with query taking the item ID.
func (r *Repository) itemEligibility(ctx context.Context, itemID, kind, query string) (*MarkerItemEligibility, error) {
	var fileCount, enabledCount int
	if err := r.pool.QueryRow(ctx, query, itemID).Scan(&fileCount, &enabledCount); err != nil {
		return nil, fmt.Errorf("checking %s marker eligibility: %w", kind, err)
	}
	return &MarkerItemEligibility{
		ItemID:                itemID,
		Kind:                  kind,
		HasMediaFiles:         fileCount > 0,
		IntroDetectionEnabled: enabledCount > 0,
	}, nil
}

func (r *Repository) IntroDetectionEligibleForPlayback(ctx context.Context, fileID int) (bool, error) {
	var id int
	err := r.pool.QueryRow(ctx, `
		SELECT mf.id
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		WHERE mf.id = $1
		  AND folders.intro_detection_enabled = true
		  AND mf.missing_since IS NULL`, fileID).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("checking playback intro detection eligibility: %w", err)
	}
	return true, nil
}

// IsFileInEnabledLibrary returns true when the file lives in any enabled
// library, regardless of folder type or intro_detection_enabled. Online
// marker providers (TheIntroDB, etc.) gate on this rather than the
// stricter local-chromaprint-only check so movie libraries can participate
// without opting into expensive audio fingerprinting.
func (r *Repository) IsFileInEnabledLibrary(ctx context.Context, fileID int) (bool, error) {
	var id int
	err := r.pool.QueryRow(ctx, `
		SELECT mf.id
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		WHERE mf.id = $1
		  AND folders.enabled = true
		  AND mf.missing_since IS NULL`, fileID).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("checking playback online marker eligibility: %w", err)
	}
	return true, nil
}

// scanCandidates scans candidates from rows. A query that selects more
// columns after the candidate columns passes their destinations as extra;
// after the scan they hold the last row's values.
func scanCandidates(rows pgx.Rows, extra ...any) ([]Candidate, error) {
	defer rows.Close()
	var candidates []Candidate
	for rows.Next() {
		var c Candidate
		var chaptersJSON, audioTracksJSON, subtitleTracksJSON, externalSubtitlesJSON, videoTrackJSON []byte
		if err := rows.Scan(append([]any{
			&c.FileID,
			&c.EpisodeID,
			&c.SeasonID,
			&c.MediaFolderID,
			&c.FilePath,
			&c.FileHash,
			&c.FileSize,
			&c.DurationSeconds,
			&c.PresentationGroupKey,
			&c.EditionKey,
			&chaptersJSON,
			&audioTracksJSON,
			&subtitleTracksJSON,
			&externalSubtitlesJSON,
			&c.IntroStart,
			&c.IntroEnd,
			&c.IntroMarkersSource,
			&c.IntroMarkersConfidence,
			&c.IntroMarkersAlgorithm,
			&c.CreditsStart,
			&c.CreditsEnd,
			&c.CreditsMarkersSource,
			&c.CreditsMarkersConfidence,
			&c.CreditsMarkersAlgorithm,
			&c.PreviewStart,
			&c.MarkersSource,
			&c.ContentID,
			&c.ExtraID,
			&c.SeasonNumber,
			&c.EpisodeNumber,
			&c.FileModifiedAt,
			&c.CodecVideo,
			&c.CodecAudio,
			&videoTrackJSON,
		}, extra...)...); err != nil {
			return nil, fmt.Errorf("scanning intro marker candidate: %w", err)
		}
		c.ChaptersHash = chaptersHash(chaptersJSON)
		if len(chaptersJSON) > 0 {
			if err := json.Unmarshal(chaptersJSON, &c.Chapters); err != nil {
				return nil, fmt.Errorf("unmarshaling chapters for file %d: %w", c.FileID, err)
			}
		}
		if len(audioTracksJSON) > 0 {
			var tracks []models.AudioTrack
			if err := json.Unmarshal(audioTracksJSON, &tracks); err != nil {
				return nil, fmt.Errorf("unmarshaling audio tracks for file %d: %w", c.FileID, err)
			}
			c.AudioLanguage = effectiveAudioLanguage(tracks)
		}
		// The bit depth only shapes a hardware decode, so a video track that
		// does not parse leaves it unknown rather than failing the scan.
		var videoTrack models.VideoTrack
		if len(videoTrackJSON) > 0 && json.Unmarshal(videoTrackJSON, &videoTrack) == nil {
			c.VideoBitDepth = models.NormalizeVideoBitDepth(videoTrack.BitDepth, videoTrack.PixelFormat, videoTrack.Profile)
		}
		if len(subtitleTracksJSON) > 0 {
			if err := json.Unmarshal(subtitleTracksJSON, &c.SubtitleTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling subtitle tracks for file %d: %w", c.FileID, err)
			}
		}
		if len(externalSubtitlesJSON) > 0 {
			if err := json.Unmarshal(externalSubtitlesJSON, &c.ExternalSubtitles); err != nil {
				return nil, fmt.Errorf("unmarshaling external subtitles for file %d: %w", c.FileID, err)
			}
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating intro marker candidates: %w", err)
	}
	return candidates, nil
}

// chaptersHash is the SHA-256 of the chapters column exactly as Postgres
// renders it, with a NULL column hashed as empty input, so the backfill query
// can compute the same value from mf.chapters::text.
func chaptersHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CountEnabledLibraries counts the enabled libraries with marker detection on
// whose kind local analysis covers: series, mixed, and movies.
func (r *Repository) CountEnabledLibraries(ctx context.Context) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM media_folders
		WHERE enabled = true
		  AND intro_detection_enabled = true
		  AND type IN ('series', 'mixed', 'movies')`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting intro-enabled libraries: %w", err)
	}
	return count, nil
}

// PatchMarker writes a detected marker of patch.Kind through the scanner's
// marker write policy, which keeps higher-priority markers in place.
func (r *Repository) PatchMarker(ctx context.Context, patch MarkerPatch) (bool, error) {
	update, err := patch.markerUpdate()
	if err != nil {
		return false, err
	}
	return scanner.NewFileRepository(r.pool).UpsertMarkers(ctx, patch.FileID, update)
}

// WithdrawMarker clears a file's marker of withdrawal.Kind while it still
// holds the scanner result withdrawal.Algorithm wrote, and reports whether it
// did. A marker another source or detector has written since stays.
func (r *Repository) WithdrawMarker(ctx context.Context, withdrawal MarkerWithdrawal) (bool, error) {
	if withdrawal.Kind != kindIntro && withdrawal.Kind != kindCredits {
		return false, fmt.Errorf("marker withdrawal for file %d has no marker kind", withdrawal.FileID)
	}
	return scanner.NewFileRepository(r.pool).WithdrawScannerMarker(ctx, withdrawal.FileID, withdrawal.Kind.String(), withdrawal.Algorithm, withdrawal.ExpectedFile)
}

func (patch MarkerPatch) markerUpdate() (scanner.MarkerUpdate, error) {
	if patch.Kind != kindIntro && patch.Kind != kindCredits {
		return scanner.MarkerUpdate{}, fmt.Errorf("marker patch for file %d has no marker kind", patch.FileID)
	}
	if patch.Source == "" {
		return scanner.MarkerUpdate{}, fmt.Errorf("%s marker source is required", patch.Kind)
	}
	if patch.Algorithm == "" {
		return scanner.MarkerUpdate{}, fmt.Errorf("%s marker algorithm is required", patch.Kind)
	}
	if patch.Start < 0 || patch.End <= patch.Start {
		return scanner.MarkerUpdate{}, fmt.Errorf("invalid %s marker range %.3f-%.3f", patch.Kind, patch.Start, patch.End)
	}
	update := scanner.MarkerUpdate{
		MarkersSource:     patch.Source,
		MarkersConfidence: &patch.Confidence,
		MarkersAlgorithm:  patch.Algorithm,
		DetectedAt:        patch.DetectedAt,
		ExpectedFile:      patch.ExpectedFile,
	}
	if patch.Kind == kindCredits {
		update.CreditsStart, update.CreditsEnd = &patch.Start, &patch.End
	} else {
		update.IntroStart, update.IntroEnd = &patch.Start, &patch.End
	}
	return update, nil
}

// LoadArtifact returns a file's stored artifact for key whatever its status,
// or nil when there is none.
func (r *Repository) LoadArtifact(ctx context.Context, fileID int, key mediaartifact.Key) (*mediaartifact.Artifact, error) {
	return r.artifacts.Load(ctx, fileID, key)
}

// LoadArtifacts returns the stored artifacts for key of the given files,
// keyed by file ID.
func (r *Repository) LoadArtifacts(ctx context.Context, fileIDs []int, key mediaartifact.Key) (map[int]mediaartifact.Artifact, error) {
	return r.artifacts.LoadMany(ctx, fileIDs, key)
}

// UpsertArtifact stores a complete or unusable artifact.
func (r *Repository) UpsertArtifact(ctx context.Context, a mediaartifact.Artifact) error {
	return r.artifacts.Upsert(ctx, a)
}

// RecordArtifactFailure records a failed analysis with a backed-off retry
// time.
func (r *Repository) RecordArtifactFailure(ctx context.Context, failure mediaartifact.Failure) error {
	return r.artifacts.RecordFailure(ctx, failure)
}

// ArtifactKindIntroFingerprint is the raw Chromaprint of a file's opening
// audio, stored as a mediaartifact. Its config_hash is Config.ConfigHash,
// which predates kind namespacing and must not change.
const ArtifactKindIntroFingerprint = "intro_fingerprint"

// introFingerprintKey and introFingerprintIdentity locate a candidate's intro
// fingerprint: the opening window of the file, keyed by Config.ConfigHash.
func introFingerprintKey(cfg Config) mediaartifact.Key {
	return mediaartifact.Key{
		Kind:             ArtifactKindIntroFingerprint,
		AlgorithmVersion: AlgorithmVersion,
		ConfigHash:       cfg.ConfigHash(),
	}
}

func introFingerprintIdentity(candidate Candidate, cfg Config) mediaartifact.Identity {
	return headWindow(candidate, cfg).identity(candidate)
}

// LoadFingerprint returns the candidate's cached intro fingerprint, or nil
// when none is stored for its current file and analysis window.
func (r *Repository) LoadFingerprint(ctx context.Context, candidate Candidate, cfg Config) (*Fingerprint, error) {
	cfg = cfg.normalized()
	artifact, err := r.LoadArtifact(ctx, candidate.FileID, introFingerprintKey(cfg))
	if err != nil {
		return nil, err
	}
	if artifact.State(introFingerprintIdentity(candidate, cfg), "", time.Time{}) != mediaartifact.Ready ||
		artifact.PayloadFormat != ChromaprintFormat {
		return nil, nil
	}
	points := mediasample.DecodeRawFingerprint(artifact.Payload)
	if len(points) == 0 {
		return nil, nil
	}
	return &Fingerprint{
		MediaFileID:           artifact.MediaFileID,
		FileHash:              artifact.FileHash,
		FileSize:              artifact.FileSize,
		DurationSeconds:       artifact.DurationSeconds,
		WindowStartSeconds:    artifact.WindowStartSeconds,
		WindowEndSeconds:      artifact.WindowEndSeconds,
		AlgorithmVersion:      artifact.AlgorithmVersion,
		ConfigHash:            artifact.ConfigHash,
		FingerprintFormat:     artifact.PayloadFormat,
		SampleDurationSeconds: artifact.SampleDurationSeconds,
		Points:                points,
	}, nil
}

// UpsertFingerprint stores a computed intro fingerprint as a complete
// artifact.
func (r *Repository) UpsertFingerprint(ctx context.Context, fp Fingerprint) error {
	return r.UpsertArtifact(ctx, mediaartifact.Artifact{
		MediaFileID: fp.MediaFileID,
		Key: mediaartifact.Key{
			Kind:             ArtifactKindIntroFingerprint,
			AlgorithmVersion: fp.AlgorithmVersion,
			ConfigHash:       fp.ConfigHash,
		},
		Identity: mediaartifact.Identity{
			FileHash:           fp.FileHash,
			FileSize:           fp.FileSize,
			DurationSeconds:    fp.DurationSeconds,
			WindowStartSeconds: fp.WindowStartSeconds,
			WindowEndSeconds:   fp.WindowEndSeconds,
		},
		Status:                mediaartifact.StatusComplete,
		PayloadFormat:         fp.FingerprintFormat,
		SampleDurationSeconds: fp.SampleDurationSeconds,
		ItemCount:             len(fp.Points),
		Payload:               mediasample.EncodeRawFingerprint(fp.Points),
	})
}

// LoadSeasonState returns a season group's stored analysis under
// analysisHash, which keys the state of one marker kind's analysis settings
// (Config.AnalysisConfigHash for intros), or nil when there is none.
func (r *Repository) LoadSeasonState(ctx context.Context, state SeasonState, analysisHash string) (*SeasonState, error) {
	var existing SeasonState
	err := r.pool.QueryRow(ctx, `
		SELECT season_id,
		       media_folder_id,
		       analysis_group_key,
		       input_signature,
		       episode_count,
		       file_count,
		       status,
		       markers_written,
		       COALESCE(last_error, ''),
		       analyzed_at
		FROM intro_season_analysis_state
		WHERE season_id = $1
		  AND media_folder_id = $2
		  AND analysis_group_key = $3
		  AND algorithm_version = $4
		  AND config_hash = $5`,
		state.SeasonID,
		state.MediaFolderID,
		state.AnalysisGroupKey,
		AlgorithmVersion,
		analysisHash,
	).Scan(
		&existing.SeasonID,
		&existing.MediaFolderID,
		&existing.AnalysisGroupKey,
		&existing.InputSignature,
		&existing.EpisodeCount,
		&existing.FileCount,
		&existing.Status,
		&existing.MarkersWritten,
		&existing.LastError,
		&existing.AnalyzedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("loading intro season state: %w", err)
	}
	return &existing, nil
}

// UpsertSeasonState stores a season group's analysis under analysisHash; see
// LoadSeasonState.
func (r *Repository) UpsertSeasonState(ctx context.Context, state SeasonState, analysisHash string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO intro_season_analysis_state (
		    season_id,
		    media_folder_id,
		    analysis_group_key,
		    algorithm_version,
		    config_hash,
		    input_signature,
		    episode_count,
		    file_count,
		    status,
		    markers_written,
		    last_error
		) VALUES (
		    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, '')
		)
		ON CONFLICT (season_id, media_folder_id, analysis_group_key, algorithm_version, config_hash) DO UPDATE SET
		    input_signature = EXCLUDED.input_signature,
		    episode_count = EXCLUDED.episode_count,
		    file_count = EXCLUDED.file_count,
		    status = EXCLUDED.status,
		    markers_written = EXCLUDED.markers_written,
		    last_error = EXCLUDED.last_error,
		    analyzed_at = NOW()`,
		state.SeasonID,
		state.MediaFolderID,
		state.AnalysisGroupKey,
		AlgorithmVersion,
		analysisHash,
		state.InputSignature,
		state.EpisodeCount,
		state.FileCount,
		state.Status,
		state.MarkersWritten,
		state.LastError,
	)
	if err != nil {
		return fmt.Errorf("upserting intro season state: %w", err)
	}
	return nil
}

func effectiveAudioLanguage(tracks []models.AudioTrack) string {
	for _, track := range tracks {
		if track.Default && strings.TrimSpace(track.Language) != "" {
			return strings.TrimSpace(track.Language)
		}
	}
	for _, track := range tracks {
		if strings.TrimSpace(track.Language) != "" {
			return strings.TrimSpace(track.Language)
		}
	}
	return ""
}

func InputSignature(candidates []Candidate) string {
	parts := make([]string, 0, len(candidates))
	for _, c := range candidates {
		parts = append(parts, fmt.Sprintf("%d:%s:%d:%.3f:%s:%s",
			c.FileID,
			c.FileHash,
			c.FileSize,
			c.DurationSeconds,
			c.EpisodeID,
			subtitleInventorySignature(c),
		))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func subtitleInventorySignature(candidate Candidate) string {
	parts := make([]string, 0, len(candidate.ExternalSubtitles)+len(candidate.SubtitleTracks))
	for _, sub := range candidate.ExternalSubtitles {
		parts = append(parts, fmt.Sprintf("ext:%s:%s:%s:%t:%t:%t",
			sub.Path,
			sub.Language,
			sub.Format,
			sub.Forced,
			sub.Default,
			sub.HearingImpaired,
		))
	}
	for _, track := range candidate.SubtitleTracks {
		parts = append(parts, fmt.Sprintf("emb:%d:%s:%s:%t:%t:%t",
			track.Index,
			track.Language,
			track.Codec,
			track.Forced,
			track.Default,
			track.HearingImpaired,
		))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
