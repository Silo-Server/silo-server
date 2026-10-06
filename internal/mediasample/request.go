package mediasample

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Request describes one analysis decode. It is plain data so that it can be
// logged, hashed, or sent to another node as JSON; the Runner, not the caller,
// turns it into ffmpeg arguments.
//
// A request names exactly one sampling mode (Window, Samples, or At) and at
// least one output (Audio, Stats, or both; Samples takes only Stats or
// Sheets, and At only Images).
type Request struct {
	// Input is the media file to decode.
	Input string `json:"input"`
	// Window samples a contiguous span of the file.
	Window *Window `json:"window,omitempty"`
	// Samples decodes one video keyframe for each of a list of times.
	Samples *Samples `json:"samples,omitempty"`
	// At decodes the single video frame at a time.
	At *At `json:"at,omitempty"`
	// Audio asks for audio features of the sampled span.
	Audio *AudioOutput `json:"audio,omitempty"`
	// Stats asks for per-frame picture statistics of the sampled span's first
	// video stream.
	Stats *StatsOutput `json:"stats,omitempty"`
	// Images asks for the sampled frames as JPEG images.
	Images *ImageOutput `json:"images,omitempty"`
	// Sheets asks for the sampled frames tiled into JPEG sprite sheets.
	Sheets *SheetsOutput `json:"sheets,omitempty"`
	// Attempts are tried in order until one succeeds. Empty means a single
	// software attempt bounded only by the caller's context.
	Attempts []Attempt `json:"attempts,omitempty"`
	// Threads caps ffmpeg's decoder threads. Zero leaves ffmpeg's default.
	Threads int `json:"threads,omitempty"`
	// VideoBitDepth is the bit depth of the input's first video stream, zero
	// when unknown. A hardware attempt on VideoToolbox needs it to download
	// the decoded surfaces, whose format follows the source's depth; an
	// unknown depth is taken as 8 bits. Validate accepts 0..16; callers pass
	// a probed depth through VideoBitDepthHint so an implausible probe value
	// becomes unknown instead of invalidating the request.
	VideoBitDepth int `json:"video_bit_depth,omitempty"`
	// Background marks work nobody is waiting on. On Linux its ffmpeg runs at
	// the lowest CPU priority (nice 19) and in the idle I/O class; elsewhere it
	// runs like any other request.
	Background bool `json:"background,omitempty"`
}

// Window is a contiguous span of the input, in media seconds.
type Window struct {
	StartSeconds    float64 `json:"start_seconds"`
	DurationSeconds float64 `json:"duration_seconds"`
	// KeyframesOnly decodes only video keyframes. It needs a video output
	// (Stats); audio outputs still read every audio frame.
	KeyframesOnly bool `json:"keyframes_only,omitempty"`
}

// Samples decodes, for each time, the video keyframe at or before it, and
// nothing between. Each frame reports the time it was sampled for rather than
// its own, which lies up to one keyframe interval earlier. Times count from
// the start of the file, as window starts do. A keyframe that serves several
// times is reported once per time. Only Stats or Sheets may be asked of
// samples. Containers without a keyframe index, such as MPEG-TS, are read
// whole over the sampled span, keyframes only (see probe.go).
type Samples struct {
	// Seconds are the media times to sample, finite, non-negative, and
	// strictly increasing.
	Seconds []float64 `json:"seconds"`
	// ReadThrough reads the sampled span as one keyframes-only window, as
	// containers without a keyframe index always are, instead of seeking to
	// each sample. It decodes every keyframe in the span but reads the file
	// once, front to back, which can cost less than a seek per sample when
	// samples lie closer together than the keyframes do.
	ReadThrough bool `json:"read_through,omitempty"`
}

// At decodes the first video frame at or after a media time, decoding from
// the keyframe before it (ffmpeg's accurate input seek). Only Images may be
// asked of it.
type At struct {
	// Seconds is the media time, finite and non-negative.
	Seconds float64 `json:"seconds"`
}

// AudioOutput selects audio features. Fingerprint and Silence may be combined
// in one run; Speech takes the run to itself.
type AudioOutput struct {
	// Fingerprint returns raw Chromaprint points for the sampled audio.
	Fingerprint bool `json:"fingerprint,omitempty"`
	// Silence returns the silences silencedetect finds, in absolute media
	// seconds.
	Silence *SilenceParams `json:"silence,omitempty"`
	// Speech returns the speech-band level of one audio stream every 10 ms
	// (see speech.go). It needs a window and no other output.
	Speech *SpeechParams `json:"speech,omitempty"`
}

// SilenceParams configures silence detection.
type SilenceParams struct {
	// NoiseDB is the level, in dB, at or below which audio counts as silent.
	NoiseDB int `json:"noise_db"`
	// MinSeconds is the shortest silence reported.
	MinSeconds float64 `json:"min_seconds"`
}

