//go:build !linux

package tcpcongestion

import (
	"context"
	"net"
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
	var config net.ListenConfig
	ln, err := config.Listen(ctx, network, address)
	if err != nil {
		return nil, Result{}, err
	}
	result := decide(input{listener: listener, enabled: opts.Congestion})
	result.SendBuffer = decideSendBuffer(sendBufferInput{listener: listener, request: opts.SendBuffer})
	logResult(ctx, result)
	logSendBuffer(ctx, result.SendBuffer)
	return ln, result, nil
}

// ReadLimits logs and returns an off result with reason unsupported_platform
// using ctx. It reads no kernel limits on this platform.
func ReadLimits(ctx context.Context) Limits {
	limits := decideLimits("", "", nil, nil, false)
	logLimits(ctx, limits)
	return limits
}
