// Package embeddedsub reads embedded subtitle tracks through a media file's
// own index instead of demuxing the whole file.
//
// A subtitle track's packets are spread through the whole file, so FFmpeg
// reads all of it to extract one; on network storage that takes minutes. A
// Matroska file's Cues usually point at every subtitle block (every FFmpeg
// muxed file and every mkvmerge 6.2 or later one), so a whole track takes a
// few hundred small reads and a time window a handful. MP4 and MOV text
// tracks are read through their sample tables.
//
// ReadIndex reads a file's subtitle tracks and where their packets are;
// ReadPackets reads one track's packets in a time window. WriteMKS writes
// packets back out as a subtitle-only Matroska stream, so the FFmpeg
// conversion that serves a subtitle can run on that stream instead of on the
// source file and produce the same bytes (playback.SubtitlePacketInput).
//
// An Index is immutable and safe for concurrent use; ReadPackets opens the
// file itself on each call. Times are on the container's timeline, before
// FFmpeg subtracts the file's start time.
package embeddedsub

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/matroska"
)

var (
	// ErrNotSupportedContainer means the file is neither Matroska nor an
	// MP4 or MOV file the reader understands.
	ErrNotSupportedContainer = errors.New("embeddedsub: not a Matroska or MP4 file")
	// ErrMalformed means the file's metadata or index is damaged: the
	// reader can't find its tracks or their packets. The caller falls back
	// to FFmpeg, which may still demux it.
	ErrMalformed = errors.New("embeddedsub: malformed file")
	// ErrTooLarge means a read would hold more packet data than
	// ReadOptions.MaxBytes allows.
	ErrTooLarge = errors.New("embeddedsub: subtitle packets over the read's memory limit")
	// ErrTrackNotFound means the index has no subtitle track with the
	// requested number.
	ErrTrackNotFound = errors.New("embeddedsub: no such subtitle track")
	// ErrNoCues means the track's packets can't be found from the index:
	// the file has no Cues, or none for this track. The caller falls back to
	// demuxing the file (FFmpeg).
	ErrNoCues = errors.New("embeddedsub: track has no cues")
	// ErrIncompleteCues means the track's cue count disagrees with the
	// NUMBER_OF_FRAMES its muxer recorded, so reading the cues would miss
	// packets. ReadOptions.AllowIncomplete reads it anyway.
	ErrIncompleteCues = errors.New("embeddedsub: track's cues leave out packets")
	// ErrUnsupported means the track stores its packets in a way the reader
	// doesn't decode (Track.Unsupported says how). The caller falls back to
	// FFmpeg.
	ErrUnsupported = errors.New("embeddedsub: track encoding not supported")
	// ErrFileChanged means the file's size or modification time differs
	// from when the index was read.
	ErrFileChanged = errors.New("embeddedsub: file changed since it was indexed")
	// ErrDamaged means some packets the index points at couldn't be read: the
	// data there isn't the track's block, is cut off, or doesn't decode.
	// ReadPackets returns it, wrapped in a *DamagedError, together with the
	// packets it did read.
	ErrDamaged = errors.New("embeddedsub: damaged subtitle data")
)

// DamagedError reports packets ReadPackets couldn't read. It matches
// ErrDamaged.
type DamagedError struct {
	// Cues is how many index entries pointed at unreadable data.
	Cues int
	// First describes the first of them.
	First error
}

func (e *DamagedError) Error() string {
	return fmt.Sprintf("embeddedsub: %d subtitle packets unreadable, first: %v", e.Cues, e.First)
}

// Is reports a match with ErrDamaged.
func (e *DamagedError) Is(target error) bool { return target == ErrDamaged }

// Unwrap returns the first failure.
func (e *DamagedError) Unwrap() error { return e.First }

// Completeness says whether a track's cues index every packet.
type Completeness string

const (
	// CompletenessComplete: the cue count equals the NUMBER_OF_FRAMES tag.
	CompletenessComplete Completeness = "complete"
	// CompletenessUnknown: the track has cues but no NUMBER_OF_FRAMES tag to
	// check them against. FFmpeg never writes the tag, and FFmpeg-muxed
	// files cue every subtitle packet.
	CompletenessUnknown Completeness = "unknown"
	// CompletenessIncomplete: the cue count differs from NUMBER_OF_FRAMES.
	CompletenessIncomplete Completeness = "incomplete"
	// CompletenessNone: the track has no cues.
	CompletenessNone Completeness = "none"
)

