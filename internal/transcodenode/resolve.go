package transcodenode

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// streamTokenHeader carries the stream token a relay forwards with a media
// request. The node passes it to the core unread.
const streamTokenHeader = "X-Silo-Stream-Token" //nolint:gosec // a header name, not a credential

// errNoCore is a node wired without the core's playback authority.
var errNoCore = errors.New("transcode node has no core client")

// sessionIdentity is the playback session a transcode serves, as the start
// request or the core's resolution named it.
type sessionIdentity struct {
	playbackSessionID string
	userID            int
	profileID         string
	mediaFileID       int
}

func identityOfStart(req TranscodeStartRequest) sessionIdentity {
	return sessionIdentity{playbackSessionID: req.PlaybackSessionID, userID: req.UserID, profileID: req.ProfileID, mediaFileID: req.MediaFileID}
}

func identityOfResolution(resolution *nodecontrol.Resolution) sessionIdentity {
	return sessionIdentity{
		playbackSessionID: resolution.PlaybackSessionID,
		userID:            resolution.UserID,
		profileID:         resolution.ProfileID,
		mediaFileID:       resolution.MediaFileID,
	}
}

// resolvedStart is the start a session record files a resolved session under:
// the playback session's own start when the core resolved one, so a rebuild or
// a remux request does not report itself as the session's beginning, else now.
func resolvedStart(resolution *nodecontrol.Resolution) (time.Time, string) {
	startedAt, source := resolution.StartedAt.UTC(), resolution.StartedAtSource
	if resolution.StartedAt.IsZero() || source == "" || source == string(streamtoken.StartedAtSourceNone) {
		return time.Now().UTC(), string(streamtoken.StartedAtSourceNone)
	}
	return startedAt, source
}

// resolve asks the core about transportID, passing the relay's stream token
// through unread.
func (s *Server) resolve(r *http.Request, transportID, purpose string) (*nodecontrol.Resolution, error) {
	if s.core == nil {
		return nil, errNoCore
	}
	return s.core.Resolve(r.Context(), nodecontrol.ResolveRequest{
		TransportID: transportID,
		Purpose:     purpose,
		StreamToken: r.Header.Get(streamTokenHeader),
	})
}

// writeRemuxRefusal answers a progressive remux the core refused to resolve
// as the node answered the same condition when it checked the token itself,
// so relays and clients see no change.
func writeRemuxRefusal(w http.ResponseWriter, r *http.Request, transportID string, err error) {
	switch nodecontrol.Reason(err) {
	case nodecontrol.ResolveInvalidToken:
		http.Error(w, "invalid stream token", http.StatusUnauthorized)
	case nodecontrol.ResolveWrongRoute:
		http.Error(w, "routing policy unsatisfied", http.StatusServiceUnavailable)
	case nodecontrol.ResolveDenied:
		writeStreamDenied(w)
	case nodecontrol.ResolveStopped:
		http.Error(w, "session stopped", http.StatusGone)
	case nodecontrol.ResolveSourceChanged:
		// The file or theme the token names changed or left the catalog.
		http.Error(w, "remux input is no longer the approved source", http.StatusBadRequest)
	case nodecontrol.ResolveNotFound:
		http.NotFound(w, r)
	default:
		slog.WarnContext(r.Context(), "progressive remux recipe unavailable", "component", "transcodenode", "error", err,
			"session", transportID)
		http.Error(w, "recipe authority unavailable", http.StatusServiceUnavailable)
	}
}

// Rebuild failures a lost HLS session's request answers: a session the core
// denied, a transport this process stopped, or a core it could not ask.
var (
	errReconstructDenied      = errors.New("transcode session denied")
	errReconstructUnavailable = errors.New("transcode recipe authority unavailable")
)

// writeReconstructFailure answers a request for a lost session that could not
// be rebuilt.
func writeReconstructFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errReconstructDenied), errors.Is(err, errTranscodeStopped):
		writeStreamDenied(w)
	case errors.Is(err, errReconstructUnavailable):
		http.Error(w, "recipe authority unavailable", http.StatusServiceUnavailable)
	case err != nil && isToneMapRecipeError(err):
		writeToneMapRecipeError(w, err)
	default:
		http.Error(w, "session not found", http.StatusNotFound)
	}
}
