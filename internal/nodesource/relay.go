package nodesource

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RenewInterval is how often a node renews an open source's grant: a third
// of GrantLifetime, so renewal survives a long core outage.
const RenewInterval = 20 * time.Minute

// openTimeout bounds one call to the core's open endpoint.
const openTimeout = 10 * time.Second

// renewBackoff paces renewal retries of a transient failure, which go on
// until the current grant expires (giveUp is unused).
var renewBackoff = backoff{initial: time.Second, max: time.Minute}

var (
	// ErrNoGrant is a job that carries no source grant. A node never trusts
	// a bare input path.
	ErrNoGrant = errors.New("nodesource: job carries no source grant")
	// ErrLocalMismatch is a local-access source whose file on this node is
	// missing, not a regular file, or not the revision the core approved: the
	// node's mount does not show the library as the core's does.
	ErrLocalMismatch = errors.New("nodesource: the approved source on this node's mount is not the granted revision")
	// ErrClosed is a handle its job released.
	ErrClosed = errors.New("nodesource: source handle closed")
	// ErrRelayClosed is a relay that has shut down.
	ErrRelayClosed = errors.New("nodesource: relay closed")
)

// OpenFunc exchanges a grant at the core's open endpoint (PathOpen), signed
// as the node. A refusal is a *StatusError with Op "open"; any other error is
// retried like no answer. nodecontrol.Client.OpenSource is the node's.
type OpenFunc func(ctx context.Context, grant string) (*OpenResponse, error)

// RelayOptions configure a node's Relay.
type RelayOptions struct {
	// Open exchanges grants at the core, for a job's start and its renewals.
	Open OpenFunc
	// CoreURL is where the node reads source bytes from (SILO_CORE_URL).
	CoreURL string
	// NodeID and NodeKey sign every byte read.
	NodeID  int
	NodeKey string
	// Mode reports the node's source access, ModeLocal or ModeCore, as its
	// configuration currently says. Nil, or anything but ModeCore, is local.
	Mode func() string
	// Transport overrides the upstream transport; tests use it.
	Transport http.RoundTripper
}

// Relay is a transcode node's side of source access. It exchanges job grants
// for sources at the core, renews them while the job runs and, for core
// source access, serves each source to FFmpeg on a private loopback listener,
// fetching the bytes from the core with retry and resume.
type Relay struct {
	open    OpenFunc
	coreURL string
	nodeID  int
	key     string
	mode    func() string
	client  *http.Client
	logger  *slog.Logger

	// listenMu guards the loopback listener, started by the first
	// core-access source.
	listenMu sync.Mutex
	listener net.Listener
	server   *http.Server

	mu sync.Mutex
	// handles are the core-access handles by URL token; live is every open
	// handle, local or core access, so Close reaches them all.
	handles map[string]*Handle
	live    map[*Handle]struct{}
	closed  bool
	// renewals counts the running renewal loops; Close waits for them.
	renewals sync.WaitGroup

	// Timing knobs; tests shorten them.
	retry         backoff
	renewRetry    backoff
	renewInterval time.Duration
	now           func() time.Time
}

// NewRelay returns a node's relay. It listens on loopback only once the first
// core-access source opens.
func NewRelay(opts RelayOptions) (*Relay, error) {
	coreURL := strings.TrimRight(strings.TrimSpace(opts.CoreURL), "/")
	if opts.Open == nil || coreURL == "" || opts.NodeID <= 0 || opts.NodeKey == "" {
		return nil, errors.New("nodesource: a relay needs the core's open endpoint, the core URL, the node id and its key")
	}
	transport := opts.Transport
	if transport == nil {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, errors.New("nodesource: no HTTP transport")
		}
		t := base.Clone()
		t.Proxy = nil
		t.DisableCompression = true
		t.ResponseHeaderTimeout = 10 * time.Second
		t.MaxIdleConnsPerHost = 16
		transport = t
	}
	mode := opts.Mode
	if mode == nil {
		mode = func() string { return ModeLocal }
	}
	return &Relay{
		open:    opts.Open,
		coreURL: coreURL,
		nodeID:  opts.NodeID,
		key:     opts.NodeKey,
		mode:    mode,
		client: &http.Client{
			Transport: transport,
			// A redirect would send a signed request somewhere it was not
			// signed for.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		logger:        slog.Default().With("component", "nodesource"),
		handles:       make(map[string]*Handle),
		live:          make(map[*Handle]struct{}),
		retry:         defaultBackoff,
		renewRetry:    renewBackoff,
		renewInterval: RenewInterval,
		now:           time.Now,
	}, nil
}

