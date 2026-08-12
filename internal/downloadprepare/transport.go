// Package downloadprepare defines the internal API used to create and relay a
// prepared download artifact on a dedicated transcode node.
package downloadprepare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	ArtifactDirectoryName = "download-artifacts"
	RelayReadIdleTimeout  = 2 * time.Minute
)

var (
	artifactIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	ErrArtifactNotFound = errors.New("remote download artifact not found")
	ErrRelayReadIdle    = errors.New("remote download artifact read stalled")
)

func newHTTPClient(responseHeaderTimeout time.Duration) *http.Client {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		baseTransport = &http.Transport{}
	}
	transport := baseTransport.Clone()
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

var (
	// Preparing a full file can legitimately take hours. Its lease-bound request
	// context supplies cancellation, so imposing a response-header timeout here
	// would abort every encode longer than that timeout before the node replies.
	defaultPrepareHTTPClient = newHTTPClient(0)
	defaultHTTPClient        = newHTTPClient(60 * time.Second)
)

// Request is the environment-neutral portion of a prepared-file recipe. The
// transcode node supplies its own FFmpeg path, hardware mode, device list, and
// output path. ArtifactID is an opaque handle, never a caller-selected path.
type Request struct {
	ArtifactID          string  `json:"artifact_id"`
	InputPath           string  `json:"input_path"`
	SourceVideoCodec    string  `json:"source_video_codec,omitempty"`
	SourceVideoProfile  string  `json:"source_video_profile,omitempty"`
	SourceVideoBitDepth int     `json:"source_video_bit_depth,omitempty"`
	SoftwareVideoDecode bool    `json:"software_video_decode,omitempty"`
	TargetCodecVideo    string  `json:"target_codec_video"`
	TargetCodecAudio    string  `json:"target_codec_audio"`
	TargetResolution    string  `json:"target_resolution,omitempty"`
	TargetBitrateKbps   int     `json:"target_bitrate_kbps,omitempty"`
	AudioTrackIndex     int     `json:"audio_track_index"`
	TotalDuration       float64 `json:"total_duration,omitempty"`
}

// Result identifies a completed artifact without exposing the node's local
// filesystem layout.
type Result struct {
	ArtifactID string `json:"artifact_id"`
	FileSize   int64  `json:"file_size"`
}

func ValidArtifactID(id string) bool { return artifactIDPattern.MatchString(id) }

// NewRequest freezes the byte-affecting recipe while deliberately omitting
// environment-specific execution settings.
func NewRequest(artifactID string, opts playback.TranscodeOpts) Request {
	return Request{
		ArtifactID:          artifactID,
		InputPath:           opts.InputPath,
		SourceVideoCodec:    opts.SourceVideoCodec,
		SourceVideoProfile:  opts.SourceVideoProfile,
		SourceVideoBitDepth: opts.SourceVideoBitDepth,
		SoftwareVideoDecode: opts.SoftwareVideoDecode,
		TargetCodecVideo:    opts.TargetCodecVideo,
		TargetCodecAudio:    opts.TargetCodecAudio,
		TargetResolution:    opts.TargetResolution,
		TargetBitrateKbps:   opts.TargetBitrateKbps,
		AudioTrackIndex:     opts.AudioTrackIndex,
		TotalDuration:       opts.TotalDuration,
	}
}

// TranscodeOpts reconstructs the prepared-file options using the selected
// node's live execution settings.
func (r Request) TranscodeOpts(ffmpegPath, hwAccel, hwDevice string, sink playback.FFmpegLogSink) playback.TranscodeOpts {
	return playback.TranscodeOpts{
		InputPath:           r.InputPath,
		SourceVideoCodec:    r.SourceVideoCodec,
		SourceVideoProfile:  r.SourceVideoProfile,
		SourceVideoBitDepth: r.SourceVideoBitDepth,
		SoftwareVideoDecode: r.SoftwareVideoDecode,
		TargetCodecVideo:    r.TargetCodecVideo,
		TargetCodecAudio:    r.TargetCodecAudio,
		TargetResolution:    r.TargetResolution,
		TargetBitrateKbps:   r.TargetBitrateKbps,
		AudioTrackIndex:     r.AudioTrackIndex,
		SubtitleTrackIndex:  -1,
		FFmpegPath:          ffmpegPath,
		HWAccel:             hwAccel,
		HWDevice:            hwDevice,
		TotalDuration:       r.TotalDuration,
		NodeType:            "transcode",
		ExecutionMode:       "download_prepare",
		FFmpegLogSink:       sink,
	}
}

