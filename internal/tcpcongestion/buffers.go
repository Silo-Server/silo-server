package tcpcongestion

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"syscall"

	"github.com/Silo-Server/silo-server/internal/netadmin"
)

const (
	sendThreshold = 25000000
	notSentLowat  = 131072

	reasonUnreadable          = "unreadable"
	reasonBelowThreshold      = "below_threshold"
	reasonNoNetAdmin          = "no_net_admin"
	reasonHelperUnavailable   = "helper_unavailable"
	reasonGrantedBelowRequest = "granted_below_request"
	reasonNotNeeded           = "not_needed"
)

// Limits describes the kernel TCP send and receive maxima in bytes. An unread
// maximum is zero. State and Reason describe the send maximum only.
type Limits struct {
	WmemMax, RmemMax int64
	State, Reason    string
}

// SendBufferResult describes the send-buffer request and readback in bytes.
// Requested is zero when no force is attempted, Granted is zero when unread,
// and NotSentLowat is the guard set independently of forcing. Errno and Error
// describe kernel failures; Action names the remedy or "none".
type SendBufferResult struct {
	Listener                            string
	Requested, Granted, NotSentLowat    int
	State, Reason, Errno, Error, Action string
}

type sendBufferInput struct {
	listener string
	request  int
	linux    bool
	limits   Limits
	helper   string
	lowatErr error
	forceErr error
	readErr  error
	granted  int
}

func decideLimits(wmem, rmem string, wmemErr, rmemErr error, linux bool) Limits {
	result := Limits{State: stateOff, Reason: reasonUnsupportedPlatform}
	if !linux {
		return result
	}
	if rmemErr == nil {
		result.RmemMax, _ = parseMaximum(rmem)
	}
	var parsed bool
	if wmemErr == nil {
		result.WmemMax, parsed = parseMaximum(wmem)
	}
	if !parsed {
		result.State, result.Reason = stateUnknown, reasonUnreadable
		return result
	}
	result.State, result.Reason = "ok", ""
	if result.WmemMax < sendThreshold {
		result.State, result.Reason = stateCapped, reasonBelowThreshold
	}
	return result
}

func parseMaximum(contents string) (int64, bool) {
	fields := strings.Fields(contents)
	if len(fields) < 3 {
		return 0, false
	}
	maximum, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return 0, false
	}
	return maximum, true
}

func needsSendBufferForce(request int, limits Limits) bool {
	return request > 0 && limits.State == stateCapped && 2*int64(request) > limits.WmemMax
}

func decideSendBuffer(in sendBufferInput) SendBufferResult {
	result := SendBufferResult{Listener: in.listener, State: stateOff, Action: actionNone}
	switch {
	case in.linux && in.lowatErr != nil:
		result.State, result.Reason = stateCapped, reasonKernelError
		result.Errno, result.Error = errorDetails(in.lowatErr)
	case in.request == 0:
		result.Reason = reasonDisabled
	case !in.linux:
		result.Reason = reasonUnsupportedPlatform
	case in.limits.State == stateUnknown:
		result.Reason = reasonLimitsUnknown
	case in.limits.State == "ok":
		result.Reason = reasonNotNeeded
	case in.limits.State != stateCapped:
		result.Reason = reasonLimitsUnknown
	case !needsSendBufferForce(in.request, in.limits):
		result.Reason = reasonNotNeeded
	case in.helper == netadmin.HelperNone:
		result.State, result.Reason = stateCapped, reasonNoNetAdmin
	case in.helper != netadmin.HelperRunning:
		result.State, result.Reason = stateCapped, reasonHelperUnavailable
	default:
		result = decideForce(in)
	}
	if in.linux && in.lowatErr == nil {
		result.NotSentLowat = notSentLowat
	}
	result.Action = sendBufferAction(result.Reason)
	return result
}

func decideForce(in sendBufferInput) SendBufferResult {
	result := SendBufferResult{Listener: in.listener, State: stateCapped}
	result.Requested = in.request
	if in.forceErr != nil {
		if in.forceErr == syscall.EPERM { //nolint:errorlint // A kernel refusal is identified by its bare errno.
			result.Reason = reasonNotAllowed
			return result
		}
		result.Reason = reasonKernelError
		result.Errno, result.Error = errorDetails(in.forceErr)
		return result
	}
	if in.readErr != nil {
		result.Reason = reasonKernelError
		result.Errno, result.Error = errorDetails(in.readErr)
		return result
	}
	result.Granted = in.granted
	if in.granted < 2*in.request {
		result.Reason = reasonGrantedBelowRequest
		return result
	}
	result.State = stateOn
	return result
}

func sendBufferAction(reason string) string {
	switch reason {
	case reasonNoNetAdmin:
		return "Grant NET_ADMIN so Silo forces a larger send buffer on its listeners (cap_add: [NET_ADMIN], as in the shipped docker-compose.yml; Kubernetes Pod Security Baseline refuses it). Or raise the maximum (third value) of net.ipv4.tcp_wmem to 67108864 in Silo's network namespace: a container sysctl on a bridge network, the host with network_mode: host or no container, or a Kubernetes pod sysctl (a safe sysctl from Kubernetes 1.32 on Linux 4.15 or later; older clusters must allow it)."
	case reasonHelperUnavailable:
		return "Raise the maximum (third value) of net.ipv4.tcp_wmem to 67108864 in Silo's network namespace: a container sysctl on a bridge network, the host with network_mode: host or no container, or a Kubernetes pod sysctl (a safe sysctl from Kubernetes 1.32 on Linux 4.15 or later; older clusters must allow it). The net admin line says why Silo cannot force a larger send buffer."
	case reasonNotAllowed:
		return "This container's NET_ADMIN cannot force buffers (for example rootless Docker or Podman). Raise the maximum (third value) of net.ipv4.tcp_wmem to 67108864 in Silo's network namespace instead."
	case reasonKernelError:
		return "Report the errno in a Silo issue."
	case reasonGrantedBelowRequest:
		return "Report the granted value in a Silo issue."
	case reasonLimitsUnknown:
		return "Check that /proc/sys/net/ipv4/tcp_wmem is readable where Silo runs."
	default:
		return actionNone
	}
}

func logLimits(ctx context.Context, limits Limits) {
	slog.LogAttrs(ctx, slog.LevelInfo, "tcp buffer limits: kernel state",
		slog.String("component", "tcpcongestion"),
		slog.Int64("tcp_wmem_max", limits.WmemMax),
		slog.Int64("tcp_rmem_max", limits.RmemMax),
		slog.Int("threshold", sendThreshold),
		slog.String("state", limits.State),
		slog.String("reason", limits.Reason),
	)
}

func logSendBuffer(ctx context.Context, result SendBufferResult) {
	attrs := []slog.Attr{
		slog.String("component", "tcpcongestion"),
		slog.String("listener", result.Listener),
		slog.Int("requested", result.Requested),
		slog.Int("granted", result.Granted),
		slog.Int("notsent_lowat", result.NotSentLowat),
		slog.String("state", result.State),
		slog.String("reason", result.Reason),
		slog.String("action", result.Action),
	}
	level := slog.LevelInfo
	if result.State == stateCapped || result.Reason == reasonLimitsUnknown {
		level = slog.LevelWarn
	}
	slog.LogAttrs(ctx, level, "tcp send buffer: listener state", withErrorAttrs(attrs, result.Errno, result.Error)...)
}
