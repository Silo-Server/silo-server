package pluginhost

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
)

const (
	// EnvIdleStopAfter sets how long a lazily started plugin process may sit
	// unused before the host stops it: a Go duration or whole seconds. Zero
	// keeps every plugin running.
	EnvIdleStopAfter = "SILO_PLUGIN_IDLE_STOP_AFTER"
	// DefaultIdleStopAfter is long enough that a plugin called in bursts (a
	// scan, a metadata refresh, someone browsing) stays up across the burst.
	DefaultIdleStopAfter = 15 * time.Minute
	// MinIdleStopAfter keeps a configured period well above the longest gap
	// between a caller resolving a plugin client and making its call.
	MinIdleStopAfter = time.Minute
	// DefaultIdleCheckInterval is how often a running plugin is checked.
	DefaultIdleCheckInterval = 30 * time.Second

	maxIdleStopAfter = time.Duration(1<<63 - 1)

	// pluginServicePrefix selects the Silo plugin protocol. The health probe
	// and go-plugin's own broker, stdio and controller calls share the
	// connection and say nothing about whether the plugin is in use.
	pluginServicePrefix = "/silo.plugin.v1."
)

// ParseIdleStopAfter reads EnvIdleStopAfter's value. Empty takes the default
// and zero disables idle stops. A shorter period than MinIdleStopAfter is
// raised to it. An unreadable value is an error and yields the default.
func ParseIdleStopAfter(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultIdleStopAfter, nil
	}
	period, err := time.ParseDuration(value)
	if err != nil {
		seconds, secondsErr := strconv.ParseInt(value, 10, 64)
		if secondsErr != nil || seconds > int64(maxIdleStopAfter/time.Second) {
			return DefaultIdleStopAfter, fmt.Errorf("invalid duration %q: %w", value, err)
		}
		period = time.Duration(seconds) * time.Second
	}
	switch {
	case period < 0:
		return DefaultIdleStopAfter, fmt.Errorf("negative duration %q", value)
	case period == 0:
		return 0, nil
	case period < MinIdleStopAfter:
		return MinIdleStopAfter, nil
	}
	return period, nil
}

// activity records when one plugin process was last used and how many calls
// are in flight, in either direction. The idle watcher reads it to decide
// whether the process can be stopped.
type activity struct {
	now func() time.Time

	mu       sync.Mutex
	inFlight int
	last     time.Time
	// pinned marks a process that opened a plugin protocol stream. Every
	// capability RPC is unary today; a stream has no end this type can see,
	// so a process that opens one is never stopped for being idle.
	pinned bool
}

func newActivity(now func() time.Time) *activity {
	return &activity{now: now, last: now()}
}

func (a *activity) touch() {
	a.mu.Lock()
	a.last = a.now()
	a.mu.Unlock()
}

func (a *activity) begin() {
	a.mu.Lock()
	a.inFlight++
	a.last = a.now()
	a.mu.Unlock()
}

func (a *activity) end() {
	a.mu.Lock()
	a.inFlight--
	a.last = a.now()
	a.mu.Unlock()
}

// idleFor reports whether nothing is in flight and nothing has used the
// process for at least period.
func (a *activity) idleFor(period time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.pinned && a.inFlight == 0 && a.now().Sub(a.last) >= period
}

// unaryClient counts host calls to the plugin for as long as they run.
func (a *activity) unaryClient(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if !strings.HasPrefix(method, pluginServicePrefix) {
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	a.begin()
	defer a.end()
	return invoker(ctx, method, req, reply, cc, opts...)
}

func (a *activity) streamClient(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	if strings.HasPrefix(method, pluginServicePrefix) {
		a.mu.Lock()
		a.pinned = true
		a.mu.Unlock()
	}
	return streamer(ctx, desc, cc, method, opts...)
}

// unaryServer counts the plugin's calls back to the host. A callback with no
// host call in flight is the only sign of work the plugin does on its own.
func (a *activity) unaryServer(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	a.begin()
	defer a.end()
	return handler(ctx, req)
}

// watchIdle stops the instance once it has been idle for the host's idle
// period. It runs only for instances started with StopWhenIdle.
func (h *Host) watchIdle(ctx context.Context, installationID int, instance *instance) {
	ticker := time.NewTicker(h.idleCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if h.stopIfIdle(ctx, installationID, instance) {
				return
			}
		}
	}
}

// stopIfIdle stops the instance when it is still the one the host maps and
// has been idle for the idle period. This is a deliberate stop: the instance
// is not marked unhealthy and the exit handler is not told.
//
// The idle check and the removal happen under h.mu, the lock Client takes to
// record a use, so a caller either finds the instance and keeps it alive or
// finds none and starts a new one. The start slot is held until the old
// process has exited, so that new start never overlaps it, and Stop and
// Shutdown wait for the same exit through h.idleStopping.
func (h *Host) stopIfIdle(ctx context.Context, installationID int, instance *instance) bool {
	if !instance.activity.idleFor(h.idleStopAfter) {
		return false
	}
	// A Start already in flight for the installation replaces this instance
	// and cancels ctx, which ends the wait.
	if err := h.acquireStart(ctx, installationID); err != nil {
		return false
	}
	defer h.releaseStart(installationID)

	h.mu.Lock()
	current, ok := h.instances[installationID]
	if !ok || current != instance || !instance.activity.idleFor(h.idleStopAfter) {
		h.mu.Unlock()
		return false
	}
	delete(h.instances, installationID)
	exited := make(chan struct{})
	h.idleStopping[installationID] = exited
	h.mu.Unlock()

	h.logger.Debug("stopping idle plugin", "installation_id", installationID, "idle_for", h.idleStopAfter)
	h.teardown(instance, true)

	h.mu.Lock()
	delete(h.idleStopping, installationID)
	h.mu.Unlock()
	close(exited)
	return true
}
