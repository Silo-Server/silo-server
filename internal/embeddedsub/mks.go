package embeddedsub

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/Silo-Server/silo-server/internal/matroska"
)

// maxMKSClusterData bounds one cluster's data before another starts.
const maxMKSClusterData = 1 << 20

// mksInlineFrame is the size from which a frame is written from its packet
// instead of being copied into its cluster's bytes, so writing a stream
// holds about maxMKSClusterData beyond the packets, however large a block.
const mksInlineFrame = 4 << 10

// WriteStream writes a track's packets as a small file of that one track
// in a container FFmpeg reads from a pipe, so FFmpeg converts them as it
// would the track in the source file. StreamFormat names the FFmpeg
// demuxer that reads it.
func WriteStream(w io.Writer, idx *Index, t *Track, packets []Packet) error {
	switch idx.Container {
	case ContainerMatroska:
		return WriteMKS(w, t, idx.TimestampScale, packets)
	case ContainerMP4:
		return writeMP4(w, t, packets)
	}
	return ErrNotSupportedContainer
}

// StreamFormat is the FFmpeg input format of what WriteStream writes for
// the index's tracks: "matroska" or "mp4".
func (idx *Index) StreamFormat() string {
	return string(idx.Container)
}

// WriteMKS writes packets as a Matroska stream holding the one track t,
// with timestamps in ticks of scale nanoseconds: the source's TimestampScale
// (Index.TimestampScale), so every timestamp comes out exactly as the source
// stores it. Packets are written in the order given, as BlockGroups with
// the duration and keyframe flag FFmpeg read from the source block, and the
// TrackEntry carries the codec, its private data and the track's language,
// name, flags and default duration. Packet data is written as is: it is
// already decompressed, so the stream has no ContentEncoding.
//
// The segment has an unknown size, so the stream is written in one pass.
func WriteMKS(w io.Writer, t *Track, scale int64, packets []Packet) error {
	if scale <= 0 {
		return fmt.Errorf("embeddedsub: invalid timestamp scale %d", scale)
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	if _, err := bw.Write(mksHead(t, scale)); err != nil {
		return err
	}
	if err := mksClusters(packets, scale, func(c *mksCluster, _ []mksBlock) error {
		return c.write(bw)
	}); err != nil {
		return err
	}
	return bw.Flush()
}

// mksHead is a stream's EBML header, the start of its segment, of unknown
// size, and its Info and Tracks.
func mksHead(t *Track, scale int64) []byte {
	var b []byte
	var header []byte
	header = matroska.AppendUint(header, matroska.IDEBMLVersion, 1)
	header = matroska.AppendUint(header, matroska.IDEBMLReadVersion, 1)
	header = matroska.AppendUint(header, matroska.IDEBMLMaxIDLength, 4)
	header = matroska.AppendUint(header, matroska.IDEBMLMaxSizeLength, 8)
	header = matroska.AppendString(header, matroska.IDDocType, docTypeMatroska)
	header = matroska.AppendUint(header, matroska.IDDocTypeVersion, 4)
	header = matroska.AppendUint(header, matroska.IDDocTypeReadVersion, 2)
	b = matroska.AppendElement(b, matroska.IDEBML, header)
	b = matroska.AppendID(b, matroska.IDSegment)
	b = matroska.AppendUnknownSize(b)
	var info []byte
	info = matroska.AppendUint(info, matroska.IDTimestampScale, uint64(scale)) //nolint:gosec // G115: callers check that scale is positive
	info = matroska.AppendString(info, matroska.IDMuxingApp, "silo")
	info = matroska.AppendString(info, matroska.IDWritingApp, "silo")
	b = matroska.AppendElement(b, matroska.IDInfo, info)
	return matroska.AppendElement(b, matroska.IDTracks, matroska.AppendElement(nil, matroska.IDTrackEntry, mksTrackEntry(t)))
}

// mksBlock is where a block went in its cluster, for a cue.
type mksBlock struct {
	// ticks is the block's timestamp; rel the offset of its BlockGroup in
	// the cluster's data.
	ticks int64
	rel   int64
	// duration is how long the block's frames show, however the source
	// said so: WriteMKS leaves an inferred one out of the block.
	duration time.Duration
}

// mksCluster is a cluster's data as it is written: its encoded bytes, with
// frames of mksInlineFrame bytes or more left in their packets.
type mksCluster struct {
	// parts and then buf are the data in order; buf holds what was encoded
	// since the last frame left in its packet.
	parts [][]byte
	buf   []byte
	size  int64
}

// bytes adds encoded data.
func (c *mksCluster) bytes(b []byte) {
	c.buf = append(c.buf, b...)
	c.size += int64(len(b))
}

// frame adds a frame's data, left in its packet when it is large.
func (c *mksCluster) frame(data []byte) {
	if len(data) < mksInlineFrame {
		c.bytes(data)
		return
	}
	if len(c.buf) > 0 {
		c.parts = append(c.parts, c.buf)
		c.buf = nil
	}
	c.parts = append(c.parts, data)
	c.size += int64(len(data))
}

// header is the Cluster element's ID and size.
func (c *mksCluster) header() []byte {
	return matroska.AppendSize(matroska.AppendID(nil, matroska.IDCluster), uint64(c.size)) //nolint:gosec // G115: a size
}

// elementSize is the size of the whole Cluster element.
func (c *mksCluster) elementSize() int64 {
	return int64(len(c.header())) + c.size
}

// write writes the Cluster element to w.
func (c *mksCluster) write(w io.Writer) error {
	if _, err := w.Write(c.header()); err != nil {
		return err
	}
	for _, part := range c.parts {
		if _, err := w.Write(part); err != nil {
			return err
		}
	}
	_, err := w.Write(c.buf)
	return err
}

// mksClusters encodes packets as Matroska clusters and passes each to emit
// with the blocks it holds. emit may keep neither. Without packets it emits
// one empty cluster: FFmpeg can't open a Matroska stream that ends before
// its first cluster, and a window can hold no packets.
func mksClusters(packets []Packet, scale int64, emit func(c *mksCluster, blocks []mksBlock) error) error {
	var c mksCluster
	if len(packets) == 0 {
		c.bytes(matroska.AppendUint(nil, matroska.IDClusterTimestamp, 0))
		return emit(&c, nil)
	}
	var (
		blocks    []mksBlock
		clusterTS int64
		open      bool
	)
	flush := func() error {
		if !open {
			return nil
		}
		return emit(&c, blocks)
	}
	for i := 0; i < len(packets); {
		group := packets[i : i+lacedRun(packets[i:])]
		i += len(group)
		p := group[0]
		ticks := int64(p.PTS) / scale
		rel := ticks - clusterTS
		if !open || rel > math.MaxInt16 || rel < math.MinInt16 || c.size > maxMKSClusterData {
			if err := flush(); err != nil {
				return err
			}
			// Cluster timestamps are unsigned; a packet before zero goes in a
			// cluster at zero.
			clusterTS, open = max(ticks, 0), true
			c = mksCluster{}
			c.bytes(matroska.AppendUint(nil, matroska.IDClusterTimestamp, uint64(clusterTS)))
			blocks = blocks[:0]
			if rel = ticks - clusterTS; rel < math.MinInt16 {
				return errors.New("embeddedsub: packet timestamp too far before zero for Matroska")
			}
		}
		var duration time.Duration
		for _, f := range group {
			duration += f.Duration
		}
		blocks = append(blocks, mksBlock{ticks: ticks, rel: c.size, duration: duration})
		c.blockGroup(group, rel, scale, duration)
	}
	return flush()
}

// lacedRun returns how many packets, from the first, are the frames of one
// laced source block, in order; 1 when the first isn't one of them or they
// aren't all there.
func lacedRun(packets []Packet) int {
	first := packets[0]
	if first.laces <= 1 || first.lace != 0 || len(packets) < first.laces {
		return 1
	}
	for j, p := range packets[1:first.laces] {
		if p.pos != first.pos || p.lace != j+1 {
			return 1
		}
	}
	return first.laces
}

// blockGroup adds packets as one block group, rel ticks into the cluster,
// showing for duration. The frames of one source block go back into one
// Xiph-laced block: FFmpeg keeps a laced block in one buffer, and its SubRip
// decoder reads a frame's text on into the frames after it, so only the same
// layout converts the same.
func (c *mksCluster) blockGroup(group []Packet, rel, scale int64, duration time.Duration) {
	p := group[0]
	var flags byte
	if len(group) > 1 {
		flags = 1 << 1 // Xiph lacing
	}
	head := []byte{0x81, byte(rel >> 8), byte(rel), flags} //nolint:gosec // G115: rel fits an int16, stored as two's complement
	frames := 0
	if len(group) > 1 {
		head = append(head, byte(len(group)-1)) //nolint:gosec // G115: a block holds at most 256 frames
		for _, f := range group[:len(group)-1] {
			n := len(f.Data)
			for ; n >= 0xFF; n -= 0xFF {
				head = append(head, 0xFF)
			}
			head = append(head, byte(n)) //nolint:gosec // G115: n < 255
		}
	}
	for _, f := range group {
		frames += len(f.Data)
	}
	var tail []byte
	// The frames' shares add up to the block's duration.
	if duration > 0 && !p.inferredDuration {
		tail = matroska.AppendUint(tail, matroska.IDBlockDuration, uint64(int64(duration)/scale)) //nolint:gosec // G115: positive
	}
	if p.nonKeyframe {
		// FFmpeg reads a block group with a reference as no keyframe.
		tail = matroska.AppendInt(tail, matroska.IDReferenceBlock, -1)
	}
	blockSize := uint64(len(head) + frames)
	block := matroska.AppendSize(matroska.AppendID(nil, matroska.IDBlock), blockSize)
	c.bytes(matroska.AppendSize(matroska.AppendID(nil, matroska.IDBlockGroup), uint64(len(block)+len(tail))+blockSize))
	c.bytes(block)
	c.bytes(head)
	for _, f := range group {
		c.frame(f.Data)
	}
	c.bytes(tail)
}

// mksTrackEntry is the TrackEntry WriteMKS writes for t, as track 1.
func mksTrackEntry(t *Track) []byte {
	uid := t.UID
	if uid == 0 {
		uid = 1
	}
	trackType := t.trackType
	if trackType == 0 {
		trackType = matroska.TrackTypeSubtitle
	}
	var e []byte
	e = matroska.AppendUint(e, matroska.IDTrackNumber, 1)
	e = matroska.AppendUint(e, matroska.IDTrackUID, uid)
	e = matroska.AppendUint(e, matroska.IDTrackType, trackType)
	e = matroska.AppendUint(e, matroska.IDFlagLacing, 0)
	e = matroska.AppendUint(e, matroska.IDFlagEnabled, boolUint(t.Enabled))
	e = matroska.AppendUint(e, matroska.IDFlagDefault, boolUint(t.Default))
	e = matroska.AppendUint(e, matroska.IDFlagForced, boolUint(t.Forced))
	if t.HearingImpaired {
		e = matroska.AppendUint(e, matroska.IDFlagHearingImpaired, 1)
	}
	if t.DefaultDuration > 0 {
		e = matroska.AppendUint(e, matroska.IDDefaultDuration, uint64(t.DefaultDuration))
	}
	if t.Name != "" {
		e = matroska.AppendString(e, matroska.IDName, t.Name)
	}
	if t.Language != "" {
		e = matroska.AppendString(e, matroska.IDLanguage, t.Language)
	}
	if t.LanguageBCP47 != "" {
		e = matroska.AppendString(e, matroska.IDLanguageBCP47, t.LanguageBCP47)
	}
	e = matroska.AppendString(e, matroska.IDCodecID, t.CodecID)
	if len(t.CodecPrivate) > 0 {
		e = matroska.AppendElement(e, matroska.IDCodecPrivate, t.CodecPrivate)
	}
	return e
}

func boolUint(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}
