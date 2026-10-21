package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Silo-Server/silo-server/internal/librarykind"
	"github.com/Silo-Server/silo-server/internal/naming"
)

// subtitleExtensions lists the recognized external subtitle file extensions.
// External bitmap subtitles are left out: playback serves no external bitmap
// sidecar and cannot burn one in, so they would be tracks that never load.
// That means .sup, and a VobSub .sub, which is told apart from a text .sub by
// the .idx beside it (see newExternalSubtitleDirListing).
var subtitleExtensions = map[string]bool{
	".srt": true,
	".vtt": true,
	".ass": true,
	".ssa": true,
	".sub": true,
}

// subtitleDirLabels are the normalized names (normalizeScannerDirLabel) of the
// folders beside a video that releases put its subtitles in. A movie walk
// skips them as media folders.
var subtitleDirLabels = map[string]bool{
	"subs":      true,
	"subtitles": true,
}

// IsSubtitleSidecar reports whether path names a file subtitle discovery
// reads: a text subtitle, or a VobSub .idx, which decides whether the .sub
// beside it is read.
func IsSubtitleSidecar(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return subtitleExtensions[ext] || ext == ".idx"
}

// SubtitleFolderOwner returns the directory whose videos read the subtitles
// in dir: the one holding dir when dir is a Subs/ or Subtitles/ folder, or
// the one holding that folder when dir is inside it (Subs/<video>/). ok is
// false for any other directory.
func SubtitleFolderOwner(dir string) (string, bool) {
	parent := filepath.Dir(dir)
	if subtitleDirLabels[normalizeScannerDirLabel(filepath.Base(dir))] {
		return parent, true
	}
	if subtitleDirLabels[normalizeScannerDirLabel(filepath.Base(parent))] {
		return filepath.Dir(parent), true
	}
	return "", false
}

// externalSubtitleDirCache lists each directory once per scan. Every video
// in a directory reads the same listings, so a season of episodes costs one
// read of the season folder and of each subtitle folder in it.
type externalSubtitleDirCache struct {
	folderType string

	mu   sync.Mutex
	dirs map[string]externalSubtitleDirListing
}

type externalSubtitleDirListing struct {
	candidates []externalSubtitleCandidate
	// videos holds the base names, without extension, of the video files.
	videos []string
	// features counts, in a movie library, the videos a movie scan keeps as
	// the folder's own title: not samples and not extras named with a suffix.
	features     int
	subtitleDirs []string
	childDirs    []string
	err          error
}

type externalSubtitleCandidate struct {
	name string
	base string
	ext  string
}

func newExternalSubtitleDirCache(folderType string) *externalSubtitleDirCache {
	return &externalSubtitleDirCache{
		folderType: folderType,
		dirs:       make(map[string]externalSubtitleDirListing),
	}
}

func (c *externalSubtitleDirCache) listing(dir string) (externalSubtitleDirListing, error) {
	c.mu.Lock()
	listing, ok := c.dirs[dir]
	c.mu.Unlock()
	if !ok {
		entries, err := os.ReadDir(dir)
		listing = newExternalSubtitleDirListing(dir, entries, librarykind.IsMovie(c.folderType))
		listing.err = err
		c.mu.Lock()
		if existing, found := c.dirs[dir]; found {
			listing = existing
		} else {
			c.dirs[dir] = listing
		}
		c.mu.Unlock()
	}
	if listing.err != nil {
		return listing, fmt.Errorf("subtitles: read dir %s: %w", dir, listing.err)
	}
	return listing, nil
}

