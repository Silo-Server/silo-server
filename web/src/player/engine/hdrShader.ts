/**
 * WGSL for the engine's WebGPU presenter. The color functions mirror
 * colorMath.ts, which carries the reference implementation and its tests.
 *
 * Two entry points read the picture differently and share the conversion:
 * `fragmentExternal` samples a GPU-backed frame whose R'G'B' the browser has
 * already derived with the YUV matrix (the decoder was told the frame is
 * sRGB-tagged so no transfer is applied), and `fragmentPlanes` reconstructs R'G'B' from uploaded 10/12-bit Y'CbCr
 * codes, keeping full precision.
 */
export const PRESENTER_WGSL = /* wgsl */ `
struct Params {
  // Picture quad in normalized device coordinates: x0, y0, x1, y1.
  rect: vec4f,
  // x: transfer (1 = PQ, 2 = HLG); y: bit depth of uploaded planes.
  mode: vec4u,
  // x: source peak nits, y: target (SDR white) nits.
  tone: vec4f,
  // Luma width/height, chroma width/height of uploaded planes.
  sizes: vec4f,
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
  out.uv = corner;
  return out;
}

const PQ_M1: f32 = 0.1593017578125;
const PQ_M2: f32 = 78.84375;
const PQ_C1: f32 = 0.8359375;
const PQ_C2: f32 = 18.8515625;
const PQ_C3: f32 = 18.6875;
const HLG_A: f32 = 0.17883277;
const HLG_B: f32 = 0.28466892;
const HLG_C: f32 = 0.55991073;
const LUMA_2020 = vec3f(0.2627, 0.678, 0.0593);

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
  var linear: vec3f;
  if (params.mode.x == 2u) {
    let scene = hlgToScene(encoded);
    let sceneLuma = dot(scene, LUMA_2020);
    linear = scene * (1000.0 * pow(max(sceneLuma, 1e-6), 0.2));
  } else {
    linear = pqToNits(encoded);
  }
  let sourcePeak = params.tone.x;
  let targetPeak = params.tone.y;
  let luma = dot(linear, LUMA_2020);
  var scale = 0.0;
  if (luma > 0.0) {
    scale = bt2390(luma, sourcePeak, targetPeak) / luma;
  }
  let toBT709 = mat3x3f(
    vec3f(1.660491, -0.124551, -0.018151),
    vec3f(-0.587641, 1.1329, -0.100579),
    vec3f(-0.07285, -0.008349, 1.11873),
  );
  let display = (toBT709 * linear) * scale / targetPeak;
  return vec4f(linearToSRGB(display), 1.0);
}

@group(1) @binding(0) var externalPicture: texture_external;

@fragment
fn fragmentExternal(in: VertexOut) -> @location(0) vec4f {
  let encoded = textureSampleBaseClampToEdge(externalPicture, pictureSampler, in.uv).rgb;
  return toneMap(encoded);
}

@group(1) @binding(0) var lumaPlane: texture_2d<u32>;
@group(1) @binding(1) var cbPlane: texture_2d<u32>;
@group(1) @binding(2) var crPlane: texture_2d<u32>;

// Integer textures cannot be filtered by a sampler, so filter by hand.
fn bilinear(plane: texture_2d<u32>, uv: vec2f, size: vec2f) -> f32 {
  let position = uv * size - 0.5;
  let base = floor(position);
  let fraction = position - base;
  let maxIndex = vec2i(size) - 1;
  let p00 = clamp(vec2i(base), vec2i(0), maxIndex);
  let p11 = clamp(vec2i(base) + 1, vec2i(0), maxIndex);
  let a = f32(textureLoad(plane, vec2i(p00.x, p00.y), 0).r);
  let b = f32(textureLoad(plane, vec2i(p11.x, p00.y), 0).r);
  let c = f32(textureLoad(plane, vec2i(p00.x, p11.y), 0).r);
  let d = f32(textureLoad(plane, vec2i(p11.x, p11.y), 0).r);
  return mix(mix(a, b, fraction.x), mix(c, d, fraction.x), fraction.y);
}

@fragment
fn fragmentPlanes(in: VertexOut) -> @location(0) vec4f {
  let scale = exp2(f32(params.mode.y) - 8.0);
  let y = bilinear(lumaPlane, in.uv, params.sizes.xy) / scale;
  let cb = bilinear(cbPlane, in.uv, params.sizes.zw) / scale;
  let cr = bilinear(crPlane, in.uv, params.sizes.zw) / scale;
  let luma = (y - 16.0) / 219.0;
  let blue = (cb - 128.0) / 224.0;
  let red = (cr - 128.0) / 224.0;
  let encoded = vec3f(
    luma + 1.4746 * red,
    luma - 0.16455313 * blue - 0.57135313 * red,
    luma + 1.8814 * blue,
  );
  return toneMap(encoded);
}
`;
