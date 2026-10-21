import { describe, expect, it } from "vitest";
import {
  bt2390,
  chromaOrigin,
  chromaTexel,
  hlgToScene,
  linearToSRGB,
  LUMA_WEIGHTS,
  nitsToPQ,
  pqToNits,
  SDR_WHITE_NITS,
  TO_BT709,
  toneMapPixel,
  yuvToRGB,
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

describe("yuvToRGB", () => {
  it("decodes limited-range 10-bit black, white and neutral gray", () => {
    for (const matrix of ["bt2020", "bt709"] as const) {
      expect(yuvToRGB(64, 512, 512, 10, matrix)).toEqual([0, 0, 0]);
      const white = yuvToRGB(940, 512, 512, 10, matrix);
      for (const channel of white) expect(channel).toBeCloseTo(1, 6);
      const gray = yuvToRGB(502, 512, 512, 10, matrix);
      expect(gray[0]).toBeCloseTo(gray[2], 6);
    }
  });

  it("reconstructs pure red with each matrix's own coefficients", () => {
    // BT.709 red: Y' 0.2126, Cb -0.1146, Cr 0.5.
    const code = (value: number, offset: number, range: number) => (offset + range * value) * 4;
    const red = yuvToRGB(
      code(0.2126, 16, 219),
      code(-0.114572, 128, 224),
      code(0.5, 128, 224),
      10,
      "bt709",
    );
    expect(red[0]).toBeCloseTo(1, 3);
    expect(red[1]).toBeCloseTo(0, 3);
    expect(red[2]).toBeCloseTo(0, 3);
  });
});

describe("source gamuts", () => {
  it("keep white white and weigh luminance to 1", () => {
    for (const primaries of ["bt2020", "bt709", "p3"] as const) {
      const m = TO_BT709[primaries];
      for (let row = 0; row < 3; row++) {
        expect(m[row * 3]! + m[row * 3 + 1]! + m[row * 3 + 2]!).toBeCloseTo(1, 3);
      }
      const [r, g, b] = LUMA_WEIGHTS[primaries];
      expect(r + g + b).toBeCloseTo(1, 4);
    }
  });

  it("leaves a BT.709 source's colours alone where BT.2020 would widen them", () => {
    // HLG 75% bars at the clips' signal level: a BT.709 red stays a plain red,
    // the same code read as BT.2020 is a deeper red than BT.709 can show.
    const red: [number, number, number] = [0.5625, 0, 0];
    const asBT709 = toneMapPixel(red, "hlg", "bt709");
    const asBT2020 = toneMapPixel(red, "hlg", "bt2020");
    expect(asBT709[1]).toBeCloseTo(0, 6);
    expect(asBT709[0]).toBeLessThan(asBT2020[0]);
    // A neutral gray is the same whichever gamut it comes from.
    const gray: [number, number, number] = [0.5, 0.5, 0.5];
    const [g709] = toneMapPixel(gray, "hlg", "bt709");
    const [g2020] = toneMapPixel(gray, "hlg", "bt2020");
    expect(g709).toBeCloseTo(g2020, 3);
  });
});

describe("chroma siting", () => {
  it("places the first chroma sample for each siting and subsampling", () => {
    expect(chromaOrigin("center", 2, 2)).toEqual([0.5, 0.5]);
    expect(chromaOrigin("left", 2, 2)).toEqual([0, 0.5]);
    expect(chromaOrigin("top-left", 2, 2)).toEqual([0, 0]);
    // 4:2:2 is co-sited horizontally and not subsampled vertically.
    expect(chromaOrigin("center", 2, 1)).toEqual([0, 0]);
    expect(chromaOrigin("left", 1, 1)).toEqual([0, 0]);
  });

  it("reproduces the old centred lookup for even sizes", () => {
    // Before siting was read, chroma was sampled at uv * chromaSize - 0.5.
    for (const uv of [0, 0.25, 0.5, 0.999]) {
      expect(chromaTexel(uv, 1920, 0.5, 2)).toBeCloseTo(uv * 960 - 0.5, 9);
    }
  });

  it("puts co-sited chroma under its luma sample, odd sizes included", () => {
    // The centre of luma pixel 2x is the centre of chroma texel x when co-sited.
    for (const width of [1920, 1919]) {
      const lumaCentre = (x: number) => (x + 0.5) / width;
      expect(chromaTexel(lumaCentre(0), width, 0, 2)).toBeCloseTo(0, 9);
      expect(chromaTexel(lumaCentre(10), width, 0, 2)).toBeCloseTo(5, 9);
      expect(chromaTexel(lumaCentre(11), width, 0, 2)).toBeCloseTo(5.5, 9);
    }
  });
});
