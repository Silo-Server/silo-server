package intromarkers

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/models"
)

// movieCandidateSelectFrom and movieCandidateWhere select movie files local
// credits detection covers: the feature files of movies (no episodes, no
// extras, no multi-part films) of at least movieCreditsMinimumDurationSeconds
// in enabled movie and mixed libraries with marker detection on. Movies have
// no episode or season, so both are empty.
const movieCandidateSelectFrom = `
	SELECT mf.id,
	       '',
	       '',` + candidateFileColumns + `
	FROM media_files mf
	JOIN media_folders folders ON folders.id = mf.media_folder_id
	JOIN media_items mi ON mi.content_id = mf.content_id`

const movieCandidateWhere = `
	WHERE mi.type = 'movie'
	  AND mf.episode_id IS NULL
	  AND COALESCE(mf.extra_id, '') = ''
	  AND folders.enabled = true
	  AND folders.intro_detection_enabled = true
	  AND folders.type IN ('movies', 'mixed')
	  AND mf.missing_since IS NULL
	  AND COALESCE(mf.duration, 0) >= $1
	  AND COALESCE(mf.presentation_part_total, 1) <= 1`

// ListMovieCandidates returns the movie files the nightly run should
// analyze for credits, never-analyzed files first, then the newest.
//
// A file whose credits came from a higher-priority source is left out, as
// is one whose movie tail pass is stored for the file as it is now: complete
// or unusable, or failed on this server and still backing off. A sampled
// tail is stored only once the credits placed from it are written, so a
// complete tail means its credits were settled; admin refresh or playback
// analyze it again on request. An unusable row an earlier build stored from
// probe metadata does not count: that verdict is now decided on every
// analysis, so a probe repair brings the file back. Such files, and movies
// that get credits from a chapter, are listed on every run, but their
// analysis reads no artifact and runs no ffmpeg. The tail's window follows from the file's duration
// and the key's parameters, so matching the file hash, size, and duration
// matches the whole identity. Failed files retried after their backoff come
// last.
//
// The artifact lookup is a LEFT JOIN, a primary-key probe per file, like the
// silence backfill's.
func (r *Repository) ListMovieCandidates(ctx context.Context, node string) ([]Candidate, error) {
	key := movieCreditsTailKey()
	rows, err := r.pool.Query(ctx, movieCandidateSelectFrom+`
		LEFT JOIN media_intro_fingerprints art
		       ON art.media_file_id = mf.id
		      AND art.algorithm_version = $2
		      AND art.config_hash = $3
		      AND art.kind = $4`+
		movieCandidateWhere+`
		  AND (mf.credits_start IS NULL
		       OR mf.credits_end IS NULL
		       OR COALESCE(NULLIF(BTRIM(mf.credits_markers_source), ''), BTRIM(mf.markers_source), '') = $5)
		  AND NOT COALESCE(
		      art.file_hash = COALESCE(mf.file_hash, '')
		      AND art.file_size = COALESCE(mf.file_size, 0)
		      AND art.duration_seconds = COALESCE(mf.duration, 0)
		      AND (art.status = $6
		           OR (art.status = $7 AND COALESCE(art.detail, '') NOT IN ($10, $11))
		           OR (art.status = $8 AND art.retry_after > NOW() AND art.recorded_by = $9)),
		      false)
		ORDER BY COALESCE(art.status = $8, false), mf.created_at DESC, mf.id DESC`,
		movieCreditsMinimumDurationSeconds,
		key.AlgorithmVersion,
		key.ConfigHash,
		key.Kind,
		models.MarkerSourceScanner,
		ArtifactComplete,
		ArtifactUnusable,
		ArtifactFailed,
		node,
		tailDetailNoVideo,
		tailDetailUnsupportedCodec,
	)
	if err != nil {
		return nil, fmt.Errorf("listing movie credits candidates: %w", err)
	}
	return scanCandidates(rows)
}

// ListMovieCandidatesForItem returns the movie files of a movie item that
// local credits detection covers, whatever their stored analysis.
func (r *Repository) ListMovieCandidatesForItem(ctx context.Context, contentID string) ([]Candidate, error) {
	rows, err := r.pool.Query(ctx, movieCandidateSelectFrom+movieCandidateWhere+`
		  AND mf.content_id = $2
		ORDER BY mf.id`, movieCreditsMinimumDurationSeconds, contentID)
	if err != nil {
		return nil, fmt.Errorf("listing movie credits candidates for item %s: %w", contentID, err)
	}
	return scanCandidates(rows)
}

// ListMovieCandidatesForFile returns the file as a movie credits candidate,
// or none when local credits detection does not cover it.
func (r *Repository) ListMovieCandidatesForFile(ctx context.Context, fileID int) ([]Candidate, error) {
	rows, err := r.pool.Query(ctx, movieCandidateSelectFrom+movieCandidateWhere+`
		  AND mf.id = $2`, movieCreditsMinimumDurationSeconds, fileID)
	if err != nil {
		return nil, fmt.Errorf("listing movie credits candidates for file %d: %w", fileID, err)
	}
	return scanCandidates(rows)
}
