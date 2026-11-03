package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/matroska"
	"github.com/Silo-Server/silo-server/internal/mediaprobe"
)

// FFprobe does not report a Matroska track's TrackNumber, but players that
// demux Matroska themselves (Media3's MatroskaExtractor) identify tracks by
// it. The scanner reads the Tracks element and matches each TrackEntry to the
// FFprobe stream it became, so the planner can name the track in the
// player's terms. The match has to be certain: a wrong ID makes the client
// render a different track than the one the user picked, so anything the
// file's layout cannot confirm leaves the ID empty and the planner falls back
// to server-side extraction.

// matroskaSubtitleStream is the part of a probed subtitle stream the match
// checks: its FFmpeg stream index and FFmpeg codec name.
type matroskaSubtitleStream struct {
	Index int
	Codec string
}

var errMatroskaLayoutMismatch = errors.New("matroska tracks do not match the probed streams")

const (
	ffmpegCodecASS    = matroska.FFmpegCodecASS
	ffmpegCodecSubRip = matroska.FFmpegCodecSubRip
	// Legacy Matroska codec IDs for ASS and SSA subtitles.
	matroskaCodecIDASSLegacy = "S_ASS"
	matroskaCodecIDSSALegacy = "S_SSA"
	// containerMKV is the normalized container of Matroska and WebM files.
	containerMKV = "mkv"
)

// unresolvableMatroskaCodecIDs are legacy aliases FFmpeg accepts but players
// that identify tracks by TrackNumber do not: Media3's MatroskaExtractor
// matches only S_TEXT/ASS and S_TEXT/SSA and drops these tracks entirely, so
// no player track carries the number. Both report as FFmpeg's "ass", so a
// client's capability cannot exclude them. Recording the number would send
// the planner down a native route that fails and replans; without it the
// track keeps server extraction.
var unresolvableMatroskaCodecIDs = map[string]bool{matroskaCodecIDASSLegacy: true, matroskaCodecIDSSALegacy: true}

// ffmpegCreatesMatroskaStream reports whether FFmpeg's Matroska demuxer turns
// a TrackEntry into a stream. It mirrors the checks matroska_parse_tracks
// makes before avformat_new_stream; every other entry becomes the next
// stream, in TrackEntry order, ahead of any attachment streams.
func ffmpegCreatesMatroskaStream(track mediaprobe.MatroskaTrack) bool {
	if track.CodecID == "" {
		return false
	}
	switch first := track.CodecID[0]; track.Type {
	case mediaprobe.MatroskaTrackTypeVideo:
		return first == 'V'
	case mediaprobe.MatroskaTrackTypeAudio:
		return first == 'A'
	case mediaprobe.MatroskaTrackTypeSubtitle, mediaprobe.MatroskaTrackTypeMetadata:
		return first == 'D' || first == 'S'
	default:
		return false
	}
}

// matroskaSubtitleMatch is what the Tracks element says about one probed
// subtitle stream: its canonical TrackNumber, empty when the match can't
// corroborate the stream's codec, and, for a stream FFmpeg probed without a
// codec, the codec the server reads it as through the index
// (matroska.IndexOnlySubtitleCodec), with IndexOnly set.
type matroskaSubtitleMatch struct {
	ID        string
	Codec     string
	IndexOnly bool
}

