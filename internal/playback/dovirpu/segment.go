package dovirpu

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrUnsupportedInit reports an init segment that parsed completely but
// cannot be relabelled as Profile 8.1: no video track, no Dolby Vision record,
// or a profile other than 7 or 8. A partly written one fails differently.
var ErrUnsupportedInit = errors.New("dovirpu: init segment cannot carry Profile 8.1")

// Track describes the video track of a fragmented MP4 init segment.
type Track struct {
	ID        uint32
	NALLength int // bytes in each NAL length prefix, from hvcC
}

// SegmentStats counts what ConvertSegment did to a media segment.
type SegmentStats struct {
	Converted int // RPUs converted to Profile 8.1
	Dropped   int // RPUs replaced by filler because they could not be converted
}

type box struct {
	typ         string
	start, body int // offsets of the header and the payload
	end         int
}

func boxes(b []byte, start, end int) ([]box, error) {
	var out []box
	for i := start; i < end; {
		if end-i < 8 {
			return nil, errors.New("dovirpu: truncated MP4 box")
		}
		size, hdr := int(binary.BigEndian.Uint32(b[i:])), 8
		if size == 1 {
			if end-i < 16 {
				return nil, errors.New("dovirpu: truncated MP4 box")
			}
			size, hdr = int(binary.BigEndian.Uint64(b[i+8:])), 16 //nolint:gosec // bounded by end below
		}
		if size < hdr || size > end-i {
			return nil, errors.New("dovirpu: bad MP4 box size")
		}
		out = append(out, box{typ: string(b[i+4 : i+8]), start: i, body: i + hdr, end: i + size})
		i += size
	}
	return out, nil
}

func child(b []byte, parent box, path ...string) (box, bool) {
	for _, typ := range path {
		kids, err := boxes(b, parent.body, parent.end)
		if err != nil {
			return box{}, false
		}
		found := false
		for _, k := range kids {
			if k.typ == typ {
				parent, found = k, true
				break
			}
		}
		if !found {
			return box{}, false
		}
	}
	return parent, true
}

// ConvertInit relabels the video track's Dolby Vision record in an fMP4 init
// segment from Profile 7 (dvcC, EL present, compatibility id 6) to Profile
// 8.1 (dvvC, no EL, compatibility id 1), in place. A record that is already
// Profile 8 is left alone. It returns the video track for ConvertSegment.
func ConvertInit(b []byte) (Track, error) {
	top, err := boxes(b, 0, len(b))
	if err != nil {
		return Track{}, err
	}
	sawMoov := false
	for _, moov := range top {
		if moov.typ != "moov" {
			continue
		}
		sawMoov = true
		traks, err := boxes(b, moov.body, moov.end)
		if err != nil {
			return Track{}, err
		}
		for _, trak := range traks {
			hdlr, ok := child(b, trak, "mdia", "hdlr")
			if trak.typ != "trak" || !ok || hdlr.end-hdlr.body < 12 || string(b[hdlr.body+8:hdlr.body+12]) != "vide" {
				continue
			}
			return convertVideoTrak(b, trak)
		}
	}
	if !sawMoov {
		// FFmpeg is still writing it, e.g. recreated empty after a restart.
		return Track{}, errors.New("dovirpu: init segment has no moov yet")
	}
	return Track{}, fmt.Errorf("%w: no video track", ErrUnsupportedInit)
}

func convertVideoTrak(b []byte, trak box) (Track, error) {
	tkhd, ok := child(b, trak, "tkhd")
	stsd, ok2 := child(b, trak, "mdia", "minf", "stbl", "stsd")
	if !ok || !ok2 || stsd.end-stsd.body < 16 {
		return Track{}, errors.New("dovirpu: incomplete video track")
	}
	idAt := tkhd.body + 12 // version 0: flags, creation and modification times
	if b[tkhd.body] == 1 {
		idAt = tkhd.body + 20
	}
	if idAt+4 > tkhd.end {
		return Track{}, errors.New("dovirpu: short tkhd")
	}
	track := Track{ID: binary.BigEndian.Uint32(b[idAt:])}

	// stsd: version/flags, entry count, then the visual sample entry, whose
	// child boxes start after its 78 bytes of fixed fields.
	entries, err := boxes(b, stsd.body+8, stsd.end)
	if err != nil || len(entries) == 0 || entries[0].end-entries[0].body < 78 {
		return Track{}, errors.New("dovirpu: bad sample entry")
	}
	kids, err := boxes(b, entries[0].body+78, entries[0].end)
	if err != nil {
		return Track{}, err
	}
	var dv *box
	for i, k := range kids {
		switch k.typ {
		case "hvcC":
			if k.end-k.body < 23 {
				return Track{}, errors.New("dovirpu: short hvcC")
			}
			track.NALLength = int(b[k.body+21]&3) + 1
		case "dvcC", "dvvC":
			dv = &kids[i]
		}
	}
	if track.NALLength == 0 || dv == nil || dv.end-dv.body < 5 {
		return Track{}, fmt.Errorf("%w: no hvcC or Dolby Vision record", ErrUnsupportedInit)
	}
	p := b[dv.body:]
	switch profile := p[2] >> 1; profile {
	case 8:
		return track, nil
	case 7:
		level := (p[2]&1)<<5 | p[3]>>3
		copy(b[dv.start+4:], "dvvC")
		p[2] = 8<<1 | level>>5
		p[3] = (level&31)<<3 | 0b101 // rpu present, no EL, bl present
		p[4] = 1<<4 | p[4]&0x0F      // compatibility id 1
		return track, nil
	default:
		return Track{}, fmt.Errorf("%w: Dolby Vision profile %d", ErrUnsupportedInit, profile)
	}
}

