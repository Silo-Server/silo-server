package nodepool

import (
	"context"
	"net"
	"time"
)

// NodeDialTimeout bounds opening a TCP connection from an API server to a
// node. Nodes sit on the same network as the API or on a private overlay, where
// a connect takes milliseconds, so a few seconds only ever expires on a node
// that is gone. Go's default of 30 seconds would hold a playback start or relay
// aimed at a dead host for that long before the caller could try another node.
// The bound includes name resolution.
const NodeDialTimeout = 3 * time.Second

// NodeUnacknowledgedTimeout bounds, on Linux, how long data sent to a node over
// an open connection may go unacknowledged before the connection fails with
// ETIMEDOUT. See controlNodeSocket.
const NodeUnacknowledgedTimeout = 10 * time.Second

// NodeDialContext is the DialContext for HTTP transports that call nodes.
func NodeDialContext() func(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: NodeDialTimeout, KeepAlive: 30 * time.Second, Control: controlNodeSocket}).DialContext
}
