package usercollections

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

// Scheduler picks user-owned collections whose next_sync_at is in the past
// and runs the configured sync. Driven by a TaskManager interval task.
type Scheduler struct {
	pool    *pgxpool.Pool
	service *Service
	logger  *slog.Logger

	inFlight sync.Map
}

func NewScheduler(pool *pgxpool.Pool, service *Service, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		pool:    pool,
		service: service,
		logger:  logger,
	}
}

type SchedulerResult struct {
	Due     int `json:"due"`
	Synced  int `json:"synced"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

type dueCollection struct {
	UserID       int
	CollectionID string
}

func (s *Scheduler) RunOnce(ctx context.Context) (json.RawMessage, error) {
	due, err := s.listDue(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing due user collections: %w", err)
	}
	result := SchedulerResult{Due: len(due)}
	if len(due) == 0 {
		return marshalResult(result), nil
	}

	s.logger.InfoContext(ctx, "user collection sync scheduler: starting", "due", len(due))

	var (
		mu      sync.Mutex
		g, gctx = errgroup.WithContext(ctx)
	)
	g.SetLimit(3)

	for _, dc := range due {
		dc := dc
		g.Go(func() error {
			s.syncOne(gctx, dc, &mu, &result)
			return nil
		})
	}
	_ = g.Wait()

	s.logger.InfoContext(ctx, "user collection sync scheduler: complete",
		"due", result.Due, "synced", result.Synced,
		"failed", result.Failed, "skipped", result.Skipped,
	)
	return marshalResult(result), nil
}

func (s *Scheduler) listDue(ctx context.Context) ([]dueCollection, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, id
		 FROM user_personal_collections
		 WHERE sync_schedule IS NOT NULL
		   AND next_sync_at IS NOT NULL
		   AND next_sync_at <= NOW()`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []dueCollection
	for rows.Next() {
		var dc dueCollection
		if err := rows.Scan(&dc.UserID, &dc.CollectionID); err != nil {
			return nil, err
		}
		out = append(out, dc)
	}
	return out, rows.Err()
}

func (s *Scheduler) syncOne(ctx context.Context, dc dueCollection, mu *sync.Mutex, result *SchedulerResult) {
	if _, loaded := s.inFlight.LoadOrStore(dc.CollectionID, struct{}{}); loaded {
		mu.Lock()
		result.Skipped++
		mu.Unlock()
		return
	}
	defer s.inFlight.Delete(dc.CollectionID)

	startedAt := time.Now()
	_, err := s.service.SyncCollection(ctx, dc.UserID, dc.CollectionID)
	dur := time.Since(startedAt).Round(time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		result.Failed++
		s.logger.ErrorContext(ctx, "user collection sync scheduler: sync failed",
			"user_id", dc.UserID,
			"collection_id", dc.CollectionID,
			"duration", dur,
			"error", err,
		)
		s.advanceAfterFailure(ctx, dc, time.Now())
		return
	}
	result.Synced++
	s.logger.InfoContext(ctx, "user collection sync scheduler: synced",
		"user_id", dc.UserID,
		"collection_id", dc.CollectionID,
		"duration", dur,
	)
}

// advanceAfterFailure pushes next_sync_at forward by the user-sync minimum
// interval so a broken source does not thrash the scheduler.
func (s *Scheduler) advanceAfterFailure(ctx context.Context, dc dueCollection, after time.Time) {
	next := after.Add(time.Duration(MinSyncIntervalHours) * time.Hour)
	if _, err := s.pool.Exec(ctx,
		`UPDATE user_personal_collections SET next_sync_at = $1 WHERE user_id = $2 AND id = $3`,
		next, dc.UserID, dc.CollectionID,
	); err != nil {
		s.logger.ErrorContext(ctx, "user collection sync scheduler: failed to advance next_sync_at after failure",
			"user_id", dc.UserID,
			"collection_id", dc.CollectionID,
			"error", err,
		)
	}
}

func (s *Scheduler) IsInFlight(collectionID string) bool {
	_, ok := s.inFlight.Load(collectionID)
	return ok
}

func marshalResult(r SchedulerResult) json.RawMessage {
	data, _ := json.Marshal(r)
	return data
}
