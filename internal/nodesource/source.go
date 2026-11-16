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

// ReadRequest is one ranged read of a source's bytes from the core's
// PathSources route: bytes [Offset, Offset+Length) of the revision ETag
// names, under Grant.
type ReadRequest struct {
	Grant  string
	ETag   string
	Offset int64
	Length int64
}

// FetchFunc sends one ReadRequest to the core, signed as the node, with the
// grant in HeaderSourceGrant, the range, If-Match on the ETag and
// Accept-Encoding identity, through a client that uses no proxy, asks for no
// compression and follows no redirect. It returns the core's answer
// whatever its status; the caller checks it and closes the body.
// nodecontrol.Client.ReadSource is the node's.
type FetchFunc func(ctx context.Context, req ReadRequest) (*http.Response, error)

// Source is one job's approved input. It renews its grant every
// RenewInterval until it is closed or the core withdraws it; either ends its
// Context, and Err says why.
type Source interface {
	// Input is what FFmpeg opens: with local access the approved,
	// fspath-encoded catalog path; with core access the source's loopback
	// relay URL, which internal/fspath answers for (fspath.IsRelayed).
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
	// renewal or a read, ErrGrantExpired when the grant ran out while the
	// core was unreachable; nil while it is open.
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
	return "source mount mismatch: " + core + ", but " + node + "; mount the library at the core's paths with the same files, or set this node's source access to core"
}

func (e *MountMismatchError) Is(target error) bool { return target == ErrMountMismatch }

func (e *MountMismatchError) Unwrap() error { return e.NodeErr }

// CoreAccess is what an opener needs to read sources through the core.
type CoreAccess struct {
	// Fetch reads source bytes from the core.
	Fetch FetchFunc
	// Mode reports the node's configured source access, ModeLocal or
	// ModeCore. It is read at every open, so a change applies to jobs
	// opened after it while open sources keep their mode.
	Mode func() string
}

// Opener opens job sources on a transcode node: it exchanges each job's grant
// at the core and renews it while the job runs. With local source access it
// checks the approved path on the node's own mount; with core access it
// serves the source to FFmpeg and Go readers through a loopback relay that
// reads the bytes from the core (see relay.go).
type Opener struct {
	open   OpenFunc
	logger *slog.Logger

	// fetch and mode are the core access; fetch is nil without it.
	fetch FetchFunc
	mode  func() string

	renewInterval time.Duration
	retryInitial  time.Duration
	retryMax      time.Duration
	// retry paces upstream reads of core-access sources.
	retry backoff
	now   func() time.Time

	// relay is the loopback listener of core-access sources, started by the
	// first one.
	relay relayListener

	mu     sync.Mutex
	closed bool
	// leases are every live lease: local sources and relay entries.
	leases map[*lease]struct{}
	// entries are the core-access sources open now, by URL token: one per
	// source revision, shared by every job that reads it.
	entries  map[string]*relayEntry
	renewals sync.WaitGroup
}

// NewOpener returns an opener that exchanges grants through open and logs to
// logger (slog.Default when nil). It opens every source with local access
// until EnableCoreAccess.
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
		retry:         defaultBackoff,
		now:           time.Now,
		relay:         newRelayListener(),
		leases:        map[*lease]struct{}{},
		entries:       map[string]*relayEntry{},
	}
}

// EnableCoreAccess lets the opener read sources through the core whenever
// access.Mode reports ModeCore. Call it before the first Open.
func (o *Opener) EnableCoreAccess(access CoreAccess) {
	o.fetch, o.mode = access.Fetch, access.Mode
}

// SetRenewInterval changes how often an open source renews its grant from
// RenewInterval, so a test can watch a renewal the core refuses end a job.
// Call it before the first Open.
func (o *Opener) SetRenewInterval(interval time.Duration) {
	o.renewInterval = interval
}

// coreMode reports whether a source opened now reads through the core.
func (o *Opener) coreMode() bool {
	return o.fetch != nil && o.mode != nil && o.mode() == ModeCore
}

