package keyframes

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
)

// ErrIncompleteIndex means the file's Cues leave out some video keyframes.
// Matroska recommends a cue for every video keyframe but doesn't require
// one, and a plan built on a partial list would cut segments where FFmpeg
// doesn't. It is reported as ErrNoIndex.
var ErrIncompleteIndex = fmt.Errorf("%w: cues leave out video keyframes", ErrNoIndex)

const (
	idCueClusterPosition = 0xF1
	idClusterTimestamp   = 0xE7
	idSimpleBlock        = 0xA3
	idBlockGroup         = 0xA0
	idBlock              = 0xA1
	idReferenceBlock     = 0xFB
	idBlockDuration      = 0x9B

	// coverageSamples is how many cued clusters, spread through the file, have
	// their keyframes checked against the Cues, each with the cluster after
	// it. Muxers that cue sparsely leave keyframes out of most clusters, or
	// cue only some clusters, so a few samples catch them while reading only
	// block headers.
	coverageSamples = 4
	// maxClusterChildren bounds the elements read from one cluster.
	maxClusterChildren = 20_000
)

// cuePoint is one video cue: its timestamp in ticks and the position of the
// cluster holding the keyframe, relative to the segment's data.
type cuePoint struct {
	ticks   uint64
	cluster int64
}

// checkCueCoverage reads the video keyframes of a few clusters spread
// through the segment, and of the cluster after each, and reports
// ErrIncompleteIndex if any keyframe has no cue.
func checkCueCoverage(r io.ReaderAt, segStart, segEnd int64, video uint64, points []cuePoint) error {
	cued := make(map[uint64]struct{}, len(points))
	var clusters []int64
	seen := map[int64]struct{}{}
	for _, p := range points {
		cued[p.ticks] = struct{}{}
		if p.cluster < 0 {
			continue
		}
		if _, ok := seen[p.cluster]; !ok {
			seen[p.cluster] = struct{}{}
			clusters = append(clusters, p.cluster)
		}
	}
	if len(clusters) == 0 {
		// CueClusterPosition is mandatory; without it the Cues can't be
		// checked, so they aren't trusted.
		return ErrIncompleteIndex
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i] < clusters[j] })

	samples := min(coverageSamples, len(clusters))
	for i := range samples {
		at := segStart + clusters[i*(len(clusters)-1)/max(samples-1, 1)]
		// The sampled cluster, then the one after it, which a muxer that
		// cues only some clusters leaves without any cue.
		for range 2 {
			keys, next, err := clusterKeyframes(r, at, segEnd, video, nil)
			if err != nil {
				return fmt.Errorf("keyframes: check cluster at %d: %w", at, err)
			}
			for _, k := range keys {
				if _, ok := cued[k]; !ok {
					return ErrIncompleteIndex
				}
			}
			if id, _, _, err := readElementHeader(r, next); next >= segEnd || err != nil || id != idCluster {
				break
			}
			at = next
		}
	}
	return nil
}

// clusterKeyframes returns the timestamps, in ticks, of the video track's
// keyframes in the cluster at off, and where the next element starts. It
// reads element headers and the first bytes of each block, never the frames.
func clusterKeyframes(r io.ReaderAt, off, segEnd int64, video uint64, last *videoEnd) ([]uint64, int64, error) {
	id, size, headerLen, err := readElementHeader(r, off)
	if err != nil {
		return nil, 0, err
	}
	if id != idCluster {
		return nil, 0, fmt.Errorf("cue points at element %#x, not a cluster", id)
	}
	end := segEnd
	if size != unknownSize && off+headerLen+size < end {
		end = off + headerLen + size
	}

	var (
		clusterTicks uint64
		haveTicks    bool
		keys         []int64
		latest       videoEnd // relative to the cluster
	)
	at := off + headerLen
	n := 0
	for ; at < end && n < maxClusterChildren; n++ {
		id, size, headerLen, err := readElementHeader(r, at)
		if err != nil {
			if isShortRead(err) {
				break
			}
			return nil, 0, err
		}
		// An unknown-size cluster ends where the next top-level element
		// starts.
		if id == idCluster || id == idCues || size == unknownSize {
			end = at
			break
		}
		data := at + headerLen
		switch id {
		case idClusterTimestamp:
			b, err := readElementData(r, data, size, 8)
			if err != nil {
				return nil, 0, err
			}
			clusterTicks, haveTicks = readUint(b), true
		case idSimpleBlock:
			track, rel, flags, err := readBlockHeader(r, data, size)
			if err != nil {
				return nil, 0, err
			}
			if track == video {
				latest.add(rel, 0)
				if flags&0x80 != 0 {
					keys = append(keys, rel)
				}
			}
		case idBlockGroup:
			track, rel, key, duration, err := readBlockGroup(r, data, size)
			if err != nil {
				return nil, 0, err
			}
			if track == video {
				latest.add(rel, duration)
				if key {
					keys = append(keys, rel)
				}
			}
		}
		at = data + size
	}
	if n == maxClusterChildren && at < end {
		// Not every block was read, so the cluster can't vouch for the Cues.
		return nil, 0, ErrIncompleteIndex
	}
	if !haveTicks {
		return nil, 0, fmt.Errorf("cluster at %d has no timestamp", off)
	}
	if last != nil {
		if latest.n > 0 {
			last.add(int64(clusterTicks)+latest.last, latest.lastDuration)
		}
		if latest.n > 1 {
			last.add(int64(clusterTicks)+latest.prev, 0)
		}
	}
	ticks := make([]uint64, 0, len(keys))
	for _, rel := range keys {
		if t := int64(clusterTicks) + rel; t >= 0 {
			ticks = append(ticks, uint64(t))
		}
	}
	return ticks, end, nil
}

