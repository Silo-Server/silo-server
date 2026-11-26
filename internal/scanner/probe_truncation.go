package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/mediaprobe"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// errTruncatedMedia reports a file whose header reads but whose media data
// stops well before the length that header gives, such as a download or copy
// that was cut off. ffprobe reads such a file without an error, so
// IsProbeRejection counts this error as a refusal of the file's content.
var errTruncatedMedia = errors.New("the file's media data ends before the length its header gives")

// truncatedTailSeconds is how far before the end its header gives a file's
// data may stop before the file counts as cut short. A file that plays to
// within a few seconds of its end is left alone. The check reads the file
// from this far before its end.
const truncatedTailSeconds = 5.0

// truncationMarkers are FFmpeg demuxer messages, lowercased, that say a file
// ended before its own structure did: the MP4 demuxer's for a sample that
// lies past the end of the file, and the Matroska demuxer's for an element
// cut off by it.
var truncationMarkers = []string{
	"partial file",
	"file ended prematurely",
}

// indexedContainer reports whether formatName is a container whose header
// says where and how long its media data is: MP4/QuickTime (and so M4A/M4B)
// and Matroska/WebM. Their duration is read, not estimated from the bitrate
// as with MPEG-TS or MP3, so data that stops short of it is missing.
func indexedContainer(formatName string) bool {
	for name := range strings.SplitSeq(formatName, ",") {
		if name == "mov" || name == "matroska" {
			return true
		}
	}
	return false
}

// truncationReadTimeout bounds each read of a file's last seconds. A seek
// usually reads one keyframe interval and a second, but a Matroska file
// without Cues, as a cut-off copy is, is read from its start to find them. A
// read that does not finish in time leaves the file treated as whole.
const truncationReadTimeout = 30 * time.Second

// errTailUnsettled reports a read of a file's last seconds that settled
// nothing: ffprobe failed or timed out, or printed no packet list.
var errTailUnsettled = errors.New("the end of the file could not be read")

// codecTypeAudio is ffprobe's codec_type of an audio stream.
const codecTypeAudio = "audio"

// checkMediaReachesEnd reads a file with an indexed container from
// truncatedTailSeconds before its end and returns errTruncatedMedia when its
// audio and video stop before that point, and ffprobe either found nothing
// to read there (an MP4 whose index points past the end of the file) or
// reported that the file ended early. Requiring both keeps a whole file whose
// header overstates its length, which ffprobe reads to its real end without
// complaint, from being called damaged. ffprobe can also end that read
// quietly on a storage error, so a file that looks cut short is read a second
// time, and only two reads that agree make it damaged.
//
// The end is the header's duration taken as a timestamp. When the file's
// timestamps start later, its real end is later still, so the check is only
// more lenient.
//
// A Matroska file without Cues is not checked: FFmpeg could only reach its
// end by reading every cluster before it, and a file written without Cues,
// such as a live recording, is usually whole.
//
// A read that fails or times out settles nothing. The library scan then
// treats the file as whole; playback's repair probe (limits.unsettledTailFails)
// fails instead, so it cannot clear a damaged mark without checking the file.
// A storage error ffprobe reports fails the probe either way. None of these
// errors is one IsProbeRejection counts against the file, so the probe is
// retried later.
func checkMediaReachesEnd(ctx context.Context, ffprobePath, filePath string, raw *ffprobeOutput, limits probeLimits) error {
	start := parseFloat(raw.Format.StartTime)
	end := parseFloat(raw.Format.Duration)
	// media maps the streams with a timeline to whether they are the file's
	// audio or video; data streams, such as an MP4 timecode track whose one
	// sample sits at the front of the file, and attachments are left out.
	media := make(map[int]bool)
	hasMedia := false
	for _, s := range raw.Streams {
		switch {
		case s.CodecType == codecTypeAudio || mediaprobe.IsPrimaryVideoStream(s):
			media[s.Index], hasMedia = true, true
		case s.CodecType == "subtitle":
			media[s.Index] = false
		}
	}
	if !indexedContainer(raw.Format.FormatName) || !durationIsPositiveFinite(end) || math.IsNaN(start) || math.IsInf(start, 0) || !hasMedia {
		return nil
	}
	seek := max(start, end-truncatedTailSeconds)
	mp4 := slices.Contains(strings.Split(raw.Format.FormatName, ","), "mov")
	if !mp4 {
		// A header that cannot be read leaves the question open, so the
		// file is checked.
		if hasCues, _, err := readMatroskaHeader(ctx, filePath, mediaprobe.MatroskaHasCues); err == nil && !hasCues {
			slog.DebugContext(ctx, "scanner: Matroska file has no Cues; not checking that it reaches its end", "component", "scanner", "path", filePath)
			return nil
		}
	}
	err := readMediaTail(ctx, ffprobePath, filePath, seek, end, media, mp4, limits)
	if !errors.Is(err, errTruncatedMedia) {
		return ignoreUnsettledTail(ctx, filePath, err, limits)
	}
	switch again := readMediaTail(ctx, ffprobePath, filePath, seek, end, media, mp4, limits); {
	case again == nil:
		slog.WarnContext(ctx, "scanner: a second read of the end of the file found it whole; check the storage it lives on", "component", "scanner", "path", filePath, "first_read", err)
		return nil
	case errors.Is(again, errTruncatedMedia):
		return err
	default:
		return ignoreUnsettledTail(ctx, filePath, again, limits)
	}
}

