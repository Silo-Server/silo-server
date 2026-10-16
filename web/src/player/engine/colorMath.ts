import type { ChromaSiting, SourceMatrix, SourcePrimaries } from "./pictureColor";

/**
 * Reference implementation of the engine's HDR-to-SDR conversion. The WGSL in
 * hdrShader.ts mirrors these functions line for line; the tests exercise them
 * here because the GPU path cannot run under the unit-test runner.
 *
 * The parameters match the server's tone-map recipes (internal/tonemap):
 * BT.2390 EETF with SDR white at 100 nits, the source gamut (BT.2020 unless the
 * stream says otherwise) to BT.709.
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

type Vec3 = readonly [number, number, number];

/** BT.2020 luminance weights. */
export const BT2020_LUMA: Vec3 = [0.2627, 0.678, 0.0593];

/** Luminance weights of each source gamut (the Y row of its RGB-to-XYZ matrix). */
export const LUMA_WEIGHTS: Record<SourcePrimaries, Vec3> = {
  bt2020: BT2020_LUMA,
  bt709: [0.2126, 0.7152, 0.0722],
  p3: [0.2289746, 0.6917385, 0.0792869],
};

/** Linear BT.2020 primaries to linear BT.709 primaries. */
export const BT2020_TO_BT709: readonly number[] = [
  1.660491, -0.587641, -0.07285, -0.124551, 1.1329, -0.008349, -0.018151, -0.100579, 1.11873,
];

/** Linear source primaries to linear BT.709, row-major. */
export const TO_BT709: Record<SourcePrimaries, readonly number[]> = {
  bt2020: BT2020_TO_BT709,
  bt709: [1, 0, 0, 0, 1, 0, 0, 0, 1],
  p3: [1.2249401, -0.2249404, 0, -0.0420569, 1.0420571, 0, -0.0196376, -0.0786361, 1.0982735],
};

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
 * Converts one pixel of HDR non-linear R'G'B' in the source gamut to
 * sRGB-encoded BT.709 for an SDR canvas.
 */
export function toneMapPixel(
  encoded: Vec3,
  transfer: HDRTransfer,
  primaries: SourcePrimaries = "bt2020",
  sourcePeak = DEFAULT_SOURCE_PEAK_NITS,
  targetPeak = SDR_WHITE_NITS,
): [number, number, number] {
  const weights = LUMA_WEIGHTS[primaries];
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
    const sceneLuma = scene[0] * weights[0] + scene[1] * weights[1] + scene[2] * weights[2];
    const gain = DEFAULT_SOURCE_PEAK_NITS * Math.pow(Math.max(sceneLuma, 1e-6), 0.2);
    linear = [scene[0] * gain, scene[1] * gain, scene[2] * gain];
  }
  const luma = linear[0] * weights[0] + linear[1] * weights[1] + linear[2] * weights[2];
  const scale = luma > 0 ? bt2390(luma, sourcePeak, targetPeak) / luma : 0;
  const m = TO_BT709[primaries];
  const r = (m[0]! * linear[0] + m[1]! * linear[1] + m[2]! * linear[2]) * scale;
  const g = (m[3]! * linear[0] + m[4]! * linear[1] + m[5]! * linear[2]) * scale;
  const b = (m[6]! * linear[0] + m[7]! * linear[1] + m[8]! * linear[2]) * scale;
  return [linearToSRGB(r / targetPeak), linearToSRGB(g / targetPeak), linearToSRGB(b / targetPeak)];
}

/**
 * Y'CbCr to R'G'B' coefficients, in the order the shader takes them: Cr into
 * R, Cb into G, Cr into G, Cb into B.
 */
export const YUV_TO_RGB: Record<SourceMatrix, readonly [number, number, number, number]> = {
  bt2020: [1.4746, 0.16455313, 0.57135313, 1.8814],
  bt709: [1.5748, 0.18732427, 0.46812427, 1.8556],
};

/**
 * Limited-range non-constant-luminance Y'CbCr codes at `bitDepth` to
 * non-linear R'G'B' (0..1).
 */
export function yuvToRGB(
  y: number,
  cb: number,
  cr: number,
  bitDepth: number,
  matrix: SourceMatrix = "bt2020",
): [number, number, number] {
  const scale = 2 ** (bitDepth - 8);
  const luma = (y / scale - 16) / 219;
  const blue = (cb / scale - 128) / 224;
  const red = (cr / scale - 128) / 224;
  const [crR, cbG, crG, cbB] = YUV_TO_RGB[matrix];
  return [luma + crR * red, luma - cbG * blue - crG * red, luma + cbB * blue];
}

/**
 * Where, in luma pixels, the first chroma sample's centre sits relative to the
 * first luma sample's centre, along x and y. Only subsampled axes have an
 * offset; 4:2:2 chroma is co-sited horizontally whatever the siting says.
 */
export function chromaOrigin(
  siting: ChromaSiting,
  subsampleX: 1 | 2,
  subsampleY: 1 | 2,
): [number, number] {
  const x = subsampleX === 2 && siting === "center" && subsampleY === 2 ? 0.5 : 0;
  const y = subsampleY === 2 && siting !== "top-left" ? 0.5 : 0;
  return [x, y];
}

/**
 * The chroma-texel coordinate (texel centres at integers) under a point of the
 * picture given in normalized coordinates. Mirrors `chromaTexel` in the
 * shader; working from luma pixels keeps it right for odd picture sizes.
 */
export function chromaTexel(
  uv: number,
  lumaSize: number,
  origin: number,
  subsample: 1 | 2,
): number {
  return (uv * lumaSize - 0.5 - origin) / subsample;
}
