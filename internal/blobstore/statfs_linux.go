//go:build linux

package blobstore

import "golang.org/x/sys/unix"

// blockSize is the unit statfs counts blocks in. Linux reports it as
// f_frsize; f_bsize is the preferred I/O size, which FUSE and some network
// filesystems set larger.
func blockSize(st *unix.Statfs_t) int64 {
	if st.Frsize > 0 {
		return int64(st.Frsize) //nolint:unconvert // int32 on 32-bit platforms
	}
	return int64(st.Bsize) //nolint:unconvert // int32 on 32-bit platforms
}
