package nodesource

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// openTimeout bounds one call to the core's open endpoint.
const openTimeout = 10 * time.Second

// Renewal retry pacing for a transient failure. Retries go on until the
// current grant expires.
const (
	renewRetryInitial = time.Second
	renewRetryMax     = time.Minute
)

var (
	// ErrNoGrant is a job that carries no source grant.
	ErrNoGrant = errors.New("nodesource: job carries no source grant")
	// ErrMountMismatch is a source whose file on this node's mount is
	// missing, not a regular file, or not the revision the core approved: the
	// node does not see the library as the core does. See MountMismatchError.
	ErrMountMismatch = errors.New("nodesource: this node's mount does not show the source revision the core approved")
	// ErrClosed is a source its holder closed.
	ErrClosed = errors.New("nodesource: source closed")
	// ErrOpenerClosed is an opener that has shut down.
	ErrOpenerClosed = errors.New("nodesource: opener closed")
	// errMalformedAnswer is an open answer that does not describe a source.
	errMalformedAnswer = errors.New("nodesource: malformed open answer")
)

// OpenFunc exchanges a grant at the core's sources/open route, signed as the
// node. A refusal is a *StatusError; any other error is retried like no
// answer. nodecontrol.Client.OpenSource is the node's.
type OpenFunc func(ctx context.Context, grant string) (*OpenResponse, error)

// Source is one job's approved input. It renews its grant every
// RenewInterval until it is closed or the core withdraws it; either ends its
// Context, and Err says why. A later source access mode (reading the bytes
// through the core) implements it with another Input.
type Source interface {
	// Input is what FFmpeg opens: with local access the approved,
	// fspath-encoded catalog path.
	Input() string
	// Identity is the source revision's entity tag (Revision.ETag). It names
	// the source in logs and may key caches.
	Identity() string
	// Size is the source's size in bytes.
	Size() int64
	// ModTime is the granted modification time, as the core's stat returned
	// it. Compare it at revision precision (SameModTime).
	ModTime() time.Time
	// Context ends when the source is closed or the core withdraws it. A job
	// derives its own context from it, so a withdrawn source stops the job.
	Context() context.Context
	// Err is why the source ended: ErrClosed after Close, ErrOpenerClosed
	// after the opener closed, a fatal *StatusError when the core refused a
	// renewal, ErrGrantExpired when the grant ran out while the core was
	// unreachable; nil while it is open.
	Err() error
	// Close releases the source and stops its renewal. It is idempotent.
	Close() error
}

// MountMismatchError is ErrMountMismatch with what each side saw. The node
// answers it with 409 and this message: it is the node's mount, not the
// source, that needs fixing.
type MountMismatchError struct {
	Path        string
	CoreSize    int64
	CoreModTime time.Time
	// NodeErr is the node's stat failure; when nil, NodeMode, NodeSize and
	// NodeModTime are what the node's stat returned.
	NodeErr     error
	NodeMode    fs.FileMode
	NodeSize    int64
	NodeModTime time.Time
}

func (e *MountMismatchError) Error() string {
	core := fmt.Sprintf("the core approved %q as %d bytes modified %s", fspath.Display(e.Path), e.CoreSize,
		e.CoreModTime.UTC().Format(time.RFC3339Nano))
	var node string
	switch {
	case e.NodeErr != nil:
		node = fmt.Sprintf("this node cannot stat it: %v", e.NodeErr)
	case !e.NodeMode.IsRegular():
		node = fmt.Sprintf("this node sees a %s, not a regular file", e.NodeMode.Type())
	default:
		node = fmt.Sprintf("this node sees %d bytes modified %s", e.NodeSize, e.NodeModTime.UTC().Format(time.RFC3339Nano))
	}
	return "source mount mismatch: " + core + ", but " + node + "; mount the library at the core's paths with the same files"
}

func (e *MountMismatchError) Is(target error) bool { return target == ErrMountMismatch }

func (e *MountMismatchError) Unwrap() error { return e.NodeErr }

