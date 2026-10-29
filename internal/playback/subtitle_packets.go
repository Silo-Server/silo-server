package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/mediaprobe"
)

// SubtitlePacketInput is a subtitle track read through the source file's
// index (embeddedsub.ReadPackets) instead of demuxed from the whole file.
// The extract functions write it as a subtitle-only stream
// (embeddedsub.WriteStream) to FFmpeg's stdin and run the same conversion
// on it as on the source, so the output bytes are the same.
type SubtitlePacketInput struct {
	Index   *embeddedsub.Index
	Track   *embeddedsub.Track
	Packets []embeddedsub.Packet
	// Stream, when set, opens a subtitle-only stream that is already written,
	// such as a kept track (embeddedsub.WriteKept, or FFmpeg's copy of the
	// track), in the FFmpeg input format StreamFormat. FFmpeg reads it
	// instead of Index, Track and Packets.
	Stream       func() (io.ReadCloser, error)
	StreamFormat string
	// SourceStartTime is the source file's start time as FFmpeg reports it
	// (SubtitleSourceStartTime). FFmpeg subtracts it from every timestamp
	// when it demuxes the source; the packets keep the source's timestamps
	// and the subtitle-only stream has no start time of its own, so the
	// conversion subtracts it explicitly.
	SourceStartTime time.Duration
}

const ffmpegCopyTS = "-copyts"

// inputArgs are the FFmpeg input options that read the packets from stdin
// on the source file's timeline: -copyts keeps the stream's timestamps and
// -itsoffset subtracts the source's start time from them, as a demux of the
// source does.
func (in *SubtitlePacketInput) inputArgs() []string {
	format := in.StreamFormat
	if in.Stream == nil {
		format = in.Index.StreamFormat()
	}
	return []string{
		ffmpegCopyTS, "-itsoffset", formatMicroseconds(-in.SourceStartTime),
		"-f", format, "-i", "pipe:0",
	}
}

func (in *SubtitlePacketInput) validate() error {
	if in.Stream != nil {
		if in.StreamFormat == "" {
			return errors.New("subtitle packet input stream needs a format")
		}
		return nil
	}
	if in.Index == nil || in.Track == nil {
		return errors.New("subtitle packet input needs an index and a track")
	}
	return nil
}

// write writes the subtitle-only stream to w and closes it.
func (in *SubtitlePacketInput) write(w io.WriteCloser) error {
	var err error
	if in.Stream != nil {
		err = copyStream(w, in.Stream)
	} else {
		err = embeddedsub.WriteStream(w, in.Index, in.Track, in.Packets)
	}
	if closeErr := w.Close(); err == nil {
		err = closeErr
	}
	return err
}

func copyStream(w io.Writer, open func() (io.ReadCloser, error)) error {
	r, err := open()
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	if closeErr := r.Close(); err == nil {
		err = closeErr
	}
	return err
}

// formatMicroseconds formats d in seconds to the microsecond, the precision
// FFmpeg keeps time options in, without going through a float.
func formatMicroseconds(d time.Duration) string {
	us := d.Microseconds()
	sign := ""
	if us < 0 {
		sign, us = "-", -us
	}
	return fmt.Sprintf("%s%d.%06d", sign, us/1_000_000, us%1_000_000)
}

// SubtitleSourceStartTime returns the start time FFmpeg gives the file at
// path: ffprobe's format start_time, which FFmpeg subtracts from every
// timestamp when it demuxes the file, or 0 when it reports none. ffprobe
// reads only what FFmpeg probes: the container's header and the first few
// megabytes. Callers keep it with the file's index.
func SubtitleSourceStartTime(ctx context.Context, ffmpegPath, path string) (time.Duration, error) {
	cmd := fspath.CommandContext(ctx, mediaprobe.FFprobePathFromFFmpeg(ffmpegPath),
		"-v", "error", "-show_entries", "format=start_time",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe start time: %w (stderr: %s)", err, truncateStderr(stderr.String()))
	}
	return parseFFprobeSeconds(strings.TrimSpace(string(out)))
}

// parseFFprobeSeconds parses a time ffprobe prints, such as "1.500000", to
// the microsecond; "N/A" and an empty value are 0.
func parseFFprobeSeconds(s string) (time.Duration, error) {
	if s == "" || s == "N/A" {
		return 0, nil
	}
	neg := strings.HasPrefix(s, "-")
	whole, frac, _ := strings.Cut(strings.TrimPrefix(s, "-"), ".")
	if len(frac) > 6 {
		frac = frac[:6]
	}
	frac += strings.Repeat("0", 6-len(frac))
	sec, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse ffprobe time %q: %w", s, err)
	}
	us, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse ffprobe time %q: %w", s, err)
	}
	d := time.Duration(sec)*time.Second + time.Duration(us)*time.Microsecond
	if neg {
		d = -d
	}
	return d, nil
}

// ExtractSubtitlePacketsWithFormat is ExtractSubtitleWithFormat on a track
// read through the index: it converts in's packets to "srt" or "ass" with
// the same FFmpeg output options.
func ExtractSubtitlePacketsWithFormat(ctx context.Context, in *SubtitlePacketInput, outputFormat string, ffmpegPath ...string) ([]byte, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	if err := checkExtractFormat(outputFormat); err != nil {
		return nil, err
	}
	args := append(in.inputArgs(), subtitleFormatOutputArgs(0, outputFormat)...)
	return runSubtitleExtract(ctx, firstFFmpegPath(ffmpegPath), args, in, 0)
}

// startSubtitleCommand starts cmd, writing in's stream to its stdin when in
// is set. wait waits for the command and then for the writer, which ends
// once the command has exited because Wait closes its stdin.
func startSubtitleCommand(cmd *exec.Cmd, in *SubtitlePacketInput) (wait func() (waitErr, feedErr error), err error) {
	if in == nil {
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return func() (error, error) { return cmd.Wait(), nil }, nil
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	feed := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				_ = stdin.Close()
				feed <- fmt.Errorf("write subtitle packets panicked: %v", r)
			}
		}()
		feed <- in.write(stdin)
	}()
	return func() (error, error) {
		waitErr := cmd.Wait()
		return waitErr, <-feed
	}, nil
}
