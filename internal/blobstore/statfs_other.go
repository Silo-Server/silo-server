//go:build !linux

package blobstore

import "golang.org/x/sys/unix"

// blockSize is the unit statfs counts blocks in: f_bsize outside Linux.
func blockSize(st *unix.Statfs_t) int64 {
	return int64(st.Bsize)
}
