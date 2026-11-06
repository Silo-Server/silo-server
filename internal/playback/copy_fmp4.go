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
			decode, err := readFMP4DecodeTime(tfdt)
			if err != nil {
				return 0, err
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
		return int64(int32(raw)), nil //nolint:gosec // G115: a version 1 trun stores composition offsets as signed 32-bit, in two's complement.
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

// shiftFMP4Decode returns a media segment with every track fragment moved
// shift seconds later on its timeline. It reports an error for a track without
// a known timescale, or a decode time that would overflow a version 0 tfdt.
//
// An audio fragment the shift would start before zero, which a tfdt can't
// express, loses its leading samples until the next one starts at or after
// zero. That is an encoder's priming: a run that transcodes audio from the
// file's start emits it before the video, so its offset is later than the
// run the timeline came from. The samples kept stay where the timeline puts
// them, so the stream stays continuous; only its first audio frame or two
// goes. A video fragment that would start before zero is an error: a run is
// placed by its first keyframe.
func shiftFMP4Decode(segment []byte, shift float64, scales map[uint32]uint32, videoTrack uint32) ([]byte, error) {
	top, err := fmp4Boxes(segment)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(segment))
	for _, b := range top {
		if b.typ == boxMoof {
			moof, err := shiftMoof(b, shift, scales, videoTrack)
			if err != nil {
				return nil, err
			}
			out = append(out, moof...)
			continue
		}
		out = append(out, b.raw...)
	}
	return out, nil
}

// shiftMoof returns moof with its track fragments shifted (shiftFMP4Decode).
func shiftMoof(moof fmp4Box, shift float64, scales map[uint32]uint32, videoTrack uint32) ([]byte, error) {
	children, err := fmp4Boxes(moof.payload)
	if err != nil {
		return nil, err
	}
	payload := make([]byte, 0, len(moof.payload))
	// skipped is, for each traf at an offset in payload, the media size of
	// the samples its trun dropped.
	skipped := map[int]uint32{}
	trimmed := false
	for _, c := range children {
		if c.typ != boxTraf {
			payload = append(payload, c.raw...)
			continue
		}
		at := len(payload)
		traf, dropped, err := shiftTraf(c, shift, scales, videoTrack)
		if err != nil {
			return nil, err
		}
		skipped[at] = dropped
		trimmed = trimmed || dropped > 0 || len(traf) != len(c.raw)
		payload = append(payload, traf...)
	}
	if !trimmed {
		out := append([]byte(nil), moof.raw[:len(moof.raw)-len(moof.payload)]...)
		return append(out, payload...), nil
	}
	// The dropped trun entries shorten the moof, which brings the media the
	// truns point at, in the mdat after it, that much nearer.
	shrink := int64(len(moof.raw) - (8 + len(payload)))
	for at, dropped := range skipped {
		traf, err := fmp4Boxes(payload[at:])
		if err != nil || len(traf) == 0 {
			return nil, errFMP4Box
		}
		if err := moveTrunData(traf[0].payload, int64(dropped)-shrink); err != nil {
			return nil, err
		}
	}
	return appendFMP4Box(nil, boxMoof, payload), nil
}

// shiftTraf returns traf with its tfdt shifted, and the media size of any
// samples dropped from its start (shiftFMP4Decode).
func shiftTraf(traf fmp4Box, shift float64, scales map[uint32]uint32, videoTrack uint32) ([]byte, uint32, error) {
	raw := append([]byte(nil), traf.raw...)
	payload := raw[len(raw)-len(traf.payload):]
	tfhd, ok := fmp4Child(payload, "tfhd")
	if !ok || len(tfhd) < 8 {
		return nil, 0, fmt.Errorf("%w: track fragment without tfhd", errFMP4Box)
	}
	trackID := binary.BigEndian.Uint32(tfhd[4:])
	scale, ok := scales[trackID]
	if !ok {
		return nil, 0, fmt.Errorf("%w: track fragment for an unknown track", errFMP4Box)
	}
	tfdt, ok := fmp4Child(payload, "tfdt")
	if !ok {
		return raw, 0, nil
	}
	v, err := readFMP4DecodeTime(tfdt)
	if err != nil {
		return nil, 0, err
	}
	v += int64(math.Round(shift * float64(scale)))
	if v >= 0 {
		return raw, 0, putFMP4DecodeTime(tfdt, v)
	}
	if trackID == videoTrack {
		return nil, 0, fmt.Errorf("%w: negative decode time", errFMP4Box)
	}
	return trimTrafStart(payload, tfhd, v)
}