// Open exchanges a job's grant at the core and returns its source, renewed
// until it is closed. With local access the core's approved path must be a
// regular file of the approved revision on this node's mount
// (*MountMismatchError otherwise); with core access the source is served on
// the loopback relay. A refusal from the core is a *StatusError; any other
// error is transient. The caller closes the source when the job ends.
func (o *Opener) Open(ctx context.Context, grant string) (Source, error) {
	grant = strings.TrimSpace(grant)
	if grant == "" {
		return nil, ErrNoGrant
	}
	if o.isClosed() {
		return nil, ErrOpenerClosed
	}
	core := o.coreMode()
	opened, err := o.exchange(ctx, grant)
	if err != nil {
		return nil, err
	}
	if core {
		return o.openCore(opened)
	}
	h := &handle{lease: o.newLease(opened)}
	if err := o.checkLocal(ctx, h.lease); err != nil {
		h.cancel(err)
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		h.cancel(ErrOpenerClosed)
		return nil, ErrOpenerClosed
	}
	o.trackLocked(h.lease)
	return h, nil
}

// newLease returns an unstarted lease of an open answer. It outlives the
// request that opened it: it ends when its holder ends it or the core
// withdraws it.
func (o *Opener) newLease(opened *OpenResponse) *lease {
	ctx, cancel := context.WithCancelCause(context.Background())
	l := &lease{
		opener:  o,
		path:    opened.Path,
		size:    opened.Size,
		modTime: time.Unix(0, opened.ModTimeUnixNano),
		etag:    opened.ETag,
		ctx:     ctx,
		cancel:  cancel,
	}
	l.setGrant(opened.Grant)
	return l
}

// trackLocked records l as live and starts its renewal. The caller holds mu
// and has checked the opener is open.
func (o *Opener) trackLocked(l *lease) {
	o.leases[l] = struct{}{}
	o.renewals.Go(l.renewLoop)
}

// Close ends every open source with ErrOpenerClosed, waits for their
// renewal loops to stop and stops the loopback relay. Open refuses
// afterwards.
func (o *Opener) Close() error {
	o.mu.Lock()
	o.closed = true
	live := make([]*lease, 0, len(o.leases))
	for l := range o.leases {
		live = append(live, l)
	}
	o.mu.Unlock()
	for _, l := range live {
		l.end(ErrOpenerClosed)
	}
	o.renewals.Wait()
	return o.relay.close()
}

// checkLocal checks that this node's mount shows l's approved path as the
// revision the core approved, at revision precision. A mismatch is logged
// here with what each side saw.
func (o *Opener) checkLocal(ctx context.Context, l *lease) error {
	info, err := fspath.StatContext(ctx, l.path)
	if err == nil && matchesRevision(info, l.size, l.modTime) {
		return nil
	}
	if err != nil && ctx.Err() != nil {
		// The job's request ended while the stat waited; that says nothing
		// about the mount.
		return err
	}
	mismatch := &MountMismatchError{Path: l.path, CoreSize: l.size, CoreModTime: l.modTime, NodeErr: err}
	if err == nil {
		mismatch.NodeMode, mismatch.NodeSize, mismatch.NodeModTime = info.Mode(), info.Size(), info.ModTime()
	}
	o.logger.WarnContext(ctx, "this node's mount does not show the source the core approved; check that it mounts the library at the core's paths",
		"source", l.etag, "error", mismatch)
	return mismatch
}

