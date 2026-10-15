/**
 * WGSL for the engine's WebGPU presenter. The color functions mirror
 * colorMath.ts, which carries the reference implementation and its tests.
 *
 * Three entry points read the picture differently and share the conversion.
 * `fragmentExternal` samples a GPU-backed frame whose R'G'B' the browser has
 * already derived with the YUV matrix (the decoder was told the frame is
 * sRGB-tagged so no transfer is applied); `fragmentCopied` samples the same
 * R'G'B' after the frame was copied into a half-float texture, for browsers
 * that import some frames wrongly; and `fragmentPlanes` reconstructs R'G'B'
 * from uploaded 10/12-bit Y'CbCr codes, keeping full precision.
 *
 * A picture drawn smaller than its own size would alias with one sample per
 * pixel, so each entry averages an N x N grid of bilinear samples across the
 * pixel's footprint, N being how many texels of that plane the pixel covers
 * (at most 4). At or above the picture's own size N is 1.
 */
export const PRESENTER_WGSL = /* wgsl */ `
struct Params {
  // Picture quad in normalized device coordinates: x0, y0, x1, y1.
  rect: vec4f,
  // x: transfer (1 = PQ, 2 = HLG); y: bit depth of uploaded planes;
  // z: clockwise quarter turns that put the picture upright; w: 1 to mirror it after.
  mode: vec4u,
  // x: source peak nits, y: target (SDR white) nits.
  tone: vec4f,
  // Luma width/height, chroma width/height of uploaded planes.
  sizes: vec4f,
  // xy: first chroma sample's offset from the first luma sample, in luma
  // pixels; zw: chroma subsampling along x and y (1 or 2).
  chroma: vec4f,
  // Y'CbCr to R'G'B': Cr into R, Cb into G, Cr into G, Cb into B.
  yuv: vec4f,
  // Luminance weights of the source gamut.
  luma: vec4f,
  // Rows of the linear source-gamut to BT.709 matrix.
  gamut0: vec4f,
  gamut1: vec4f,
  gamut2: vec4f,
};

@group(0) @binding(0) var<uniform> params: Params;
@group(0) @binding(1) var pictureSampler: sampler;

struct VertexOut {
  @builtin(position) position: vec4f,
  @location(0) uv: vec2f,
};

@vertex
fn vertexMain(@builtin(vertex_index) index: u32) -> VertexOut {
  var corners = array<vec2f, 6>(
    vec2f(0.0, 0.0), vec2f(1.0, 0.0), vec2f(0.0, 1.0),
    vec2f(0.0, 1.0), vec2f(1.0, 0.0), vec2f(1.0, 1.0),
  );
  let corner = corners[index];
  var out: VertexOut;
  out.position = vec4f(
    mix(params.rect.x, params.rect.z, corner.x),
    mix(params.rect.y, params.rect.w, corner.y),
    0.0,
    1.0,
  );
  out.uv = decodedUV(corner);
  return out;
}

// Where a point of the upright picture (0..1, y down) lies in the decoded
// one: undo the mirror, then the clockwise quarter turns.
fn decodedUV(upright: vec2f) -> vec2f {
  var p = upright;
  if (params.mode.w == 1u) {
    p.x = 1.0 - p.x;
  }
  var uv = p;
  switch params.mode.z {
    case 1u: { uv = vec2f(p.y, 1.0 - p.x); }
    case 2u: { uv = vec2f(1.0 - p.x, 1.0 - p.y); }
    case 3u: { uv = vec2f(1.0 - p.y, p.x); }
    default: {}
  }
  return uv;
}

const PQ_M1: f32 = 0.1593017578125;
const PQ_M2: f32 = 78.84375;
const PQ_C1: f32 = 0.8359375;
const PQ_C2: f32 = 18.8515625;
const PQ_C3: f32 = 18.6875;
const HLG_A: f32 = 0.17883277;
const HLG_B: f32 = 0.28466892;
const HLG_C: f32 = 0.55991073;

fn pqToNits(signal: vec3f) -> vec3f {
  let e = pow(max(signal, vec3f(0.0)), vec3f(1.0 / PQ_M2));
  let n = max(e - PQ_C1, vec3f(0.0)) / (PQ_C2 - PQ_C3 * e);
  return 10000.0 * pow(n, vec3f(1.0 / PQ_M1));
}

fn nitsToPQ(nits: f32) -> f32 {
  let y = pow(max(nits, 0.0) / 10000.0, PQ_M1);
  return pow((PQ_C1 + PQ_C2 * y) / (1.0 + PQ_C3 * y), PQ_M2);
}

fn pqScalarToNits(signal: f32) -> f32 {
  let e = pow(max(signal, 0.0), 1.0 / PQ_M2);
  let n = max(e - PQ_C1, 0.0) / (PQ_C2 - PQ_C3 * e);
  return 10000.0 * pow(n, 1.0 / PQ_M1);
}

fn hlgToScene(signal: vec3f) -> vec3f {
  let e = max(signal, vec3f(0.0));
  let low = e * e / 3.0;
  let high = (exp((e - HLG_C) / HLG_A) + HLG_B) / 12.0;
  return select(high, low, e <= vec3f(0.5));
}

fn bt2390(nits: f32, sourcePeak: f32, targetPeak: f32) -> f32 {
  if (sourcePeak <= targetPeak) {
    return min(nits, targetPeak);
  }
  let sourcePQ = nitsToPQ(sourcePeak);
  let e1 = nitsToPQ(nits) / sourcePQ;
  let maxLum = nitsToPQ(targetPeak) / sourcePQ;
  let knee = 1.5 * maxLum - 0.5;
  var e2 = e1;
  if (e1 >= knee) {
    let t = (e1 - knee) / (1.0 - knee);
    let t2 = t * t;
    let t3 = t2 * t;
    e2 = (2.0 * t3 - 3.0 * t2 + 1.0) * knee + (t3 - 2.0 * t2 + t) * (1.0 - knee) +
      (-2.0 * t3 + 3.0 * t2) * maxLum;
  }
  return pqScalarToNits(min(e2, 1.0) * sourcePQ);
}

fn linearToSRGB(value: vec3f) -> vec3f {
  let v = clamp(value, vec3f(0.0), vec3f(1.0));
  let low = 12.92 * v;
  let high = 1.055 * pow(v, vec3f(1.0 / 2.4)) - 0.055;
  return select(high, low, v <= vec3f(0.0031308));
}

fn toneMap(encoded: vec3f) -> vec4f {
  let weights = params.luma.xyz;
  var linear: vec3f;
  if (params.mode.x == 2u) {
    let scene = hlgToScene(encoded);
    let sceneLuma = dot(scene, weights);
    linear = scene * (1000.0 * pow(max(sceneLuma, 1e-6), 0.2));
  } else {
    linear = pqToNits(encoded);
  }
  let sourcePeak = params.tone.x;
  let targetPeak = params.tone.y;
  let luma = dot(linear, weights);
  var scale = 0.0;
  if (luma > 0.0) {
    scale = bt2390(luma, sourcePeak, targetPeak) / luma;
  }
  let bt709 = vec3f(
    dot(params.gamut0.xyz, linear),
    dot(params.gamut1.xyz, linear),
    dot(params.gamut2.xyz, linear),
  );
  return vec4f(linearToSRGB(bt709 * scale / targetPeak), 1.0);
}

// How many samples per axis cover a pixel spanning these texel steps. The
// tolerance keeps whole-number scales (1:1, 2:1) from tipping into an extra
// row of samples through rounding in the interpolated coordinates.
fn tapCount(stepX: vec2f, stepY: vec2f) -> i32 {
  return i32(clamp(ceil(max(length(stepX), length(stepY)) - 0.01), 1.0, 4.0));
}

// Offset of sample (i, j) of an n x n grid across the pixel, in pixels.
fn tapOffset(i: i32, j: i32, n: i32) -> vec2f {
  return (vec2f(f32(i), f32(j)) + 0.5) / f32(n) - 0.5;
}

@group(1) @binding(0) var externalPicture: texture_external;

@fragment
fn fragmentExternal(in: VertexOut) -> @location(0) vec4f {
  // Derivatives first, while control flow is still uniform.
  let du = dpdx(in.uv);
  let dv = dpdy(in.uv);
  let size = vec2f(textureDimensions(externalPicture));
  let n = tapCount(du * size, dv * size);
  var sum = vec3f(0.0);
  for (var j = 0; j < n; j++) {
    for (var i = 0; i < n; i++) {
      let o = tapOffset(i, j, n);
      let uv = in.uv + du * o.x + dv * o.y;
      sum += textureSampleBaseClampToEdge(externalPicture, pictureSampler, uv).rgb;
    }
  }
  return toneMap(sum / f32(n * n));
}

@group(1) @binding(0) var copiedPicture: texture_2d<f32>;

@fragment
fn fragmentCopied(in: VertexOut) -> @location(0) vec4f {
  // Derivatives first, while control flow is still uniform.
  let du = dpdx(in.uv);
  let dv = dpdy(in.uv);
  let size = vec2f(textureDimensions(copiedPicture));
  let n = tapCount(du * size, dv * size);
  var sum = vec3f(0.0);
  for (var j = 0; j < n; j++) {
    for (var i = 0; i < n; i++) {
      let o = tapOffset(i, j, n);
      let uv = in.uv + du * o.x + dv * o.y;
      sum += textureSampleLevel(copiedPicture, pictureSampler, uv, 0.0).rgb;
    }
  }
  return toneMap(sum / f32(n * n));
}

@group(1) @binding(0) var lumaPlane: texture_2d<u32>;
@group(1) @binding(1) var cbPlane: texture_2d<u32>;
@group(1) @binding(2) var crPlane: texture_2d<u32>;

// Integer textures cannot be filtered by a sampler, so filter by hand. q is
// in texels with texel centres at integers.
fn bilinear(plane: texture_2d<u32>, q: vec2f, size: vec2f) -> f32 {
  let base = floor(q);
  let fraction = q - base;
  let maxIndex = vec2i(size) - 1;
  let p00 = clamp(vec2i(base), vec2i(0), maxIndex);
  let p11 = clamp(vec2i(base) + 1, vec2i(0), maxIndex);
  let a = f32(textureLoad(plane, vec2i(p00.x, p00.y), 0).r);
  let b = f32(textureLoad(plane, vec2i(p11.x, p00.y), 0).r);
  let c = f32(textureLoad(plane, vec2i(p00.x, p11.y), 0).r);
  let d = f32(textureLoad(plane, vec2i(p11.x, p11.y), 0).r);
  return mix(mix(a, b, fraction.x), mix(c, d, fraction.x), fraction.y);
}

// The chroma texel under a point of the picture: chroma sample k sits at luma
// pixel k * subsampling + origin, so this holds for any picture size.
fn chromaTexel(uv: vec2f) -> vec2f {
  return (uv * params.sizes.xy - 0.5 - params.chroma.xy) / params.chroma.zw;
}

@fragment
fn fragmentPlanes(in: VertexOut) -> @location(0) vec4f {
  // Derivatives first, while control flow is still uniform.
  let du = dpdx(in.uv);
  let dv = dpdy(in.uv);
  let lumaSize = params.sizes.xy;
  let chromaSize = params.sizes.zw;

  let nl = tapCount(du * lumaSize, dv * lumaSize);
  var y = 0.0;
  for (var j = 0; j < nl; j++) {
    for (var i = 0; i < nl; i++) {
      let o = tapOffset(i, j, nl);
      y += bilinear(lumaPlane, (in.uv + du * o.x + dv * o.y) * lumaSize - 0.5, lumaSize);
    }
  }
  y /= f32(nl * nl);

  let chromaScale = lumaSize / params.chroma.zw;
  let nc = tapCount(du * chromaScale, dv * chromaScale);
  var cbcr = vec2f(0.0);
  for (var j = 0; j < nc; j++) {
    for (var i = 0; i < nc; i++) {
      let o = tapOffset(i, j, nc);
      let q = chromaTexel(in.uv + du * o.x + dv * o.y);
      cbcr += vec2f(bilinear(cbPlane, q, chromaSize), bilinear(crPlane, q, chromaSize));
    }
  }
  cbcr /= f32(nc * nc);

  let scale = exp2(f32(params.mode.y) - 8.0);
  let luma = (y / scale - 16.0) / 219.0;
  let blue = (cbcr.x / scale - 128.0) / 224.0;
  let red = (cbcr.y / scale - 128.0) / 224.0;
  let encoded = vec3f(
    luma + params.yuv.x * red,
    luma - params.yuv.y * blue - params.yuv.z * red,
    luma + params.yuv.w * blue,
  );
  return toneMap(encoded);
}
`;
