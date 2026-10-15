package playback

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

// unknownMediaMIME is the MIME type of a file with an unknown extension.
const unknownMediaMIME = "application/octet-stream"

const (
	mimeVideoMatroska = "video/x-matroska"
	mimeVideoWebM     = "video/webm"
	mimeVideoMPEGTS   = "video/mp2t"
	mimeVideoMPEGPS   = "video/mpeg"
	mimeVideoOgg      = "video/ogg"
)

// mediaMIMETypes maps the media file extensions the scanner catalogs to the
// MIME type their original bytes are served with.
var mediaMIMETypes = mimeTypesByExtension(map[string]string{
	mimeVideoMP4V3:       ".mp4 .m4v .f4v",
	mimeVideoMatroska:    ".mkv",
	mimeVideoWebM:        ".webm",
	"video/x-msvideo":    ".avi .divx",
	"video/quicktime":    ".mov",
	"video/3gpp":         ".3gp",
	"video/3gpp2":        ".3g2",
	mimeVideoMPEGTS:      ".ts .m2ts .mts",
	mimeVideoMPEGPS:      ".mpg .mpeg",
	"video/x-flv":        ".flv",
	"video/x-ms-wmv":     ".wmv",
	"video/x-ms-asf":     ".asf",
	mimeVideoOgg:         ".ogv .ogm",
	AudioOnlyRemuxMIMEV3: ".m4b .m4a",
	"audio/mpeg":         ".mp3",
	"audio/flac":         ".flac",
	"audio/ogg":          ".opus .ogg",
	"audio/wav":          ".wav",
	"audio/aac":          ".aac",
})

// mimeTypesByExtension inverts a table of MIME type to space-separated
// extensions.
func mimeTypesByExtension(extensionsByMIME map[string]string) map[string]string {
	byExtension := make(map[string]string)
	for mime, extensions := range extensionsByMIME {
		for _, ext := range strings.Fields(extensions) {
			byExtension[ext] = mime
		}
	}
	return byExtension
}

// MimeFromExtension returns a MIME type based on the file extension.
// Falls back to "application/octet-stream" for unknown extensions.
func MimeFromExtension(name string) string {
	if mime, ok := mediaMIMETypes[strings.ToLower(filepath.Ext(name))]; ok {
		return mime
	}
	return unknownMediaMIME
}

// MediaMIMETypes returns, sorted and without duplicates, every MIME type
// MimeFromExtension returns for a known extension. Contracts that declare the
// content types of original media bytes list these.
func MediaMIMETypes() []string {
	types := slices.Collect(maps.Values(mediaMIMETypes))
	slices.Sort(types)
	return slices.Compact(types)
}

// ServeDirectPlay serves a media file with HTTP byte-range support.
// Uses http.ServeContent for proper range handling, which supports
// Range requests, conditional requests (including If-Match, If-Range, and
// If-None-Match), and Content-Type detection.
func ServeDirectPlay(w http.ResponseWriter, r *http.Request, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
			return err
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return err
	}
	etag := DirectPlayEntityTag(f, stat)

	hadRange := len(r.Header.Values("Range")) > 0
	hadIfMatch := len(r.Header.Values("If-Match")) > 0
	hadIfRange := len(r.Header.Values("If-Range")) > 0
	ifRangeResult := directStreamIfRangeResult(r, etag, stat.ModTime())
	directStreamActive.Inc()
	streamWriter := serveOpenFile(w, r, f, stat, etag)
	outcome := streamWriter.Outcome(r.Context())
	status := streamWriter.StatusCode()
	bytesSent := streamWriter.BytesWritten()
	rangeStart := directStreamRangeStart(status, w.Header().Get("Content-Range"))
	recordDirectStreamEnd(outcome, status, bytesSent, rangeStart)
	logAttrs := []any{
		"component", "playback",
		"outcome", outcome,
		"status", status,
		"bytes_sent", bytesSent,
		"range_requested", hadRange,
		"range_start", rangeStart,
		"had_if_match", hadIfMatch,
		"had_if_range", hadIfRange,
		"conditional_result", directStreamConditionalResult(status, hadIfMatch, hadIfRange, ifRangeResult),
	}
	if fingerprint := directStreamValidatorFingerprint(etag); fingerprint != "" {
		logAttrs = append(logAttrs, "etag_fingerprint", fingerprint)
	}
	if fingerprint := directStreamHeaderFingerprint(r.Header, "If-Match"); fingerprint != "" {
		logAttrs = append(logAttrs, "if_match_fingerprint", fingerprint)
	}
	if fingerprint := directStreamHeaderFingerprint(r.Header, "If-Range"); fingerprint != "" {
		logAttrs = append(logAttrs, "if_range_fingerprint", fingerprint)
	}
	slog.InfoContext(r.Context(), "direct stream ended", logAttrs...)
	return nil
}

