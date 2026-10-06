//go:build !unix

package landiscovery

import "syscall"

// shareMDNSPort is a no-op where port sharing is not available; binding 5353
// then fails if another responder holds it, and discovery logs that.
func shareMDNSPort(_, _ string, _ syscall.RawConn) error { return nil }
