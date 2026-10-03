package downloads

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Preparation states reported to administrators. They collapse the recipe
// variants of download_artifacts.status into what an operator acts on.
const (
	PreparationRunning  = "running"
	PreparationQueued   = "queued"
	PreparationRetrying = "retrying" // failed an attempt, waiting out its backoff
	PreparationFailed   = "failed"
)

// PreparationFailedWindow is how long a terminally failed job stays listed.
const PreparationFailedWindow = 24 * time.Hour

// Preparation is one prepare job as the admin activity view shows it.
type Preparation struct {
	ArtifactID    string
	State         string
	Format        string // remux | transcode
	QueuePosition int    // 1-based among queued jobs; 0 otherwise

	MediaFileID   int
	ContentID     string // the movie, or the episode's series
	EpisodeID     string
	MediaTitle    string
	MediaType     string
	SeriesName    string
	EpisodeName   string
	SeasonNumber  *int
	EpisodeNumber *int

	SourceContainer   string
	SourceVideoCodec  string
	SourceResolution  string
	SourceHDR         bool
	SourceAudioTracks []models.AudioTrack
	SourceFileSize    int64
	SourceDuration    float64
	SourceBitrateKbps int

	TargetContainer   string
	TargetVideoCodec  string
	TargetAudioCodec  string
	TargetResolution  string
	TargetBitrateKbps int
	ToneMapMode       string
	ToneMapSourceKind string
	AllAudioTracks    bool // the multi-track layout keeps every source audio track

	WorkerKind          string
	WorkerNodeID        *int
	WorkerName          string
	Attempts            int
	MaxAttempts         int
	ErrorMessage        string
	CreatedAt           time.Time
	StartedAt           *time.Time
	CompletedAt         *time.Time
	NextRetryAt         *time.Time
	Progress            *ArtifactProgress
	ProgressUnavailable bool

	Requesters []PreparationRequester
}

// PreparationRequester is one download row waiting on a job.
type PreparationRequester struct {
	UserID         int
	Username       string
	ProfileID      string
	ProfileName    string
	DeviceID       string // empty for a one-off web download
	DeviceName     string
	DevicePlatform string
	Status         string
}

// PreparationCounts totals every listed job, including ones past the item cap.
type PreparationCounts struct {
	Running      int
	Queued       int
	Retrying     int
	FailedRecent int
}

// PreparationList is the admin snapshot of the preparation queue.
type PreparationList struct {
	Counts PreparationCounts
	Items  []Preparation
}

// ProfileNamesFunc resolves one account's profile ids to display names.
// Profiles may live outside Postgres, so the reader cannot join them.
type ProfileNamesFunc func(ctx context.Context, userID int) (map[string]string, error)

// PreparationReader reads the admin view of the preparation queue.
type PreparationReader struct {
	pool         *pgxpool.Pool
	profileNames ProfileNamesFunc
}

// NewPreparationReader creates a reader over download_artifacts.
func NewPreparationReader(pool *pgxpool.Pool, profileNames ProfileNamesFunc) *PreparationReader {
	return &PreparationReader{pool: pool, profileNames: profileNames}
}

// Available reports whether the reader can serve lists.
func (r *PreparationReader) Available() bool { return r != nil && r.pool != nil }

// The unready predicate matches download_artifacts_unready_idx.
const preparationStatesCTE = `WITH listed AS (
	SELECT a.*,
	       CASE WHEN a.status IN ('running', 'tone_map_running', 'audio_v2_running', 'tracks_v1_running') THEN 'running'
	            WHEN a.status = 'failed' THEN 'failed'
	            WHEN a.next_retry_at > now() THEN 'retrying'
	            ELSE 'queued' END AS state
	FROM download_artifacts a
	WHERE a.status NOT IN ('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready')
	  AND (a.status <> 'failed' OR a.completed_at >= now() - make_interval(secs => $1))
)`

