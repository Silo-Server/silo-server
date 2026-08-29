//go:build linux || darwin

package nodemetrics

import (
	"golang.org/x/sys/unix"
)

// osStatfs reports one path's filesystem capacity, in the terms that matter to
// a process deciding whether it can keep writing.
//
// Used counts every block the filesystem considers taken (Blocks-Bfree), which
// is what `df` puts in its Used column. Total is that plus the blocks still
// available to *this* process (Bavail) — deliberately not the raw device size.
// A filesystem holds blocks back from unprivileged users (ext4 reserves 5% by
// default), and those are writable by root alone: counting them as capacity
// makes a volume with nothing left for Silo to write read as 95% full, exactly
// where the scratch admission guard is set. A node would then admit sessions
// until the moment it has zero bytes of headroom and start failing transcodes
// mid-stream, which is the failure that guard exists to prevent.
//
// The resulting Used/Total is the same ratio `df` prints as Use%.
//
// This call can block indefinitely on an unresponsive network mount. Callers
// must treat it as such — see probeDisk, which runs it on a goroutine nothing
// waits for.
func osStatfs(path string) (fsStats, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return fsStats{}, err
	}
	stats := fsCapacity(st.Blocks, st.Bfree, st.Bavail, uint64(st.Bsize))
	stats.FSID = formatFSID(int64(st.Fsid.Val[0]), int64(st.Fsid.Val[1]))
	return stats, nil
}
