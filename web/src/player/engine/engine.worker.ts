/// <reference lib="webworker" />
/**
 * The engine's decode worker: demuxes the original file over HTTP ranges,
 * decodes video with WebCodecs and audio with WebCodecs, the bundled FFmpeg
 * decoders, or a PCM unpacker, and streams the results out. Video frames go to
 * the page for presentation; audio goes straight to the audio worklet through
 * a MessagePort, so the page's main thread never touches PCM.
 *
 * Decoding is credit-driven. The page releases each video frame after drawing
 * it, and the worklet reports how much audio it has played; the pumps wait
 * whenever too much is outstanding. A seek starts a new generation: older
 * pumps notice and stop, and everything they already sent is dropped on
 * arrival.
 */
import {
  ALL_FORMATS,
  EncodedPacketSink,
  Input,
  UrlSource,
  type EncodedPacket,
  type InputAudioTrack,
  type InputVideoTrack,
} from "mediabunny";
import wasmUrl from "./wasm/silo-audio-decoders.wasm?url";
import {
  defaultSpeakers,
  LinearResampler,
  mixMatrix,
  mixToInterleaved,
  SERVER_PCM_FORMATS,
  speakersFromMask,
  unpackPCM,
  type PCMFormat,
} from "./audioMix";
import { isFFmpegAudioCodec, type FFmpegAudioCodec } from "./codecs";
import {
  FFmpegAudioDecoder,
  loadFFmpegAudioDecoders,
  type DecodedAudioFrame,
} from "./ffmpegAudioDecoder";
import { hevcLengthSize, stripDolbyVisionNalUnits } from "./hevc";
import type {
  EnginePlaneFrame,
  PageToWorkerMessage,
  WorkerToPageMessage,
  WorkerToWorkletMessage,
  WorkletToWorkerMessage,
} from "./protocol";
import type { EngineSourceSpec } from "./source";

const scope = self as unknown as DedicatedWorkerGlobalScope;

/** Decoded frames handed to the page and not yet released. */
const MAX_FRAMES_IN_FLIGHT = 6;
const MAX_VIDEO_DECODE_QUEUE = 4;
/** Audio decoded ahead of what the worklet has played. */
const AUDIO_AHEAD_SECONDS = 1.5;
/**
 * TrueHD can only start at a major sync, and the FFmpeg decoders all need a
 * frame or two of history, so they start a little before the target and the
 * excess is trimmed.
 */
const FFMPEG_PREROLL_SECONDS = 0.5;
const URL_CACHE_BYTES = 96 * 1024 * 1024;
/** Audio sent to the worklet before the page may report the position playable. */
const AUDIO_BUFFERED_SECONDS = 0.25;

export const ENGINE_FAILURES = {
  open: "browser_engine_open_failed",
  videoUnsupported: "browser_engine_video_unsupported",
  videoDecode: "browser_engine_video_decode_failed",
  audioDecode: "browser_engine_audio_decode_failed",
  audioTrack: "browser_engine_audio_track_mismatch",
  read: "browser_engine_read_failed",
} as const;

class EngineFailure extends Error {
  constructor(
    readonly classification: string,
    message: string,
  ) {
    super(message);
  }
}

/** Resolves every pending wait() on the next wake(). */
class Waker {
  private waiters: Array<() => void> = [];
  wait(): Promise<void> {
    return new Promise((resolve) => this.waiters.push(resolve));
  }
  wake(): void {
    const waiters = this.waiters;
    this.waiters = [];
    for (const resolve of waiters) resolve();
  }
}

interface DecodedAudio {
  planes: Float32Array[];
  sampleRate: number;
  speakers: number[];
  timestamp: number;
}

interface AudioDecoderAdapter {
  decode(packet: EncodedPacket): Promise<DecodedAudio[]>;
  flush(): Promise<DecodedAudio[]>;
  close(): void;
}

