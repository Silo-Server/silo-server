package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// narratorSuffixRE matches a trailing "read by X" / "(Read by X)" /
// "(UK Version: Read by X)" / "- read by X" pattern that some audiobook
// taggers put in the title field. The narrator is already captured as
// item_people kind=8 from the dedicated narrator tag, so removing this
// noise yields a clean human-readable title without losing any data.
//
// The narrator body deliberately excludes the dash so titles like
// "Series Name Read by Foo N - Book Title" don't get mistakenly
// truncated (the "Read by Foo" there is part of the series name, not a
// narrator credit). Real narrator suffixes never contain a `-` after the
// "read by".
var narratorSuffixRE = regexp.MustCompile(`(?i)\s*\(?\s*[-:,]?\s*(UK Version:?|US Version:?)?\s*read by [A-Za-z0-9., '&]+\)?\s*$`)

// unabridgedTokenRE matches a parenthesized "(unabridged)" anywhere in
// the title (sometimes mid-string between series and book). Stripped
// because it's a format marker, not part of the work's name.
var unabridgedTokenRE = regexp.MustCompile(`(?i)\s*\(unabridged\)\s*`)

// collapseSpacesRE squashes any runs of whitespace into a single space.
// Used after the strip passes since removing a mid-string token can
// leave double spaces behind.
var collapseSpacesRE = regexp.MustCompile(`\s+`)
var audiobookDedupeTitleTokenRE = regexp.MustCompile(`[^A-Za-z0-9]+`)

var audiobookFilesystemTitleNumericIDRE = regexp.MustCompile(`\s*[\[(]\d+[\])]\s*$`)
var audiobookFilesystemTitleNoiseSuffixRE = regexp.MustCompile(`(?i)(?:\s*[-_. ]+\s*)?(?:nmr|audio\s*book|audiobook|unabridged|abridged)\s*$`)

// stripNarratorSuffix removes the narrator-suffix noise and "(unabridged)"
// markers from a title. Returns the input unchanged when no match.
// Kept in sync with the SQL `regexp_replace` used by migration 146 so
// the scanner write path and one-shot backfill produce identical output.
func stripNarratorSuffix(title string) string {
	cleaned := narratorSuffixRE.ReplaceAllString(title, "")
	cleaned = unabridgedTokenRE.ReplaceAllString(cleaned, " ")
	cleaned = collapseSpacesRE.ReplaceAllString(cleaned, " ")
	return strings.TrimSpace(cleaned)
}

func normalizeAudiobookDedupeTitle(title string) string {
	title = stripNarratorSuffix(title)
	title = audiobookDedupeTitleTokenRE.ReplaceAllString(title, " ")
	title = collapseSpacesRE.ReplaceAllString(title, " ")
	return strings.ToLower(strings.TrimSpace(title))
}

func audiobookDedupeTitlesMatch(a, b string) bool {
	left := normalizeAudiobookDedupeTitle(a)
	right := normalizeAudiobookDedupeTitle(b)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	if len(left) < len(right) {
		return strings.HasPrefix(right, left+" ")
	}
	return strings.HasPrefix(left, right+" ")
}

// parsedAudiobook is the structured output of parseAudiobookFolder.
// The scanner write path (Task 8) converts this into media_items +
// media_files + item_people rows.
type parsedAudiobook struct {
	Title string
	// Author and Narrator are the first of Authors and Narrators, the
	// people named by the author and narrator tags.
	Author         string
	Narrator       string
	Authors        []string
	Narrators      []string
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
	// UnreadableParts lists audio files ffprobe rejected as damaged. The
	// book is indexed from its other parts.
	UnreadableParts []string
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
	// Size and ModifiedAt are captured before probing so the persisted row
	// can be checked against them after the probe: a file replaced mid-scan
	// must not store old probe facts under the new file's identity.
	Size       int64
	ModifiedAt time.Time
}

// parseAudiobookFolder reads the book rooted at folderPath (a book folder, or
// the file of a loose single-file book; audiobookBookParts lists its parts in
// disc-folder then filename order) and returns its structured representation:
//   - one audio file, optionally with embedded chapters
//   - multiple audio files, including those in disc subfolders; each becomes its own
//     parsedAudiobookFile, ordered and read as parseAudiobookParts describes
//
// Returns an error wrapping errFolderHasNoMedia when the book has no audio
// files, so the caller can skip it. Every other error (including an
// ffprobe binary that cannot be executed) is a real failure and must be
// reported by the caller.
// allowedPaths is the inventory accepted by discovery, including ignore rules.
// A nil inventory allows every audio file in the folder. walked, when set, is
// what the walk read of the folder (audiobookWalkedBook.partsOrRelist).
func parseAudiobookFolder(ctx context.Context, ffprobePath string, folderPath string, allowedPaths map[string]bool, walked *audiobookWalkedBook) (*parsedAudiobook, error) {
	var book *parsedAudiobook
	err := walked.partsOrRelist(folderPath, allowedPaths, func(parts []string) (err error) {
		book, err = parseAudiobookParts(ctx, ffprobePath, folderPath, parts)
		return err
	})
	return book, err
}

