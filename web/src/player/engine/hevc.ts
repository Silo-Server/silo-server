/**
 * HEVC helpers for presenting a Dolby Vision stream through its base layer.
 *
 * A Dolby Vision HEVC track interleaves base-layer NAL units with the RPU
 * (NAL type 62) and, for Profile 7 in one track, the enhancement layer wrapped
 * in NAL type 63 or carried with `nuh_layer_id > 0`. An ordinary decoder is
 * only guaranteed to ignore none of these, so they are removed before decode.
 */

import { nalUnitRanges } from "./nalUnits";

const NAL_UNSPEC62_RPU = 62;
const NAL_UNSPEC63_ENHANCEMENT = 63;

/**
 * NAL length-field size from an `hvcC` record (byte 21, low two bits plus
 * one), or null when the record is too short to hold it.
 */
export function hevcLengthSize(hvcC: Uint8Array | null | undefined): number | null {
  if (!hvcC || hvcC.byteLength < 23) return null;
  return (hvcC[21]! & 0x03) + 1;
}

/**
 * Removes Dolby Vision RPU and enhancement-layer NAL units from one
 * length-prefixed access unit. Returns the input unchanged when it holds none,
 * and the input as-is when it does not parse as length-prefixed NAL units.
 */
export function stripDolbyVisionNalUnits(data: Uint8Array, lengthSize: number): Uint8Array {
  const units = nalUnitRanges(data, lengthSize, 2);
  if (!units) return data;
  const keep: Array<[number, number]> = [];
  let kept = 0;
  let removed = false;
  for (const [start, end] of units) {
    const type = (data[start]! >> 1) & 0x3f;
    const layer = ((data[start]! & 0x01) << 5) | (data[start + 1]! >> 3);
    if (type === NAL_UNSPEC62_RPU || type === NAL_UNSPEC63_ENHANCEMENT || layer > 0) {
      removed = true;
    } else {
      // Kept with its length field.
      keep.push([start - lengthSize, end]);
      kept += end - start + lengthSize;
    }
  }
  if (!removed) return data;
  const out = new Uint8Array(kept);
  let cursor = 0;
  for (const [start, end] of keep) {
    out.set(data.subarray(start, end), cursor);
    cursor += end - start;
  }
  return out;
}
