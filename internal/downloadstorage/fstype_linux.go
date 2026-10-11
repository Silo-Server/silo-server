//go:build linux

package downloadstorage

import "golang.org/x/sys/unix"

// linuxFSTypes names the statfs f_type magic numbers (linux/magic.h) an
// administrator is likely to meet under a prepared-download directory.
var linuxFSTypes = map[int64]string{
	0xef53:     "ext4", // ext2, ext3 and ext4 share one magic
	0x58465342: "xfs",
	0x9123683e: "btrfs",
	0x2fc12fc1: "zfs",
	0xf2f52010: "f2fs",
	0x01021994: fsTmpfs,
	0x858458f6: fsRamfs,
	0x794c7630: fsOverlay,
	0x6969:     "nfs",
	0x517b:     "smb",
	0xff534d42: "cifs",
	0xfe534d42: "smb2",
	0x00c36400: "ceph",
	0x01021997: "9p",
	0x65735546: "fuse",
}

func fsTypeName(st *unix.Statfs_t) string {
	if name, ok := linuxFSTypes[int64(st.Type)]; ok { //nolint:unconvert // Type width varies by arch
		return name
	}
	return ""
}
