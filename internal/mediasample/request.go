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
// A request names exactly one sampling mode (today only Window) and at least
// one output (today only Audio).
type Request struct {
	// Input is the media file to decode.
	Input string `json:"input"`
	// Window samples a contiguous span of the file.
	Window *Window `json:"window,omitempty"`
	// Audio asks for audio features of the sampled span.
	Audio *AudioOutput `json:"audio,omitempty"`
	// Attempts are tried in order until one succeeds. Empty means a single
	// software attempt bounded only by the caller's context.
	Attempts []Attempt `json:"attempts,omitempty"`
	// Threads caps ffmpeg's decoder threads. Zero leaves ffmpeg's default.
	Threads int `json:"threads,omitempty"`
	// Background marks work nobody is waiting on. It does not change the run
	// yet; process priority for background runs is planned.
	Background bool `json:"background,omitempty"`
}

// Window is a contiguous span of the input, in media seconds.
type Window struct {
	StartSeconds    float64 `json:"start_seconds"`
	DurationSeconds float64 `json:"duration_seconds"`
	// KeyframesOnly decodes only video keyframes. It needs a video output,
	// which no request type offers yet, so Validate rejects it for now.
	KeyframesOnly bool `json:"keyframes_only,omitempty"`
}

// AudioOutput selects audio features. Fingerprint and Silence may be combined
// in one run.
type AudioOutput struct {
	// Fingerprint returns raw Chromaprint points for the sampled audio.
	Fingerprint bool `json:"fingerprint,omitempty"`
	// Silence returns the silences silencedetect finds, in absolute media
	// seconds.
	Silence *SilenceParams `json:"silence,omitempty"`
}

// SilenceParams configures silence detection.
type SilenceParams struct {
	// NoiseDB is the level, in dB, at or below which audio counts as silent.
	NoiseDB int `json:"noise_db"`
	// MinSeconds is the shortest silence reported.
	MinSeconds float64 `json:"min_seconds"`
}

// Attempt is one decode attempt.
type Attempt struct {
	// Hardware decodes on the Runner's configured hardware. Only video
	// outputs can use it, and none exist yet, so Validate rejects it for now.
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
)

// Validate reports whether the request can be run.
func (r Request) Validate() error {
	if strings.TrimSpace(r.Input) == "" {
		return errors.New("request has no input")
	}
	if r.Window == nil {
		return errors.New("request needs exactly one sampling mode")
	}
	if err := r.Window.validate(); err != nil {
		return err
	}
	if !r.hasOutput() {
		return errors.New("request has no output")
	}
	if r.Audio != nil && r.Audio.Silence != nil {
		if err := r.Audio.Silence.validate(); err != nil {
			return err
		}
	}
	if r.Threads < 0 || r.Threads > maxThreads {
		return fmt.Errorf("threads %d is outside 0..%d", r.Threads, maxThreads)
	}
	if len(r.Attempts) > maxAttempts {
		return fmt.Errorf("request has %d attempts, at most %d are allowed", len(r.Attempts), maxAttempts)
	}
	for i, attempt := range r.Attempts {
		if !finite(attempt.TimeoutSeconds) || attempt.TimeoutSeconds < 0 || attempt.TimeoutSeconds > maxAttemptSeconds {
			return fmt.Errorf("attempt %d timeout must be between 0 and %d seconds", i+1, maxAttemptSeconds)
		}
		if attempt.Hardware {
			return fmt.Errorf("attempt %d asks for hardware decode, which needs a video output", i+1)
		}
	}
	return nil
}

func (r Request) hasOutput() bool {
	return r.Audio != nil && (r.Audio.Fingerprint || r.Audio.Silence != nil)
}

// attempts returns the attempts to make, defaulting to one software attempt.
func (r Request) attempts() []Attempt {
	if len(r.Attempts) == 0 {
		return []Attempt{{}}
	}
	return r.Attempts
}

// parsesStderr reports whether an output is read from ffmpeg's log, which
// then has to run at info level.
func (r Request) parsesStderr() bool {
	return r.Audio != nil && r.Audio.Silence != nil
}

func (w Window) validate() error {
	if !finite(w.StartSeconds) || w.StartSeconds < 0 {
		return errors.New("window start must be a non-negative number of seconds")
	}
	if !finite(w.DurationSeconds) || w.DurationSeconds <= 0 {
		return errors.New("window duration must be a positive number of seconds")
	}
	if w.KeyframesOnly {
		return errors.New("a keyframes-only window needs a video output")
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

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
