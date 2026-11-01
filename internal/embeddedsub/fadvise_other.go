//go:build !linux

package embeddedsub

import "os"

// adviseRandom is a no-op where POSIX_FADV_RANDOM isn't available.
func adviseRandom(*os.File) {}