let source: EngineSourceSpec | null = null;
let input: Input | null = null;
let videoTrack: InputVideoTrack | null = null;
let audioTrack: InputAudioTrack | null = null;
/** Audio track facts, read once with Mediabunny's async getters. */
let audioFacts: { sampleRate: number; channels: number } | null = null;
let videoConfig: VideoDecoderConfig | null = null;
let nalLengthSize: number | null = null;
let audioOutput: { port: MessagePort; channels: number; sampleRate: number } | null = null;

let generation = -1;
let framesInFlight = 0;
let audioPostedSeconds = 0;
let audioConsumedSeconds = 0;
const waker = new Waker();
const planePool: ArrayBuffer[] = [];
let activeVideoDecoder: VideoDecoder | null = null;
let activeAudioDecoder: AudioDecoderAdapter | null = null;

function post(message: WorkerToPageMessage, transfer: Transferable[] = []): void {
  scope.postMessage(message, transfer);
}

function fail(run: number | null, error: unknown, fallback: string): void {
  if (run !== null && run !== generation) return;
  const classification = error instanceof EngineFailure ? error.classification : fallback;
  const message = error instanceof Error ? error.message : String(error);
  post({ type: "error", generation: run, classification, message });
}

/** Server codec name for an audio track, from Mediabunny's codec or the raw Matroska ID. */
async function serverAudioCodec(track: InputAudioTrack): Promise<string> {
  const codec = await track.getCodec();
  if (codec) {
    if (codec.startsWith("pcm-")) {
      const entry = Object.entries(SERVER_PCM_FORMATS).find(([, format]) => format === codec);
      return entry ? entry[0] : codec;
    }
    return codec;
  }
  const id = String((await track.getInternalCodecId()) ?? "").toUpperCase();
  if (id.startsWith("A_TRUEHD")) return "truehd";
  if (id.startsWith("A_MLP")) return "mlp";
  if (id.startsWith("A_DTS")) return "dts";
  return id.toLowerCase();
}

async function open(spec: EngineSourceSpec): Promise<void> {
  source = spec;
  input = new Input({
    formats: ALL_FORMATS,
    source: new UrlSource(spec.url, {
      requestInit: { headers: spec.headers },
      maxCacheSize: URL_CACHE_BYTES,
      getRetryDelay: (attempts) => (attempts < 4 ? Math.min(4, 0.5 * 2 ** attempts) : null),
    }),
  });
  try {
    videoTrack = await input.getPrimaryVideoTrack();
  } catch (error) {
    throw new EngineFailure(ENGINE_FAILURES.open, `Could not read the file: ${String(error)}`);
  }
  if (!videoTrack) throw new EngineFailure(ENGINE_FAILURES.open, "The file has no video track.");

  if (spec.audio) {
    const tracks = await input.getAudioTracks();
    const track = tracks[spec.audio.ordinal] ?? null;
    if (!track || (await serverAudioCodec(track)) !== spec.audio.codec) {
      throw new EngineFailure(
        ENGINE_FAILURES.audioTrack,
        `Audio track ${spec.audio.ordinal} is not the ${spec.audio.codec} track the plan selected.`,
      );
    }
    audioTrack = track;
    audioFacts = {
      sampleRate: await track.getSampleRate(),
      channels: await track.getNumberOfChannels(),
    };
  }

  const config = await videoTrack.getDecoderConfig();
  if (!config) {
    throw new EngineFailure(
      ENGINE_FAILURES.videoUnsupported,
      "The video track has no decoder configuration.",
    );
  }
  if (spec.video.transfer !== "sdr") {
    // Tag HDR frames as BT.709/sRGB so that a GPU-backed frame imported as an
    // external texture keeps its PQ/HLG-encoded R'G'B' values: the browser
    // then applies only the YUV matrix, and the engine's shader decodes the
    // transfer itself. Frames copied out as planes ignore the tag.
    config.colorSpace = {
      primaries: "bt709",
      transfer: "iec61966-2-1",
      // TypeScript's DOM types predate the BT.2020 matrix value.
      matrix: config.colorSpace?.matrix ?? ("bt2020-ncl" as VideoMatrixCoefficients),
      fullRange: config.colorSpace?.fullRange ?? false,
    };
  }
  const support = await VideoDecoder.isConfigSupported(config);
  if (!support.supported) {
    throw new EngineFailure(
      ENGINE_FAILURES.videoUnsupported,
      `This browser cannot decode ${config.codec}.`,
    );
  }
  videoConfig = config;
  nalLengthSize =
    spec.video.stripDolbyVision && (await videoTrack.getCodec()) === "hevc"
      ? hevcLengthSize(config.description ? toBytes(config.description) : null)
      : null;

  let duration = spec.durationSeconds ?? 0;
  if (!(duration > 0)) duration = await input.computeDuration();

  post({
    type: "ready",
    info: {
      durationSeconds: duration,
      video: {
        codedWidth: await videoTrack.getCodedWidth(),
        codedHeight: await videoTrack.getCodedHeight(),
        displayWidth: await videoTrack.getDisplayWidth(),
        displayHeight: await videoTrack.getDisplayHeight(),
      },
      audio: audioFacts,
    },
  });
}

