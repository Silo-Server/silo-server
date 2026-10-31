package embeddedsub

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/Silo-Server/silo-server/internal/matroska"
)

// WriteKept writes a track's packets as a file of that one track, to keep
// in place of reading the source again. FFmpeg reads it as it reads what
// WriteStream writes, to the same packets, and ReadIndex reads it back like
// a source file, so ReadPackets can cut windows from it: a Matroska stream
// carries a Cues element ahead of its clusters with an entry for every
// block, and an MP4 stream has its sample table anyway.
//
// A cue's CueDuration is how long its block shows, including a duration
// the block leaves out because the source's cue or the track's default
// gave it, so the packets read back show as long as the ones written.
func WriteKept(w io.Writer, idx *Index, t *Track, packets []Packet) error {
	switch idx.Container {
	case ContainerMatroska:
		return writeKeptMKS(w, t, idx.TimestampScale, packets)
	case ContainerMP4:
		return writeMP4(w, t, packets)
	}
	return ErrNotSupportedContainer
}

// keptCuePositionSize is the width of a kept stream's CueClusterPosition
// values. A fixed width makes the Cues element's length independent of the
// positions it records, which depend on that length.
const keptCuePositionSize = 8

// writeKeptMKS writes WriteMKS's stream with Cues between Tracks and the
// first cluster. The clusters are built first, since the cues point into
// them.
func writeKeptMKS(w io.Writer, t *Track, scale int64, packets []Packet) error {
	if scale <= 0 {
		return fmt.Errorf("embeddedsub: invalid timestamp scale %d", scale)
	}
	head := mksHead(t, scale)
	var (
		clusters  [][]byte
		positions []int64 // each cluster's offset from the first
		blocks    [][]mksBlock
		offset    int64
	)
	if err := mksClusters(packets, scale, func(cluster []byte, bs []mksBlock) error {
		clusters = append(clusters, cluster)
		positions = append(positions, offset)
		blocks = append(blocks, append([]mksBlock(nil), bs...))
		offset += int64(len(cluster))
		return nil
	}); err != nil {
		return err
	}
	// Cue positions count from the segment's data, which starts with Info
	// and Tracks; the segment header is the end of head.
	segmentData := mksSegmentDataStart(head)
	cues := keptCues(blocks, positions, 0, scale)
	base := int64(len(head)) - segmentData + int64(len(cues))
	cues = keptCues(blocks, positions, base, scale)
	for _, part := range append([][]byte{head, cues}, clusters...) {
		if _, err := w.Write(part); err != nil {
			return err
		}
	}
	return nil
}

// mksSegmentDataStart returns where the segment's data starts in head:
// after the EBML header and the segment's ID and unknown size.
func mksSegmentDataStart(head []byte) int64 {
	_, size, hl, _ := matroska.ParseElementHeader(head)
	pos := int64(hl) + size
	_, _, n, _ := matroska.ParseElementHeader(head[pos:])
	return pos + int64(n)
}

// keptCues encodes a cue point per block. base is the first cluster's
// offset in the segment's data. A packet before zero gets a cue at zero:
// cue times are unsigned.
func keptCues(blocks [][]mksBlock, positions []int64, base, scale int64) []byte {
	var points []byte
	for i, bs := range blocks {
		var position [keptCuePositionSize]byte
		binary.BigEndian.PutUint64(position[:], uint64(base+positions[i])) //nolint:gosec // G115: a positive offset
		for _, b := range bs {
			var tp []byte
			tp = matroska.AppendUint(tp, matroska.IDCueTrack, 1)
			tp = matroska.AppendElement(tp, matroska.IDCueClusterPosition, position[:])
			tp = matroska.AppendUint(tp, matroska.IDCueRelativePosition, uint64(b.rel)) //nolint:gosec // G115: an offset into the cluster
			if b.duration > 0 {
				tp = matroska.AppendUint(tp, matroska.IDCueDuration, uint64(int64(b.duration)/scale)) //nolint:gosec // G115: positive
			}
			var point []byte
			point = matroska.AppendUint(point, matroska.IDCueTime, uint64(max(b.ticks, 0)))
			point = matroska.AppendElement(point, matroska.IDCueTrackPositions, tp)
			points = matroska.AppendElement(points, matroska.IDCuePoint, point)
		}
	}
	return matroska.AppendElement(nil, matroska.IDCues, points)
}