// ignoreUnsettledTail returns err unless it is errTailUnsettled and limits
// let such a read leave the file treated as whole, which it logs.
func ignoreUnsettledTail(ctx context.Context, filePath string, err error, limits probeLimits) error {
	if !errors.Is(err, errTailUnsettled) || limits.unsettledTailFails {
		return err
	}
	slog.DebugContext(ctx, "scanner: could not read the end of the file to check it is whole", "component", "scanner", "path", filePath, "error", err)
	return nil
}

// readMediaTail reads filePath from the keyframe before seek to a second past
// it and returns errTruncatedMedia when the audio and video in media stop
// before seek as checkMediaReachesEnd describes, a storage error ffprobe
// reported, errTailUnsettled, or nil. Only packets of the streams in media
// count as something found there; mp4 reports an MP4/QuickTime file.
func readMediaTail(ctx context.Context, ffprobePath, filePath string, seek, end float64, media map[int]bool, mp4 bool, limits probeLimits) error {
	runCtx, cancel := context.WithTimeout(ctx, min(limits.probe, truncationReadTimeout))
	defer cancel()
	interval := strconv.FormatFloat(seek, 'f', 6, 64) + "%" + strconv.FormatFloat(seek+1, 'f', 6, 64)
	// -read_intervals goes first: ffprobe 8.0 skips every packet when an open
	// interval follows a -show_entries naming several packet fields.
	cmd := fspath.CommandContext(runCtx, ffprobePath,
		"-read_intervals", interval,
		"-v", "error",
		"-print_format", "json",
		"-show_entries", "packet=stream_index,pts_time,dts_time,duration_time",
		filePath,
	)
	cmd.WaitDelay = limits.waitDelay
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	processmetrics.Record(processmetrics.Probe, cmd.ProcessState, err, runCtx.Err())
	// A storage error counts whether or not ffprobe went on to exit 0.
	if accessErr := probeStderrAccessFailure(stderr.Bytes()); accessErr != nil {
		return fmt.Errorf("reading the end of %s to check it is whole failed: %w", filePath, accessErr)
	}
	if err != nil {
		// Its text, not the error: the exit error must not make
		// IsProbeRejection blame the file.
		return fmt.Errorf("%w: %s: %s", errTailUnsettled, filePath, err.Error())
	}
	var tail struct {
		Packets *[]struct {
			StreamIndex int    `json:"stream_index"`
			PTS         string `json:"pts_time"`
			DTS         string `json:"dts_time"`
			Duration    string `json:"duration_time"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(output, &tail); err != nil || tail.Packets == nil {
		return fmt.Errorf("%w: %s: ffprobe printed no packet list", errTailUnsettled, filePath)
	}

	// A subtitle cue that outlasts the cut says nothing about the audio and
	// video, so only their packets count toward where the data ends. Any of
	// their packets counts as something found, since a seek lands on the
	// keyframe before seek. In an MP4 a subtitle cue counts only when it
	// reaches seek: the MP4 demuxer returns the last cue before seek from the
	// surviving front of a cut file. A Matroska file cut short says so (a
	// truncation marker), so any of its cues counts.
	found, dataEnd := false, math.Inf(-1)
	for _, packet := range *tail.Packets {
		isMedia, timed := media[packet.StreamIndex]
		if !timed {
			continue
		}
		found = found || isMedia
		at, err := strconv.ParseFloat(packet.PTS, 64)
		if err != nil {
			if at, err = strconv.ParseFloat(packet.DTS, 64); err != nil {
				continue
			}
		}
		length, err := strconv.ParseFloat(packet.Duration, 64)
		if err != nil || length < 0 {
			length = 0
		}
		found = found || !mp4 || at+length >= seek
		if isMedia {
			dataEnd = max(dataEnd, at+length)
		}
	}
	if dataEnd >= seek {
		return nil
	}
	if !found {
		return fmt.Errorf("%w: %s has no media data after %.1fs of %.1fs", errTruncatedMedia, filePath, seek, end)
	}
	lower := strings.ToLower(stderr.String())
	if !slices.ContainsFunc(truncationMarkers, func(marker string) bool { return strings.Contains(lower, marker) }) {
		return nil
	}
	return fmt.Errorf("%w: %s has audio and video to %.1fs of %.1fs", errTruncatedMedia, filePath, dataEnd, end)
}
