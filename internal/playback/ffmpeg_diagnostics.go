package playback

import (
	"math"
	"strconv"
	"strings"
)

const pipelineSoftwareDecodeHardwareEncode = "software_decode_hardware_encode"

// FFmpegDiagnostics is the complete, event-only snapshot of the recipe and
// runtime policy behind one FFmpeg process. Stderr rows deliberately omit it:
// repeating the command and recipe on every line would inflate operational log
// storage without adding information.
type FFmpegDiagnostics struct {
	FFmpegPath               string
	FFmpegArgs               []string
	Pipeline                 string
	VideoEncoder             string
	AudioEncoder             string
	VideoFilter              string
	VideoPixelFormats        []string
	SegmentType              string
	SourceVideoCodec         string
	SourceVideoProfile       string
	SourceVideoBitDepth      int
	SourceVideoResolution    string
	SourceAudioChannels      int
	SoftwareVideoDecode      bool
	HWDevice                 string
	TargetAudioChannels      int
	TargetAudioBitrateKbps   int
	TargetBitrateKbps        int
	ToneMapPolicy            string
	ToneMapMode              string
	ToneMapSourceKind        string
	ToneMapFilter            string
	ToneMapRecipeVersion     string
	ToneMapPreflightRequired bool
	VideoBitstreamFilter     string
	VideoSampleEntry         string
	AudioTrackIndex          int
	SubtitleTrackIndex       int
	SubtitleBurnIn           bool
	SubtitleCodec            string
	SegmentDuration          int
	SegmentRetentionSeconds  int
	TotalDuration            float64
	FastStart                bool
	SegmentGeneration        uint64
	LastRequestedSegment     int
	LastCompletedSegment     int
	ThrottleConfigured       bool
	ThrottleEnabled          bool
	ThrottleThresholdSeconds int
	ThrottlePaused           bool
}

// ffmpegDiagnosticsOf describes the exact executable recipe selected for a
// process. It derives encoder, filter, pixel, and segment facts from the same
// argument list passed to FFmpeg so the admin debug view cannot drift from the
// command that actually ran.
func ffmpegDiagnosticsOf(opts TranscodeOpts, args []string) FFmpegDiagnostics {
	return FFmpegDiagnostics{
		FFmpegPath:               opts.FFmpegPath,
		FFmpegArgs:               append([]string(nil), args...),
		Pipeline:                 transcodePipelineName(opts),
		VideoEncoder:             ffmpegOptionValue(args, "-c:v"),
		AudioEncoder:             ffmpegOptionValue(args, "-c:a"),
		VideoFilter:              ffmpegVideoFilter(args),
		VideoPixelFormats:        ffmpegVideoPixelFormats(args),
		SegmentType:              ffmpegOptionValue(args, "-hls_segment_type"),
		SourceVideoCodec:         opts.SourceVideoCodec,
		SourceVideoProfile:       opts.SourceVideoProfile,
		SourceVideoBitDepth:      opts.SourceVideoBitDepth,
		SourceVideoResolution:    opts.SourceVideoResolution,
		SourceAudioChannels:      opts.SourceAudioChannels,
		SoftwareVideoDecode:      opts.SoftwareVideoDecode,
		HWDevice:                 opts.HWDevice,
		TargetAudioChannels:      effectiveFFmpegInt(args, "-ac", opts.TargetAudioChannels),
		TargetAudioBitrateKbps:   effectiveFFmpegBitrateKbps(args, "-b:a", opts.TargetAudioBitrateKbps),
		TargetBitrateKbps:        effectiveFFmpegBitrateKbps(args, "-maxrate", opts.TargetBitrateKbps),
		ToneMapPolicy:            string(opts.ToneMapPolicy),
		ToneMapMode:              string(opts.ToneMapMode),
		ToneMapSourceKind:        string(opts.ToneMapSourceKind),
		ToneMapFilter:            opts.ToneMapFilter,
		ToneMapRecipeVersion:     opts.ToneMapRecipeVersion,
		ToneMapPreflightRequired: opts.ToneMapPreflightRequired,
		VideoBitstreamFilter:     opts.VideoBitstreamFilter,
		VideoSampleEntry:         opts.VideoSampleEntry,
		AudioTrackIndex:          opts.AudioTrackIndex,
		SubtitleTrackIndex:       opts.SubtitleTrackIndex,
		SubtitleBurnIn:           opts.SubtitleBurnIn,
		SubtitleCodec:            opts.SubtitleCodec,
		SegmentDuration:          opts.SegmentDuration,
		SegmentRetentionSeconds:  opts.SegmentRetentionSeconds,
		TotalDuration:            opts.TotalDuration,
		FastStart:                opts.FastStart,
	}
}