function toBytes(source: AllowSharedBufferSource): Uint8Array {
  // SharedArrayBuffer is not even defined without cross-origin isolation, so
  // only views and plain buffers are told apart.
  return ArrayBuffer.isView(source)
    ? new Uint8Array(source.buffer, source.byteOffset, source.byteLength)
    : new Uint8Array(source);
}

function stopRun(): void {
  const video = activeVideoDecoder;
  activeVideoDecoder = null;
  if (video && video.state !== "closed") video.close();
  const audio = activeAudioDecoder;
  activeAudioDecoder = null;
  audio?.close();
}

function start(run: number, position: number): void {
  stopRun();
  generation = run;
  framesInFlight = 0;
  audioPostedSeconds = 0;
  audioConsumedSeconds = 0;
  waker.wake();
  void runVideo(run, position).catch((error: unknown) =>
    fail(run, error, ENGINE_FAILURES.videoDecode),
  );
  if (audioTrack && audioOutput) {
    void runAudio(run, position).catch((error: unknown) =>
      fail(run, error, ENGINE_FAILURES.audioDecode),
    );
  }
}

const RAW_PLANE_FORMATS = /^I4(20|22|44)P1[02]$/;

function planeBuffer(size: number): ArrayBuffer {
  const index = planePool.findIndex((buffer) => buffer.byteLength >= size);
  if (index >= 0) return planePool.splice(index, 1)[0]!;
  return new ArrayBuffer(size);
}

/**
 * Copies a high-bit-depth software frame out as planes. GPU paths quantize an
 * imported frame to 8 bits; HDR needs the 10-bit codes for clean gradients.
 */
async function copyPlanes(frame: VideoFrame): Promise<EnginePlaneFrame> {
  const format = frame.format!;
  const rect = frame.visibleRect!;
  const bitDepth = format.endsWith("P12") ? 12 : 10;
  const chromaWidth = format.startsWith("I444") ? rect.width : Math.ceil(rect.width / 2);
  const chromaHeight = format.startsWith("I420") ? Math.ceil(rect.height / 2) : rect.height;
  const lumaBytes = rect.width * rect.height * 2;
  const chromaBytes = chromaWidth * chromaHeight * 2;
  const buffer = planeBuffer(lumaBytes + chromaBytes * 2);
  const offsets: [number, number, number] = [0, lumaBytes, lumaBytes + chromaBytes];
  const strides: [number, number, number] = [rect.width * 2, chromaWidth * 2, chromaWidth * 2];
  await frame.copyTo(buffer, {
    rect,
    layout: [
      { offset: offsets[0], stride: strides[0] },
      { offset: offsets[1], stride: strides[1] },
      { offset: offsets[2], stride: strides[2] },
    ],
  });
  return {
    timestamp: frame.timestamp / 1e6,
    duration: (frame.duration ?? 0) / 1e6,
    bitDepth,
    width: rect.width,
    height: rect.height,
    chromaWidth,
    chromaHeight,
    buffer,
    offsets,
    strides,
  };
}

