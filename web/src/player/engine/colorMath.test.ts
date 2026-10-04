import { describe, expect, it } from "vitest";
import {
  bt2390,
  hlgToScene,
  linearToSRGB,
  nitsToPQ,
  pqToNits,
  SDR_WHITE_NITS,
  toneMapPixel,
  yuvToRGB2020,
} from "./colorMath";

describe("PQ", () => {
  it("round-trips and hits the reference points", () => {
    expect(pqToNits(1)).toBeCloseTo(10_000, 6);
    expect(pqToNits(0)).toBe(0);
    expect(nitsToPQ(100)).toBeCloseTo(0.5081, 3);
    for (const nits of [0.1, 1, 100, 203, 1000, 4000]) {
      expect(pqToNits(nitsToPQ(nits))).toBeCloseTo(nits, 6);
    }
  });
});

describe("HLG", () => {
  it("joins its two segments at the reference level", () => {
    expect(hlgToScene(0.5)).toBeCloseTo(1 / 12, 6);
    expect(hlgToScene(1)).toBeCloseTo(1, 4);
  });
});

describe("bt2390", () => {
  it("leaves shadows and mid-tones alone and lands the source peak on the target", () => {
    expect(bt2390(5, 1000, SDR_WHITE_NITS)).toBeCloseTo(5, 6);
    expect(bt2390(1000, 1000, SDR_WHITE_NITS)).toBeCloseTo(SDR_WHITE_NITS, 1);
  });

  it("is monotonic over the source range", () => {
    let previous = -1;
    for (let nits = 0; nits <= 1000; nits += 5) {
      const mapped = bt2390(nits, 1000, SDR_WHITE_NITS);
      expect(mapped).toBeGreaterThanOrEqual(previous);
      previous = mapped;
    }
  });

  it("passes through when the source fits the target", () => {
    expect(bt2390(80, 100, 203)).toBe(80);
  });
});

describe("toneMapPixel", () => {
  it("maps PQ black and a 1000-nit white to SDR black and white", () => {
    expect(toneMapPixel([0, 0, 0], "pq")).toEqual([0, 0, 0]);
    const white = toneMapPixel([nitsToPQ(1000), nitsToPQ(1000), nitsToPQ(1000)], "pq");
    for (const channel of white) expect(channel).toBeCloseTo(1, 2);
  });

  it("keeps a dim neutral gray neutral and unclipped", () => {
    const pq = nitsToPQ(20);
    const [r, g, b] = toneMapPixel([pq, pq, pq], "pq");
    expect(r).toBeCloseTo(g, 4);
    expect(g).toBeCloseTo(b, 4);
    expect(g).toBeCloseTo(linearToSRGB(20 / SDR_WHITE_NITS), 3);
  });

  it("maps HLG reference white near SDR white", () => {
    const [, g] = toneMapPixel([0.75, 0.75, 0.75], "hlg");
    expect(g).toBeGreaterThan(0.85);
    expect(g).toBeLessThanOrEqual(1);
  });
});

describe("yuvToRGB2020", () => {
  it("decodes limited-range 10-bit black, white and neutral gray", () => {
    expect(yuvToRGB2020(64, 512, 512, 10)).toEqual([0, 0, 0]);
    const white = yuvToRGB2020(940, 512, 512, 10);
    for (const channel of white) expect(channel).toBeCloseTo(1, 6);
    const gray = yuvToRGB2020(502, 512, 512, 10);
    expect(gray[0]).toBeCloseTo(gray[2], 6);
  });
});