// ConvertSegment converts every Profile 7 RPU of track in an fMP4 media
// segment to Profile 8.1, in place and without changing any size: each
// converted RPU is preceded by a filler-data NAL that takes up the bytes it
// saved. An RPU that cannot be converted becomes filler entirely, so that
// frame plays its HDR10 base layer. It returns the byte ranges it changed.
func ConvertSegment(b []byte, track Track) (SegmentStats, [][2]int, error) {
	var stats SegmentStats
	var dirty [][2]int
	top, err := boxes(b, 0, len(b))
	if err != nil {
		return stats, nil, err
	}
	for _, moof := range top {
		if moof.typ != "moof" {
			continue
		}
		samples, err := videoSamples(b, moof, track.ID)
		if err != nil {
			return stats, dirty, err
		}
		for _, s := range samples {
			d, err := convertSample(b[s[0]:s[1]], track.NALLength, &stats)
			if err != nil {
				return stats, dirty, err
			}
			for _, r := range d {
				dirty = append(dirty, [2]int{s[0] + r[0], s[0] + r[1]})
			}
		}
	}
	return stats, dirty, nil
}

// videoSamples returns the [start, end) file offsets of track's samples in
// one moof's runs.
func videoSamples(b []byte, moof box, trackID uint32) ([][2]int, error) {
	trafs, err := boxes(b, moof.body, moof.end)
	if err != nil {
		return nil, err
	}
	var out [][2]int
	for _, traf := range trafs {
		tfhd, ok := child(b, traf, "tfhd")
		if traf.typ != "traf" || !ok || tfhd.end-tfhd.body < 8 || binary.BigEndian.Uint32(b[tfhd.body+4:]) != trackID {
			continue
		}
		base, defaultSize, err := tfhdDefaults(b, tfhd, moof.start)
		if err != nil {
			return nil, err
		}
		next := base // a run without a data offset continues where the last one ended
		runs, err := boxes(b, traf.body, traf.end)
		if err != nil {
			return nil, err
		}
		for _, trun := range runs {
			if trun.typ != "trun" {
				continue
			}
			s, err := trunSamples(b, trun, base, next, defaultSize)
			if err != nil {
				return nil, err
			}
			if len(s) > 0 {
				next = s[len(s)-1][1]
			}
			out = append(out, s...)
		}
	}
	return out, nil
}

func tfhdDefaults(b []byte, tfhd box, moofStart int) (base int, defaultSize uint32, err error) {
	flags := binary.BigEndian.Uint32(b[tfhd.body:]) & 0xFFFFFF
	at := tfhd.body + 8
	base = moofStart
	field := func(n int) uint64 {
		if at+n > tfhd.end {
			err = errors.New("dovirpu: short tfhd")
			return 0
		}
		v := b[at : at+n]
		at += n
		if n == 8 {
			return binary.BigEndian.Uint64(v)
		}
		return uint64(binary.BigEndian.Uint32(v))
	}
	if flags&0x1 != 0 { // base-data-offset-present
		base = int(field(8)) //nolint:gosec // checked against the file size per sample
	} else if flags&0x20000 == 0 { // not default-base-is-moof
		return 0, 0, errors.New("dovirpu: fragment without default-base-is-moof")
	}
	if flags&0x2 != 0 { // sample-description-index-present
		field(4)
	}
	if flags&0x8 != 0 { // default-sample-duration-present
		field(4)
	}
	if flags&0x10 != 0 { // default-sample-size-present
		defaultSize = uint32(field(4)) //nolint:gosec // a 32-bit field
	}
	return base, defaultSize, err
}

