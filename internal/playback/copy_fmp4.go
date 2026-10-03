package playback

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// The few fMP4 boxes a keyframe-planned session reads: which track is the
// video and its timescale (from init.mp4), and where each keyframe group
// FFmpeg writes starts (from its first moof). See copy_group_assembler.go.

var errFMP4Box = errors.New("malformed fMP4 box")

const (
	boxMoof = "moof"
	boxTraf = "traf"
)

type fmp4Box struct {
	typ     string
	payload []byte // the box's data, after its header
	raw     []byte // the whole box
}

// fmp4Boxes splits data into its top-level boxes.
func fmp4Boxes(data []byte) ([]fmp4Box, error) {
	var boxes []fmp4Box
	for len(data) > 0 {
		if len(data) < 8 {
			return nil, errFMP4Box
		}
		size := uint64(binary.BigEndian.Uint32(data))
		typ := string(data[4:8])
		header := uint64(8)
		switch size {
		case 0:
			size = uint64(len(data))
		case 1:
			if len(data) < 16 {
				return nil, errFMP4Box
			}
			size, header = binary.BigEndian.Uint64(data[8:]), 16
		}
		if size < header || size > uint64(len(data)) {
			return nil, errFMP4Box
		}
		boxes = append(boxes, fmp4Box{typ: typ, payload: data[header:size], raw: data[:size]})
		data = data[size:]
	}
	return boxes, nil
}

func fmp4Child(data []byte, typ string) ([]byte, bool) {
	boxes, err := fmp4Boxes(data)
	if err != nil {
		return nil, false
	}
	for _, b := range boxes {
		if b.typ == typ {
			return b.payload, true
		}
	}
	return nil, false
}

// fmp4VideoTrack returns the video track's ID and timescale from an init
// segment.
func fmp4VideoTrack(init []byte) (trackID, timescale uint32, err error) {
	top, err := fmp4Boxes(init)
	if err != nil {
		return 0, 0, err
	}
	for _, b := range top {
		if b.typ != "moov" {
			continue
		}
		traks, err := fmp4Boxes(b.payload)
		if err != nil {
			return 0, 0, err
		}
		for _, trak := range traks {
			if trak.typ != "trak" {
				continue
			}
			tkhd, ok1 := fmp4Child(trak.payload, "tkhd")
			mdia, ok2 := fmp4Child(trak.payload, "mdia")
			if !ok1 || !ok2 {
				continue
			}
			hdlr, ok1 := fmp4Child(mdia, "hdlr")
			mdhd, ok2 := fmp4Child(mdia, "mdhd")
			// hdlr: version/flags, pre_defined, then handler_type.
			if !ok1 || !ok2 || len(hdlr) < 12 || string(hdlr[8:12]) != "vide" {
				continue
			}
			id, err := fullBoxField(tkhd, 8, 16)
			if err != nil {
				return 0, 0, err
			}
			scale, err := fullBoxField(mdhd, 8, 16)
			if err != nil {
				return 0, 0, err
			}
			if scale == 0 {
				return 0, 0, fmt.Errorf("%w: zero video timescale", errFMP4Box)
			}
			return id, scale, nil
		}
	}
	return 0, 0, fmt.Errorf("%w: no video track", errFMP4Box)
}

// fullBoxField reads the 32-bit field that follows a full box's two
// timestamps: v0Offset bytes in for version 0, v1Offset for version 1, both
// counted after the version and flags.
func fullBoxField(box []byte, v0Offset, v1Offset int) (uint32, error) {
	if len(box) < 4 {
		return 0, errFMP4Box
	}
	off := 4 + v0Offset
	if box[0] == 1 {
		off = 4 + v1Offset
	}
	if len(box) < off+4 {
		return 0, errFMP4Box
	}
	return binary.BigEndian.Uint32(box[off:]), nil
}

// fmp4FirstVideoPTS returns the presentation time, in timescale ticks, of the
// first video sample in a media segment: its track fragment's decode time
// plus the first sample's composition offset.
func fmp4FirstVideoPTS(segment []byte, trackID uint32) (int64, error) {
	top, err := fmp4Boxes(segment)
	if err != nil {
		return 0, err
	}
	for _, b := range top {
		if b.typ != boxMoof {
			continue
		}
		trafs, err := fmp4Boxes(b.payload)
		if err != nil {
			return 0, err
		}
		for _, traf := range trafs {
			if traf.typ != boxTraf {
				continue
			}
			tfhd, ok := fmp4Child(traf.payload, "tfhd")
			if !ok || len(tfhd) < 8 || binary.BigEndian.Uint32(tfhd[4:]) != trackID {
				continue
			}
			tfdt, ok := fmp4Child(traf.payload, "tfdt")
			if !ok || len(tfdt) < 8 {
				return 0, fmt.Errorf("%w: video fragment without tfdt", errFMP4Box)
			}
			var decode int64
			if tfdt[0] == 1 {
				if len(tfdt) < 12 {
					return 0, errFMP4Box
				}
				decode = int64(binary.BigEndian.Uint64(tfdt[4:]))
			} else {
				decode = int64(binary.BigEndian.Uint32(tfdt[4:]))
			}
			offset, err := firstCompositionOffset(traf.payload)
			if err != nil {
				return 0, err
			}
			return decode + offset, nil
		}
		return 0, fmt.Errorf("%w: no video fragment", errFMP4Box)
	}
	return 0, fmt.Errorf("%w: no moof", errFMP4Box)
}

