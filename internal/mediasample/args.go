package mediasample

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// hardwareDecode is the Runner's hardware decode configuration. Hardware
// attempts will apply it to video outputs; none does yet.
type hardwareDecode struct {
	Accel  string
	Device string
}

// logLevel is ffmpeg's log level for runs whose outputs are parsed from its
// log. "repeat" keeps ffmpeg from folding identical consecutive lines into
// "Last message repeated N times", which would drop per-frame values.
const logLevel = "repeat+info"

// buildArgs turns a validated request into ffmpeg arguments for one attempt.
// It also returns bytes for ffmpeg's stdin, which no request needs yet.
//
// The layout keeps the argument order the intro pipeline has always used:
// input seeking (-ss before -i), -t as an output option, and the output
// options last. Intro fingerprints are cached for years, so a change here that
// alters the decoded samples must be proven byte-identical first (see
// docs/architecture/media-sampling.md).
//
// Each output reads the same input once: the audio output first, then the
// stats output of the first video stream. Every output repeats -t, which
// applies only to the output it precedes.
func buildArgs(req Request, attempt Attempt, hw hardwareDecode) ([]string, []byte, error) {
	if req.Window == nil {
		return nil, nil, errors.New("request needs exactly one sampling mode")
	}
	if attempt.Hardware {
		return nil, nil, fmt.Errorf("hardware decode (%s) is not offered for sampling yet", hw.Accel)
	}
	if !req.hasOutput() {
		return nil, nil, errors.New("request has no output")
	}
	level := "warning"
	if req.parsesStderr() {
		level = logLevel
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", level}
	if req.Threads > 0 {
		args = append(args, "-threads", strconv.Itoa(req.Threads))
		if req.Stats != nil {
			args = append(args, "-filter_threads", strconv.Itoa(req.Threads))
		}
	}
	if req.Window.KeyframesOnly {
		args = append(args, "-skip_frame:v", "nokey")
	}
	args = append(args,
		"-ss", formatSeconds(req.Window.StartSeconds),
		"-i", req.Input,
	)
	duration := formatSeconds(req.Window.DurationSeconds)

	if audio := req.Audio; req.hasAudioOutput() {
		args = append(args, "-t", duration, "-vn", "-sn", "-dn")
		if audio.Silence != nil {
			args = append(args, "-af", fmt.Sprintf("silencedetect=noise=%ddB:duration=%s",
				audio.Silence.NoiseDB, formatSeconds(audio.Silence.MinSeconds)))
		}
		if audio.Fingerprint {
			// The chromaprint muxer takes only mono or stereo audio.
			args = append(args, "-ac", "2", "-f", "chromaprint", "-fp_format", "raw", "-")
		} else {
			args = append(args, "-f", "null", "-")
		}
	}
	if req.Stats != nil {
		args = append(args, "-t", duration,
			"-map", "0:V:0", "-an", "-sn", "-dn",
			"-vf", buildStatsGraph(*req.Stats).filter,
			"-f", "null", "-")
	}
	return args, nil, nil
}

// formatSeconds prints seconds with at most millisecond precision and no
// trailing zeros, as ffmpeg time options have always been given here.
func formatSeconds(seconds float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(seconds, 'f', 3, 64), "0"), ".")
}