// StatsOutput selects per-frame picture statistics. Each frame is cropped to
// its center, scaled down, and converted to 8-bit 4:2:0 before it is
// measured, so statistics compare across sources of any size and depth.
type StatsOutput struct {
	// CropWidth and CropHeight are the centered share of the picture kept,
	// in (0, 1]. Cropping drops letterbox bars and corner logos.
	CropWidth  float64 `json:"crop_width"`
	CropHeight float64 `json:"crop_height"`
	// Width is the width, in pixels, the cropped picture is scaled to; the
	// height keeps the aspect ratio. It must be even.
	Width int `json:"width"`
	// BlackThresholds are luma levels. For each one, a frame reports the
	// percentage of its pixels darker than the level (FrameStats.PBlack, in
	// this order).
	BlackThresholds []int `json:"black_thresholds,omitempty"`
}

// Attempt is one decode attempt.
type Attempt struct {
	// Hardware decodes video on the Runner's configured hardware (see
	// hwdecode.go). It needs a video output (Images, Stats, or Sheets); audio
	// always decodes in software.
	Hardware bool `json:"hardware,omitempty"`
	// TimeoutSeconds bounds the attempt. Zero means only the caller's context
	// bounds it.
	TimeoutSeconds float64 `json:"timeout_seconds,omitempty"`
}

// Bounds Validate enforces. They reject nonsense rather than tune anything.
const (
	maxThreads        = 64
	maxAttempts       = 4
	maxAttemptSeconds = 24 * 60 * 60
	minSilenceNoiseDB = -200
	maxSilenceSeconds = 3600
	maxStatsWidth     = 3840
	maxBlackLevels    = 8
	maxSamples        = 10000
	maxVideoBitDepth  = 16
)

// VideoBitDepthHint returns a probed video bit depth as a Request's
// VideoBitDepth: the depth itself when it is 1..16, else zero (unknown). The
// depth only picks the VideoToolbox download format, so a value outside that
// range must not stop a request that software can still decode.
func VideoBitDepthHint(depth int) int {
	if depth < 1 || depth > maxVideoBitDepth {
		return 0
	}
	return depth
}

// Validate reports whether the request can be run.
func (r Request) Validate() error {
	if strings.TrimSpace(r.Input) == "" {
		return errors.New("request has no input")
	}
	if modes := countSet(r.Window != nil, r.Samples != nil, r.At != nil); modes != 1 {
		return errors.New("request needs exactly one sampling mode")
	}
	if r.Window != nil {
		if err := r.Window.validate(); err != nil {
			return err
		}
	}
	if r.Samples != nil {
		if err := r.Samples.validate(); err != nil {
			return err
		}
		if r.Audio != nil || r.Images != nil {
			return errors.New("samples take only a stats or sheets output")
		}
		if _, err := concatPath(r.Input); err != nil {
			return err
		}
	}
	if r.At != nil {
		if err := r.At.validate(); err != nil {
			return err
		}
		if r.Images == nil || r.Audio != nil || r.Stats != nil {
			return errors.New("a single frame takes only an images output")
		}
	}
	if r.Images != nil {
		if r.At == nil {
			return errors.New("images need a single-frame sampling mode")
		}
		if err := r.Images.validate(); err != nil {
			return err
		}
	}
	if r.Sheets != nil {
		if r.Samples == nil || r.Stats != nil {
			return errors.New("sheets need samples and no other output")
		}
		if err := r.Sheets.validate(); err != nil {
			return err
		}
	}
	if !r.hasOutput() {
		return errors.New("request has no output")
	}
	if r.Window != nil && r.Window.KeyframesOnly && r.Stats == nil {
		return errors.New("a keyframes-only window needs a video output")
	}
	if r.Audio != nil && r.Audio.Silence != nil {
		if err := r.Audio.Silence.validate(); err != nil {
			return err
		}
	}
	if r.Audio != nil && r.Audio.Speech != nil {
		if err := r.Audio.Speech.validate(); err != nil {
			return err
		}
		if r.Window == nil || r.Window.KeyframesOnly {
			return errors.New("speech needs a window")
		}
		if r.Window.DurationSeconds > maxSpeechWindowSeconds {
			return fmt.Errorf("speech window must be at most %d seconds", maxSpeechWindowSeconds)
		}
		if r.Audio.Fingerprint || r.Audio.Silence != nil || r.Stats != nil {
			return errors.New("speech takes no other output")
		}
	}
	if r.Stats != nil {
		if err := r.Stats.validate(); err != nil {
			return err
		}
	}
	if r.Threads < 0 || r.Threads > maxThreads {
		return fmt.Errorf("threads %d is outside 0..%d", r.Threads, maxThreads)
	}
	if r.VideoBitDepth < 0 || r.VideoBitDepth > maxVideoBitDepth {
		return fmt.Errorf("video bit depth %d is outside 0..%d", r.VideoBitDepth, maxVideoBitDepth)
	}
	if len(r.Attempts) > maxAttempts {
		return fmt.Errorf("request has %d attempts, at most %d are allowed", len(r.Attempts), maxAttempts)
	}
	for i, attempt := range r.Attempts {
		if !finite(attempt.TimeoutSeconds) || attempt.TimeoutSeconds < 0 || attempt.TimeoutSeconds > maxAttemptSeconds {
			return fmt.Errorf("attempt %d timeout must be between 0 and %d seconds", i+1, maxAttemptSeconds)
		}
		if attempt.Hardware && !r.hasVideoOutput() {
			return fmt.Errorf("attempt %d asks for hardware decode without a video output", i+1)
		}
	}
	return nil
}

