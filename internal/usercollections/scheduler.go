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

	"github.com/Silo-Server/silo-server/internal/collectionutil"
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

	if claimed, err := s.claim(ctx, dc); !claimed {
		if err != nil {
			s.logger.ErrorContext(ctx, "user collection sync scheduler: failed to claim a due collection",
				"user_id", dc.UserID,
				"collection_id", dc.CollectionID,
				"error", err,
			)
		}
		mu.Lock()
		result.Skipped++
		mu.Unlock()
		return
	}

	startedAt := time.Now()
	syncCtx, cancel := context.WithTimeout(ctx, collectionutil.SyncTimeout)
	_, err := s.service.SyncCollection(syncCtx, dc.UserID, dc.CollectionID)
	cancel()
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
		return
	}
	result.Synced++
	s.logger.InfoContext(ctx, "user collection sync scheduler: synced",
		"user_id", dc.UserID,
		"collection_id", dc.CollectionID,
		"duration", dur,
	)
}

// claim takes a due collection for this node before its sync runs. Every
// node runs the scheduler, so two can list the same collection; the claim
// moves next_sync_at past the user-sync minimum interval while it is still
// due, and only the node whose UPDATE does that runs the sync. The moved
// next_sync_at is also the retry time when the sync fails, and the sync
// starts from it, so its own next run replaces it on success and a schedule
// edited while the sync ran keeps what the edit wrote. "Still due" and the
// retry time use the database clock, as listDue does.
func (s *Scheduler) claim(ctx context.Context, dc dueCollection) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE user_personal_collections
		 SET next_sync_at = NOW() + make_interval(hours => $1)
		 WHERE user_id = $2 AND id = $3
		   AND sync_schedule IS NOT NULL
		   AND next_sync_at <= NOW()`,
		MinSyncIntervalHours, dc.UserID, dc.CollectionID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Scheduler) IsInFlight(collectionID string) bool {
	_, ok := s.inFlight.Load(collectionID)
	return ok
}

func marshalResult(r SchedulerResult) json.RawMessage {
	data, _ := json.Marshal(r)
	return data
}
