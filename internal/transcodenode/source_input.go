package transcodenode

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// SourceInput is one job's opened source. *nodesource.Handle implements it.
type SourceInput interface {
	// Input is what FFmpeg opens: a path, or a loopback URL with core
	// source access.
	Input() string
	// Identity names the source revision in logs and what a session
	// records about it; the input may be a capability that must not be
	// logged.
	Identity() string
	// Context ends when the input is closed or the core withdraws it; Err
	// then says which.
	Context() context.Context
	Err() error
	Close() error
}

// SourceOpener opens a job's source through the core with the grant the job
// carries, and keeps the grant renewed until the input is closed: the
// approved catalog path with local source access, a loopback relay URL with
// core access (internal/nodesource).
type SourceOpener interface {
	OpenSource(ctx context.Context, grant string) (SourceInput, error)
}

// RelaySources is the SourceOpener of a node's source relay.
func RelaySources(relay *nodesource.Relay) SourceOpener { return relaySources{relay: relay} }

type relaySources struct{ relay *nodesource.Relay }

func (r relaySources) OpenSource(ctx context.Context, grant string) (SourceInput, error) {
	input, err := r.relay.Open(ctx, grant)
	if err != nil {
		// Not input: a nil *Handle would be a non-nil SourceInput.
		return nil, err
	}
	return input, nil
}

// SetSourceOpener wires the core's source access. Without it the node refuses
// every media input: it never trusts a bare input path.
func (s *Server) SetSourceOpener(sources SourceOpener) {
	s.sources = sources
}

// errRemoteTextBurnIn refuses a text subtitle burn-in from a core-access
// source (playback.RelayedTextBurnIn): the libass subtitles filter opens its
// file itself and must not be given the relay URL. It names the limitation,
// so the caller moves the work to another node or the API server rather than
// reading the failure as a broken file. Bitmap burn-in reads the decoded
// stream and is not refused.
const errRemoteTextBurnIn = "text subtitle burn-in needs local source access on this node"

// errSourceAuthorityUnavailable is a node that cannot ask the core about a
// source right now.
var errSourceAuthorityUnavailable = errors.New("source authority unavailable")

// resolveInput opens a job's source with its grant. Every job input goes
// through here, so a node runs FFmpeg only on what the core granted it. The
// caller closes the handle when the job ends, which ends its renewal.
func (s *Server) resolveInput(ctx context.Context, grant string) (SourceInput, error) {
	if strings.TrimSpace(grant) == "" {
		return nil, nodesource.ErrNoGrant
	}
	if s.sources == nil {
		return nil, errSourceAuthorityUnavailable
	}
	return s.sources.OpenSource(ctx, grant)
}

// sourceRefused reports whether err is the core's lasting answer that the
// job may not use its source (or carried no grant), as opposed to a core the
// node could not reach.
func sourceRefused(err error) bool {
	var statusErr *nodesource.StatusError
	return errors.Is(err, nodesource.ErrNoGrant) || errors.Is(err, nodesource.ErrLocalMismatch) ||
		errors.As(err, &statusErr) && statusErr.Fatal()
}

// errLocalSourceMismatch answers a job whose approved source this node's own
// mount shows as another revision (local source access): the node's mount,
// not the job or the source, needs fixing, so the caller should move the
// work to another node.
const errLocalSourceMismatch = "local source access: this node's mount does not show the approved source " +
	"at the revision the core approved (missing, or another size or modification second); check the node's library mount"

// localSourceMismatchDescription documents openInput's 409 in each job
// route's protocol entry.
const localSourceMismatchDescription = "This node's library mount does not show the approved source at the revision the core approved (local source access). " +
	"A fault of this node, not of the source: the caller runs the job on another node or falls back."

// openInput is resolveInput for a request handler: when the source cannot be
// opened it answers the request itself, 400 when the job carries no grant or
// the core refused it, 409 when this node's mount does not match the core's
// (local source access), 503 when the core cannot be reached.
func (s *Server) openInput(w http.ResponseWriter, r *http.Request, grant string) (SourceInput, bool) {
	input, err := s.resolveInput(r.Context(), grant)
	switch {
	case err == nil:
		return input, true
	case errors.Is(err, nodesource.ErrNoGrant):
		http.Error(w, "source_grant is required", http.StatusBadRequest)
	case errors.Is(err, nodesource.ErrLocalMismatch):
		// The relay logged what each side saw.
		slog.WarnContext(r.Context(), "refusing a job: its source on this node's mount is not the approved revision",
			"component", "transcodenode", "error", err)
		http.Error(w, errLocalSourceMismatch, http.StatusConflict)
	case sourceRefused(err):
		slog.WarnContext(r.Context(), "core refused a job's source", "component", "transcodenode", "error", err)
		http.Error(w, "source_grant must name an approved media file", http.StatusBadRequest)
	default:
		slog.WarnContext(r.Context(), "open a job's source", "component", "transcodenode", "error", err)
		http.Error(w, errSourceAuthorityUnavailable.Error(), http.StatusServiceUnavailable)
	}
	return nil, false
}

// inputContext derives a job's context from ctx that also ends when the job's
// source does: closed, or withdrawn by the core at a renewal or a read.
func inputContext(ctx context.Context, input SourceInput) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(input.Context(), func() { cancel(input.Err()) })
	return ctx, func() {
		stop()
		cancel(context.Canceled)
	}
}

// watchSessionInput ends session sessionID when its source is withdrawn while
// it runs: a renewal or a read the core refused. A handle closed by the
// session's own teardown ends nothing.
func (s *Server) watchSessionInput(sessionID string, session *playback.TranscodeSession, input SourceInput) {
	context.AfterFunc(input.Context(), func() {
		if errors.Is(input.Err(), nodesource.ErrClosed) {
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
			"session", sessionID, "source", input.Identity(), "error", input.Err())
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
