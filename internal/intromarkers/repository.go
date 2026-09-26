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

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

var ErrEpisodeNotFound = errors.New("episode not found")

type EpisodeIntroEligibility struct {
	EpisodeID             string
	HasMediaFiles         bool
	IntroDetectionEnabled bool
}

const baseCandidateSelect = baseCandidateSelectFrom + baseCandidateWhere

// baseCandidateSelectFrom and baseCandidateWhere are split so a query can add
// joins between them.
const baseCandidateSelectFrom = `
	SELECT mf.id,
	       mf.episode_id,
	       e.season_id,
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
	       mf.markers_source,
	       COALESCE(mf.content_id, ''),
	       COALESCE(mf.extra_id, ''),
	       COALESCE(mf.season_number, 0),
	       COALESCE(mf.episode_number, 0),
	       mf.file_modified_at
	FROM media_files mf
	JOIN media_folders folders ON folders.id = mf.media_folder_id
	JOIN episodes e ON e.content_id = mf.episode_id`

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

func (r *Repository) EpisodeIntroEligibility(ctx context.Context, episodeID string) (*EpisodeIntroEligibility, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM episodes WHERE content_id = $1)`, episodeID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("checking episode existence for intro detection: %w", err)
	}
	if !exists {
		return nil, ErrEpisodeNotFound
	}

	var fileCount, introEnabledCount int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (
		           WHERE folders.enabled = true
		             AND folders.intro_detection_enabled = true
		             AND folders.type IN ('series', 'mixed')
		       )
		FROM media_files mf
		JOIN media_folders folders ON folders.id = mf.media_folder_id
		WHERE mf.episode_id = $1
		  AND mf.missing_since IS NULL`, episodeID).Scan(&fileCount, &introEnabledCount)
	if err != nil {
		return nil, fmt.Errorf("checking episode intro eligibility: %w", err)
	}

	return &EpisodeIntroEligibility{
		EpisodeID:             episodeID,
		HasMediaFiles:         fileCount > 0,
		IntroDetectionEnabled: introEnabledCount > 0,
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

func scanCandidates(rows pgx.Rows) ([]Candidate, error) {
	defer rows.Close()
	var candidates []Candidate
	for rows.Next() {
		var c Candidate
		var chaptersJSON, audioTracksJSON, subtitleTracksJSON, externalSubtitlesJSON []byte
		if err := rows.Scan(
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
			&c.MarkersSource,
			&c.ContentID,
			&c.ExtraID,
			&c.SeasonNumber,
			&c.EpisodeNumber,
			&c.FileModifiedAt,
		); err != nil {
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

func (r *Repository) CountEnabledLibraries(ctx context.Context) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM media_folders
		WHERE enabled = true
		  AND intro_detection_enabled = true
		  AND type IN ('series', 'mixed')`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting intro-enabled libraries: %w", err)
	}
	return count, nil
}

func (r *Repository) PatchIntroMarker(ctx context.Context, patch IntroMarkerPatch) (bool, error) {
	if patch.Source == "" {
		return false, fmt.Errorf("intro marker source is required")
	}
	if patch.Algorithm == "" {
		return false, fmt.Errorf("intro marker algorithm is required")
	}
	if patch.Start < 0 || patch.End <= patch.Start {
		return false, fmt.Errorf("invalid intro marker range %.3f-%.3f", patch.Start, patch.End)
	}
	return scanner.NewFileRepository(r.pool).UpsertMarkers(ctx, patch.FileID, scanner.MarkerUpdate{
		IntroStart:        &patch.Start,
		IntroEnd:          &patch.End,
		MarkersSource:     patch.Source,
		MarkersConfidence: &patch.Confidence,
		MarkersAlgorithm:  patch.Algorithm,
		DetectedAt:        patch.DetectedAt,
		ExpectedFile:      patch.ExpectedFile,
	})
}

// ErrArtifactKindConflict reports an artifact whose primary key is already
// held by another kind's row, which means two kinds derived the same
// config_hash. ArtifactConfigHash prevents that.
var ErrArtifactKindConflict = errors.New("media analysis artifact key belongs to another kind")

const artifactSelect = `
	SELECT media_file_id,
	       kind,
	       algorithm_version,
	       config_hash,
	       file_hash,
	       COALESCE(file_size, 0),
	       duration_seconds,
	       window_start_seconds,
	       window_end_seconds,
	       status,
	       COALESCE(detail, ''),
	       fingerprint_format,
	       sample_duration_seconds,
	       point_count,
	       points,
	       failure_count,
	       COALESCE(last_error, ''),
	       retry_after,
	       recorded_by,
	       updated_at
	FROM media_intro_fingerprints`

func scanArtifact(row pgx.Row) (Artifact, error) {
	var a Artifact
	err := row.Scan(
		&a.MediaFileID,
		&a.Kind,
		&a.AlgorithmVersion,
		&a.ConfigHash,
		&a.FileHash,
		&a.FileSize,
		&a.DurationSeconds,
		&a.WindowStartSeconds,
		&a.WindowEndSeconds,
		&a.Status,
		&a.Detail,
		&a.PayloadFormat,
		&a.SampleDurationSeconds,
		&a.ItemCount,
		&a.Payload,
		&a.FailureCount,
		&a.LastError,
		&a.RetryAfter,
		&a.RecordedBy,
		&a.UpdatedAt,
	)
	return a, err
}

// LoadArtifact returns a file's stored artifact for key whatever its status,
// or nil when there is none. Artifact.State says whether it applies.
func (r *Repository) LoadArtifact(ctx context.Context, fileID int, key ArtifactKey) (*Artifact, error) {
	return loadArtifact(ctx, r.pool, fileID, key, false)
}

// artifactDB is a pool or a transaction.
type artifactDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadArtifact(ctx context.Context, q artifactDB, fileID int, key ArtifactKey, forUpdate bool) (*Artifact, error) {
	query := artifactSelect + `
		WHERE media_file_id = $1
		  AND algorithm_version = $2
		  AND config_hash = $3
		  AND kind = $4`
	if forUpdate {
		query += `
		FOR UPDATE`
	}
	a, err := scanArtifact(q.QueryRow(ctx, query, fileID, key.AlgorithmVersion, key.ConfigHash, key.Kind))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("loading %s artifact for file %d: %w", key.Kind, fileID, err)
	}
	return &a, nil
}

// LoadArtifacts returns the stored artifacts for key of the given files,
// whatever their status, keyed by file ID. Files without one are absent.
func (r *Repository) LoadArtifacts(ctx context.Context, fileIDs []int, key ArtifactKey) (map[int]Artifact, error) {
	artifacts := make(map[int]Artifact, len(fileIDs))
	if len(fileIDs) == 0 {
		return artifacts, nil
	}
	rows, err := r.pool.Query(ctx, artifactSelect+`
		WHERE media_file_id = ANY($1::bigint[])
		  AND algorithm_version = $2
		  AND config_hash = $3
		  AND kind = $4`, fileIDs, key.AlgorithmVersion, key.ConfigHash, key.Kind)
	if err != nil {
		return nil, fmt.Errorf("loading %s artifacts: %w", key.Kind, err)
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning %s artifact: %w", key.Kind, err)
		}
		artifacts[a.MediaFileID] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating %s artifacts: %w", key.Kind, err)
	}
	return artifacts, nil
}

// artifactUpsert writes every column of a row. Its ON CONFLICT target is the
// primary key older binaries upsert on, and it never takes over another
// kind's row.
const artifactUpsert = `
	INSERT INTO media_intro_fingerprints (
	    media_file_id,
	    kind,
	    algorithm_version,
	    config_hash,
	    file_hash,
	    file_size,
	    duration_seconds,
	    window_start_seconds,
	    window_end_seconds,
	    status,
	    detail,
	    fingerprint_format,
	    sample_duration_seconds,
	    point_count,
	    points,
	    failure_count,
	    last_error,
	    retry_after,
	    recorded_by
	) VALUES (
	    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''), $12, $13, $14, $15, $16, NULLIF($17, ''), $18, $19
	)
	ON CONFLICT (media_file_id, algorithm_version, config_hash) DO UPDATE SET
	    file_hash = EXCLUDED.file_hash,
	    file_size = EXCLUDED.file_size,
	    duration_seconds = EXCLUDED.duration_seconds,
	    window_start_seconds = EXCLUDED.window_start_seconds,
	    window_end_seconds = EXCLUDED.window_end_seconds,
	    status = EXCLUDED.status,
	    detail = EXCLUDED.detail,
	    fingerprint_format = EXCLUDED.fingerprint_format,
	    sample_duration_seconds = EXCLUDED.sample_duration_seconds,
	    point_count = EXCLUDED.point_count,
	    points = EXCLUDED.points,
	    failure_count = EXCLUDED.failure_count,
	    last_error = EXCLUDED.last_error,
	    retry_after = EXCLUDED.retry_after,
	    recorded_by = EXCLUDED.recorded_by,
	    updated_at = NOW()
	WHERE media_intro_fingerprints.kind = EXCLUDED.kind`

func execArtifactUpsert(ctx context.Context, q artifactDB, a Artifact) error {
	payload := a.Payload
	if payload == nil {
		payload = []byte{}
	}
	tag, err := q.Exec(ctx, artifactUpsert,
		a.MediaFileID,
		a.Kind,
		a.AlgorithmVersion,
		a.ConfigHash,
		a.FileHash,
		a.FileSize,
		a.DurationSeconds,
		a.WindowStartSeconds,
		a.WindowEndSeconds,
		a.Status,
		a.Detail,
		a.PayloadFormat,
		a.SampleDurationSeconds,
		a.ItemCount,
		payload,
		a.FailureCount,
		a.LastError,
		a.RetryAfter,
		a.RecordedBy,
	)
	if err != nil {
		return fmt.Errorf("upserting %s artifact for file %d: %w", a.Kind, a.MediaFileID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("upserting %s artifact for file %d: %w", a.Kind, a.MediaFileID, ErrArtifactKindConflict)
	}
	return nil
}

func validateArtifactKey(key ArtifactKey) error {
	if strings.TrimSpace(key.Kind) == "" || strings.TrimSpace(key.ConfigHash) == "" {
		return fmt.Errorf("media analysis artifact needs a kind and a config hash")
	}
	return nil
}

// UpsertArtifact stores a complete or unusable artifact, replacing any
// earlier row for its key and clearing recorded failures. An unusable
// artifact carries no payload, so binaries that predate artifact statuses
// read it as a cache miss.
func (r *Repository) UpsertArtifact(ctx context.Context, a Artifact) error {
	if err := validateArtifactKey(a.ArtifactKey); err != nil {
		return err
	}
	switch a.Status {
	case ArtifactComplete:
	case ArtifactUnusable:
		if len(a.Payload) > 0 || a.ItemCount != 0 {
			return fmt.Errorf("unusable %s artifact for file %d carries a payload", a.Kind, a.MediaFileID)
		}
	default:
		return fmt.Errorf("upserting %s artifact for file %d: status %q is not complete or unusable", a.Kind, a.MediaFileID, a.Status)
	}
	a.FailureCount = 0
	a.LastError = ""
	a.RetryAfter = nil
	return execArtifactUpsert(ctx, r.pool, a)
}

// RecordArtifactFailure records a failed analysis with a retry time that
// backs off over the recording server's consecutive failures. It leaves a
// complete or unusable row for the same file identity in place: that result
// still stands, and was most likely written by another server meanwhile.
// A failed row carries no payload, so binaries that predate artifact statuses
// read it as a cache miss.
func (r *Repository) RecordArtifactFailure(ctx context.Context, failure ArtifactFailure) error {
	if err := validateArtifactKey(failure.ArtifactKey); err != nil {
		return err
	}
	if strings.TrimSpace(failure.RecordedBy) == "" {
		return errors.New("intromarkers: artifact failure requires the recording server")
	}
	if failure.At.IsZero() {
		failure.At = time.Now().UTC()
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		previous, err := loadArtifact(ctx, tx, failure.MediaFileID, failure.ArtifactKey, true)
		if err != nil {
			return err
		}
		if previous != nil && previous.ArtifactIdentity == failure.ArtifactIdentity &&
			(previous.Status == ArtifactComplete || previous.Status == ArtifactUnusable) {
			return nil
		}
		count, retryAfter := nextArtifactFailure(previous, failure)
		return execArtifactUpsert(ctx, tx, Artifact{
			MediaFileID:      failure.MediaFileID,
			ArtifactKey:      failure.ArtifactKey,
			ArtifactIdentity: failure.ArtifactIdentity,
			Status:           ArtifactFailed,
			FailureCount:     count,
			LastError:        failure.Error,
			RetryAfter:       &retryAfter,
			RecordedBy:       failure.RecordedBy,
		})
	})
}

// introFingerprintKey and introFingerprintIdentity locate a candidate's intro
// fingerprint: the opening window of the file, keyed by Config.ConfigHash.
func introFingerprintKey(cfg Config) ArtifactKey {
	return ArtifactKey{
		Kind:             ArtifactKindIntroFingerprint,
		AlgorithmVersion: AlgorithmVersion,
		ConfigHash:       cfg.ConfigHash(),
	}
}

func introFingerprintIdentity(candidate Candidate, cfg Config) ArtifactIdentity {
	return ArtifactIdentity{
		FileHash:           candidate.FileHash,
		FileSize:           candidate.FileSize,
		DurationSeconds:    candidate.DurationSeconds,
		WindowStartSeconds: 0,
		WindowEndSeconds:   analysisWindowEnd(candidate.DurationSeconds, cfg),
	}
}

// LoadFingerprint returns the candidate's cached intro fingerprint, or nil
// when none is stored for its current file and analysis window.
func (r *Repository) LoadFingerprint(ctx context.Context, candidate Candidate, cfg Config) (*Fingerprint, error) {
	cfg = cfg.normalized()
	artifact, err := r.LoadArtifact(ctx, candidate.FileID, introFingerprintKey(cfg))
	if err != nil {
		return nil, err
	}
	if artifact.State(introFingerprintIdentity(candidate, cfg), "", time.Time{}) != ArtifactReady ||
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
	return r.UpsertArtifact(ctx, Artifact{
		MediaFileID: fp.MediaFileID,
		ArtifactKey: ArtifactKey{
			Kind:             ArtifactKindIntroFingerprint,
			AlgorithmVersion: fp.AlgorithmVersion,
			ConfigHash:       fp.ConfigHash,
		},
		ArtifactIdentity: ArtifactIdentity{
			FileHash:           fp.FileHash,
			FileSize:           fp.FileSize,
			DurationSeconds:    fp.DurationSeconds,
			WindowStartSeconds: fp.WindowStartSeconds,
			WindowEndSeconds:   fp.WindowEndSeconds,
		},
		Status:                ArtifactComplete,
		PayloadFormat:         fp.FingerprintFormat,
		SampleDurationSeconds: fp.SampleDurationSeconds,
		ItemCount:             len(fp.Points),
		Payload:               mediasample.EncodeRawFingerprint(fp.Points),
	})
}

func (r *Repository) LoadSeasonState(ctx context.Context, state SeasonState, cfg Config) (*SeasonState, error) {
	cfg = cfg.normalized()
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
		cfg.AnalysisConfigHash(),
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

func (r *Repository) UpsertSeasonState(ctx context.Context, state SeasonState, cfg Config) error {
	cfg = cfg.normalized()
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
		cfg.AnalysisConfigHash(),
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
