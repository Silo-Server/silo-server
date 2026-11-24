package playback

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
)

// pastEndError is ErrPastEndOfStream for one segment.
type pastEndError struct{ segment int }

func pastEndOfStreamError(segment int) error { return pastEndError{segment: segment} }

func (e pastEndError) Error() string {
	return fmt.Sprintf("%s: segment %d", ErrPastEndOfStream, e.segment)
}

func (e pastEndError) Unwrap() error { return ErrPastEndOfStream }

// ServePastEndOfStream answers a video segment request that
// OpenSegmentWithRecovery found past the media's last segment (err is
// ErrPastEndOfStream) with a segment that holds no media, and reports whether
// it did. The synthetic playlist lists segments to the stored duration, which
// can outlast every stream, and an error status there is retried until
// playback fails.
//
// The segment is valid but has no samples: an MPEG-TS PAT and PMT, or an fMP4
// fragment whose tracks have no samples. hls.js skips such a segment as a gap
// (up to two in a row, and always the playlist's last), while a zero-byte
// body fails its format probe and is requested again without end.
func (s *TranscodeSession) ServePastEndOfStream(w http.ResponseWriter, name string, err error) bool {
	var pastEnd pastEndError
	if !errors.As(err, &pastEnd) {
		return false
	}
	var body []byte
	if videoUsesFMP4(s.Opts()) {
		body = s.emptyFMP4Fragment(pastEnd.segment)
	} else {
		body = s.emptyTSSegment()
	}
	if contentType := mime.TypeByExtension(filepath.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return true
}

const tsPacketSize = 188

// emptyTSSegment returns the PAT and PMT of one of the session's segments, so
// the empty segment declares the same programs and stream types, or a generic
// H.264 PAT and PMT when no segment can be read.
func (s *TranscodeSession) emptyTSSegment() []byte {
	s.mu.Lock()
	opts := s.opts
	end := s.streamEnd
	s.mu.Unlock()
	if root, err := os.OpenRoot(s.outputDir); err == nil && end.known {
		defer func() { _ = root.Close() }()
		for n := end.segment; n >= 0 && n > end.segment-3; n-- {
			if tables := readTSProgramTables(root, segmentFilename(n, opts)); tables != nil {
				return tables
			}
		}
	}
	return genericTSProgramTables()
}

// readTSProgramTables returns the first PAT packet and the first packet of
// the PMT it names from the start of an MPEG-TS file, or nil.
func readTSProgramTables(root *os.Root, name string) []byte {
	f, err := root.Open(name)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 16*tsPacketSize)
	n, _ := io.ReadFull(f, head)
	head = head[:n-n%tsPacketSize]
	var pat []byte
	pmtPID := -1
	for off := 0; off+tsPacketSize <= len(head); off += tsPacketSize {
		packet := head[off : off+tsPacketSize]
		if packet[0] != 0x47 || packet[1]&0x40 == 0 { // sync byte, payload unit start
			continue
		}
		pid := int(binary.BigEndian.Uint16(packet[1:3]) & 0x1fff)
		switch {
		case pat == nil && pid == 0:
			if p := patProgramMapPID(packet); p > 0 {
				pat, pmtPID = packet, p
			}
		case pat != nil && pid == pmtPID:
			return append(append([]byte(nil), pat...), packet...)
		}
	}
	return nil
}

// patProgramMapPID returns the PMT PID of the first program in a PAT packet,
// or -1 when the packet holds none, including when its lengths overrun it.
func patProgramMapPID(packet []byte) int {
	if packet[3]&0x10 == 0 { // no payload
		return -1
	}
	payload := packet[4:]
	if adaptation := packet[3] & 0x30; adaptation == 0x30 {
		if 1+int(payload[0]) > len(payload) {
			return -1
		}
		payload = payload[1+int(payload[0]):]
	}
	if len(payload) < 1 || 1+int(payload[0]) > len(payload) {
		return -1
	}
	section := payload[1+int(payload[0]):] // pointer field
	if len(section) < 12 || section[0] != 0x00 {
		return -1
	}
	sectionLength := int(binary.BigEndian.Uint16(section[1:3]) & 0x0fff)
	if 3+sectionLength-4 < 8 {
		return -1
	}
	programs := section[8:min(len(section), 3+sectionLength-4)]
	for i := 0; i+4 <= len(programs); i += 4 {
		if binary.BigEndian.Uint16(programs[i:i+2]) != 0 { // 0 names the network PID
			return int(binary.BigEndian.Uint16(programs[i+2:i+4]) & 0x1fff)
		}
	}
	return -1
}

