//go:build !unix

package plugins

// EnsurePrivateCacheRoot does nothing here: its checks use Unix ownership and
// mode bits. The installer creates the root on first use.
func EnsurePrivateCacheRoot(string) error { return nil }
