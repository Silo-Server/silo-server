package mediasample

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// hardwareDecode is the Runner's hardware decode configuration. Hardware
// attempts apply it to video outputs, which do not exist yet.
type hardwareDecode struct {
	Accel  string
	Device string
}

// buildArgs turns a validated request into ffmpeg arguments for one attempt.
// It also returns bytes for ffmpeg's stdin, which no request needs yet.
//
// The layout keeps the argument order the intro pipeline has always used:
// input seeking (-ss before -i), -t as an output option, and the output
// options last. Intro fingerprints are cached for years, so a change here that
// alters the decoded samples must be proven byte-identical first (see
// docs/architecture/media-sampling.md).
func buildArgs(req Request, attempt Attempt, hw hardwareDecode) ([]string, []byte, error) {
	if req.Window == nil {
		return nil, nil, errors.New("request needs exactly one sampling mode")
	}
	if attempt.Hardware {
		return nil, nil, fmt.Errorf("hardware decode (%s) needs a video output", hw.Accel)
	}
	logLevel := "warning"
	if req.parsesStderr() {
		logLevel = "info"
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", logLevel}
	if req.Threads > 0 {
		args = append(args, "-threads", strconv.Itoa(req.Threads))
	}
	args = append(args,
		"-ss", formatSeconds(req.Window.StartSeconds),
		"-i", req.Input,
		"-t", formatSeconds(req.Window.DurationSeconds),
		"-vn", "-sn", "-dn",
	)

	audio := req.Audio
	if audio == nil {
		return nil, nil, errors.New("request has no output")
	}
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
	return args, nil, nil
}

// formatSeconds prints seconds with at most millisecond precision and no
// trailing zeros, as ffmpeg time options have always been given here.
func formatSeconds(seconds float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(seconds, 'f', 3, 64), "0"), ".")
}
