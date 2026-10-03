package playback

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

const directorySyncUnsupportedGOOS = "windows"

// PrepareTarget describes the concrete encode target for a prepared download
// artifact (remux or transcode-to-file).
type PrepareTarget struct {
	Container                  string
	CodecVideo                 string // "copy" for remux, else an encoder codec (e.g. "h264")
	CodecAudio                 string // "copy" or "aac"
	Resolution                 string // "" = keep source resolution (no scale)
	AudioTrackIndex            int
	TargetBitrateKbps          int // 0 = encoder default/CRF; >0 caps video bitrate
	ToneMapPolicy              tonemap.Policy
	ToneMapMode                tonemap.Mode
	ToneMapSourceKind          tonemap.SourceKind
	ToneMapRecipeVersion       string
	ToneMapPreflightRequired   bool
	ToneMapSourceRevision      tonemap.SourceRevision
	ToneMapDVConfigPresent     bool
	ToneMapDVBLCompatIDPresent bool
	ToneMapDVBLPresent         bool
	ToneMapDVRPUPresent        bool
}

// ResolveRemuxTarget computes the encode target for a remux download of file,
// reusing Resolve so a download's audio decision matches the streaming
// decision for the same client: copy video, copy audio unless the client
// can't decode it (then AAC), and keep the source resolution.
func ResolveRemuxTarget(file *models.MediaFile, caps ClientCapabilities, settings AdminSettings) PrepareTarget {
	t := PrepareTarget{Container: containerMP4V3, CodecVideo: codecCopyV3, CodecAudio: codecCopyV3, AudioTrackIndex: -1}
	if Resolve(file, caps, settings).TranscodeAudio {
		t.CodecAudio = audioCodecAACV3
	}
	return t
}

// DownloadTranscodeSettings are the server switches and policy limits a
// bitrate-capped download encode honors.
type DownloadTranscodeSettings struct {
	AllowHEVCEncoding bool
	// MaxHeight is a policy ceiling on the output ladder class; 0 means none.
	MaxHeight int
}

// ResolveDownloadTranscodeTarget computes a bitrate-capped download encode.
// The bitrate picks the largest ladder class it encodes well at the source's
// frame rate (LadderClassForBitrate); the class steps down until the device's
// decoder can take the box-fit output; and the source is never enlarged nor
// re-encoded above its own bitrate. HEVC is chosen when the server allows HEVC
// encoding and the caps attest an 8-bit HEVC decoder that reaches at least the
// class H.264 would, otherwise the output is H.264. ok is false when the caps
// attest no decoder, for any codec the server may encode, that takes at least
// the smallest ladder class.
//
// Resolution holds the ladder class label ("1080p") when the source must be
// downscaled and is empty when the source already fits; DownloadScaleResolution
// turns the class back into the exact encoder height for this file.
func ResolveDownloadTranscodeTarget(file *models.MediaFile, caps ClientCapabilities, capKbps int, settings DownloadTranscodeSettings) (PrepareTarget, bool) {
	source := SourceDescriptorFromFileV3(file, 0)
	codec := transcodeCodecH264
	decoder, class, ok := bestDownloadDecoder(source, downloadDecodersFor(caps, codec, source.FrameRate), capKbps, codec, settings.MaxHeight)
	if settings.AllowHEVCEncoding && caps.hasDetailedVideoEvidence() {
		hevc, hevcClass, hevcOK := bestDownloadDecoder(source, downloadDecodersFor(caps, transcodeCodecHEVC, source.FrameRate), capKbps, transcodeCodecHEVC, settings.MaxHeight)
		if hevcOK && (!ok || hevcClass >= class) {
			codec, decoder, class, ok = transcodeCodecHEVC, hevc, hevcClass, true
		}
	}
	if !ok {
		return PrepareTarget{}, false
	}
	target := PrepareTarget{Container: containerMP4V3, CodecVideo: codec, CodecAudio: audioCodecAACV3, AudioTrackIndex: -1, TargetBitrateKbps: capKbps}
	if width, height := FitLadderBox(source.Width, source.Height, class); height == 0 || width != source.Width || height != source.Height {
		target.Resolution = heightLabel(class)
	}
	if sourceKbps := sourceEquivalentKbps(source, codec); sourceKbps > 0 {
		target.TargetBitrateKbps = min(capKbps, sourceKbps)
	}
	if decoder.maxBitrateKbps > 0 {
		target.TargetBitrateKbps = min(target.TargetBitrateKbps, decoder.maxBitrateKbps)
	}
	return target, true
}

