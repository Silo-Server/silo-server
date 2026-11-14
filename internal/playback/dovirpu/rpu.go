package dovirpu

import (
	"encoding/binary"
	"errors"
)

// ErrProfile8 reports an RPU that is already Profile 8, such as one this
// package converted. Any other RPU it cannot convert is an ordinary error.
var ErrProfile8 = errors.New("dovirpu: RPU is already Profile 8")

// ConvertRPU converts one Profile 7 RPU to Profile 8.1, byte for byte as
// dovi_tool's mode 2 does: the residual is disabled, the NLQ data is removed,
// a FEL's mapping is reset to identity, and the DM coefficients are replaced
// with the 8.1 ones. rpu is the escaped payload after the 7C 01 NAL header,
// starting with the 0x19 prefix; the result has the same form.
func ConvertRPU(rpu []byte) ([]byte, error) {
	d := unescape(rpu)
	trailing := 0
	for trailing < len(d) && d[len(d)-1-trailing] == 0 {
		trailing++
	}
	end := len(d) - trailing
	if end < 7 || d[0] != 0x19 || d[end-1] != 0x80 {
		return nil, errors.New("dovirpu: not an RPU")
	}
	crcAt := end - 5
	if crc32MPEG2(d[1:crcAt]) != binary.BigEndian.Uint32(d[crcAt:]) {
		return nil, errors.New("dovirpu: RPU CRC mismatch")
	}
	c := converter{r: bitReader{data: d[:crcAt], pos: 8}}
	c.convert()
	if c.err != nil {
		return nil, c.err
	}
	c.w.align()
	out := append([]byte{0x19}, c.w.out...)
	out = binary.BigEndian.AppendUint32(out, crc32MPEG2(c.w.out))
	out = append(out, 0x80)
	out = append(out, make([]byte, trailing)...)
	return escape(out), nil
}

// converter reads the source RPU and writes its 8.1 form (everything after
// the 0x19 prefix). The first error sticks; reads after it return zero.
type converter struct {
	r   bitReader
	w   bitWriter
	err error

	coefDataType uint64
	denomBits    int
}

func (c *converter) fail(err error) {
	if c.err == nil {
		c.err = err
	}
}

func (c *converter) check(ok bool, what string) {
	if !ok {
		c.fail(errors.New("dovirpu: " + what))
	}
}

func (c *converter) read(n int) uint64 {
	if c.err != nil {
		return 0
	}
	v, err := c.r.bits(n)
	c.fail(err)
	return v
}

func (c *converter) readUE() uint64 {
	if c.err != nil {
		return 0
	}
	v, err := c.r.ue()
	c.fail(err)
	return v
}

func (c *converter) skip(n int) {
	if c.err == nil {
		c.fail(c.r.skip(n))
	}
}

func (c *converter) copy(n int) uint64 {
	v := c.read(n)
	c.w.bits(v, n)
	return v
}

func (c *converter) copyUE() uint64 {
	v := c.readUE()
	c.w.ue(v)
	return v
}

// coef skips one mapping or NLQ coefficient: an Exp-Golomb integer part when
// coefficient_data_type is 0, then the fraction. It reports whether both are 0.
func (c *converter) coef() bool {
	zero := true
	if c.coefDataType == 0 {
		v, err := c.r.se()
		c.fail(err)
		zero = v == 0
	}
	return c.read(c.denomBits) == 0 && zero
}

func (c *converter) convert() {
	dmPresent, usePrev, compressedDM := c.header()
	if c.err != nil {
		return
	}
	if !usePrev {
		c.mapping()
	}
	if dmPresent {
		c.dm(compressedDM)
	}
	if c.err != nil {
		return
	}
	// The rest is byte-aligned in the source: extension blocks, CM v4.0 and
	// anything dovi_tool keeps as "remaining". It is copied verbatim.
	c.fail(c.r.alignZeros())
	c.w.copyBits(c.r.data, c.r.pos, len(c.r.data)*8)
}

