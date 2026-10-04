import { describe, expect, it } from "vitest";
import {
  defaultSpeakers,
  LinearResampler,
  mixMatrix,
  mixToInterleaved,
  outputChannelCount,
  SPEAKER,
  speakersFromMask,
  unpackPCM,
} from "./audioMix";

const S = SPEAKER;

describe("speakersFromMask", () => {
  it("lists native mask bits in channel order", () => {
    expect(speakersFromMask(0x60f, 6)).toEqual([S.FL, S.FR, S.FC, S.LFE, S.SL, S.SR]);
    expect(speakersFromMask(0x63f, 8)).toEqual([S.FL, S.FR, S.FC, S.LFE, S.BL, S.BR, S.SL, S.SR]);
  });

  it("falls back to the WAVE order when the mask is absent or inconsistent", () => {
    expect(speakersFromMask(0, 6)).toEqual(defaultSpeakers(6));
    expect(speakersFromMask(0x3, 6)).toEqual(defaultSpeakers(6));
  });
});

describe("outputChannelCount", () => {
  it("passes surround through only when the device carries it", () => {
    expect(outputChannelCount(8, 8)).toBe(8);
    expect(outputChannelCount(8, 6)).toBe(6);
    expect(outputChannelCount(6, 2)).toBe(2);
    expect(outputChannelCount(1, 8)).toBe(2);
  });
});

describe("mixMatrix", () => {
  it("downmixes 5.1 to stereo with Lo/Ro coefficients and no LFE", () => {
    const matrix = mixMatrix([S.FL, S.FR, S.FC, S.LFE, S.SL, S.SR], 2);
    const c = Math.SQRT1_2;
    expect(Array.from(matrix.slice(0, 6))).toEqual([1, 0, Math.fround(c), 0, Math.fround(c), 0]);
    expect(Array.from(matrix.slice(6))).toEqual([0, 1, Math.fround(c), 0, 0, Math.fround(c)]);
  });

  it("maps 5.1(side) onto a 5.1 device without loss", () => {
    const matrix = mixMatrix([S.FL, S.FR, S.FC, S.LFE, S.SL, S.SR], 6);
    for (let output = 0; output < 6; output++) {
      const row = Array.from(matrix.slice(output * 6, output * 6 + 6));
      expect(row.filter((gain) => gain !== 0)).toEqual([1]);
    }
  });

  it("folds 7.1 surrounds into a 5.1 device", () => {
    const matrix = mixMatrix([S.FL, S.FR, S.FC, S.LFE, S.BL, S.BR, S.SL, S.SR], 6);
    // Output 4 is the left surround: back-left and side-left both land there.
    expect(matrix[4 * 8 + 4]).toBe(1);
    expect(matrix[4 * 8 + 6]).toBe(1);
  });

  it("plays mono on both speakers", () => {
    expect(Array.from(mixMatrix([S.FC], 2))).toEqual([1, 1]);
  });
});

describe("mixToInterleaved", () => {
  it("applies the matrix and interleaves", () => {
    const out = mixToInterleaved(
      [new Float32Array([1, 0]), new Float32Array([0, 1])],
      new Float32Array([1, 0, 0, 1]),
      2,
    );
    expect(Array.from(out)).toEqual([1, 0, 0, 1]);
  });
});

describe("unpackPCM", () => {
  it("unpacks little-endian 24-bit and big-endian 16-bit samples", () => {
    const s24 = unpackPCM(new Uint8Array([0x00, 0x00, 0x40, 0x00, 0x00, 0xc0]), "pcm-s24", 2);
    expect(s24.map((plane) => plane[0])).toEqual([0.5, -0.5]);
    const s16be = unpackPCM(new Uint8Array([0x40, 0x00]), "pcm-s16be", 1);
    expect(s16be[0]![0]).toBe(0.5);
  });
});

describe("LinearResampler", () => {
  it("produces the expected length across chunk boundaries", () => {
    const resampler = new LinearResampler(44_100, 48_000, 1);
    let produced = 0;
    for (let i = 0; i < 10; i++) produced += resampler.process([new Float32Array(441)])[0]!.length;
    expect(Math.abs(produced - 4800)).toBeLessThanOrEqual(1);
  });

  it("interpolates a ramp continuously over a chunk boundary", () => {
    const resampler = new LinearResampler(2, 4, 1);
    const first = resampler.process([new Float32Array([0, 2])])[0]!;
    const second = resampler.process([new Float32Array([4, 6])])[0]!;
    expect(Array.from([...first, ...second])).toEqual([0, 1, 2, 3, 4, 5, 6]);
  });
});
