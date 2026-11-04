package backup

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
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

// folders lists this server's backup folders at t, reading each manifest
// (listBackupFolders bounds the listing; a listing that fails or reaches a
// cap is an error, so nothing acts on part of it). A folder whose manifest
// lists an object that is not there, or not at its recorded size, is
// incomplete but keeps its manifest, so the sweep leaves it. One with more
// objects than Silo writes was listed only in part and is unreadable.
func (s *Service) folders(ctx context.Context, t target, serverID string) ([]folder, error) {
	return s.readFolders(ctx, t, serverID, nil)
}

// listFolders is folders for the listing, through the manifest cache: a
// manifest listed unchanged since the listing last read it is not read
// again. Rotation and a backup's own checks read every manifest.
func (s *Service) listFolders(ctx context.Context, t target, serverID string) ([]folder, error) {
	return s.readFolders(ctx, t, serverID, &s.manifests)
}

func (s *Service) readFolders(ctx context.Context, t target, serverID string, cache *manifestCache) ([]folder, error) {
	listed, err := listBackupFolders(ctx, t, serverPrefix(serverID))
	if err != nil {
		return nil, err
	}
	out := make([]folder, 0, len(listed))
	cached := map[string]bool{}
	for _, l := range listed {
		at, kind, _ := ParseFolderName(l.id)
		f := folder{id: l.id, kind: kind, at: at}
		sizes := make(map[string]int64, len(l.objects))
		var manifestObject blobstore.ObjectInfo
		for _, obj := range l.objects {
			f.size += obj.Size
			if obj.ModTime.After(f.newest) {
				f.newest = obj.ModTime
			}
			name := strings.TrimPrefix(obj.Key, folderPrefix(serverID, l.id))
			sizes[name] = obj.Size
			if name == ManifestName {
				manifestObject = obj
			}
		}
		if l.overflow {
			// Only part of it was listed: what it holds is unknown.
			f.unreadable = true
			slog.WarnContext(ctx, "backup folder holds more objects than a backup writes; leaving it alone", "component", "backup", "id", l.id)
			out = append(out, f)
			continue
		}
		// From here the folder's listing is complete.
		if _, ok := sizes[ManifestName]; !ok {
			f.noManifest = true
			out = append(out, f)
			continue
		}
		m, err := s.cachedManifest(ctx, t, serverID, l.id, manifestObject, cache, cached)
		switch {
		case err != nil:
			f.tampered = errors.Is(err, ErrManifestTampered)
			f.unreadable = !f.tampered
			slog.WarnContext(ctx, "unreadable backup manifest", "component", "backup", "id", l.id, "error", err)
		case !m.objectsPresent(sizes):
			slog.WarnContext(ctx, "backup manifest lists objects that are gone; the folder is incomplete", "component", "backup", "id", l.id)
		default:
			f.manifest, f.verified = m, m.authenticated || s.opts.ManifestKey == nil
		}
		out = append(out, f)
	}
	if cache != nil {
		cache.keepOnly(cacheLocation(t, serverPrefix(serverID)), cached)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.After(out[j].at) })
	return out, nil
}

// cachedManifest is readManifest through cache, when there is one, for the
// manifest object obj of backup id; seen collects the cache locations the
// listing saw.
func (s *Service) cachedManifest(ctx context.Context, t target, serverID, id string, obj blobstore.ObjectInfo, cache *manifestCache, seen map[string]bool) (*Manifest, error) {
	if cache == nil {
		return s.readManifest(ctx, t, serverID, id)
	}
	location := cacheLocation(t, obj.Key)
	seen[location] = true
	if m, ok := cache.get(location, obj); ok {
		return m, nil
	}
	m, err := s.readManifest(ctx, t, serverID, id)
	if err == nil {
		cache.put(cacheLocation(t, serverPrefix(serverID)), location, obj, m)
	}
	return m, err
}

// objectsPresent reports whether every object m lists is in sizes (a
// folder's listing, by name) at its recorded size.
func (m *Manifest) objectsPresent(sizes map[string]int64) bool {
	for _, o := range m.Objects {
		if size, ok := sizes[o.Name]; !ok || size != o.Size {
			return false
		}
	}
	return true
}
