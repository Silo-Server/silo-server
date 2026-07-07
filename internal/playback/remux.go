package playback

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

var (
	resolvedFFmpegPath string
	ffmpegOnce         sync.Once

	doviRPUAvailable bool
	doviRPUOnce      sync.Once
)

// ffmpegBinary returns the path to the ffmpeg binary.
// Resolved once at first call, then cached for the process lifetime.
func ffmpegBinary() string {
	ffmpegOnce.Do(func() {
		const jellyfinPath = "/usr/lib/jellyfin-ffmpeg/ffmpeg"
		if _, err := exec.LookPath(jellyfinPath); err == nil {
			resolvedFFmpegPath = jellyfinPath
			return
		}
		resolvedFFmpegPath = "ffmpeg"
	})
	return resolvedFFmpegPath
}

// supportsDoviRPUFilter reports whether the resolved ffmpeg binary ships the
// dovi_rpu bitstream filter (FFmpeg 7.1+). Probed once per process.
func supportsDoviRPUFilter() bool {
	doviRPUOnce.Do(func() {
		out, err := exec.Command(ffmpegBinary(), "-hide_banner", "-bsfs").Output()
		doviRPUAvailable = err == nil && bytes.Contains(out, []byte("dovi_rpu"))
		if !doviRPUAvailable {
			slog.Warn("ffmpeg lacks the dovi_rpu bitstream filter (needs FFmpeg 7.1+); " +
				"Dolby Vision profile 7 remuxes will keep their dangling dual-layer RPUs")
		}
	})
	return doviRPUAvailable
}

// remuxDVProfile neutralizes a Dolby Vision profile the local ffmpeg cannot
// handle. Profile 7 is the only profile that triggers an RPU strip in
// buildRemuxArgs; when the dovi_rpu filter is unavailable the remux must
// still start (an unknown bitstream filter aborts ffmpeg immediately), so
// fall back to the pre-strip behavior instead of failing playback.
func remuxDVProfile(dvProfile int, canStripRPU bool) int {
	if dvProfile == 7 && !canStripRPU {
		return 0
	}
	return dvProfile
}

// RemuxSession represents a running ffmpeg remux process that copies
// codecs to a new container format without re-encoding.
type RemuxSession struct {
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	outputPipe io.ReadCloser
}

// buildRemuxArgs constructs the ffmpeg argument list for a remux operation.
// The args perform codec copy (-c copy) into the target container format,
// using fragmented output for streaming (frag_keyframe+empty_moov+default_base_moof) and
// pipe:1 for stdout output.
// When transcodeAudio is true, video is copied but audio is transcoded to
// stereo AAC (handles cases like DTS/TrueHD that browsers cannot decode).
// dvProfile is the file's Dolby Vision profile (0 = none). Profile 7 remuxes
// strip DV RPUs: the enhancement layer is dropped by the video map below, so
// the RPUs would dangle — stripping yields a clean HDR10 base layer (the
// Apple-parity fallback for devices without a P7 decoder). Profile 8 RPUs
// stay: the base layer is self-contained and DV clients can render it.
func buildRemuxArgs(filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int) []string {
	args := []string{
		"-nostdin",
		"-hide_banner",
		"-loglevel", "error",
		// Cap the input probe to speed up startup on large MKV containers
		// (defaults scan up to 5 seconds / several MB). Mirrors the transcode
		// pipeline, which has run these caps in production without issue.
		"-fflags", "+genpts+fastseek",
		"-analyzeduration", "3000000",
		"-probesize", "5000000",
	}

	// Add seek if requested (before input for fast seek).
	if seekSeconds > 0 {
		args = append(args, "-ss", strconv.FormatFloat(seekSeconds, 'f', 3, 64))
		if transcodeAudio {
			// In copy mode (-c:v copy) video must start at a keyframe, but
			// accurate_seek (the default) trims re-encoded audio to the exact
			// seek point. This mismatch causes A/V desync equal to the gap
			// between the keyframe and the seek point. Disabling accurate seek
			// keeps both streams aligned at the keyframe boundary.
			args = append(args, "-noaccurate_seek")
		}
	}

	args = append(args, "-i", filePath)

	// Strip metadata/chapters and skip subtitle/data streams — the remux
	// output is fed to an HTML <video> tag, none of it is consumed.
	args = append(args,
		"-map_metadata", "-1",
		"-map_chapters", "-1",
	)

	// Select specific video and audio streams.
	args = append(args, "-map", "0:v:0")
	if audioTrackIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:a:%d?", audioTrackIndex))
	} else {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-sn", "-dn")

	if dvProfile == 7 {
		args = append(args, "-bsf:v", "dovi_rpu=strip=1")
	}

	if transcodeAudio {
		// Video copy + stereo AAC encode is effectively single-threaded work.
		// ffmpeg's default auto-threading spawns one filter thread per CPU
		// core for the implicit downmix/resampler, all idle. Pin to one.
		args = append(args,
			"-threads", "1",
			"-filter_threads", "1",
			"-filter_complex_threads", "1",
			"-c:v", "copy",
			"-c:a", "aac",
			"-ac", "2",
			"-b:a", "192k",
		)
	} else {
		args = append(args, "-c", "copy")
	}

	args = append(args,
		"-avoid_negative_ts", "make_zero",
		"-f", outputFormat,
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"pipe:1",
	)

	return args
}