// firstCompositionOffset reads the first sample's composition time offset
// from a track fragment's trun, or 0 when it carries none.
func firstCompositionOffset(traf []byte) (int64, error) {
	trun, ok := fmp4Child(traf, "trun")
	if !ok {
		return 0, nil
	}
	if len(trun) < 8 {
		return 0, errFMP4Box
	}
	version := trun[0]
	flags := binary.BigEndian.Uint32(trun) & 0xFFFFFF
	if binary.BigEndian.Uint32(trun[4:]) == 0 || flags&0x800 == 0 {
		return 0, nil
	}
	off := 8
	if flags&0x1 != 0 { // data offset
		off += 4
	}
	if flags&0x4 != 0 { // first sample flags
		off += 4
	}
	for _, bit := range []uint32{0x100, 0x200, 0x400} { // duration, size, flags
		if flags&bit != 0 {
			off += 4
		}
	}
	if len(trun) < off+4 {
		return 0, errFMP4Box
	}
	raw := binary.BigEndian.Uint32(trun[off:])
	if version == 1 {
		return int64(int32(raw)), nil
	}
	return int64(raw), nil
}

// joinFMP4Groups concatenates keyframe-group segments into one media
// segment: every moof and mdat in order, the first group's styp, and no sidx,
// whose byte references would be wrong in the joined file. Fragment data
// offsets are relative to each moof, so whole moof/mdat pairs move freely.
func joinFMP4Groups(groups [][]byte) ([]byte, error) {
	size := 0
	for _, g := range groups {
		size += len(g)
	}
	out := make([]byte, 0, size)
	for i, g := range groups {
		boxes, err := fmp4Boxes(g)
		if err != nil {
			return nil, err
		}
		for _, b := range boxes {
			switch b.typ {
			case "sidx":
				continue
			case "styp":
				if i > 0 {
					continue
				}
			}
			out = append(out, b.raw...)
		}
	}
	return out, nil
}

// fmp4TrackTimescales maps each track's ID to its media timescale, from an
// init segment.
func fmp4TrackTimescales(init []byte) (map[uint32]uint32, error) {
	top, err := fmp4Boxes(init)
	if err != nil {
		return nil, err
	}
	scales := map[uint32]uint32{}
	for _, b := range top {
		if b.typ != "moov" {
			continue
		}
		traks, err := fmp4Boxes(b.payload)
		if err != nil {
			return nil, err
		}
		for _, trak := range traks {
			if trak.typ != "trak" {
				continue
			}
			tkhd, ok1 := fmp4Child(trak.payload, "tkhd")
			mdia, ok2 := fmp4Child(trak.payload, "mdia")
			if !ok1 || !ok2 {
				continue
			}
			mdhd, ok := fmp4Child(mdia, "mdhd")
			if !ok {
				continue
			}
			id, err := fullBoxField(tkhd, 8, 16)
			if err != nil {
				return nil, err
			}
			scale, err := fullBoxField(mdhd, 8, 16)
			if err != nil {
				return nil, err
			}
			scales[id] = scale
		}
	}
	return scales, nil
}

// shiftFMP4Decode moves every track fragment in a media segment shift seconds
// later on its timeline by rewriting its tfdt, in place. It reports an error
// for a track without a known timescale, or a decode time that would go
// negative or overflow a version 0 tfdt.
func shiftFMP4Decode(segment []byte, shift float64, scales map[uint32]uint32) error {
	top, err := fmp4Boxes(segment)
	if err != nil {
		return err
	}
	for _, b := range top {
		if b.typ != boxMoof {
			continue
		}
		trafs, err := fmp4Boxes(b.payload)
		if err != nil {
			return err
		}
		for _, traf := range trafs {
			if traf.typ != boxTraf {
				continue
			}
			tfhd, ok := fmp4Child(traf.payload, "tfhd")
			if !ok || len(tfhd) < 8 {
				return fmt.Errorf("%w: track fragment without tfhd", errFMP4Box)
			}
			scale, ok := scales[binary.BigEndian.Uint32(tfhd[4:])]
			if !ok {
				return fmt.Errorf("%w: track fragment for an unknown track", errFMP4Box)
			}
			tfdt, ok := fmp4Child(traf.payload, "tfdt")
			if !ok {
				continue
			}
			delta := int64(math.Round(shift * float64(scale)))
			if tfdt[0] == 1 && len(tfdt) >= 12 {
				v := int64(binary.BigEndian.Uint64(tfdt[4:])) + delta
				if v < 0 {
					return fmt.Errorf("%w: negative decode time", errFMP4Box)
				}
				binary.BigEndian.PutUint64(tfdt[4:], uint64(v))
			} else if len(tfdt) >= 8 {
				v := int64(binary.BigEndian.Uint32(tfdt[4:])) + delta
				if v < 0 || v > math.MaxUint32 {
					return fmt.Errorf("%w: decode time out of range", errFMP4Box)
				}
				binary.BigEndian.PutUint32(tfdt[4:], uint32(v))
			}
		}
	}
	return nil
}