// List returns running jobs first, then jobs waiting to retry, the queue in
// claim order, and recent failures, newest first; at most limit items.
func (r *PreparationReader) List(ctx context.Context, limit int) (*PreparationList, error) {
	if !r.Available() {
		return nil, fmt.Errorf("preparation reader is not configured")
	}
	window := PreparationFailedWindow.Seconds()
	out := &PreparationList{Items: []Preparation{}}

	rows, err := r.pool.Query(ctx, preparationStatesCTE+` SELECT state, count(*) FROM listed GROUP BY state`, window)
	if err != nil {
		return nil, fmt.Errorf("counting preparations: %w", err)
	}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning preparation count: %w", err)
		}
		switch state {
		case PreparationRunning:
			out.Counts.Running = count
		case PreparationQueued:
			out.Counts.Queued = count
		case PreparationRetrying:
			out.Counts.Retrying = count
		case PreparationFailed:
			out.Counts.FailedRecent = count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("counting preparations: %w", err)
	}

	rows, err = r.pool.Query(ctx, preparationStatesCTE+`,
	ranked AS (
		SELECT l.*,
		       CASE WHEN l.state = 'queued'
		            THEN row_number() OVER (PARTITION BY l.state = 'queued' ORDER BY l.created_at, l.id)
		       END AS queue_position
		FROM listed l
	)
	SELECT l.id, l.state, l.format, COALESCE(l.queue_position, 0),
	       l.media_file_id, COALESCE(e.series_id, mf.content_id, ''), COALESCE(mf.episode_id, ''),
	       COALESCE(mi.title, ''), COALESCE(mi.type, ''), COALESCE(series_mi.title, ''), COALESCE(e.title, ''),
	       e.season_number, e.episode_number,
	       COALESCE(mf.container, ''), COALESCE(mf.codec_video, ''), COALESCE(mf.resolution, ''), COALESCE(mf.hdr, false),
	       COALESCE(mf.audio_tracks::text, '[]'), COALESCE(mf.file_size, 0), COALESCE(mf.duration, 0), COALESCE(mf.bitrate, 0),
	       l.container, l.codec_video, l.codec_audio, l.resolution, l.target_bitrate_kbps,
	       l.tone_map_mode, l.tone_map_source_kind, l.track_recipe_version <> '',
	       l.worker_kind, l.worker_node_id, l.worker_name,
	       l.attempts, l.max_attempts, l.error_message, l.created_at, l.started_at, l.completed_at,
	       CASE WHEN l.state = 'retrying' THEN l.next_retry_at END,
	       l.progress_encoded_seconds, l.progress_duration_seconds, l.progress_speed, l.progress_updated_at,
	       l.progress_unavailable
	FROM ranked l
	LEFT JOIN media_files mf ON mf.id = l.media_file_id
	LEFT JOIN media_items mi ON mi.content_id = mf.content_id
	LEFT JOIN episodes e ON e.content_id = mf.episode_id
	LEFT JOIN media_items series_mi ON series_mi.content_id = e.series_id
	ORDER BY CASE l.state WHEN 'running' THEN 0 WHEN 'retrying' THEN 1 WHEN 'queued' THEN 2 ELSE 3 END,
	         CASE l.state WHEN 'running' THEN l.started_at WHEN 'retrying' THEN l.next_retry_at WHEN 'queued' THEN l.created_at END,
	         l.completed_at DESC NULLS LAST, l.id
	LIMIT $2`, window, limit)
	if err != nil {
		return nil, fmt.Errorf("listing preparations: %w", err)
	}
	defer rows.Close()
	index := map[string]int{}
	for rows.Next() {
		var p Preparation
		var audioTracks string
		var sourceDuration int64
		var encoded, duration, speed *float64
		var progressAt *time.Time
		if err := rows.Scan(
			&p.ArtifactID, &p.State, &p.Format, &p.QueuePosition,
			&p.MediaFileID, &p.ContentID, &p.EpisodeID,
			&p.MediaTitle, &p.MediaType, &p.SeriesName, &p.EpisodeName,
			&p.SeasonNumber, &p.EpisodeNumber,
			&p.SourceContainer, &p.SourceVideoCodec, &p.SourceResolution, &p.SourceHDR,
			&audioTracks, &p.SourceFileSize, &sourceDuration, &p.SourceBitrateKbps,
			&p.TargetContainer, &p.TargetVideoCodec, &p.TargetAudioCodec, &p.TargetResolution, &p.TargetBitrateKbps,
			&p.ToneMapMode, &p.ToneMapSourceKind, &p.AllAudioTracks,
			&p.WorkerKind, &p.WorkerNodeID, &p.WorkerName,
			&p.Attempts, &p.MaxAttempts, &p.ErrorMessage, &p.CreatedAt, &p.StartedAt, &p.CompletedAt,
			&p.NextRetryAt,
			&encoded, &duration, &speed, &progressAt,
			&p.ProgressUnavailable,
		); err != nil {
			return nil, fmt.Errorf("scanning preparation: %w", err)
		}
		p.SourceDuration = float64(sourceDuration)
		if err := json.Unmarshal([]byte(audioTracks), &p.SourceAudioTracks); err != nil {
			p.SourceAudioTracks = nil
		}
		if progressAt != nil && p.State == PreparationRunning {
			p.Progress = &ArtifactProgress{UpdatedAt: *progressAt}
			if encoded != nil {
				p.Progress.EncodedSeconds = *encoded
			}
			if duration != nil {
				p.Progress.DurationSeconds = *duration
			}
			if speed != nil {
				p.Progress.Speed = *speed
			}
		}
		if p.State != PreparationRunning {
			p.ProgressUnavailable = false
		}
		index[p.ArtifactID] = len(out.Items)
		out.Items = append(out.Items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing preparations: %w", err)
	}
	rows.Close()
	if len(out.Items) == 0 {
		return out, nil
	}
	if err := r.attachRequesters(ctx, out.Items, index); err != nil {
		return nil, err
	}
	return out, nil
}

