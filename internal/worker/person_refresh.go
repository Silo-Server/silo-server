package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type PersonRefresher interface {
	RefreshPerson(ctx context.Context, id int64) (*models.Person, error)
	FindCandidates(ctx context.Context, limit int) ([]int64, error)
}

type PersonRefreshWorkerConfig struct {
	Interval       time.Duration
	Delay          time.Duration
	BatchSize      int
	RefreshTimeout time.Duration
}

func DefaultPersonRefreshWorkerConfig() PersonRefreshWorkerConfig {
	return PersonRefreshWorkerConfig{
		Interval:       10 * time.Minute,
		Delay:          200 * time.Millisecond,
		BatchSize:      100,
		RefreshTimeout: 2 * time.Minute,
	}
}

type PersonRefreshWorker struct {
	service PersonRefresher
	config  PersonRefreshWorkerConfig

	mu          sync.Mutex
	manualQueue []int64
	queued      map[int64]struct{}
	stop        chan struct{}
	wake        chan struct{}
}

func NewPersonRefreshWorker(service PersonRefresher, config PersonRefreshWorkerConfig) *PersonRefreshWorker {
	if config.Interval <= 0 {
		config.Interval = 10 * time.Minute
	}
	if config.Delay < 0 {
		config.Delay = 0
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 100
	}
	if config.RefreshTimeout <= 0 {
		config.RefreshTimeout = 2 * time.Minute
	}

	return &PersonRefreshWorker{
		service: service,
		config:  config,
		queued:  make(map[int64]struct{}),
		stop:    make(chan struct{}),
		wake:    make(chan struct{}, 1),
	}
}

func (w *PersonRefreshWorker) Enqueue(id int64) {
	if id <= 0 {
		return
	}

	w.mu.Lock()
	if _, exists := w.queued[id]; exists {
		w.mu.Unlock()
		return
	}
	w.queued[id] = struct{}{}
	w.manualQueue = append(w.manualQueue, id)
	w.mu.Unlock()

	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *PersonRefreshWorker) Start() {
	go func() {
		ticker := time.NewTicker(w.config.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-w.stop:
				return
			case <-w.wake:
				w.processBatch()
			case <-ticker.C:
				w.processBatch()
			}
		}
	}()
}

func (w *PersonRefreshWorker) Stop() {
	close(w.stop)
}

func (w *PersonRefreshWorker) processBatch() {
	if w.service == nil {
		return
	}

	batch := w.collectBatch()
	if len(batch) == 0 {
		return
	}

	for index, id := range batch {
		ctx, cancel := context.WithTimeout(context.Background(), w.config.RefreshTimeout)
		_, err := w.service.RefreshPerson(ctx, id)
		cancel()
		if err != nil {
			slog.Warn("person refresh worker: refresh failed", "person_id", id, "error", err)
		}

		w.mu.Lock()
		delete(w.queued, id)
		w.mu.Unlock()

		if w.config.Delay > 0 && index < len(batch)-1 {
			select {
			case <-time.After(w.config.Delay):
			case <-w.stop:
				return
			}
		}
	}
}

func (w *PersonRefreshWorker) collectBatch() []int64 {
	w.mu.Lock()
	manualCount := min(w.config.BatchSize, len(w.manualQueue))
	batch := append([]int64(nil), w.manualQueue[:manualCount]...)
	w.manualQueue = append([]int64(nil), w.manualQueue[manualCount:]...)
	queued := make(map[int64]struct{}, len(w.queued))
	for id := range w.queued {
		queued[id] = struct{}{}
	}
	w.mu.Unlock()

	if len(batch) >= w.config.BatchSize {
		return batch
	}

	candidates, err := w.service.FindCandidates(context.Background(), w.config.BatchSize-len(batch))
	if err != nil {
		slog.Warn("person refresh worker: failed to find candidates", "error", err)
		return batch
	}

	seen := make(map[int64]struct{}, len(batch))
	for _, id := range batch {
		seen[id] = struct{}{}
	}

	for _, id := range candidates {
		if _, exists := seen[id]; exists {
			continue
		}
		if _, exists := queued[id]; exists {
			continue
		}
		batch = append(batch, id)
		seen[id] = struct{}{}
		if len(batch) >= w.config.BatchSize {
			break
		}
	}

	return batch
}