func (o *Opener) forget(l *lease) {
	o.mu.Lock()
	delete(o.leases, l)
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

// lease is one approved source revision and the grant that keeps it: it
// renews the grant every renewInterval until it ends, and ends when a renewal
// fails for good. A local-access source holds one of its own; core-access
// sources of one revision share their relay entry's.
type lease struct {
	opener  *Opener
	path    string
	size    int64
	modTime time.Time
	etag    string

	grantMu sync.Mutex
	grant   string
	expires time.Time

	ctx    context.Context //nolint:containedctx // the lease's own lifetime, which sources expose
	cancel context.CancelCauseFunc
	ended  atomic.Bool
	// release runs once when the lease ends, after its context; nil forgets
	// it from the opener.
	release func()
}

// Err is why the lease ended, or nil while it lives.
func (l *lease) Err() error { return context.Cause(l.ctx) }

func (l *lease) currentGrant() (string, time.Time) {
	l.grantMu.Lock()
	defer l.grantMu.Unlock()
	return l.grant, l.expires
}

// end ends the lease once, with cause as its Err.
func (l *lease) end(cause error) {
	if !l.ended.CompareAndSwap(false, true) {
		return
	}
	l.cancel(cause)
	if l.release != nil {
		l.release()
	} else {
		l.opener.forget(l)
	}
}

// fail ends the lease with the core's lasting refusal of its source.
func (l *lease) fail(err error) {
	if l.ctx.Err() == nil {
		l.opener.logger.Warn("source withdrawn; its job's input ends", "source", l.etag, "error", err)
	}
	l.end(err)
}

// grantExpiry is when grant stops being valid, read from its payload; a
// grant whose payload cannot be read is assumed to last GrantLifetime.
func grantExpiry(grant string) time.Time {
	if claims, err := peek(grant); err == nil {
		return claims.Expires()
	}
	return time.Now().Add(GrantLifetime)
}

// setGrant swaps in grant.
func (l *lease) setGrant(grant string) {
	expires := grantExpiry(grant)
	l.grantMu.Lock()
	l.grant, l.expires = grant, expires
	l.grantMu.Unlock()
}

// offerGrant swaps in grant when it lasts longer than the current one: a job
// that opens a shared source brings a fresher grant for the same revision.
func (l *lease) offerGrant(grant string) {
	expires := grantExpiry(grant)
	l.grantMu.Lock()
	if expires.After(l.expires) {
		l.grant, l.expires = grant, expires
	}
	l.grantMu.Unlock()
}

// renewLoop renews the grant every renewInterval while the lease lives, and
// ends it when a renewal fails for good.
func (l *lease) renewLoop() {
	timer := time.NewTimer(l.opener.renewInterval)
	defer timer.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-timer.C:
		}
		if err := l.renew(); err != nil {
			l.fail(err)
			return
		}
		timer.Reset(l.opener.renewInterval)
	}
}

// renew exchanges the current grant for a fresh one, retrying a transient
// failure until the current grant expires. The core keeps the revision; an
// answer about another one is a changed source.
func (l *lease) renew() error {
	delay := l.opener.retryInitial
	for {
		grant, expires := l.currentGrant()
		opened, err := l.opener.exchange(l.ctx, grant)
		if err == nil {
			if opened.ETag != l.etag || opened.Path != l.path || opened.Size != l.size ||
				!SameModTime(time.Unix(0, opened.ModTimeUnixNano), l.modTime) {
				return &StatusError{Op: "renew", Status: http.StatusPreconditionFailed}
			}
			l.offerGrant(opened.Grant)
			return nil
		}
		if l.ctx.Err() != nil {
			return context.Cause(l.ctx)
		}
		if statusErr, ok := errors.AsType[*StatusError](err); ok && statusErr.Fatal() {
			// The refusal answers the grant this attempt carried. Another
			// job may have brought a newer one meanwhile: that one decides.
			if held, _ := l.currentGrant(); held != grant {
				continue
			}
			return err
		}
		// Another job on this source may have brought a longer grant while
		// the attempt was out: judge by the one held now.
		if _, held := l.currentGrant(); held.After(expires) {
			expires = held
		}
		if !time.Now().Add(delay).Before(expires) {
			return fmt.Errorf("%w while the core was unreachable: %w", ErrGrantExpired, err)
		}
		l.opener.logger.Warn("renew source grant; retrying", "source", l.etag, "retry_in", delay, "error", err)
		if !sleepCtx(l.ctx, delay) {
			return context.Cause(l.ctx)
		}
		delay = min(delay*2, l.opener.retryMax)
	}
}

// handle is a local-access Source: the lease of its own revision.
type handle struct{ *lease }

func (h *handle) Input() string            { return h.path }
func (h *handle) Identity() string         { return h.etag }
func (h *handle) Size() int64              { return h.size }
func (h *handle) ModTime() time.Time       { return h.modTime }
func (h *handle) Context() context.Context { return h.ctx }
func (h *handle) Close() error             { h.end(ErrClosed); return nil }

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
