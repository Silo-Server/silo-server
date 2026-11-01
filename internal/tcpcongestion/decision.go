// Package tcpcongestion configures congestion control and send buffers on
// client-facing listeners without making kernel or helper refusals bind errors.
package tcpcongestion

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Result describes the requested and granted congestion control and send buffer
// for one listener. Errno and Error describe kernel failures; Action names the
// remedy, or "none" when no action is needed.
type Result struct {
	Listener, Requested, Granted, State, Reason, Errno, Error, Action string
	CapNetAdmin                                                       bool
	SendBuffer                                                        SendBufferResult
}

const (
	// ListenerAPI identifies the native API listener.
	ListenerAPI = "api"
	// ListenerJellyfin identifies the Jellyfin compatibility listener.
	ListenerJellyfin = "jellyfin"
	// ListenerABS identifies the Audiobookshelf compatibility listener.
	ListenerABS = "abs"
	// ListenerProxy identifies the proxy listener.
	ListenerProxy = "proxy"
)

// Options selects congestion control and supplies the kernel limits read at
// startup. Congestion enables BBR; SendBuffer is the force size in bytes, with
// zero disabling forcing. The kernel limits independently decide if it is needed.
// Linux listeners always set the unsent-data low-water mark independently.
type Options struct {
	Congestion bool
	SendBuffer int
	Limits     Limits
}

const (
	algorithmBBR              = "bbr"
	stateOn                   = "on"
	stateOff                  = "off"
	stateUnknown              = "unknown"
	stateCapped               = "capped"
	stateFallback             = "fallback"
	reasonDisabled            = "disabled"
	reasonModuleMissing       = "module_missing"
	reasonModuleNotLoaded     = "module_not_loaded"
	reasonOverridden          = "overridden"
	reasonUnsupportedPlatform = "unsupported_platform"
	reasonKernelError         = "kernel_error"
	reasonNotAllowed          = "not_allowed"
	reasonLimitsUnknown       = "limits_unknown"
	actionNone                = "none"
)

type input struct {
	listener    string
	enabled     bool
	linux       bool
	setErr      error
	readback    string
	readErr     error
	capNetAdmin bool
}

func decide(in input) Result {
	result := Result{Listener: in.listener, Requested: algorithmBBR, State: stateOff, Action: actionNone}
	if !in.enabled {
		result.Requested, result.Reason = stateOff, reasonDisabled
		return result
	}
	if !in.linux {
		result.Reason = reasonUnsupportedPlatform
		return result
	}
	result.CapNetAdmin = in.capNetAdmin
	if in.readErr == nil {
		result.Granted, _, _ = strings.Cut(in.readback, "\x00")
	}
	switch {
	case in.setErr != nil:
		switch {
		case errors.Is(in.setErr, syscall.ENOENT) && in.capNetAdmin:
			result.Reason = reasonModuleMissing
		case errors.Is(in.setErr, syscall.ENOENT):
			result.Reason = reasonModuleNotLoaded
		case errors.Is(in.setErr, syscall.EPERM) && !in.capNetAdmin:
			result.Reason = reasonNotAllowed
		default:
			result.Reason = reasonKernelError
			result.Errno, result.Error = errorDetails(in.setErr)
		}
	case in.readErr != nil:
		result.Reason = reasonKernelError
		result.Errno, result.Error = errorDetails(in.readErr)
	case result.Granted != algorithmBBR:
		result.Reason = reasonOverridden
	default:
		result.State = stateOn
		return result
	}
	result.State = stateFallback
	result.Action = congestionAction(result.Reason)
	return result
}

func congestionAction(reason string) string {
	switch reason {
	case reasonModuleMissing:
		return "Load tcp_bbr on the host (modprobe tcp_bbr). A rootless container cannot load kernel modules even with NET_ADMIN. If modprobe fails, this kernel has no tcp_bbr."
	case reasonModuleNotLoaded:
		return "Load tcp_bbr on the host and add bbr to net.ipv4.tcp_allowed_congestion_control, or grant NET_ADMIN (cap_add: [NET_ADMIN], as in the shipped docker-compose.yml)."
	case reasonNotAllowed:
		return "Add bbr to net.ipv4.tcp_allowed_congestion_control on the host, or grant NET_ADMIN (cap_add: [NET_ADMIN], as in the shipped docker-compose.yml)."
	case reasonKernelError:
		return "Report the errno in a Silo issue."
	case reasonOverridden:
		return "A route congctl setting or host policy overrides the socket; check ip route."
	default:
		return actionNone
	}
}

func errorDetails(err error) (errno, detail string) {
	var code syscall.Errno
	if errors.As(err, &code) {
		if name := unix.ErrnoName(code); name != "" {
			return name, ""
		}
	}
	return stateUnknown, err.Error()
}

func logResult(ctx context.Context, result Result) {
	attrs := []slog.Attr{
		slog.String("component", "tcpcongestion"),
		slog.String("listener", result.Listener),
		slog.String("requested", result.Requested),
		slog.String("granted", result.Granted),
		slog.String("state", result.State),
		slog.String("reason", result.Reason),
		slog.Bool("cap_net_admin", result.CapNetAdmin),
		slog.String("action", result.Action),
	}
	level := slog.LevelInfo
	if result.State == stateFallback {
		level = slog.LevelWarn
	}
	slog.LogAttrs(ctx, level, "tcp congestion control: listener state", withErrorAttrs(attrs, result.Errno, result.Error)...)
}

func withErrorAttrs(attrs []slog.Attr, errno, detail string) []slog.Attr {
	if errno != "" {
		attrs = append(attrs, slog.String("errno", errno))
		if detail != "" {
			attrs = append(attrs, slog.String("error", detail))
		}
	}
	return attrs
}