// readFMP4DecodeTime reads a tfdt box's base media decode time. A version 1
// time past math.MaxInt64 isn't one a real stream has, so it is malformed.
func readFMP4DecodeTime(tfdt []byte) (int64, error) {
	switch {
	case len(tfdt) >= 12 && tfdt[0] == 1:
		t := binary.BigEndian.Uint64(tfdt[4:])
		if t > math.MaxInt64 {
			return 0, fmt.Errorf("%w: decode time out of range", errFMP4Box)
		}
		return int64(t), nil
	case len(tfdt) >= 8 && tfdt[0] != 1:
		return int64(binary.BigEndian.Uint32(tfdt[4:])), nil
	}
	return 0, errFMP4Box
}

func putFMP4DecodeTime(tfdt []byte, v int64) error {
	if v < 0 {
		return fmt.Errorf("%w: decode time out of range", errFMP4Box)
	}
	if tfdt[0] == 1 && len(tfdt) >= 12 {
		binary.BigEndian.PutUint64(tfdt[4:], uint64(v))
		return nil
	}
	if v > math.MaxUint32 {
		return fmt.Errorf("%w: decode time out of range", errFMP4Box)
	}
	binary.BigEndian.PutUint32(tfdt[4:], uint32(v))
	return nil
}

// trimTrafStart rebuilds a track fragment that would start at decode time
// v < 0 without its leading samples up to zero, and returns it with the media
// size of the dropped samples. Its media must count from the moof
// (default-base-is-moof, as FFmpeg writes it), so the moof can shrink.
func trimTrafStart(traf, tfhd []byte, v int64) ([]byte, uint32, error) {
	flags := binary.BigEndian.Uint32(tfhd) & 0xFFFFFF
	if flags&0x1 != 0 || flags&0x020000 == 0 {
		return nil, 0, fmt.Errorf("%w: negative decode time in a fragment not based on its moof", errFMP4Box)
	}
	off := 8
	if flags&0x2 != 0 { // sample description index
		off += 4
	}
	var defaultDuration, defaultSize uint32
	for _, f := range []struct {
		bit uint32
		v   *uint32
	}{{0x8, &defaultDuration}, {0x10, &defaultSize}} {
		if flags&f.bit == 0 {
			continue
		}
		if len(tfhd) < off+4 {
			return nil, 0, errFMP4Box
		}
		*f.v = binary.BigEndian.Uint32(tfhd[off:])
		off += 4
	}

	children, err := fmp4Boxes(traf)
	if err != nil {
		return nil, 0, err
	}
	var payload []byte
	var dropped uint32
	truns := 0
	for _, c := range children {
		if c.typ != "trun" {
			payload = append(payload, c.raw...)
			continue
		}
		if truns++; truns > 1 {
			return nil, 0, fmt.Errorf("%w: negative decode time in a fragment with several truns", errFMP4Box)
		}
		var trun []byte
		if trun, dropped, err = trimTrunStart(c.payload, defaultDuration, defaultSize, &v); err != nil {
			return nil, 0, err
		}
		payload = appendFMP4Box(payload, "trun", trun)
	}
	if truns == 0 {
		return nil, 0, fmt.Errorf("%w: negative decode time in a fragment without samples", errFMP4Box)
	}
	// v is now the first kept sample's decode time.
	if tfdt, ok := fmp4Child(payload, "tfdt"); ok {
		if err := putFMP4DecodeTime(tfdt, v); err != nil {
			return nil, 0, err
		}
	}
	return appendFMP4Box(nil, boxTraf, payload), dropped, nil
}

