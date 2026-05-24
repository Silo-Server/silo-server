package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// parsedAudiobook is the structured output of parseAudiobookFolder.
// The scanner write path (Task 8) converts this into media_items +
// media_files + item_people rows.
type parsedAudiobook struct {
	Title    string
	Author         string
	Narrator       string
	Series         string
	SeriesPosition string
	Year           int
	ASIN           string
	Overview       string
	Genres         []string
	Publisher      string
	ReleaseDate    string
	Language       string
	Files          []parsedAudiobookFile
}

// parsedAudiobookFile is one audio file belonging to a parsed audiobook.
// For single-file .m4b audiobooks there is exactly one entry; for
// multi-file folders (Task 7) there is one per file.
type parsedAudiobookFile struct {
	Path          string
	Chapters      []ChapterInfo
	Duration      int    // seconds
	Bitrate       int    // kbps
	CodecAudio    string // aac, mp3, opus, flac
	Container     string // m4b, mp3, mka, ...
	AudioChannels int
}

// parseAudiobookFolder reads a single audiobook folder and returns its
// structured representation. Recognized layouts:
//   - one audio file in the folder, optionally with embedded chapters
//   - multiple audio files in the folder; each becomes its own
//     parsedAudiobookFile with a single synthesized chapter (title =
//     filename stem); metadata comes from the first file's tags
//
// Returns an error wrapping os.ErrNotExist when the folder contains zero
// audio files, so the caller can skip it.
func parseAudiobookFolder(ctx context.Context, ffprobePath string, folderPath string) (*parsedAudiobook, error) {
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, fmt.Errorf("read audiobook folder %s: %w", folderPath, err)
	}

	var audioFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if SupportsAudioFile(entry.Name()) {
			audioFiles = append(audioFiles, filepath.Join(folderPath, entry.Name()))
		}
	}
	if len(audioFiles) == 0 {
		return nil, fmt.Errorf("audiobook folder %s: %w", folderPath, os.ErrNotExist)
	}
	sort.Strings(audioFiles)

	book := &parsedAudiobook{}

	if len(audioFiles) == 1 {
		probed, err := ProbeFile(ctx, ffprobePath, audioFiles[0])
		if err != nil {
			return nil, fmt.Errorf("probe audiobook file %s: %w", audioFiles[0], err)
		}
		book.populateFromTags(probed.FormatTags)
		book.Files = []parsedAudiobookFile{{
			Path:          audioFiles[0],
			Chapters:      probed.Chapters,
			Duration:      probed.Duration,
			Bitrate:       probed.Bitrate,
			CodecAudio:    probed.CodecAudio,
			Container:     probed.Container,
			AudioChannels: probed.AudioChannels,
		}}
		return book, nil
	}

	// Multi-file case: read header from the first file, synthesize one
	// chapter per file with title = filename stem.
	probedFirst, err := ProbeFile(ctx, ffprobePath, audioFiles[0])
	if err != nil {
		return nil, fmt.Errorf("probe first audiobook file %s: %w", audioFiles[0], err)
	}
	book.populateFromTags(probedFirst.FormatTags)

	book.Files = make([]parsedAudiobookFile, 0, len(audioFiles))
	for i, path := range audioFiles {
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		probed, err := ProbeFile(ctx, ffprobePath, path)
		if err != nil {
			return nil, fmt.Errorf("probe audiobook file %s: %w", path, err)
		}
		book.Files = append(book.Files, parsedAudiobookFile{
			Path: path,
			Chapters: []ChapterInfo{{
				Index:        i,
				Title:        stem,
				StartSeconds: 0,
				EndSeconds:   float64(probed.Duration),
			}},
			Duration:      probed.Duration,
			Bitrate:       probed.Bitrate,
			CodecAudio:    probed.CodecAudio,
			Container:     probed.Container,
			AudioChannels: probed.AudioChannels,
		})
	}
	return book, nil
}

// populateFromTags fills the audiobook's header fields (Title, Author,
// Narrator, Series, Year) from the ffprobe format tags. Tags are
// lower-cased by normalizeFormatTags upstream.
func (b *parsedAudiobook) populateFromTags(tags map[string]string) {
	b.Title = firstNonEmpty(tags["title"], tags["album"])
	b.Author = firstNonEmpty(tags["album_artist"], tags["artist"], tags["composer"])
	b.Narrator = firstNonEmpty(tags["narrator"], tags["performer"], tags["composer"])
	b.Series = firstNonEmpty(tags["series"], tags["mvnm"], tags["album"])
	b.SeriesPosition = firstNonEmpty(tags["series-part"], tags["mvin"], tags["movement"])
	b.ASIN = firstNonEmpty(tags["asin"], tags["audible_asin"], tags["com.audible.asin"])
	b.Overview = firstNonEmpty(tags["description"], tags["summary"], tags["comment"], tags["©cmt"])
	b.Publisher = firstNonEmpty(tags["publisher"], tags["label"], tags["©pub"])
	b.ReleaseDate = firstNonEmpty(tags["releasedate"], tags["release_date"], tags["date"], tags["year"], tags["releasetime"])
	b.Language = firstNonEmpty(tags["language"], tags["lang"])
	if year := firstNonEmpty(tags["date"], tags["year"], tags["releasetime"]); year != "" {
		if y := parseTagYear(year); y > 0 {
			b.Year = y
		}
	}
	b.Genres = parseGenresFromTags(tags)
}

// parseGenresFromTags pulls genres from the `genre` tag (which is often
// slash-separated like "Mystery, Thriller & Suspense/Suspense/Fiction")
// plus tmp_Genre1..tmp_Genre5 sub-tags written by some audiobook taggers.
// Returns a deduplicated list in input order.
func parseGenresFromTags(tags map[string]string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(g string) {
		g = strings.TrimSpace(g)
		if g == "" {
			return
		}
		key := strings.ToLower(g)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, g)
	}
	if raw := tags["genre"]; raw != "" {
		for _, part := range strings.Split(raw, "/") {
			for _, sub := range strings.Split(part, ",") {
				add(sub)
			}
		}
	}
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("tmp_genre%d", i)
		if v := tags[key]; v != "" {
			add(v)
		}
	}
	return out
}

// parseTagYear extracts a 4-digit year (e.g. 1900-9999) from a tag value
// that may be a bare year ("2024"), an ISO date ("2024-05-23"), or a
// padded form ("(2024)"). Returns 0 if no plausible year is found.
func parseTagYear(s string) int {
	s = strings.TrimSpace(s)
	for i := 0; i+4 <= len(s); i++ {
		candidate := s[i : i+4]
		if isAllDigits(candidate) {
			year := 0
			for _, c := range candidate {
				year = year*10 + int(c-'0')
			}
			if year >= 1900 && year <= 9999 {
				return year
			}
		}
	}
	return 0
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
