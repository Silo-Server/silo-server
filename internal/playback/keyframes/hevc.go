package keyframes

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/matroska"
)

// ErrNoHEVCBlock means a keyframe's picture type can't be read from its
// block: the track isn't HEVC stored as is, its cue gives no block position,
// the file changed since the index was read, or the block doesn't match the
// cue.
var ErrNoHEVCBlock = errors.New("keyframes: no readable HEVC block for this keyframe")

// Elements a cluster can hold ahead of its timestamp.
const (
	idCRC32 = matroska.IDCRC32
	idVoid  = matroska.IDVoid
)

// HEVC NAL unit types (ITU-T H.265 table 7-1).
const (
	hevcIDRWithRADL = 19
	hevcIDRNoLP     = 20
	hevcFirstNonVCL = 32
)

// maxNALUnitsBeforeSlice bounds how many NAL units (access unit delimiter,
// parameter sets, SEI) HEVCKeyframeIsIDR skips looking for the first slice.
const maxNALUnitsBeforeSlice = 16

// hevcLayout locates the keyframe blocks of an HEVC video track whose frames
// are stored as is, from the Cues' block positions.
type hevcLayout struct {
	segStart int64
	video    uint64
	// nalLength is the size of each NAL unit's length prefix, from the
	// hvcC configuration record.
	nalLength int
	// blocks[i] locates the block of the keyframe at ticks[i].
	blocks []cueBlock
	ticks  []uint64
}

// cueBlock is a cue's block position: its cluster relative to the segment's
// data, and the block relative to the cluster's data. cluster is negative
// when the cue gives no block position.
type cueBlock struct{ cluster, relative int64 }

// newHEVCLayout returns the layout of the video track in entry, or nil when
// it isn't HEVC or its frames are stored with a ContentEncoding.
func newHEVCLayout(entry, cues []byte, video uint64, ticks []uint64, segStart int64) *hevcLayout {
	track := matroska.ParseTrackEntry(entry)
	// CodecPrivate must be an hvcC configuration record (version 1), whose
	// lengthSizeMinusOne gives the NAL unit length prefix.
	if track.CodecID != "V_MPEGH/ISO/HEVC" || len(track.Encodings) > 0 || len(track.CodecPrivate) < 23 || track.CodecPrivate[0] != 1 {
		return nil
	}
	positions := make(map[uint64]cueBlock, len(ticks))
	matroska.WalkCuePoints(cues, func(t uint64, hasTime bool, pos matroska.CueTrackPosition) {
		if !hasTime || pos.Track != video || pos.ClusterPosition < 0 || pos.RelativePosition <= 0 {
			return
		}
		if _, seen := positions[t]; !seen {
			positions[t] = cueBlock{cluster: pos.ClusterPosition, relative: pos.RelativePosition}
		}
	})
	blocks := make([]cueBlock, len(ticks))
	for i, t := range ticks {
		block, ok := positions[t]
		if !ok {
			block.cluster = -1
		}
		blocks[i] = block
	}
	return &hevcLayout{
		segStart: segStart, video: video, nalLength: int(track.CodecPrivate[21]&3) + 1,
		blocks: blocks, ticks: ticks,
	}
}

