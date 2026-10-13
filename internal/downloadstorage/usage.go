// Package downloadstorage measures the directory that holds prepared download
// files, on the API server and on transcode nodes alike. It reads what is on
// disk rather than what the database recorded, so an administrator sees real
// usage, including files no download_artifacts row accounts for.
package downloadstorage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Filesystem types whose contents are lost on a restart or container recreation.
const (
	fsTmpfs   = "tmpfs"
	fsOverlay = "overlay"
	fsRamfs   = "ramfs"
)

// File kinds found in an artifact directory.
const (
	KindComplete = "complete" // a finished prepared file (*.mp4)
	KindPartial  = "partial"  // an encode still writing, or one that died (*.mp4.part)
	KindOther    = "other"    // receipts, temporary receipt files, anything else
)

// File is one entry in an artifact directory. Name is the base name: callers
// match it against the name they expect for a download_artifacts row.
type File struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Bytes   int64     `json:"bytes"`
	ModTime time.Time `json:"mod_time"`
}

// Usage is one measurement of an artifact directory and the filesystem it is on.
type Usage struct {
	// Dir is where the files live. It is deployment layout, so surfaces that
	// answer without a credential must drop it (see RedactPath).
	Dir          string    `json:"dir,omitempty"`
	MeasuredAt   time.Time `json:"measured_at"`
	Files        int       `json:"files"`
	Bytes        int64     `json:"bytes"`
	PartialFiles int       `json:"partial_files"`
	PartialBytes int64     `json:"partial_bytes"`
	OtherBytes   int64     `json:"other_bytes"`
	// FSUsedBytes and FSTotalBytes describe the whole filesystem, measured the
	// way `df` reports Use%: total excludes blocks reserved for root.
	FSUsedBytes  int64  `json:"fs_used_bytes"`
	FSTotalBytes int64  `json:"fs_total_bytes"`
	FSType       string `json:"fs_type,omitempty"`
	// SharesScratch is true when the directory is on the same filesystem as the
	// transcode scratch directory, so prepared files and live transcodes
	// compete for the same space.
	SharesScratch bool `json:"shares_scratch"`
	// Ephemeral is true on filesystems whose contents do not survive a restart
	// or a container recreation (tmpfs, a container's overlay layer).
	Ephemeral bool `json:"ephemeral"`
	// Stale marks numbers carried over because the current measurement has not
	// finished (a hung network mount) or failed.
	Stale bool `json:"stale,omitempty"`
	// Error is the reason the last measurement failed, if it did.
	Error string `json:"error,omitempty"`
}

// TotalBytes is every byte the directory holds: finished, partial, and other files.
func (u Usage) TotalBytes() int64 { return u.Bytes + u.PartialBytes + u.OtherBytes }

// errorText describes a filesystem error without the path it names, so Error
// can be served wherever the usage is, including a node's unauthenticated
// health check.
func errorText(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Op + ": " + pathErr.Err.Error()
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return "the directory could not be read"
}

// RedactPath returns a copy without the directory path.
func (u Usage) RedactPath() Usage {
	u.Dir = ""
	return u
}

// fileKind classifies an artifact directory entry by name. The API server names
// files <media file>_<format>_<hash>_<id>.mp4 and nodes name them <id>.mp4;
// both write <name>.part while encoding and nodes add <name>.receipt.json.
func fileKind(name string) string {
	switch {
	case strings.HasSuffix(name, ".mp4"):
		return KindComplete
	case strings.HasSuffix(name, ".mp4.part"):
		return KindPartial
	default:
		return KindOther
	}
}

// ListFiles reads the regular files directly inside dir. A directory that does
// not exist yet holds nothing and is not an error: both the server and nodes
// create it on the first preparation.
func ListFiles(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]File, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			// Removed between the read and the stat: a cleanup or a rename
			// raced the listing, and the file is simply no longer there.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, File{Name: entry.Name(), Kind: fileKind(entry.Name()), Bytes: info.Size(), ModTime: info.ModTime()})
	}
	return out, nil
}

// Listing is a measurement together with the files it counted, as a node
// returns it to the API for reconciliation.
type Listing struct {
	Usage Usage  `json:"usage"`
	Files []File `json:"files"`
}

// Measure reads dir and the filesystem it lives on. scratchDir is the transcode
// working directory, used only to tell whether the two share a filesystem; it
// may be empty. Measure may block on an unresponsive network mount, so callers
// that must stay responsive run it through a Prober.
func Measure(dir, scratchDir string, now time.Time) Usage {
	return Inspect(dir, scratchDir, now).Usage
}

// Inspect is Measure that also returns the files it counted.
func Inspect(dir, scratchDir string, now time.Time) Listing {
	u := Usage{Dir: dir, MeasuredAt: now}
	files, err := ListFiles(dir)
	if err != nil {
		u.Error = errorText(err)
		return Listing{Usage: u}
	}
	for _, f := range files {
		switch f.Kind {
		case KindComplete:
			u.Files++
			u.Bytes += f.Bytes
		case KindPartial:
			u.PartialFiles++
			u.PartialBytes += f.Bytes
		default:
			u.OtherBytes += f.Bytes
		}
	}
	existing := nearestExisting(dir)
	stats, err := statFS(existing)
	if err != nil {
		u.Error = errorText(&fs.PathError{Op: "statfs", Path: existing, Err: err})
		return Listing{Usage: u, Files: files}
	}
	u.FSUsedBytes, u.FSTotalBytes, u.FSType = stats.used, stats.total, stats.fsType
	u.Ephemeral = ephemeralFS(stats.fsType)
	if scratchDir != "" {
		if a, err := deviceOf(existing); err == nil {
			if b, err := deviceOf(nearestExisting(scratchDir)); err == nil {
				u.SharesScratch = a == b
			}
		}
	}
	return Listing{Usage: u, Files: files}
}

// nearestExisting walks up from path to the closest directory that exists, so
// a not-yet-created artifact directory reports the filesystem it will be on.
func nearestExisting(path string) string {
	path = filepath.Clean(path)
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

// ephemeralFS reports whether files on a filesystem of this type are lost on a
// restart (tmpfs) or when the container is recreated (the overlay layer of a
// container's root filesystem; a volume or bind mount reports its own type).
func ephemeralFS(fsType string) bool {
	switch fsType {
	case fsTmpfs, fsOverlay, fsRamfs:
		return true
	}
	return false
}

type fsStats struct {
	used, total int64
	fsType      string
}
