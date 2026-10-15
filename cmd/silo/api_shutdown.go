package main

import (
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// apiShutdownDelayEnv sets how long an API process keeps serving after it
// fails readiness on shutdown. 0 stops at once.
const apiShutdownDelayEnv = "SILO_SHUTDOWN_DELAY"

// defaultAPIShutdownDelay covers a readiness probe period or two of a load
// balancer and Kubernetes' endpoint propagation, while keeping a restart of a
// single server quick.
const defaultAPIShutdownDelay = 5 * time.Second

// apiShutdownDelay parses SILO_SHUTDOWN_DELAY: a Go duration ("10s") or whole
// seconds ("10"). Empty or invalid means the default.
func apiShutdownDelay(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultAPIShutdownDelay
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 && seconds <= maxDurationSeconds {
		return time.Duration(seconds) * time.Second
	}
	if delay, err := time.ParseDuration(value); err == nil && delay >= 0 {
		return delay
	}
	slog.Warn("invalid shutdown delay; using the default", "env", apiShutdownDelayEnv, "value", value, "default", defaultAPIShutdownDelay)
	return defaultAPIShutdownDelay
}

// maxDurationSeconds is the most whole seconds a time.Duration holds.
const maxDurationSeconds = int(math.MaxInt64 / int64(time.Second))

// waitForAPIStop blocks until the API process must shut down. A signal calls
// beginRouteAway, which fails readiness, and keeps serving for delay, so load
// balancers and Kubernetes stop sending new requests before the listeners
// close. A restart request or a listener failure stops at once, before or
// during that wait, and so does a second signal during it.
func waitForAPIStop(signals <-chan os.Signal, restarts <-chan struct{}, serverErrs <-chan error, delay time.Duration, beginRouteAway func()) {
	var routedAway <-chan time.Time
	for {
		select {
		case sig := <-signals:
			if routedAway != nil {
				slog.Warn("second signal: stopping without waiting for traffic to route away", "signal", sig)
				return
			}
			slog.Info("received signal, shutting down", "signal", sig)
			beginRouteAway()
			if delay <= 0 {
				return
			}
			slog.Info("failing readiness before shutdown so traffic routes away", "delay", delay)
			routedAway = time.After(delay)
		case <-routedAway:
			return
		case <-restarts:
			slog.Info("server restart requested, shutting down")
			return
		case serverErr := <-serverErrs:
			slog.Error("server error, shutting down", "error", serverErr)
			return
		}
	}
}
