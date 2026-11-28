package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// MissingFileMarker marks media files as missing when playback discovers they
// no longer exist on disk.
type MissingFileMarker interface {
	MarkMissing(ctx context.Context, id int, filePath string, since time.Time) error
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

// playbackSessionEndedErrorCode is the answer for a session the player must
// not resume: an administrator or the paused timeout ended it, or (on media)
// no rebuild can serve the playlist it holds or the transport it names is
// denied. A session's stream deny marker stops every token from serving or
// reconstructing it, however it ended; see writeDeniedPlaybackSession for the
// answer a request for a denied session gets.
const playbackSessionEndedErrorCode = "playback_session_ended"

var errPlaybackSessionEnded = errors.New("playback session has ended")

func writePlaybackSessionEnded(w http.ResponseWriter) {
	writeError(w, http.StatusGone, playbackSessionEndedErrorCode, "Playback session has ended")
}

// writePlaybackSessionEndedError answers a serve error that ends the session
// for this request: the deny marker (errPlaybackSessionEnded), answered as
// writeDeniedPlaybackSession does, or a keyframe playlist that can't be
// rebuilt, which is over too because no rebuild can serve the playlist its
// player holds.
func writePlaybackSessionEndedError(w http.ResponseWriter, r *http.Request, store playback.PlanStoreV3, sessionID string, err error) bool {
	switch {
	case errors.Is(err, errPlaybackSessionEnded):
		writeDeniedPlaybackSession(w, r, store, sessionID)
	case errors.Is(err, playback.ErrKeyframePlanChanged):
		writePlaybackSessionEnded(w)
	default:
		return false
	}
	return true
}

// writeDeniedPlaybackSession answers a media request for a session whose
// stream deny marker is set. The marker withholds the bytes for every way a
// session ends; the status tells the owner which end it was, the way progress
// and replan answer (docs/playback-api.md): 410 playback_session_ended when an
// administrator or the paused timeout ended the attempt, so the player
// leaves, and 404 for any other end (an idle expiry, the client's own stop,
// an abort), so a player resuming after a long pause starts over at its
// position instead of giving up (issue #5500).
func writeDeniedPlaybackSession(w http.ResponseWriter, r *http.Request, store playback.PlanStoreV3, sessionID string) {
	writeAPIError(w, deniedPlaybackSessionError(r.Context(), store, sessionID))
}

// deniedPlaybackSessionError is writeDeniedPlaybackSession's answer as an
// error. Only the attempt's account (and profile, when the request selects
// one, as requireOwningProfile admits) learns that the end was final; anyone
// else, an unknown attempt and an unreadable store get the 404.
func deniedPlaybackSessionError(ctx context.Context, store playback.PlanStoreV3, sessionID string) *APIError {
	if store != nil {
		record, err := store.GetAttempt(ctx, sessionID)
		profileID := apimw.GetProfileID(ctx)
		if err == nil && record != nil && record.UserID == apimw.GetUserID(ctx) &&
			(profileID == "" || record.ProfileID == profileID) && endedAttemptError(record) != nil {
			return apiError(http.StatusGone, playbackSessionEndedErrorCode, "Playback session has ended")
		}
	}
	return apiError(http.StatusNotFound, playbackSessionNotFoundErrorCode, "Playback session not found")
}

var errPlaybackTransportSuperseded = errors.New("playback transport superseded")

func writePlaybackTransportSupersededError(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, errPlaybackTransportSuperseded) {
		return false
	}
	w.Header().Set(playback.ErrorHeader, playback.TransportSupersededCode)
	writeError(w, http.StatusNotFound, playback.TransportSupersededCode, playback.TransportSupersededMessage)
	return true
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

	if _, err := fspath.StatContext(ctx, file.FilePath); err != nil {
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
		if err := marker.MarkMissing(ctx, file.ID, file.FilePath, since); err != nil {
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

// requireOwningProfile refuses a media request that selects a profile other
// than the one the session was started for. Account ownership is checked by
// the session loaders; this adds the household-profile half so a restricted
// profile cannot read another profile's session by id. A request that selects
// no profile (media elements that cannot set headers) is admitted on account
// ownership alone, as on the v1 bridge.
func requireOwningProfile(w http.ResponseWriter, r *http.Request, session *playback.Session) bool {
	if session == nil {
		return true
	}
	profileID := apimw.GetProfileID(r.Context())
	if profileID == "" || session.ProfileID == "" || profileID == session.ProfileID {
		return true
	}
	writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another profile")
	return false
}
