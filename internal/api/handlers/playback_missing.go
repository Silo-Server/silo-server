package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// MissingFileMarker marks media files as missing when playback discovers they
// no longer exist on disk.
type MissingFileMarker interface {
	MarkMissing(ctx context.Context, id int, since time.Time) error
}

type playbackFilePreflightError struct {
	notFound bool
	err      error
}

func (e *playbackFilePreflightError) Error() string {
	if e == nil {
		return ""
	}
	if e.notFound {
		return "source media file is missing"
	}
	if e.err != nil {
		return fmt.Sprintf("failed to access media file: %v", e.err)
	}
	return "failed to access media file"
}

func (e *playbackFilePreflightError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func isPlaybackFileMissing(err error) bool {
	var target *playbackFilePreflightError
	return errors.As(err, &target) && target.notFound
}

func isPlaybackFileLookupMissing(err error) bool {
	return errors.Is(err, scanner.ErrFileNotFound) || errors.Is(err, playback.ErrFileNotFound)
}

func writePlaybackFilePreflightError(w http.ResponseWriter, err error) {
	if isPlaybackFileMissing(err) {
		writeError(w, http.StatusNotFound, "not_found", "Source media file is missing")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access source media file")
}

const playbackSessionNotFoundErrorCode = "playback_session_not_found"

func writePlaybackSessionNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, playbackSessionNotFoundErrorCode, "Playback session not found")
}

func preflightPlaybackFile(
	ctx context.Context,
	file *models.MediaFile,
	marker MissingFileMarker,
	eventsHub *evt.Hub,
) error {
	if file == nil {
		return &playbackFilePreflightError{notFound: true}
	}
	if file.MissingSince != nil {
		return &playbackFilePreflightError{notFound: true}
	}

	if _, err := os.Stat(file.FilePath); err != nil {
		if os.IsNotExist(err) {
			markPlaybackFileMissing(ctx, file, marker, eventsHub)
			return &playbackFilePreflightError{notFound: true, err: err}
		}
		return &playbackFilePreflightError{err: err}
	}

	return nil
}

func markPlaybackFileMissing(
	ctx context.Context,
	file *models.MediaFile,
	marker MissingFileMarker,
	eventsHub *evt.Hub,
) {
	if file == nil {
		return
	}

	since := time.Now().UTC()
	file.MissingSince = &since

	if marker != nil {
		if err := marker.MarkMissing(ctx, file.ID, since); err != nil {
			slog.WarnContext(ctx, "failed to mark playback file missing", "component", "api",
				"file_id", file.ID,
				"path", file.FilePath,
				"error", err,
			)
		}
	}

	contentID := playbackProgressTarget(file)
	if contentID != "" && eventsHub != nil {
		publishEventMetadataUpdate(ctx, eventsHub, file.MediaFolderID, contentID)
	}
}