// genericTSProgramTables is a PAT for program 1 at PID 0x1000 and a PMT with
// one H.264 stream at PID 0x100, FFmpeg's default layout.
func genericTSProgramTables() []byte {
	// Program 1 maps to PID 0x1000; the PMT names PCR PID 0x100 and one
	// H.264 stream (type 0x1b) there. Reserved bits are set.
	pat := []byte{0x00, 0xb0, 0x00, 0x00, 0x01, 0xc1, 0x00, 0x00, 0x00, 0x01, 0xf0, 0x00}
	pmt := []byte{0x02, 0xb0, 0x00, 0x00, 0x01, 0xc1, 0x00, 0x00, 0xe1, 0x00, 0xf0, 0x00, 0x1b, 0xe1, 0x00, 0xf0, 0x00}
	return append(tsSectionPacket(0, pat), tsSectionPacket(0x1000, pmt)...)
}

// tsSectionPacket wraps a PSI section, whose length field it fills in, in one
// TS packet with its CRC and stuffing.
func tsSectionPacket(pid uint16, section []byte) []byte {
	length := uint16(len(section) - 3 + 4) //nolint:gosec // G115: a section here is a few dozen bytes.
	binary.BigEndian.PutUint16(section[1:], 0xb000|length)
	section = binary.BigEndian.AppendUint32(section, crc32MPEG2(section))
	packet := binary.BigEndian.AppendUint16([]byte{0x47}, 0x4000|pid) // payload unit start
	packet = append(packet, 0x10, 0x00)                               // payload only; pointer field
	packet = append(packet, section...)
	for len(packet) < tsPacketSize {
		packet = append(packet, 0xff)
	}
	return packet
}

// crc32MPEG2 is the CRC-32/MPEG-2 of PSI sections: unreflected, initial
// 0xffffffff, no final xor. hash/crc32 only computes the reflected form.
func crc32MPEG2(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// emptyFMP4Fragment returns a moof and empty mdat in which each track of the
// session's init segment has no samples, decoding from segment's start, or
// nil (a zero-byte answer) when the init segment cannot be read.
func (s *TranscodeSession) emptyFMP4Fragment(segment int) []byte {
	s.mu.Lock()
	segmentDuration := s.opts.SegmentDuration
	s.mu.Unlock()
	if segmentDuration <= 0 {
		segmentDuration = defaultSegmentDuration
	}
	root, err := os.OpenRoot(s.outputDir)
	if err != nil {
		return nil
	}
	defer func() { _ = root.Close() }()
	init, err := root.ReadFile(hlsInitSegmentName)
	if err != nil {
		return nil
	}
	scales, err := fmp4TrackTimescales(init)
	if err != nil || len(scales) == 0 {
		return nil
	}
	start := float64(segment * segmentDuration)
	var trafs []byte
	for _, id := range slices.Sorted(maps.Keys(scales)) {
		decodeTime := uint64(math.Round(start * float64(scales[id])))
		var traf []byte
		traf = appendFMP4Box(traf, "tfhd", fullBoxPayload(0, 0x020000, binary.BigEndian.AppendUint32(nil, id))) // default-base-is-moof
		traf = appendFMP4Box(traf, "tfdt", fullBoxPayload(1, 0, binary.BigEndian.AppendUint64(nil, decodeTime)))
		traf = appendFMP4Box(traf, "trun", fullBoxPayload(0, 0, binary.BigEndian.AppendUint32(nil, 0))) // no samples
		trafs = appendFMP4Box(trafs, "traf", traf)
	}
	moof := appendFMP4Box(nil, "mfhd", fullBoxPayload(0, 0, binary.BigEndian.AppendUint32(nil, uint32(segment+1)))) //nolint:gosec // G115: segment numbers are small.
	moof = append(moof, trafs...)
	return appendFMP4Box(appendFMP4Box(nil, "moof", moof), "mdat", nil)
}

// fullBoxPayload prefixes a full box's version and flags to its fields.
func fullBoxPayload(version byte, flags uint32, fields []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(version)<<24|flags), fields...)
}