// Open exchanges a job's grant at the core and returns its source, renewed
// until the handle is closed. With local access the core's approved path must
// be a regular file of the approved revision on this node's mount; with core
// access the handle serves the bytes on loopback. The caller closes the handle
// when the job ends.
func (r *Relay) Open(ctx context.Context, grant string) (*Handle, error) {
	grant = strings.TrimSpace(grant)
	if grant == "" {
		return nil, ErrNoGrant
	}
	if r.isClosed() {
		return nil, ErrRelayClosed
	}
	opened, err := r.exchange(ctx, grant)
	if err != nil {
		return nil, err
	}
	h := &Handle{
		relay:   r,
		path:    opened.Path,
		size:    opened.Size,
		modTime: time.Unix(opened.Mtime, 0),
		etag:    opened.ETag,
		remote:  r.mode() == ModeCore,
	}
	h.setGrant(opened.Grant, r.now())
	h.ctx, h.cancel = context.WithCancelCause(context.Background())
	if !h.remote {
		if err := r.checkLocal(ctx, h); err != nil {
			h.cancel(err)
			return nil, err
		}
	}
	if err := r.track(h); err != nil {
		h.cancel(err)
		return nil, err
	}
	go func() {
		defer r.renewals.Done()
		h.renewLoop()
	}()
	return h, nil
}

// checkLocal checks that this node's mount shows h's approved path as the
// revision the core approved, at revision precision (RevisionMtime). A
// mismatch is logged here with what each side saw, since it is the node's
// mount, not the source, that needs fixing.
func (r *Relay) checkLocal(ctx context.Context, h *Handle) error {
	info, err := os.Stat(h.path)
	if err == nil && sameRevision(info, h.size, RevisionMtime(h.modTime)) {
		return nil
	}
	attrs := []any{"source", h.etag, "path", h.path,
		"core_size", h.size, "core_mtime", RevisionMtime(h.modTime)}
	switch {
	case err != nil:
		attrs = append(attrs, "error", err)
	case !info.Mode().IsRegular():
		attrs = append(attrs, "mode", info.Mode().String())
	default:
		attrs = append(attrs, "node_size", info.Size(), "node_mtime", RevisionMtime(info.ModTime()))
	}
	r.logger.WarnContext(ctx, "local source access: this node's mount does not show the source the core approved; "+
		"check that it mounts the library at the core's paths", attrs...)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLocalMismatch, err)
	}
	return ErrLocalMismatch
}

// track records h as open, and for core access registers its loopback URL.
// A relay that has closed takes no new handle.
func (r *Relay) track(h *Handle) error {
	if h.remote {
		if err := r.register(h); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		if h.token != "" {
			delete(r.handles, h.token)
			unregisterURL(h)
		}
		return ErrRelayClosed
	}
	r.live[h] = struct{}{}
	r.renewals.Add(1)
	return nil
}

// openHandles is the number of the relay's handles not yet closed.
func (r *Relay) openHandles() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.live)
}

func (r *Relay) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// exchange calls the core's open endpoint once with grant and checks the
// answer.
func (r *Relay) exchange(ctx context.Context, grant string) (*OpenResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	opened, err := r.open(ctx, grant)
	if err != nil {
		return nil, err
	}
	if opened == nil || opened.Grant == "" || opened.ETag == "" || opened.Size < 0 || !filepath.IsAbs(opened.Path) || strings.ContainsRune(opened.Path, 0) {
		return nil, errors.New("nodesource: malformed open answer")
	}
	return opened, nil
}

