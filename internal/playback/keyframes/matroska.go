// Package keyframes reads a media file's video keyframe times from its
// container index, without reading the media itself, and plans HLS segment
// boundaries on them.
package keyframes

import (
	"errors"
	"fmt"
	"io"
	"math"
	"slices"

	"github.com/Silo-Server/silo-server/internal/matroska"
)

// ErrNoIndex means the file has no usable keyframe index: no Cues, none for
// its video track, video cues that aren't independent, or unusable timing
// metadata.
var ErrNoIndex = errors.New("keyframes: no keyframe index")

// notMatroskaError reports a file that isn't Matroska or WebM: a stable fact
// about the file, unlike a read failure.
type notMatroskaError struct{}

func (notMatroskaError) Error() string { return "keyframes: not a Matroska file" }

func isShortRead(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// Index is a file's video keyframe times, in seconds on the container's
// timeline, ascending and without duplicates.
type Index struct {
	Keyframes []float64
	// Duration is the container's duration in seconds, or 0 when unknown.
	Duration float64
	// VideoEnd is when the video track's last frame ends, in seconds on the
	// same timeline as Keyframes, or 0 when unknown. Other tracks can run
	// longer, so it can be earlier than Duration.
	VideoEnd float64
}

// Matroska element IDs, from the Matroska specification.
const (
	idEBML                = matroska.IDEBML
	idSegment             = matroska.IDSegment
	idSeekHead            = matroska.IDSeekHead
	idSeek                = matroska.IDSeek
	idSeekID              = matroska.IDSeekID
	idSeekPosition        = matroska.IDSeekPosition
	idInfo                = matroska.IDInfo
	idTimestampScale      = matroska.IDTimestampScale
	idDuration            = matroska.IDDuration
	idTracks              = matroska.IDTracks
	idTrackEntry          = matroska.IDTrackEntry
	idTrackNumber         = matroska.IDTrackNumber
	idTrackType           = matroska.IDTrackType
	idDefaultDuration     = matroska.IDDefaultDuration
	idTrackTimestampScale = matroska.IDTrackTimestampScale
	idCues                = matroska.IDCues
	idCuePoint            = matroska.IDCuePoint
	idCueTime             = matroska.IDCueTime
	idCueTrackPos         = matroska.IDCueTrackPositions
	idCueTrack            = matroska.IDCueTrack
	idCueCodecState       = matroska.IDCueCodecState
	idCueReference        = matroska.IDCueReference
	idCluster             = matroska.IDCluster
	trackTypeVideo        = matroska.TrackTypeVideo
	defaultTimescale      = matroska.DefaultTimestampScale
	unknownSize           = matroska.UnknownSize
)

// Upper bounds on what the reader loads into memory; anything larger is
// treated as a damaged file. The metadata elements are small. FFmpeg writes
// a video cue point in 17 to 24 bytes, so a film with a keyframe every few
// seconds has Cues well under a megabyte, and 16 MiB holds about 700,000
// points: every frame of 8 hours at 24 fps, or of 3 hours at 60 fps.
// Reading Cues costs at most about three times their size (cuePoints), so
// the limit also bounds what a crafted file can cost.
const (
	maxCuesSize     = 16 << 20
	maxMetadataSize = 16 << 20
)

// ReadMatroska returns the keyframe index of a Matroska or WebM file from its
// Cues element. It reads the header, the seek head, segment info, tracks and
// cues, following the seek head to Cues stored after the clusters. It checks
// the Cues against the block headers of a few clusters spread through the
// file (checkCueCoverage) and of the clusters after the last cue, which give
// the video's end (tailVideoEnd). It never reads the frames, nor most of the
// clusters, so it costs a few reads however large the file is (#2453).
func ReadMatroska(r io.ReaderAt, size int64) (Index, error) {
	id, dataSize, headerLen, err := matroska.ReadElementHeader(r, 0)
	if err != nil {
		if isShortRead(err) {
			// Empty, or not starting with an EBML element at all.
			return Index{}, notMatroskaError{}
		}
		return Index{}, fmt.Errorf("keyframes: read EBML header: %w", err)
	}
	if id != idEBML {
		return Index{}, notMatroskaError{}
	}
	pos := headerLen + dataSize

	id, segSize, headerLen, err := matroska.ReadElementHeader(r, pos)
	if err != nil {
		if isShortRead(err) {
			return Index{}, notMatroskaError{}
		}
		return Index{}, fmt.Errorf("keyframes: read segment: %w", err)
	}
	if id != idSegment {
		return Index{}, notMatroskaError{}
	}
	segStart := pos + headerLen
	segEnd := size
	if segSize != unknownSize && segStart+segSize < segEnd {
		segEnd = segStart + segSize
	}

	var (
		timescale int64 = defaultTimescale
		duration  float64
		video     uint64
		frameNS   uint64
		cuesAt    int64 = -1
		infoAt    int64 = -1
		tracksAt  int64 = -1
		cues      []byte
		haveInfo  bool
		haveTrack bool
	)
	for at := segStart; at < segEnd; {
		id, dataSize, headerLen, err := matroska.ReadElementHeader(r, at)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return Index{}, fmt.Errorf("keyframes: read element at %d: %w", at, err)
		}
		dataAt := at + headerLen
		switch id {
		case idSeekHead:
			data, err := matroska.ReadElementData(r, dataAt, dataSize, maxMetadataSize)
			if err != nil {
				return Index{}, err
			}
			if p, ok := matroska.SeekPosition(data, idCues); ok {
				cuesAt = segStart + p
			}
			if p, ok := matroska.SeekPosition(data, idInfo); ok {
				infoAt = segStart + p
			}
			if p, ok := matroska.SeekPosition(data, idTracks); ok {
				tracksAt = segStart + p
			}
		case idInfo:
			data, err := matroska.ReadElementData(r, dataAt, dataSize, maxMetadataSize)
			if err != nil {
				return Index{}, err
			}
			var ok bool
			if timescale, duration, ok = parseInfo(data); !ok {
				return Index{}, ErrNoIndex
			}
			haveInfo = true
		case idTracks:
			data, err := matroska.ReadElementData(r, dataAt, dataSize, maxMetadataSize)
			if err != nil {
				return Index{}, err
			}
			var scaled bool
			video, frameNS, scaled, haveTrack = firstVideoTrack(data)
			if scaled {
				// The track's timestamps are scaled again, a deprecated
				// feature the times here don't apply; don't plan from them.
				return Index{}, ErrNoIndex
			}
		case idCues:
			cues, err = matroska.ReadElementData(r, dataAt, dataSize, maxCuesSize)
			if err != nil {
				return Index{}, err
			}
		}
		if cues != nil && haveInfo && haveTrack {
			break
		}
		// Clusters hold the media. Stop the linear scan at the first one and
		// jump to the Cues the seek head points at, if they come later.
		if id == idCluster || dataSize == unknownSize {
			break
		}
		at = dataAt + dataSize
	}

	if cues == nil && cuesAt >= 0 {
		id, dataSize, headerLen, err := matroska.ReadElementHeader(r, cuesAt)
		if err != nil {
			return Index{}, fmt.Errorf("keyframes: read cues at %d: %w", cuesAt, err)
		}
		if id != idCues {
			return Index{}, fmt.Errorf("keyframes: seek head points at element %#x, not Cues", id)
		}
		cues, err = matroska.ReadElementData(r, cuesAt+headerLen, dataSize, maxCuesSize)
		if err != nil {
			return Index{}, err
		}
	}
	// Info and Tracks may also come after the clusters, found through the
	// seek head. Info's timestamp scale converts every cue time, so an index
	// without it isn't trusted.
	if !haveInfo && infoAt >= 0 {
		data, err := readSeekTarget(r, infoAt, idInfo)
		if err != nil {
			return Index{}, err
		}
		var ok bool
		if timescale, duration, ok = parseInfo(data); !ok {
			return Index{}, ErrNoIndex
		}
		haveInfo = true
	}
	if !haveTrack && tracksAt >= 0 {
		data, err := readSeekTarget(r, tracksAt, idTracks)
		if err != nil {
			return Index{}, err
		}
		var scaled bool
		video, frameNS, scaled, haveTrack = firstVideoTrack(data)
		if scaled {
			return Index{}, ErrNoIndex
		}
	}
	if cues == nil || !haveTrack || !haveInfo {
		return Index{}, ErrNoIndex
	}

	ticks, clusters, complete := cuePoints(cues, video)
	if !complete || len(ticks) == 0 {
		// A malformed cue point ends the list early, so the points read so
		// far would leave out later keyframes; a dependent video cue isn't
		// a boundary a segment can start at.
		return Index{}, ErrNoIndex
	}
	if err := checkCueCoverage(r, segStart, segEnd, video, ticks, clusters); err != nil {
		return Index{}, err
	}
	end, err := tailVideoEnd(r, segStart, segEnd, video, ticks, clusters)
	if err != nil {
		return Index{}, err
	}
	idx := Index{Keyframes: make([]float64, len(ticks))}
	for i, t := range ticks {
		idx.Keyframes[i] = float64(t) * float64(timescale) / 1e9
	}
	if duration > 0 {
		idx.Duration = duration * float64(timescale) / 1e9
	}
	idx.VideoEnd = end.seconds(timescale, float64(frameNS)/1e9)
	return idx, nil
}

