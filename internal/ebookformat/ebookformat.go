// Package ebookformat owns the single ebook format whitelist shared by every
// surface that serves or selects book files: the native v1 reader
// (internal/api/handlers), the Audiobookshelf compatibility layer
// (internal/audiobooks/abs), and anything added later.
//
// Keeping one table here is deliberate. The reader's admission check, the MIME
// type on the wire, and the "which file is the book" choice all have to agree:
// a file admitted for reading must map to a concrete ebook MIME type, and a
// file the mark-read path points at must be one the reader can open.
package ebookformat

import (
	"mime"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Normalize maps a filename extension or a catalog container value onto the
// ebook format whitelist, returning "" for anything outside it. The leading
// dot and surrounding whitespace are optional and case is ignored.
func Normalize(value string) string {
	format := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
	switch format {
	case "epub", "pdf", "mobi", "azw", "azw3", "cbz", "cbr", "fb2", "fbz":
		return format
	default:
		return ""
	}
}

// Format resolves the whitelisted format for a file from its filename
// extension, falling back to the catalog container when the extension is not a
// known ebook format. It returns "" when neither identity is whitelisted.
// IsEbookFile admission and MimeTypeForFile both key off this resolution, so an
// admitted file always maps to a concrete ebook MIME type and never falls
// through to application/octet-stream.
func Format(path, container string) string {
	if strings.HasSuffix(strings.ToLower(path), ".fb2.zip") {
		return "fbz"
	}
	if format := Normalize(filepath.Ext(path)); format != "" {
		return format
	}
	return Normalize(container)
}

// FormatForFile resolves the whitelisted format of a media file. It does not
// consider the file's base type: callers that require a file belonging to an
// ebook item must use IsEbookFile.
func FormatForFile(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	return Format(file.FilePath, file.Container)
}

// MimeType returns the MIME type for a whitelisted format, or "" for a format
// outside the whitelist (including "").
func MimeType(format string) string {
	switch format {
	case "epub":
		return "application/epub+zip"
	case "pdf":
		return "application/pdf"
	case "mobi":
		return "application/x-mobipocket-ebook"
	case "azw":
		return "application/vnd.amazon.ebook"
	case "azw3":
		return "application/vnd.amazon.mobi8-ebook"
	case "cbz":
		return "application/vnd.comicbook+zip"
	case "cbr":
		return "application/vnd.comicbook-rar"
	case "fb2":
		return "application/x-fictionbook+xml"
	case "fbz":
		return "application/x-zip-compressed-fb2"
	default:
		return ""
	}
}

// MimeTypeForFile returns the MIME type to serve a file with, falling back to
// application/octet-stream for anything outside the whitelist. Files admitted
// by IsEbookFile never take the fallback.
func MimeTypeForFile(file *models.MediaFile) string {
	if mimeType := MimeType(FormatForFile(file)); mimeType != "" {
		return mimeType
	}
	return "application/octet-stream"
}

// IsEbookFile reports whether a media file is a reader-supported ebook: it
// must belong to an ebook item and resolve to a whitelisted format.
func IsEbookFile(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	if !strings.EqualFold(file.BaseType, "ebook") {
		return false
	}
	return FormatForFile(file) != ""
}

// PreferredReadFile picks the file a reader should open by default, mirroring
// the web reader's choice (web/src/pages/ItemDetail/EbookContent.tsx
// preferredReadVersion): prefer an EPUB version, otherwise the first
// reader-supported ebook file in stable order. Callers must pass
// access-filtered files.
func PreferredReadFile(files []*models.MediaFile) *models.MediaFile {
	return preferred(files, IsEbookFile)
}

// PreferredFile applies the same EPUB-first choice as PreferredReadFile but
// admits any file whose format is whitelisted, regardless of the owning item's
// base type. The Audiobookshelf layer needs this: a supplementary ebook can sit
// alongside audio files in an audiobook item, so its base type is not "ebook".
func PreferredFile(files []*models.MediaFile) *models.MediaFile {
	return preferred(files, func(file *models.MediaFile) bool {
		return FormatForFile(file) != ""
	})
}

// FileByID returns the file with the given media_files.id from files, but only
// when its format is whitelisted — the same format-only admission PreferredFile
// applies. It returns nil when no such file is present or the addressed file is
// not a supported ebook, so an explicitly requested id can never widen what a
// caller is willing to serve.
func FileByID(files []*models.MediaFile, id int) *models.MediaFile {
	for _, file := range files {
		if file != nil && file.ID == id && FormatForFile(file) != "" {
			return file
		}
	}
	return nil
}

func preferred(files []*models.MediaFile, admit func(*models.MediaFile) bool) *models.MediaFile {
	var first *models.MediaFile
	for _, file := range files {
		if !admit(file) {
			continue
		}
		if FormatForFile(file) == "epub" {
			return file
		}
		if first == nil {
			first = file
		}
	}
	return first
}

// ContentDisposition builds a Content-Disposition header value of the given
// kind ("inline" or "attachment"). mime.FormatMediaType handles quoting,
// backslash escaping, and RFC 5987 (filename*) encoding for non-ASCII names;
// the fallback covers the case where it refuses the name outright, so the
// header is never emitted empty.
func ContentDisposition(kind, filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		filename = "ebook"
	}
	if disposition := mime.FormatMediaType(kind, map[string]string{"filename": filename}); disposition != "" {
		return disposition
	}
	return kind + `; filename="ebook"`
}
