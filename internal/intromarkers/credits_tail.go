package intromarkers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// ArtifactKindCreditsTail is the keyframe statistics and silences of an
// episode's tail window, which credits detection classifies for end-credit
// visuals.
const ArtifactKindCreditsTail = "credits_tail"

// The tail pass: every video keyframe in the tail window, cropped to the
// center 90 by 80 percent (dropping letterbox bars and corner logos, but not
// corner credits), scaled to 480 pixels wide, with the share of pixels below
// each creditsBlackThresholds level; and the silences in its audio.
const (
	tailCropWidth       = 0.9
	tailCropHeight      = 0.8
	tailWidth           = 480
	tailSilenceNoiseDB  = -50
	tailSilenceSeconds  = 0.5
	maxTailKeyframes    = 5000
	tailKeyframeSeconds = 30.0
)

// Why a tail cannot be analyzed, stored as the unusable artifact's detail.
// Permanent ffmpeg failures store their mediasample.Reason instead.
const (
	tailDetailNoVideo          = "no_video"
	tailDetailUnsupportedCodec = "unsupported_codec"
	tailDetailTooManyKeyframes = "too_many_keyframes"
	tailDetailSparse           = "sparse"
)

// allIntraVideoCodecs make every frame a keyframe, so a keyframes-only pass
// would decode the whole tail. Their files are mostly masters and captures.
var allIntraVideoCodecs = map[string]struct{}{
	"prores": {}, "mjpeg": {}, "dnxhd": {}, "ffv1": {}, "rawvideo": {}, "v210": {}, "utvideo": {}, "huffyuv": {},
}

// creditsTailParams are the parameters that shape a credits tail payload.
// Changing them discards every cached credits tail.
var creditsTailParams = fmt.Sprintf("tail=%.0f:%.2f;crop=%.2fx%.2f;width=%d;black=%s;silence=%d:%.2f;keyframes;format=%s",
	episodeCreditsTailSeconds, episodeCreditsTailFraction, tailCropWidth, tailCropHeight, tailWidth,
	joinInts(creditsBlackThresholds),
	tailSilenceNoiseDB, tailSilenceSeconds, creditsTailFormat)

// creditsTailKey keys an episode's cached tail pass.
func creditsTailKey() ArtifactKey {
	return ArtifactKey{
		Kind:             ArtifactKindCreditsTail,
		AlgorithmVersion: AlgorithmVersion,
		ConfigHash:       ArtifactConfigHash(ArtifactKindCreditsTail, creditsTailParams),
	}
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ",")
}

func (c Candidate) hasVideo() bool { return strings.TrimSpace(c.CodecVideo) != "" }

func (c Candidate) hasAudio() bool { return strings.TrimSpace(c.CodecAudio) != "" }

// metadataTailDetail reports whether an unusable tail's detail came from
// tailUnusableBeforeSampling rather than from decoding the tail.
func metadataTailDetail(detail string) bool {
	return detail == tailDetailNoVideo || detail == tailDetailUnsupportedCodec
}

// tailUnusableBeforeSampling returns why the candidate's tail cannot be
// analyzed without decoding it, or "". It reads probe metadata, so callers
// decide it again on every analysis instead of storing it.
func tailUnusableBeforeSampling(candidate Candidate) string {
	if !candidate.hasVideo() {
		return tailDetailNoVideo
	}
	if _, ok := allIntraVideoCodecs[strings.ToLower(strings.TrimSpace(candidate.CodecVideo))]; ok {
		return tailDetailUnsupportedCodec
	}
	return ""
}

// tailUnusableAfterSampling returns why a sampled tail cannot be classified,
// or "". Too many keyframes means the codec is effectively all-intra; too few
// cannot show where credits start.
func tailUnusableAfterSampling(frames int, window fingerprintWindow) string {
	switch {
	case frames > maxTailKeyframes:
		return tailDetailTooManyKeyframes
	case float64(frames)*tailKeyframeSeconds < window.duration():
		return tailDetailSparse
	}
	return ""
}

