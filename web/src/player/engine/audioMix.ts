/**
 * Channel mapping, downmixing, PCM unpacking and rate conversion for the
 * engine's audio path. Everything here is synchronous and allocation-light: it
 * runs in the worker for every decoded frame.
 */

/** Speaker positions as FFmpeg `AV_CH_*` bit indexes. */
export const SPEAKER = {
  FL: 0,
  FR: 1,
  FC: 2,
  LFE: 3,
  BL: 4,
  BR: 5,
  FLC: 6,
  FRC: 7,
  BC: 8,
  SL: 9,
  SR: 10,
  TC: 11,
  TFL: 12,
  TFC: 13,
  TFR: 14,
  TBL: 15,
  TBC: 16,
  TBR: 17,
} as const;

const S = SPEAKER;

/**
 * Speaker order a decoder uses when it reports only a channel count. WebCodecs
 * AudioData and FFmpeg's defaults both follow the WAVE order.
 */
export function defaultSpeakers(count: number): number[] {
  switch (count) {
    case 1:
      return [S.FC];
    case 2:
      return [S.FL, S.FR];
    case 3:
      return [S.FL, S.FR, S.FC];
    case 4:
      return [S.FL, S.FR, S.BL, S.BR];
    case 5:
      return [S.FL, S.FR, S.FC, S.BL, S.BR];
    case 6:
      return [S.FL, S.FR, S.FC, S.LFE, S.BL, S.BR];
    case 7:
      return [S.FL, S.FR, S.FC, S.LFE, S.BC, S.SL, S.SR];
    case 8:
      return [S.FL, S.FR, S.FC, S.LFE, S.BL, S.BR, S.SL, S.SR];
    default:
      // Unknown wide layouts keep their first pair as the fronts.
      return Array.from({ length: count }, (_, i) => (i === 0 ? S.FL : i === 1 ? S.FR : -1));
  }
}

/** Speakers named by a native FFmpeg channel mask, in channel order. */
export function speakersFromMask(mask: number, count: number): number[] {
  if (mask <= 0) return defaultSpeakers(count);
  const speakers: number[] = [];
  for (let bit = 0; bit < 64 && speakers.length < count; bit++) {
    if (Math.floor(mask / 2 ** bit) % 2 === 1) speakers.push(bit);
  }
  return speakers.length === count ? speakers : defaultSpeakers(count);
}

/**
 * Output channel count for a source: surround passes through when the device
 * output can carry it, everything else plays as stereo.
 */
export function outputChannelCount(sourceChannels: number, deviceMaxChannels: number): number {
  if (sourceChannels >= 8 && deviceMaxChannels >= 8) return 8;
  if (sourceChannels >= 6 && deviceMaxChannels >= 6) return 6;
  return 2;
}

/** Output speakers per channel count; 6 and 8 follow the Web Audio/WAVE order. */
function outputSpeakers(channels: number): number[] {
  if (channels === 8) return [S.FL, S.FR, S.FC, S.LFE, S.BL, S.BR, S.SL, S.SR];
  if (channels === 6) return [S.FL, S.FR, S.FC, S.LFE, S.BL, S.BR];
  return [S.FL, S.FR];
}

const MINUS_3DB = Math.SQRT1_2;

/**
 * Where an input speaker goes when the output has no speaker of its own for
 * it. Downmixing to stereo follows the ATSC A/52 Lo/Ro coefficients: center
 * and surrounds at -3 dB, LFE dropped. The worklet's limiter catches the rare
 * overs this produces on fully correlated loud passages.
 */
