package catalog

import (
	"slices"
	"strings"
)

// A file's identity is where it lives on the server and what it is called:
// its path, its file name, an external subtitle's file name, and a subtitle
// title that is only that name. Only a viewer who may curate metadata
// sees it. Everyone else still gets every technical detail of a version.
//
// Audiobooks and ebooks keep their file names: those beta features name,
// order and open files by them.

// HideFileIdentity strips file identity from versions sent to a viewer who
// may not see it. itemType is the type of the item that owns them.
func HideFileIdentity(versions []FileVersion, itemType string) {
	keepNames := itemType == detailTypeAudiobook || itemType == detailTypeEbook
	for i := range versions {
		versions[i].hideFileIdentity(keepNames)
	}
}

// HideFileIdentity strips file identity from the detail's versions, playback
// variant parts included, and drops its folder paths.
func (d *ItemDetail) HideFileIdentity() {
	if d == nil {
		return
	}
	HideFileIdentity(d.Versions, d.Type)
	hideVariantFileIdentity(d.PlaybackVariants, d.Type)
	d.FolderPaths = nil
}

// HideFileIdentity strips file identity from the watch detail's versions,
// playback variant parts included.
func (d *WatchDetail) HideFileIdentity() {
	if d == nil {
		return
	}
	HideFileIdentity(d.Versions, d.Type)
	hideVariantFileIdentity(d.PlaybackVariants, d.Type)
}

func hideVariantFileIdentity(variants []PlaybackVariant, itemType string) {
	for i := range variants {
		for j := range variants[i].Parts {
			HideFileIdentity(variants[i].Parts[j].Versions, itemType)
		}
	}
}

func (v *FileVersion) hideFileIdentity(keepNames bool) {
	v.FilePath = ""
	if keepNames {
		return
	}
	v.FileName = ""
	// A playback variant part holds a copy of the version that shares this
	// slice, so edit a copy of it.
	tracks := slices.Clone(v.SubtitleTracks)
	for i := range tracks {
		t := &tracks[i]
		// The catalog fills an untitled sidecar's title from its file name,
		// and the scanner stores the file name as its title.
		if t.External && (t.TitleIsFallback || strings.TrimSpace(t.Title) == t.FileName) {
			t.Title, t.TitleIsFallback = "", false
		}
		t.FileName = ""
	}
	v.SubtitleTracks = tracks
}