// HEVCKeyframeIsIDR reports whether the first slice of the HEVC keyframe at
// Keyframes[k] is an IDR picture, rather than a CRA or BLA. It reads that
// block's headers from the file version Load read the index from, a few
// small reads, and fails with ErrNoHEVCBlock when it can't tell. A relayed
// input is read through the relay, as Load reads it.
func (idx Index) HEVCKeyframeIsIDR(k int) (bool, error) {
	l := idx.hevc
	if l == nil || k < 0 || k >= len(l.blocks) || l.blocks[k].cluster < 0 || idx.File.Path == "" {
		return false, ErrNoHEVCBlock
	}
	f, err := fspath.OpenReader(context.Background(), idx.File.Path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if info.Size() != idx.File.Size || !info.ModTime().Equal(idx.File.ModTime) {
		return false, fmt.Errorf("%w: the file changed", ErrNoHEVCBlock)
	}
	nalType, err := l.firstSliceType(f, k)
	if err != nil {
		return false, err
	}
	return nalType == hevcIDRWithRADL || nalType == hevcIDRNoLP, nil
}

// firstSliceType returns the NAL unit type of the first slice in the block
// of keyframe k, after checking the block is that keyframe's.
func (l *hevcLayout) firstSliceType(r io.ReaderAt, k int) (int, error) {
	clusterAt := l.segStart + l.blocks[k].cluster
	id, _, headerLen, err := matroska.ReadElementHeader(r, clusterAt)
	if err != nil {
		return 0, err
	}
	if id != idCluster {
		return 0, fmt.Errorf("%w: cue points at element %#x, not a cluster", ErrNoHEVCBlock, id)
	}
	clusterData := clusterAt + headerLen
	timestamp, err := clusterTimestamp(r, clusterData)
	if err != nil {
		return 0, err
	}

	blockAt := clusterData + l.blocks[k].relative
	blockData, blockSize, err := cueBlockData(r, blockAt)
	if err != nil {
		return 0, err
	}
	track, rel, flags, blockHeaderLen, err := readBlockHeader(r, blockData, blockSize)
	if err != nil {
		return 0, err
	}
	// Bits 1-2 are the lacing; a laced block holds several frames.
	// Matroska tick counts sit far below 2^63.
	if track != l.video || int64(timestamp)+rel != int64(l.ticks[k]) || flags&0x06 != 0 { //nolint:gosec // see above
		return 0, fmt.Errorf("%w: the block at %d isn't the keyframe its cue names", ErrNoHEVCBlock, blockAt)
	}

	at, end := blockData+blockHeaderLen, blockData+blockSize
	var buf [6]byte
	for range maxNALUnitsBeforeSlice {
		prefix := buf[:l.nalLength+2]
		if at+int64(len(prefix)) > end {
			break
		}
		if _, err := r.ReadAt(prefix, at); err != nil {
			return 0, err
		}
		var length int64
		for _, b := range prefix[:l.nalLength] {
			length = length<<8 | int64(b)
		}
		if nalType := int(prefix[l.nalLength]>>1) & 0x3f; nalType < hevcFirstNonVCL {
			return nalType, nil
		}
		at += int64(l.nalLength) + length
	}
	return 0, fmt.Errorf("%w: no slice among the first NAL units of the block at %d", ErrNoHEVCBlock, blockAt)
}

// clusterTimestamp reads the timestamp of the cluster whose data starts at
// data. Muxers write it ahead of the blocks, after at most a CRC-32 or Void
// element.
func clusterTimestamp(r io.ReaderAt, data int64) (uint64, error) {
	at := data
	for range 3 {
		id, size, headerLen, err := matroska.ReadElementHeader(r, at)
		if err != nil {
			return 0, err
		}
		switch {
		case id == idClusterTimestamp:
			b, err := matroska.ReadElementData(r, at+headerLen, size, 8)
			if err != nil {
				return 0, err
			}
			return matroska.Uint(b), nil
		case size == unknownSize || (id != idCRC32 && id != idVoid):
			return 0, fmt.Errorf("%w: no timestamp at the start of the cluster at %d", ErrNoHEVCBlock, data)
		}
		at += headerLen + size
	}
	return 0, fmt.Errorf("%w: no timestamp at the start of the cluster at %d", ErrNoHEVCBlock, data)
}

// cueBlockData returns where the frame block a cue points at starts and its
// size: the SimpleBlock itself, or the Block inside a BlockGroup.
func cueBlockData(r io.ReaderAt, at int64) (int64, int64, error) {
	id, size, headerLen, err := matroska.ReadElementHeader(r, at)
	if err != nil {
		return 0, 0, err
	}
	switch {
	case size == unknownSize:
	case id == idSimpleBlock:
		return at + headerLen, size, nil
	case id == idBlockGroup:
		for child, end := at+headerLen, at+headerLen+size; child < end; {
			id, childSize, childHeaderLen, err := matroska.ReadElementHeader(r, child)
			if err != nil {
				return 0, 0, err
			}
			if childSize == unknownSize {
				break
			}
			if id == idBlock {
				return child + childHeaderLen, childSize, nil
			}
			child += childHeaderLen + childSize
		}
	}
	return 0, 0, fmt.Errorf("%w: cue points at element %#x, not a block", ErrNoHEVCBlock, id)
}