// readSeekTarget reads the element a seek head points at, which must be want.
func readSeekTarget(r io.ReaderAt, at int64, want uint64) ([]byte, error) {
	id, dataSize, headerLen, err := matroska.ReadElementHeader(r, at)
	if err != nil {
		return nil, fmt.Errorf("keyframes: read element at %d: %w", at, err)
	}
	if id != want {
		return nil, fmt.Errorf("keyframes: seek head points at element %#x, not %#x", id, want)
	}
	return matroska.ReadElementData(r, at+headerLen, dataSize, maxMetadataSize)
}

// parseInfo reads Info's timestamp scale, defaulting when it is absent, and
// duration. ok is false for an explicit scale Matroska doesn't allow (zero)
// or that doesn't fit, or incomplete metadata: guessed scaling is unsafe.
func parseInfo(info []byte) (timescale int64, duration float64, ok bool) {
	timescale, ok = defaultTimescale, true
	complete := matroska.ChildrenComplete(info, func(id uint64, v []byte) {
		switch id {
		case idTimestampScale:
			if s := matroska.Uint(v); s > 0 && s <= math.MaxInt64 {
				timescale = int64(s)
			} else {
				ok = false
			}
		case idDuration:
			duration = matroska.Float(v)
		}
	})
	return timescale, duration, complete && ok
}

