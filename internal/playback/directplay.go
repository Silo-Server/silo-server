package playback

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

// MimeFromExtension returns a MIME type based on the file extension.
// Falls back to "application/octet-stream" for unknown extensions.
func MimeFromExtension(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	case ".ts":
		return "video/mp2t"
	case ".flv":
		return "video/x-flv"
	case ".wmv":
		return "video/x-ms-wmv"
	case ".m4b", ".m4a":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".opus", ".ogg":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".aac":
		return "audio/aac"
	default:
		return "application/octet-stream"
	}
}

// ServeDirectPlay serves a media file with HTTP byte-range support.
// Uses http.ServeContent for proper range handling, which supports
// Range requests, conditional requests (including If-Match, If-Range, and
// If-None-Match), and Content-Type detection.
func ServeDirectPlay(w http.ResponseWriter, r *http.Request, filePath string) error {
	// Media bodies routinely take longer than the server's absolute
	// WriteTimeout; roll the write deadline with progress instead.
	streamWriter := httpstream.NewRollingDeadlineWriter(w)
	w = streamWriter
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
	w = &directPlayResponseWriter{
		RollingDeadlineWriter: streamWriter,
		size:                  stat.Size(),
	}

	w.Header().Del("ETag")
	etag := directPlayEntityTag(f, stat)
	if etag != "" {
		w.Header().Set("ETag", etag)
	}

	// Set Content-Type explicitly so ServeContent does not sniff.
	w.Header().Set("Content-Type", MimeFromExtension(filePath))

	hadRange := len(r.Header.Values("Range")) > 0
	hadIfMatch := len(r.Header.Values("If-Match")) > 0
	hadIfRange := len(r.Header.Values("If-Range")) > 0
	ifRangeResult := directStreamIfRangeResult(r, etag, stat.ModTime())
	directStreamActive.Inc()
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
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
