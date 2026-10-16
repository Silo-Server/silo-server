// Package workerinput keeps source credentials in Go and exposes private,
// seekable loopback inputs to the existing FFmpeg execution engines.
package workerinput

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Source identifies one core-admitted representation. Its renewable grant is
// deliberately separate from stable source identity and revision.
type Source struct {
	ID, Revision, URL, Grant, ETag, WorkID string
	Size                                   int64
	Generation                             uint64
}

type Options struct {
	LocalRoot string
	FFprobe   string
	Origins   []string
	AllowHTTP bool
	Allowed   func(workID string, generation uint64) bool
	Sign      func(*http.Request) error
	Transport http.RoundTripper
	MaxInputs int
	MaxReads  int
}

type Adapter struct {
	options  Options
	client   *http.Client
	listener net.Listener
	server   *http.Server
	mu       sync.RWMutex
	handles  map[string]*Handle
	reads    chan struct{}
	closed   bool
}

// Handle owns a transient input and cancels every in-flight read on release.
// The URL is never a durable recipe identity.
type Handle struct {
	adapter *Adapter
	source  Source
	token   string
	ctx     context.Context //nolint:containedctx // Handle owns cancellation of every read until Close releases its input.
	cancel  context.CancelFunc
	once    sync.Once
}

func New(options Options) (*Adapter, error) {
	if options.Allowed == nil || len(options.Origins) == 0 {
		return nil, errors.New("worker input requires source and work authority")
	}
	for _, origin := range options.Origins {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "https" && (u.Scheme != "http" || !options.AllowHTTP)) {
			return nil, errors.New("invalid approved source origin")
		}
	}
	if options.MaxInputs <= 0 {
		options.MaxInputs = 128
	}
	if options.MaxReads <= 0 {
		options.MaxReads = 32
	}
	transport := options.Transport
	if transport == nil {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, errors.New("worker input requires an HTTP transport")
		}
		t := base.Clone()
		t.Proxy = nil
		t.DisableCompression = true
		t.MaxIdleConnsPerHost = options.MaxReads
		t.MaxConnsPerHost = options.MaxReads
		t.ResponseHeaderTimeout = 10 * time.Second
		transport = t
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	a := &Adapter{options: options, listener: listener, handles: make(map[string]*Handle), reads: make(chan struct{}, options.MaxReads)}
	a.client = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	a.server = &http.Server{Handler: http.HandlerFunc(a.serve), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { _ = a.server.Serve(listener) }()
	return a, nil
}