// readBlockHeader reads a (Simple)Block's track number, timestamp relative
// to its cluster, and flags.
func readBlockHeader(r io.ReaderAt, off, size int64) (track uint64, rel int64, flags byte, err error) {
	var buf [12]byte
	n := min(int64(len(buf)), size)
	if _, err := r.ReadAt(buf[:n], off); err != nil {
		return 0, 0, 0, err
	}
	track, trackLen, ok := readVint(buf[:n], false)
	if !ok || int64(trackLen+3) > n {
		return 0, 0, 0, fmt.Errorf("short block header at %d", off)
	}
	rel = int64(int16(binary.BigEndian.Uint16(buf[trackLen:])))
	return track, rel, buf[trackLen+2], nil
}

// readBlockGroup reads a BlockGroup's block header, whether it is a keyframe
// (a block without a ReferenceBlock references no other frame), and its
// BlockDuration in ticks, or 0 when it has none.
func readBlockGroup(r io.ReaderAt, off, size int64) (track uint64, rel int64, key bool, duration int64, err error) {
	end := off + size
	key = true
	haveBlock := false
	for at := off; at < end; {
		id, childSize, headerLen, err := readElementHeader(r, at)
		if err != nil {
			return 0, 0, false, 0, err
		}
		if childSize == unknownSize {
			return 0, 0, false, 0, fmt.Errorf("unknown-size element in block group at %d", at)
		}
		switch id {
		case idBlock:
			track, rel, _, err = readBlockHeader(r, at+headerLen, childSize)
			if err != nil {
				return 0, 0, false, 0, err
			}
			haveBlock = true
		case idReferenceBlock:
			key = false
		case idBlockDuration:
			b, err := readElementData(r, at+headerLen, childSize, 8)
			if err != nil {
				return 0, 0, false, 0, err
			}
			if d := readUint(b); d <= math.MaxInt64 {
				duration = int64(d)
			}
		}
		at += headerLen + childSize
	}
	if !haveBlock {
		return 0, 0, false, 0, nil
	}
	return track, rel, key, duration, nil
}

// VerifyMatroska reads a Matroska file's index like ReadMatroska and then
// checks it against every cluster: each video keyframe must have a cue, and
// each video cue must be a keyframe. Only then does the index list exactly
// the keyframes FFmpeg cuts at. It reads every block's header, which on a
// large file can take as long as reading the file, so callers run it once per
// file in the background (see Load).
func VerifyMatroska(r io.ReaderAt, size int64) (Index, error) {
	m, err := readMatroska(r, size)
	if err != nil {
		return Index{}, err
	}
	end, err := verifyAllClusters(r, m)
	if err != nil {
		return Index{}, err
	}
	m.VideoEnd = end.seconds(m.timescale, m.frameSeconds)
	return m.Index, nil
}

// videoEnd keeps the two latest distinct timestamps of a track's frames, and
// the latest frame's own duration when its block gives one.
type videoEnd struct {
	last, prev   int64
	lastDuration int64 // in ticks; 0 when the block doesn't say
	n            int   // how many of last and prev are set
}

// add records a frame at t ticks; duration is its BlockDuration, or 0.
func (v *videoEnd) add(t, duration int64) {
	switch {
	case v.n > 0 && t == v.last:
		// Blocks sharing the last timestamp end with the longest.
		v.lastDuration = max(v.lastDuration, duration)
		return
	case v.n > 1 && t == v.prev:
		return
	case v.n == 0 || t > v.last:
		v.prev, v.last, v.lastDuration = v.last, t, duration
	case v.n == 1 || t > v.prev:
		v.prev = t
	default:
		return
	}
	v.n = min(v.n+1, 2)
}

// seconds returns when the last frame ends: its timestamp plus its block's
// duration, else the track's default frame duration, else the gap to the
// frame before. It returns 0 when that can't be worked out.
func (v videoEnd) seconds(timescale int64, frame float64) float64 {
	if v.n == 0 {
		return 0
	}
	scale := float64(timescale) / 1e9
	if v.lastDuration > 0 {
		frame = float64(v.lastDuration) * scale
	}
	if frame <= 0 {
		if v.n < 2 {
			return 0
		}
		frame = float64(v.last-v.prev) * scale
	}
	return float64(v.last)*scale + frame
}

func verifyAllClusters(r io.ReaderAt, m matroskaIndex) (videoEnd, error) {
	var end videoEnd
	cued := make(map[uint64]struct{}, len(m.points))
	for _, p := range m.points {
		cued[p.ticks] = struct{}{}
	}
	found := make(map[uint64]struct{}, len(cued))
	for at := m.segStart; at < m.segEnd; {
		id, size, headerLen, err := readElementHeader(r, at)
		if err != nil {
			if isShortRead(err) {
				break
			}
			return end, err
		}
		if id == idCluster {
			keys, next, err := clusterKeyframes(r, at, m.segEnd, m.video, &end)
			if err != nil {
				return end, err
			}
			for _, k := range keys {
				if _, ok := cued[k]; !ok {
					return end, ErrIncompleteIndex
				}
				found[k] = struct{}{}
			}
			if next <= at {
				return end, ErrIncompleteIndex
			}
			at = next
			continue
		}
		if size == unknownSize {
			// An unknown-size element other than a cluster can't be
			// skipped, so the rest of the file can't be checked.
			return end, ErrIncompleteIndex
		}
		at += headerLen + size
	}
	if len(found) != len(cued) {
		// A cue on a frame that isn't a keyframe would put a planned
		// boundary where FFmpeg can't cut.
		return end, ErrIncompleteIndex
	}
	return end, nil
}
