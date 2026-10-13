package scanner

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// localMetadataSidecarExtensions are the files the builtin NFO provider reads
// beside media: .nfo documents and sidecar artwork. It must cover every
// extension internal/metadata/nfo reads (TestSidecarExtensionsCoverNFOReads).
var localMetadataSidecarExtensions = strings.Fields(".nfo .jpg .jpeg .png .webp .tbn .gif")

// IsLocalMetadataSidecar reports whether path names a local metadata sidecar:
// an .nfo document or sidecar artwork next to media.
func IsLocalMetadataSidecar(path string) bool {
	return slices.Contains(localMetadataSidecarExtensions, strings.ToLower(filepath.Ext(path)))
}

// SidecarListing maps each directory a video scan read to the names of the
// local metadata sidecars in it. A directory without sidecars maps to an empty
// slice; a directory the scan did not read is absent. It comes from the
// directory listings the walk already reads, so it costs the scan no extra
// filesystem call; metadata compares it with what it last applied to notice
// an edited NFO or replaced poster.
type SidecarListing map[string][]string

// record adds the sidecars among a directory's entries.
func (l SidecarListing) record(dir string, entries []os.DirEntry) {
	names := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() && IsLocalMetadataSidecar(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	l[dir] = names
}

// merge adds other's directories to l.
func (l SidecarListing) merge(other SidecarListing) {
	for dir, names := range other {
		l[dir] = names
	}
}

// ReadSidecarListing lists the local metadata sidecars in each of dirs the way
// a scan does. A directory that cannot be read is left out.
func ReadSidecarListing(dirs []string) SidecarListing {
	listing := make(SidecarListing, len(dirs))
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		listing.record(dir, entries)
	}
	return listing
}