async function runVideo(run: number, position: number): Promise<void> {
  const track = videoTrack!;
  const config = videoConfig!;
  const sink = new EncodedPacketSink(track);
  const startPacket =
    (await sink.getKeyPacket(position, { verifyKeyPackets: true })) ??
    (await sink.getFirstKeyPacket({ verifyKeyPackets: true }));
  if (run !== generation) return;
  if (!startPacket) {
    post({ type: "video-ended", generation: run });
    return;
  }

  // Frames are handed over strictly in output order; copying planes is async.
  let delivery = Promise.resolve();
  const usePlanes = source!.video.transfer !== "sdr";
  const decoder = new VideoDecoder({
    output: (frame) => {
      if (run !== generation) {
        frame.close();
        return;
      }
      const end = (frame.timestamp + (frame.duration ?? 0)) / 1e6;
      if (end <= position - 0.001) {
        // Decoded only as a reference for the frames after the seek target.
        frame.close();
        return;
      }
      framesInFlight += 1;
      delivery = delivery.then(async () => {
        if (run !== generation) {
          frame.close();
          return;
        }
        if (usePlanes && frame.format && RAW_PLANE_FORMATS.test(frame.format)) {
          try {
            const planes = await copyPlanes(frame);
            frame.close();
            if (run !== generation) {
              planePool.push(planes.buffer);
              return;
            }
            post({ type: "planes", generation: run, planes }, [planes.buffer]);
            return;
          } catch {
            // Some GPU-backed frames report a format they cannot copy; fall
            // through and hand the frame over for import instead.
          }
        }
        post({ type: "frame", generation: run, frame }, [frame]);
      });
    },
    error: (error) => fail(run, error, ENGINE_FAILURES.videoDecode),
  });
  activeVideoDecoder = decoder;
  decoder.addEventListener("dequeue", () => waker.wake());
  decoder.configure(config);

  try {
    for await (const packet of sink.packets(startPacket)) {
      while (
        run === generation &&
        (framesInFlight >= MAX_FRAMES_IN_FLIGHT ||
          decoder.decodeQueueSize >= MAX_VIDEO_DECODE_QUEUE)
      ) {
        await waker.wait();
      }
      if (run !== generation || decoder.state === "closed") return;
      const data = nalLengthSize
        ? stripDolbyVisionNalUnits(packet.data, nalLengthSize)
        : packet.data;
      decoder.decode(
        new EncodedVideoChunk({
          type: packet.type,
          timestamp: Math.round(packet.timestamp * 1e6),
          duration: Math.round(packet.duration * 1e6),
          data,
        }),
      );
    }
  } catch (error) {
    if (run !== generation) return;
    throw new EngineFailure(ENGINE_FAILURES.read, `Reading video failed: ${String(error)}`);
  }
  if (run !== generation || decoder.state === "closed") return;
  await decoder.flush();
  await delivery;
  if (run === generation) post({ type: "video-ended", generation: run });
}