function routeSpeaker(speaker: number, outputs: number[]): Array<[number, number]> {
  const direct = outputs.indexOf(speaker);
  if (direct >= 0) return [[direct, 1]];
  const at = (target: number) => outputs.indexOf(target);
  const pair = (left: number, right: number, gain: number): Array<[number, number]> => {
    const routes: Array<[number, number]> = [
      [at(left), gain],
      [at(right), gain],
    ];
    return routes.filter(([index]) => index >= 0);
  };
  const one = (target: number, gain: number): Array<[number, number]> =>
    at(target) >= 0 ? [[at(target), gain]] : [];
  switch (speaker) {
    case S.FC:
      return pair(S.FL, S.FR, MINUS_3DB);
    case S.LFE:
      return [];
    case S.SL:
      return at(S.BL) >= 0 ? one(S.BL, 1) : one(S.FL, MINUS_3DB);
    case S.SR:
      return at(S.BR) >= 0 ? one(S.BR, 1) : one(S.FR, MINUS_3DB);
    case S.BL:
      return at(S.SL) >= 0 ? one(S.SL, 1) : one(S.FL, MINUS_3DB);
    case S.BR:
      return at(S.SR) >= 0 ? one(S.SR, 1) : one(S.FR, MINUS_3DB);
    case S.BC:
      return at(S.BL) >= 0 ? pair(S.BL, S.BR, MINUS_3DB) : pair(S.FL, S.FR, 0.5);
    case S.FLC:
      return one(S.FL, 1);
    case S.FRC:
      return one(S.FR, 1);
    case S.TFL:
    case S.TBL:
      return one(S.FL, 0.5);
    case S.TFR:
    case S.TBR:
      return one(S.FR, 0.5);
    case S.TC:
    case S.TFC:
    case S.TBC:
      return pair(S.FL, S.FR, 0.35);
    default:
      return [];
  }
}

/**
 * Gain matrix, `[output][input]` flattened row-major, taking the input
 * speakers to `outputChannels`. Mono plays on both stereo speakers.
 */
export function mixMatrix(inputSpeakers: number[], outputChannels: number): Float32Array {
  const outputs = outputSpeakers(outputChannels);
  const matrix = new Float32Array(outputs.length * inputSpeakers.length);
  const monoSource = inputSpeakers.length === 1;
  inputSpeakers.forEach((speaker, input) => {
    const routes = monoSource
      ? outputs.slice(0, 2).map((_, index) => [index, 1] as [number, number])
      : routeSpeaker(speaker, outputs);
    for (const [output, gain] of routes) {
      const cell = output * inputSpeakers.length + input;
      matrix[cell] = matrix[cell]! + gain;
    }
  });
  return matrix;
}

/** Applies a mix matrix to planar input, writing interleaved output. */
export function mixToInterleaved(
  planes: readonly Float32Array[],
  matrix: Float32Array,
  outputChannels: number,
  start = 0,
  end = planes[0]?.length ?? 0,
): Float32Array {
  const inputs = planes.length;
  const frames = Math.max(0, end - start);
  const out = new Float32Array(frames * outputChannels);
  for (let o = 0; o < outputChannels; o++) {
    for (let i = 0; i < inputs; i++) {
      const gain = matrix[o * inputs + i]!;
      if (gain === 0) continue;
      const plane = planes[i]!;
      for (let f = 0; f < frames; f++) out[f * outputChannels + o]! += plane[start + f]! * gain;
    }
  }
  return out;
}

/** Mediabunny PCM codec names the engine unpacks itself. */
export type PCMFormat =
  | "pcm-u8"
  | "pcm-s16"
  | "pcm-s16be"
  | "pcm-s24"
  | "pcm-s24be"
  | "pcm-s32"
  | "pcm-s32be"
  | "pcm-f32"
  | "pcm-f32be"
  | "pcm-f64"
  | "pcm-f64be";

/** Server codec name → Mediabunny PCM codec name. */
export const SERVER_PCM_FORMATS: Record<string, PCMFormat> = {
  pcm_u8: "pcm-u8",
  pcm_s16le: "pcm-s16",
  pcm_s16be: "pcm-s16be",
  pcm_s24le: "pcm-s24",
  pcm_s24be: "pcm-s24be",
  pcm_s32le: "pcm-s32",
  pcm_s32be: "pcm-s32be",
  pcm_f32le: "pcm-f32",
  pcm_f32be: "pcm-f32be",
  pcm_f64le: "pcm-f64",
  pcm_f64be: "pcm-f64be",
};

