package keyframes

import (
	"encoding/binary"

	"github.com/Silo-Server/silo-server/internal/matroska"
)

// maxSignaledReorder is the most frames FFmpeg's H.264 and HEVC decoders
// hold back for reordering (MAX_DELAYED_PIC_COUNT); a header claiming more
// is invalid.
const maxSignaledReorder = 16

// signaledReorder returns how many frames the video's codec headers declare
// a decoder holds back for B-frame reordering, and false when they declare
// none or can't be read. FFmpeg's decoder delay for the stream is then
// exactly this: its H.264 decoder sets has_b_frames to the SPS's
// max_num_reorder_frames when the VUI carries bitstream_restriction, and its
// HEVC decoder to sps_max_num_reorder_pics of the highest sub-layer. Without
// such a header FFmpeg takes the delay from the first frames it decodes
// while probing, which the planning read can't reproduce.
func signaledReorder(e matroska.TrackEntry) (int, bool) {
	for _, enc := range e.Encodings {
		if enc.Scope&matroska.ScopeCodecPrivate != 0 {
			return 0, false
		}
	}
	switch e.CodecID {
	case "V_MPEG4/ISO/AVC":
		return avcReorder(e.CodecPrivate)
	case "V_MPEGH/ISO/HEVC":
		return hevcReorder(e.CodecPrivate)
	case "V_VP8", "V_VP9", "V_AV1":
		// No reordering delay: hidden frames aren't B-frames to FFmpeg.
		return 0, true
	}
	return 0, false
}

// avcReorder reads max_num_reorder_frames from the SPS of an avcC record
// (ISO/IEC 14496-15 5.3.3.1, H.264 7.3.2.1.1 and E.1.1). A record with
// several SPSs doesn't say which one the stream uses.
func avcReorder(avcC []byte) (int, bool) {
	if len(avcC) < 8 || avcC[0] != 1 || avcC[5]&0x1f != 1 {
		return 0, false
	}
	n := int(binary.BigEndian.Uint16(avcC[6:8]))
	if len(avcC) < 8+n || n < 4 || avcC[8]&0x1f != 7 {
		return 0, false
	}
	b := newRBSPReader(avcC[9 : 8+n])
	b.avcSPSToVUI()
	if b.u(1) == 0 { // vui_parameters_present_flag
		return 0, false
	}
	b.avcVUIToRestriction()
	if b.u(1) == 0 { // bitstream_restriction_flag
		return 0, false
	}
	b.u(1) // motion_vectors_over_pic_boundaries_flag
	b.ue() // max_bytes_per_pic_denom
	b.ue() // max_bits_per_mb_denom
	b.ue() // log2_max_mv_length_horizontal
	b.ue() // log2_max_mv_length_vertical
	reorder := b.ue()
	// FFmpeg ignores the restriction when the SPS ends before its last
	// field, max_dec_frame_buffering.
	b.ue()
	return b.reorder(reorder)
}

// avcSPSToVUI reads an H.264 SPS up to vui_parameters_present_flag.
func (b *rbspReader) avcSPSToVUI() {
	profile := b.u(8)
	b.u(16) // constraint flags, level_idc
	b.ue()  // seq_parameter_set_id
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma := b.ue()
		if chroma == 3 {
			b.u(1) // separate color plane flag
		}
		b.ue()           // bit_depth_luma_minus8
		b.ue()           // bit_depth_chroma_minus8
		b.u(1)           // qpprime_y_zero_transform_bypass_flag
		if b.u(1) == 1 { // seq_scaling_matrix_present_flag
			lists := 8
			if chroma == 3 {
				lists = 12
			}
			b.avcScalingLists(lists)
		}
	}
	b.ue()          // log2_max_frame_num_minus4
	switch b.ue() { // pic_order_cnt_type
	case 0:
		b.ue() // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		b.u(1) // delta_pic_order_always_zero_flag
		b.se() // offset_for_non_ref_pic
		b.se() // offset_for_top_to_bottom_field
		cycle := b.ue()
		for i := uint64(0); i < cycle && !b.bad; i++ {
			b.se()
		}
	}
	b.ue()           // max_num_ref_frames
	b.u(1)           // gaps_in_frame_num_value_allowed_flag
	b.ue()           // pic_width_in_mbs_minus1
	b.ue()           // pic_height_in_map_units_minus1
	if b.u(1) == 0 { // frame_mbs_only_flag
		b.u(1) // mb_adaptive_frame_field_flag
	}
	b.u(1)           // direct_8x8_inference_flag
	if b.u(1) == 1 { // frame_cropping_flag
		b.ue()
		b.ue()
		b.ue()
		b.ue()
	}
}

