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

	if _, exists := w.Header()[http.CanonicalHeaderKey("ETag")]; !exists {
		w.Header().Set("ETag", fmt.Sprintf("\"%x-%x\"", stat.ModTime().UnixNano(), stat.Size()))
	}

	// Set Content-Type explicitly so ServeContent does not sniff.
	w.Header().Set("Content-Type", MimeFromExtension(filePath))

	rangeStart := directStreamRangeStart(r.Header.Get("Range"))
	hadIfRange := len(r.Header.Values("If-Range")) > 0
	directStreamActive.Inc()
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
	outcome := streamWriter.Outcome(r.Context())
	status := streamWriter.StatusCode()
	bytesSent := streamWriter.BytesWritten()
	recordDirectStreamEnd(outcome, status, bytesSent, rangeStart)
	slog.InfoContext(r.Context(), "direct stream ended",
		"component", "playback",
		"outcome", outcome,
		"status", status,
		"bytes_sent", bytesSent,
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

func directStreamRangeStart(rangeHeader string) int64 {
	const prefix = "bytes="
	if !strings.HasPrefix(rangeHeader, prefix) {
		return -1
	}
	firstRange, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(rangeHeader, prefix)), ",")
	start, _, ok := strings.Cut(strings.TrimSpace(firstRange), "-")
	if !ok || start == "" {
		return -1
	}
	value, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	if err != nil || value < 0 {
		return -1
	}
	return value
}