function webCodecsAudioDecoder(config: AudioDecoderConfig, run: number): AudioDecoderAdapter {
  let ready: DecodedAudio[] = [];
  const decoder = new AudioDecoder({
    output: (data) => {
      const planes: Float32Array[] = [];
      for (let channel = 0; channel < data.numberOfChannels; channel++) {
        const plane = new Float32Array(data.numberOfFrames);
        data.copyTo(plane, { planeIndex: channel, format: "f32-planar" });
        planes.push(plane);
      }
      ready.push({
        planes,
        sampleRate: data.sampleRate,
        speakers: defaultSpeakers(data.numberOfChannels),
        timestamp: data.timestamp / 1e6,
      });
      data.close();
    },
    error: (error) => fail(run, error, ENGINE_FAILURES.audioDecode),
  });
  decoder.configure(config);
  const take = () => {
    const out = ready;
    ready = [];
    return out;
  };
  return {
    async decode(packet) {
      decoder.decode(
        new EncodedAudioChunk({
          type: "key",
          timestamp: Math.round(packet.timestamp * 1e6),
          duration: Math.round(packet.duration * 1e6),
          data: packet.data,
        }),
      );
      // Let the decoder run ahead a few packets rather than flushing each one.
      while (decoder.decodeQueueSize > 8) {
        await new Promise((resolve) =>
          decoder.addEventListener("dequeue", resolve, { once: true }),
        );
      }
      return take();
    },
    async flush() {
      if (decoder.state === "configured") await decoder.flush();
      return take();
    },
    close() {
      if (decoder.state !== "closed") decoder.close();
    },
  };
}

async function ffmpegAudioDecoder(
  codec: FFmpegAudioCodec,
  track: InputAudioTrack,
): Promise<AudioDecoderAdapter> {
  const module = await loadFFmpegAudioDecoders({ locateFile: () => wasmUrl });
  const config = await track.getDecoderConfig().catch(() => null);
  const extradata = config?.description ? toBytes(config.description) : null;
  const decoder = new FFmpegAudioDecoder(
    module,
    codec,
    audioFacts!.sampleRate,
    audioFacts!.channels,
    extradata,
  );
  const convert =
    (packetTimestamp: number) =>
    (frame: DecodedAudioFrame): DecodedAudio => ({
      planes: frame.channels,
      sampleRate: frame.sampleRate,
      speakers: speakersFromMask(frame.channelMask, frame.channels.length),
      timestamp: frame.timestampUs === null ? packetTimestamp : frame.timestampUs / 1e6,
    });
  return {
    async decode(packet) {
      return decoder.decode(packet.data, packet.timestamp * 1e6).map(convert(packet.timestamp));
    },
    async flush() {
      return decoder.drain().map(convert(0));
    },
    close() {
      decoder.close();
    },
  };
}

function pcmAudioDecoder(format: PCMFormat): AudioDecoderAdapter {
  const { channels, sampleRate } = audioFacts!;
  return {
    async decode(packet) {
      return [
        {
          planes: unpackPCM(packet.data, format, channels),
          sampleRate,
          speakers: defaultSpeakers(channels),
          timestamp: packet.timestamp,
        },
      ];
    },
    async flush() {
      return [];
    },
    close() {},
  };
}

async function createAudioDecoder(
  run: number,
  codec: string,
  track: InputAudioTrack,
): Promise<AudioDecoderAdapter> {
  if (isFFmpegAudioCodec(codec)) return ffmpegAudioDecoder(codec, track);
  const pcm = SERVER_PCM_FORMATS[codec];
  if (pcm) return pcmAudioDecoder(pcm);
  const config = await track.getDecoderConfig();
  if (!config)
    throw new EngineFailure(ENGINE_FAILURES.audioDecode, `No decoder configuration for ${codec}.`);
  return webCodecsAudioDecoder(config, run);
}

