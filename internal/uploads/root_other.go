//go:build !unix

package uploads

import "io/fs"

// checkRootOwner does nothing here: its checks use Unix ownership and mode
// bits.
func checkRootOwner(string, fs.FileInfo, int) error { return nil }
