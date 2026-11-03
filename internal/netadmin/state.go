// Package netadmin installs a C constructor when imported on Linux with cgo
// that runs before the Go runtime, starts a helper only with effective NET_ADMIN,
// and clears residual NET_ADMIN grants from the process. The helper
// sets TCP_CONGESTION to bbr and SO_SNDBUFFORCE to a validated byte size on TCP
// sockets. Release closes its channel and reaps it after listeners are configured.
package netadmin

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// State describes the process's initial NET_ADMIN capability and containment.
type State struct {
	HeldAtStart     bool   // NET_ADMIN was in the effective set before the constructor drop
	Helper          string // one of the Helper* constants
	ChildrenInherit bool   // readback finds retained NET_ADMIN or an unprotected bounding bit, or cannot prove containment
	NoNewPrivs      bool   // no_new_privs read back after the drop
	Threads         int    // entries in /proc/self/task at constructor entry; 0 when not read or no constructor ran
	Errno           string // symbolic errno of the first failed constructor step, or ""
}

const (
	// HelperRunning means the helper is available for socket operations.
	HelperRunning = "running"
	// HelperNone means effective NET_ADMIN was absent at process start.
	HelperNone = "none"
	// HelperStartFailed means the helper could not start; the drop still runs.
	HelperStartFailed = "start_failed"
	// HelperInspectFailed means the constructor could not read capabilities.
	HelperInspectFailed = "inspect_failed"
	// HelperUnsupportedPlatform means the platform has no NET_ADMIN helper.
	HelperUnsupportedPlatform = "unsupported_platform"
	// HelperReleased means a previously running helper has been released.
	HelperReleased = "released"
)

// ErrUnavailable means no helper is available for socket operations.
var ErrUnavailable = errors.New("netadmin: helper not running")

// LogState logs the NET_ADMIN state once, before any call to Release.
func LogState(ctx context.Context) {
	logStateOnce.Do(func() {
		state := Current()
		attrs := []slog.Attr{
			slog.String("component", "netadmin"),
			slog.Bool("held_at_start", state.HeldAtStart),
			slog.String("helper", state.Helper),
			slog.Bool("children_inherit", state.ChildrenInherit),
			slog.Bool("no_new_privs", state.NoNewPrivs),
			slog.Int("threads", state.Threads),
			slog.String("action", stateAction(state)),
		}
		if state.Errno != "" {
			attrs = append(attrs, slog.String("errno", state.Errno))
		}
		level := slog.LevelInfo
		if state.ChildrenInherit || state.Helper == HelperStartFailed || state.Helper == HelperInspectFailed ||
			(state.Helper == HelperRunning && state.Threads != 1) {
			level = slog.LevelWarn
		}
		slog.LogAttrs(ctx, level, "net admin: capability state", attrs...)
	})
}

var logStateOnce sync.Once

func stateAction(state State) string {
	switch {
	case state.Helper == HelperInspectFailed:
		return "Remove NET_ADMIN, or report the errno in a Silo issue: Silo could not read its capabilities, so it cannot confirm that it and the programs it starts run without NET_ADMIN."
	case state.ChildrenInherit:
		return "Remove NET_ADMIN, or report the errno in a Silo issue: Silo could not stop programs it starts from regaining NET_ADMIN."
	case (state.Helper == HelperRunning || state.Helper == HelperStartFailed) && state.Threads != 1:
		return "Report this line in a Silo issue: Silo could not confirm that it dropped NET_ADMIN before any other thread started."
	case state.Helper == HelperStartFailed:
		return "Report the errno in a Silo issue. Silo dropped NET_ADMIN anyway; kernel BBR and the send buffer use the unprivileged path."
	default:
		return "none"
	}
}
