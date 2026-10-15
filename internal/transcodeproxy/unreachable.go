package transcodeproxy

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// netOpDial is the net.OpError operation for opening a connection.
const netOpDial = "dial"

// NodeUnreachable reports whether a relay to a transcode node failed before
// the node received the request: the connection could not be opened (refused,
// unroutable, timed out, or the name did not resolve). Nothing was written to
// the client, and the node cannot be serving the transcode for this caller, so
// the caller may move the transcode elsewhere and retry.
//
// A dial this process could not even attempt (out of file descriptors or
// socket buffers, no local address) says nothing about the node and does not
// count. Neither does an error after the request reached the node, such as a
// connection reset mid-response: the node was alive when it was asked, nor a
// dial abandoned because the caller's own request was canceled.
func NodeUnreachable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || localSocketFailure(err) {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == netOpDial {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}

// localSocketFailure reports a dial that failed on this host's own resources.
// Go reports these as a dial OpError, the same shape as a refused connection.
func localSocketFailure(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.ENOBUFS)
}