// Opener opens job sources on a transcode node: it exchanges each job's grant
// at the core, checks the approved path on the node's own mount, and renews
// the grant while the job runs.
type Opener struct {
	open   OpenFunc
	logger *slog.Logger

	renewInterval time.Duration
	retryInitial  time.Duration
	retryMax      time.Duration

	mu       sync.Mutex
	closed   bool
	live     map[*handle]struct{}
	renewals sync.WaitGroup
}

// NewOpener returns an opener that exchanges grants through open and logs to
// logger (slog.Default when nil).
func NewOpener(open OpenFunc, logger *slog.Logger) *Opener {
	if logger == nil {
		logger = slog.Default()
	}
	return &Opener{
		open:          open,
		logger:        logger.With("component", "nodesource"),
		renewInterval: RenewInterval,
		retryInitial:  renewRetryInitial,
		retryMax:      renewRetryMax,
		live:          map[*handle]struct{}{},
	}
}

// SetRenewInterval changes how often an open source renews its grant from
// RenewInterval, so a test can watch a renewal the core refuses end a job.
// Call it before the first Open.
func (o *Opener) SetRenewInterval(interval time.Duration) {
	o.renewInterval = interval
}

// Open exchanges a job's grant at the core and returns its source, renewed
// until it is closed. The core's approved path must be a regular file of the
// approved revision on this node's mount (*MountMismatchError otherwise). A
// refusal from the core is a *StatusError; any other error is transient. The
// caller closes the source when the job ends.
func (o *Opener) Open(ctx context.Context, grant string) (Source, error) {
	grant = strings.TrimSpace(grant)
	if grant == "" {
		return nil, ErrNoGrant
	}
	if o.isClosed() {
		return nil, ErrOpenerClosed
	}
	opened, err := o.exchange(ctx, grant)
	if err != nil {
		return nil, err
	}
	// The source outlives the request that opened it: it ends when the job
	// closes it or the core withdraws it.
	sourceCtx, cancel := context.WithCancelCause(context.Background())
	h := &handle{
		opener:  o,
		path:    opened.Path,
		size:    opened.Size,
		modTime: time.Unix(0, opened.ModTimeUnixNano),
		etag:    opened.ETag,
		ctx:     sourceCtx,
		cancel:  cancel,
	}
	h.setGrant(opened.Grant)
	if err := o.checkLocal(ctx, h); err != nil {
		cancel(err)
		return nil, err
	}
	if err := o.track(h); err != nil {
		h.cancel(err)
		return nil, err
	}
	return h, nil
}

// Close ends every open source with ErrOpenerClosed and waits for their
// renewal loops to stop. Open refuses afterwards.
func (o *Opener) Close() error {
	o.mu.Lock()
	o.closed = true
	live := make([]*handle, 0, len(o.live))
	for h := range o.live {
		live = append(live, h)
	}
	o.mu.Unlock()
	for _, h := range live {
		h.end(ErrOpenerClosed)
	}
	o.renewals.Wait()
	return nil
}

// checkLocal checks that this node's mount shows h's approved path as the
// revision the core approved, at revision precision. A mismatch is logged
// here with what each side saw.
func (o *Opener) checkLocal(ctx context.Context, h *handle) error {
	info, err := fspath.StatContext(ctx, h.path)
	if err == nil && matchesRevision(info, h.size, h.modTime) {
		return nil
	}
	if err != nil && ctx.Err() != nil {
		// The job's request ended while the stat waited; that says nothing
		// about the mount.
		return err
	}
	mismatch := &MountMismatchError{Path: h.path, CoreSize: h.size, CoreModTime: h.modTime, NodeErr: err}
	if err == nil {
		mismatch.NodeMode, mismatch.NodeSize, mismatch.NodeModTime = info.Mode(), info.Size(), info.ModTime()
	}
	o.logger.WarnContext(ctx, "this node's mount does not show the source the core approved; check that it mounts the library at the core's paths",
		"source", h.etag, "error", mismatch)
	return mismatch
}

func (o *Opener) track(h *handle) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return ErrOpenerClosed
	}
	o.live[h] = struct{}{}
	o.renewals.Go(h.renewLoop)
	return nil
}

func (o *Opener) forget(h *handle) {
	o.mu.Lock()
	delete(o.live, h)
	o.mu.Unlock()
}

