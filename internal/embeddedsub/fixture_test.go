package embeddedsub

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/matroska"
)

// The fixture builder writes Matroska files by hand, for layouts FFmpeg's
// muxer doesn't produce: lacing, compression, cues without relative
// positions, damage. Every element gets an 8-byte size and every integer 8
// bytes, so an element's length never depends on its values and positions
// can be worked out before they are written.

func el(id uint64, parts ...[]byte) []byte {
	data := bytes.Join(parts, nil)
	out := matroska.AppendID(nil, id)
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	size[0] = 0x01
	return append(append(out, size[:]...), data...)
}

func uintEl(id, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return el(id, b[:])
}

func floatEl(id uint64, v float64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], math.Float64bits(v))
	return el(id, b[:])
}

func strEl(id uint64, s string) []byte { return el(id, []byte(s)) }

type cueMode int

const (
	cueWithPosition cueMode = iota
	cueNone
	cueWithoutPosition
)

type fixtureBlock struct {
	track uint64
	rel   int16
	// simple writes a SimpleBlock instead of a BlockGroup.
	simple bool
	// lacing is 0 (none), 1 (Xiph), 2 (fixed) or 3 (EBML).
	lacing byte
	// frames are stored as given: compress them first for a compressed
	// track.
	frames [][]byte
	// duration is the BlockDuration in ticks; negative writes none.
	duration       int64
	references     bool
	cue            cueMode
	cueBlockNumber uint64
	// cueDuration writes the block's duration as its CueDuration too.
	cueDuration bool
	// corruptAt overwrites the block's first byte, so its cue points at
	// garbage.
	corrupt bool
}

type fixtureCluster struct {
	ticks  uint64
	crc    bool
	blocks []fixtureBlock
}

type fixtureTrack struct {
	number, uid, kind uint64
	codec             string
	private           []byte
	// extra are further TrackEntry children.
	extra [][]byte
}

type mkvFixture struct {
	scale    uint64
	tracks   []fixtureTrack
	clusters []fixtureCluster
	// frameCounts are NUMBER_OF_FRAMES tags by TrackUID.
	frameCounts map[uint64]int64
	cuesFirst   bool
	noCues      bool
}

func (f mkvFixture) build() []byte {
	// The first pass only measures: every length is fixed.
	_, layout := f.assemble(nil)
	file, _ := f.assemble(layout)
	return file
}

type fixtureLayout struct {
	info, tracks, tags, cues int64
	clusters                 []int64
	// blocks holds each block's position relative to its cluster's data.
	blocks [][]int64
}

func (f mkvFixture) assemble(at *fixtureLayout) ([]byte, *fixtureLayout) {
	seekHead, info, tracks, tags := f.metadata(at)
	layout := &fixtureLayout{}
	clusters := f.clusterBytes(layout)
	cues := f.cues(at)

	// Segment-relative positions.
	layout.info = int64(len(seekHead))
	layout.tracks = layout.info + int64(len(info))
	layout.tags = layout.tracks + int64(len(tracks))
	clustersAt := layout.tags + int64(len(tags))
	if f.cuesFirst {
		layout.cues = clustersAt
		clustersAt += int64(len(cues))
	}
	for i := range layout.clusters {
		layout.clusters[i] += clustersAt
	}
	if !f.cuesFirst {
		layout.cues = clustersAt + int64(len(clusters))
	}
	body := slices.Concat(seekHead, info, tracks, tags)
	if f.cuesFirst {
		body = slices.Concat(body, cues, clusters)
	} else {
		body = slices.Concat(body, clusters, cues)
	}
	if at != nil {
		// Damage the blocks marked corrupt, after their positions are known:
		// the cluster's data starts after its 12-byte header.
		for ci, c := range f.clusters {
			for bi, b := range c.blocks {
				if b.corrupt {
					body[at.clusters[ci]+12+at.blocks[ci][bi]] = matroska.IDVoid
				}
			}
		}
	}
	header := el(matroska.IDEBML, uintEl(matroska.IDEBMLVersion, 1), uintEl(matroska.IDEBMLReadVersion, 1),
		uintEl(matroska.IDEBMLMaxIDLength, 4), uintEl(matroska.IDEBMLMaxSizeLength, 8),
		strEl(matroska.IDDocType, "matroska"), uintEl(matroska.IDDocTypeVersion, 4), uintEl(matroska.IDDocTypeReadVersion, 2))
	return slices.Concat(header, el(matroska.IDSegment, body)), layout
}

