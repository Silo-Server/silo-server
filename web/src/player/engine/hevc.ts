/**
 * HEVC helpers for the engine's HDR presenter.
 *
 * A Dolby Vision HEVC track interleaves base-layer NAL units with the RPU
 * (NAL type 62) and, for Profile 7 in one track, the enhancement layer wrapped
 * in NAL type 63 or carried with `nuh_layer_id > 0`. An ordinary decoder is
 * only guaranteed to ignore none of these, so they are removed before decode.
 *
 * Chromium's HEVC decoders also tag each frame with the colour description in
 * the stream's SPS, ignoring the decoder configuration's `colorSpace`. The
 * engine needs HDR frames tagged as `IMPORT_COLOR_TAG` (see pictureColor.ts)
 * so that the WebGPU presenter receives the PQ/HLG codes untouched; a frame
 * that keeps its PQ tag can only be drawn by the browser's own, much darker,
 * HDR-to-SDR conversion. So the SPS's colour primaries and transfer are
 * rewritten to the import tag before decode, in the decoder configuration and
 * wherever an SPS repeats in the stream. The matrix stays: the decoder still
 * needs it to derive R'G'B'.
 */

import { BitReader, escapeRbsp, nalUnitRanges, unescapeRbsp } from "./nalUnits";
import { IMPORT_COLOR_CODE_POINTS } from "./pictureColor";

const NAL_SPS = 33;
const NAL_UNSPEC62_RPU = 62;
const NAL_UNSPEC63_ENHANCEMENT = 63;

/** Sanity bounds that stop a corrupt SPS from looping for long. */
const MAX_SHORT_TERM_REF_PIC_SETS = 64;
const MAX_DELTA_POCS = 32;
const MAX_LONG_TERM_REF_PICS = 32;

/**
 * NAL length-field size from an `hvcC` record (byte 21, low two bits plus
 * one), or null when the record is too short to hold it.
 */
export function hevcLengthSize(hvcC: Uint8Array | null | undefined): number | null {
  if (!hvcC || hvcC.byteLength < 23) return null;
  return (hvcC[21]! & 0x03) + 1;
}

export interface HevcAccessUnitOptions {
  /** Remove Dolby Vision RPU and enhancement-layer NAL units. */
  stripDolbyVision: boolean;
  /** Rewrite in-band SPS colour descriptions to the import tag. */
  retagColor: boolean;
}

/** How each access unit of an HEVC track is prepared before decode. */
export interface HevcPreparation {
  lengthSize: number;
  options: HevcAccessUnitOptions;
}

/**
 * Prepares an HEVC track for decode as `options` say: its `hvcC` record, with
 * the SPS retagged when asked, and how each access unit is prepared, or null
 * when access units need no change.
 */
export function prepareHevcTrack(
  hvcC: Uint8Array,
  options: HevcAccessUnitOptions,
): { description: Uint8Array; units: HevcPreparation | null } {
  const description = options.retagColor ? retagHevcDecoderConfig(hvcC) : hvcC;
  const lengthSize = hevcLengthSize(hvcC);
  const needed = options.stripDolbyVision || options.retagColor;
  return { description, units: needed && lengthSize ? { lengthSize, options } : null };
}

function nalType(data: Uint8Array, start: number): number {
  return (data[start]! >> 1) & 0x3f;
}

function nalLayer(data: Uint8Array, start: number): number {
  return ((data[start]! & 0x01) << 5) | (data[start + 1]! >> 3);
}

/**
 * Prepares one length-prefixed access unit for decode as `options` say.
 * Returns the input itself when nothing changes, including when it does not
 * parse as length-prefixed NAL units.
 */