func trunSamples(b []byte, trun box, base, next int, defaultSize uint32) ([][2]int, error) {
	if trun.end-trun.body < 8 {
		return nil, errors.New("dovirpu: short trun")
	}
	flags := binary.BigEndian.Uint32(b[trun.body:]) & 0xFFFFFF
	count := int(binary.BigEndian.Uint32(b[trun.body+4:]))
	at := trun.body + 8
	off := next
	if flags&0x200 == 0 && defaultSize == 0 {
		// shortcut: sizes from trex are not read; FFmpeg writes them in tfhd or trun.
		return nil, errors.New("dovirpu: fragment has no sample sizes")
	}
	if flags&0x1 != 0 { // data-offset-present
		if at+4 > trun.end {
			return nil, errors.New("dovirpu: short trun")
		}
		off = base + int(int32(binary.BigEndian.Uint32(b[at:]))) //nolint:gosec // a signed 32-bit field
		at += 4
	}
	if flags&0x4 != 0 { // first-sample-flags-present
		at += 4
	}
	per := 0
	for _, bit := range []uint32{0x100, 0x200, 0x400, 0x800} {
		if flags&bit != 0 {
			per += 4
		}
	}
	if count < 0 || at+count*per > trun.end {
		return nil, errors.New("dovirpu: short trun")
	}
	out := make([][2]int, 0, count)
	for range count {
		size := defaultSize
		p := at
		if flags&0x100 != 0 {
			p += 4
		}
		if flags&0x200 != 0 {
			size = binary.BigEndian.Uint32(b[p:])
		}
		at += per
		end := off + int(size)
		if off < 0 || end > len(b) {
			return nil, errors.New("dovirpu: sample outside the segment")
		}
		out = append(out, [2]int{off, end})
		off = end
	}
	return out, nil
}

// convertSample rewrites the RPU NALs of one length-prefixed HEVC sample and
// returns the [start, end) ranges it changed.
func convertSample(s []byte, nalLength int, stats *SegmentStats) ([][2]int, error) {
	var dirty [][2]int
	for i := 0; i < len(s); {
		if len(s)-i < nalLength {
			return dirty, errors.New("dovirpu: truncated NAL length")
		}
		n := 0
		for _, c := range s[i : i+nalLength] {
			n = n<<8 | int(c)
		}
		start, end := i, i+nalLength+n
		if n < 2 || end > len(s) {
			return dirty, errors.New("dovirpu: bad NAL length")
		}
		i = end
		if s[start+nalLength]>>1&0x3F != 62 {
			continue
		}
		rpu, err := ConvertRPU(s[start+nalLength+2 : end])
		if errors.Is(err, ErrProfile8) {
			continue
		}
		// The filler takes the bytes the conversion saved: its length prefix,
		// a 2-byte header and the 0x80 trailing byte at minimum. An RPU that
		// reuses the previous mapping converts to the same length and needs
		// none.
		gap := end - start - (nalLength + 2 + len(rpu))
		if err != nil || gap != 0 && gap < nalLength+3 {
			writeFiller(s[start:end], nalLength)
			stats.Dropped++
		} else {
			if gap > 0 {
				writeFiller(s[start:start+gap], nalLength)
			}
			r := s[start+gap:]
			putLength(r, nalLength, 2+len(rpu))
			r[nalLength], r[nalLength+1] = 0x7C, 0x01
			copy(r[nalLength+2:], rpu)
			stats.Converted++
		}
		dirty = append(dirty, [2]int{start, end})
	}
	return dirty, nil
}

// writeFiller fills b with one length-prefixed HEVC filler-data NAL (type 38).
func writeFiller(b []byte, nalLength int) {
	putLength(b, nalLength, len(b)-nalLength)
	b[nalLength], b[nalLength+1] = 0x4C, 0x01
	for i := nalLength + 2; i < len(b)-1; i++ {
		b[i] = 0xFF
	}
	b[len(b)-1] = 0x80
}

func putLength(b []byte, nalLength, n int) {
	for i := nalLength - 1; i >= 0; i-- {
		b[i] = byte(n & 0xFF)
		n >>= 8
	}
}

// File is the part of *os.File the file converters use.
type File interface {
	ReadFile
	io.WriterAt
}

// ReadFile is the part of *os.File InitTrack uses.
type ReadFile interface {
	io.ReaderAt
	Stat() (os.FileInfo, error)
}

// ConvertInitFile applies ConvertInit to an init segment file, writing back
// only the bytes that changed.
func ConvertInitFile(f File) (Track, error) {
	b, err := readAll(f)
	if err != nil {
		return Track{}, err
	}
	orig := bytes.Clone(b)
	t, err := ConvertInit(b)
	if err != nil {
		return t, err
	}
	first, last := 0, len(b)
	for first < last && b[first] == orig[first] {
		first++
	}
	for last > first && b[last-1] == orig[last-1] {
		last--
	}
	if first < last {
		_, err = f.WriteAt(b[first:last], int64(first))
	}
	return t, err
}

// InitTrack returns the video track of an init segment file without
// changing the file.
func InitTrack(f ReadFile) (Track, error) {
	b, err := readAll(f)
	if err != nil {
		return Track{}, err
	}
	return ConvertInit(b)
}

// ConvertSegmentFile applies ConvertSegment to a media segment file, writing
// back only the bytes that changed.
func ConvertSegmentFile(f File, track Track) (SegmentStats, error) {
	b, err := readAll(f)
	if err != nil {
		return SegmentStats{}, err
	}
	stats, dirty, err := ConvertSegment(b, track)
	for _, r := range dirty {
		if _, werr := f.WriteAt(b[r[0]:r[1]], int64(r[0])); werr != nil {
			return stats, werr
		}
	}
	return stats, err
}

func readAll(f ReadFile) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	b := make([]byte, info.Size())
	_, err = f.ReadAt(b, 0)
	return b, err
}