// Container is the kind of file an Index describes.
type Container string

const (
	ContainerMatroska Container = "matroska"
	ContainerMP4      Container = "mp4"
)

// Index is what a file's metadata and index say about its subtitle tracks.
// It is immutable once read and safe for concurrent use.
type Index struct {
	Container Container
	// Size and ModTime identify the file version the index describes;
	// ReadPackets refuses a file that no longer matches.
	Size    int64
	ModTime time.Time
	// TimestampScale is the nanoseconds per tick of the packet timestamps
	// WriteMKS writes: Matroska's TimestampScale, or 1 for MP4.
	TimestampScale int64
	// Tracks are the subtitle tracks, in the container's track order.
	Tracks []Track
	// Stats are the reads ReadIndex made.
	Stats ReadStats

	// segmentEnd is where the Matroska segment's data ends.
	segmentEnd int64
}

// Track returns the subtitle track with the given number: the Matroska
// TrackNumber or the MP4 track ID, which the scanner records as a track's
// container_track_id.
func (idx *Index) Track(number uint64) (*Track, bool) {
	for i := range idx.Tracks {
		if idx.Tracks[i].Number == number {
			return &idx.Tracks[i], true
		}
	}
	return nil, false
}

// Compression is a track's per-packet compression.
type Compression struct {
	// Algorithm is a matroska.Compression* value: zlib, bzlib or header
	// stripping.
	Algorithm uint64
	// Settings is the header-stripping prefix.
	Settings []byte
}

// Track is one subtitle track. Its fields are read-only.
type Track struct {
	// Number is the Matroska TrackNumber or MP4 track ID.
	Number uint64
	// UID is the Matroska TrackUID; 0 for MP4.
	UID uint64
	// CodecID is the Matroska CodecID (S_TEXT/UTF8, S_TEXT/ASS,
	// S_HDMV/PGS...) or, for MP4, the sample entry type (tx3g, or text for
	// QuickTime text).
	CodecID string
	// CodecPrivate is the codec's private data, decompressed: the ASS
	// script header, or the tx3g sample description.
	CodecPrivate []byte
	// Compression is how each packet is compressed; nil when it isn't.
	Compression *Compression
	// Language is the ISO 639-2 language, "eng" when the entry omits it, as
	// Matroska specifies; LanguageBCP47, when present, supersedes it.
	Language      string
	LanguageBCP47 string
	Name          string
	Enabled       bool
	Default       bool
	Forced        bool
	// HearingImpaired is FlagHearingImpaired, false when absent.
	HearingImpaired bool
	// DefaultDuration is the track's default packet duration, or 0.
	DefaultDuration time.Duration
	// Cues are a Matroska track's index entries, in file order. An MP4
	// track's sample table lives unexported in the track.
	Cues []Cue
	// FrameCount is the NUMBER_OF_FRAMES statistics tag, or -1; for MP4
	// the sample count.
	FrameCount int64
	// Completeness rates the cues against FrameCount. An MP4 track's sample
	// table lists every sample, so it is complete when it has any.
	Completeness Completeness
	// Unsupported says why ReadPackets can't read the track's packets
	// (encryption, a compression the reader lacks...), or is empty.
	Unsupported string

	// trackType is the Matroska TrackType WriteMKS writes.
	trackType uint64
	// maxCueDuration is the longest CueDuration; allCueDurations reports
	// whether every cue has one.
	maxCueDuration  time.Duration
	allCueDurations bool
	// mp4 holds an MP4 track's sample table.
	mp4 *mp4Track
}

// FFmpegCodec is the codec name FFmpeg gives the track: what a probe of the
// source records as the stream's codec. It is "" for a Matroska CodecID
// FFmpeg doesn't recognize.
func (t *Track) FFmpegCodec() string {
	if t.mp4 != nil {
		return "mov_text"
	}
	return matroska.FFmpegSubtitleCodec(t.CodecID)
}

