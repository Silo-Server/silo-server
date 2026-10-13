package workercontrol

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

// Config returns an immutable published snapshot, matching the existing
// watcher's pointer-identity reload fence. Callers must not modify it. The
// local allowlist never populates secret/customer settings in this Config.
func (c *localConfig) Config() *config.Config {
	c.mu.RLock()
	current := c.cfg
	c.mu.RUnlock()
	return current
}

func (c *localConfig) NodeDrainRequested() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.execution.Drain
}

func (c *localConfig) OutputAuthorizationEpoch() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.execution.OutputAuthorizationEpoch
}

func (c *localConfig) OnNodeDrainChange(callback func(bool)) {
	c.mu.Lock()
	c.drainCallback = callback
	c.mu.Unlock()
}

func (c *localConfig) setRefresh(refresh func(context.Context) error) {
	c.mu.Lock()
	c.refresh = refresh
	c.mu.Unlock()
}

func (c *localConfig) ForceReload(ctx context.Context) error {
	c.mu.RLock()
	refresh := c.refresh
	c.mu.RUnlock()
	if refresh == nil {
		return errors.New("worker signed settings refresh unavailable")
	}
	return refresh(ctx)
}

func (c *localConfig) check(next workerwire.ExecutionConfig) error {
	if err := next.Validate(); err != nil {
		return err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.execution.Revision == next.Revision && c.execution != next {
		return errors.New("worker execution revision changed without new identity")
	}
	return nil
}

// install returns drain notification work separately so a refresh can release
// its reconcile lock before a callback reads state or starts another refresh.
func (c *localConfig) install(next workerwire.ExecutionConfig) (func(), error) {
	if err := next.Validate(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.execution.Revision == next.Revision && c.execution != next {
		c.mu.Unlock()
		return nil, errors.New("worker execution revision changed without new identity")
	}
	if c.execution == next {
		c.mu.Unlock()
		return nil, nil
	}
	copy := *c.cfg
	copy.Playback.SegmentRetentionSeconds = next.SegmentRetentionSeconds
	capacity := next.SubtitleSyncCapacity
	if c.maxJobs > 0 && capacity > c.maxJobs {
		capacity = c.maxJobs
	}
	copy.Playback.SubtitleSyncNodeCapacity = capacity
	changed := c.execution.Drain != next.Drain
	callback := c.drainCallback
	c.cfg, c.execution = &copy, next
	c.mu.Unlock()
	if changed && callback != nil {
		return func() { callback(next.Drain) }, nil
	}
	return nil, nil
}

func (c *localConfig) apply(next workerwire.ExecutionConfig) error {
	notify, err := c.install(next)
	if notify != nil {
		notify()
	}
	return err
}
