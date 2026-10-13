//go:build linux

package workersource

import (
	"os"
	"syscall"
)

// Nonblocking open avoids hanging if an approved regular file is raced into a
// FIFO before open. The descriptor is still checked for a regular-file identity.
func openSourceFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
