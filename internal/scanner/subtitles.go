package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Silo-Server/silo-server/internal/naming"
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
	candidates []externalSubtitleCandidate
	err        error
}

type externalSubtitleCandidate struct {
	name string
	base string
	ext  string
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
		listing = externalSubtitleDirListing{candidates: externalSubtitleCandidates(entries), err: err}
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

	return externalSubtitlesFromCandidates(mediaFilePath, listing.candidates), nil
}

// DetectExternalSubtitles scans the directory containing the media file for
// sidecar subtitle files that match the media file's basename.
//
// Naming conventions:
//
//	Movie.srt             -> no language
//	Movie.eng.srt         -> language: "en"
//	Movie.en.forced.srt   -> language: "en", forced: true
//	Movie.forced.eng.srt  -> language: "en", forced: true
//	Movie.sdh.en.srt      -> language: "en", hearing impaired: true
//	Movie.ai.hr.srt       -> language: "hr"
//	Movie.hi.srt          -> language: "hi" (Hindi)
//	Movie.en.hi.srt       -> language: "en", hearing impaired: true
func DetectExternalSubtitles(mediaFilePath string) ([]ExternalSubtitleInfo, error) {
	dir := filepath.Dir(mediaFilePath)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("subtitles: read dir %s: %w", dir, err)
	}

	return externalSubtitlesFromEntries(mediaFilePath, entries), nil
}

func externalSubtitlesFromEntries(mediaFilePath string, entries []os.DirEntry) []ExternalSubtitleInfo {
	return externalSubtitlesFromCandidates(mediaFilePath, externalSubtitleCandidates(entries))
}

func externalSubtitleCandidates(entries []os.DirEntry) []externalSubtitleCandidate {
	var candidates []externalSubtitleCandidate
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !subtitleExtensions[ext] {
			continue
		}
		candidates = append(candidates, externalSubtitleCandidate{
			name: name,
			base: name[:len(name)-len(ext)],
			ext:  ext,
		})
	}
	return candidates
}

func externalSubtitlesFromCandidates(mediaFilePath string, candidates []externalSubtitleCandidate) []ExternalSubtitleInfo {
	dir := filepath.Dir(mediaFilePath)
	mediaBase := stripExtension(filepath.Base(mediaFilePath))
	var results []ExternalSubtitleInfo

	for _, candidate := range candidates {
		suffix, ok := naming.SubtitleSuffix(mediaBase, candidate.base)
		if !ok {
			continue
		}

		info := ExternalSubtitleInfo{
			Path:   filepath.Join(dir, candidate.name),
			Format: strings.TrimPrefix(candidate.ext, "."),
			Title:  candidate.name,
		}

		parseSuffix(suffix, &info)

		results = append(results, info)
	}

	return results
}

// parseSuffix reads the language and flags from the suffix portion of a
// subtitle filename: the part between the media basename and the subtitle
// extension, e.g. ".en.forced" or ".sdh.eng".
func parseSuffix(suffix string, info *ExternalSubtitleInfo) {
	tags := naming.ParseSubtitleSuffix(suffix)
	info.Language = tags.Language
	info.Forced = tags.Forced
	info.HearingImpaired = tags.HearingImpaired
}

// stripExtension removes the file extension from a filename.
func stripExtension(name string) string {
	ext := filepath.Ext(name)
	return name[:len(name)-len(ext)]
}
