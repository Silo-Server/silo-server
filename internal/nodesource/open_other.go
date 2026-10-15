//go:build !linux

package nodesource

import "os"

// openNonblocking is a plain open where O_NONBLOCK is not portable. The
// caller still checks the descriptor is a regular file.
func openNonblocking(path string) (*os.File, error) { return os.Open(path) }