// StartRemux starts an ffmpeg process that copies codecs to a new container.
// When transcodeAudio is false the command is:
//
//	ffmpeg -i {input} -c copy -f {format} -movflags frag_keyframe+empty_moov+default_base_moof pipe:1
//
// When transcodeAudio is true video is copied but audio is transcoded to AAC.
// The caller must call Close() when done to clean up resources.
func StartRemux(ctx context.Context, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int) (*RemuxSession, error) {
	ctx, cancel := context.WithCancel(ctx)

	bin := ffmpegBinary()
	args := buildRemuxArgs(filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex,
		remuxDVProfile(dvProfile, supportsDoviRPUFilter()))
	cmd := exec.CommandContext(ctx, bin, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	return &RemuxSession{
		cmd:        cmd,
		cancel:     cancel,
		outputPipe: stdout,
	}, nil
}

// Read implements io.Reader, piping ffmpeg stdout to the caller.
func (s *RemuxSession) Read(p []byte) (int, error) {
	return s.outputPipe.Read(p)
}

// Close stops the ffmpeg process and cleans up all resources.
// It is safe to call Close multiple times.
func (s *RemuxSession) Close() error {
	s.cancel()
	// Drain the pipe so cmd.Wait does not block.
	_, _ = io.Copy(io.Discard, s.outputPipe)
	return s.cmd.Wait()
}

// containerMIME maps output format names to MIME types for HTTP responses.
func containerMIME(format string) string {
	switch format {
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "matroska":
		return "video/x-matroska"
	case "mpegts":
		return "video/mp2t"
	default:
		return "application/octet-stream"
	}
}

// ServeRemux streams a remuxed file to the HTTP response.
// It starts an ffmpeg remux session and copies the output directly to the
// response writer. The response is streamed (chunked transfer) since the
// total size is not known in advance.
// When transcodeAudio is true, audio is transcoded to AAC while video is copied.
func ServeRemux(w http.ResponseWriter, r *http.Request, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int) error {
	// Check file exists before starting ffmpeg to return a proper 404.
	// Headers must be written before streaming begins, so we can't detect
	// ffmpeg errors after WriteHeader(200) has been sent.
	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
			return err
		}
		http.Error(w, "failed to access file", http.StatusInternalServerError)
		return err
	}

	session, err := StartRemux(r.Context(), filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile)
	if err != nil {
		http.Error(w, "failed to start remux", http.StatusInternalServerError)
		return err
	}
	defer session.Close()

	w.Header().Set("Content-Type", containerMIME(outputFormat))
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)

	// Stream ffmpeg output to the HTTP response.
	buf := make([]byte, 32*1024) // 32 KB buffer
	for {
		n, readErr := session.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return nil // Client disconnected.
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if readErr != nil {
			return nil // EOF or error — done streaming.
		}
	}
}