// header copies rpu_data_header, clearing el_spatial_resampling_filter_flag
// and setting disable_residual_flag.
func (c *converter) header() (dmPresent, usePrev, compressedDM bool) {
	c.check(c.copy(6) == 2, "rpu_type is not 2")
	rpuFormat := c.copy(11)
	vdrRPUProfile := c.copy(4)
	c.check(c.copy(4) == 0, "vdr_rpu_level is not 0")
	if c.copy(1) != 1 || rpuFormat&0x700 != 0 { // vdr_seq_info_present_flag
		c.check(false, "RPU without sequence info")
		return
	}
	c.copy(1) // chroma_resampling_explicit_filter_flag
	c.coefDataType = c.copy(2)
	denom := uint64(0)
	if c.coefDataType == 0 {
		denom = c.copyUE()
	}
	c.copy(2) // vdr_rpu_normalized_idc
	c.copy(1) // bl_video_full_range_flag
	c.check(c.copyUE() == 2, "bl_bit_depth_minus8 is not 2")
	c.check(c.copyUE()&0xFF == 2, "el_bit_depth_minus8 is not 2")
	vdrBitDepth := c.copyUE()
	c.copy(1) // spatial_resampling_filter_flag
	compressedDM = c.copy(3) == 1
	elSpatial := c.read(1) == 1
	disableResidual := c.read(1) == 1
	// dovi_tool's get_dovi_profile: 7 needs the residual and a 12-bit VDR;
	// without the residual it is 8.
	switch {
	case vdrRPUProfile == 1 && (!elSpatial || disableResidual):
		c.fail(ErrProfile8)
	case vdrRPUProfile != 1 || vdrBitDepth != 4:
		c.check(false, "RPU is neither Profile 7 nor Profile 8")
	}
	c.check(denom <= 23, "coefficient_log2_denom is above 23")
	c.check(c.coefDataType <= 1, "invalid coefficient_data_type")
	c.denomBits = int(denom)
	if c.coefDataType == 1 {
		c.denomBits = 32
	}
	c.w.bits(0b01, 2) // el_spatial_resampling_filter_flag 0, disable_residual_flag 1
	dmPresent = c.copy(1) == 1
	usePrev = c.copy(1) == 1
	if usePrev {
		c.copyUE() // prev_vdr_rpu_id
	}
	return dmPresent, usePrev, compressedDM
}

// mapping reads rpu_data_mapping and writes its 8.1 form: no NLQ, one
// partition, the curves unchanged for a MEL and reset to identity for a FEL.
func (c *converter) mapping() {
	idStart := c.r.pos
	c.readUE() // vdr_rpu_id
	c.check(c.readUE() == 0, "mapping_color_space is not 0")
	c.check(c.readUE() == 0, "mapping_chroma_format_idc is not 0")
	idEnd := c.r.pos

	var pieces [3]uint64
	for cmp := range pieces {
		n := c.readUE() // num_pivots_minus2
		c.check(n < 8, "too many reshaping pivots")
		if c.err != nil {
			return
		}
		pieces[cmp] = n + 1
		c.skip(10 * int(n+2)) //nolint:gosec // n < 8, checked above
	}
	pivotsEnd := c.r.pos

	c.check(c.read(3) == 0, "unsupported NLQ method")
	c.check(c.read(10)+c.read(10) == 1023, "NLQ pivots do not add up to 1023")
	c.readUE() // num_x_partitions_minus1
	c.readUE() // num_y_partitions_minus1

	paramsStart := c.r.pos
	for _, n := range pieces {
		for range n {
			c.mappingPiece()
		}
	}
	paramsEnd := c.r.pos
	mel := c.isMEL()
	// The identity curve below is written as integer and fraction parts; with
	// float coefficients (coefficient_data_type 1) it would map every pixel
	// to 0, so refuse that RPU and let its frame fall back.
	c.check(mel || c.coefDataType == 0, "identity mapping with float coefficients")
	if c.err != nil {
		return
	}

	w := &c.w
	w.copyBits(c.r.data, idStart, idEnd)
	if mel {
		w.copyBits(c.r.data, idEnd, pivotsEnd)
	} else {
		for range 3 {
			w.ue(0) // num_pivots_minus2
			w.bits(0, 10)
			w.bits(1023, 10)
		}
	}
	w.ue(0) // num_x_partitions_minus1
	w.ue(0) // num_y_partitions_minus1
	if mel {
		w.copyBits(c.r.data, paramsStart, paramsEnd)
		return
	}
	for range 3 {
		w.ue(0)                   // mapping_idc: polynomial
		w.ue(0)                   // poly_order_minus1
		w.bits(0, 1)              // linear_interp_flag
		for k := range int64(2) { // coefficients 0 and 1
			if c.coefDataType == 0 {
				w.se(k)
			}
			w.bits(0, c.denomBits)
		}
	}
}

