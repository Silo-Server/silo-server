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
	// manifest is nil for an incomplete folder, and for one whose manifest
	// did not verify (tampered) or could not be read (unreadable).
	manifest *Manifest
	// tampered is set when the folder's manifest named this server's
	// SECRET_KEY and did not verify: nothing in it is trusted, and the
	// sweep leaves the folder for the admin to look at.
	tampered bool
	// unreadable is set when the folder lists a manifest that could not be
	// read for any other reason (a timeout, a destination error): what it
	// holds is unknown, so it is neither rotated nor swept.
	unreadable bool
	// noManifest is set only when a complete listing of the folder showed
	// no manifest object: the one state the sweep deletes.
	noManifest bool
	// verified is set when the manifest verified under this server's
	// SECRET_KEY, or the service checks none (tests).
	verified bool
	size     int64
	// newest is the latest modification time of any object in the folder.
	newest time.Time
}

// cleanCopy reports whether f is a copy rotation protects: complete, with
// every blob, and verified, so this server can restore it without extra
// flags. An unverified backup never stands in for a verified clean one.
func (f folder) cleanCopy() bool {
	return f.manifest != nil && f.verified && f.manifest.Status() == StatusComplete
}

// planRotation returns the ids rotation deletes. Only folders with a manifest
// are candidates. pinned ids (the source of an active restore) are never
// deleted. Rules:
//
//   - nightly and manual copies beyond keep are deleted oldest first, except
//     that the newest clean copy (cleanCopy) is always kept, even when that
//     keeps one more than keep, so repeated runs with missing blobs, or
//     backups this server cannot verify, cannot rotate away the last copy it
//     can restore with every blob;
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
		if f.cleanCopy() {
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

// planSweep returns incomplete folders whose name and newest object are
// both older than a day. It deletes only a folder a complete listing showed
// without a manifest object (noManifest): one whose manifest could not be
// read, or did not verify, may be a complete backup and is left alone. The
// sweep runs under the subsystem lock, so no backup is writing to any
// folder it sees.
func planSweep(folders []folder, now time.Time) []string {
	var deletes []string
	for _, f := range folders {
		if !f.noManifest {
			continue
		}
		if now.Sub(f.at) > incompleteSweepAge && now.Sub(f.newest) > incompleteSweepAge {
			deletes = append(deletes, f.id)
		}
	}
	sort.Strings(deletes)
	return deletes
}
