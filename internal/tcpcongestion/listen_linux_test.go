//go:build linux

package tcpcongestion

import (
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/netadmin"
)

func TestListenKeepsServingWhenKernelRefuses(t *testing.T) {
	if netadmin.Current().Helper == netadmin.HelperRunning {
		t.Skip("requires no NET_ADMIN helper to exercise an unprivileged kernel refusal")
	}
	ln, result, err := listen(t.Context(), "tcp", "127.0.0.1:0", "api", "silo_none", Options{Congestion: true, Limits: Limits{State: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeForTest(t, ln) })
	if result.State != "fallback" {
		t.Errorf("State = %q, want fallback", result.State)
	}
	if result.Reason != "module_not_loaded" && result.Reason != "module_missing" {
		t.Errorf("Reason = %q, want module_not_loaded or module_missing", result.Reason)
	}
	dialAndAccept(t, ln)
}

func TestListenReadsBackBBR(t *testing.T) {
	ln, result, err := Listen(t.Context(), "tcp", "127.0.0.1:0", "api", Options{Congestion: true, Limits: Limits{State: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeForTest(t, ln) })
	switch result.Reason {
	case "module_not_loaded", "module_missing", "not_allowed":
		t.Skipf("BBR unavailable: reason=%s granted=%q", result.Reason, result.Granted)
	case "":
	default:
		t.Fatalf("Reason = %q, want success or an unavailable BBR reason", result.Reason)
	}
	if result.State != "on" {
		t.Fatalf("State = %q, want on", result.State)
	}
	conn := dialAndAccept(t, ln)
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var algorithm string
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		algorithm, sockErr = unix.GetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION)
	}); err != nil {
		t.Fatal(err)
	}
	if sockErr != nil {
		t.Fatal(sockErr)
	}
	if algorithm != "bbr" {
		t.Errorf("accepted TCP_CONGESTION = %q, want bbr", algorithm)
	}
}

func TestListenForcesSendBufferThroughHelper(t *testing.T) {
	if os.Getenv("SILO_TEST_NET_ADMIN") != "1" {
		t.Skip("requires SILO_TEST_NET_ADMIN=1 and a container with NET_ADMIN")
	}
	if state := netadmin.Current(); state.Helper != netadmin.HelperRunning {
		t.Fatalf("NET_ADMIN helper is not running: Current() = %+v", state)
	}
	for _, tt := range []struct {
		name      string
		bytes     int
		notNeeded bool
	}{
		{name: "default size", bytes: 33554432},
		{name: "custom size", bytes: 16777216},
		{name: "size within maximum", bytes: 1048576, notNeeded: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ln, result, err := listen(t.Context(), "tcp", "127.0.0.1:0", "api", "bbr", Options{
				Congestion: false, SendBuffer: tt.bytes, Limits: Limits{State: "capped", WmemMax: 4194304},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeForTest(t, ln) })
			conn := dialAndAccept(t, ln)
			granted, lowat := acceptedBuffers(t, conn)
			buffer := result.SendBuffer
			if lowat != 131072 || buffer.NotSentLowat != lowat {
				t.Errorf("accepted TCP_NOTSENT_LOWAT/SendBuffer.NotSentLowat = %d/%d, want 131072/131072", lowat, buffer.NotSentLowat)
			}
			if tt.notNeeded {
				if buffer.State != "off" || buffer.Reason != "not_needed" {
					t.Errorf("SendBuffer State/Reason = %q/%q, want off/not_needed", buffer.State, buffer.Reason)
				}
				if granted == 2*tt.bytes {
					t.Errorf("accepted SO_SNDBUF = %d, want no force to %d", granted, 2*tt.bytes)
				}
				return
			}
			if buffer.State != "on" || buffer.Granted != 2*tt.bytes || buffer.Requested != tt.bytes {
				t.Errorf("SendBuffer State/Requested/Granted = %q/%d/%d, want on/%d/%d", buffer.State, buffer.Requested, buffer.Granted, tt.bytes, 2*tt.bytes)
			}
			if granted != buffer.Granted {
				t.Errorf("accepted SO_SNDBUF = %d, want SendBuffer.Granted %d", granted, buffer.Granted)
			}
		})
	}
}

