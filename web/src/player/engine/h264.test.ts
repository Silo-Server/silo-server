import { describe, expect, it } from "vitest";
import { h264RandomAccess } from "./h264";

/** One access unit of NAL units with 4-byte length fields. */
function accessUnit(...nals: number[][]): Uint8Array {
  const bytes: number[] = [];
  for (const nal of nals) bytes.push(0, 0, 0, nal.length, ...nal);
  return new Uint8Array(bytes);
}

const IDR_SLICE = [0x65, 0x88, 0x84];
const NON_IDR_SLICE = [0x41, 0x9a, 0x02];
// recovery_frame_cnt 0, exact_match 1, broken_link 0, changing_slice_group_idc 0.
const RECOVERY_NOW = [0xc4];
// recovery_frame_cnt 1 (ue "010"), the rest as above.
const RECOVERY_LATER = [0x58, 0x80];

function sei(...messages: Array<[type: number, payload: number[]]>): number[] {
  const bytes = [0x06];
  for (const [type, payload] of messages) bytes.push(type, payload.length, ...payload);
  bytes.push(0x80);
  return bytes;
}

describe("h264RandomAccess", () => {
  it.each([
    ["an IDR slice", accessUnit([0x09, 0xf0], IDR_SLICE), "idr"],
    [
      "a recovery point that recovers on this picture",
      accessUnit(sei([6, RECOVERY_NOW]), NON_IDR_SLICE),
      "recovery-point",
    ],
    [
      "a recovery point after buffering period and picture timing messages",
      accessUnit(sei([0, [0x80, 0x12]], [1, [0x12, 0x34]], [6, RECOVERY_NOW]), NON_IDR_SLICE),
      "recovery-point",
    ],
    [
      "a recovery point in its own SEI after another SEI",
      accessUnit(sei([5, [0xaa, 0xbb]]), sei([6, RECOVERY_NOW]), NON_IDR_SLICE),
      "recovery-point",
    ],
    [
      "a recovery point that recovers pictures later",
      accessUnit(sei([6, RECOVERY_LATER]), NON_IDR_SLICE),
      null,
    ],
    ["a non-IDR slice without a recovery point", accessUnit(sei([5, [0x01]]), NON_IDR_SLICE), null],
    ["a recovery point with no slice after it", accessUnit(sei([6, RECOVERY_NOW])), null],
    ["a length field that runs past the data", new Uint8Array([0, 0, 0, 9, 0x65, 0x88]), null],
  ])("classifies %s", (_, data, expected) => {
    expect(h264RandomAccess(data, 4)).toBe(expected);
  });

  it("skips emulation prevention bytes inside an earlier SEI payload", () => {
    // The payload's escaped 00 00 03 01 is three bytes once unescaped; read
    // with the 03 kept, the next message would start one byte off.
    const escaped = [0x06, 5, 3, 0x00, 0x00, 0x03, 0x01, 6, 1, ...RECOVERY_NOW, 0x80];
    expect(h264RandomAccess(accessUnit(escaped, NON_IDR_SLICE), 4)).toBe("recovery-point");
  });
});
