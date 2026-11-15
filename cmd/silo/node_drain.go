package main

import (
	"context"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// nodeDrainTimeoutEnv bounds how long a transcode node that received SIGTERM
// keeps serving its sessions before it shuts down. 0 turns the drain off.
const nodeDrainTimeoutEnv = "SILO_NODE_DRAIN_TIMEOUT"

// defaultNodeDrainTimeout lets most episodes and a long tail of movie sessions
// finish, while keeping a rolling upgrade of several nodes bounded.
const defaultNodeDrainTimeout = 10 * time.Minute

// nodeDrainKeyCheckInterval is how often a SIGTERM drain asks whether the
// main server still accepts this node's key. The answer is an atomic read.
const nodeDrainKeyCheckInterval = time.Second

// nodeDrain is how startStandaloneServer drains a transcode node on SIGTERM.
type nodeDrain struct {
	timeout    time.Duration
	begin      func()
	wait       func(context.Context) error
	activeJobs func() int
	// keyRejected reports whether the main server refuses this node's key
	// (nodeconfig.Remote.KeyRejected). While it does, no relay can sign a
	// request to the node, so its sessions serve no viewer and nothing will
	// ever stop them: the drain does not wait for them. nil never rejects.
	keyRejected func() bool
}

// newTranscodeNodeDrain drains srv on SIGTERM for up to the configured
// timeout, or not at all when it is 0. keyRejected ends the wait early; see
// nodeDrain.keyRejected.
func newTranscodeNodeDrain(srv *transcodenode.Server, keyRejected func() bool) *nodeDrain {
	timeout := nodeDrainTimeout(os.Getenv(nodeDrainTimeoutEnv))
	if timeout <= 0 {
		return nil
	}
	return &nodeDrain{timeout: timeout, begin: srv.BeginShutdownDrain, wait: srv.WaitDrained, activeJobs: srv.ActiveJobs, keyRejected: keyRejected}
}

// nodeDrainTimeout parses SILO_NODE_DRAIN_TIMEOUT: a Go duration ("15m") or
// whole seconds ("900"). Empty or invalid means the default.
func nodeDrainTimeout(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultNodeDrainTimeout
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 && seconds <= math.MaxInt64/int(time.Second) {
		return time.Duration(seconds) * time.Second
	}
	if timeout, err := time.ParseDuration(value); err == nil && timeout >= 0 {
		return timeout
	}
	slog.Warn("invalid node drain timeout; using the default", "env", nodeDrainTimeoutEnv, "value", value, "default", defaultNodeDrainTimeout)
	return defaultNodeDrainTimeout
}

// run drains the node: it stops taking new work and waits until its work is
// done, the timeout passes, another signal arrives on sigCh, or the main
// server is found to refuse this node's key. The listener and every
// background loop keep running meanwhile, so the node keeps serving its
// sessions, answering health checks and reading its configuration, which is
// what notices a key that becomes rejected (or accepted again) mid-drain.
func (d *nodeDrain) run(sigCh <-chan os.Signal) {
	d.begin()
	if d.rejected() {
		d.stopUnreachable()
		return
	}
	slog.Info("draining before shutdown: taking no new work while active sessions finish",
		"timeout", d.timeout, "active_jobs", d.activeJobs())

	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- d.wait(ctx) }()
	var keyCheck <-chan time.Time
	if d.keyRejected != nil {
		ticker := time.NewTicker(nodeDrainKeyCheckInterval)
		defer ticker.Stop()
		keyCheck = ticker.C
	}
	for {
		select {
		case sig := <-sigCh:
			slog.Warn("second signal: stopping without waiting for the drain", "signal", sig, "active_jobs", d.activeJobs())
			return
		case err := <-done:
			if err != nil {
				slog.Warn("drain timed out; stopping the remaining work", "timeout", d.timeout, "active_jobs", d.activeJobs())
				return
			}
			slog.Info("drained: no work left", "waited", time.Since(started).Round(time.Millisecond))
			return
		case <-keyCheck:
			if d.rejected() {
				d.stopUnreachable()
				return
			}
		}
	}
}

func (d *nodeDrain) rejected() bool {
	return d.keyRejected != nil && d.keyRejected()
}

// stopUnreachable logs why the drain ends without waiting for active work.
func (d *nodeDrain) stopUnreachable() {
	slog.Warn("the main server no longer accepts this node's key, so its sessions cannot be served or stopped; shutting down without waiting",
		"active_jobs", d.activeJobs())
}

// subscribeNodeDrainChanges rechecks a transcode node when it announces a
// drain change (POST /api/internal/nodes/self/health-changed, which any API
// server publishes as node_drain_changed), so placement stops (or resumes) using it right away.
func subscribeNodeDrainChanges(ctx context.Context, bus cache.EventBus, healthChecker *nodepool.HealthChecker) {
	rechecks := newNodeRechecks(func(id int) { healthChecker.CheckTranscodeNode(ctx, id) })
	err := bus.Subscribe(ctx, cache.ChannelAdmin, func(event cache.Event) {
		if event.Type != cache.EventNodeDrainChanged {
			return
		}
		id, err := strconv.Atoi(event.Payload)
		if err != nil || id <= 0 {
			return
		}
		rechecks.request(id)
	})
	if err != nil {
		slog.WarnContext(ctx, "subscribe to node drain changes; drains reach placement at the next health sweep", "error", err)
	}
}

// nodeRechecks runs at most one check per node at a time. A request that
// arrives while one runs makes it run once more afterwards, so the last check
// always starts after the last announcement and a burst costs two checks.
type nodeRechecks struct {
	check func(id int)
	mu    sync.Mutex
	// rerun holds every node with a check running, and whether a request
	// arrived during it.
	rerun map[int]bool
}

func newNodeRechecks(check func(id int)) *nodeRechecks {
	return &nodeRechecks{check: check, rerun: map[int]bool{}}
}

func (r *nodeRechecks) request(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, running := r.rerun[id]; running {
		r.rerun[id] = true
		return
	}
	r.rerun[id] = false
	go func() {
		for {
			r.check(id)
			r.mu.Lock()
			again := r.rerun[id]
			if again {
				r.rerun[id] = false
			} else {
				delete(r.rerun, id)
			}
			r.mu.Unlock()
			if !again {
				return
			}
		}
	}()
}