export function prepareHevcAccessUnit(
  data: Uint8Array,
  lengthSize: number,
  options: HevcAccessUnitOptions,
): Uint8Array {
  const units = nalUnitRanges(data, lengthSize, 2);
  if (!units) return data;
  // Most access units hold nothing to change: leave them without copying.
  const touched = units.some(([start]) => {
    const type = nalType(data, start);
    const layer = nalLayer(data, start);
    if (options.retagColor && type === NAL_SPS && layer === 0) return true;
    return (
      options.stripDolbyVision &&
      (type === NAL_UNSPEC62_RPU || type === NAL_UNSPEC63_ENHANCEMENT || layer > 0)
    );
  });
  if (!touched) return data;
  const kept: Uint8Array[] = [];
  let changed = false;
  for (const [start, end] of units) {
    const type = nalType(data, start);
    const layer = nalLayer(data, start);
    if (
      options.stripDolbyVision &&
      (type === NAL_UNSPEC62_RPU || type === NAL_UNSPEC63_ENHANCEMENT || layer > 0)
    ) {
      changed = true;
      continue;
    }
    const unit = data.subarray(start, end);
    const retagged =
      options.retagColor && type === NAL_SPS && layer === 0 ? retagHevcSps(unit) : null;
    if (retagged && retagged.byteLength < 2 ** (8 * lengthSize)) {
      kept.push(retagged);
      changed = true;
    } else {
      kept.push(unit);
    }
  }
  if (!changed) return data;
  const out = new Uint8Array(kept.reduce((sum, unit) => sum + lengthSize + unit.byteLength, 0));
  let cursor = 0;
  for (const unit of kept) {
    for (let i = lengthSize - 1; i >= 0; i--) {
      out[cursor + i] = Math.floor(unit.byteLength / 2 ** (8 * (lengthSize - 1 - i))) & 0xff;
    }
    out.set(unit, cursor + lengthSize);
    cursor += lengthSize + unit.byteLength;
  }
  return out;
}

/**
 * The `hvcC` record with every base-layer SPS's colour description rewritten
 * to the import tag. Returns the input itself when nothing changes or the
 * record does not parse.
 */
export function retagHevcDecoderConfig(hvcC: Uint8Array): Uint8Array {
  if (hvcC.byteLength < 23) return hvcC;
  const parts: Uint8Array[] = [hvcC.subarray(0, 23)];
  let pos = 23;
  let changed = false;
  const arrays = hvcC[22]!;
  for (let a = 0; a < arrays; a++) {
    if (pos + 3 > hvcC.byteLength) return hvcC;
    const type = hvcC[pos]! & 0x3f;
    const count = (hvcC[pos + 1]! << 8) | hvcC[pos + 2]!;
    parts.push(hvcC.subarray(pos, pos + 3));
    pos += 3;
    for (let n = 0; n < count; n++) {
      if (pos + 2 > hvcC.byteLength) return hvcC;
      const length = (hvcC[pos]! << 8) | hvcC[pos + 1]!;
      const end = pos + 2 + length;
      if (length < 2 || end > hvcC.byteLength) return hvcC;
      const unit = hvcC.subarray(pos + 2, end);
      pos = end;
      const retagged = type === NAL_SPS && nalLayer(unit, 0) === 0 ? retagHevcSps(unit) : null;
      if (retagged && retagged.byteLength <= 0xffff) {
        changed = true;
        parts.push(Uint8Array.of(retagged.byteLength >> 8, retagged.byteLength & 0xff), retagged);
      } else {
        parts.push(hvcC.subarray(end - length - 2, end));
      }
    }
  }
  if (!changed) return hvcC;
  parts.push(hvcC.subarray(pos));
  const out = new Uint8Array(parts.reduce((sum, part) => sum + part.byteLength, 0));
  let cursor = 0;
  for (const part of parts) {
    out.set(part, cursor);
    cursor += part.byteLength;
  }
  return out;
}

/**
 * One SPS NAL unit (with its two-byte header) with its colour primaries and
 * transfer rewritten to the import tag, or null when there is nothing to
 * rewrite: the SPS has no colour description, already carries the tag, or
 * does not parse.
 */
export function retagHevcSps(unit: Uint8Array): Uint8Array | null {
  if (unit.byteLength < 3) return null;
  const rbsp = unescapeRbsp(unit.subarray(2));
  let offset: number | null;
  try {
    offset = colourDescriptionOffset(rbsp);
  } catch {
    return null;
  }
  if (offset === null) return null;
  const reader = new BitReader(rbsp);
  reader.position = offset;
  const { primaries, transfer } = IMPORT_COLOR_CODE_POINTS;
  if (reader.bits(8) === primaries && reader.bits(8) === transfer) return null;
  const patched = rbsp.slice();
  writeBits(patched, offset, 8, primaries);
  writeBits(patched, offset + 8, 8, transfer);
  const escaped = escapeRbsp(patched);
  const out = new Uint8Array(2 + escaped.byteLength);
  out.set(unit.subarray(0, 2));
  out.set(escaped, 2);
  return out;
}