// trimTrunStart drops a trun's leading samples while *v, the next sample's
// decode time, is negative, moving *v past each. It returns the new trun
// payload and the media size of the dropped samples.
func trimTrunStart(trun []byte, defaultDuration, defaultSize uint32, v *int64) ([]byte, uint32, error) {
	if len(trun) < 8 {
		return nil, 0, errFMP4Box
	}
	flags := binary.BigEndian.Uint32(trun) & 0xFFFFFF
	count := int(binary.BigEndian.Uint32(trun[4:]))
	header := 8
	for _, bit := range []uint32{0x1, 0x4} { // data offset, first sample flags
		if flags&bit != 0 {
			header += 4
		}
	}
	entry := 0
	for _, bit := range []uint32{0x100, 0x200, 0x400, 0x800} { // duration, size, flags, composition offset
		if flags&bit != 0 {
			entry += 4
		}
	}
	if count < 0 || len(trun) < header+count*entry {
		return nil, 0, errFMP4Box
	}
	if flags&0x100 == 0 && defaultDuration == 0 || flags&0x200 == 0 && defaultSize == 0 {
		return nil, 0, fmt.Errorf("%w: negative decode time in a trun without sample durations or sizes", errFMP4Box)
	}
	drop := 0
	var dropped uint32
	for ; *v < 0 && drop < count; drop++ {
		e := trun[header+drop*entry:]
		duration, size := defaultDuration, defaultSize
		if flags&0x100 != 0 {
			duration, e = binary.BigEndian.Uint32(e), e[4:]
		}
		if flags&0x200 != 0 {
			size = binary.BigEndian.Uint32(e)
		}
		*v += int64(duration)
		dropped += size
	}
	if drop == count {
		return nil, 0, fmt.Errorf("%w: negative decode time for a whole fragment", errFMP4Box)
	}
	out := append(append([]byte(nil), trun[:header]...), trun[header+drop*entry:]...)
	kept := count - drop
	if kept < 0 || kept > math.MaxUint32 {
		return nil, 0, errFMP4Box
	}
	binary.BigEndian.PutUint32(out[4:], uint32(kept))
	return out, dropped, nil
}

// moveTrunData moves the data offset of every trun in a traf by delta. The
// offsets must count from the moof.
func moveTrunData(traf []byte, delta int64) error {
	tfhd, ok := fmp4Child(traf, "tfhd")
	if !ok || len(tfhd) < 8 {
		return fmt.Errorf("%w: track fragment without tfhd", errFMP4Box)
	}
	if flags := binary.BigEndian.Uint32(tfhd) & 0xFFFFFF; flags&0x1 != 0 || flags&0x020000 == 0 {
		return fmt.Errorf("%w: a moof that changed size has a fragment not based on it", errFMP4Box)
	}
	children, err := fmp4Boxes(traf)
	if err != nil {
		return err
	}
	for _, c := range children {
		if c.typ != "trun" {
			continue
		}
		if len(c.payload) < 12 || binary.BigEndian.Uint32(c.payload)&0x1 == 0 {
			return fmt.Errorf("%w: trun without a data offset in a moof that changed size", errFMP4Box)
		}
		o := int64(int32(binary.BigEndian.Uint32(c.payload[8:]))) + delta //nolint:gosec // G115: a trun's data offset is signed 32-bit, in two's complement.
		if o < 0 || o > math.MaxInt32 {
			return fmt.Errorf("%w: data offset out of range", errFMP4Box)
		}
		binary.BigEndian.PutUint32(c.payload[8:], uint32(o))
	}
	return nil
}

func appendFMP4Box(out []byte, typ string, payload []byte) []byte {
	out = binary.BigEndian.AppendUint32(out, uint32(8+len(payload))) //nolint:gosec // G115: payloads are boxes of one HLS segment, far below 4 GiB.
	out = append(out, typ...)
	return append(out, payload...)
}