// register puts h on the loopback listener under a fresh unguessable path.
func (r *Relay) register(h *Handle) error {
	addr, err := r.listenAddr()
	if err != nil {
		return err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	h.token = hex.EncodeToString(token[:])
	h.url = "http://" + addr + "/src/" + h.token
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRelayClosed
	}
	r.handles[h.token] = h
	registerURL(h)
	return nil
}

// forget drops h from the relay: its URL, if any, stops resolving.
func (r *Relay) forget(h *Handle) {
	r.mu.Lock()
	delete(r.live, h)
	if h.token != "" && r.handles[h.token] == h {
		delete(r.handles, h.token)
	}
	r.mu.Unlock()
	if h.token != "" {
		unregisterURL(h)
	}
}

// listenAddr starts the loopback listener on first use and returns its
// address. A failure is not remembered: the next core-access source tries
// again.
func (r *Relay) listenAddr() (string, error) {
	r.listenMu.Lock()
	defer r.listenMu.Unlock()
	if r.listener != nil {
		return r.listener.Addr().String(), nil
	}
	if r.isClosed() {
		return "", ErrRelayClosed
	}
	listener, err := listenLoopback()
	if err != nil {
		return "", fmt.Errorf("nodesource: listen on loopback: %w", err)
	}
	r.listener = listener
	r.server = &http.Server{
		Handler:           http.HandlerFunc(r.serve),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func(server *http.Server) { _ = server.Serve(listener) }(r.server)
	return listener.Addr().String(), nil
}

// listenLoopback opens the relay's listener; tests replace it.
var listenLoopback = func() (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }

// Close fails every open handle, local or core access, with ErrRelayClosed,
// waits for their renewal loops to stop, and stops the loopback listener.
// Opening a source afterwards fails with ErrRelayClosed.
func (r *Relay) Close() error {
	r.mu.Lock()
	r.closed = true
	handles := make([]*Handle, 0, len(r.live))
	for h := range r.live {
		handles = append(handles, h)
	}
	r.mu.Unlock()
	for _, h := range handles {
		h.end(ErrRelayClosed)
	}
	r.renewals.Wait()
	r.client.CloseIdleConnections()
	r.listenMu.Lock()
	server := r.server
	r.listenMu.Unlock()
	if server != nil {
		return server.Close()
	}
	return nil
}

// Handle is one job's source. It renews its grant every RenewInterval until
// it is closed or the core refuses it, which fails the handle: its Context
// ends and every read fails. Its loopback URL (core access) is a temporary
// bearer capability on the node host, so it is never logged; Identity names
// the source in logs instead.
type Handle struct {
	relay   *Relay
	path    string
	size    int64
	modTime time.Time
	etag    string
	remote  bool
	token   string
	url     string

	grantMu sync.RWMutex
	grant   string
	expires time.Time

	ctx    context.Context
	cancel context.CancelCauseFunc
	closed atomic.Bool
	// upstreamFailed is set once the relay could not serve a read of the
	// handle's URL because of the core (see UpstreamFailed).
	upstreamFailed atomic.Bool
}

// Input is what FFmpeg opens: the approved path with local access, or the
// loopback URL with core access.
func (h *Handle) Input() string {
	if h.remote {
		return h.url
	}
	return h.path
}

// URL is the handle's loopback URL, or "" with local access.
func (h *Handle) URL() string { return h.url }

// Remote reports core source access: FFmpeg reads Input over HTTP.
func (h *Handle) Remote() bool { return h.remote }

// Identity is the source revision's protocol entity tag. It names the source
// in logs and keys caches; unlike the URL it is no capability.
func (h *Handle) Identity() string { return h.etag }

// Size is the source's size in bytes.
func (h *Handle) Size() int64 { return h.size }

// ModTime is the source's modification time.
func (h *Handle) ModTime() time.Time { return h.modTime }

// Context ends when the handle is closed or fails. A job derives its own
// context from it so a source the core withdrew stops the job.
func (h *Handle) Context() context.Context { return h.ctx }

// Err is why the handle ended: ErrClosed after Close, ErrRelayClosed after
// the relay closed, the core's *StatusError when it refused the source, nil
// while it is open.
func (h *Handle) Err() error { return context.Cause(h.ctx) }

// UpstreamFailed reports whether a read through the handle's URL failed on
// the core's side: a range the relay could not fetch, or a body it had to cut
// short. FFmpeg sees either as an input error of its own, so a caller telling
// a fault in the source's bytes from a relay failure asks here.
func (h *Handle) UpstreamFailed() bool { return h.upstreamFailed.Load() }

// Close releases the handle: it stops renewal, unregisters the loopback URL
// and interrupts every read in flight, including a write FFmpeg's back
// pressure holds.
func (h *Handle) Close() error {
	h.end(ErrClosed)
	return nil
}

// Reopen opens the handle's source again through its relay, from its current
// grant, as a handle of its own with its own renewal: for work on the job's
// behalf that may outlive the job's handle, such as staging a subtitle that a
// later job on the same source reuses. The caller closes it.
func (h *Handle) Reopen(ctx context.Context) (*Handle, error) {
	return h.relay.Open(ctx, h.currentGrant())
}

// end releases the handle once, with cause as its Err.
func (h *Handle) end(cause error) {
	if h.closed.CompareAndSwap(false, true) {
		h.cancel(cause)
		h.relay.forget(h)
	}
}

// fail ends the handle with the core's lasting refusal.
func (h *Handle) fail(err error) {
	if h.ctx.Err() == nil {
		h.relay.logger.Warn("source withdrawn by the core; its job's input ends", "source", h.etag, "error", err)
	}
	h.cancel(err)
}

func (h *Handle) currentGrant() string {
	h.grantMu.RLock()
	defer h.grantMu.RUnlock()
	return h.grant
}

func (h *Handle) grantExpires() time.Time {
	h.grantMu.RLock()
	defer h.grantMu.RUnlock()
	return h.expires
}

// setGrant swaps in grant. Its expiry is read from its payload; a grant
// whose payload cannot be read is assumed to last GrantLifetime.
func (h *Handle) setGrant(grant string, now time.Time) {
	expires := now.Add(GrantLifetime)
	if claims, err := peek(grant); err == nil {
		expires = claims.Expires()
	}
	h.grantMu.Lock()
	h.grant, h.expires = grant, expires
	h.grantMu.Unlock()
}

// renewLoop renews the grant every RenewInterval while the handle is open.
func (h *Handle) renewLoop() {
	timer := time.NewTimer(h.relay.renewInterval)
	defer timer.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-timer.C:
		}
		if err := h.renew(); err != nil {
			h.fail(err)
			return
		}
		timer.Reset(h.relay.renewInterval)
	}
}

