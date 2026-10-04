/// <reference types="@webgpu/types" />
import { ENGINE_PCM_CODECS, FFMPEG_AUDIO_CODECS } from "./codecs";

/**
 * What the browser decode engine can play on this browser, in the server's
 * codec and container vocabulary.
 *
 * These are `declared` claims like the rest of the web capability block: a
 * WebCodecs `isConfigSupported` answer, not a decode of real media. A source
 * that passes here and still fails to decode reports a typed failure, and the
 * server replans onto a packaged route (see docs/architecture/web-decode-engine.md).
 */
export interface EngineCapabilities {
  containers: string[];
  videoCodecs: string[];
  audioCodecs: string[];
  /** WebGPU is available, so HDR sources can be tone-mapped for the display. */
  hdr: boolean;
}

/** Containers Mediabunny demuxes with random access over HTTP ranges. */
const ENGINE_CONTAINERS = ["mkv", "mp4"];

/** Server codec name → a representative WebCodecs configuration to probe. */
const VIDEO_PROBES: Record<string, VideoDecoderConfig> = {
  h264: { codec: "avc1.640028", codedWidth: 1920, codedHeight: 1080 },
  hevc: { codec: "hev1.1.6.L120.90", codedWidth: 1920, codedHeight: 1080 },
  av1: { codec: "av01.0.08M.08", codedWidth: 1920, codedHeight: 1080 },
  vp9: { codec: "vp09.00.40.08", codedWidth: 1920, codedHeight: 1080 },
  vp8: { codec: "vp8", codedWidth: 1920, codedHeight: 1080 },
};

/**
 * A FLAC STREAMINFO header (48 kHz, stereo, 16-bit). WebCodecs requires the
 * stream header as `description` before it will answer for FLAC at all.
 */
function flacDescription(): Uint8Array {
  const header = new Uint8Array(4 + 4 + 34);
  header.set([0x66, 0x4c, 0x61, 0x43], 0); // "fLaC"
  header.set([0x80, 0, 0, 34], 4); // last metadata block, STREAMINFO, 34 bytes
  const info = header.subarray(8);
  info.set([0x10, 0x00, 0x10, 0x00], 0); // min/max block size 4096
  // Sample rate (20 bits), channels - 1 (3 bits), bits per sample - 1 (5 bits).
  const sampleRate = 48_000;
  info[10] = (sampleRate >> 12) & 0xff;
  info[11] = (sampleRate >> 4) & 0xff;
  info[12] = ((sampleRate & 0x0f) << 4) | (1 << 1) | (15 >> 4);
  info[13] = (15 & 0x0f) << 4;
  return header;
}

const WEBCODECS_AUDIO_PROBES: Record<string, () => AudioDecoderConfig> = {
  aac: () => ({ codec: "mp4a.40.2", sampleRate: 48_000, numberOfChannels: 2 }),
  opus: () => ({ codec: "opus", sampleRate: 48_000, numberOfChannels: 2 }),
  mp3: () => ({ codec: "mp3", sampleRate: 48_000, numberOfChannels: 2 }),
  flac: () => ({
    codec: "flac",
    sampleRate: 48_000,
    numberOfChannels: 2,
    description: flacDescription(),
  }),
};

function runtimeAvailable(): boolean {
  return (
    typeof window !== "undefined" &&
    window.isSecureContext === true &&
    typeof Worker !== "undefined" &&
    typeof VideoDecoder !== "undefined" &&
    typeof EncodedVideoChunk !== "undefined" &&
    typeof AudioContext !== "undefined" &&
    typeof AudioWorkletNode !== "undefined" &&
    typeof WebAssembly !== "undefined"
  );
}

async function supportsVideo(config: VideoDecoderConfig): Promise<boolean> {
  try {
    return (await VideoDecoder.isConfigSupported(config)).supported === true;
  } catch {
    return false;
  }
}

async function supportsAudio(config: AudioDecoderConfig): Promise<boolean> {
  if (typeof AudioDecoder === "undefined") return false;
  try {
    return (await AudioDecoder.isConfigSupported(config)).supported === true;
  } catch {
    return false;
  }
}

async function supportsWebGPU(): Promise<boolean> {
  const gpu = (navigator as Navigator & { gpu?: GPU }).gpu;
  if (!gpu) return false;
  try {
    return (await gpu.requestAdapter()) !== null;
  } catch {
    return false;
  }
}

async function probe(): Promise<EngineCapabilities | null> {
  if (!runtimeAvailable()) return null;
  const [videoResults, audioResults, hdr] = await Promise.all([
    Promise.all(
      Object.entries(VIDEO_PROBES).map(
        async ([codec, config]) => [codec, await supportsVideo(config)] as const,
      ),
    ),
    Promise.all(
      Object.entries(WEBCODECS_AUDIO_PROBES).map(
        async ([codec, config]) => [codec, await supportsAudio(config())] as const,
      ),
    ),
    supportsWebGPU(),
  ]);
  const videoCodecs = videoResults.filter(([, ok]) => ok).map(([codec]) => codec);
  if (videoCodecs.length === 0) return null;
  return {
    containers: [...ENGINE_CONTAINERS],
    videoCodecs,
    audioCodecs: [
      ...audioResults.filter(([, ok]) => ok).map(([codec]) => codec),
      ...FFMPEG_AUDIO_CODECS,
      ...ENGINE_PCM_CODECS,
    ],
    hdr,
  };
}

let probePromise: Promise<EngineCapabilities | null> | null = null;

/** Probes once per page; the answers do not change while the page lives. */
export function probeEngineCapabilities(): Promise<EngineCapabilities | null> {
  probePromise ??= probe().catch(() => null);
  return probePromise;
}

export function resetEngineCapabilitiesForTests(): void {
  probePromise = null;
}
