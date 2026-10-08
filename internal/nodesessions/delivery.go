package nodesessions

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	deliveryKeyPrefix = "silo:playback-delivery:"

	// DeliveryWindow is how long one recorded media delivery stays readable.
	// The API reads it on every sweep, so it only has to outlast the gap
	// between sweeps; it is kept longer than the API's five-minute idle grace
	// for remote-transport sessions so that a late or failed sweep still finds
	// the last delivery and ends the session one grace after it.
	DeliveryWindow = 6 * time.Minute

	// deliveryWriteInterval throttles writes per session: the proxy reports
	// every chunk of media it sends, and one write per interval is enough to
	// keep the key alive.
	deliveryWriteInterval = 20 * time.Second

	deliveryWriteTimeout = 2 * time.Second
)

// RecordDelivery notes that this node just served playback media for
// sessionID to a client. The API's idle sweep reads these records so a session
// whose bytes this node serves stays alive while the client is still pulling
// media, even when nothing reports progress (a Cast receiver whose sender phone
// went to sleep).
//
// The proxy calls it for every chunk of media a client receives. Writes are
// throttled per session and run off the request path, so a slow or unavailable
// Redis never delays media.
func (tr *Tracker) RecordDelivery(sessionID string) {
	if tr == nil || tr.rdb == nil || sessionID == "" {
		return
	}
	now := time.Now()
	tr.mu.Lock()
	if last, ok := tr.delivered[sessionID]; ok && now.Sub(last) < deliveryWriteInterval {
		tr.mu.Unlock()
		return
	}
	tr.delivered[sessionID] = now
	tr.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), deliveryWriteTimeout)
		defer cancel()
		// A failed write is retried after the interval like any other: the record
		// outlives several missed writes, and retrying on every chunk of media
		// would flood a Redis that is already failing.
		if err := tr.rdb.Set(ctx, deliveryKeyPrefix+sessionID, 1, DeliveryWindow).Err(); err != nil {
			slog.Debug("playback delivery record failed", "component", "nodesessions", "error", err, "session", sessionID)
		}
	}()
}

// pruneDeliveredLocked drops throttle entries whose Redis record has expired.
func (tr *Tracker) pruneDeliveredLocked(now time.Time) {
	for id, last := range tr.delivered {
		if now.Sub(last) > DeliveryWindow {
			delete(tr.delivered, id)
		}
	}
}

// RecentDeliveries reports when a node last recorded media delivery for each
// of sessionIDs, omitting sessions with no delivery within DeliveryWindow.
//
// The time comes from the record's remaining TTL, which Redis counts on its own
// clock, so node and API clocks never need to agree. It is accurate to within
// the write throttle.
//
// The API's idle sweep waits on this lookup, so rdb should honor ctx's deadline
// on its sockets (cache.NewDeadlineRedisClientForRole); otherwise a Redis that
// stops answering holds the sweep for the client's read timeout.
func RecentDeliveries(ctx context.Context, rdb *redis.Client, sessionIDs []string) (map[string]time.Time, error) {
	if rdb == nil || len(sessionIDs) == 0 {
		return nil, nil
	}
	pipe := rdb.Pipeline()
	cmds := make([]*redis.DurationCmd, len(sessionIDs))
	for i, id := range sessionIDs {
		cmds[i] = pipe.PTTL(ctx, deliveryKeyPrefix+id)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	now := time.Now()
	recent := make(map[string]time.Time)
	for i, cmd := range cmds {
		// PTTL is negative for a missing key or one without an expiry; neither
		// is a delivery record this package wrote.
		remaining := cmd.Val()
		if remaining <= 0 {
			continue
		}
		recent[sessionIDs[i]] = now.Add(-max(DeliveryWindow-remaining, 0))
	}
	return recent, nil
}
