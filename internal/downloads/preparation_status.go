package downloads

import (
	"context"
	"fmt"
	"math"
	"time"
)

// PreparationStatus is how far the server has got preparing one download's
// file, as the device that asked for it sees it.
type PreparationStatus struct {
	State string // PreparationQueued, PreparationRunning, PreparationRetrying or PreparationPaused
	// QueuePosition is the 1-based place among every queued preparation on
	// this server, in claim order; 0 unless queued.
	QueuePosition int
	// Progress is the encoded fraction, and RemainingSeconds the estimate at
	// the reported speed; both nil until a running encode reports them.
	Progress         *float64
	RemainingSeconds *int
}

// preparationSnapshotTTL bounds how stale a reported queue position or
// progress can be. Clients poll every few seconds at most, and the snapshot
// keeps that polling from ranking the server's whole queue per request.
const preparationSnapshotTTL = 5 * time.Second

// preparationProgressFreshFor is how long a running encode's last progress
// sample is reported. Workers record one every progressFlushInterval; a
// sample older than several flushes means progress stopped arriving, and a
// frozen percentage and estimate would mislead.
const preparationProgressFreshFor = 6 * progressFlushInterval

// preparationSnapshot is every unfinished preparation's status at one moment.
type preparationSnapshot struct {
	at      time.Time
	byJobID map[string]PreparationStatus
}

// attachPreparations sets Preparation on each preparing row linked to an
// artifact. A row whose artifact finished or failed since it was read keeps
// none; its status catches up on the next read.
func (r *Repository) attachPreparations(ctx context.Context, rows []*Download) error {
	var linked []*Download
	for _, row := range rows {
		if row.Status == StatusPreparing && row.ArtifactID != "" {
			linked = append(linked, row)
		}
	}
	if len(linked) == 0 {
		return nil
	}
	statuses, err := r.preparationStatuses(ctx)
	if err != nil {
		return err
	}
	for _, row := range linked {
		if status, ok := statuses[row.ArtifactID]; ok {
			row.Preparation = &status
		}
	}
	return nil
}

// preparationStatuses returns every unfinished preparation's status, ranked
// the same way as the admin view and the claim query. The ranking covers the
// whole server queue, so one snapshot serves every reader on this node for
// preparationSnapshotTTL; concurrent readers wait for a single refresh.
func (r *Repository) preparationStatuses(ctx context.Context) (map[string]PreparationStatus, error) {
	r.prepMu.Lock()
	defer r.prepMu.Unlock()
	if snap := r.preparations; snap != nil && time.Since(snap.at) < preparationSnapshotTTL {
		return snap.byJobID, nil
	}
	byJobID, err := r.rankPreparations(ctx)
	if err != nil {
		// Readers waiting on this refresh get an empty snapshot rather than
		// each repeating the failing query; the next refresh tries again.
		r.preparations = &preparationSnapshot{at: time.Now(), byJobID: map[string]PreparationStatus{}}
		return nil, err
	}
	r.preparations = &preparationSnapshot{at: time.Now(), byJobID: byJobID}
	return byJobID, nil
}

// rankPreparations reads every unfinished preparation's status.
func (r *Repository) rankPreparations(ctx context.Context) (map[string]PreparationStatus, error) {
	result, err := r.pool.Query(ctx, preparationStatesCTE+`,
	ranked AS (
		SELECT l.id, l.state, l.progress_encoded_seconds, l.progress_duration_seconds, l.progress_speed,
		       NOT l.progress_unavailable AND l.progress_updated_at >= now() - make_interval(secs => $2) AS progress_fresh,
		       CASE WHEN l.state = 'queued'
		            THEN row_number() OVER (PARTITION BY l.state = 'queued' ORDER BY l.created_at, l.id)
		       END AS queue_position
		FROM listed l
	)
	SELECT id, state, COALESCE(queue_position, 0), progress_encoded_seconds, progress_duration_seconds, progress_speed,
	       COALESCE(progress_fresh, false)
	FROM ranked WHERE state <> 'failed'`, PreparationFailedWindow.Seconds(), preparationProgressFreshFor.Seconds())
	if err != nil {
		return nil, fmt.Errorf("reading download preparations: %w", err)
	}
	defer result.Close()
	byJobID := map[string]PreparationStatus{}
	for result.Next() {
		var id string
		var status PreparationStatus
		var encoded, duration, speed *float64
		var fresh bool
		if err := result.Scan(&id, &status.State, &status.QueuePosition, &encoded, &duration, &speed, &fresh); err != nil {
			return nil, fmt.Errorf("scanning download preparation: %w", err)
		}
		if status.State == PreparationRunning && fresh {
			status.Progress, status.RemainingSeconds = preparationProgress(encoded, duration, speed)
		}
		byJobID[id] = status
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("reading download preparations: %w", err)
	}
	return byJobID, nil
}

// preparationProgress turns an encode's reported position into a fraction
// and, when the speed is known, the seconds left at that speed.
func preparationProgress(encoded, duration, speed *float64) (*float64, *int) {
	if encoded == nil || duration == nil || *duration <= 0 {
		return nil, nil
	}
	fraction := math.Min(1, math.Max(0, *encoded / *duration))
	if speed == nil || *speed <= 0 {
		return &fraction, nil
	}
	remaining := int(math.Ceil(math.Max(0, *duration-*encoded) / *speed))
	return &fraction, &remaining
}
