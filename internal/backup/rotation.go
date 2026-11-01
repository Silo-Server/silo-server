package backup

import (
	"sort"
	"time"
)

// incompleteSweepAge is how old an incomplete folder must be before the
// sweep deletes it.
const incompleteSweepAge = 24 * time.Hour

// folder is one backup folder at the destination for this server.
type folder struct {
	id   string
	kind Kind
	at   time.Time
	// manifest is nil for an incomplete folder.
	manifest *Manifest
	size     int64
	// newest is the latest modification time of any object in the folder.
	newest time.Time
}

// planRotation returns the ids rotation deletes. Only folders with a manifest
// are candidates. Nightly and manual copies beyond keep are deleted oldest
// first, except that the newest copy without warnings is always kept, even
// when that keeps one more than keep, so repeated runs with missing blobs
// cannot rotate away the last copy that has every blob.
func planRotation(folders []folder, keep int) []string {
	if keep < 1 {
		keep = 1
	}
	var regular []folder
	for _, f := range folders {
		if f.manifest != nil {
			regular = append(regular, f)
		}
	}
	sort.Slice(regular, func(i, j int) bool { return regular[i].at.After(regular[j].at) })
	newestClean := ""
	for _, f := range regular {
		if f.manifest.Status() == StatusComplete {
			newestClean = f.id
			break
		}
	}
	var deletes []string
	for i, f := range regular {
		if i >= keep && f.id != newestClean {
			deletes = append(deletes, f.id)
		}
	}
	sort.Strings(deletes)
	return deletes
}

// planSweep returns incomplete folders (no manifest) whose name and newest
// object are both older than a day. The sweep runs under the subsystem lock,
// so no backup is writing to any folder it sees.
func planSweep(folders []folder, now time.Time) []string {
	var deletes []string
	for _, f := range folders {
		if f.manifest != nil {
			continue
		}
		if now.Sub(f.at) > incompleteSweepAge && now.Sub(f.newest) > incompleteSweepAge {
			deletes = append(deletes, f.id)
		}
	}
	sort.Strings(deletes)
	return deletes
}
