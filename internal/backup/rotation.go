package backup

import (
	"sort"
	"time"
)

const (
	// beforeRestoreRetention keeps a before-restore copy, the recovery point
	// for a full restore, for a week outside the nightly count.
	beforeRestoreRetention = 7 * 24 * time.Hour
	// incompleteSweepAge is how old an incomplete folder must be before the
	// sweep deletes it.
	incompleteSweepAge = 24 * time.Hour
)

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
// are candidates. pinned ids (the source of an active restore) are never
// deleted. Rules:
//
//   - nightly and manual copies beyond keep are deleted oldest first, except
//     that the newest copy without warnings is always kept, even when that
//     keeps one more than keep, so repeated runs with missing blobs cannot
//     rotate away the last copy that has every blob;
//   - before-restore copies are deleted once older than seven days, and do
//     not count toward keep.
func planRotation(folders []folder, keep int, now time.Time, pinned map[string]bool) []string {
	if keep < 1 {
		keep = 1
	}
	byKind := map[Kind][]folder{}
	for _, f := range folders {
		if f.manifest == nil {
			continue
		}
		group := f.kind
		if group == KindManual {
			group = KindNightly
		}
		byKind[group] = append(byKind[group], f)
	}
	for _, list := range byKind {
		sort.Slice(list, func(i, j int) bool { return list[i].at.After(list[j].at) })
	}
	var deletes []string
	drop := func(f folder) {
		if !pinned[f.id] {
			deletes = append(deletes, f.id)
		}
	}
	regular := byKind[KindNightly]
	newestClean := ""
	for _, f := range regular {
		if f.manifest.Status() == StatusComplete {
			newestClean = f.id
			break
		}
	}
	for i, f := range regular {
		if i >= keep && f.id != newestClean {
			drop(f)
		}
	}
	for _, f := range byKind[KindBeforeRestore] {
		if now.Sub(f.at) > beforeRestoreRetention {
			drop(f)
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