// probedAudiobookPart is one readable part of a book with the format tags
// its probe reported.
type probedAudiobookPart struct {
	file parsedAudiobookFile
	tags map[string]string
}

// parseAudiobookParts probes a book's audio files (in audiobookBookParts order)
// and assembles the book:
//   - A part ffprobe rejects as damaged is left out with a warning; the book
//     is built from the readable parts, and fails only when none is readable.
//     Any other probe failure (ffprobe missing, unreadable storage) fails the
//     book so a transient problem cannot drop parts.
//   - Parts follow their disc and track tags when audiobookPartOrder trusts
//     them, a disc folder's number standing in for a missing disc tag;
//     otherwise the disc-folder then filename order they arrive in.
//   - Book tags come from the first part. In a multi-file book whose title
//     tags name the parts ("Chapter 01"), the album tag is the book title.
//   - A part keeps its embedded chapters; a part of a multi-file book without
//     them gets one chapter named after its file, even when it is the only
//     readable part.
func parseAudiobookParts(ctx context.Context, ffprobePath, folderPath string, audioFiles []string) (*parsedAudiobook, error) {
	book := &parsedAudiobook{}
	parts := make([]probedAudiobookPart, 0, len(audioFiles))
	for _, path := range audioFiles {
		info, err := fspath.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat audiobook file %s: %w", path, err)
		}
		size, modTime := info.Size(), normalizeFileModifiedAt(info.ModTime())
		if audiobookPartRejected(path, size, modTime) {
			book.UnreadableParts = append(book.UnreadableParts, path)
			continue
		}
		probed, err := ProbeFile(ctx, ffprobePath, path)
		if err != nil {
			if !IsProbeRejection(ctx, path, err) {
				return nil, fmt.Errorf("probe audiobook file %s: %w", path, err)
			}
			rememberRejectedAudiobookPart(path, size, modTime)
			slog.WarnContext(ctx, "audiobook scan: ffprobe rejected an audio file; leaving it out of the book",
				"component", "scanner", "folder", folderPath, "path", path, "error", err)
			book.UnreadableParts = append(book.UnreadableParts, path)
			continue
		}
		parts = append(parts, probedAudiobookPart{
			tags: probed.FormatTags,
			file: parsedAudiobookFile{
				Path:          path,
				Chapters:      probed.Chapters,
				Duration:      probed.Duration,
				Bitrate:       probed.Bitrate,
				CodecAudio:    probed.CodecAudio,
				Container:     probed.Container,
				AudioChannels: probed.AudioChannels,
				Size:          size,
				ModifiedAt:    modTime,
			},
		})
	}
	if len(parts) == 0 {
		return nil, &noReadableAudiobookPartError{folder: folderPath, paths: book.UnreadableParts}
	}

	tags := make([]map[string]string, len(parts))
	for i, p := range parts {
		tags[i] = p.tags
	}
	folderDiscs := make([]int, len(parts))
	for i, p := range parts {
		if dir := filepath.Dir(p.file.Path); p.file.Path != folderPath && dir != folderPath {
			folderDiscs[i] = audiobookDiscNumber(filepath.Base(dir))
		}
	}
	if order, ok := audiobookPartOrder(tags, folderDiscs); ok {
		ordered := make([]probedAudiobookPart, len(parts))
		for i, idx := range order {
			ordered[i] = parts[idx]
		}
		parts = ordered
	}

	bookTags := parts[0].tags
	if extra := readMP4BookAtomTags(parts[0].file.Path); len(extra) > 0 {
		merged := make(map[string]string, len(bookTags)+len(extra))
		for k, v := range extra {
			merged[k] = v
		}
		for k, v := range bookTags {
			if strings.TrimSpace(v) != "" {
				merged[k] = v
			}
		}
		bookTags = merged
	}
	book.populateFromTags(bookTags)
	if len(audioFiles) > 1 {
		titles := make([]string, len(parts))
		for i, p := range parts {
			titles[i] = p.tags["title"]
		}
		// A part title never names the book. Without an album tag the
		// filesystem fallback below supplies the title.
		if partTitlesNameParts(titles) {
			book.Title = strings.TrimSpace(bookTags["album"])
		}
	}
	book.applyFilesystemFallbacks(folderPath, audioFiles)

	book.Files = make([]parsedAudiobookFile, len(parts))
	for i, p := range parts {
		file := p.file
		if len(audioFiles) > 1 && len(file.Chapters) == 0 {
			stem := strings.TrimSuffix(filepath.Base(file.Path), filepath.Ext(file.Path))
			file.Chapters = []ChapterInfo{{
				Index:        i,
				Title:        fspath.Display(stem),
				StartSeconds: 0,
				EndSeconds:   float64(file.Duration),
			}}
		}
		book.Files[i] = file
	}
	return book, nil
}

