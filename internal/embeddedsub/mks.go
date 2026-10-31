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

// maxMKSClusterData bounds one cluster's size, so writing a stream holds at
// most this much beyond the packets.
const maxMKSClusterData = 1 << 20

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
	info = matroska.AppendUint(info, matroska.IDTimestampScale, uint64(scale))
	info = matroska.AppendString(info, matroska.IDMuxingApp, "silo")
	info = matroska.AppendString(info, matroska.IDWritingApp, "silo")
	b = matroska.AppendElement(b, matroska.IDInfo, info)
	b = matroska.AppendElement(b, matroska.IDTracks, matroska.AppendElement(nil, matroska.IDTrackEntry, mksTrackEntry(t)))
	if _, err := bw.Write(b); err != nil {
		return err
	}

	var (
		cluster   []byte
		clusterTS int64
		open      bool
	)
	flush := func() error {
		if !open {
			return nil
		}
		_, err := bw.Write(matroska.AppendElement(nil, matroska.IDCluster, cluster))
		return err
	}
	for i := 0; i < len(packets); {
		group := packets[i : i+lacedRun(packets[i:])]
		i += len(group)
		p := group[0]
		ticks := int64(p.PTS) / scale
		rel := ticks - clusterTS
		if !open || rel > math.MaxInt16 || rel < math.MinInt16 || len(cluster) > maxMKSClusterData {
			if err := flush(); err != nil {
				return err
			}
			// Cluster timestamps are unsigned; a packet before zero goes in a
			// cluster at zero.
			clusterTS, open = max(ticks, 0), true
			cluster = matroska.AppendUint(cluster[:0], matroska.IDClusterTimestamp, uint64(clusterTS))
			if rel = ticks - clusterTS; rel < math.MinInt16 {
				return errors.New("embeddedsub: packet timestamp too far before zero for Matroska")
			}
		}
		cluster = matroska.AppendElement(cluster, matroska.IDBlockGroup, mksBlockGroup(group, rel, scale))
	}
	if err := flush(); err != nil {
		return err
	}
	return bw.Flush()
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

// mksBlockGroup encodes packets as one block, rel ticks into its cluster.
// The frames of one source block go back into one Xiph-laced block: FFmpeg
// keeps a laced block in one buffer, and its SubRip decoder reads a frame's
// text on into the frames after it, so only the same layout converts the
// same.
func mksBlockGroup(group []Packet, rel, scale int64) []byte {
	p := group[0]
	var flags byte
	if len(group) > 1 {
		flags = 1 << 1 // Xiph lacing
	}
	block := make([]byte, 0, 8+len(p.Data))
	block = append(block, 0x81, byte(rel>>8), byte(rel), flags) //nolint:gosec // G115: rel fits an int16, stored as two's complement
	var duration time.Duration
	if len(group) > 1 {
		block = append(block, byte(len(group)-1)) //nolint:gosec // G115: a block holds at most 256 frames
		for _, f := range group[:len(group)-1] {
			n := len(f.Data)
			for ; n >= 0xFF; n -= 0xFF {
				block = append(block, 0xFF)
			}
			block = append(block, byte(n)) //nolint:gosec // G115: n < 255
		}
	}
	for _, f := range group {
		block = append(block, f.Data...)
		duration += f.Duration
	}
	out := matroska.AppendElement(nil, matroska.IDBlock, block)
	// The frames' shares add up to the block's duration.
	if duration > 0 && !p.inferredDuration {
		out = matroska.AppendUint(out, matroska.IDBlockDuration, uint64(int64(duration)/scale)) //nolint:gosec // G115: positive
	}
	if p.nonKeyframe {
		// FFmpeg reads a block group with a reference as no keyframe.
		out = matroska.AppendInt(out, matroska.IDReferenceBlock, -1)
	}
	return out
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