// matroskaSubtitleTrackIDs matches each probed subtitle stream to its
// TrackEntry, aligned with subtitles. It returns an error when the Tracks
// element and the probed layout disagree anywhere, and an empty ID for a
// track whose codec it cannot corroborate.
//
// The probed layout is the number of playable video and audio streams plus
// the subtitle streams. FFmpeg numbers streams in TrackEntry order, so the
// match holds only when the entries FFmpeg turns into streams have exactly
// those counts per type and every subtitle stream index lands on a subtitle
// entry with the same codec. A stream FFmpeg probed without a codec matches
// an entry whose CodecID it doesn't recognize; only one the server reads
// through the index gets an ID.
func matroskaSubtitleTrackIDs(tracks []mediaprobe.MatroskaTrack, videoStreams, audioStreams int, subtitles []matroskaSubtitleStream) ([]matroskaSubtitleMatch, error) {
	numbers := make(map[uint64]struct{}, len(tracks))
	var streams []mediaprobe.MatroskaTrack
	video, audio, subtitle := 0, 0, 0
	for _, track := range tracks {
		if track.Number == 0 || track.Number > math.MaxUint32 {
			return nil, fmt.Errorf("%w: invalid track number %d", errMatroskaLayoutMismatch, track.Number)
		}
		if _, dup := numbers[track.Number]; dup {
			return nil, fmt.Errorf("%w: duplicate track number %d", errMatroskaLayoutMismatch, track.Number)
		}
		numbers[track.Number] = struct{}{}
		if !ffmpegCreatesMatroskaStream(track) {
			continue
		}
		streams = append(streams, track)
		switch track.Type {
		case mediaprobe.MatroskaTrackTypeVideo:
			video++
		case mediaprobe.MatroskaTrackTypeAudio:
			audio++
		case mediaprobe.MatroskaTrackTypeSubtitle:
			subtitle++
		default:
			// Metadata tracks become streams the catalog does not record,
			// so their position cannot be confirmed.
			return nil, fmt.Errorf("%w: metadata track %d", errMatroskaLayoutMismatch, track.Number)
		}
	}
	if video != videoStreams || audio != audioStreams || subtitle != len(subtitles) {
		return nil, fmt.Errorf("%w: tracks have %d video, %d audio, %d subtitle; probe has %d, %d, %d",
			errMatroskaLayoutMismatch, video, audio, subtitle, videoStreams, audioStreams, len(subtitles))
	}

	matches := make([]matroskaSubtitleMatch, len(subtitles))
	seen := make(map[int]struct{}, len(subtitles))
	for i, sub := range subtitles {
		if sub.Index < 0 || sub.Index >= len(streams) {
			return nil, fmt.Errorf("%w: subtitle stream %d has no track entry", errMatroskaLayoutMismatch, sub.Index)
		}
		if _, dup := seen[sub.Index]; dup {
			return nil, fmt.Errorf("%w: duplicate subtitle stream %d", errMatroskaLayoutMismatch, sub.Index)
		}
		seen[sub.Index] = struct{}{}
		track := streams[sub.Index]
		if track.Type != mediaprobe.MatroskaTrackTypeSubtitle {
			return nil, fmt.Errorf("%w: stream %d is track type %d", errMatroskaLayoutMismatch, sub.Index, track.Type)
		}
		codec := matroska.FFmpegSubtitleCodec(track.CodecID)
		if codec == "" {
			// FFmpeg probes a CodecID it doesn't recognize as a stream with
			// no codec, so only such a stream corroborates it.
			if indexCodec := matroska.IndexOnlySubtitleCodec(track.CodecID); indexCodec != "" && sub.Codec == "" {
				matches[i] = matroskaSubtitleMatch{ID: strconv.FormatUint(track.Number, 10), Codec: indexCodec, IndexOnly: true}
			}
			continue
		}
		if !strings.EqualFold(codec, sub.Codec) {
			return nil, fmt.Errorf("%w: stream %d is %q, track %d is %q",
				errMatroskaLayoutMismatch, sub.Index, sub.Codec, track.Number, track.CodecID)
		}
		if unresolvableMatroskaCodecIDs[track.CodecID] {
			continue
		}
		matches[i].ID = strconv.FormatUint(track.Number, 10)
	}
	return matches, nil
}

// matroskaTracksReadTimeout bounds one Tracks read. The read is a few small
// reads near the start of the file, so only stalled storage takes this long.
var matroskaTracksReadTimeout = 30 * time.Second

var errMatroskaTracksReadTimeout = errors.New("reading Matroska tracks timed out")