// metadata encodes the seek head, Info, Tracks and Tags, with the
// positions at holds, or zeros on the measuring pass.
func (f mkvFixture) metadata(at *fixtureLayout) (seekHead, info, tracks, tags []byte) {
	var l fixtureLayout
	if at != nil {
		l = *at
	}
	seek := func(id uint64, p int64) []byte {
		return el(matroska.IDSeek, el(matroska.IDSeekID, matroska.AppendID(nil, id)), uintEl(matroska.IDSeekPosition, uint64(p)))
	}
	seekHead = el(matroska.IDSeekHead, seek(matroska.IDInfo, l.info), seek(matroska.IDTracks, l.tracks),
		seek(matroska.IDTags, l.tags), seek(matroska.IDCues, l.cues))
	scale := f.scale
	if scale == 0 {
		scale = 1_000_000
	}
	info = el(matroska.IDInfo, uintEl(matroska.IDTimestampScale, scale), floatEl(matroska.IDDuration, 60_000),
		strEl(matroska.IDMuxingApp, "fixture"), strEl(matroska.IDWritingApp, "fixture"))
	var entries [][]byte
	for _, t := range f.tracks {
		kind := t.kind
		if kind == 0 {
			kind = matroska.TrackTypeSubtitle
		}
		parts := [][]byte{uintEl(matroska.IDTrackNumber, t.number), uintEl(matroska.IDTrackUID, t.uid),
			uintEl(matroska.IDTrackType, kind), strEl(matroska.IDCodecID, t.codec)}
		if t.private != nil {
			parts = append(parts, el(matroska.IDCodecPrivate, t.private))
		}
		entries = append(entries, el(matroska.IDTrackEntry, append(parts, t.extra...)...))
	}
	tracks = el(matroska.IDTracks, entries...)
	var tagList [][]byte
	for _, uid := range slices.Sorted(maps.Keys(f.frameCounts)) {
		tagList = append(tagList, el(matroska.IDTag,
			el(matroska.IDTargets, uintEl(matroska.IDTagTrackUID, uid)),
			el(matroska.IDSimpleTag, strEl(matroska.IDTagName, "NUMBER_OF_FRAMES"),
				strEl(matroska.IDTagString, strconv.FormatInt(f.frameCounts[uid], 10)))))
	}
	return seekHead, info, tracks, el(matroska.IDTags, tagList...)
}

// clusterBytes encodes the clusters, recording in layout where each starts,
// relative to the first, and where its blocks start in its data.
func (f mkvFixture) clusterBytes(layout *fixtureLayout) []byte {
	var clusters []byte
	for _, c := range f.clusters {
		parts := [][]byte{}
		if c.crc {
			parts = append(parts, el(matroska.IDCRC32, []byte{0, 0, 0, 0}))
		}
		parts = append(parts, uintEl(matroska.IDClusterTimestamp, c.ticks))
		var blockPos []int64
		for _, b := range c.blocks {
			blockPos = append(blockPos, int64(len(bytes.Join(parts, nil))))
			parts = append(parts, b.element())
		}
		layout.blocks = append(layout.blocks, blockPos)
		layout.clusters = append(layout.clusters, int64(len(clusters)))
		clusters = append(clusters, el(matroska.IDCluster, parts...)...)
	}
	return clusters
}

