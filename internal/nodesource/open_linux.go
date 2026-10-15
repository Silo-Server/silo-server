//go:build linux

package nodesource

import (
	"os"
	"syscall"
)

// openNonblocking opens path read-only without hanging if a regular file was
// raced into a FIFO before the open. The caller still checks the descriptor
// is a regular file.
func openNonblocking(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
