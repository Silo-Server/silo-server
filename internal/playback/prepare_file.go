package playback

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// ResolvePrepareTarget computes the encode target for a remux/transcode download
// of file, reusing Resolve so a download's encoding matches the streaming
// decision for the same client (no duplicated codec logic).
//
//   - remux: copy video; copy audio unless the client can't decode it (then AAC);
//     keep source resolution.
//   - transcode: H.264/AAC, downscaled to the client's max resolution when the
//     source exceeds it.
func ResolvePrepareTarget(file *models.MediaFile, format string, caps ClientCapabilities, settings AdminSettings) PrepareTarget {
	t := PrepareTarget{Container: "mp4", AudioTrackIndex: -1}
	decision := Resolve(file, caps, settings)
	if format == "remux" {
		t.CodecVideo = "copy"
		if decision.TranscodeAudio {
			t.CodecAudio = "aac"
		} else {
			t.CodecAudio = "copy"
		}
		return t
	}
	// transcode
	t.CodecVideo = "h264"
	t.CodecAudio = "aac"
	if caps.MaxResolution != "" && resolutionOrder(file.Resolution) > resolutionOrder(caps.MaxResolution) {
		t.Resolution = caps.MaxResolution
	}
	return t
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
		args = appendVideoArgs(args, opts)
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