func TestListenSetsLowWaterWithoutForce(t *testing.T) {
	for _, tt := range []struct {
		name, listener, reason string
		bytes                  int
		limits                 Limits
	}{
		{name: "not needed", listener: ListenerAPI, bytes: 1048576, limits: Limits{State: "capped", WmemMax: 4194304}, reason: "not_needed"},
		{name: "off", listener: ListenerProxy, limits: Limits{State: "capped", WmemMax: 4194304}, reason: "disabled"},
		{name: "uncapped", listener: ListenerAPI, bytes: 33554432, limits: Limits{State: "ok", WmemMax: 67108864}, reason: "not_needed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ln, result, err := Listen(t.Context(), "tcp", "127.0.0.1:0", tt.listener, Options{SendBuffer: tt.bytes, Limits: tt.limits})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeForTest(t, ln) })
			granted, lowat := acceptedBuffers(t, dialAndAccept(t, ln))
			buffer := result.SendBuffer
			if buffer.State != "off" || buffer.Reason != tt.reason || buffer.Requested != 0 || buffer.Granted != 0 {
				t.Errorf("SendBuffer = %+v, want off/%s with no force", buffer, tt.reason)
			}
			if lowat != 131072 || buffer.NotSentLowat != lowat {
				t.Errorf("accepted TCP_NOTSENT_LOWAT/SendBuffer.NotSentLowat = %d/%d, want 131072/131072", lowat, buffer.NotSentLowat)
			}
			if tt.bytes > 0 && granted == 2*tt.bytes {
				t.Errorf("accepted SO_SNDBUF = %d, want no force to %d", granted, 2*tt.bytes)
			}
		})
	}
}

func TestConfigureSendBufferKeepsLowWaterAfterForceError(t *testing.T) {
	if netadmin.Current().Helper == netadmin.HelperRunning {
		t.Skip("requires no NET_ADMIN helper to exercise a failed force")
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, unix.IPPROTO_TCP)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Close(fd); err != nil {
			t.Errorf("close socket: %v", err)
		}
	})
	in := sendBufferInput{listener: ListenerAPI, request: 33554432, linux: true, limits: Limits{State: "capped"}, helper: netadmin.HelperRunning}
	configureSendBuffer(fd, &in)
	if in.lowatErr != nil || in.forceErr == nil {
		t.Fatalf("lowat/force errors = %v/%v, want successful low-water set and failed force", in.lowatErr, in.forceErr)
	}
	lowat, err := unix.GetsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT)
	if err != nil {
		t.Fatal(err)
	}
	if lowat != 131072 {
		t.Errorf("TCP_NOTSENT_LOWAT after force error = %d, want 131072", lowat)
	}
	result := decideSendBuffer(in)
	if result.Reason != "kernel_error" || result.NotSentLowat != lowat {
		t.Errorf("Reason/NotSentLowat = %q/%d, want kernel_error/%d", result.Reason, result.NotSentLowat, lowat)
	}
}

func dialAndAccept(t *testing.T, ln net.Listener) *net.TCPConn {
	t.Helper()
	dialer := net.Dialer{Timeout: 5 * time.Second}
	client, err := dialer.DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeForTest(t, client) })
	// Bound Accept even if a listener wrapper stops forwarding connections.
	timer := time.AfterFunc(5*time.Second, func() { closeForTest(t, ln) })
	defer timer.Stop()
	accepted, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeForTest(t, accepted) })
	conn, ok := accepted.(*net.TCPConn)
	if !ok {
		t.Fatalf("accepted connection has type %T, want *net.TCPConn", accepted)
	}
	return conn
}

func acceptedBuffers(t *testing.T, conn *net.TCPConn) (granted, lowat int) {
	t.Helper()
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		granted, sockErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUF)
		if sockErr == nil {
			lowat, sockErr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if sockErr != nil {
		t.Fatal(sockErr)
	}
	return granted, lowat
}

func closeForTest(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("close %T: %v", closer, err)
	}
}