// attachRequesters lists the download rows waiting on each job. Canceled and
// revoked rows no longer wait on anything; failed rows stay so a failed job
// still shows who asked for it.
func (r *PreparationReader) attachRequesters(ctx context.Context, items []Preparation, index map[string]int) error {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ArtifactID)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT d.artifact_id, d.user_id, COALESCE(u.username, ''), COALESCE(d.profile_id, ''), COALESCE(d.device_id, ''),
		       COALESCE(ud.device_name, ''), COALESCE(ud.device_platform, ''), d.status
		FROM downloads d
		LEFT JOIN users u ON u.id = d.user_id
		LEFT JOIN user_devices ud ON ud.user_id = d.user_id AND ud.profile_id = d.profile_id AND ud.device_id = d.device_id
		WHERE d.artifact_id = ANY($1) AND d.status NOT IN ('cancelled', 'revoked')
		ORDER BY d.created_at, d.id`, ids)
	if err != nil {
		return fmt.Errorf("listing preparation requesters: %w", err)
	}
	defer rows.Close()
	users := map[int]struct{}{}
	for rows.Next() {
		var artifactID string
		var req PreparationRequester
		if err := rows.Scan(&artifactID, &req.UserID, &req.Username, &req.ProfileID, &req.DeviceID,
			&req.DeviceName, &req.DevicePlatform, &req.Status); err != nil {
			return fmt.Errorf("scanning preparation requester: %w", err)
		}
		i, ok := index[artifactID]
		if !ok {
			continue
		}
		items[i].Requesters = append(items[i].Requesters, req)
		users[req.UserID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("listing preparation requesters: %w", err)
	}
	if r.profileNames == nil {
		return nil
	}
	names := make(map[int]map[string]string, len(users))
	for userID := range users {
		byProfile, err := r.profileNames(ctx, userID)
		if err != nil {
			// A name is decoration; the list is still correct without it.
			slog.WarnContext(ctx, "preparation requester profile lookup failed", "component", "downloads", "user_id", userID, "error", err)
			continue
		}
		names[userID] = byProfile
	}
	for i := range items {
		for j := range items[i].Requesters {
			req := &items[i].Requesters[j]
			req.ProfileName = names[req.UserID][req.ProfileID]
		}
	}
	return nil
}
