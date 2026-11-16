package transcodenode

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// errSourceAuthorityUnavailable is a node that cannot ask the core about a
// source right now.
var errSourceAuthorityUnavailable = errors.New("source authority unavailable")

// mountMismatchDescription documents a job route's 409 in its protocol entry.
const mountMismatchDescription = "This node's mount does not show the approved source revision"

// relayedBurnInRefusal is a start's 409 when its text subtitle burn-in
// cannot be staged and the node reads the source through the core, so libass
// would read the whole file over the network. The caller runs the start
// elsewhere.
const relayedBurnInRefusal = "this node reads its source through the core and cannot burn in this subtitle track"

// sourceGrantRequired is the answer to a job that carries no grant.
const sourceGrantRequired = "source_grant is required"

// openSource opens a job's input with its grant. Every job input goes through
// here, so the node runs FFmpeg, and reads in Go, only what the core granted
// it. The caller closes the source when the job ends, which ends its renewal.
func (s *Server) openSource(ctx context.Context, grant string) (nodesource.Source, error) {
	if s.opener == nil {
		return nil, errSourceAuthorityUnavailable
	}
	source, err := s.opener.Open(ctx, grant)
	if err != nil {
		// Not source: a nil concrete source would be a non-nil interface.
		return nil, err
	}
	return source, nil
}

// renditionSourceOnNode restates planned, the source version a plan read its
// on-demand subtitle tracks from, as this node's mount shows it. The API
// server recorded the version from its own stat, and playback compares it
// with a stat of the input here. When it is the revision the core granted,
// its modification time becomes the one this node's mount gave the file at
// open, so a mount that dates files differently still serves the cues, and a
// file replaced on this node afterwards still does not. A plan that read
// another version becomes unmatchedRenditionSource: left as it was, its time
// could happen to be the one this node's mount gives the granted file.
func renditionSourceOnNode(planned playback.SubtitleRenditionSource, source nodesource.Source) playback.SubtitleRenditionSource {
	if planned.IsZero() {
		return planned
	}
	if planned.Size != source.Size() || !nodesource.SameModTime(time.Unix(0, planned.ModTimeUnixNano), source.ModTime()) {
		return unmatchedRenditionSource
	}
	planned.ModTimeUnixNano = source.InputModTime().UnixNano()
	return planned
}

// toneMapRevisionOnNode restates revision, the catalog's version of a
// tone-mapped job's source, as this node's mount shows it. The revision
// carries the modification time the scanner recorded on the API server, and
// playback rechecks it against a stat of the input here
// (tonemap.SourceRevision.ValidatePath). When it is the revision the core
// granted, its modification time becomes the one this node's mount gave the
// file at open, so a mount that dates files differently still runs the job.
// A revision the core did not grant gets a size no file has, so the recheck
// refuses it as it does on the API server, whose own file is the granted
// one: left as it was, its time could happen to be the one this node's mount
// gives the granted file. A revision without a modification time has none to
// compare and is left as it is.
func toneMapRevisionOnNode(revision tonemap.SourceRevision, source nodesource.Source) tonemap.SourceRevision {
	if revision.FileModifiedUnixNano <= 0 {
		return revision
	}
	if revision.FileSize != source.Size() || !nodesource.SameModTime(time.Unix(0, revision.FileModifiedUnixNano), source.ModTime()) {
		revision.FileSize = -1
		return revision
	}
	revision.FileModifiedUnixNano = models.NormalizeFileModifiedAt(source.InputModTime()).UnixNano()
	return revision
}

// unmatchedRenditionSource is a recorded source version no file has, so its
// on-demand subtitle segments answer empty.
var unmatchedRenditionSource = playback.SubtitleRenditionSource{Size: -1}

// sourceRefused reports whether err is the core's lasting answer that the
// job may not use its source, or a job that carried no grant, as opposed to a
// core the node could not reach or a mount that does not match.
func sourceRefused(err error) bool {
	if errors.Is(err, nodesource.ErrNoGrant) {
		return true
	}
	statusErr, ok := errors.AsType[*nodesource.StatusError](err)
	return ok && statusErr.Fatal()
}

