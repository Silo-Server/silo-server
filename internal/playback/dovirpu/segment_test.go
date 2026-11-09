package dovirpu

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func mp4Box(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	return append(binary.BigEndian.AppendUint32(nil, uint32(8+len(body))), append([]byte(typ), body...)...)
}

func nal(payload []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(payload))), payload...)
}

func TestConvertInitNotReadyIsNotUnsupported(t *testing.T) {
	// FFmpeg recreates init.mp4 empty on a restart; that is not ready, not unsupported.
	for _, b := range [][]byte{nil, mp4Box("ftyp", make([]byte, 8))} {
		if _, err := ConvertInit(b); err == nil || errors.Is(err, ErrUnsupportedInit) {
			t.Fatalf("ConvertInit(% x) = %v, want a not-ready error", b, err)
		}
	}
	if _, err := ConvertInit(mp4Box("moov")); !errors.Is(err, ErrUnsupportedInit) {
		t.Fatalf("ConvertInit(moov without video) = %v, want ErrUnsupportedInit", err)
	}
}

func TestConvertInitAndSegment(t *testing.T) {
	hvcC := make([]byte, 23)
	hvcC[21] = 0xFF // lengthSizeMinusOne 3
	// The DOVI record FFmpeg writes for this repo's Profile 7 test disc.
	dvcC := append([]byte{1, 0, 0x0e, 0x37, 0x60}, make([]byte, 19)...)
	init := mp4Box("moov", mp4Box("trak",
		mp4Box("tkhd", make([]byte, 12), []byte{0, 0, 0, 1}, make([]byte, 64)),
		mp4Box("mdia",
			mp4Box("hdlr", make([]byte, 8), []byte("vide"), make([]byte, 13)),
			mp4Box("minf", mp4Box("stbl", mp4Box("stsd", []byte{0, 0, 0, 0, 0, 0, 0, 1},
				mp4Box("hvc1", make([]byte, 78), mp4Box("hvcC", hvcC), mp4Box("dvcC", dvcC))))))))

	track, err := ConvertInit(init)
	if err != nil || track != (Track{ID: 1, NALLength: 4}) {
		t.Fatalf("ConvertInit = %+v, %v", track, err)
	}
	at := bytes.Index(init, []byte("dvvC"))
	if at < 0 || !bytes.Equal(init[at+4:at+9], []byte{1, 0, 0x10, 0x35, 0x10}) {
		t.Fatalf("DOVI record not relabelled as 8.1: % x", init[at:at+9])
	}

	slice := []byte{0x02, 0x01, 0xAA, 0xBB}
	sample := append(nal(slice), nal(append([]byte{0x7C, 0x01}, fixture(t, "mel_orig.bin")...))...)
	trun := []byte{0, 0, 0x02, 0x01, 0, 0, 0, 1, 0, 0, 0, 0} // data offset + sizes, 1 sample
	trun = binary.BigEndian.AppendUint32(trun, uint32(len(sample)))
	traf := mp4Box("traf", mp4Box("tfhd", []byte{0, 0x02, 0, 0, 0, 0, 0, 1}), mp4Box("trun", trun))
	moof := mp4Box("moof", traf)
	binary.BigEndian.PutUint32(moof[len(moof)-8:], uint32(len(moof)+8)) // data_offset: the mdat payload
	seg := append(moof, mp4Box("mdat", sample)...)
	size := len(seg)

	stats, _, err := ConvertSegment(seg, track)
	if err != nil || stats != (SegmentStats{Converted: 1}) || len(seg) != size {
		t.Fatalf("ConvertSegment = %+v, %v", stats, err)
	}
	got := seg[len(moof)+8:]
	want81 := append([]byte{0x7C, 0x01}, fixture(t, "mel_to_81.bin")...)
	filler := got[len(nal(slice)) : len(got)-len(nal(want81))]
	if !bytes.Equal(got[:len(nal(slice))], nal(slice)) ||
		!bytes.Equal(got[len(got)-len(nal(want81)):], nal(want81)) ||
		filler[4]>>1 != 38 || filler[len(filler)-1] != 0x80 ||
		int(binary.BigEndian.Uint32(filler)) != len(filler)-4 {
		t.Fatalf("sample = % x", got)
	}
	if stats, _, err := ConvertSegment(seg, track); err != nil || stats != (SegmentStats{}) {
		t.Fatalf("second pass = %+v, %v", stats, err)
	}
}

// An RPU that reuses the previous mapping keeps its length when converted,
// so it is rewritten in place with no filler rather than dropped.
func TestConvertSampleKeepsSameLengthRPU(t *testing.T) {
	// Rebuild dovi_tool's MEL RPU with use_prev_vdr_rpu_flag set and the
	// mapping removed.
	mel := fixture(t, "mel_orig.bin")
	d := unescape(mel)
	crcAt := len(d) - 5
	for d[crcAt+4] == 0 { // trailing zero bytes
		crcAt--
	}
	c := converter{r: bitReader{data: d[:crcAt], pos: 8}}
	_, _, compressed := c.header()
	usePrev := c.r.pos - 1
	c.mapping()
	dmStart := c.r.pos
	c.dm(compressed)
	if c.err != nil {
		t.Fatal(c.err)
	}
	var w bitWriter
	w.copyBits(d, 8, usePrev)
	w.bits(1, 1) // use_prev_vdr_rpu_flag
	w.ue(0)      // prev_vdr_rpu_id
	w.copyBits(d, dmStart, c.r.pos)
	w.align()
	w.copyBits(d, c.r.pos, crcAt*8)
	body := append([]byte{0x19}, w.out...)
	body = binary.BigEndian.AppendUint32(body, crc32MPEG2(w.out))
	rpu := escape(append(body, 0x80))

	if out, err := ConvertRPU(rpu); err != nil || len(out) != len(rpu) || bytes.Equal(out, rpu) {
		t.Fatalf("ConvertRPU = %d bytes, %v; want a converted RPU of %d bytes", len(out), err, len(rpu))
	}
	sample := nal(append([]byte{0x7C, 0x01}, rpu...))
	var stats SegmentStats
	if _, err := convertSample(sample, 4, &stats); err != nil || stats != (SegmentStats{Converted: 1}) || sample[4]>>1 != 62 {
		t.Fatalf("convertSample = %+v, %v, NAL type %d", stats, err, sample[4]>>1)
	}
}