func (o *Opener) isClosed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closed
}

// exchange calls the core's open endpoint once with grant and checks the
// answer is shaped like a source.
func (o *Opener) exchange(ctx context.Context, grant string) (*OpenResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	opened, err := o.open(ctx, grant)
	if err != nil {
		return nil, err
	}
	if opened == nil || opened.Grant == "" || opened.ETag == "" || opened.Size < 0 || !plainAbsolutePath(opened.Path) {
		return nil, errMalformedAnswer
	}
	return opened, nil
}

// handle is a local-access Source.
type handle struct {
	opener  *Opener
	path    string
	size    int64
	modTime time.Time
	etag    string

	grantMu sync.Mutex
	grant   string
	expires time.Time

	ctx    context.Context //nolint:containedctx // the source's own lifetime, which Context exposes
	cancel context.CancelCauseFunc
	closed atomic.Bool
}

func (h *handle) Input() string            { return h.path }
func (h *handle) Identity() string         { return h.etag }
func (h *handle) Size() int64              { return h.size }
func (h *handle) ModTime() time.Time       { return h.modTime }
func (h *handle) Context() context.Context { return h.ctx }
func (h *handle) Err() error               { return context.Cause(h.ctx) }
func (h *handle) Close() error             { h.end(ErrClosed); return nil }
func (h *handle) currentGrant() (string, time.Time) {
	h.grantMu.Lock()
	defer h.grantMu.Unlock()
	return h.grant, h.expires
}

// end releases the handle once, with cause as its Err.
func (h *handle) end(cause error) {
	if h.closed.CompareAndSwap(false, true) {
		h.cancel(cause)
		h.opener.forget(h)
	}
}

// setGrant swaps in grant. Its expiry is read from its payload; a grant whose
// payload cannot be read is assumed to last GrantLifetime.
func (h *handle) setGrant(grant string) {
	expires := time.Now().Add(GrantLifetime)
	if claims, err := peek(grant); err == nil {
		expires = claims.Expires()
	}
	h.grantMu.Lock()
	h.grant, h.expires = grant, expires
	h.grantMu.Unlock()
}

// renewLoop renews the grant every renewInterval while the handle is open,
// and ends the handle when a renewal fails for good.
func (h *handle) renewLoop() {
	timer := time.NewTimer(h.opener.renewInterval)
	defer timer.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-timer.C:
		}
		if err := h.renew(); err != nil {
			if h.ctx.Err() == nil {
				h.opener.logger.Warn("source withdrawn; its job's input ends", "source", h.etag, "error", err)
			}
			h.end(err)
			return
		}
		timer.Reset(h.opener.renewInterval)
	}
}

// renew exchanges the current grant for a fresh one, retrying a transient
// failure until the current grant expires. The core keeps the revision; an
// answer about another one is a changed source.
func (h *handle) renew() error {
	delay := h.opener.retryInitial
	for {
		grant, expires := h.currentGrant()
		opened, err := h.opener.exchange(h.ctx, grant)
		if err == nil {
			if opened.ETag != h.etag || opened.Path != h.path || opened.Size != h.size ||
				!SameModTime(time.Unix(0, opened.ModTimeUnixNano), h.modTime) {
				return &StatusError{Op: "renew", Status: http.StatusPreconditionFailed}
			}
			h.setGrant(opened.Grant)
			return nil
		}
		if h.ctx.Err() != nil {
			return context.Cause(h.ctx)
		}
		if statusErr, ok := errors.AsType[*StatusError](err); ok && statusErr.Fatal() {
			return err
		}
		if !time.Now().Add(delay).Before(expires) {
			return fmt.Errorf("%w while the core was unreachable: %w", ErrGrantExpired, err)
		}
		h.opener.logger.Warn("renew source grant; retrying", "source", h.etag, "retry_in", delay, "error", err)
		if !sleepCtx(h.ctx, delay) {
			return context.Cause(h.ctx)
		}
		delay = min(delay*2, h.opener.retryMax)
	}
}

// sleepCtx waits d or until ctx ends, and reports whether it waited d.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