// avcScalingLists skips an SPS's scaling_list() entries (7.3.2.1.1.1).
func (b *rbspReader) avcScalingLists(lists int) {
	for i := 0; i < lists && !b.bad; i++ {
		if b.u(1) == 0 {
			continue
		}
		size := 16
		if i >= 6 {
			size = 64
		}
		last, next := int64(8), int64(8)
		for j := 0; j < size && next != 0 && !b.bad; j++ {
			next = (last + b.se() + 256) % 256
			if next != 0 {
				last = next
			}
		}
	}
}

// avcVUIToRestriction reads an H.264 VUI up to bitstream_restriction_flag.
func (b *rbspReader) avcVUIToRestriction() {
	if b.u(1) == 1 { // aspect_ratio_info_present_flag
		if b.u(8) == 255 { // Extended_SAR
			b.u(32)
		}
	}
	if b.u(1) == 1 { // overscan_info_present_flag
		b.u(1)
	}
	if b.u(1) == 1 { // video_signal_type_present_flag
		b.u(4)
		if b.u(1) == 1 { // color description
			b.u(24)
		}
	}
	if b.u(1) == 1 { // chroma_loc_info_present_flag
		b.ue()
		b.ue()
	}
	if b.u(1) == 1 { // timing_info_present_flag
		b.u(65)
	}
	nalHRD := b.u(1) == 1
	if nalHRD {
		b.hrd()
	}
	vclHRD := b.u(1) == 1
	if vclHRD {
		b.hrd()
	}
	if nalHRD || vclHRD {
		b.u(1) // low_delay_hrd_flag
	}
	b.u(1) // pic_struct_present_flag
}

// hrd skips an H.264 hrd_parameters() (E.1.2).
func (b *rbspReader) hrd() {
	cpb := b.ue() // cpb_cnt_minus1
	if cpb > 31 {
		b.bad = true
		return
	}
	b.u(8) // bit_rate_scale, cpb_size_scale
	for i := uint64(0); i <= cpb && !b.bad; i++ {
		b.ue()
		b.ue()
		b.u(1)
	}
	b.u(20) // four 5-bit lengths
}

// hevcReorder reads sps_max_num_reorder_pics of the highest sub-layer from
// the SPS of an hvcC record (ISO/IEC 14496-15 8.3.3.1, H.265 7.3.2.2.1). A
// record with several SPSs doesn't say which one the stream uses.
func hevcReorder(hvcC []byte) (int, bool) {
	if len(hvcC) < 23 || hvcC[0] != 1 {
		return 0, false
	}
	var sps []byte
	arrays := int(hvcC[22])
	at := 23
	for i := 0; i < arrays; i++ {
		if len(hvcC) < at+3 {
			return 0, false
		}
		nalType := hvcC[at] & 0x3f
		count := int(binary.BigEndian.Uint16(hvcC[at+1 : at+3]))
		at += 3
		for j := 0; j < count; j++ {
			if len(hvcC) < at+2 {
				return 0, false
			}
			n := int(binary.BigEndian.Uint16(hvcC[at : at+2]))
			at += 2
			if len(hvcC) < at+n {
				return 0, false
			}
			if nalType == 33 {
				if sps != nil || n <= 2 {
					return 0, false
				}
				sps = hvcC[at+2 : at+n]
			}
			at += n
		}
	}
	if sps == nil {
		return 0, false
	}
	return hevcSPSReorder(sps)
}

