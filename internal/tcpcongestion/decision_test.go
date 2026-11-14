package tcpcongestion

import (
	"syscall"
	"testing"
)

func TestDecide(t *testing.T) {
	tests := []struct {
		name      string
		in        input
		state     string
		reason    string
		errno     string
		granted   string
		requested string
	}{
		{name: "bbr readback", in: input{enabled: true, linux: true, readback: "bbr\x00\x00"}, state: "on", granted: "bbr"},
		{name: "missing module with capability", in: input{enabled: true, linux: true, setErr: syscall.ENOENT, capNetAdmin: true}, state: "fallback", reason: "module_missing"},
		{name: "unloaded module without capability", in: input{enabled: true, linux: true, setErr: syscall.ENOENT}, state: "fallback", reason: "module_not_loaded"},
		{name: "permission without capability", in: input{enabled: true, linux: true, setErr: syscall.EPERM}, state: "fallback", reason: "not_allowed"},
		{name: "permission with capability", in: input{enabled: true, linux: true, setErr: syscall.EPERM, capNetAdmin: true}, state: "fallback", reason: "kernel_error", errno: "EPERM"},
		{name: "kernel busy", in: input{enabled: true, linux: true, setErr: syscall.EBUSY}, state: "fallback", reason: "kernel_error", errno: "EBUSY"},
		{name: "readback failed", in: input{enabled: true, linux: true, readErr: syscall.EBADF}, state: "fallback", reason: "kernel_error", errno: "EBADF"},
		{name: "overridden", in: input{enabled: true, linux: true, readback: "cubic"}, state: "fallback", reason: "overridden", granted: "cubic"},
		{name: "not linux", in: input{enabled: true}, state: "off", reason: "unsupported_platform"},
		{name: "disabled", in: input{linux: true}, state: "off", reason: "disabled", requested: "off"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.listener = ListenerAPI
			got := decide(tt.in)
			if got.State != tt.state || got.Reason != tt.reason || got.Errno != tt.errno {
				t.Errorf("state/reason/errno = %q/%q/%q, want %q/%q/%q", got.State, got.Reason, got.Errno, tt.state, tt.reason, tt.errno)
			}
			if tt.granted != "" && got.Granted != tt.granted {
				t.Errorf("Granted = %q, want %q", got.Granted, tt.granted)
			}
			if tt.requested != "" && got.Requested != tt.requested {
				t.Errorf("Requested = %q, want %q", got.Requested, tt.requested)
			}
			if tt.state == "fallback" && (got.Action == "" || got.Action == "none") {
				t.Errorf("Action = %q, want a remedy", got.Action)
			}
		})
	}
}
