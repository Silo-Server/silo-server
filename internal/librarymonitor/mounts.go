package librarymonitor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// mountEntry is one line of /proc/self/mountinfo.
type mountEntry struct {
	id int
	// dev is the filesystem's major:minor.
	dev    string
	point  string
	fsType string
}

// unsupportedMountTypes names the mountinfo filesystem types the monitor
// does not watch, matching the statfs classification of roots (see
// knownFilesystems). A filesystem of one of these types mounted inside a
// monitored folder is skipped by the walk.
var unsupportedMountTypes = map[string]string{
	mountTypeNFS:  fsNameNFS,
	mountTypeNFS4: fsNameNFS,
	"smbfs":       fsNameSMB,
	"smb3":        fsNameSMB,
	"cifs":        fsNameCIFS,
	"ceph":        fsNameCephFS,
	"9p":          fsName9p,
}

// NFS's mountinfo types.
const (
	mountTypeNFS  = "nfs"
	mountTypeNFS4 = "nfs4"
)

// parseMountInfo parses /proc/self/mountinfo. Malformed lines are skipped.
func parseMountInfo(data string) []mountEntry {
	var out []mountEntry
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		// id parent major:minor root mountpoint options [optional...] - fstype source superoptions
		if len(fields) < 7 {
			continue
		}
		id, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		sep := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+1 >= len(fields) {
			continue
		}
		out = append(out, mountEntry{
			id:     id,
			dev:    fields[2],
			point:  unescapeMountPath(fields[4]),
			fsType: fields[sep+1],
		})
	}
	return out
}

// unescapeMountPath decodes the octal escapes (\040 for a space, \011,
// \012, \134) the kernel writes in mountinfo paths.
func unescapeMountPath(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// mountView is what one root needs from the mount table.
type mountView struct {
	// signature describes the mount holding the root and every mount at or
	// below it. Reconcile walks an attached root again when it changes: a
	// filesystem mounted inside a monitored folder has no watches or handles
	// recorded until something walks it.
	signature string
	// unsupported lists the logical paths of mounts below the root on
	// filesystems the monitor does not watch, with the filesystem name.
	unsupported []string
}

// viewMounts builds root's mountView from the mount table. physical is the
// root with symlinks resolved; mount points are physical paths.
func viewMounts(root, physical string, mounts []mountEntry) mountView {
	var view mountView
	var lines []string
	cover := -1
	for i, e := range mounts {
		switch {
		case e.point == physical:
			lines = append(lines, e.key())
			cover = i
		case isBelow(e.point, physical):
			lines = append(lines, e.key())
			if name := unsupportedMountTypes[e.fsType]; name != "" {
				logical := filepath.Join(root, strings.TrimPrefix(e.point, physical))
				view.unsupported = append(view.unsupported, logical+" ("+name+")")
			}
		case isBelow(physical, e.point):
			// Later lines mount over earlier ones at the same point.
			if cover < 0 || len(e.point) >= len(mounts[cover].point) {
				cover = i
			}
		}
	}
	if cover >= 0 && mounts[cover].point != physical {
		lines = append(lines, mounts[cover].key())
	}
	sort.Strings(lines)
	sort.Strings(view.unsupported)
	view.signature = strings.Join(lines, "\n")
	return view
}

func (e mountEntry) key() string {
	return fmt.Sprintf("%d %s %s %s", e.id, e.dev, e.fsType, e.point)
}

// isBelow reports whether path is strictly below dir.
func isBelow(path, dir string) bool {
	_, ok := below(path, dir)
	return ok
}

// walkMounts caches the mount table for walks: a burst of new folders walks
// several times a second, and the table can be large on container hosts.
// Reconcile's mount check reads it fresh.
var walkMounts struct {
	mu sync.Mutex
	at time.Time
	// skips maps every mount point to whether the walk skips its
	// filesystem.
	skips map[string]bool
}

const walkMountsTTL = time.Second

// unsupportedMountPoints returns every physical mount point, mapped to
// whether its filesystem is one the walk skips. It never touches the mounts
// themselves, so a hung network mount below a root cannot stall the walk.
func unsupportedMountPoints() map[string]bool {
	walkMounts.mu.Lock()
	defer walkMounts.mu.Unlock()
	if walkMounts.skips != nil && time.Since(walkMounts.at) < walkMountsTTL {
		return walkMounts.skips
	}
	mounts, err := readMounts()
	skips := make(map[string]bool)
	if err == nil {
		for _, e := range mounts {
			// A later mount on the same point hides the earlier one.
			skips[e.point] = unsupportedMountTypes[e.fsType] != ""
		}
	}
	walkMounts.skips = skips
	walkMounts.at = time.Now()
	return skips
}

// onUnsupportedMount reports whether the mount that holds physical, the
// deepest mount point at or above it, is one the walk skips. A local disk
// mounted inside a network share is therefore not skipped.
func onUnsupportedMount(mounts map[string]bool, physical string) bool {
	best, skip := "", false
	for point, unsupported := range mounts {
		if (physical == point || isBelow(physical, point)) && len(point) > len(best) {
			best, skip = point, unsupported
		}
	}
	return skip
}

// linkOntoUnsupportedMount reports whether the symlink at path, whose parent
// directory resolves to physicalParent, names a target on a filesystem the
// walk skips. It reads only the link text, so it never touches the target.
// A target reached through further links is not caught here; callers check
// again after resolving.
func linkOntoUnsupportedMount(mounts map[string]bool, path, physicalParent string) bool {
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(physicalParent, target)
	}
	return onUnsupportedMount(mounts, filepath.Clean(target))
}
