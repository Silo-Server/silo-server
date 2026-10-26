package librarymonitor

// fsSupport is how the monitor treats a root's filesystem.
type fsSupport int

const (
	// fsMonitored: a local filesystem; monitored with no caveat.
	fsMonitored fsSupport = iota
	// fsMonitoredCaveat: monitored, but changes made outside this mount (for
	// example directly on a pool member disk) produce no events.
	fsMonitoredCaveat
	// fsUnsupported: writes from other machines produce no events, and a
	// hung mount would stall the walk. Not monitored.
	fsUnsupported
)

// Filesystem names shown in status details.
const (
	fsNameNFS    = "NFS"
	fsNameSMB    = "SMB"
	fsNameCIFS   = "CIFS"
	fsNameCephFS = "CephFS"
	fsName9p     = "9p"
)

// fsClass is the classification of one root's filesystem.
type fsClass struct {
	Name    string
	Support fsSupport
}

// knownFilesystems maps statfs f_type magic numbers (linux/magic.h) to the
// filesystems the monitor treats specially. Any other type is a local
// filesystem and is monitored.
var knownFilesystems = map[int64]fsClass{
	0x6969:     {Name: fsNameNFS, Support: fsUnsupported},
	0x517b:     {Name: fsNameSMB, Support: fsUnsupported},
	0xff534d42: {Name: fsNameCIFS, Support: fsUnsupported},
	0xfe534d42: {Name: "SMB2", Support: fsUnsupported},
	0x00c36400: {Name: fsNameCephFS, Support: fsUnsupported},
	// 9p (WSL /mnt/c, some VM shares): host-side changes produce no events.
	0x01021997: {Name: fsName9p, Support: fsUnsupported},
	// FUSE covers mergerfs, Unraid user shares, and virtiofs (Docker
	// Desktop), which reports the FUSE magic.
	0x65735546: {Name: "FUSE", Support: fsMonitoredCaveat},
}

// classifyFSType classifies a statfs f_type value.
func classifyFSType(fsType int64) fsClass {
	if class, ok := knownFilesystems[fsType]; ok {
		return class
	}
	return fsClass{Support: fsMonitored}
}
