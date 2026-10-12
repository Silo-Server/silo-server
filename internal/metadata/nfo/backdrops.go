package nfo

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// Numbered backdrops follow Jellyfin's LocalImageProvider: each family is
// tried from 1 to maxNumberedBackdrop and stops after maxBackdropGap numbers in
// a row are missing. Kodi spells extra fanart without the dash (fanart1.jpg),
// so a number counts as present under either spelling.
var numberedBackdropFamilies = [][]string{
	{"fanart-%d", "fanart%d"},
	{"background-%d"},
	{"art-%d"},
	{"backdrop%d"},
}

const (
	maxNumberedBackdrop = 20
	maxBackdropGap      = 3
)

// findBackdrops returns the item's backdrop sources in display order, at most
// metadata.MaxItemBackdrops. The first is the primary backdrop, found by the
// same precedence findItemArtwork uses: the media file's "-fanart" sidecar,
// then fanart, backdrop, background and art. The numbered families follow,
// then the images in extrafanart/, sorted by name, while room remains; images
// past the cap are never checked.
func (f *sidecarFinder) findBackdrops(contentType string, searchDirs, mediaFiles []string) []string {
	var sources []string
	seen := make(map[string]bool)
	add := func(source string) bool {
		if source == "" {
			return false
		}
		if !seen[source] && len(sources) < metadata.MaxItemBackdrops {
			seen[source] = true
			sources = append(sources, source)
		}
		return true
	}

	suffixes, names := itemArtworkNames(contentType, metadata.ImageBackdrop)
	for _, suffix := range suffixes {
		for _, mediaFile := range mediaFiles {
			add(f.find(filepath.Dir(mediaFile), mediaBasename(mediaFile)+suffix))
		}
	}
	for _, name := range names {
		for _, dir := range searchDirs {
			add(f.find(dir, name))
		}
	}
	for _, dir := range searchDirs {
		for _, family := range numberedBackdropFamilies {
			missing := 0
			for n := 1; n <= maxNumberedBackdrop && missing < maxBackdropGap; n++ {
				found := false
				for _, pattern := range family {
					if add(f.find(dir, fmt.Sprintf(pattern, n))) {
						found = true
						break
					}
				}
				if found {
					missing = 0
				} else {
					missing++
				}
			}
		}
		if room := metadata.MaxItemBackdrops - len(sources); room > 0 {
			for _, source := range f.extraFanart(dir, room) {
				add(source)
			}
		}
	}
	return sources
}

// extraFanart returns up to limit usable images in dir's extrafanart folder,
// sorted by name ignoring case. It stops checking files once it has limit, so a
// large folder on a network share is not checked file by file.
func (f *sidecarFinder) extraFanart(dir string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	folders := []string{metadata.ExtraFanartFolder}
	if listing := f.listing(dir); listing != nil {
		folders = listing[metadata.ExtraFanartFolder]
	}
	var sources []string
	for _, folder := range folders {
		entries := f.listing(filepath.Join(dir, folder))
		names := make([]string, 0, len(entries))
		for lower := range entries {
			if slices.Contains(localArtworkExtensions, strings.ToLower(filepath.Ext(lower))) {
				names = append(names, lower)
			}
		}
		slices.Sort(names)
		for _, lower := range names {
			for _, entry := range entries[lower] {
				if source := f.usableSource(filepath.Join(dir, folder, entry)); source != "" {
					sources = append(sources, source)
					if len(sources) >= limit {
						return sources
					}
				}
			}
		}
	}
	return sources
}