// Detect finds the sidecar subtitles of the video at mediaFilePath:
//
//   - files beside it named for it ("Movie.en.srt", "movie.EN.srt");
//   - in a Subs/ or Subtitles/ folder beside it, files named for it, and
//     every file in a folder named for it ("Subs/Episode/2_English.srt");
//   - in a movie library, the other files in that folder when they name only
//     a language or flags ("Subs/English.srt") and the video is the folder's
//     only feature. Several features (versions, parts, a mixed folder) or a
//     season folder of episodes leave such a file without an owner.
//
// The language and flags come from the name, as ParseSubtitleSuffix reads
// them. When a subtitle folder can't be read, Detect returns the error with
// the sidecars it found before it.
func (c *externalSubtitleDirCache) Detect(mediaFilePath string) ([]ExternalSubtitleInfo, error) {
	dir := filepath.Dir(mediaFilePath)
	mediaBase := stripExtension(filepath.Base(mediaFilePath))
	top, err := c.listing(dir)
	if err != nil {
		return nil, err
	}

	var results []ExternalSubtitleInfo
	add := func(dir string, candidate externalSubtitleCandidate, tags naming.SubtitleTags) {
		results = append(results, ExternalSubtitleInfo{
			Path:            filepath.Join(dir, candidate.name),
			Format:          strings.TrimPrefix(candidate.ext, "."),
			Title:           candidate.name,
			Language:        tags.Language,
			Forced:          tags.Forced,
			HearingImpaired: tags.HearingImpaired,
		})
	}
	// addNamed adds the candidates named for the video and returns the rest.
	addNamed := func(dir string, candidates []externalSubtitleCandidate) []externalSubtitleCandidate {
		var rest []externalSubtitleCandidate
		for _, candidate := range candidates {
			if suffix, ok := sidecarSuffix(mediaBase, candidate.base, top.videos); ok {
				add(dir, candidate, naming.ParseSubtitleSuffix(suffix))
			} else {
				rest = append(rest, candidate)
			}
		}
		return rest
	}

	addNamed(dir, top.candidates)
	soleFeature := librarykind.IsMovie(c.folderType) && top.features == 1 && isMovieFeature(mediaFilePath)
	for _, name := range top.subtitleDirs {
		subsDir := filepath.Join(dir, name)
		subs, err := c.listing(subsDir)
		if err != nil {
			return results, err
		}
		for _, candidate := range addNamed(subsDir, subs.candidates) {
			if !soleFeature {
				continue
			}
			if tags, ok := naming.SubtitleDescriptorTags(candidate.base); ok {
				add(subsDir, candidate, tags)
			}
		}
		for _, child := range subs.childDirs {
			if suffix, ok := sidecarSuffix(mediaBase, child, top.videos); !ok || suffix != "" {
				continue
			}
			videoDir := filepath.Join(subsDir, child)
			named, err := c.listing(videoDir)
			if err != nil {
				return results, err
			}
			for _, candidate := range addNamed(videoDir, named.candidates) {
				// Every file here is the video's; a name that isn't a
				// descriptor just leaves the language unknown.
				tags, _ := naming.SubtitleDescriptorTags(candidate.base)
				add(videoDir, candidate, tags)
			}
		}
	}
	return results, nil
}

func newExternalSubtitleDirListing(dir string, entries []os.DirEntry, movies bool) externalSubtitleDirListing {
	var listing externalSubtitleDirListing
	vobSubs := make(map[string]bool)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			listing.childDirs = append(listing.childDirs, name)
			if subtitleDirLabels[normalizeScannerDirLabel(name)] {
				listing.subtitleDirs = append(listing.subtitleDirs, name)
			}
			continue
		}
		path := filepath.Join(dir, name)
		ext := strings.ToLower(filepath.Ext(name))
		base := stripExtension(name)
		switch {
		case ext == ".idx":
			vobSubs[strings.ToLower(base)] = true
		case subtitleExtensions[ext]:
			listing.candidates = append(listing.candidates, externalSubtitleCandidate{name: name, base: base, ext: ext})
		case SupportsVideoFile(path):
			listing.videos = append(listing.videos, base)
			if movies && isMovieFeature(path) {
				listing.features++
			}
		}
	}
	listing.candidates = slices.DeleteFunc(listing.candidates, func(candidate externalSubtitleCandidate) bool {
		return candidate.ext == ".sub" && vobSubs[strings.ToLower(candidate.base)]
	})
	return listing
}

// isMovieFeature reports whether a movie scan keeps the video at path as its
// folder's title rather than skipping it as a sample or filing it as an extra
// by its name.
func isMovieFeature(path string) bool {
	if shouldSkipMovieSupplementalFile(path) {
		return false
	}
	_, extra := naming.ParseExtraSuffix(path)
	return !extra
}

// sidecarSuffix is naming.SubtitleSuffix with one more rule for a name that
// matches only when case is ignored: it does not belong to the video when
// another video in the folder, from the videos list, has exactly that name or
// differs from this video's name only in case ("Movie" and "movie").
func sidecarSuffix(mediaBase, base string, videos []string) (string, bool) {
	suffix, ok := naming.SubtitleSuffix(mediaBase, base)
	if !ok || base[:len(base)-len(suffix)] == mediaBase {
		return suffix, ok
	}
	for _, video := range videos {
		if video == mediaBase {
			continue
		}
		if strings.EqualFold(video, mediaBase) ||
			strings.HasPrefix(base, video) && (len(base) == len(video) || base[len(video)] == '.') {
			return "", false
		}
	}
	return suffix, true
}

// stripExtension removes the file extension from a filename.
func stripExtension(name string) string {
	ext := filepath.Ext(name)
	return name[:len(name)-len(ext)]
}