func (a *Adapter) Open(ctx context.Context, source Source) (*Handle, error) {
	u, err := url.Parse(source.URL)
	if err != nil || u.User != nil || u.Fragment != "" || source.ID == "" || source.Revision == "" || source.Grant == "" || source.ETag == "" || source.Size <= 0 || source.WorkID == "" || source.Generation == 0 {
		return nil, errors.New("invalid source descriptor")
	}
	approved := false
	for _, origin := range a.options.Origins {
		if u.Scheme+"://"+u.Host == origin {
			approved = true
			break
		}
	}
	if !approved || !a.options.Allowed(source.WorkID, source.Generation) {
		return nil, errors.New("source is outside admitted authority")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	inputCtx, cancel := context.WithCancel(ctx)
	h := &Handle{adapter: a, source: source, token: hex.EncodeToString(nonce[:]), ctx: inputCtx, cancel: cancel}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || len(a.handles) >= a.options.MaxInputs {
		cancel()
		return nil, errors.New("worker input quota exceeded")
	}
	a.handles[h.token] = h
	return h, nil
}

func (h *Handle) URL() string {
	return "http://" + h.adapter.listener.Addr().String() + "/input/" + h.token
}
func (h *Handle) Identity() string { return h.source.ID + ":" + h.source.Revision }
func (h *Handle) Size() int64      { return h.source.Size }

// Descriptor returns the immutable admitted source for scoped lease renewal.
func (h *Handle) Descriptor() Source { return h.source }
func (h *Handle) Close() error {
	h.once.Do(func() { h.cancel(); h.adapter.mu.Lock(); delete(h.adapter.handles, h.token); h.adapter.mu.Unlock() })
	return nil
}
func (a *Adapter) Allowed(path string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, h := range a.handles {
		if h.URL() == path && h.ctx.Err() == nil && a.options.Allowed(h.source.WorkID, h.source.Generation) {
			return true
		}
	}
	return false
}
func (a *Adapter) CloseWork(workID string) {
	a.mu.RLock()
	var handles []*Handle
	for _, h := range a.handles {
		if h.source.WorkID == workID {
			handles = append(handles, h)
		}
	}
	a.mu.RUnlock()
	for _, h := range handles {
		_ = h.Close()
	}
}
func (a *Adapter) Close() error {
	a.mu.Lock()
	a.closed = true
	var handles []*Handle
	for _, h := range a.handles {
		handles = append(handles, h)
	}
	a.mu.Unlock()
	for _, h := range handles {
		_ = h.Close()
	}
	a.client.CloseIdleConnections()
	return a.server.Close()
}

func (a *Adapter) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	a.mu.RLock()
	h := a.handles[strings.TrimPrefix(r.URL.Path, "/input/")]
	a.mu.RUnlock()
	if h == nil || !strings.HasPrefix(r.URL.Path, "/input/") {
		http.NotFound(w, r)
		return
	}
	if !a.authorized(h) {
		http.Error(w, "work authority expired", http.StatusForbidden)
		return
	}
	select {
	case a.reads <- struct{}{}:
		defer func() { <-a.reads }()
	case <-r.Context().Done():
		return
	case <-h.ctx.Done():
		w.WriteHeader(http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	cancelDone := make(chan struct{})
	handleDetached := false
	stop := context.AfterFunc(h.ctx, func() { cancel(); close(cancelDone) })
	writeDone := make(chan struct{})
	stopWrite := context.AfterFunc(ctx, func() {
		// Cancellation must also wake a writer held by downstream flow control.
		// Healthy readers have no transfer deadline.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now())
		close(writeDone)
	})
	defer func() {
		if !handleDetached && !stop() {
			<-cancelDone
		}
		if !stopWrite() {
			<-writeDone
		}
		cancel()
	}()
	byteRange := ""
	if r.Method != http.MethodHead {
		byteRange = r.Header.Get("Range")
	}
	started := false
	err := h.transfer(ctx, r.Method, byteRange, w, func(response *http.Response, length int64) {
		w.Header().Set("ETag", h.source.ETag)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		if value := response.Header.Get("Content-Range"); value != "" {
			w.Header().Set("Content-Range", value)
		}
		w.WriteHeader(response.StatusCode)
		started = true
	}, nil)
	if err == nil || r.Context().Err() != nil {
		return
	}
	status := http.StatusServiceUnavailable
	var sourceErr *sourceResponseError
	if errors.As(err, &sourceErr) {
		status = sourceErr.status
		if sourceErr.retire {
			// Retire every source read, while allowing this request to finish
			// its denial response under the original client context.
			if !stop() {
				<-cancelDone
			}
			handleDetached = true
			_ = h.Close()
		}
	}
	if started {
		panic(http.ErrAbortHandler)
	}
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", h.source.Size))
	}
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	http.Error(w, http.StatusText(status), status)
}

func (a *Adapter) authorized(h *Handle) bool {
	return h.ctx.Err() == nil && a.options.Allowed(h.source.WorkID, h.source.Generation)
}

func validateResponse(request *http.Request, response *http.Response, source Source) (int64, error) {
	if response.Header.Get("ETag") != source.ETag || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") {
		return 0, errors.New("source revision or encoding mismatch")
	}
	if request.Header.Get("Range") == "" {
		if response.StatusCode != http.StatusOK || response.ContentLength != source.Size || response.Header.Get("Content-Range") != "" {
			return 0, errors.New("invalid full source response")
		}
		return source.Size, nil
	}
	start, end, err := requestedRange(request.Header.Get("Range"), source.Size)
	if err != nil {
		return 0, err
	}
	expected := fmt.Sprintf("bytes %d-%d/%d", start, end, source.Size)
	if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Range") != expected || response.ContentLength != end-start+1 {
		return 0, errors.New("invalid source range response")
	}
	return end - start + 1, nil
}

func requestedRange(value string, size int64) (int64, int64, error) {
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, errors.New("single byte range required")
	}
	first, last, ok := strings.Cut(strings.TrimPrefix(value, "bytes="), "-")
	if !ok {
		return 0, 0, errors.New("invalid range")
	}
	if first == "" {
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, errors.New("invalid suffix range")
		}
		return max(0, size-n), size - 1, nil
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, errors.New("invalid range start")
	}
	end := size - 1
	if last != "" {
		end, err = strconv.ParseInt(last, 10, 64)
		if err != nil || end < start {
			return 0, 0, errors.New("invalid range end")
		}
		end = min(end, size-1)
	}
	return start, end, nil
}
