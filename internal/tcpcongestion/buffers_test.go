package tcpcongestion

import (
	"syscall"
	"testing"

	"github.com/Silo-Server/silo-server/internal/netadmin"
)

func TestDecideLimits(t *testing.T) {
	const stockWmem = "4096\t16384\t4194304\n"
	const stockRmem = "4096\t131072\t6291456\n"
	tests := []struct {
		name, wmem       string
		wmemErr, rmemErr error
		notLinux         bool
		state, reason    string
		wmemMax, rmemMax int64
	}{
		{name: "capped", wmem: stockWmem, state: "capped", reason: "below_threshold", wmemMax: 4194304, rmemMax: 6291456},
		{name: "threshold", wmem: "4096 16384 25000000", state: "ok", wmemMax: 25000000, rmemMax: 6291456},
		{name: "below threshold", wmem: "4096 16384 24999999", state: "capped", reason: "below_threshold", wmemMax: 24999999, rmemMax: 6291456},
		{name: "wmem unreadable", wmemErr: syscall.ENOENT, state: "unknown", reason: "unreadable", rmemMax: 6291456},
		{name: "wmem malformed", wmem: "garbage", state: "unknown", reason: "unreadable", rmemMax: 6291456},
		{name: "rmem unreadable", wmem: "4096 16384 67108864", rmemErr: syscall.ENOENT, state: "ok", wmemMax: 67108864},
		{name: "not linux", notLinux: true, state: "off", reason: "unsupported_platform"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideLimits(tt.wmem, stockRmem, tt.wmemErr, tt.rmemErr, !tt.notLinux)
			if got.State != tt.state || got.Reason != tt.reason {
				t.Errorf("state/reason = %q/%q, want %q/%q", got.State, got.Reason, tt.state, tt.reason)
			}
			if got.WmemMax != tt.wmemMax || got.RmemMax != tt.rmemMax {
				t.Errorf("WmemMax/RmemMax = %d/%d, want %d/%d", got.WmemMax, got.RmemMax, tt.wmemMax, tt.rmemMax)
			}
		})
	}
}

func TestDecideSendBuffer(t *testing.T) {
	tests := []struct {
		name                          string
		off                           bool
		request                       int
		notLinux                      bool
		limits, helper                string
		wmemMax                       int64
		lowatErr, forceErr, readErr   error
		granted                       int
		state, reason, errno          string
		requested, wantGranted, lowat int
	}{
		{name: "disabled", off: true, state: "off", reason: "disabled", lowat: 131072},
		{name: "custom size", request: 16777216, granted: 33554432, state: "on", requested: 16777216, wantGranted: 33554432, lowat: 131072},
		{name: "custom grant too small", request: 16777216, granted: 33554431, state: "capped", reason: "granted_below_request", requested: 16777216, wantGranted: 33554431, lowat: 131072},
		{name: "not linux", notLinux: true, state: "off", reason: "unsupported_platform"},
		{name: "not needed", limits: "ok", state: "off", reason: "not_needed", lowat: 131072},
		{name: "custom size within maximum", request: 2097152, wmemMax: 4194304, state: "off", reason: "not_needed", lowat: 131072},
		{name: "limits unknown", limits: "unknown", state: "off", reason: "limits_unknown", lowat: 131072},
		{name: "no helper", helper: netadmin.HelperNone, state: "capped", reason: "no_net_admin", lowat: 131072},
		{name: "helper start failed", helper: netadmin.HelperStartFailed, state: "capped", reason: "helper_unavailable", lowat: 131072},
		{name: "lowat failed", lowatErr: syscall.EINVAL, state: "capped", reason: "kernel_error", errno: "EINVAL"},
		{name: "lowat failed with force disabled", off: true, lowatErr: syscall.EINVAL, state: "capped", reason: "kernel_error", errno: "EINVAL"},
		{name: "force refused", forceErr: syscall.EPERM, state: "capped", reason: "not_allowed", requested: 33554432, lowat: 131072},
		{name: "force no memory", forceErr: syscall.ENOMEM, state: "capped", reason: "kernel_error", errno: "ENOMEM", requested: 33554432, lowat: 131072},
		{name: "readback failed", readErr: syscall.EBADF, state: "capped", reason: "kernel_error", errno: "EBADF", requested: 33554432, lowat: 131072},
		{name: "helper inspect failed", helper: netadmin.HelperInspectFailed, state: "capped", reason: "helper_unavailable", lowat: 131072},
		{name: "grant too small", granted: 33554432, state: "capped", reason: "granted_below_request", requested: 33554432, wantGranted: 33554432, lowat: 131072},
		{name: "forced", granted: 67108864, state: "on", requested: 33554432, wantGranted: 67108864, lowat: 131072},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits, helper := tt.limits, tt.helper
			if limits == "" {
				limits = "capped"
			}
			if helper == "" {
				helper = netadmin.HelperRunning
			}
			request := tt.request
			if request == 0 && !tt.off {
				request = 33554432
			}
			got := decideSendBuffer(sendBufferInput{
				listener: ListenerAPI, request: request, linux: !tt.notLinux, limits: Limits{State: limits, WmemMax: tt.wmemMax}, helper: helper,
				lowatErr: tt.lowatErr, forceErr: tt.forceErr, readErr: tt.readErr, granted: tt.granted,
			})
			if got.State != tt.state || got.Reason != tt.reason || got.Errno != tt.errno {
				t.Errorf("state/reason/errno = %q/%q/%q, want %q/%q/%q", got.State, got.Reason, got.Errno, tt.state, tt.reason, tt.errno)
			}
			if got.Requested != tt.requested || got.Granted != tt.wantGranted || got.NotSentLowat != tt.lowat {
				t.Errorf("Requested/Granted/NotSentLowat = %d/%d/%d, want %d/%d/%d", got.Requested, got.Granted, got.NotSentLowat, tt.requested, tt.wantGranted, tt.lowat)
			}
			if tt.state == "capped" && (got.Action == "" || got.Action == "none") {
				t.Errorf("Action = %q, want a remedy", got.Action)
			}
		})
	}
}