// sourceEquivalentKbps is the source's video bitrate converted to codec, the
// most a re-encode of it ever needs; 0 when the source bitrate is unknown.
func sourceEquivalentKbps(source SourceDescriptorV3, codec string) int {
	if source.BitrateKbps <= 0 {
		return 0
	}
	return max(int(float64(source.BitrateKbps)*codecEfficiency(codec)/codecEfficiency(source.VideoCodec)), 1)
}

// bestDownloadDecoder returns the decoder whose limits let the encode reach
// the tallest ladder class, and that class. A tie goes to the higher bitrate
// limit, so a large but slow decoder never wins over one the preset can use in
// full. ok is false when no decoder takes any ladder class.
func bestDownloadDecoder(source SourceDescriptorV3, decoders []downloadDecoder, capKbps int, codec string, maxHeight int) (best downloadDecoder, class int, ok bool) {
	for _, decoder := range decoders {
		decoderClass, fits := downloadLadderClass(source, decoder, capKbps, codec, maxHeight)
		if fits && (!ok || decoderClass > class || decoderClass == class && bitrateLimitAbove(decoder.maxBitrateKbps, best.maxBitrateKbps)) {
			best, class, ok = decoder, decoderClass, true
		}
	}
	return best, class, ok
}

// bitrateLimitAbove reports whether limit a allows more than b, treating zero
// as unlimited.
func bitrateLimitAbove(a, b int) bool {
	return b > 0 && (a == 0 || a > b)
}

// DownloadScaleResolution converts a download artifact's ladder class into
// the exact height the encoder scales to, so a cinema-aspect source keeps its
// shape inside the class box. An empty class, or a source already inside the
// box, leaves the source unscaled unless its frame is odd.
func DownloadScaleResolution(file *models.MediaFile, classLabel string) string {
	source := SourceDescriptorFromFileV3(file, 0)
	class := resolutionHeightV3(classLabel)
	if class <= 0 {
		if classLabel == "" {
			return evenFrameLabel(source)
		}
		return classLabel
	}
	width, height := FitLadderBox(source.Width, source.Height, class)
	switch {
	case height == 0:
		return heightLabel(class)
	case width == source.Width && height == source.Height:
		return evenFrameLabel(source)
	default:
		return heightLabel(height)
	}
}

// evenFrameLabel is the scale an encode that keeps the source frame still
// needs: none for an even frame, and the even height at or below an odd one,
// since 4:2:0 output needs even dimensions and scale=-2 evens the width.
func evenFrameLabel(source SourceDescriptorV3) string {
	if source.Height <= 0 || source.Height%2 == 0 && source.Width%2 == 0 {
		return ""
	}
	return heightLabel(source.Height &^ 1)
}

// downloadLadderClass returns the tallest ladder class, at or below the one
// the bitrate earns, whose box-fit output fits the device's decoder. The
// budget is the preset or the decoder's bitrate limit, whichever is lower, so
// a 3 Mbps decoder limit never earns a class that needs 5 Mbps. The H.264
// level is checked at the bitrate the encoder will get, which never exceeds
// the source's own. fits is false when even the smallest class is too large
// for the decoder: the ladder does not go below it.
func downloadLadderClass(source SourceDescriptorV3, decoder downloadDecoder, capKbps int, codec string, maxHeight int) (class int, fits bool) {
	budget := capKbps
	if decoder.maxBitrateKbps > 0 {
		budget = min(budget, decoder.maxBitrateKbps)
	}
	encodeKbps := budget
	if sourceKbps := sourceEquivalentKbps(source, codec); sourceKbps > 0 {
		encodeKbps = min(encodeKbps, sourceKbps)
	}
	top := LadderClassForBitrate(budget, source.FrameRate, codec)
	if maxHeight > 0 {
		top = min(top, maxHeight)
	}
	for _, class := range ladderClassesFrom(top) {
		// The decoder must take the frame the encoder writes, which evens an
		// odd source size.
		width, height := FitLadderBox(source.Width, source.Height, class.Height)
		width, height = encodedFrame(source.Width, source.Height, width, height)
		if width == 0 {
			// An unknown width is checked as the full class box.
			width, height = class.Width, max(height, class.Height)
		}
		if decoder.maxWidth > 0 && width > decoder.maxWidth || decoder.maxHeight > 0 && height > decoder.maxHeight {
			continue
		}
		if decoder.maxLevel > 0 && h264LevelFor(width, height, source.FrameRate, encodeKbps) > decoder.maxLevel {
			continue
		}
		return class.Height, true
	}
	return 0, false
}

