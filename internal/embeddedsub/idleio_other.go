//go:build !linux

package embeddedsub

// idleIOThread does nothing where I/O priority isn't per thread.
func idleIOThread() bool { return true }
