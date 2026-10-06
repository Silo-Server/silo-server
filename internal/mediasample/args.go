package mediasample

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// hardwareDecode is the hardware a hardware attempt decodes video on: the
// Runner's accelerator and the one render device the attempt reserved.
type hardwareDecode struct {
	Accel  string
	Device string
}

// logLevel is ffmpeg's log level for runs whose outputs are parsed from its
// log. "repeat" keeps ffmpeg from folding identical consecutive lines into
// "Last message repeated N times", which would drop per-frame values.
const logLevel = "repeat+info"

// errorLogLevel logs only errors, for runs that read nothing from the log.
const errorLogLevel = "error"

const (
	mapStreamOption        = "-map"
	firstVideoStream       = "0:V:0"
	disableAudioOption     = "-an"
	disableSubtitlesOption = "-sn"
	disableDataOption      = "-dn"
)

// buildArgs turns a validated request into ffmpeg arguments for one attempt.
// It also returns bytes for ffmpeg's stdin: the ffconcat list of a Samples
// request, whose inpoints are offset by inputStart (the input's container
// start time, which the probe learned), and nil otherwise.
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
//
// A hardware attempt adds the decode options of hw (see hwdecode.go) to the
// input's options. Its frames stay in hardware surfaces, which the stats
// chain downloads, on VAAPI after scaling them on the GPU (see
// buildStatsGraph). Audio decodes in software either way.
func buildArgs(req Request, attempt Attempt, hw hardwareDecode, inputStart float64) ([]string, []byte, error) {
	if (req.Window == nil) == (req.Samples == nil) {
		return nil, nil, errors.New("request needs exactly one sampling mode")
	}
	if !req.hasOutput() {
		return nil, nil, errors.New("request has no output")
	}
	var decode []string
	if attempt.Hardware {
		if req.Stats == nil {
			return nil, nil, errors.New("hardware decode needs a video output")
		}
		var err error
		if decode, err = hardwareDecodeArgs(hw, true); err != nil {
			return nil, nil, err
		}
	}
	level := "warning"
	if req.parsesStderr() {
		level = logLevel
	}
	args := quietArgs(level)
	if req.Threads > 0 {
		args = append(args, "-threads", strconv.Itoa(req.Threads))
		if req.Stats != nil {
			args = append(args, "-filter_threads", strconv.Itoa(req.Threads))
		}
	}
	if req.Samples != nil {
		return buildSamplesArgs(req, attempt, hw, append(args, decode...), inputStart)
	}
	if req.Window.KeyframesOnly {
		args = append(args, "-skip_frame:v", "nokey")
	}
	args = append(args, decode...)
	args = append(args,
		"-ss", formatSeconds(req.Window.StartSeconds),
		"-i", req.Input,
	)
	duration := formatSeconds(req.Window.DurationSeconds)

	if speech := req.speech(); speech != nil {
		// Raw samples on stdout; see speech.go.
		return append(args, "-t", duration,
			mapStreamOption, "0:a:"+strconv.Itoa(speech.AudioStream), "-vn", disableSubtitlesOption, disableDataOption,
			"-af", speech.filter(), "-ac", "1",
			"-f", "s16le", "-acodec", "pcm_s16le", "-"), nil, nil
	}
	if audio := req.Audio; req.hasAudioOutput() {
		args = append(args, "-t", duration, "-vn", disableSubtitlesOption, disableDataOption)
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
			mapStreamOption, firstVideoStream, disableAudioOption, disableSubtitlesOption, disableDataOption,
			videoFilterOption, req.statsGraph(attempt, hw.Accel).filter,
			"-f", "null", "-")
	}
	return args, nil, nil
}

// hideBanner keeps ffmpeg from printing its build banner; logLevelOption
// sets its log level. videoFilterOption and pixelFormatOption set an
// output's video filter chain and pixel format.
const (
	hideBanner        = "-hide_banner"
	logLevelOption    = "-loglevel"
	videoFilterOption = "-vf"
	pixelFormatOption = "-pix_fmt"
)

// quietArgs are the global options that open every sampling ffmpeg: no
// banner, no reading keys from stdin, and the log level.
func quietArgs(level string) []string {
	return []string{hideBanner, "-nostdin", logLevelOption, level}
}

// buildSamplesArgs finishes the arguments of a Samples request, whose only
// output is the statistics of the first video stream, after the global and
// hardware decode options in args. See concat.go for how the list samples the
// input.
func buildSamplesArgs(req Request, attempt Attempt, hw hardwareDecode, args []string, inputStart float64) ([]string, []byte, error) {
	if req.Audio != nil || req.Stats == nil {
		return nil, nil, errors.New("samples take only a stats output")
	}
	list, err := buildConcatList(req.Input, req.Samples.Seconds, inputStart)
	if err != nil {
		return nil, nil, err
	}
	args = append(args, concatInputArgs...)
	args = append(args, "-i", concatListInput,
		mapStreamOption, firstVideoStream, disableAudioOption, disableSubtitlesOption, disableDataOption,
		videoFilterOption, req.statsGraph(attempt, hw.Accel).filter,
		"-f", "null", "-")
	return args, list, nil
}

// buildSheetsArgs builds the arguments of a Sheets request whose video chain
// is graph (see buildSheetsGraph). A Samples request reads its list as
// buildSamplesArgs does and returns the list for stdin; a Window request,
// which a Samples request becomes for inputs the list cannot seek in, decodes
// the window's keyframes. Either way the frames go to stdout raw, and hardware
// attempts leave VideoToolbox frames in system memory, as images do.
func buildSheetsArgs(req Request, attempt Attempt, hw hardwareDecode, inputStart float64, graph, packetTimingPath string) ([]string, []byte, error) {
	if (req.Window == nil) == (req.Samples == nil) || req.Sheets == nil {
		return nil, nil, errors.New("sheets need exactly one sampling mode")
	}
	var decode []string
	if attempt.Hardware {
		var err error
		if decode, err = hardwareDecodeArgs(hw, false); err != nil {
			return nil, nil, err
		}
	}
	args := quietArgs(logLevel)
	if req.Threads > 0 {
		threads := strconv.Itoa(req.Threads)
		args = append(args, "-threads", threads, "-filter_threads", threads)
	}
	if req.Samples != nil {
		list, err := buildConcatListSpan(req.Input, req.Samples.Seconds, inputStart, sheetSampleSpanSeconds)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, decode...)
		args = append(args, concatInputArgs...)
		args = append(args, "-i", concatListInput)
		return append(args, sheetsOutputArgs(graph)...), list, nil
	}
	// The window's duration bounds the input, not the output: an output -t
	// drops frames after the filters logged them, and the log would no
	// longer count the frames on stdout.
	args = append(args, "-skip_frame:v", "nokey")
	args = append(args, decode...)
	args = append(args,
		"-ss", formatSeconds(req.Window.StartSeconds),
		"-t", formatSeconds(req.Window.DurationSeconds),
		"-i", req.Input,
	)
	// Copy the same input's packets to a timing log without decoding them.
	// Their extent distinguishes a valid final GOP from premature EOF;
	// keyframe timestamps alone cannot. A separate file keeps packet metadata
	// from interleaving with frame log lines on stderr.
	args = append(args, mapStreamOption, firstVideoStream, "-c:v", "copy", "-f", "framecrc", packetTimingPath)
	return append(args, sheetsOutputArgs(graph)...), nil, nil
}

// formatSeconds prints seconds with at most millisecond precision and no
// trailing zeros, as ffmpeg time options have always been given here.
func formatSeconds(seconds float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(seconds, 'f', 3, 64), "0"), ".")
}
