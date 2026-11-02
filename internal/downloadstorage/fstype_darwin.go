//go:build darwin

package downloadstorage

import "golang.org/x/sys/unix"

func fsTypeName(st *unix.Statfs_t) string {
	return unix.ByteSliceToString(st.Fstypename[:])
}