// cues encodes the Cues, with the positions at holds, or zeros on the
// measuring pass; noCues writes a Void of the same length instead.
func (f mkvFixture) cues(at *fixtureLayout) []byte {
	var points [][]byte
	for ci, c := range f.clusters {
		for bi, b := range c.blocks {
			if b.cue == cueNone {
				continue
			}
			var cluster, rel int64
			if at != nil {
				cluster, rel = at.clusters[ci], at.blocks[ci][bi]
			}
			tp := [][]byte{uintEl(matroska.IDCueTrack, b.track), uintEl(matroska.IDCueClusterPosition, uint64(cluster))}
			if b.cueBlockNumber != 0 {
				tp = append(tp, uintEl(matroska.IDCueBlockNumber, b.cueBlockNumber))
			}
			if b.cue == cueWithPosition {
				tp = append(tp, uintEl(matroska.IDCueRelativePosition, uint64(rel)))
			}
			if b.cueDuration && b.duration >= 0 {
				tp = append(tp, uintEl(matroska.IDCueDuration, uint64(b.duration)))
			}
			points = append(points, el(matroska.IDCuePoint,
				uintEl(matroska.IDCueTime, uint64(int64(c.ticks)+int64(b.rel))), el(matroska.IDCueTrackPositions, tp...)))
		}
	}
	cues := el(matroska.IDCues, points...)
	if f.noCues {
		return el(matroska.IDVoid, make([]byte, len(cues)-9))
	}
	return cues
}

// element encodes the block as a SimpleBlock or a BlockGroup.
func (b fixtureBlock) element() []byte {
	flags := b.lacing << 1
	if b.simple && !b.references {
		flags |= 0x80
	}
	body := []byte{0x80 | byte(b.track), byte(uint16(b.rel) >> 8), byte(uint16(b.rel)), flags}
	if b.lacing != 0 {
		body = append(body, byte(len(b.frames)-1))
		switch b.lacing {
		case 1:
			for _, fr := range b.frames[:len(b.frames)-1] {
				n := len(fr)
				for ; n >= 255; n -= 255 {
					body = append(body, 255)
				}
				body = append(body, byte(n))
			}
		case 3:
			body = matroska.AppendSize(body, uint64(len(b.frames[0])))
			for i := 1; i < len(b.frames)-1; i++ {
				// A 2-byte signed vint: the raw value is biased by 2^13-1.
				raw := uint16(len(b.frames[i])-len(b.frames[i-1])+8191) | 0x4000
				body = append(body, byte(raw>>8), byte(raw))
			}
		}
	}
	for _, fr := range b.frames {
		body = append(body, fr...)
	}
	if b.simple {
		return el(matroska.IDSimpleBlock, body)
	}
	parts := [][]byte{el(matroska.IDBlock, body)}
	if b.duration >= 0 {
		parts = append(parts, uintEl(matroska.IDBlockDuration, uint64(b.duration)))
	}
	if b.references {
		parts = append(parts, el(matroska.IDReferenceBlock, []byte{0xFF}))
	}
	return el(matroska.IDBlockGroup, parts...)
}

// textBlock is an unlaced BlockGroup of one text frame.
func textBlock(track uint64, rel int16, duration int64, text string) fixtureBlock {
	return fixtureBlock{track: track, rel: rel, frames: [][]byte{[]byte(text)}, duration: duration}
}

// encodingEl is a ContentEncodings element of one compression.
func encodingEl(scope, algo uint64, settings []byte) []byte {
	comp := [][]byte{uintEl(matroska.IDContentCompAlgo, algo)}
	if settings != nil {
		comp = append(comp, el(matroska.IDContentCompSettings, settings))
	}
	return el(matroska.IDContentEncodings, el(matroska.IDContentEncoding,
		uintEl(matroska.IDContentEncodingOrder, 0), uintEl(matroska.IDContentEncodingScope, scope),
		uintEl(matroska.IDContentEncodingType, 0), el(matroska.IDContentCompression, comp...)))
}

func zlibBytes(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeFixture(t testing.TB, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.mkv")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
