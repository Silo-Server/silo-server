/**
 * Reference implementation of the engine's HDR-to-SDR conversion. The WGSL in
 * hdrShader.ts mirrors these functions line for line; the tests exercise them
 * here because the GPU path cannot run under the unit-test runner.
 *
 * The parameters match the server's tone-map recipes (internal/tonemap):
 * BT.2390 EETF with SDR white at 100 nits, BT.2020 to BT.709 gamut.
 */

/** Nits that SDR white represents; the server's tone-map recipes use 100. */
export const SDR_WHITE_NITS = 100;
/**
 * Peak assumed for the source when it carries no usable mastering metadata,
 * and the nominal peak of HLG.
 */
export const DEFAULT_SOURCE_PEAK_NITS = 1000;

const PQ_M1 = 2610 / 16384;
const PQ_M2 = (2523 / 4096) * 128;
const PQ_C1 = 3424 / 4096;
const PQ_C2 = (2413 / 4096) * 32;
const PQ_C3 = (2392 / 4096) * 32;

/** SMPTE ST 2084 EOTF: signal (0..1) to nits. */
export function pqToNits(signal: number): number {
  const e = Math.pow(Math.max(signal, 0), 1 / PQ_M2);
  const n = Math.max(e - PQ_C1, 0) / (PQ_C2 - PQ_C3 * e);
  return 10000 * Math.pow(n, 1 / PQ_M1);
}

/** Inverse ST 2084 EOTF: nits to signal (0..1). */
export function nitsToPQ(nits: number): number {
  const y = Math.pow(Math.max(nits, 0) / 10000, PQ_M1);
  return Math.pow((PQ_C1 + PQ_C2 * y) / (1 + PQ_C3 * y), PQ_M2);
}

const HLG_A = 0.17883277;
const HLG_B = 1 - 4 * HLG_A;
const HLG_C = 0.5 - HLG_A * Math.log(4 * HLG_A);

/** BT.2100 HLG inverse OETF: signal (0..1) to normalized scene light (0..1). */
export function hlgToScene(signal: number): number {
  const e = Math.max(signal, 0);
  return e <= 0.5 ? (e * e) / 3 : (Math.exp((e - HLG_C) / HLG_A) + HLG_B) / 12;
}

/** BT.2020 luminance weights. */
export const BT2020_LUMA: readonly [number, number, number] = [0.2627, 0.678, 0.0593];

/** Linear BT.2020 primaries to linear BT.709 primaries. */
export const BT2020_TO_BT709: readonly number[] = [
  1.660491, -0.587641, -0.07285, -0.124551, 1.1329, -0.008349, -0.018151, -0.100579, 1.11873,
];

/**
 * ITU-R BT.2390 EETF on absolute luminance: compresses [0, sourcePeak] into
 * [0, targetPeak] with a Hermite roll-off in the PQ domain, leaving
 * everything below the knee untouched.
 */
export function bt2390(nits: number, sourcePeak: number, targetPeak: number): number {
  if (sourcePeak <= targetPeak) return Math.min(nits, targetPeak);
  const sourcePQ = nitsToPQ(sourcePeak);
  const e1 = nitsToPQ(nits) / sourcePQ;
  const maxLum = nitsToPQ(targetPeak) / sourcePQ;
  const knee = 1.5 * maxLum - 0.5;
  let e2 = e1;
  if (e1 >= knee) {
    const t = (e1 - knee) / (1 - knee);
    const t2 = t * t;
    const t3 = t2 * t;
    e2 =
      (2 * t3 - 3 * t2 + 1) * knee + (t3 - 2 * t2 + t) * (1 - knee) + (-2 * t3 + 3 * t2) * maxLum;
  }
  return pqToNits(Math.min(e2, 1) * sourcePQ);
}

/** sRGB OETF: linear light (0..1) to the canvas's sRGB-encoded value. */
export function linearToSRGB(value: number): number {
  const v = Math.min(Math.max(value, 0), 1);
  return v <= 0.0031308 ? 12.92 * v : 1.055 * Math.pow(v, 1 / 2.4) - 0.055;
}

export type HDRTransfer = "pq" | "hlg";

/**
 * Converts one pixel of HDR non-linear BT.2020 R'G'B' to sRGB-encoded BT.709
 * for an SDR canvas.
 */
export function toneMapPixel(
  encoded: readonly [number, number, number],
  transfer: HDRTransfer,
  sourcePeak = DEFAULT_SOURCE_PEAK_NITS,
  targetPeak = SDR_WHITE_NITS,
): [number, number, number] {
  let linear: [number, number, number];
  if (transfer === "pq") {
    linear = [pqToNits(encoded[0]), pqToNits(encoded[1]), pqToNits(encoded[2])];
  } else {
    // HLG OOTF for a 1000-nit display: system gamma 1.2.
    const scene: [number, number, number] = [
      hlgToScene(encoded[0]),
      hlgToScene(encoded[1]),
      hlgToScene(encoded[2]),
    ];
    const sceneLuma =
      scene[0] * BT2020_LUMA[0] + scene[1] * BT2020_LUMA[1] + scene[2] * BT2020_LUMA[2];
    const gain = DEFAULT_SOURCE_PEAK_NITS * Math.pow(Math.max(sceneLuma, 1e-6), 0.2);
    linear = [scene[0] * gain, scene[1] * gain, scene[2] * gain];
  }
  const luma = linear[0] * BT2020_LUMA[0] + linear[1] * BT2020_LUMA[1] + linear[2] * BT2020_LUMA[2];
  const scale = luma > 0 ? bt2390(luma, sourcePeak, targetPeak) / luma : 0;
  const m = BT2020_TO_BT709;
  const r = (m[0]! * linear[0] + m[1]! * linear[1] + m[2]! * linear[2]) * scale;
  const g = (m[3]! * linear[0] + m[4]! * linear[1] + m[5]! * linear[2]) * scale;
  const b = (m[6]! * linear[0] + m[7]! * linear[1] + m[8]! * linear[2]) * scale;
  return [linearToSRGB(r / targetPeak), linearToSRGB(g / targetPeak), linearToSRGB(b / targetPeak)];
}

/**
 * Limited-range BT.2020 non-constant-luminance Y'CbCr codes at `bitDepth` to
 * non-linear R'G'B' (0..1).
 */
export function yuvToRGB2020(
  y: number,
  cb: number,
  cr: number,
  bitDepth: number,
): [number, number, number] {
  const scale = 2 ** (bitDepth - 8);
  const luma = (y / scale - 16) / 219;
  const blue = (cb / scale - 128) / 224;
  const red = (cr / scale - 128) / 224;
  return [luma + 1.4746 * red, luma - 0.16455313 * blue - 0.57135313 * red, luma + 1.8814 * blue];
}