func firstVideoTrack(tracks []byte) (number, frameNS uint64, scaled, found bool) {
	matroska.Children(tracks, func(id uint64, entry []byte) {
		if id != idTrackEntry || found {
			return
		}
		var n, kind, frame uint64
		trackScale := 1.0
		matroska.Children(entry, func(id uint64, v []byte) {
			switch id {
			case idTrackNumber:
				n = matroska.Uint(v)
			case idTrackType:
				kind = matroska.Uint(v)
			case idDefaultDuration:
				frame = matroska.Uint(v)
			case idTrackTimestampScale:
				trackScale = matroska.Float(v)
			}
		})
		if kind == trackTypeVideo && n > 0 {
			number, frameNS, scaled, found = n, frame, trackScale != 1, true
		}
	})
	return number, frameNS, scaled, found
}

// cuePoints returns the times, in ticks, of the cue points that index the
// video track, and the positions of the clusters holding them, relative to
// the segment's data; both are sorted and without duplicates. Matroska
// muxers index video keyframes, so the times are keyframe times; whether
// every keyframe has one is checked separately (checkCueCoverage).
// It reports false when a malformed cue point stopped the walk early, or
// when a video cue declares frame dependencies or needs decoder state
// outside the initial TrackEntry: that frame can't start a segment.
//
// A crafted Cues element can hold over a million points, so it counts them
// first and allocates each list once, at its final length.
func cuePoints(cues []byte, video uint64) (ticks []uint64, clusters []int64, ok bool) {
	var nTicks, nClusters int
	if !walkCuePoints(cues, video, func(_ uint64, cluster int64) {
		nTicks++
		if cluster >= 0 {
			nClusters++
		}
	}) {
		return nil, nil, false
	}
	ticks = make([]uint64, 0, nTicks)
	clusters = make([]int64, 0, nClusters)
	walkCuePoints(cues, video, func(t uint64, cluster int64) {
		ticks = append(ticks, t)
		if cluster >= 0 {
			clusters = append(clusters, cluster)
		}
	})
	slices.Sort(ticks)
	slices.Sort(clusters)
	return slices.Compact(ticks), slices.Compact(clusters), true
}

// walkCuePoints calls fn for each cue point that indexes the video track,
// with the position of the cluster holding it (negative when the cue doesn't
// say). It reports false when a malformed cue point stopped the walk early,
// or when a video cue isn't independent (see cuePoints).
func walkCuePoints(cues []byte, video uint64, fn func(ticks uint64, cluster int64)) bool {
	independent := true
	complete := matroska.WalkCuePoints(cues, func(t uint64, hasTime bool, pos matroska.CueTrackPosition) {
		if pos.Track != video {
			return
		}
		if pos.HasReference || pos.CodecState != 0 {
			independent = false
		}
		if hasTime {
			fn(t, pos.ClusterPosition)
		}
	})
	return complete && independent
}
