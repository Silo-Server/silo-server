package playback

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
// size H.264 would, otherwise the output is H.264.
//
// Resolution holds the ladder class label ("1080p") when the source must be
// downscaled and is empty when the source already fits; DownloadScaleResolution
// turns the class back into the exact encoder height for this file.
func ResolveDownloadTranscodeTarget(file *models.MediaFile, caps ClientCapabilities, capKbps int, settings DownloadTranscodeSettings) PrepareTarget {
	source := SourceDescriptorFromFileV3(file, 0)
	codec := transcodeCodecH264
	decoder := downloadDecoderFor(caps, codec, source.FrameRate)
	class := downloadLadderClass(source, decoder, capKbps, codec, settings.MaxHeight)
	if settings.AllowHEVCEncoding && caps.hasDetailedVideoEvidence() {
		if hevc := downloadDecoderFor(caps, transcodeCodecHEVC, source.FrameRate); hevc.ok {
			if hevcClass := downloadLadderClass(source, hevc, capKbps, transcodeCodecHEVC, settings.MaxHeight); hevcClass >= class {
				codec, class, decoder = transcodeCodecHEVC, hevcClass, hevc
			}
		}
	}
	target := PrepareTarget{Container: containerMP4V3, CodecVideo: codec, CodecAudio: audioCodecAACV3, AudioTrackIndex: -1, TargetBitrateKbps: capKbps}
	if width, height := FitLadderBox(source.Width, source.Height, class); width == 0 || width != source.Width || height != source.Height {
		target.Resolution = heightLabel(class)
	}
	if source.BitrateKbps > 0 {
		sourceEquivalent := int(float64(source.BitrateKbps) * codecEfficiency(codec) / codecEfficiency(source.VideoCodec))
		target.TargetBitrateKbps = min(capKbps, max(sourceEquivalent, 1))
	}
	if decoder.maxBitrateKbps > 0 {
		target.TargetBitrateKbps = min(target.TargetBitrateKbps, decoder.maxBitrateKbps)
	}
	return target
}

// DownloadScaleResolution converts a download artifact's ladder class into
// the exact height the encoder scales to, so a cinema-aspect source keeps its
// shape inside the class box. An empty class, or a source already inside the
// box, leaves the source unscaled.
func DownloadScaleResolution(file *models.MediaFile, classLabel string) string {
	class := resolutionHeightV3(classLabel)
	if class <= 0 {
		return classLabel
	}
	source := SourceDescriptorFromFileV3(file, 0)
	width, height := FitLadderBox(source.Width, source.Height, class)
	switch {
	case width == 0:
		return heightLabel(class)
	case width == source.Width && height == source.Height:
		return ""
	default:
		return heightLabel(height)
	}
}

// downloadLadderClass returns the tallest ladder class, at or below the one
// the bitrate earns, whose box-fit output fits the device's decoder. The
// smallest class is the floor: the ladder never drops below it.
func downloadLadderClass(source SourceDescriptorV3, decoder downloadDecoder, capKbps int, codec string, maxHeight int) int {
	top := LadderClassForBitrate(capKbps, source.FrameRate, codec)
	if maxHeight > 0 {
		top = min(top, maxHeight)
	}
	classes := ladderClassesFrom(top)
	for _, class := range classes {
		width, height := FitLadderBox(source.Width, source.Height, class.Height)
		if width == 0 {
			width, height = class.Width, class.Height
		}
		if (decoder.maxWidth == 0 || width <= decoder.maxWidth) && (decoder.maxHeight == 0 || height <= decoder.maxHeight) {
			return class.Height
		}
	}
	return classes[len(classes)-1].Height
}

// h264HighProfileV3 is the profile every H.264 encode here produces.
const h264HighProfileV3 = "high"

// downloadDecoder is the decoder bound a download encode must fit. Zero
// fields are unbounded; ok is false when strict-tier caps list no decoder
// that can take this output.
type downloadDecoder struct {
	maxWidth, maxHeight, maxBitrateKbps int
	ok                                  bool
}

