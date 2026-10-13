//go:build !linux

package nodepool

import "syscall"

// controlNodeSocket has no unacknowledged-data bound to set outside Linux;
// connections there rely on the connect bound and TCP keepalive.
func controlNodeSocket(_, _ string, _ syscall.RawConn) error { return nil }