// h264Level is one row of the H.264 level limits (Table A-1): frame size in
// macroblocks, macroblocks per second, and the High-profile bitrate and
// coded-picture-buffer ceilings in kbit.
type h264Level struct {
	idc, maxFrameMBs, maxMBPerSecond, maxHighKbps, maxHighCPB int
}

var h264Levels = []h264Level{
	{30, 1_620, 40_500, 12_500, 12_500},
	{31, 3_600, 108_000, 17_500, 17_500},
	{32, 5_120, 216_000, 25_000, 25_000},
	{40, 8_192, 245_760, 25_000, 31_250},
	{41, 8_192, 245_760, 62_500, 78_125},
	{42, 8_704, 522_240, 62_500, 78_125},
	{50, 22_080, 589_824, 168_750, 168_750},
	{51, 36_864, 983_040, 300_000, 300_000},
	{52, 36_864, 2_073_600, 300_000, 300_000},
}

// h264LevelFor is the lowest H.264 level (as level_idc, 41 for 4.1) that
// holds a width x height stream at frameRate and kbps with the encoder's
// buffer of twice the rate, which is what libx264 picks when no level is
// forced. Unknown frame rates count as 30 fps.
func h264LevelFor(width, height int, frameRate float64, kbps int) int {
	if frameRate <= 0 {
		frameRate = 30
	}
	frameMBs := ((width + 15) / 16) * ((height + 15) / 16)
	mbPerSecond := int(math.Ceil(float64(frameMBs) * frameRate))
	for _, level := range h264Levels {
		if frameMBs <= level.maxFrameMBs && mbPerSecond <= level.maxMBPerSecond &&
			kbps <= level.maxHighKbps && 2*kbps <= level.maxHighCPB {
			return level.idc
		}
	}
	return 60
}

// h264LevelBitrateKbps is the highest cap whose rate and doubled buffer fit
// the given level; 0 for an unknown level.
func h264LevelBitrateKbps(idc int) int {
	for _, level := range h264Levels {
		if level.idc == idc {
			return min(level.maxHighKbps, level.maxHighCPB/2)
		}
	}
	return 0
}

// h264HighProfileV3 is the profile every H.264 encode here produces.
const h264HighProfileV3 = "high"

// downloadDecoder is the decoder bound a download encode must fit. Zero
// fields are unbounded.
type downloadDecoder struct {
	maxWidth, maxHeight, maxBitrateKbps int
	// maxLevel is the highest H.264 level_idc the decoder lists when its
	// levels are checked.
	maxLevel int
}

// downloadDecodersFor returns the decoders the caps say play an 8-bit
// download in codec. Strict-tier video_decode entries are authoritative and
// prefer hardware decoders, since a download plays back later on the same
// device. A decoder qualifies when it takes the source's frame rate and lists
// the profile the encode produces; as in HLS playback, a platform-attested
// hardware decoder is exempt from the profile and level lists, and a
// level-bound HEVC decoder is skipped because the HEVC recipe does not pin a
// level. If no listed H.264 decoder qualifies, their sizes still bound the
// output, since H.264 is the fallback and a smaller frame is the best the
// encode can offer. Strict caps with no decoder for codec return none.
// Without strict entries the coarse max_resolution ceiling applies.
func downloadDecodersFor(caps ClientCapabilities, codec string, frameRate float64) []downloadDecoder {
	if caps.hasDetailedVideoEvidence() {
		if decoders := strictDownloadDecoders(caps, codec, frameRate, true); len(decoders) > 0 || codec != transcodeCodecH264 {
			return decoders
		}
		return strictDownloadDecoders(caps, codec, frameRate, false)
	}
	if height := resolutionHeightV3(caps.MaxResolution); height > 0 {
		width, _ := dimensionsFromResolutionV3(heightLabel(height))
		return []downloadDecoder{{maxWidth: width, maxHeight: height}}
	}
	return []downloadDecoder{{}}
}