// downloadDecoderFor returns the largest decoder the caps say plays an 8-bit
// download in codec at the source's frame rate. Strict-tier video_decode
// entries are authoritative and prefer hardware decoders, since a download
// plays back later on the same device; an exact-tier profile list must include
// the profile the encode produces, and a level-bound HEVC decoder is skipped
// because the HEVC recipe does not pin a level (the same rule HLS applies).
// Without strict entries the coarse max_resolution ceiling applies.
func downloadDecoderFor(caps ClientCapabilities, codec string, frameRate float64) downloadDecoder {
	if caps.hasDetailedVideoEvidence() {
		softwareOptIn := HasFeatureV3(caps.ClientFeatures, FeatureSoftwareVideoDecodeV3)
		outputProfile := h264HighProfileV3
		if codec == transcodeCodecHEVC {
			outputProfile = hevcMainProfileV3
		}
		var best downloadDecoder
		for _, hardware := range []bool{true, false} {
			for _, decoder := range caps.VideoDecode {
				if decoder.Hardware != hardware || (!hardware && !softwareOptIn) ||
					!strings.EqualFold(decoder.Codec, codec) ||
					(len(decoder.BitDepths) > 0 && !containsIntV3(decoder.BitDepths, 8)) ||
					(decoder.MaxFrameRate > 0 && frameRate > decoder.MaxFrameRate+0.01) ||
					(caps.VideoEvidence == EvidenceExactV3 && len(decoder.Profiles) > 0 && !videoProfileSupportedV3(codec, outputProfile, decoder.Profiles)) ||
					(codec == transcodeCodecHEVC && len(decoder.Levels) > 0) {
					continue
				}
				if !best.ok || boxLarger(decoder.MaxWidth, decoder.MaxHeight, best.maxWidth, best.maxHeight) {
					best = downloadDecoder{maxWidth: decoder.MaxWidth, maxHeight: decoder.MaxHeight, maxBitrateKbps: decoder.MaxBitrateKbps, ok: true}
				}
			}
			if best.ok {
				return best
			}
		}
		if codec != transcodeCodecH264 {
			return downloadDecoder{}
		}
		// H.264 is the universal fallback output; strict caps that list no
		// decoder able to take it still fall through to the coarse ceiling
		// rather than failing, since no output size could satisfy them.
	}
	if height := resolutionHeightV3(caps.MaxResolution); height > 0 {
		width, _ := dimensionsFromResolutionV3(heightLabel(height))
		return downloadDecoder{maxWidth: width, maxHeight: height, ok: true}
	}
	return downloadDecoder{ok: true}
}

// boxLarger reports whether a decoder bound is larger than the current one,
// treating zero as unbounded.
func boxLarger(width, height, currentWidth, currentHeight int) bool {
	if currentWidth == 0 && currentHeight == 0 {
		return false
	}
	if width == 0 && height == 0 {
		return true
	}
	return width*height > currentWidth*currentHeight
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

	runOnce := func(runOpts TranscodeOpts) error {
		args := buildPrepareFileArgs(runOpts, partPath)
		bin := runOpts.FFmpegPath
		if bin == "" {
			bin = ffmpegBinary()
		}

		cmd := exec.CommandContext(ctx, bin, args...)
		stderr := newBoundedTailBuffer(stderrTailMaxBytes)
		cmd.Stderr = stderr
		cmd.WaitDelay = 3 * time.Second

		if err := cmd.Run(); err != nil {
			_ = os.Remove(partPath)
			if tail := truncateStderr(stderr.String()); tail != "" {
				return fmt.Errorf("%w: %w (stderr: %s)", ErrTranscodeFailed, err, tail)
			}
			return fmt.Errorf("%w: %w", ErrTranscodeFailed, err)
		}
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

	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}

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
	args = appendStreamSelectionArgs(args, opts)

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
	args = appendAudioArgs(args, opts)

	if !isVideoCopy {
		args = appendVideoFilterArgs(args, opts)
	}

	// One finalized MP4. +faststart relocates the moov atom in a finalization pass
	// (impossible over a pure pipe) so the file is cleanly seekable and resumable.
	args = append(args, "-movflags", "+faststart", "-f", "mp4", "-y", outputPath)
	return args
}