// matroskaTracksReadSlots caps Tracks reads in flight across the process. A
// read abandoned on stalled storage keeps its goroutine and open file until
// the kernel returns, so without a cap intermittent stalls could pile them up
// over a long backfill. Healthy reads finish in milliseconds and free their
// slot; the backfill's four workers and concurrent scans fit well under it.
var matroskaTracksReadSlots = make(chan struct{}, 32)

var errMatroskaTracksReadBusy = errors.New("too many Matroska track reads are waiting on stalled storage")

// readMatroskaTracks opens path and reads its Tracks element, returning the
// opened file's info even when the tracks cannot be read.
//
// A call on a stalled network mount cannot be interrupted, so the open, stat
// and read run in their own goroutine, which is abandoned when ctx ends or the
// timeout passes, the same way boundedProbeInputReadable treats storage
// checks. The abandoned goroutine finishes whenever the syscall returns and
// holds one of matroskaTracksReadSlots until then; with every slot held the
// read is refused with errMatroskaTracksReadBusy.
func readMatroskaTracks(ctx context.Context, path string) ([]mediaprobe.MatroskaTrack, os.FileInfo, error) {
	type readResult struct {
		tracks []mediaprobe.MatroskaTrack
		info   os.FileInfo
		err    error
	}
	// One read of the setting: the goroutine must release the slot it took.
	slots := matroskaTracksReadSlots
	select {
	case slots <- struct{}{}:
	default:
		return nil, nil, errMatroskaTracksReadBusy
	}
	done := make(chan readResult, 1)
	go func() {
		tracks, info, err := readMatroskaTracksFile(path)
		<-slots
		done <- readResult{tracks, info, err}
	}()
	timer := time.NewTimer(matroskaTracksReadTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.tracks, r.info, r.err
	case <-timer.C:
		return nil, nil, errMatroskaTracksReadTimeout
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func readMatroskaTracksFile(path string) ([]mediaprobe.MatroskaTrack, os.FileInfo, error) {
	f, err := fspath.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	tracks, err := mediaprobe.ReadMatroskaTracks(f, info.Size())
	// Stat again after the read: a file written while it was read reports its
	// new size or mtime, so the caller sees it changed instead of trusting
	// tracks read from a revision that no longer exists.
	info, statErr := f.Stat()
	if statErr != nil {
		return nil, nil, statErr
	}
	return tracks, info, err
}

// applyMatroskaSubtitleTrackIDs fills the container track ID of probed
// Matroska subtitle tracks that FFprobe left without one, and the codec of
// one FFprobe couldn't name that the server reads through the index. Failure
// is not a probe failure: the tracks keep an empty ID and play through
// extraction.
func applyMatroskaSubtitleTrackIDs(ctx context.Context, filePath string, probe *ProbeData) {
	if probe == nil || probe.Container != containerMKV || !subtitleTracksMissingContainerID(probe.SubtitleTracks) {
		return
	}
	subtitles := make([]matroskaSubtitleStream, len(probe.SubtitleTracks))
	for i, track := range probe.SubtitleTracks {
		subtitles[i] = matroskaSubtitleStream{Index: track.Index, Codec: track.Codec}
	}
	tracks, _, err := readMatroskaTracks(ctx, filePath)
	var matches []matroskaSubtitleMatch
	if err == nil {
		matches, err = matroskaSubtitleTrackIDs(tracks, len(probe.VideoTracks), len(probe.AudioTracks), subtitles)
	}
	if err != nil {
		slog.DebugContext(ctx, "scanner: Matroska subtitle track numbers not recorded",
			"component", "scanner", "path", filePath, "error", err)
		return
	}
	for i := range probe.SubtitleTracks {
		track := &probe.SubtitleTracks[i]
		if track.ContainerTrackID != "" {
			continue
		}
		track.ContainerTrackID = matches[i].ID
		if matches[i].IndexOnly {
			track.Codec, track.IndexOnly = matches[i].Codec, true
		}
	}
}

func subtitleTracksMissingContainerID(tracks []SubtitleTrackInfo) bool {
	for _, track := range tracks {
		if track.ContainerTrackID == "" {
			return true
		}
	}
	return false
}
