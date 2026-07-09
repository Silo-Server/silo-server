package nodepool

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// healthResponse is the JSON response from a node's /health endpoint.
type healthResponse struct {
	Status     string `json:"status"`
	ActiveJobs int    `json:"active_jobs"`
	EgressKbps int    `json:"egress_kbps"`
}

// CheckNode pings a node's /health endpoint and returns its health status,
// active job count, and reported egress bandwidth.
func CheckNode(ctx context.Context, n *Node) (healthy bool, activeJobs, egressKbps int) {
	client := &http.Client{Timeout: 5 * time.Second}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.URL+"/api/v1/health", nil)
	if err != nil {
		return false, 0, 0
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, 0, 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, 0, 0
	}

	var hr healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		return false, 0, 0
	}

	return true, hr.ActiveJobs, hr.EgressKbps
}

// HealthChecker runs periodic health checks on all nodes in both pools,
// updating in-memory state and optionally persisting to the database.
type HealthChecker struct {
	proxyPool     *ProxyPool
	transcodePool *TranscodePool
	repo          *Repository // may be nil (proxy/transcode modes have no DB)
	interval      time.Duration
}

// NewHealthChecker creates a health checker for the given pools.
func NewHealthChecker(proxyPool *ProxyPool, transcodePool *TranscodePool, repo *Repository) *HealthChecker {
	return &HealthChecker{
		proxyPool:     proxyPool,
		transcodePool: transcodePool,
		repo:          repo,
		interval:      30 * time.Second,
	}
}

// Start runs health checks in a background goroutine. Stops when ctx is cancelled.
func (hc *HealthChecker) Start(ctx context.Context) {
	go func() {
		hc.checkAll(ctx)
		ticker := time.NewTicker(hc.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hc.checkAll(ctx)
			}
		}
	}()
}

func (hc *HealthChecker) checkAll(ctx context.Context) {
	var wg sync.WaitGroup
	check := func(n *Node, applyHealth func(int, bool, int, int, time.Time)) {
		wg.Go(func() {
			healthy, activeJobs, egressKbps := CheckNode(ctx, n)

			// Publish the result through the pool lock so readers never see
			// a Node struct mutated in place (the pool swaps in a copy).
			applyHealth(n.ID, healthy, activeJobs, egressKbps, time.Now())

			if n.Healthy && !healthy {
				slog.WarnContext(ctx, "stream node unhealthy", "component", "nodepool", "id", n.ID, "name", n.Name, "url", n.URL)
			} else if !n.Healthy && healthy {
				slog.InfoContext(ctx, "stream node recovered", "component", "nodepool", "id", n.ID, "name", n.Name, "url", n.URL)
			}

			if hc.repo != nil {
				if err := hc.repo.UpdateHealth(ctx, n.ID, healthy, activeJobs, egressKbps); err != nil {
					slog.ErrorContext(ctx, "failed to persist node health", "component", "nodepool", "id", n.ID, "error", err)
				}
			}
		})
	}
	for _, n := range hc.proxyPool.Nodes() {
		check(n, hc.proxyPool.ApplyHealth)
	}
	for _, n := range hc.transcodePool.Nodes() {
		check(n, hc.transcodePool.ApplyHealth)
	}
	wg.Wait()
}
