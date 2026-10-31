//go:build !unix

package privatedir

import "io/fs"

// checkOwner accepts any owner here: the check uses Unix ownership and mode
// bits.
func checkOwner(string, fs.FileInfo, int) error { return nil }