func (r Request) hasOutput() bool {
	return r.hasAudioOutput() || r.hasVideoOutput()
}

// hasVideoOutput reports whether the request decodes video, which hardware
// attempts need.
func (r Request) hasVideoOutput() bool {
	return r.Stats != nil || r.Images != nil || r.Sheets != nil
}

func (r Request) hasAudioOutput() bool {
	return r.Audio != nil && (r.Audio.Fingerprint || r.Audio.Silence != nil || r.Audio.Speech != nil)
}

// attempts returns the attempts to make, defaulting to one software attempt.
func (r Request) attempts() []Attempt {
	if len(r.Attempts) == 0 {
		return []Attempt{{}}
	}
	return r.Attempts
}

// speech returns the request's speech output, nil when it has none.
func (r Request) speech() *SpeechParams {
	if r.Audio == nil {
		return nil
	}
	return r.Audio.Speech
}

// parsesStderr reports whether an output is read from ffmpeg's log, which
// then has to run at info level.
func (r Request) parsesStderr() bool {
	return (r.Audio != nil && r.Audio.Silence != nil) || r.Stats != nil || r.Sheets != nil
}

func (w Window) validate() error {
	if !finite(w.StartSeconds) || w.StartSeconds < 0 {
		return errors.New("window start must be a non-negative number of seconds")
	}
	if !finite(w.DurationSeconds) || w.DurationSeconds <= 0 {
		return errors.New("window duration must be a positive number of seconds")
	}
	return nil
}

func (a At) validate() error {
	if !finite(a.Seconds) || a.Seconds < 0 {
		return errors.New("frame time must be a non-negative number of seconds")
	}
	return nil
}

func (s Samples) validate() error {
	if len(s.Seconds) == 0 {
		return errors.New("samples need at least one time")
	}
	if len(s.Seconds) > maxSamples {
		return fmt.Errorf("request has %d samples, at most %d are allowed", len(s.Seconds), maxSamples)
	}
	for i, seconds := range s.Seconds {
		if !finite(seconds) || seconds < 0 {
			return errors.New("sample times must be non-negative numbers of seconds")
		}
		if i > 0 && seconds <= s.Seconds[i-1] {
			return errors.New("sample times must be strictly increasing")
		}
	}
	return nil
}

func (s StatsOutput) validate() error {
	for _, share := range []float64{s.CropWidth, s.CropHeight} {
		if !finite(share) || share <= 0 || share > 1 {
			return errors.New("stats crop must keep a share of the picture in (0, 1]")
		}
	}
	if s.Width < 2 || s.Width > maxStatsWidth || s.Width%2 != 0 {
		return fmt.Errorf("stats width %d must be even and between 2 and %d", s.Width, maxStatsWidth)
	}
	if len(s.BlackThresholds) > maxBlackLevels {
		return fmt.Errorf("stats has %d black thresholds, at most %d are allowed", len(s.BlackThresholds), maxBlackLevels)
	}
	for _, threshold := range s.BlackThresholds {
		if threshold < 0 || threshold > 255 {
			return fmt.Errorf("black threshold %d is outside 0..255", threshold)
		}
	}
	return nil
}

func (s SilenceParams) validate() error {
	if s.NoiseDB > 0 || s.NoiseDB < minSilenceNoiseDB {
		return fmt.Errorf("silence noise %d dB is outside %d..0", s.NoiseDB, minSilenceNoiseDB)
	}
	if !finite(s.MinSeconds) || s.MinSeconds <= 0 || s.MinSeconds > maxSilenceSeconds {
		return fmt.Errorf("silence minimum must be between 0 and %d seconds", maxSilenceSeconds)
	}
	return nil
}

var errAudioStreamRange = fmt.Errorf("audio stream must be between 0 and %d", maxAudioStream)

// countSet counts the true values.
func countSet(values ...bool) int {
	n := 0
	for _, v := range values {
		if v {
			n++
		}
	}
	return n
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