// creditsTailRequest is the one sampling run over a candidate's tail window:
// its video keyframes' statistics and, when the file has audio, its silences
// and, when asked, its Chromaprint fingerprint. Audio is read in full either
// way, so adding video costs decode time but no extra reads.
func creditsTailRequest(ctx context.Context, candidate Candidate, window fingerprintWindow, fingerprint bool) mediasample.Request {
	hasVideo := candidate.hasVideo()
	req := mediasample.Request{
		Input:      candidate.FilePath,
		Window:     &mediasample.Window{StartSeconds: window.Start, DurationSeconds: window.duration(), KeyframesOnly: hasVideo},
		Threads:    1,
		Background: backgroundAnalysis(ctx),
	}
	if hasVideo {
		req.Stats = &mediasample.StatsOutput{
			CropWidth:       tailCropWidth,
			CropHeight:      tailCropHeight,
			Width:           tailWidth,
			BlackThresholds: creditsBlackThresholds,
		}
		req.VideoBitDepth = mediasample.VideoBitDepthHint(candidate.VideoBitDepth)
	}
	if candidate.hasAudio() {
		req.Audio = &mediasample.AudioOutput{
			Fingerprint: fingerprint,
			Silence:     &mediasample.SilenceParams{NoiseDB: tailSilenceNoiseDB, MinSeconds: tailSilenceSeconds},
		}
	}
	return req
}

// creditsTailSample is what one tail pass produced.
type creditsTailSample struct {
	Tail creditsTail
	// Fingerprint is set when the pass was asked for one; its Points are
	// empty when the tail has no audio to fingerprint.
	Fingerprint *Fingerprint
}

// creditsTailSampler runs tail passes. ChromaprintExtractor implements it.
type creditsTailSampler interface {
	// PreflightCreditsTail reports what the ffmpeg binary lacks for a tail
	// pass with a fingerprint.
	PreflightCreditsTail(ctx context.Context) error
	// SampleCreditsTail runs the tail pass over a candidate with video, with
	// its Chromaprint fingerprint when fingerprint is set.
	SampleCreditsTail(ctx context.Context, candidate Candidate, fingerprint bool) (creditsTailSample, error)
}

// tailPreflightCandidate stands for a file with video and audio, whose tail
// pass needs every output.
var tailPreflightCandidate = Candidate{CodecVideo: "video", CodecAudio: "audio"}

// PreflightCreditsTail reports what ffmpeg lacks for a tail pass.
func (e *ChromaprintExtractor) PreflightCreditsTail(ctx context.Context) error {
	caps, err := mediasample.LoadCapabilities(ctx, e.config.FFmpegPath)
	if err != nil {
		return err
	}
	return caps.Require(creditsTailRequest(ctx, tailPreflightCandidate, fingerprintWindow{End: 1}, true))
}

// SampleCreditsTail runs the tail pass over the candidate's tail window.
func (e *ChromaprintExtractor) SampleCreditsTail(ctx context.Context, candidate Candidate, fingerprint bool) (creditsTailSample, error) {
	window := tailWindow(candidate)
	if window.empty() {
		return creditsTailSample{}, fmt.Errorf("file %d has no tail window", candidate.FileID)
	}
	req := creditsTailRequest(ctx, candidate, window, fingerprint)
	runner := e.tailRunner(ctx, &req)
	result, err := runner.Run(ctx, req)
	if err != nil && req.Audio != nil && req.Stats != nil && mediasample.Classify(err) == mediasample.ReasonNoStream {
		// ffmpeg fails the whole run when any output lacks its stream, and
		// probe metadata can name audio the file cannot give. Run the video
		// alone: when that succeeds, the audio was missing and the tail has
		// no silences or fingerprint.
		req.Audio = nil
		result, err = runner.Run(ctx, req)
	}
	if err != nil {
		return creditsTailSample{}, fmt.Errorf("sampling the credits tail of file %d: %w", candidate.FileID, err)
	}
	e.logTailDecoder(ctx, candidate, req, result)
	sample := creditsTailSample{Tail: creditsTail{Frames: result.Frames, Silences: result.Silences}}
	if fingerprint && candidate.hasAudio() {
		key := creditsFingerprintKey()
		sample.Fingerprint = &Fingerprint{
			MediaFileID:           candidate.FileID,
			FileHash:              candidate.FileHash,
			FileSize:              candidate.FileSize,
			DurationSeconds:       candidate.DurationSeconds,
			WindowStartSeconds:    window.Start,
			WindowEndSeconds:      window.End,
			AlgorithmVersion:      key.AlgorithmVersion,
			ConfigHash:            key.ConfigHash,
			FingerprintFormat:     ChromaprintFormat,
			SampleDurationSeconds: float64(len(result.Fingerprint)) * DefaultPointHopSeconds,
			Points:                result.Fingerprint,
		}
	}
	return sample, nil
}