function writeBits(data: Uint8Array, offset: number, count: number, value: number): void {
  for (let i = 0; i < count; i++) {
    const bit = offset + i;
    const mask = 0x80 >> (bit & 7);
    if ((value >> (count - 1 - i)) & 1) data[bit >> 3]! |= mask;
    else data[bit >> 3]! &= ~mask;
  }
}

/**
 * Bit position of `colour_primaries` in a base-layer SPS RBSP (H.265
 * 7.3.2.2), or null when the SPS carries no colour description. Throws a
 * RangeError when the SPS ends early or is implausible.
 */
function colourDescriptionOffset(rbsp: Uint8Array): number | null {
  const r = new BitReader(rbsp);
  r.skip(4); // sps_video_parameter_set_id
  const maxSubLayersMinus1 = r.bits(3);
  r.skip(1); // sps_temporal_id_nesting_flag
  profileTierLevel(r, maxSubLayersMinus1);
  r.ue(); // sps_seq_parameter_set_id
  if (r.ue() === 3) r.skip(1); // chroma_format_idc, separate_colour_plane_flag
  r.ue(); // pic_width_in_luma_samples
  r.ue(); // pic_height_in_luma_samples
  if (r.bit()) {
    // conformance window offsets
    for (let i = 0; i < 4; i++) r.ue();
  }
  r.ue(); // bit_depth_luma_minus8
  r.ue(); // bit_depth_chroma_minus8
  const log2MaxPocLsb = r.ue() + 4;
  const orderingInfoForAll = r.bit();
  for (let i = orderingInfoForAll ? 0 : maxSubLayersMinus1; i <= maxSubLayersMinus1; i++) {
    r.ue(); // sps_max_dec_pic_buffering_minus1
    r.ue(); // sps_max_num_reorder_pics
    r.ue(); // sps_max_latency_increase_plus1
  }
  // Coding block, transform block and transform hierarchy sizes.
  for (let i = 0; i < 6; i++) r.ue();
  if (r.bit() && r.bit()) scalingListData(r); // scaling_list_enabled, sps_scaling_list_data_present
  r.skip(2); // amp_enabled_flag, sample_adaptive_offset_enabled_flag
  if (r.bit()) {
    // pcm_enabled_flag
    r.skip(8); // PCM sample bit depths
    r.ue();
    r.ue();
    r.skip(1); // pcm_loop_filter_disabled_flag
  }
  shortTermRefPicSets(r);
  if (r.bit()) {
    // long_term_ref_pics_present_flag
    const count = r.ue();
    if (count > MAX_LONG_TERM_REF_PICS) throw new RangeError("too many long-term pictures");
    for (let i = 0; i < count; i++) r.skip(log2MaxPocLsb + 1);
  }
  r.skip(2); // sps_temporal_mvp_enabled_flag, strong_intra_smoothing_enabled_flag
  if (!r.bit()) return null; // vui_parameters_present_flag
  if (r.bit()) {
    // aspect_ratio_info_present_flag
    if (r.bits(8) === 255) r.skip(32); // EXTENDED_SAR: sar_width, sar_height
  }
  if (r.bit()) r.skip(1); // overscan_info_present_flag, overscan_appropriate_flag
  if (!r.bit()) return null; // video_signal_type_present_flag
  r.skip(4); // video_format, video_full_range_flag
  if (!r.bit()) return null; // colour_description_present_flag
  // The colour description itself must be there to be rewritten.
  r.skip(24);
  return r.position - 24;
}

function profileTierLevel(r: BitReader, maxSubLayersMinus1: number): void {
  // General profile space, tier, profile, compatibility and constraint flags,
  // then general_level_idc.
  r.skip(96);
  const profilePresent: number[] = [];
  const levelPresent: number[] = [];
  for (let i = 0; i < maxSubLayersMinus1; i++) {
    profilePresent.push(r.bit());
    levelPresent.push(r.bit());
  }
  if (maxSubLayersMinus1 > 0) r.skip(2 * (8 - maxSubLayersMinus1));
  for (let i = 0; i < maxSubLayersMinus1; i++) {
    if (profilePresent[i]) r.skip(88);
    if (levelPresent[i]) r.skip(8);
  }
}

