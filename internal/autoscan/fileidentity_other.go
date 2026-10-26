//go:build !unix

package autoscan

import "io/fs"

// fileIdentity has no portable inode equivalent here; the debounce state
// falls back to size and modification time.
func fileIdentity(fs.FileInfo) string { return "" }