func hevcSPSReorder(sps []byte) (int, bool) {
	b := newRBSPReader(sps)
	b.u(4)                // sps_video_parameter_set_id
	maxSub := int(b.u(3)) //nolint:gosec // G115: a 3-bit field
	b.u(1)                // sps_temporal_id_nesting_flag
	// profile_tier_level(1, maxSub): the general profile and level.
	b.u(96)
	profile, level := make([]bool, maxSub), make([]bool, maxSub)
	for i := 0; i < maxSub; i++ {
		profile[i] = b.u(1) == 1
		level[i] = b.u(1) == 1
	}
	if maxSub > 0 {
		b.u(2 * (8 - maxSub)) // reserved_zero_2bits
	}
	for i := 0; i < maxSub; i++ {
		if profile[i] {
			b.u(88)
		}
		if level[i] {
			b.u(8)
		}
	}
	b.ue()           // sps_seq_parameter_set_id
	if b.ue() == 3 { // chroma_format_idc
		b.u(1) // separate color plane flag
	}
	b.ue()           // pic_width_in_luma_samples
	b.ue()           // pic_height_in_luma_samples
	if b.u(1) == 1 { // conformance_window_flag
		b.ue()
		b.ue()
		b.ue()
		b.ue()
	}
	b.ue() // bit_depth_luma_minus8
	b.ue() // bit_depth_chroma_minus8
	b.ue() // log2_max_pic_order_cnt_lsb_minus4
	first := maxSub
	if b.u(1) == 1 { // sps_sub_layer_ordering_info_present_flag
		first = 0
	}
	var reorder uint64
	for i := first; i <= maxSub; i++ {
		b.ue() // sps_max_dec_pic_buffering_minus1
		reorder = b.ue()
		b.ue() // sps_max_latency_increase_plus1
	}
	return b.reorder(reorder)
}

// reorder returns a reorder count read from a header, or false when the
// read ran past the data or the count is invalid.
func (b *rbspReader) reorder(n uint64) (int, bool) {
	if b.bad || n > maxSignaledReorder {
		return 0, false
	}
	return int(n), true
}

// unescapeRBSP removes the emulation prevention bytes (00 00 03) from a NAL
// unit's payload.
func unescapeRBSP(nal []byte) []byte {
	out := make([]byte, 0, len(nal))
	zeros := 0
	for _, c := range nal {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// rbspReader reads bits MSB first; bad is set once a read runs past the
// payload, after which every read returns 0.
type rbspReader struct {
	data []byte
	// bits is the payload's length, up to its rbsp_stop_one_bit: as in
	// FFmpeg's parser, a field that reaches into it was never written.
	bits int
	pos  int
	bad  bool
}

// newRBSPReader reads the payload of a NAL unit, without its header.
func newRBSPReader(nal []byte) *rbspReader {
	data := unescapeRBSP(nal)
	b := &rbspReader{data: data}
	for i := len(data) - 1; i >= 0; i-- {
		if c := data[i]; c != 0 {
			stop := 0
			for c&1 == 0 {
				c >>= 1
				stop++
			}
			b.bits = i*8 + 7 - stop
			break
		}
	}
	return b
}

func (b *rbspReader) u(n int) uint64 {
	var v uint64
	for ; n > 0; n-- {
		if b.bad || b.pos >= b.bits {
			b.bad = true
			return 0
		}
		v = v<<1 | uint64(b.data[b.pos/8]>>(7-b.pos%8)&1)
		b.pos++
	}
	return v
}

// ue reads an Exp-Golomb code of at most 32 leading zeros.
func (b *rbspReader) ue() uint64 {
	zeros := 0
	for b.u(1) == 0 {
		if b.bad || zeros == 32 {
			b.bad = true
			return 0
		}
		zeros++
	}
	return 1<<zeros - 1 + b.u(zeros)
}

// se reads a signed Exp-Golomb code.
func (b *rbspReader) se() int64 {
	k := int64(b.ue()) //nolint:gosec // G115: ue reads at most 33 bits
	if k&1 == 1 {
		return (k + 1) / 2
	}
	return -k / 2
}
