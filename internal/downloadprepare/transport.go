// Package downloadprepare defines the internal API used to create and relay a
// prepared download artifact on a dedicated transcode node.
package downloadprepare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

const (
	ArtifactDirectoryName = "download-artifacts"
	RelayReadIdleTimeout  = 2 * time.Minute

	resultToneMapRecipeVersionHeader             = "X-Silo-Tone-Map-Recipe-Version"
	resultToneMapModeHeader                      = "X-Silo-Tone-Map-Mode"
	resultToneMapSourceRevisionFingerprintHeader = "X-Silo-Tone-Map-Source-Revision-Fingerprint"
	resultExecutionFingerprintHeader             = "X-Silo-Download-Execution-Fingerprint"
	resultArtifactSizeHeader                     = "X-Silo-Download-Artifact-Size"
	maxResultAttestationHeaderBytes              = 1024
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
	ArtifactID                 string                 `json:"artifact_id"`
	InputPath                  string                 `json:"input_path"`
	SourceVideoCodec           string                 `json:"source_video_codec,omitempty"`
	SourceVideoProfile         string                 `json:"source_video_profile,omitempty"`
	SourceVideoBitDepth        int                    `json:"source_video_bit_depth,omitempty"`
	SoftwareVideoDecode        bool                   `json:"software_video_decode,omitempty"`
	ToneMapPolicy              tonemap.Policy         `json:"tone_map_policy,omitempty"`
	ToneMapMode                tonemap.Mode           `json:"tone_map_mode,omitempty"`
	ToneMapSourceKind          tonemap.SourceKind     `json:"tone_map_source_kind,omitempty"`
	ToneMapRecipeVersion       string                 `json:"tone_map_recipe_version,omitempty"`
	ToneMapPreflightRequired   bool                   `json:"tone_map_preflight_required,omitempty"`
	ToneMapSourceRevision      tonemap.SourceRevision `json:"tone_map_source_revision,omitzero"`
	ToneMapDVConfigPresent     bool                   `json:"tone_map_dv_config_present,omitempty"`
	ToneMapDVBLCompatIDPresent bool                   `json:"tone_map_dv_bl_compat_id_present,omitempty"`
	ToneMapDVBLPresent         bool                   `json:"tone_map_dv_bl_present,omitempty"`
	ToneMapDVRPUPresent        bool                   `json:"tone_map_dv_rpu_present,omitempty"`
	TargetCodecVideo           string                 `json:"target_codec_video"`
	TargetCodecAudio           string                 `json:"target_codec_audio"`
	TargetAudioChannels        int                    `json:"target_audio_channels,omitempty"`
	TargetAudioBitrateKbps     int                    `json:"target_audio_bitrate_kbps,omitempty"`
	TargetResolution           string                 `json:"target_resolution,omitempty"`
	TargetBitrateKbps          int                    `json:"target_bitrate_kbps,omitempty"`
	AudioTrackIndex            int                    `json:"audio_track_index"`
	// AudioRecipeVersion makes the prepared-file executor contract explicit;
	// older nodes ignore it and therefore cannot return the matching execution
	// fingerprint required by the current caller.
	AudioRecipeVersion string `json:"audio_recipe_version,omitempty"`
	// SourceAudioChannels freezes the selected input stream's probed channel
	// count. Zero is the mixed-version-safe unknown value and never enables gain.
	SourceAudioChannels int     `json:"source_audio_channels,omitempty"`
	TotalDuration       float64 `json:"total_duration,omitempty"`
}

// Result identifies a completed artifact without exposing the node's local
// filesystem layout.
type Result struct {
	ArtifactID                       string       `json:"artifact_id"`
	FileSize                         int64        `json:"file_size"`
	ToneMapRecipeVersion             string       `json:"tone_map_recipe_version,omitempty"`
	ToneMapMode                      tonemap.Mode `json:"tone_map_mode,omitempty"`
	ToneMapSourceRevisionFingerprint string       `json:"tone_map_source_revision_fingerprint,omitempty"`
	ExecutionFingerprint             string       `json:"execution_fingerprint,omitempty"`
}

// SetResultHeaders exposes bounded attestation metadata on artifact status
// responses. Ordinary artifacts add no headers.
func SetResultHeaders(header http.Header, result Result) {
	setBoundedResultHeader(header, resultToneMapRecipeVersionHeader, result.ToneMapRecipeVersion)
	setBoundedResultHeader(header, resultToneMapModeHeader, string(result.ToneMapMode))
	setBoundedResultHeader(header, resultToneMapSourceRevisionFingerprintHeader, result.ToneMapSourceRevisionFingerprint)
	setBoundedResultHeader(header, resultExecutionFingerprintHeader, result.ExecutionFingerprint)
	if result.ExecutionFingerprint != "" && result.FileSize > 0 {
		header.Set(resultArtifactSizeHeader, strconv.FormatInt(result.FileSize, 10))
	}
}

