package intromarkers

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/models"
)

// movieCandidateSelectFrom and movieCandidateWhere select movie files local
// credits detection covers: the feature files of movies (no episodes, no
// extras, no multi-part films) of at least movieCreditsMinimumDurationSeconds
// in enabled movie and mixed libraries with marker detection on. Movies have
// no episode or season, so both are empty.
const movieCandidateSelectFrom = movieCandidateSelect + movieCandidateFrom

const movieCandidateSelect = `
	SELECT mf.id,
	       '',
	       '',` + candidateFileColumns

const movieCandidateFrom = `
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

// movieCandidateCursor is the position of a file in ListMovieCandidates'
// order: whether its movie tail is a failure being retried, when the file
// was created, and its ID.
type movieCandidateCursor struct {
	retried   bool
	createdAt time.Time
	fileID    int
}

// ListMovieCandidates returns up to limit of the movie files the nightly
// run should analyze for credits, never-analyzed files first, then the
// newest, starting after the file at after (nil for the first page). It
// also returns the position of the last file listed, to pass as after for
// the next page; the order is decided when a file is listed, so a file the
// run analyzed and that is still eligible is not listed again.
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
// analysis reads no artifact and runs no ffmpeg. So is a movie whose stored
// credits came from a chapter, even with its tail stored, so analysis can
// withdraw them once its chapters no longer produce them. The tail's window follows from the file's duration
// and the key's parameters, so matching the file hash, size, and duration
// matches the whole identity. Failed files retried after their backoff come
// last.
//
// The artifact lookup is a LEFT JOIN, a primary-key probe per file, like the
// silence backfill's.
func (r *Repository) ListMovieCandidates(ctx context.Context, node string, after *movieCandidateCursor, limit int) ([]Candidate, *movieCandidateCursor, error) {
	key := movieCreditsTailKey()
	args := []any{
		movieCreditsMinimumDurationSeconds,
		key.AlgorithmVersion,
		key.ConfigHash,
		key.Kind,
		models.MarkerSourceScanner,
		mediaartifact.StatusComplete,
		mediaartifact.StatusUnusable,
		mediaartifact.StatusFailed,
		node,
		tailDetailNoVideo,
		tailDetailUnsupportedCodec,
		limit,
		CreditsChapterAlgorithm,
	}
	// The order is ascending on the retry flag, then descending on creation
	// time and ID, so a page starts at a later flag or, on the same flag, at
	// an older (created_at, id) pair.
	page := ""
	if after != nil {
		page = `
		  AND (COALESCE(art.status = $8, false) > $14
		       OR (COALESCE(art.status = $8, false) = $14 AND (mf.created_at, mf.id) < ($15, $16)))`
		args = append(args, after.retried, after.createdAt, after.fileID)
	}
	rows, err := r.pool.Query(ctx, movieCandidateSelect+`,
	       COALESCE(art.status = $8, false),
	       mf.created_at,
	       mf.id`+movieCandidateFrom+`
		LEFT JOIN media_intro_fingerprints art
		       ON art.media_file_id = mf.id
		      AND art.algorithm_version = $2
		      AND art.config_hash = $3
		      AND art.kind = $4`+
		movieCandidateWhere+`
		  AND (mf.credits_start IS NULL
		       OR mf.credits_end IS NULL
		       OR COALESCE(NULLIF(BTRIM(mf.credits_markers_source), ''), BTRIM(mf.markers_source), '') = $5)
		  AND (COALESCE(mf.credits_markers_algorithm, '') = $13
		       OR NOT COALESCE(
		           art.file_hash = COALESCE(mf.file_hash, '')
		           AND art.file_size = COALESCE(mf.file_size, 0)
		           AND art.duration_seconds = COALESCE(mf.duration, 0)
		           AND (art.status = $6
		                OR (art.status = $7 AND COALESCE(art.detail, '') NOT IN ($10, $11))
		                OR (art.status = $8 AND art.retry_after > NOW() AND art.recorded_by = $9)),
		           false))`+page+`
		ORDER BY COALESCE(art.status = $8, false), mf.created_at DESC, mf.id DESC
		LIMIT $12`,
		args...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("listing movie credits candidates: %w", err)
	}
	var last movieCandidateCursor
	candidates, err := scanCandidates(rows, &last.retried, &last.createdAt, &last.fileID)
	if err != nil || len(candidates) == 0 {
		return candidates, nil, err
	}
	return candidates, &last, nil
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