func (c *converter) mappingPiece() {
	switch c.readUE() { // mapping_idc
	case 0: // polynomial
		order := c.readUE() // poly_order_minus1
		c.check(order <= 1, "polynomial order above 2")
		if c.err != nil {
			return
		}
		c.check(order != 0 || c.read(1) == 0, "linear-interpolation polynomials are unsupported")
		for range order + 2 {
			c.coef()
		}
	case 1: // MMR
		order := c.read(2) // mmr_order_minus1
		c.check(order <= 2, "MMR order above 3")
		c.coef() // mmr_constant
		for range (order + 1) * 7 {
			c.coef()
		}
	default:
		c.check(false, "invalid mapping_idc")
	}
}

// isMEL reads rpu_data_nlq and reports whether every value is neutral, as
// dovi_tool's is_mel does. Without integer parts (coefficient_data_type 1)
// vdr_in_max_int counts as 0, so such an RPU is never MEL.
func (c *converter) isMEL() bool {
	mel := c.coefDataType == 0
	for range 3 {
		mel = c.read(10) == 0 && mel // nlq_offset
		if c.coefDataType == 0 {
			mel = c.readUE() == 1 && mel // vdr_in_max_int
		}
		mel = c.read(c.denomBits) == 0 && mel // vdr_in_max
		mel = c.nlqParam() && mel             // linear_deadzone_slope
		mel = c.nlqParam() && mel             // linear_deadzone_threshold
	}
	return mel
}

func (c *converter) nlqParam() bool {
	zero := true
	if c.coefDataType == 0 {
		zero = c.readUE() == 0
	}
	return c.read(c.denomBits) == 0 && zero
}

// dm copies vdr_dm_data with dovi_tool's 8.1 coefficients and YCbCr signal
// color space, up to and including the CM v2.9 extension-block count.
func (c *converter) dm(compressed bool) {
	c.copyUE() // affected_dm_metadata_id
	c.copyUE() // current_dm_metadata_id
	c.copyUE() // scene_refresh_flag
	if !compressed {
		c.skip(9*16 + 3*32 + 9*16) // ycc_to_rgb_coef/offset, rgb_to_lms_coef
		for _, k := range p81Coefs {
			c.w.bits(uint64(k.v), k.n)
		}
		eotf := c.copy(16)
		p0, p1, p2 := c.copy(16), c.copy(16), c.copy(32)
		bitDepth := c.copy(5)
		c.check(bitDepth >= 8 && bitDepth <= 16, "signal_bit_depth out of range")
		c.check(p0|p1|p2 != 0 || eotf == 65535, "signal_eotf is not 65535")
		c.read(2)                    // signal_color_space
		c.w.bits(0, 2)               // YCbCr
		c.copy(2 + 2 + 12 + 12 + 10) // chroma format, full range, source min/max PQ, diagonal
	}
	c.check(c.copyUE() <= 255, "too many extension blocks") // CM v2.9 num_ext_blocks
	if c.err == nil {
		c.fail(c.r.alignZeros())
	}
	c.w.align()
}

// p81Coefs are dovi_tool's set_p81_coeffs: ycc_to_rgb_coef0..8,
// ycc_to_rgb_offset0..2 and rgb_to_lms_coef0..8, in bitstream order. Negative
// coefficients are their 16-bit two's complement.
var p81Coefs = []struct {
	v uint32
	n int
}{
	{9574, 16}, {0, 16}, {13802, 16}, {9574, 16}, {0xF9FC, 16}, {0xEB1C, 16}, {9574, 16}, {17610, 16}, {0, 16}, // -1540, -5348
	{16777216, 32}, {134217728, 32}, {134217728, 32},
	{7222, 16}, {8771, 16}, {390, 16}, {2654, 16}, {12430, 16}, {1300, 16}, {0, 16}, {422, 16}, {15962, 16},
}
