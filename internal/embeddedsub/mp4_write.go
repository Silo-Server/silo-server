package embeddedsub

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"slices"
)

// emptyTextSample is a tx3g sample with no text: it shows nothing.
var emptyTextSample = []byte{0, 0}

// writeMP4 writes an MP4 text track's packets as an MP4 file of that one
// track, moov first so FFmpeg reads it from a pipe. The source's ftyp,
// movie header, track header, edit list, media header, handler, media
// information header, data information and sample description are copied
// unchanged, and the packets form one chunk. A gap before or between the
// packets' decode times is filled with empty samples, so each keeps its
// time.
func writeMP4(w io.Writer, t *Track, packets []Packet) error {
	m := t.mp4
	if m == nil || m.mvhd == nil || m.tkhd == nil || m.mdhd == nil || m.hdlr == nil || m.stsd == nil {
		return errors.New("embeddedsub: not an MP4 track")
	}
	type sample struct {
		data  []byte
		delta uint32
	}
	var (
		samples []sample
		now     int64
		size    int64
	)
	for _, p := range packets {
		if p.mp4DTS < now {
			return errors.New("embeddedsub: MP4 packets overlap or are out of order")
		}
		for gap := p.mp4DTS - now; gap > 0; {
			d := min(gap, math.MaxUint32)
			samples = append(samples, sample{emptyTextSample, clampUint32(d)})
			size += int64(len(emptyTextSample))
			gap -= d
		}
		samples = append(samples, sample{p.Data, p.mp4Delta})
		size += int64(len(p.Data))
		now = p.mp4DTS + int64(p.mp4Delta)
	}

	// Sample sizes, and durations as runs of equal ones.
	stsz := binary.BigEndian.AppendUint32(nil, 0) // sizes vary
	stsz = binary.BigEndian.AppendUint32(stsz, clampUint32(int64(len(samples))))
	var runs [][2]uint32
	for _, s := range samples {
		stsz = binary.BigEndian.AppendUint32(stsz, uint32(len(s.data))) //nolint:gosec // G115: a text sample is far below 4 GiB
		if n := len(runs); n > 0 && runs[n-1][1] == s.delta {
			runs[n-1][0]++
		} else {
			runs = append(runs, [2]uint32{1, s.delta})
		}
	}
	stts := binary.BigEndian.AppendUint32(nil, clampUint32(int64(len(runs))))
	for _, run := range runs {
		stts = binary.BigEndian.AppendUint32(stts, run[0])
		stts = binary.BigEndian.AppendUint32(stts, run[1])
	}
	var stsc []byte
	if len(samples) > 0 {
		stsc = binary.BigEndian.AppendUint32(stsc, 1)
		stsc = binary.BigEndian.AppendUint32(stsc, 1)
		stsc = binary.BigEndian.AppendUint32(stsc, clampUint32(int64(len(samples))))
		stsc = binary.BigEndian.AppendUint32(stsc, 1)
	} else {
		stsc = binary.BigEndian.AppendUint32(stsc, 0)
	}
	ftyp := m.ftyp
	if ftyp == nil {
		ftyp = mp4Box(mp4BoxFtyp, []byte("isom\x00\x00\x02\x00isomiso2mp41"))
	}
	mdatHeader := 8
	if 8+size > math.MaxUint32 {
		mdatHeader = 16
	}
	// The chunk offset depends on the moov's length, which doesn't depend on
	// the offset: co64 always takes 8 bytes.
	moov := mp4Moov(m, stts, stsc, stsz, 0)
	moov = mp4Moov(m, stts, stsc, stsz, uint64(len(ftyp)+len(moov)+mdatHeader))

	bw := bufio.NewWriterSize(w, 64<<10)
	if _, err := bw.Write(slices.Concat(ftyp, moov)); err != nil {
		return err
	}
	var hdr []byte
	if mdatHeader == 16 {
		hdr = binary.BigEndian.AppendUint32(hdr, 1)
		hdr = append(hdr, "mdat"...)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(16+size))
	} else {
		hdr = binary.BigEndian.AppendUint32(hdr, clampUint32(8+size))
		hdr = append(hdr, "mdat"...)
	}
	if _, err := bw.Write(hdr); err != nil {
		return err
	}
	for _, s := range samples {
		if _, err := bw.Write(s.data); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// clampUint32 is n as a 32-bit box field. The values here are far smaller:
// a text track's sample count, or a size checked to fit.
func clampUint32(n int64) uint32 {
	if n < 0 {
		return 0
	}
	if n > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(n)
}

// mp4Moov builds the moov box of writeMP4's file, its one chunk at offset.
func mp4Moov(m *mp4Track, stts, stsc, stsz []byte, offset uint64) []byte {
	co64 := binary.BigEndian.AppendUint32(nil, 1)
	co64 = binary.BigEndian.AppendUint64(co64, offset)
	stbl := mp4Box("stbl", m.stsd, mp4FullBox("stts", stts), mp4FullBox("stsc", stsc),
		mp4FullBox("stsz", stsz), mp4FullBox("co64", co64))
	mediaHeader := m.mediaHeader
	if mediaHeader == nil {
		mediaHeader = mp4FullBox("nmhd", nil)
	}
	dinf := m.dinf
	if dinf == nil {
		// One data reference: this file, flagged self-contained.
		url := mp4Box("url ", []byte{0, 0, 0, 1})
		dinf = mp4Box("dinf", mp4FullBox("dref", slices.Concat(binary.BigEndian.AppendUint32(nil, 1), url)))
	}
	minf := mp4Box("minf", mediaHeader, dinf, stbl)
	mdia := mp4Box("mdia", m.mdhd, m.hdlr, minf)
	trak := mp4Box("trak", m.tkhd, m.edts, mdia)
	return mp4Box(mp4BoxMoov, m.mvhd, trak)
}

// mp4Box encodes a box of the parts.
func mp4Box(kind string, parts ...[]byte) []byte {
	payload := slices.Concat(parts...)
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(payload))) //nolint:gosec // G115: a moov is far below 4 GiB
	b = append(b, kind...)
	return append(b, payload...)
}

// mp4FullBox encodes a version 0 full box without flags.
func mp4FullBox(kind string, payload []byte) []byte {
	return mp4Box(kind, []byte{0, 0, 0, 0}, payload)
}
