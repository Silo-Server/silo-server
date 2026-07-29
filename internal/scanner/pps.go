package scanner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// copySafetyScanSeconds bounds how much of the stream the multi-PPS scan
// demuxes. Affected encoders emit every PPS variant within the opening GOPs
// (all four in the reference file appear inside the first two seconds); a
// generous window catches slower rotations while staying a stream-copy, so the
// scan finishes in well under a second regardless of runtime.
const copySafetyScanSeconds = 15

// DetectMultiplePPSH264 reports whether an H.264 stream redefines the same
// pic_parameter_set_id in-band with more than one distinct content within the
// opening copySafetyScanSeconds. Such streams are unsafe to stream-copy into an
// avc1/fMP4 HLS segment because the avcC advertises a single parameter set.
//
// It stream-copies (no decode) the leading window, extracts the raw PPS NAL
// units with ffmpeg's filter_units bitstream filter, and groups them by
// pic_parameter_set_id. A legal stream that uses several distinct PPS ids
// (0, 1, 2 …) is not flagged — only a single id carrying conflicting
// definitions is.
func DetectMultiplePPSH264(ctx context.Context, ffmpegPath, filePath string) (bool, error) {
	if strings.TrimSpace(ffmpegPath) == "" {
		return false, fmt.Errorf("ffmpeg path not configured")
	}
	// -bsf:v filter_units=pass_types=8 keeps only PPS NAL units (type 8); the
	// Annex-B h264 muxer emits them start-code delimited on stdout.
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-v", "error",
		"-t", fmt.Sprintf("%d", copySafetyScanSeconds),
		"-i", filePath,
		"-map", "0:v:0",
		"-c:v", "copy",
		"-bsf:v", "filter_units=pass_types=8",
		"-f", "h264",
		"-",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("pps scan: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	return annexBHasConflictingPPS(stdout.Bytes()), nil
}

// annexBHasConflictingPPS reports whether the Annex-B PPS stream redefines any
// single pic_parameter_set_id with more than one distinct payload. A stream
// that uses several distinct ids (each with one definition) is legal and not
// flagged; only conflicting redefinitions of the same id are unsafe.
func annexBHasConflictingPPS(data []byte) bool {
	byID := make(map[uint]map[string]struct{})
	for _, nal := range splitAnnexBNALs(data) {
		if len(nal) < 2 || nal[0]&0x1f != 8 {
			continue // not a PPS NAL
		}
		id, ok := ppsParameterSetID(nal[1:])
		if !ok {
			continue
		}
		if byID[id] == nil {
			byID[id] = make(map[string]struct{})
		}
		// nal_ref_idc belongs to the NAL header, not the PPS definition. Two
		// otherwise-identical PPS payloads may carry different valid priorities.
		byID[id][string(nal[1:])] = struct{}{}
		if len(byID[id]) > 1 {
			return true
		}
	}
	return false
}

// splitAnnexBNALs splits an Annex-B byte stream into NAL unit payloads,
// dropping the 3- or 4-byte start codes.
func splitAnnexBNALs(data []byte) [][]byte {
	var nals [][]byte
	n := len(data)
	for start := nextStartCode(data, 0); start >= 0; {
		// skip the 3-byte start code (00 00 01); a leading extra 00 stays with
		// the previous NAL's trailing bytes, harmless for delimiting.
		payloadStart := start + 3
		next := nextStartCode(data, payloadStart)
		end := n
		if next >= 0 {
			end = next
		}
		// Trim trailing zero bytes: the 4th byte of a 00 00 00 01 start code and
		// any trailing_zero_bits/cabac_zero_word belong to the delimiter, not the
		// NAL. Without this, an identical PPS before a 4-byte start code and one
		// at end-of-stream compare unequal.
		for end > payloadStart && data[end-1] == 0x00 {
			end--
		}
		if payloadStart < end {
			nals = append(nals, data[payloadStart:end])
		}
		start = next
	}
	return nals
}

// nextStartCode returns the index of the 00 00 01 sequence at or after from,
// pointing at the first 00 (a leading 00 00 00 01 start code resolves to the
// three trailing bytes, which is sufficient for delimiting). Returns -1 when
// none remains.
func nextStartCode(data []byte, from int) int {
	for i := from; i+2 < len(data); i++ {
		if data[i] == 0x00 && data[i+1] == 0x00 && data[i+2] == 0x01 {
			return i
		}
	}
	return -1
}

// ppsParameterSetID decodes pic_parameter_set_id, the first ue(v) element of a
// PPS RBSP. It is the leading field, so no emulation-prevention byte can occur
// before it and the raw payload can be read directly. Returns false when the
// payload is too short or malformed.
func ppsParameterSetID(rbsp []byte) (uint, bool) {
	br := bitReader{data: rbsp}
	return br.readUE()
}

type bitReader struct {
	data []byte
	pos  int // bit position
}

func (b *bitReader) readBit() (uint, bool) {
	if b.pos/8 >= len(b.data) {
		return 0, false
	}
	bit := (b.data[b.pos/8] >> (7 - uint(b.pos%8))) & 1
	b.pos++
	return uint(bit), true
}

// readUE decodes an unsigned Exp-Golomb value.
func (b *bitReader) readUE() (uint, bool) {
	zeros := 0
	for {
		bit, ok := b.readBit()
		if !ok {
			return 0, false
		}
		if bit == 1 {
			break
		}
		zeros++
		if zeros > 31 {
			return 0, false
		}
	}
	val := uint(0)
	for i := 0; i < zeros; i++ {
		bit, ok := b.readBit()
		if !ok {
			return 0, false
		}
		val = (val << 1) | bit
	}
	return (1 << uint(zeros)) - 1 + val, true
}
