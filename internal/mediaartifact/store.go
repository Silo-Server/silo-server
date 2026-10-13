package mediaartifact

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrKindConflict reports an artifact whose primary key is already held by
// another kind's row, which means two kinds derived the same config_hash.
// ConfigHash prevents that.
var ErrKindConflict = errors.New("media analysis artifact key belongs to another kind")

// Store reads and writes artifacts in media_intro_fingerprints. The table
// name predates generalization.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store backed by pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

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

// Load returns a file's stored artifact for key whatever its status, or nil
// when there is none. Artifact.State says whether it applies.
func (s *Store) Load(ctx context.Context, fileID int, key Key) (*Artifact, error) {
	return loadArtifact(ctx, s.pool, fileID, key, false)
}

// querier is a pool or a transaction.
type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadArtifact(ctx context.Context, q querier, fileID int, key Key, forUpdate bool) (*Artifact, error) {
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

// LoadMany returns the stored artifacts for key of the given files, whatever
// their status, keyed by file ID. Files without one are absent.
func (s *Store) LoadMany(ctx context.Context, fileIDs []int, key Key) (map[int]Artifact, error) {
	artifacts := make(map[int]Artifact, len(fileIDs))
	if len(fileIDs) == 0 {
		return artifacts, nil
	}
	rows, err := s.pool.Query(ctx, artifactSelect+`
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

// artifactInsert writes every column of a row. The statements built on it
// conflict on the primary key older binaries upsert on.
const artifactInsert = `
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
	)`

// artifactUpsert replaces any row for the key, but never another kind's.
const artifactUpsert = artifactInsert + `
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

// artifactInsertNew writes a row only when the key has none.
const artifactInsertNew = artifactInsert + `
	ON CONFLICT (media_file_id, algorithm_version, config_hash) DO NOTHING`

func execArtifactUpsert(ctx context.Context, q querier, a Artifact) error {
	written, err := execArtifactWrite(ctx, q, artifactUpsert, a)
	if err != nil {
		return err
	}
	if !written {
		return fmt.Errorf("upserting %s artifact for file %d: %w", a.Kind, a.MediaFileID, ErrKindConflict)
	}
	return nil
}

// execArtifactWrite runs one of the artifactInsert statements for a and
// reports whether it wrote a row.
func execArtifactWrite(ctx context.Context, q querier, query string, a Artifact) (bool, error) {
	payload := a.Payload
	if payload == nil {
		payload = []byte{}
	}
	tag, err := q.Exec(ctx, query,
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
		return false, fmt.Errorf("writing %s artifact for file %d: %w", a.Kind, a.MediaFileID, err)
	}
	return tag.RowsAffected() > 0, nil
}

func validateKey(key Key) error {
	if strings.TrimSpace(key.Kind) == "" || strings.TrimSpace(key.ConfigHash) == "" {
		return fmt.Errorf("media analysis artifact needs a kind and a config hash")
	}
	return nil
}

// Upsert stores a complete or unusable artifact, replacing any earlier row
// for its key and clearing recorded failures. An unusable artifact carries no
// payload, so binaries that predate artifact statuses read it as a cache
// miss.
func (s *Store) Upsert(ctx context.Context, a Artifact) error {
	if err := validateKey(a.Key); err != nil {
		return err
	}
	switch a.Status {
	case StatusComplete:
	case StatusUnusable:
		if len(a.Payload) > 0 || a.ItemCount != 0 {
			return fmt.Errorf("unusable %s artifact for file %d carries a payload", a.Kind, a.MediaFileID)
		}
	default:
		return fmt.Errorf("upserting %s artifact for file %d: status %q is not complete or unusable", a.Kind, a.MediaFileID, a.Status)
	}
	a.FailureCount = 0
	a.LastError = ""
	a.RetryAfter = nil
	return execArtifactUpsert(ctx, s.pool, a)
}

// RecordFailure records a failed analysis with a retry time that backs off
// over the recording server's consecutive failures. It leaves a complete or
// unusable row for the same file identity in place: that result still
// stands, and was most likely written by another server meanwhile. A failed
// row carries no payload, so binaries that predate artifact statuses read it
// as a cache miss.
func (s *Store) RecordFailure(ctx context.Context, failure Failure) error {
	if err := validateKey(failure.Key); err != nil {
		return err
	}
	if strings.TrimSpace(failure.RecordedBy) == "" {
		return errors.New("mediaartifact: artifact failure requires the recording server")
	}
	if failure.At.IsZero() {
		failure.At = time.Now().UTC()
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Locking the row serializes this with other writers only when it
		// exists. Without one, the failure is inserted only if the key is
		// still free; a row another server wrote meanwhile is locked and
		// judged on the second pass.
		for range 2 {
			previous, err := loadArtifact(ctx, tx, failure.MediaFileID, failure.Key, true)
			if err != nil {
				return err
			}
			if previous != nil && previous.Identity == failure.Identity &&
				(previous.Status == StatusComplete || previous.Status == StatusUnusable) {
				return nil
			}
			count, retryAfter := NextFailure(previous, failure)
			row := Artifact{
				MediaFileID:  failure.MediaFileID,
				Key:          failure.Key,
				Identity:     failure.Identity,
				Status:       StatusFailed,
				FailureCount: count,
				LastError:    failure.Error,
				RetryAfter:   &retryAfter,
				RecordedBy:   failure.RecordedBy,
			}
			if previous != nil {
				return execArtifactUpsert(ctx, tx, row)
			}
			inserted, err := execArtifactWrite(ctx, tx, artifactInsertNew, row)
			if err != nil || inserted {
				return err
			}
		}
		// The key stayed taken by a row this kind cannot see: another kind's.
		return fmt.Errorf("recording %s artifact failure for file %d: %w", failure.Kind, failure.MediaFileID, ErrKindConflict)
	})
}