// Cue is one index entry: where one packet of the track is.
type Cue struct {
	// Time is the CueTime: the packet's presentation time.
	Time time.Duration
	// ClusterPosition is the file offset of the cluster holding the block.
	ClusterPosition int64
	// RelativePosition is the block's offset within the cluster's data, or
	// -1 when the cue doesn't say and the cluster has to be scanned.
	RelativePosition int64
	// Duration is the CueDuration, or 0 when the cue has none.
	Duration time.Duration
}

// Packet is one subtitle packet.
type Packet struct {
	// PTS is the presentation time on the container's timeline.
	PTS time.Duration
	// Duration is how long the packet displays: the block's duration,
	// else its cue's, else the track's default; 0 when none says. PGS
	// packets usually have none: each display set lasts until the next.
	Duration time.Duration
	// Data is the packet's payload, decompressed.
	Data []byte

	// pos is the file offset of the block, and lace the frame's index in
	// it; they order packets as the file stores them. laces is how many
	// frames the block holds.
	pos   int64
	lace  int
	laces int
	// inferredDuration reports that Duration came from the cue or the
	// track's default rather than the block, so WriteMKS leaves the block's
	// duration out, as the source did.
	inferredDuration bool
	// nonKeyframe is set for a block FFmpeg doesn't read as a keyframe:
	// one with references, or a laced frame after the first.
	nonKeyframe bool
	// mp4DTS and mp4Delta are an MP4 sample's decode time and duration in
	// media ticks, which the MP4 stream WriteStream writes keeps.
	mp4DTS   int64
	mp4Delta uint32
}

// End is when the packet stops displaying, or PTS when its duration is
// unknown.
func (p Packet) End() time.Duration { return p.PTS + p.Duration }

// ReadStats reports what a read cost.
type ReadStats struct {
	// Reads and Bytes count the read calls and bytes read.
	Reads int64
	Bytes int64
	Wall  time.Duration
	// Cues is how many index entries were read; Clusters how many cluster
	// headers; ScannedClusters how many clusters were walked for blocks
	// whose cues have no CueRelativePosition.
	Cues            int
	Clusters        int
	ScannedClusters int
}

// Whole bounds a read of a whole track: ReadPackets(ctx, path, idx, track,
// WholeFrom, WholeTo, opts) returns every packet.
const (
	WholeFrom = time.Duration(math.MinInt64)
	WholeTo   = time.Duration(math.MaxInt64)
)

// memSize estimates the bytes an Index keeps alive.
func (idx *Index) memSize() int64 {
	const cueSize, trackSize, sampleSize = 32, 256, 24
	n := int64(128)
	for i := range idx.Tracks {
		t := &idx.Tracks[i]
		n += trackSize + int64(len(t.CodecPrivate)+len(t.Language)+len(t.LanguageBCP47)+len(t.Name)+len(t.CodecID))
		n += int64(len(t.Cues)) * cueSize
		if t.mp4 != nil {
			n += int64(len(t.mp4.samples))*sampleSize + int64(t.mp4.boxBytes())
		}
	}
	return n
}

// sortedCueTimes returns the track's cue times in ascending order.
func (t *Track) sortedCueTimes() []time.Duration {
	times := make([]time.Duration, len(t.Cues))
	for i, c := range t.Cues {
		times[i] = c.Time
	}
	slices.Sort(times)
	return times
}

// nextStart returns the first cue time after p in times (ascending), or
// WholeTo.
func nextStart(times []time.Duration, p time.Duration) time.Duration {
	i, found := slices.BinarySearch(times, p)
	for found && i < len(times) && times[i] == p {
		i++
	}
	if i >= len(times) {
		return WholeTo
	}
	return times[i]
}

// satSub is a-b, saturating at the int64 range.
func satSub(a, b time.Duration) time.Duration {
	if b > 0 && a < math.MinInt64+b {
		return math.MinInt64
	}
	if b < 0 && a > math.MaxInt64+b {
		return math.MaxInt64
	}
	return a - b
}

// satAdd is a+b, saturating at the int64 range.
func satAdd(a, b time.Duration) time.Duration {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

// overlaps reports whether a packet starting at p and ending at end shows
// any time in [from, to).
func overlaps(p, end, from, to time.Duration) bool {
	return p < to && end > from
}