function pcmBytesPerSample(format: PCMFormat): number {
  switch (format) {
    case "pcm-u8":
      return 1;
    case "pcm-s16":
    case "pcm-s16be":
      return 2;
    case "pcm-s24":
    case "pcm-s24be":
      return 3;
    case "pcm-f64":
    case "pcm-f64be":
      return 8;
    default:
      return 4;
  }
}

/** Unpacks interleaved PCM bytes into planar float32. */
export function unpackPCM(data: Uint8Array, format: PCMFormat, channels: number): Float32Array[] {
  const bytes = pcmBytesPerSample(format);
  const frames = Math.floor(data.byteLength / (bytes * channels));
  const planes = Array.from({ length: channels }, () => new Float32Array(frames));
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  for (let f = 0; f < frames; f++) {
    for (let c = 0; c < channels; c++) {
      const at = (f * channels + c) * bytes;
      let value: number;
      switch (format) {
        case "pcm-u8":
          value = (view.getUint8(at) - 128) / 128;
          break;
        case "pcm-s16":
          value = view.getInt16(at, true) / 32768;
          break;
        case "pcm-s16be":
          value = view.getInt16(at, false) / 32768;
          break;
        case "pcm-s24": {
          const raw =
            view.getUint8(at) | (view.getUint8(at + 1) << 8) | (view.getInt8(at + 2) << 16);
          value = raw / 8388608;
          break;
        }
        case "pcm-s24be": {
          const raw =
            (view.getInt8(at) << 16) | (view.getUint8(at + 1) << 8) | view.getUint8(at + 2);
          value = raw / 8388608;
          break;
        }
        case "pcm-s32":
          value = view.getInt32(at, true) / 2147483648;
          break;
        case "pcm-s32be":
          value = view.getInt32(at, false) / 2147483648;
          break;
        case "pcm-f32":
          value = view.getFloat32(at, true);
          break;
        case "pcm-f32be":
          value = view.getFloat32(at, false);
          break;
        case "pcm-f64":
          value = view.getFloat64(at, true);
          break;
        case "pcm-f64be":
          value = view.getFloat64(at, false);
          break;
      }
      planes[c]![f] = value;
    }
  }
  return planes;
}

/**
 * Streaming linear-interpolation resampler for planar audio. Only used when a
 * decoder reports a rate other than the one the AudioContext was opened at,
 * which the container metadata normally predicts exactly.
 *
 * `position` is the next output sample's fractional index into the current
 * chunk; -1..0 interpolates from the previous chunk's last sample.
 */
export class LinearResampler {
  private position = 0;
  private readonly previous: Float32Array;

  constructor(
    private readonly inputRate: number,
    private readonly outputRate: number,
    channels: number,
  ) {
    this.previous = new Float32Array(channels);
  }

  process(planes: readonly Float32Array[]): Float32Array[] {
    const frames = planes[0]?.length ?? 0;
    if (frames === 0) return planes.map(() => new Float32Array(0));
    const step = this.inputRate / this.outputRate;
    const capacity = Math.ceil((frames - this.position) / step) + 1;
    const out = planes.map(() => new Float32Array(capacity));
    let written = 0;
    let position = this.position;
    for (;;) {
      const index = Math.floor(position);
      const fraction = position - index;
      if (index > frames - 1 || (index + 1 > frames - 1 && fraction > 0)) break;
      for (let c = 0; c < planes.length; c++) {
        const plane = planes[c]!;
        const a = index < 0 ? this.previous[c]! : plane[index]!;
        const b = fraction > 0 ? plane[index + 1]! : a;
        out[c]![written] = a + (b - a) * fraction;
      }
      written += 1;
      position += step;
    }
    this.position = position - frames;
    for (let c = 0; c < planes.length; c++) this.previous[c] = planes[c]![frames - 1]!;
    return out.map((plane) => plane.subarray(0, written));
  }
}