// noReadableAudiobookPartError reports a book folder in which ffprobe
// rejected every audio file. paths lists those files so the caller can
// retire rows indexed for them before they became unreadable.
type noReadableAudiobookPartError struct {
	folder string
	paths  []string
}

func (e *noReadableAudiobookPartError) Error() string {
	return fmt.Sprintf("audiobook folder %s: ffprobe rejected every audio file (%s)", e.folder, strings.Join(e.paths, ", "))
}

// populateFromTags fills the audiobook's header fields from the ffprobe
// format tags. Tags are lower-cased by normalizeFormatTags upstream.
func (b *parsedAudiobook) populateFromTags(tags map[string]string) {
	b.Authors = splitAudiobookCredits(firstNonEmpty(tags["album_artist"], tags["artist"], tags["composer"]))
	b.Narrators = audiobookTagNarrators(tags, b.Authors)
	b.Author, b.Narrator = "", ""
	if len(b.Authors) > 0 {
		b.Author = b.Authors[0]
	}
	if len(b.Narrators) > 0 {
		b.Narrator = b.Narrators[0]
	}
	// Note: do NOT fall back to tags["album"] for Series. In audiobook
	// tagging, `album` is the book title, not the series name — using it
	// as the fallback writes series_name = title for every book without
	// an explicit series tag, producing one fake singleton "series" per
	// title (~141k of them on this server before this fix).
	b.Series = firstNonEmpty(tags["series"], tags["mvnm"])
	b.SeriesPosition = firstNonEmpty(tags["series-part"], tags["mvin"], tags["movement"])
	if groupName, groupPosition := parseGroupingSeries(tags["grouping"]); groupName != "" {
		if b.Series == "" {
			b.Series = groupName
		}
		if b.SeriesPosition == "" && strings.EqualFold(b.Series, groupName) {
			b.SeriesPosition = groupPosition
		}
	}
	b.Title = audiobookTagTitle(tags["title"], tags["album"], b.Series, b.SeriesPosition)
	b.ASIN = firstNonEmpty(tags["asin"], tags["audible_asin"], tags["com.audible.asin"])
	// ffprobe reports an M4B's long description (ldes) as "synopsis" and the
	// short one (desc, at most 255 characters) as "description".
	b.Overview = firstNonEmpty(tags["synopsis"], tags["description"], tags["summary"], tags["comment"], tags["©cmt"])
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

func (b *parsedAudiobook) applyFilesystemFallbacks(folderPath string, audioFiles []string) {
	if b == nil || strings.TrimSpace(b.Title) != "" {
		return
	}
	b.Title = deriveAudiobookTitleFromFilesystem(folderPath, audioFiles)
}

func deriveAudiobookTitleFromFilesystem(folderPath string, audioFiles []string) string {
	var candidates []string
	if len(audioFiles) != 1 || audioFiles[0] != folderPath {
		// A loose single-file book is rooted at, and named by, its file alone.
		candidates = append(candidates, filepath.Base(folderPath))
	}
	if len(audioFiles) == 1 {
		file := audioFiles[0]
		candidates = append(candidates, strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)))
	}
	for _, candidate := range candidates {
		if cleaned := cleanAudiobookFilesystemTitle(fspath.Display(candidate)); cleaned != "" {
			return cleaned
		}
	}
	return ""
}

func cleanAudiobookFilesystemTitle(title string) string {
	cleaned := strings.TrimSpace(title)
	cleaned = audiobookFilesystemTitleNumericIDRE.ReplaceAllString(cleaned, "")
	cleaned = strings.NewReplacer(".", " ", "_", " ").Replace(cleaned)
	for {
		next := audiobookFilesystemTitleNoiseSuffixRE.ReplaceAllString(cleaned, "")
		if next == cleaned {
			break
		}
		cleaned = next
	}
	cleaned = collapseSpacesRE.ReplaceAllString(cleaned, " ")
	return strings.Trim(cleaned, " -_.")
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
