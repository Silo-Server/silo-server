package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Silo-Server/silo-server/internal/lang"
)

// subtitleExtensions lists the recognized external subtitle file extensions.
var subtitleExtensions = map[string]bool{
	".srt": true,
	".vtt": true,
	".ass": true,
	".ssa": true,
	".sub": true,
}

type externalSubtitleDirCache struct {
	mu   sync.Mutex
	dirs map[string]externalSubtitleDirListing
}

type externalSubtitleDirListing struct {
	entries []os.DirEntry
	err     error
}

func newExternalSubtitleDirCache() *externalSubtitleDirCache {
	return &externalSubtitleDirCache{
		dirs: make(map[string]externalSubtitleDirListing),
	}
}

func (c *externalSubtitleDirCache) Detect(mediaFilePath string) ([]ExternalSubtitleInfo, error) {
	if c == nil {
		return DetectExternalSubtitles(mediaFilePath)
	}

	dir := filepath.Dir(mediaFilePath)
	c.mu.Lock()
	listing, ok := c.dirs[dir]
	c.mu.Unlock()
	if !ok {
		entries, err := os.ReadDir(dir)
		listing = externalSubtitleDirListing{entries: entries, err: err}
		c.mu.Lock()
		if existing, found := c.dirs[dir]; found {
			listing = existing
		} else {
			c.dirs[dir] = listing
		}
		c.mu.Unlock()
	}
	if listing.err != nil {
		return nil, fmt.Errorf("subtitles: read dir %s: %w", dir, listing.err)
	}

	return externalSubtitlesFromEntries(mediaFilePath, listing.entries), nil
}

// DetectExternalSubtitles scans the directory containing the media file for
// sidecar subtitle files that match the media file's basename.
//
// Naming conventions:
//
//	Movie.srt             -> no language
//	Movie.eng.srt         -> language: "eng"
//	Movie.en.srt          -> language: "en"
//	Movie.en.forced.srt   -> language: "en", forced: true
//	Movie.forced.eng.srt  -> language: "eng", forced: true
func DetectExternalSubtitles(mediaFilePath string) ([]ExternalSubtitleInfo, error) {
	dir := filepath.Dir(mediaFilePath)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("subtitles: read dir %s: %w", dir, err)
	}

	return externalSubtitlesFromEntries(mediaFilePath, entries), nil
}

func externalSubtitlesFromEntries(mediaFilePath string, entries []os.DirEntry) []ExternalSubtitleInfo {
	dir := filepath.Dir(mediaFilePath)
	mediaBase := stripExtension(filepath.Base(mediaFilePath))
	var results []ExternalSubtitleInfo

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !subtitleExtensions[ext] {
			continue
		}

		// Check that the subtitle file starts with the media basename
		// followed by either a dot separator or nothing (exact match).
		nameWithoutExt := name[:len(name)-len(ext)]
		if !strings.HasPrefix(nameWithoutExt, mediaBase) {
			continue
		}

		// The suffix after the base name must be empty or start with a dot.
		// This prevents "MovieExtra.srt" from matching "Movie.mkv".
		suffix := nameWithoutExt[len(mediaBase):]
		if suffix != "" && !strings.HasPrefix(suffix, ".") {
			continue
		}

		info := ExternalSubtitleInfo{
			Path:   filepath.Join(dir, name),
			Format: strings.TrimPrefix(ext, "."),
			Title:  name,
		}

		parseSuffix(suffix, &info)

		results = append(results, info)
	}

	return results
}

// parseSuffix extracts the language and forced flag from the suffix portion
// of a subtitle filename. The suffix is the part between the media basename
// and the subtitle extension, e.g. ".en.forced" or ".forced.eng" or ".eng".
func parseSuffix(suffix string, info *ExternalSubtitleInfo) {
	if suffix == "" {
		return
	}

	// Split on dots, filtering out empty parts.
	parts := splitDots(suffix)
	if len(parts) == 0 {
		return
	}

	// Identify forced flag and language from the parts.
	// Valid patterns:
	//   [lang]
	//   [lang, "forced"]
	//   ["forced", lang]
	for _, p := range parts {
		lower := strings.ToLower(p)
		if lower == "forced" {
			info.Forced = true
		} else if info.Language == "" {
			info.Language = lang.Canonical(p)
		}
	}
}

// splitDots splits a string on '.' and returns non-empty parts.
func splitDots(s string) []string {
	raw := strings.Split(s, ".")
	var parts []string
	for _, p := range raw {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// stripExtension removes the file extension from a filename.
func stripExtension(name string) string {
	ext := filepath.Ext(name)
	return name[:len(name)-len(ext)]
}
