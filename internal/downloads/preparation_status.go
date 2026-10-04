package downloads

import (
	"context"
	"fmt"
	"math"
)

// PreparationStatus is how far the server has got preparing one download's
// file, as the device that asked for it sees it.
type PreparationStatus struct {
	State string // PreparationQueued, PreparationRunning or PreparationRetrying
	// QueuePosition is the 1-based place among every queued preparation on
	// this server, in claim order; 0 unless queued.
	QueuePosition int
	// Progress is the encoded fraction, and RemainingSeconds the estimate at
	// the reported speed; both nil until a running encode reports them.
	Progress         *float64
	RemainingSeconds *int
}

// attachPreparations sets Preparation on each preparing row linked to an
// artifact. A row whose artifact finished or failed since it was read keeps
// none; its status catches up on the next read.
func (r *Repository) attachPreparations(ctx context.Context, rows []*Download) error {
	byArtifact := map[string][]*Download{}
	for _, row := range rows {
		if row.Status == StatusPreparing && row.ArtifactID != "" {
			byArtifact[row.ArtifactID] = append(byArtifact[row.ArtifactID], row)
		}
	}
	if len(byArtifact) == 0 {
		return nil
	}
	ids := make([]string, 0, len(byArtifact))
	for id := range byArtifact {
		ids = append(ids, id)
	}
	// The queue is ranked over every unready artifact, the same order the
	// admin view and the claim query use.
	result, err := r.pool.Query(ctx, preparationStatesCTE+`,
	ranked AS (
		SELECT l.id, l.state, l.progress_encoded_seconds, l.progress_duration_seconds, l.progress_speed,
		       CASE WHEN l.state = 'queued'
		            THEN row_number() OVER (PARTITION BY l.state = 'queued' ORDER BY l.created_at, l.id)
		       END AS queue_position
		FROM listed l
	)
	SELECT id, state, COALESCE(queue_position, 0), progress_encoded_seconds, progress_duration_seconds, progress_speed
	FROM ranked WHERE id = ANY($2) AND state <> 'failed'`, PreparationFailedWindow.Seconds(), ids)
	if err != nil {
		return fmt.Errorf("reading download preparations: %w", err)
	}
	defer result.Close()
	for result.Next() {
		var id string
		var status PreparationStatus
		var encoded, duration, speed *float64
		if err := result.Scan(&id, &status.State, &status.QueuePosition, &encoded, &duration, &speed); err != nil {
			return fmt.Errorf("scanning download preparation: %w", err)
		}
		if status.State == PreparationRunning {
			status.Progress, status.RemainingSeconds = preparationProgress(encoded, duration, speed)
		}
		for _, row := range byArtifact[id] {
			row.Preparation = &status
		}
	}
	return result.Err()
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
