import { describe, expect, it } from "vitest";
import { hevcLengthSize, stripDolbyVisionNalUnits } from "./hevc";

/** One 4-byte length-prefixed NAL unit with the given type and layer id. */
function nal(type: number, layer = 0, payload = [0xaa, 0xbb]): number[] {
  const header = [(type << 1) | (layer >> 5), ((layer & 0x1f) << 3) | 1];
  const body = [...header, ...payload];
  return [0, 0, 0, body.length, ...body];
}

describe("stripDolbyVisionNalUnits", () => {
  it("removes RPU, wrapped enhancement-layer and layer>0 units", () => {
    const vps = nal(32);
    const slice = nal(1);
    const input = new Uint8Array([...vps, ...nal(62), ...slice, ...nal(63), ...nal(1, 1)]);
    expect(Array.from(stripDolbyVisionNalUnits(input, 4))).toEqual([...vps, ...slice]);
  });

  it("returns the same buffer when there is nothing to remove", () => {
    const input = new Uint8Array([...nal(19), ...nal(1)]);
    expect(stripDolbyVisionNalUnits(input, 4)).toBe(input);
  });

  it("leaves data that does not parse as length-prefixed units untouched", () => {
    const input = new Uint8Array([0, 0, 1, 0x40, 0x01]);
    expect(stripDolbyVisionNalUnits(input, 4)).toBe(input);
  });
});

describe("hevcLengthSize", () => {
  it("reads lengthSizeMinusOne from an hvcC record", () => {
    const record = new Uint8Array(23);
    record[21] = 0xf3;
    expect(hevcLengthSize(record)).toBe(4);
    expect(hevcLengthSize(new Uint8Array(4))).toBeNull();
  });
});
