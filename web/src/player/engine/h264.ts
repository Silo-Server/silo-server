/**
 * H.264 helpers for starting decode in the middle of a stream.
 *
 * Blu-ray and other broadcast-style H.264 is usually coded in open GOPs: an
 * IDR only at the start of the stream, then I-frames that carry a recovery
 * point SEI. Containers mark those I-frames as key frames, and a decoder can
 * start on one; the pictures that follow it in decode order but show before
 * it (leading pictures) reference the GOP before and cannot be decoded.
 */

import { nalUnitRanges } from "./nalUnits";

const NAL_NON_IDR_SLICE = 1;
const NAL_IDR_SLICE = 5;
const NAL_SEI = 6;
const SEI_RECOVERY_POINT = 6;

/** How decoding can start on an access unit, if it can. */
export type H264RandomAccess = "idr" | "recovery-point";

/**
 * NAL length-field size from an `avcC` record (byte 4, low two bits plus
 * one), or null when the record is too short to hold it.
 */
export function avcLengthSize(avcC: Uint8Array | null | undefined): number | null {
  if (!avcC || avcC.byteLength < 5) return null;
  return (avcC[4]! & 0x03) + 1;
}

/**
 * Whether decoding can start on one length-prefixed access unit: at an IDR
 * slice, or at a recovery point SEI that recovers on this very picture
 * (`recovery_frame_cnt` 0). Returns null for anything else, including data
 * that does not parse as length-prefixed NAL units.
 */
export function h264RandomAccess(data: Uint8Array, lengthSize: number): H264RandomAccess | null {
  const units = nalUnitRanges(data, lengthSize, 1);
  if (!units) return null;
  let recoveryPoint = false;
  for (const [start, end] of units) {
    const type = data[start]! & 0x1f;
    if (type === NAL_IDR_SLICE) return "idr";
    if (type === NAL_NON_IDR_SLICE) return recoveryPoint ? "recovery-point" : null;
    if (type === NAL_SEI && recoversImmediately(data.subarray(start + 1, end))) {
      recoveryPoint = true;
    }
  }
  return null;
}

/**
 * Whether an SEI payload holds a recovery point with `recovery_frame_cnt` 0.
 * Unlike the WebCodecs rule Mediabunny follows, `exact_match_flag` is not
 * required: the pictures it may leave inexact are the leading ones, which
 * are not decoded, and a stream the decoder still rejects starts earlier.
 */
function recoversImmediately(escaped: Uint8Array): boolean {
  const sei = unescape(escaped);
  let pos = 0;
  // The last byte holds the RBSP stop bit.
  while (pos < sei.length - 1) {
    let payloadType = 0;
    while (pos < sei.length && sei[pos] === 0xff) payloadType += sei[pos++]!;
    if (pos >= sei.length) return false;
    payloadType += sei[pos++]!;
    let payloadSize = 0;
    while (pos < sei.length && sei[pos] === 0xff) payloadSize += sei[pos++]!;
    if (pos >= sei.length) return false;
    payloadSize += sei[pos++]!;
    if (payloadType === SEI_RECOVERY_POINT) {
      // recovery_frame_cnt is ue(v), and ue(v) is 0 exactly when its first bit is set.
      return pos < sei.length && (sei[pos]! & 0x80) !== 0;
    }
    pos += payloadSize;
  }
  return false;
}

/** Removes emulation prevention bytes (the 0x03 in 00 00 03). */
function unescape(data: Uint8Array): Uint8Array {
  const out = new Uint8Array(data.byteLength);
  let length = 0;
  let zeros = 0;
  for (const byte of data) {
    if (zeros >= 2 && byte === 0x03) {
      zeros = 0;
      continue;
    }
    zeros = byte === 0 ? zeros + 1 : 0;
    out[length++] = byte;
  }
  return out.subarray(0, length);
}