async function runAudio(run: number, position: number): Promise<void> {
  const track = audioTrack!;
  const output = audioOutput!;
  const codec = source!.audio!.codec;
  const sink = new EncodedPacketSink(track);
  const preroll = isFFmpegAudioCodec(codec) ? FFMPEG_PREROLL_SECONDS : 0;
  const startPacket =
    (await sink.getPacket(Math.max(0, position - preroll))) ?? (await sink.getFirstPacket());
  if (run !== generation) return;
  const send = (message: WorkerToWorkletMessage, transfer: Transferable[] = []) =>
    output.port.postMessage(message, transfer);
  if (!startPacket) {
    send({ type: "end", generation: run });
    post({ type: "audio-ended", generation: run });
    return;
  }

  const decoder = await createAudioDecoder(run, codec, track);
  if (run !== generation) {
    decoder.close();
    return;
  }
  activeAudioDecoder = decoder;
  let mix: { key: string; matrix: Float32Array } | null = null;
  let resampler: LinearResampler | null = null;

  const emit = (decoded: DecodedAudio) => {
    if (run !== generation) return;
    let planes = decoded.planes;
    let timestamp = decoded.timestamp;
    const frames = planes[0]?.length ?? 0;
    if (frames === 0) return;
    const end = timestamp + frames / decoded.sampleRate;
    if (end <= position) return;
    if (timestamp < position) {
      const skip = Math.min(frames, Math.round((position - timestamp) * decoded.sampleRate));
      planes = planes.map((plane) => plane.subarray(skip));
      timestamp = position;
    }
    if (decoded.sampleRate !== output.sampleRate) {
      resampler ??= new LinearResampler(decoded.sampleRate, output.sampleRate, planes.length);
      planes = resampler.process(planes);
    }
    const key = `${decoded.speakers.join(",")}>${output.channels}`;
    if (mix?.key !== key) mix = { key, matrix: mixMatrix(decoded.speakers, output.channels) };
    const samples = mixToInterleaved(planes, mix.matrix, output.channels);
    if (samples.length === 0) return;
    const wasBuffered = audioPostedSeconds >= AUDIO_BUFFERED_SECONDS;
    audioPostedSeconds += samples.length / output.channels / output.sampleRate;
    send({ type: "pcm", generation: run, timestamp, samples }, [samples.buffer]);
    if (!wasBuffered && audioPostedSeconds >= AUDIO_BUFFERED_SECONDS) {
      post({ type: "audio-buffered", generation: run });
    }
  };

  try {
    for await (const packet of sink.packets(startPacket)) {
      while (
        run === generation &&
        audioPostedSeconds - audioConsumedSeconds > AUDIO_AHEAD_SECONDS
      ) {
        await waker.wait();
      }
      if (run !== generation) return;
      for (const decoded of await decoder.decode(packet)) emit(decoded);
    }
  } catch (error) {
    if (run !== generation) return;
    throw error instanceof EngineFailure
      ? error
      : new EngineFailure(ENGINE_FAILURES.read, `Reading audio failed: ${String(error)}`);
  }
  if (run !== generation) return;
  for (const decoded of await decoder.flush()) emit(decoded);
  if (run !== generation) return;
  send({ type: "end", generation: run });
  post({ type: "audio-ended", generation: run });
}

scope.onmessage = (event: MessageEvent<PageToWorkerMessage>) => {
  const message = event.data;
  switch (message.type) {
    case "open":
      void open(message.source).catch((error: unknown) => fail(null, error, ENGINE_FAILURES.open));
      return;
    case "audio-output":
      audioOutput = {
        port: message.port,
        channels: message.channels,
        sampleRate: message.sampleRate,
      };
      message.port.onmessage = (portEvent: MessageEvent<WorkletToWorkerMessage>) => {
        const report = portEvent.data;
        if (report.type === "consumed" && report.generation === generation) {
          audioConsumedSeconds = report.consumedSeconds;
          waker.wake();
        }
      };
      return;
    case "start":
      start(message.generation, message.position);
      return;
    case "frame-released":
      if (message.generation === generation && framesInFlight > 0) {
        framesInFlight -= 1;
        waker.wake();
      }
      return;
    case "planes-released":
      if (planePool.length < MAX_FRAMES_IN_FLIGHT) planePool.push(message.buffer);
      return;
    case "close":
      generation += 1;
      stopRun();
      waker.wake();
      input?.dispose();
      input = null;
      scope.close();
      return;
  }
};
