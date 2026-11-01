package backup

import (
	"errors"
	"strings"
	"time"
)

// Kind says why a backup was taken. Rotation treats each kind differently.
type Kind string

const (
	KindNightly       Kind = "nightly"
	KindManual        Kind = "manual"
	KindBeforeRestore Kind = "before-restore"
)

func (k Kind) valid() bool {
	switch k {
	case KindNightly, KindManual, KindBeforeRestore:
		return true
	}
	return false
}

const (
	// RootPrefix holds every server's backups at a destination.
	RootPrefix = "backups/"

	ManifestName = "manifest.json"
	DumpName     = "db.dump"
	BlobsName    = "blobs.tar"
	// RestoreSetName holds the sections selective restore reads.
	RestoreSetName = "restore-set.tar"

	// folderTimeLayout is the UTC time, to the millisecond, at the start of
	// every folder name. secondTimeLayout is the same to the second, which
	// earlier builds wrote and ParseFolderName still reads.
	folderTimeLayout = "20060102T150405.000Z"
	secondTimeLayout = "20060102T150405Z"
)

// ErrNotFound reports a backup id that names no backup of this server.
var ErrNotFound = errors.New("backup not found")

// FolderName is a backup's id: its UTC start time, to the millisecond, and
// kind. A new backup's id comes from reserveID, which never names a folder
// that exists.
func FolderName(at time.Time, kind Kind) string {
	return at.UTC().Truncate(time.Millisecond).Format(folderTimeLayout) + "-" + string(kind)
}

// ParseFolderName reads a backup id, with a time to the millisecond or, as
// earlier builds wrote them, to the second. Folders that are not backups,
// such as a full restore's <time>-overwritten-blobs, do not parse, so
// neither listing nor the sweep ever touches them.
func ParseFolderName(name string) (time.Time, Kind, bool) {
	stamp, kind, found := strings.Cut(name, "-")
	if !found || !Kind(kind).valid() {
		return time.Time{}, "", false
	}
	layout := folderTimeLayout
	if len(stamp) == len(secondTimeLayout) {
		layout = secondTimeLayout
	}
	at, err := time.Parse(layout, stamp)
	if err != nil || at.Format(layout) != stamp {
		return time.Time{}, "", false
	}
	return at, Kind(kind), true
}

// reserveID returns the id of a new backup of kind started at: one whose
// time no folder at the destination has, of any kind and complete or not.
// A time in use (two backups in one millisecond, or a clock that went back)
// moves on a millisecond until it is free, so a backup never writes into
// another's folder, and every folder's time is distinct, which keeps
// rotation's newest-first order total. folders must be listed under the
// subsystem lock, so nothing else takes the id before the backup writes.
func reserveID(folders []folder, at time.Time, kind Kind) string {
	taken := make(map[time.Time]bool, len(folders))
	for _, f := range folders {
		taken[f.at.UTC()] = true
	}
	at = at.UTC().Truncate(time.Millisecond)
	for taken[at] {
		at = at.Add(time.Millisecond)
	}
	return FolderName(at, kind)
}

// serverPrefix is one server's folder at the destination.
func serverPrefix(serverID string) string { return RootPrefix + serverID + "/" }

// folderPrefix is one backup's folder, with a trailing slash.
func folderPrefix(serverID, id string) string { return serverPrefix(serverID) + id + "/" }

func objectKey(serverID, id, name string) string { return folderPrefix(serverID, id) + name }

// validServerID accepts the identities serveridentity mints (UUIDs) and
// anything else that is one safe path segment.
func validServerID(id string) bool {
	if id == "" || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if !safeIDRune(r) {
			return false
		}
	}
	return true
}

func safeIDRune(r rune) bool {
	return r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