// ServeOpenFile serves the already open file f, described by info, with
// HTTP byte-range support, under etag (none when empty). It never reopens a
// path, so what it serves is exactly the file the caller checked. Ranges
// (single and suffix) and conditional requests (If-Match, If-Range,
// If-None-Match) are http.ServeContent's; a 416 names the file's size. The
// write deadline rolls with progress, since media bodies routinely outlast
// the server's absolute WriteTimeout.
func ServeOpenFile(w http.ResponseWriter, r *http.Request, f *os.File, info os.FileInfo, etag string) {
	serveOpenFile(w, r, f, info, etag)
}

func serveOpenFile(w http.ResponseWriter, r *http.Request, f *os.File, info os.FileInfo, etag string) *httpstream.RollingDeadlineWriter {
	streamWriter := httpstream.NewRollingDeadlineWriter(w)
	w = &directPlayResponseWriter{
		RollingDeadlineWriter: streamWriter,
		size:                  info.Size(),
	}
	w.Header().Del("ETag")
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	// Set Content-Type explicitly so ServeContent does not sniff.
	w.Header().Set("Content-Type", MimeFromExtension(info.Name()))
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	return streamWriter
}

type directPlayResponseWriter struct {
	*httpstream.RollingDeadlineWriter
	size int64
}

func (w *directPlayResponseWriter) WriteHeader(status int) {
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", w.size))
	}
	w.RollingDeadlineWriter.WriteHeader(status)
}

func directStreamRangeStart(status int, contentRange string) int64 {
	if status != http.StatusPartialContent {
		return -1
	}

	value, ok := strings.CutPrefix(strings.TrimSpace(contentRange), "bytes ")
	if !ok {
		return -1
	}
	bounds, _, ok := strings.Cut(value, "/")
	if !ok {
		return -1
	}
	start, _, ok := strings.Cut(strings.TrimSpace(bounds), "-")
	if !ok || start == "" {
		return -1
	}
	parsedStart, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	if err != nil || parsedStart < 0 {
		return -1
	}
	return parsedStart
}

const (
	directStreamConditionalNone                = "none"
	directStreamConditionalIfMatchPassed       = "if_match_passed"
	directStreamConditionalIfMatchFailed       = "if_match_failed"
	directStreamConditionalIfRangeMatched      = "if_range_matched"
	directStreamConditionalIfRangeMismatched   = "if_range_mismatched"
	directStreamConditionalIfRangeNotEvaluated = "if_range_not_evaluated"
)

func directStreamConditionalResult(status int, hadIfMatch, hadIfRange bool, ifRangeResult string) string {
	switch {
	case hadIfMatch && status == http.StatusPreconditionFailed:
		return directStreamConditionalIfMatchFailed
	case ifRangeResult != "" && status != http.StatusNotModified && status != http.StatusPreconditionFailed:
		return ifRangeResult
	case hadIfMatch:
		return directStreamConditionalIfMatchPassed
	case hadIfRange:
		return directStreamConditionalIfRangeNotEvaluated
	default:
		return directStreamConditionalNone
	}
}

// directStreamIfRangeResult mirrors the If-Range decision made by
// http.ServeContent before range parsing. The final status cannot encode that
// decision reliably: ServeContent may reject a matched range with 416 or
// deliberately ignore matched aggregate ranges and return 200.
func directStreamIfRangeResult(r *http.Request, etag string, modtime time.Time) string {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return ""
	}
	if r.Header.Get("Range") == "" {
		return ""
	}
	validator := r.Header.Get("If-Range")
	if validator == "" {
		return ""
	}
	if validatorETag := directStreamScanEntityTag(validator); validatorETag != "" {
		if validatorETag == etag && validatorETag[0] == '"' {
			return directStreamConditionalIfRangeMatched
		}
		return directStreamConditionalIfRangeMismatched
	}
	if modtime.IsZero() {
		return directStreamConditionalIfRangeMismatched
	}
	validatorTime, err := http.ParseTime(validator)
	if err == nil && validatorTime.Unix() == modtime.Unix() {
		return directStreamConditionalIfRangeMatched
	}
	return directStreamConditionalIfRangeMismatched
}

// directStreamScanEntityTag is the narrow ETag scanner needed to mirror
// net/http's unexported scanETag behavior for If-Range diagnostics.
func directStreamScanEntityTag(value string) string {
	value = textproto.TrimString(value)
	start := 0
	if strings.HasPrefix(value, "W/") {
		start = 2
	}
	if len(value[start:]) < 2 || value[start] != '"' {
		return ""
	}
	for i := start + 1; i < len(value); i++ {
		character := value[i]
		switch {
		case character == 0x21 || character >= 0x23 && character <= 0x7e || character >= 0x80:
		case character == '"':
			return value[:i+1]
		default:
			return ""
		}
	}
	return ""
}

func directStreamHeaderFingerprint(header http.Header, name string) string {
	return directStreamValidatorFingerprint(strings.Join(header.Values(name), "\x00"))
}

func directStreamValidatorFingerprint(validator string) string {
	if strings.TrimSpace(validator) == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(validator))
	return fmt.Sprintf("%x", digest[:8])
}