// effectiveFFmpegInt returns the generated integer option when it is valid,
// otherwise preserving the configured diagnostic value.
func effectiveFFmpegInt(args []string, option string, configured int) int {
	value := ffmpegOptionValue(args, option)
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return configured
	}
	return parsed
}

// effectiveFFmpegBitrateKbps returns a generated FFmpeg bitrate in kbps.
// Unsuffixed FFmpeg values are bits per second; k, M, and G use SI units.
func effectiveFFmpegBitrateKbps(args []string, option string, configured int) int {
	value := strings.TrimSpace(ffmpegOptionValue(args, option))
	if value == "" {
		return configured
	}

	multiplier := float64(1)
	if suffix := value[len(value)-1]; suffix == 'k' || suffix == 'K' || suffix == 'm' || suffix == 'M' || suffix == 'g' || suffix == 'G' {
		value = value[:len(value)-1]
		switch suffix {
		case 'k', 'K':
			multiplier = 1_000
		case 'm', 'M':
			multiplier = 1_000_000
		case 'g', 'G':
			multiplier = 1_000_000_000
		}
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 {
		return configured
	}
	return int(math.Round(parsed * multiplier / 1_000))
}

// transcodePipelineName names which stages run on CPU or GPU after all
// automatic fallbacks and source-safety rules have been applied.
func transcodePipelineName(opts TranscodeOpts) string {
	if strings.EqualFold(strings.TrimSpace(opts.TargetCodecVideo), "copy") {
		if TranscodesAudio(opts.TargetCodecAudio) {
			return "video_copy_audio_transcode"
		}
		return string(PlayRemux)
	}
	if !isHardwareTranscodeBackend(opts.HWAccel) {
		return "software_decode_software_encode"
	}
	if opts.SoftwareVideoDecode {
		return pipelineSoftwareDecodeHardwareEncode
	}
	return "hardware_decode_hardware_encode"
}

// ffmpegOptionValue returns the final value of a repeatable FFmpeg option.
func ffmpegOptionValue(args []string, option string) string {
	for index := len(args) - 2; index >= 0; index-- {
		if args[index] == option {
			return args[index+1]
		}
	}
	return ""
}

// ffmpegVideoFilter returns the effective simple or complex video graph.
func ffmpegVideoFilter(args []string) string {
	if value := ffmpegOptionValue(args, "-filter_complex"); value != "" {
		return value
	}
	return ffmpegOptionValue(args, "-vf")
}

// ffmpegVideoPixelFormats reports each explicit pixel format in execution
// order, including CPU frames and uploaded hardware surfaces.
func ffmpegVideoPixelFormats(args []string) []string {
	formats := make([]string, 0, 3)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range formats {
			if existing == value {
				return
			}
		}
		formats = append(formats, value)
	}

	add(ffmpegOptionValue(args, "-pix_fmt"))
	for remaining := ffmpegVideoFilter(args); ; {
		marker := strings.Index(remaining, "format=")
		if marker < 0 {
			break
		}
		remaining = remaining[marker+len("format="):]
		end := strings.IndexAny(remaining, ",:;[]")
		if end < 0 {
			add(remaining)
			break
		}
		add(remaining[:end])
		remaining = remaining[end+1:]
	}
	return formats
}
