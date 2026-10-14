package main

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerregistration"
)

// Complete snapshots permit legacy misses only until their database-backed
// certificate expires. Database mutations drain every older certificate before
// committing; past worker origins remain in the snapshot as denials.
type coreWorkerRegistry struct {
	repo    *workerregistration.Repository
	mu      sync.RWMutex
	nodes   map[string]workercontrolcore.Node
	origins map[string]struct{}
	expires time.Time
	flights singleflight.Group
}

const workerRegistryRefreshTimeout = 10 * time.Second

func registrySnapshotValid(now, deadline time.Time) bool {
	// Monotonic time guards wall-clock corrections. Wall time also guards host
	// suspend on systems whose monotonic clock stops while the machine sleeps.
	return now.Before(deadline) && now.Round(0).Before(deadline.Round(0))
}

func (r *coreWorkerRegistry) refresh(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result := r.flights.DoChan("snapshot", func() (any, error) {
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerRegistryRefreshTimeout)
		defer cancel()
		snapshot, err := r.repo.Snapshot(readCtx)
		if err != nil {
			return nil, err
		}
		// The repository issued this deadline while holding the shared fence,
		// before reading data. Never extend it after the transaction releases.
		deadline := snapshot.ValidUntil
		nodes := make(map[string]workercontrolcore.Node, len(snapshot.Nodes))
		for _, node := range snapshot.Nodes {
			if canonical, err := workeridentity.ValidateOrigin(node.URL, node.AllowHTTP); err == nil {
				node.URL = canonical
			}
			origin := workerRegistryOrigin(node.URL)
			if previous, exists := nodes[origin]; exists && previous.ID != node.ID {
				// Canonical aliases belonging to different rows are ambiguous.
				// Keep a disabled sentinel so they cannot become legacy misses.
				node = workercontrolcore.Node{URL: origin}
			}
			nodes[origin] = node
		}
		origins := make(map[string]struct{}, len(snapshot.Origins))
		for _, raw := range snapshot.Origins {
			origins[workerRegistryOrigin(raw)] = struct{}{}
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if !registrySnapshotValid(time.Now(), deadline) || readCtx.Err() != nil {
			return nil, workercontrolcore.ErrUnavailable
		}
		if deadline.After(r.expires) {
			r.nodes, r.origins, r.expires = nodes, origins, deadline
		}
		return nil, nil
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case finished := <-result:
		if err := ctx.Err(); err != nil {
			return err
		}
		return finished.Err
	}
}

func (r *coreWorkerRegistry) cached(ctx context.Context, origin string) (workercontrolcore.Node, bool, error, bool) {
	r.mu.RLock()
	node, enrolled := r.nodes[origin]
	_, historical := r.origins[origin]
	deadline := r.expires
	r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return workercontrolcore.Node{}, false, err, true
	}
	if !registrySnapshotValid(time.Now(), deadline) {
		return workercontrolcore.Node{}, false, nil, false
	}
	if enrolled {
		return node, true, nil, true
	}
	if historical {
		return workercontrolcore.Node{}, false, workercontrolcore.ErrDenied, true
	}
	return workercontrolcore.Node{}, false, nil, true
}

func (r *coreWorkerRegistry) ByURL(ctx context.Context, raw string) (workercontrolcore.Node, bool, error) {
	origin, err := workeridentity.ValidateOrigin(raw, true)
	if err != nil {
		return workercontrolcore.Node{}, false, err
	}
	for range 2 {
		if node, enrolled, err, valid := r.cached(ctx, origin); valid {
			return node, enrolled, err
		}
		if err := r.refresh(ctx); err != nil {
			return workercontrolcore.Node{}, false, err
		}
	}
	return workercontrolcore.Node{}, false, workercontrolcore.ErrUnavailable
}

func workerRegistryOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	origin := parsed.Scheme + "://" + parsed.Host
	if canonical, err := workeridentity.ValidateOrigin(origin, true); err == nil {
		return canonical
	}
	return origin
}

func (r *coreWorkerRegistry) ByID(ctx context.Context, id int) (workercontrolcore.Node, bool, error) {
	return r.repo.ByID(ctx, id)
}
