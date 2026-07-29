package playback

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
// Range requests, conditional requests (If-Modified-Since, If-None-Match),
// and Content-Type detection.
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
	if etag := directPlayEntityTag(f, stat); etag != "" {
		w.Header().Set("ETag", etag)
	}

	// Set Content-Type explicitly so ServeContent does not sniff.
	w.Header().Set("Content-Type", MimeFromExtension(filePath))

	hadRange := len(r.Header.Values("Range")) > 0
	hadIfRange := len(r.Header.Values("If-Range")) > 0
	directStreamActive.Inc()
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
	outcome := streamWriter.Outcome(r.Context())
	status := streamWriter.StatusCode()
	bytesSent := streamWriter.BytesWritten()
	rangeStart := directStreamRangeStart(status, w.Header().Get("Content-Range"))
	recordDirectStreamEnd(outcome, status, bytesSent, rangeStart)
	slog.InfoContext(r.Context(), "direct stream ended",
		"component", "playback",
		"outcome", outcome,
		"status", status,
		"bytes_sent", bytesSent,
		"range_requested", hadRange,
		"range_start", rangeStart,
		"had_if_range", hadIfRange,
	)
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