function scalingListData(r: BitReader): void {
  for (let sizeId = 0; sizeId < 4; sizeId++) {
    for (let matrixId = 0; matrixId < 6; matrixId += sizeId === 3 ? 3 : 1) {
      if (!r.bit()) {
        r.ue(); // scaling_list_pred_matrix_id_delta
        continue;
      }
      const coefficients = Math.min(64, 1 << (4 + (sizeId << 1)));
      if (sizeId > 1) r.se(); // scaling_list_dc_coef_minus8
      for (let i = 0; i < coefficients; i++) r.se();
    }
  }
}

/** A short-term reference picture set's delta POCs, before and after the picture. */
interface RefPicSet {
  negative: number[];
  positive: number[];
}

/**
 * Reads the SPS's `st_ref_pic_set` structures (H.265 7.3.7). A predicted set
 * codes one flag group per entry of the set before it, so each set's delta
 * POCs are derived as in equations 7-61 and 7-62: an entry whose predicted
 * delta is 0 drops out, which a plain count of the flags would miss.
 */
function shortTermRefPicSets(r: BitReader): void {
  const count = r.ue();
  if (count > MAX_SHORT_TERM_REF_PIC_SETS) throw new RangeError("too many short-term RPS");
  const sets: RefPicSet[] = [];
  for (let index = 0; index < count; index++) {
    // inter_ref_pic_set_prediction_flag; in an SPS a predicted set always
    // refers to the set before it (delta_idx_minus1 is only in slice headers).
    sets.push(
      index > 0 && r.bit() ? predictedRefPicSet(r, sets[index - 1]!) : explicitRefPicSet(r),
    );
  }
}

function explicitRefPicSet(r: BitReader): RefPicSet {
  const negativeCount = r.ue();
  const positiveCount = r.ue();
  if (negativeCount + positiveCount > MAX_DELTA_POCS) throw new RangeError("too many delta POCs");
  const read = (count: number, sign: number): number[] => {
    const deltas: number[] = [];
    let poc = 0;
    for (let i = 0; i < count; i++) {
      poc += sign * (r.ue() + 1); // delta_poc_s*_minus1
      r.skip(1); // used_by_curr_pic_s*_flag
      deltas.push(poc);
    }
    return deltas;
  };
  const negative = read(negativeCount, -1);
  return { negative, positive: read(positiveCount, 1) };
}

function predictedRefPicSet(r: BitReader, ref: RefPicSet): RefPicSet {
  const sign = r.bit(); // delta_rps_sign
  const deltaRps = (1 - 2 * sign) * (r.ue() + 1); // abs_delta_rps_minus1
  const refCount = ref.negative.length + ref.positive.length;
  // use_delta_flag per entry of the reference set, then for deltaRps itself;
  // it is inferred as 1 where used_by_curr_pic_flag is set.
  const use: boolean[] = [];
  for (let j = 0; j <= refCount; j++) use.push(r.bit() === 1 || r.bit() === 1);
  const negative: number[] = [];
  const positive: number[] = [];
  const s0 = ref.negative;
  const s1 = ref.positive;
  for (let j = s1.length - 1; j >= 0; j--) {
    const poc = s1[j]! + deltaRps;
    if (poc < 0 && use[s0.length + j]) negative.push(poc);
  }
  if (deltaRps < 0 && use[refCount]) negative.push(deltaRps);
  for (let j = 0; j < s0.length; j++) {
    const poc = s0[j]! + deltaRps;
    if (poc < 0 && use[j]) negative.push(poc);
  }
  for (let j = s0.length - 1; j >= 0; j--) {
    const poc = s0[j]! + deltaRps;
    if (poc > 0 && use[j]) positive.push(poc);
  }
  if (deltaRps > 0 && use[refCount]) positive.push(deltaRps);
  for (let j = 0; j < s1.length; j++) {
    const poc = s1[j]! + deltaRps;
    if (poc > 0 && use[s0.length + j]) positive.push(poc);
  }
  if (negative.length + positive.length > MAX_DELTA_POCS) {
    throw new RangeError("too many delta POCs");
  }
  return { negative, positive };
}
