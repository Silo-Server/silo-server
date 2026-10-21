/// <reference types="@webgpu/types" />
import { ENGINE_PCM_CODECS, FFMPEG_AUDIO_CODECS } from "./codecs";
import { isSoftwareAdapter } from "./gpuAdapter";

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
  /**
   * HDR sources can be tone-mapped for the display: the WebGPU presenter
   * compiled and drew through both of its paths, or, when that check was still
   * running at playback start, a hardware adapter was found and nothing had
   * failed yet (see `supportsHDRPresentation`).
   */
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

/**
 * Playback start waits on the probe, so a GPU or decoder query that never
 * answers must not hold it: past this, the page plays without the engine.
 * The codec answers and the HDR answer are each bounded by it, so a slow GPU
 * never costs the codec answers.
 */
export const PROBE_TIMEOUT_MS = 4_000;

/**
 * How long, once a hardware adapter is found, playback start waits for the
 * rest of the HDR check before trusting the adapter alone.
 */
export const HDR_CHECK_GRACE_MS = 1_000;

/** The full HDR check's answer, once it has a definite one; later probes use it. */
let presenterVerdict: boolean | undefined;
/** The HDR check found a hardware adapter, even if only after its deadline. */
let hardwareAdapter = false;

/** The HDR answer as it stands: the full check's, else whether there is a hardware adapter. */
function hdrAnswer(): boolean {
  return presenterVerdict ?? hardwareAdapter;
}

/** `work`, or `fallback()` once `ms` pass first. */
function withDeadline<T>(work: Promise<T>, ms: number, fallback: () => T): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<T>((resolve) => {
    timer = setTimeout(() => resolve(fallback()), ms);
  });
  return Promise.race([work, deadline]).finally(() => clearTimeout(timer));
}

/**
 * Whether the WebGPU presenter works here: a hardware adapter, a device, the
 * shaders compiled, and a picture drawn through each path (`probePresenter`).
 * The presenter is loaded only once an adapter is found, so pages that never
 * turn the engine on do not carry it.
 *
 * Creating the device and compiling takes milliseconds on most systems and
 * seconds on a cold GPU process with some Linux Vulkan drivers, while every
 * playback start, SDR included, waits for this answer. So once a hardware
 * adapter is found, the rest of the check gets `HDR_CHECK_GRACE_MS`; past
 * that, and as long as nothing has failed, the adapter is answer enough, as
 * it was before the full check existed. The presenter reports any failure at
 * runtime, and what the check learns after its deadline applies to the
 * probes after it.
 */
async function supportsHDRPresentation(): Promise<boolean> {
  const gpu = (navigator as Navigator & { gpu?: GPU }).gpu;
  if (!gpu) return false;
  let adapterFound!: () => void;
  const found = new Promise<void>((resolve) => (adapterFound = resolve));
  const check = (async (): Promise<boolean | null> => {
    const adapter = await gpu.requestAdapter();
    if (!adapter || isSoftwareAdapter(adapter)) return false;
    hardwareAdapter = true;
    adapterFound();
    const { probePresenter } = await import("./renderers");
    return probePresenter(adapter);
  })()
    // A chunk that failed to load says nothing about the presenter.
    .catch(() => null)
    .then((verdict) => {
      if (verdict !== null) presenterVerdict = verdict;
      return hdrAnswer();
    });
  const afterGrace = found.then(() => withDeadline(check, HDR_CHECK_GRACE_MS, hdrAnswer));
  return withDeadline(Promise.race([check, afterGrace]), PROBE_TIMEOUT_MS, hdrAnswer);
}

async function probeCodecs(): Promise<Omit<EngineCapabilities, "hdr"> | null> {
  const [videoResults, audioResults] = await Promise.all([
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
  };
}

async function probe(): Promise<EngineCapabilities | null> {
  if (!runtimeAvailable()) return null;
  const hdr = supportsHDRPresentation();
  const codecs = await withDeadline(
    probeCodecs().catch(() => null),
    PROBE_TIMEOUT_MS,
    () => null,
  );
  return codecs && { ...codecs, hdr: await hdr };
}

let probePromise: Promise<EngineCapabilities | null> | null = null;

/**
 * Probes once per page; the answers do not change while the page lives,
 * except that an HDR check still running at playback start settles `hdr` for
 * the calls after it (the player calls this each time it opens).
 */
export function probeEngineCapabilities(): Promise<EngineCapabilities | null> {
  probePromise ??= probe().catch(() => null);
  return probePromise.then((capabilities) => {
    const hdr = hdrAnswer();
    return capabilities && capabilities.hdr !== hdr ? { ...capabilities, hdr } : capabilities;
  });
}

export function resetEngineCapabilitiesForTests(): void {
  probePromise = null;
  presenterVerdict = undefined;
  hardwareAdapter = false;
}