// strictDownloadDecoders lists the strict-tier decoders for codec: the
// hardware ones when any is usable, else the opted-in software ones. With
// qualify set it also applies the frame rate, profile and level rules
// downloadDecodersFor describes.
func strictDownloadDecoders(caps ClientCapabilities, codec string, frameRate float64, qualify bool) []downloadDecoder {
	softwareOptIn := HasFeatureV3(caps.ClientFeatures, FeatureSoftwareVideoDecodeV3)
	outputProfile := h264HighProfileV3
	if codec == transcodeCodecHEVC {
		outputProfile = hevcMainProfileV3
	}
	for _, hardware := range []bool{true, false} {
		// Platform attestation vouches for the platform's hardware decoders only.
		checkLists := caps.VideoEvidence == EvidenceExactV3 || !hardware
		var decoders []downloadDecoder
		for _, decoder := range caps.VideoDecode {
			if decoder.Hardware != hardware || (!hardware && !softwareOptIn) ||
				!strings.EqualFold(decoder.Codec, codec) ||
				(len(decoder.BitDepths) > 0 && !containsIntV3(decoder.BitDepths, 8)) {
				continue
			}
			if qualify && ((decoder.MaxFrameRate > 0 && frameRate > decoder.MaxFrameRate+0.01) ||
				(checkLists && len(decoder.Profiles) > 0 && !videoProfileSupportedV3(codec, outputProfile, decoder.Profiles)) ||
				(codec == transcodeCodecHEVC && len(decoder.Levels) > 0)) {
				continue
			}
			bound := downloadDecoder{maxWidth: decoder.MaxWidth, maxHeight: decoder.MaxHeight, maxBitrateKbps: decoder.MaxBitrateKbps}
			if checkLists && codec == transcodeCodecH264 && len(decoder.Levels) > 0 {
				bound.maxLevel = slices.Max(decoder.Levels)
				// The level also bounds the rate and buffer the encode may use.
				if levelKbps := h264LevelBitrateKbps(bound.maxLevel); levelKbps > 0 &&
					(bound.maxBitrateKbps == 0 || levelKbps < bound.maxBitrateKbps) {
					bound.maxBitrateKbps = levelKbps
				}
			}
			decoders = append(decoders, bound)
		}
		if len(decoders) > 0 {
			return decoders
		}
	}
	return nil
}

// PrepareFile encodes a single finalized MP4 (with a relocated moov atom via
// -movflags +faststart, enabling clean seek/resume) from opts.InputPath. It
// writes to outputPath+".part" and atomically renames on success so a partial
// file is never observable at outputPath. The call blocks until ffmpeg exits.
func PrepareFile(ctx context.Context, opts TranscodeOpts, outputPath string) error {
	if outputPath == "" {
		return fmt.Errorf("prepare-file: empty output path")
	}
	opts = normalizeTranscodeOptsContext(ctx, opts)
	if err := validateToneMapOpts(opts); err != nil {
		return fmt.Errorf("prepare-file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("prepare-file: create output dir: %w", err)
	}
	partPath := outputPath + ".part"
	// A reclaimed job overwrites its own .part; ffmpeg -y handles that, but remove
	// any stale partial first so a failed prior attempt can't be mistaken for output.
	_ = os.Remove(partPath)

	// Resolve a multi-device hw_device list to one concrete GPU for this
	// encode. Run blocks until ffmpeg exits, so the deferred release fires at
	// exactly the process-exit boundary.
	hwDevice, releaseHWDevice := AcquireHWDevice(opts.HWDevice, opts.HWAccel)
	opts.HWDevice = hwDevice
	defer releaseHWDevice()
	var encoderErr error
	opts, encoderErr = resolveHEVCTranscodeEncoder(ctx, opts)
	if encoderErr != nil {
		return fmt.Errorf("prepare-file: %w", encoderErr)
	}
	if opts, encoderErr = resolveVAAPIRateControl(ctx, opts); encoderErr != nil {
		return fmt.Errorf("prepare-file: %w", encoderErr)
	}
	if opts.HWAccel == transcodeHWNone {
		releaseHWDevice()
	}
	if err := validateToneMapSource(ctx, opts); err != nil {
		return fmt.Errorf("prepare-file: %w", err)
	}

	logs := newPrepareLogWriter(ctx, opts)
	runOnce := func(runOpts TranscodeOpts) error {
		args := buildPrepareFileArgs(runOpts, partPath)
		bin := runOpts.FFmpegPath
		if bin == "" {
			bin = ffmpegBinary()
		}

		cmd := exec.CommandContext(ctx, bin, args...)
		stderr := newBoundedTailBuffer(stderrTailMaxBytes)
		cmd.Stderr = stderr
		if logs != nil {
			logs.setHWAccel(runOpts.HWAccel)
			cmd.Stderr = io.MultiWriter(stderr, logs)
		}
		if runOpts.PrepareProgressSink != nil {
			cmd.Stdout = newPrepareProgressWriter(runOpts.PrepareProgressSink, runOpts.TotalDuration)
		}
		cmd.WaitDelay = 3 * time.Second

		logs.event("ffmpeg process starting", "")
		err := cmd.Run()
		logs.flush()
		if err != nil {
			_ = os.Remove(partPath)
			logs.event("ffmpeg process exit error", formatWaitError(err))
			if tail := truncateStderr(stderr.String()); tail != "" {
				return fmt.Errorf("%w: %w (stderr: %s)", ErrTranscodeFailed, err, tail)
			}
			return fmt.Errorf("%w: %w", ErrTranscodeFailed, err)
		}
		logs.event("ffmpeg process exited", "")
		return nil
	}

	err := runOnce(opts)
	if err != nil && ctx.Err() == nil && opts.ToneMapMode != tonemap.ModeHardware {
		// Mirror the transport startup retry: a VideoToolbox encode the
		// hardware cannot perform (e.g. at the artifact's dimensions) retries
		// once in software. A frozen hardware tone-map recipe cannot take this
		// shortcut: changing only HWAccel would drop its conversion graph while
		// still tagging the unconverted output as SDR.
		if retryAccel := StartupRetryHWAccel(opts); retryAccel != opts.HWAccel {
			slog.WarnContext(ctx, "prepared encode failed; retrying with software encoding",
				"hw_accel", opts.HWAccel, "output", outputPath, "error", err)
			logs.event("retrying with software encoding", "")
			retryOpts := opts
			retryOpts.HWAccel = retryAccel
			err = runOnce(retryOpts)
		}
	}
	if err != nil {
		return err
	}

	if err := syncPreparedFile(partPath); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("prepare-file: sync artifact: %w", err)
	}
	if err := os.Rename(partPath, outputPath); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("prepare-file: finalize artifact: %w", err)
	}
	if err := syncPreparedDirectory(filepath.Dir(outputPath)); err != nil {
		return fmt.Errorf("prepare-file: sync artifact directory: %w", err)
	}
	return nil
}

func syncPreparedFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return file.Sync()
}

func syncPreparedDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil && runtime.GOOS != directorySyncUnsupportedGOOS {
		return err
	}
	return nil
}

// buildPrepareFileArgs constructs single-file ffmpeg args. It mirrors
// buildFFmpegArgs' input/stream/codec/audio/subtitle handling but emits one
// faststart MP4 instead of HLS segments. Full-file output needs no seek or
// segment-boundary keyframes.
func buildPrepareFileArgs(opts TranscodeOpts, outputPath string) []string {
	opts = normalizeTranscodeOpts(opts)
	isVideoCopy := opts.TargetCodecVideo == "copy"
	isAudioCopy := opts.TargetCodecAudio == "copy"
	if opts.PreparedTracks != nil {
		isAudioCopy = opts.PreparedTracks.allAudioCopied()
	}

	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if opts.PrepareProgressSink != nil {
		// Machine-readable progress on stdout; the output is a file, so stdout
		// is otherwise unused.
		args = append(args, "-progress", "pipe:1", "-nostats")
	}

	if !isVideoCopy {
		args = appendHWAccelArgs(args, opts)
	}
	args = append(args,
		"-fflags", "+genpts+fastseek",
		"-analyzeduration", "3000000",
		"-probesize", "5000000",
	)
	args = append(args, "-i", opts.InputPath)
	args = append(args, "-map_metadata", "-1", "-map_chapters", "-1")
	if opts.PreparedTracks != nil {
		args = appendPreparedTrackArgs(args, opts)
	} else {
		args = appendStreamSelectionArgs(args, opts)
	}

	if isVideoCopy {
		args = append(args, "-c:v", "copy")
	} else {
		opts.preparedFileEncode = true
		args = appendVideoArgs(args, opts)
		if strings.EqualFold(opts.TargetCodecVideo, transcodeCodecHEVC) {
			// Apple players only open HEVC in MP4 under the hvc1 sample entry;
			// FFmpeg's default hev1 plays elsewhere but not on iOS or tvOS.
			args = append(args, "-tag:v", VideoSampleEntryHVC1)
		}
	}
	if isVideoCopy && !isAudioCopy {
		args = append(args, "-threads", "1", "-filter_threads", "1", "-filter_complex_threads", "1")
	}
	if opts.PreparedTracks == nil {
		args = appendAudioArgs(args, opts)
	}

	if !isVideoCopy {
		args = appendVideoFilterArgs(args, opts)
	}

	// One finalized MP4. +faststart relocates the moov atom in a finalization pass
	// (impossible over a pure pipe) so the file is cleanly seekable and resumable.
	args = append(args, "-movflags", "+faststart", "-f", "mp4", "-y", outputPath)
	return args
}
