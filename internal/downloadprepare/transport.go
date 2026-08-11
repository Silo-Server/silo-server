// Package downloadprepare defines the internal API used to execute a prepared
// download artifact on a dedicated transcode node.
package downloadprepare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// Request is the environment-neutral portion of a prepared-file recipe. The
// transcode node supplies its own FFmpeg path, hardware mode, and device list.
type Request struct {
	JobID               string  `json:"job_id"`
	InputPath           string  `json:"input_path"`
	OutputPath          string  `json:"output_path"`
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

// NewRequest freezes the byte-affecting recipe while deliberately omitting
// environment-specific execution settings.
func NewRequest(jobID string, opts playback.TranscodeOpts, outputPath string) Request {
	return Request{
		JobID:               jobID,
		InputPath:           opts.InputPath,
		OutputPath:          outputPath,
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

// RemotePreparer executes one request on a selected transcode node.
type RemotePreparer interface {
	Prepare(ctx context.Context, nodeURL, jwtSecret string, req Request) error
}

// HTTPPreparer implements RemotePreparer over the bearer-protected node API.
// The request context is the only overall timeout because a full-file encode
// may legitimately run for hours; artifact lease loss cancels that context.
type HTTPPreparer struct {
	Client *http.Client
}

func (p HTTPPreparer) Prepare(ctx context.Context, nodeURL, jwtSecret string, req Request) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("remote download prepare: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(nodeURL, "/")+"/downloads/prepare", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("remote download prepare: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+jwtSecret)

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("remote download prepare: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("remote download prepare: node returned %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}