func setBoundedResultHeader(header http.Header, name, value string) {
	if value == "" || len(value) > maxResultAttestationHeaderBytes || strings.ContainsAny(value, "\r\n") {
		return
	}
	header.Set(name, value)
}

func resultAttestationFromHeaders(header http.Header) (Result, error) {
	values := []struct{ name string }{
		{name: resultToneMapRecipeVersionHeader},
		{name: resultToneMapModeHeader},
		{name: resultToneMapSourceRevisionFingerprintHeader},
		{name: resultExecutionFingerprintHeader},
	}
	decoded := make([]string, len(values))
	for i := range values {
		value := header.Get(values[i].name)
		if len(value) > maxResultAttestationHeaderBytes {
			return Result{}, fmt.Errorf("invalid %s header", values[i].name)
		}
		decoded[i] = value
	}
	fileSize := int64(0)
	if raw := header.Get(resultArtifactSizeHeader); raw != "" {
		var err error
		fileSize, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || fileSize <= 0 {
			return Result{}, fmt.Errorf("invalid %s header", resultArtifactSizeHeader)
		}
	}
	return Result{
		FileSize:                         fileSize,
		ToneMapRecipeVersion:             decoded[0],
		ToneMapMode:                      tonemap.Mode(decoded[1]),
		ToneMapSourceRevisionFingerprint: decoded[2],
		ExecutionFingerprint:             decoded[3],
	}, nil
}

// ResultFromHeaders decodes the bounded receipt attestation returned by a node.
func ResultFromHeaders(header http.Header) (Result, error) {
	return resultAttestationFromHeaders(header)
}

func ValidArtifactID(id string) bool { return artifactIDPattern.MatchString(id) }

// ToneMapRequested reports whether any transported field claims that the
// request carries a tone-map recipe, including partial recipes that must fail.
func (r Request) ToneMapRequested() bool {
	return (r.ToneMapPolicy != "" && r.ToneMapPolicy != tonemap.PolicyNone) ||
		r.ToneMapMode != "" || r.ToneMapSourceKind != "" || r.ToneMapRecipeVersion != "" ||
		r.ToneMapPreflightRequired || !r.ToneMapSourceRevision.IsZero() ||
		r.ToneMapDVConfigPresent || r.ToneMapDVBLCompatIDPresent || r.ToneMapDVBLPresent || r.ToneMapDVRPUPresent
}

// AudioRecipeRequested includes incomplete requests so the node can reject a
// partial recipe instead of treating it as an ordinary encode.
func (r Request) AudioRecipeRequested() bool {
	return r.AudioRecipeVersion != "" || r.SourceAudioChannels != 0
}

// StereoDownmixBoostRequested reports whether this is the complete,
// source-sensitive audio_to_aac recipe. Prepared encoded audio uses the
// historical stereo default, so only a known surround source qualifies.
func (r Request) StereoDownmixBoostRequested() bool {
	return r.AudioRecipeVersion == playback.TransformationAudioToAACRecipeVersionV3 &&
		playback.IsAudioToAACStereoDownmixV3(r.SourceAudioChannels, r.TargetCodecAudio, r.TargetAudioChannels)
}

// ExecutionAttestationRequested reports whether accepting bytes requires a
// receipt from a node that understood all newly transported recipe fields.
// Explicit audio output settings affect bytes even when the v2 boost does not.
func (r Request) ExecutionAttestationRequested() bool {
	return r.ToneMapRequested() || r.AudioRecipeRequested() ||
		r.TargetAudioChannels != 0 || r.TargetAudioBitrateKbps != 0
}

// ValidToneMapAttestation reports whether a requested recipe carries every
// frozen field needed to compare a node's artifact receipt exactly.
func (r Request) ValidToneMapAttestation() bool {
	return r.ToneMapRequested() && r.ToneMapMode != "" &&
		r.ToneMapRecipeVersion == playback.TransformationHDRToSDRToneMapRecipeVersionV3 &&
		!r.ToneMapSourceRevision.IsZero() && r.ToneMapPolicy.Allows(r.ToneMapMode) &&
		tonemap.ValidSourceKind(r.ToneMapSourceKind)
}