// sourceFailure is how a job route answers a source it could not open: 400
// when the job carries no grant or the core refused it, 409 when this node's
// mount does not show the approved source (the node's fault: the caller
// runs the job elsewhere), 503 when the core could not be asked.
func sourceFailure(ctx context.Context, err error) (status int, message string) {
	switch {
	case errors.Is(err, nodesource.ErrNoGrant):
		return http.StatusBadRequest, sourceGrantRequired
	case errors.Is(err, nodesource.ErrMountMismatch):
		// The opener logged what each side saw.
		return http.StatusConflict, err.Error()
	case sourceRefused(err):
		slog.WarnContext(ctx, "core refused a job's source", "component", "transcodenode", "error", err)
		return http.StatusBadRequest, "source_grant must name an approved media file"
	default:
		slog.WarnContext(ctx, "open a job's source", "component", "transcodenode", "error", err)
		return http.StatusServiceUnavailable, errSourceAuthorityUnavailable.Error()
	}
}

// openJobSource is openSource for a plain-text job route: when the source
// cannot be opened it answers the request itself.
func (s *Server) openJobSource(w http.ResponseWriter, r *http.Request, grant string) (nodesource.Source, bool) {
	source, err := s.openSource(r.Context(), grant)
	if err != nil {
		status, message := sourceFailure(r.Context(), err)
		http.Error(w, message, status)
		return nil, false
	}
	return source, true
}

// sourceContext derives a job's context from ctx that also ends when the
// job's source does: closed, or withdrawn by the core at a renewal.
func sourceContext(ctx context.Context, source nodesource.Source) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(source.Context(), func() { cancel(source.Err()) })
	return ctx, func() {
		stop()
		cancel(context.Canceled)
	}
}

// registerSessionLocked publishes session under transportID with the
// playback identity it serves and the source it reads, which it then owns.
// The caller holds mu.
func (s *Server) registerSessionLocked(transportID string, session *playback.TranscodeSession, identity sessionIdentity, source nodesource.Source) {
	s.sessions[transportID] = session
	if s.identities == nil {
		s.identities = make(map[string]sessionIdentity)
	}
	s.identities[transportID] = identity
	if source != nil {
		if s.sessionSources == nil {
			s.sessionSources = make(map[string]nodesource.Source)
		}
		s.sessionSources[transportID] = source
		s.watchSessionSource(transportID, session, source)
	}
	s.noteSessionAccessLocked(transportID)
}

// forgetSessionLocked drops transportID's session, its liveness and its
// identity, and closes its source. The caller holds mu.
func (s *Server) forgetSessionLocked(transportID string) {
	delete(s.sessions, transportID)
	delete(s.lastAccess, transportID)
	delete(s.identities, transportID)
	if source := s.sessionSources[transportID]; source != nil {
		_ = source.Close()
		delete(s.sessionSources, transportID)
	}
}

// watchSessionSource ends session sessionID when the core withdraws its
// source while it runs. A source closed by the session's own teardown ends
// nothing.
func (s *Server) watchSessionSource(sessionID string, session *playback.TranscodeSession, source nodesource.Source) {
	context.AfterFunc(source.Context(), func() {
		if errors.Is(source.Err(), nodesource.ErrClosed) {
			return
		}
		unlock := s.lockSessionLifecycle(sessionID)
		defer unlock()
		s.mu.Lock()
		if live, ok := s.sessions[sessionID]; !ok || live != session {
			s.mu.Unlock()
			return
		}
		s.forgetSessionLocked(sessionID)
		s.mu.Unlock()
		slog.Warn("transcode session's source was withdrawn; ending it", "component", "transcodenode",
			"session", sessionID, "source", source.Identity(), "error", source.Err())
		if err := s.closeSessionOffGPU(session); err != nil {
			slog.Error("close transcode session", "component", "transcodenode", "error", err, "session", sessionID)
		}
		if err := os.RemoveAll(s.sessionOutputDir(sessionID)); err != nil {
			slog.Warn("remove transcode session directory", "component", "transcodenode", "session", sessionID, "error", err)
		}
		if s.tracker != nil {
			s.tracker.Remove(context.Background(), sessionID)
		}
	})
}
