//go:build linux

package tcpcongestion

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/netadmin"
)

// Listen binds network and address with opts for the named listener, using ctx
// for logging. It returns the bound listener and its congestion and send-buffer
// results, logging both result lines once before return. A bind error is
// returned unchanged and is the only error; kernel and helper refusals never
// return an error.
func Listen(ctx context.Context, network, address, listener string, opts Options) (net.Listener, Result, error) {
	return listen(ctx, network, address, listener, "bbr", opts)
}

func listen(ctx context.Context, network, address, listener, algorithm string, opts Options) (net.Listener, Result, error) {
	congestion := input{listener: listener, enabled: opts.Congestion, linux: true}
	buffer := sendBufferInput{listener: listener, request: opts.SendBuffer, linux: true, limits: opts.Limits}
	var config net.ListenConfig
	// Go defaults listeners to MPTCP; the helper accepts only TCP.
	config.SetMultipathTCP(false)
	config.Control = func(_, _ string, conn syscall.RawConn) error {
		congestion = input{listener: listener, enabled: opts.Congestion, linux: true}
		buffer = sendBufferInput{listener: listener, request: opts.SendBuffer, linux: true, limits: opts.Limits}
		err := conn.Control(func(fd uintptr) {
			if opts.Congestion {
				configureCongestion(int(fd), algorithm, &congestion)
			}
			buffer.helper = netadmin.Current().Helper
			configureSendBuffer(int(fd), &buffer)
		})
		if err != nil {
			congestion.setErr = err
			buffer.helper = netadmin.Current().Helper
			buffer.lowatErr = err
		}
		return nil
	}
	ln, err := config.Listen(ctx, network, address)
	if err != nil {
		return nil, Result{}, err
	}
	result := decide(congestion)
	result.SendBuffer = decideSendBuffer(buffer)
	logResult(ctx, result)
	logSendBuffer(ctx, result.SendBuffer)
	if result.State == stateOn {
		ln = &congestionListener{Listener: ln, check: func(conn net.Conn) {
			checkAccepted(ctx, conn, listener, result.CapNetAdmin)
		}}
	}
	return ln, result, nil
}

func configureCongestion(fd int, algorithm string, in *input) {
	if netadmin.Current().Helper == netadmin.HelperRunning {
		in.capNetAdmin = true
		in.setErr = netadmin.SetCongestion(fd)
	} else {
		in.setErr = unix.SetsockoptString(fd, unix.IPPROTO_TCP, unix.TCP_CONGESTION, algorithm)
		in.capNetAdmin = hasNetAdmin()
	}
	in.readback, in.readErr = unix.GetsockoptString(fd, unix.IPPROTO_TCP, unix.TCP_CONGESTION)
}

var hasNetAdmin = sync.OnceValue(func() bool {
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&header, &data[0]); err != nil {
		return false
	}
	return data[0].Effective&(1<<unix.CAP_NET_ADMIN) != 0
})

func configureSendBuffer(fd int, in *sendBufferInput) {
	in.lowatErr = unix.SetsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT, notSentLowat)
	if in.lowatErr != nil || !needsSendBufferForce(in.request, in.limits) || in.helper != netadmin.HelperRunning {
		return
	}
	in.forceErr = netadmin.ForceSendBuffer(fd, in.request)
	if in.forceErr != nil {
		return
	}
	in.granted, in.readErr = unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF)
}

type congestionListener struct {
	net.Listener
	once  sync.Once
	check func(net.Conn)
}

func (ln *congestionListener) Accept() (net.Conn, error) {
	conn, err := ln.Listener.Accept()
	if err == nil {
		ln.once.Do(func() { ln.check(conn) })
	}
	return conn, err
}

func checkAccepted(ctx context.Context, conn net.Conn, listener string, capNetAdmin bool) {
	in := input{listener: listener, enabled: true, linux: true, capNetAdmin: capNetAdmin}
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		in.readErr = fmt.Errorf("accepted connection has type %T, expected *net.TCPConn", conn)
	} else {
		raw, err := tcp.SyscallConn()
		if err == nil {
			err = raw.Control(func(fd uintptr) {
				in.readback, in.readErr = unix.GetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION)
			})
		}
		if err != nil {
			in.readErr = err
		}
	}
	if result := decide(in); result.State != stateOn {
		logResult(ctx, result)
	}
}

// ReadLimits reads the kernel TCP send and receive maxima for this network
// namespace and logs the limits result once using ctx. Unreadable or malformed
// send limits produce an unknown state; receive failures leave RmemMax zero.
func ReadLimits(ctx context.Context) Limits {
	wmem, wmemErr := os.ReadFile("/proc/sys/net/ipv4/tcp_wmem")
	rmem, rmemErr := os.ReadFile("/proc/sys/net/ipv4/tcp_rmem")
	limits := decideLimits(string(wmem), string(rmem), wmemErr, rmemErr, true)
	logLimits(ctx, limits)
	return limits
}