// RemotePreparer executes and manages artifacts on a selected transcode node.
type RemotePreparer interface {
	Prepare(ctx context.Context, nodeURL, jwtSecret string, req Request) (Result, error)
	Stat(ctx context.Context, nodeURL, jwtSecret, artifactID string) (Result, error)
	Delete(ctx context.Context, nodeURL, jwtSecret, artifactID string) error
}

// HTTPPreparer implements RemotePreparer over bearer-protected node APIs. A
// full-file prepare has no transport timeout; its caller owns cancellation.
// Metadata and relay operations use a bounded response-header wait by default.
type HTTPPreparer struct {
	Client          *http.Client
	ReadIdleTimeout time.Duration
}

func (p HTTPPreparer) Prepare(ctx context.Context, nodeURL, jwtSecret string, req Request) (Result, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, fmt.Errorf("remote download prepare: marshal request: %w", err)
	}
	responseCtx, cancel := context.WithCancel(ctx)
	httpReq, err := p.request(responseCtx, http.MethodPost, nodeURL, jwtSecret, "/downloads/prepare", bytes.NewReader(body))
	if err != nil {
		cancel()
		return Result{}, fmt.Errorf("remote download prepare: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.prepareClient().Do(httpReq)
	if err != nil {
		cancel()
		return Result{}, fmt.Errorf("remote download prepare: request: %w", err)
	}
	// The encode itself remains unbounded while waiting for response headers, but
	// once the node starts its small JSON result body, each read must make progress.
	resp.Body = newIdleReadCloser(resp.Body, p.readIdleTimeout(), cancel)
	defer func() { _ = resp.Body.Close() }()
	return decodeResult(resp, "remote download prepare")
}

func (p HTTPPreparer) Stat(ctx context.Context, nodeURL, jwtSecret, artifactID string) (Result, error) {
	if !ValidArtifactID(artifactID) {
		return Result{}, fmt.Errorf("remote download artifact stat: invalid artifact id")
	}
	httpReq, err := p.request(ctx, http.MethodHead, nodeURL, jwtSecret, "/downloads/artifacts/"+url.PathEscape(artifactID), nil)
	if err != nil {
		return Result{}, fmt.Errorf("remote download artifact stat: %w", err)
	}
	resp, err := p.client().Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("remote download artifact stat: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return Result{}, ErrArtifactNotFound
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Result{}, responseError(resp, "remote download artifact stat")
	}
	size, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if err != nil || size < 0 {
		return Result{}, fmt.Errorf("remote download artifact stat: invalid content length")
	}
	return Result{ArtifactID: artifactID, FileSize: size}, nil
}

func (p HTTPPreparer) Delete(ctx context.Context, nodeURL, jwtSecret, artifactID string) error {
	if !ValidArtifactID(artifactID) {
		return fmt.Errorf("remote download artifact delete: invalid artifact id")
	}
	deleteCtx, cancel := context.WithCancel(ctx)
	httpReq, err := p.request(deleteCtx, http.MethodDelete, nodeURL, jwtSecret, "/downloads/artifacts/"+url.PathEscape(artifactID), nil)
	if err != nil {
		cancel()
		return fmt.Errorf("remote download artifact delete: %w", err)
	}
	resp, err := p.client().Do(httpReq)
	if err != nil {
		cancel()
		return fmt.Errorf("remote download artifact delete: request: %w", err)
	}
	resp.Body = newIdleReadCloser(resp.Body, p.readIdleTimeout(), cancel)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusNoContent {
		return responseError(resp, "remote download artifact delete")
	}
	return nil
}

// Open returns an authenticated streaming response for a GET or HEAD relay.
// The caller owns closing the body and copying only safe response headers.
func (p HTTPPreparer) Open(ctx context.Context, nodeURL, jwtSecret, artifactID, method string, sourceHeader http.Header) (*http.Response, error) {
	if !ValidArtifactID(artifactID) {
		return nil, fmt.Errorf("remote download artifact open: invalid artifact id")
	}
	if method != http.MethodGet && method != http.MethodHead {
		return nil, fmt.Errorf("remote download artifact open: unsupported method %s", method)
	}
	relayCtx, cancel := context.WithCancel(ctx)
	httpReq, err := p.request(relayCtx, method, nodeURL, jwtSecret, "/downloads/artifacts/"+url.PathEscape(artifactID), nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("remote download artifact open: %w", err)
	}
	for _, name := range []string{"Range", "If-Match", "If-Range", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		if value := sourceHeader.Get(name); value != "" {
			httpReq.Header.Set(name, value)
		}
	}
	resp, err := p.client().Do(httpReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("remote download artifact open: request: %w", err)
	}
	resp.Body = newIdleReadCloser(resp.Body, p.readIdleTimeout(), cancel)
	return resp, nil
}

// idleReadCloser bounds only time spent blocked in an upstream Read. Time
// between reads is deliberately ignored so local bandwidth throttling and slow
// clients cannot be mistaken for a stalled origin.
type idleReadCloser struct {
	source    io.ReadCloser
	timeout   time.Duration
	readStart chan struct{}
	readDone  chan struct{}
	stop      chan struct{}
	stopped   chan struct{}
	closeOnce sync.Once
	timedOut  atomic.Bool
	cancel    context.CancelFunc
}

func newIdleReadCloser(source io.ReadCloser, timeout time.Duration, cancel context.CancelFunc) io.ReadCloser {
	if source == nil || timeout <= 0 {
		return source
	}
	r := &idleReadCloser{
		source:    source,
		timeout:   timeout,
		readStart: make(chan struct{}),
		readDone:  make(chan struct{}),
		stop:      make(chan struct{}),
		stopped:   make(chan struct{}),
		cancel:    cancel,
	}
	go r.watch()
	return r
}

func (r *idleReadCloser) Read(p []byte) (int, error) {
	if r.timedOut.Load() {
		return 0, ErrRelayReadIdle
	}
	select {
	case r.readStart <- struct{}{}:
	case <-r.stop:
		return 0, io.ErrClosedPipe
	}
	n, err := r.source.Read(p)
	select {
	case r.readDone <- struct{}{}:
	case <-r.stop:
	}
	if r.timedOut.Load() {
		return n, ErrRelayReadIdle
	}
	return n, err
}

func (r *idleReadCloser) Close() error {
	r.closeOnce.Do(func() {
		close(r.stop)
		if r.cancel != nil {
			r.cancel()
		}
		_ = r.source.Close()
		<-r.stopped
	})
	return nil
}

func (r *idleReadCloser) watch() {
	defer close(r.stopped)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	reading := false
	for {
		select {
		case <-r.readStart:
			timer.Reset(r.timeout)
			reading = true
		case <-r.readDone:
			if reading && !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			reading = false
		case <-timer.C:
			if reading {
				r.timedOut.Store(true)
				if r.cancel != nil {
					r.cancel()
				}
				_ = r.source.Close()
				reading = false
			}
		case <-r.stop:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func (p HTTPPreparer) request(ctx context.Context, method, nodeURL, jwtSecret, path string, body io.Reader) (*http.Request, error) {
	base, err := url.Parse(strings.TrimSpace(nodeURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("invalid node URL")
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawQuery = ""
	base.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, method, base.String(), body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtSecret)
	return req, nil
}

func (p HTTPPreparer) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return defaultHTTPClient
}

func (p HTTPPreparer) prepareClient() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return defaultPrepareHTTPClient
}

func (p HTTPPreparer) readIdleTimeout() time.Duration {
	if p.ReadIdleTimeout > 0 {
		return p.ReadIdleTimeout
	}
	return RelayReadIdleTimeout
}

// CopyResponseHeaders forwards only representation/range metadata that is safe
// across the transcode-node relay boundary.
func CopyResponseHeaders(dst, src http.Header) {
	for _, name := range []string{
		"Accept-Ranges", "Content-Length", "Content-Range",
		"Content-Type", "ETag", "Last-Modified",
	} {
		if value := src.Get(name); value != "" {
			dst.Set(name, value)
		}
	}
}

// RelayStatusAllowed identifies the complete set of normal ServeContent
// outcomes that an internal relay must preserve unchanged.
func RelayStatusAllowed(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices ||
		status == http.StatusNotModified ||
		status == http.StatusPreconditionFailed ||
		status == http.StatusRequestedRangeNotSatisfiable
}

func decodeResult(resp *http.Response, operation string) (Result, error) {
	if resp.StatusCode == http.StatusNotFound {
		return Result{}, ErrArtifactNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, responseError(resp, operation)
	}
	var result Result
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return Result{}, fmt.Errorf("%s: decode response: %w", operation, err)
	}
	if !ValidArtifactID(result.ArtifactID) || result.FileSize < 0 {
		return Result{}, fmt.Errorf("%s: invalid response", operation)
	}
	return result, nil
}

func responseError(resp *http.Response, operation string) error {
	message, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("%s: read node error response: %w", operation, err)
	}
	return fmt.Errorf("%s: node returned %d: %s", operation, resp.StatusCode, strings.TrimSpace(string(message)))
}
