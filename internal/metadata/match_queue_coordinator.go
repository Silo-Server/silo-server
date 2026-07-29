package metadata

import (
	"context"
	"fmt"
	"strings"
)

// MatchQueueCoordinator wires scanner-side queue production to the durable
// movie and series queue repositories.
type MatchQueueCoordinator struct {
	movieRepo  *MovieMatchQueueRepository
	seriesRepo *SeriesRootMatchQueueRepository
}

func NewMatchQueueCoordinator(movieRepo *MovieMatchQueueRepository, seriesRepo *SeriesRootMatchQueueRepository) *MatchQueueCoordinator {
	return &MatchQueueCoordinator{
		movieRepo:  movieRepo,
		seriesRepo: seriesRepo,
	}
}

func (c *MatchQueueCoordinator) EnqueueMovieFile(ctx context.Context, fileID int) error {
	if c == nil || c.movieRepo == nil {
		return nil
	}
	if fileID <= 0 {
		return fmt.Errorf("file id must be positive")
	}
	return c.movieRepo.EnqueueMovieFile(ctx, fileID)
}

func (c *MatchQueueCoordinator) EnqueueSeriesRoot(ctx context.Context, folderID int, observedRootPath string) error {
	if c == nil || c.seriesRepo == nil {
		return nil
	}
	if folderID <= 0 {
		return fmt.Errorf("folder id must be positive")
	}
	if strings.TrimSpace(observedRootPath) == "" {
		return fmt.Errorf("observed root path is required")
	}
	return c.seriesRepo.EnqueueSeriesRoot(ctx, folderID, observedRootPath)
}

// WakeForChangedInputs recalculates queue fingerprints after a provider
// lifecycle event. Only work whose effective provider chain (or another
// fingerprint input) changed is reset, including pending rows in backoff.
func (c *MatchQueueCoordinator) WakeForChangedInputs(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if c.movieRepo != nil {
		if _, err := c.movieRepo.WakeForChangedInputs(ctx); err != nil {
			return err
		}
	}
	if c.seriesRepo != nil {
		if _, err := c.seriesRepo.WakeForChangedInputs(ctx); err != nil {
			return err
		}
	}
	return nil
}