// ExecutionFingerprint identifies every transported byte-affecting field while
// deliberately excluding the idempotency handle.
func (r Request) ExecutionFingerprint() string {
	r.ArtifactID = ""
	data, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// NewRequest freezes the byte-affecting recipe while deliberately omitting
// environment-specific execution settings.
func NewRequest(artifactID string, opts playback.TranscodeOpts) Request {
	request := Request{
		ArtifactID:                 artifactID,
		InputPath:                  opts.InputPath,
		SourceVideoCodec:           opts.SourceVideoCodec,
		SourceVideoProfile:         opts.SourceVideoProfile,
		SourceVideoBitDepth:        opts.SourceVideoBitDepth,
		SoftwareVideoDecode:        opts.SoftwareVideoDecode,
		ToneMapPolicy:              opts.ToneMapPolicy,
		ToneMapMode:                opts.ToneMapMode,
		ToneMapSourceKind:          opts.ToneMapSourceKind,
		ToneMapRecipeVersion:       opts.ToneMapRecipeVersion,
		ToneMapPreflightRequired:   opts.ToneMapPreflightRequired,
		ToneMapSourceRevision:      opts.ToneMapSourceRevision,
		ToneMapDVConfigPresent:     opts.ToneMapDVConfigPresent,
		ToneMapDVBLCompatIDPresent: opts.ToneMapDVBLCompatIDPresent,
		ToneMapDVBLPresent:         opts.ToneMapDVBLPresent,
		ToneMapDVRPUPresent:        opts.ToneMapDVRPUPresent,
		TargetCodecVideo:           opts.TargetCodecVideo,
		TargetCodecAudio:           opts.TargetCodecAudio,
		TargetAudioChannels:        opts.TargetAudioChannels,
		TargetAudioBitrateKbps:     opts.TargetAudioBitrateKbps,
		TargetResolution:           opts.TargetResolution,
		TargetBitrateKbps:          opts.TargetBitrateKbps,
		AudioTrackIndex:            opts.AudioTrackIndex,
		TotalDuration:              opts.TotalDuration,
	}
	if playback.IsAudioToAACStereoDownmixV3(opts.SourceAudioChannels, request.TargetCodecAudio, request.TargetAudioChannels) {
		request.SourceAudioChannels = opts.SourceAudioChannels
		request.AudioRecipeVersion = playback.TransformationAudioToAACRecipeVersionV3
	}
	return request
}

// TranscodeOpts reconstructs the prepared-file options using the selected
// node's live execution settings.
func (r Request) TranscodeOpts(ffmpegPath, hwAccel, hwDevice string, sink playback.FFmpegLogSink) playback.TranscodeOpts {
	return playback.TranscodeOpts{
		InputPath:                  r.InputPath,
		SourceVideoCodec:           r.SourceVideoCodec,
		SourceVideoProfile:         r.SourceVideoProfile,
		SourceVideoBitDepth:        r.SourceVideoBitDepth,
		SoftwareVideoDecode:        r.SoftwareVideoDecode,
		ToneMapPolicy:              r.ToneMapPolicy,
		ToneMapMode:                r.ToneMapMode,
		ToneMapSourceKind:          r.ToneMapSourceKind,
		ToneMapRecipeVersion:       r.ToneMapRecipeVersion,
		ToneMapPreflightRequired:   r.ToneMapPreflightRequired,
		ToneMapSourceRevision:      r.ToneMapSourceRevision,
		ToneMapDVConfigPresent:     r.ToneMapDVConfigPresent,
		ToneMapDVBLCompatIDPresent: r.ToneMapDVBLCompatIDPresent,
		ToneMapDVBLPresent:         r.ToneMapDVBLPresent,
		ToneMapDVRPUPresent:        r.ToneMapDVRPUPresent,
		TargetCodecVideo:           r.TargetCodecVideo,
		TargetCodecAudio:           r.TargetCodecAudio,
		TargetAudioChannels:        r.TargetAudioChannels,
		TargetAudioBitrateKbps:     r.TargetAudioBitrateKbps,
		TargetResolution:           r.TargetResolution,
		TargetBitrateKbps:          r.TargetBitrateKbps,
		AudioTrackIndex:            r.AudioTrackIndex,
		SourceAudioChannels:        r.SourceAudioChannels,
		SubtitleTrackIndex:         -1,
		FFmpegPath:                 ffmpegPath,
		HWAccel:                    hwAccel,
		HWDevice:                   hwDevice,
		TotalDuration:              r.TotalDuration,
		NodeType:                   "transcode",
		ExecutionMode:              "download_prepare",
		FFmpegLogSink:              sink,
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
	attestation, err := resultAttestationFromHeaders(resp.Header)
	if err != nil {
		return Result{}, fmt.Errorf("remote download artifact stat: %w", err)
	}
	attestation.ArtifactID = artifactID
	if attestation.FileSize == 0 {
		attestation.FileSize = size
	}
	return attestation, nil
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