// renew exchanges the current grant for a fresh one, retrying a transient
// failure until the current grant expires. The core keeps the revision; an
// answer about another one is a changed source.
func (h *Handle) renew() error {
	delay := h.relay.renewRetry.initial
	for {
		opened, err := h.relay.exchange(h.ctx, h.currentGrant())
		if err == nil {
			if opened.ETag != h.etag || opened.Path != h.path || opened.Size != h.size {
				return &StatusError{Op: "renew", Status: http.StatusPreconditionFailed}
			}
			h.setGrant(opened.Grant, h.relay.now())
			return nil
		}
		if h.ctx.Err() != nil {
			return context.Cause(h.ctx)
		}
		var statusErr *StatusError
		if errors.As(err, &statusErr) && statusErr.Fatal() {
			return err
		}
		if !h.relay.now().Add(delay).Before(h.grantExpires()) {
			h.relay.logger.Warn("source grant expired while the core was unreachable", "source", h.etag, "error", err)
			return &StatusError{Op: "renew", Status: http.StatusForbidden}
		}
		h.relay.logger.Warn("renew source grant; retrying", "source", h.etag, "retry_in", delay, "error", err)
		if !sleepCtx(h.ctx, delay) {
			return context.Cause(h.ctx)
		}
		delay = h.relay.renewRetry.next(delay)
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
