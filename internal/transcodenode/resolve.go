package transcodenode

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// streamTokenHeader carries the stream token a relay forwards with a media
// request. The node passes it to the core unread.
const streamTokenHeader = "X-Silo-Stream-Token"

// errNoRecipeResolver is a node wired without the core's recipe resolution.
var errNoRecipeResolver = errors.New("transcode node has no recipe resolver")

// RecipeResolver asks the core for the recipe and playback identity of a
// transport this node serves (POST /api/internal/nodes/self/resolve). The
// node verifies no stream token and checks no deny marker itself: the core
// does both. A refusal is a *nodecontrol.StatusError carrying one of the
// nodecontrol.Resolve* reasons. *nodecontrol.Client implements it.
type RecipeResolver interface {
	Resolve(ctx context.Context, req nodecontrol.ResolveRequest) (*nodecontrol.Resolution, error)
}

// SetRecipeResolver wires the core's recipe resolution. Without one the node
// rebuilds no lost transcode and serves no progressive remux.
func (s *Server) SetRecipeResolver(resolver RecipeResolver) {
	s.resolver = resolver
}

// sessionIdentity is the playback session a transcode serves, as the start
// request or the core's resolution named it.
type sessionIdentity struct {
	playbackSessionID string
	userID            int
	profileID         string
	mediaFileID       int
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
	if startedAt.IsZero() || source == "" || source == string(streamtoken.StartedAtSourceNone) {
		return time.Now().UTC(), string(streamtoken.StartedAtSourceNone)
	}
	return startedAt, source
}

// registerSessionLocked publishes session under transportID with the identity
// it serves and the source it reads, which it then owns. The caller holds mu.
func (s *Server) registerSessionLocked(transportID string, session *playback.TranscodeSession, identity sessionIdentity, input SourceInput) {
	s.sessions[transportID] = session
	if s.identities == nil {
		s.identities = make(map[string]sessionIdentity)
	}
	s.identities[transportID] = identity
	if input != nil {
		if s.inputs == nil {
			s.inputs = make(map[string]SourceInput)
		}
		s.inputs[transportID] = input
		s.watchSessionInput(transportID, session, input)
	}
	s.noteSessionAccessLocked(transportID)
}

// forgetSessionLocked drops transportID's session, its liveness and its
// identity, and releases its source. The caller holds mu.
func (s *Server) forgetSessionLocked(transportID string) {
	delete(s.sessions, transportID)
	delete(s.lastAccess, transportID)
	delete(s.identities, transportID)
	if input := s.inputs[transportID]; input != nil {
		_ = input.Close()
		delete(s.inputs, transportID)
	}
}

// playbackSessionID is the playback session the transcode under transportID
// serves. A session started without one is filed under its transport id,
// which is the playback session's own id for a jellycompat transcode.
func (s *Server) playbackSessionID(transportID string) string {
	s.mu.RLock()
	identity := s.identities[transportID]
	s.mu.RUnlock()
	if identity.playbackSessionID != "" {
		return identity.playbackSessionID
	}
	return transportID
}

// resolve asks the core about transportID. It reports a missing resolver as
// an error the callers answer as the core being unavailable.
func (s *Server) resolve(r *http.Request, transportID, purpose string) (*nodecontrol.Resolution, error) {
	if s.resolver == nil {
		return nil, errNoRecipeResolver
	}
	return s.resolver.Resolve(r.Context(), nodecontrol.ResolveRequest{
		TransportID: transportID,
		Purpose:     purpose,
		StreamToken: r.Header.Get(streamTokenHeader),
	})
}

// writeRemuxRefusal answers a progressive remux the core refused to resolve
// exactly as the node answered the same condition when it checked the token
// itself, so relays and clients see no change.
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
			"session", transportID, "playback_session_id", transportID)
		http.Error(w, "recipe authority unavailable", http.StatusServiceUnavailable)
	}
}
