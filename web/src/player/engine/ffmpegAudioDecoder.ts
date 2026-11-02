import createSiloAudioDecoders, {
  type SiloAudioDecodersModule,
  type SiloAudioDecodersOptions,
} from "./wasm/silo-audio-decoders.mjs";
import type { FFmpegAudioCodec } from "./codecs";

const CODEC_IDS: Record<FFmpegAudioCodec, number> = {
  ac3: 1,
  eac3: 2,
  dts: 3,
  truehd: 4,
  mlp: 5,
};

const AVERROR_EOF = -541478725;

/** One decoded frame, planar float32, one array per channel. */
export interface DecodedAudioFrame {
  channels: Float32Array[];
  sampleRate: number;
  /** FFmpeg native channel mask (`AV_CH_*` bits), or 0 when unknown. */
  channelMask: number;
  /** Microseconds, or null when the decoder could not attribute one. */
  timestampUs: number | null;
}

let modulePromise: Promise<SiloAudioDecodersModule> | null = null;

/**
 * Loads the WebAssembly module once per realm. In the browser the `.wasm`
 * resolves next to the bundled glue; tests pass the bytes directly.
 */
export function loadFFmpegAudioDecoders(
  options?: SiloAudioDecodersOptions,
): Promise<SiloAudioDecodersModule> {
  modulePromise ??= createSiloAudioDecoders(options).catch((error: unknown) => {
    modulePromise = null;
    throw error;
  });
  return modulePromise;
}

export function resetFFmpegAudioDecodersForTests(): void {
  modulePromise = null;
}

/**
 * A libavcodec audio decoder for the codecs WebCodecs does not offer. Calls are
 * synchronous: the module runs inside the engine's worker, never on the page.
 */
export class FFmpegAudioDecoder {
  private handle: number;

  constructor(
    private readonly module: SiloAudioDecodersModule,
    readonly codec: FFmpegAudioCodec,
    sampleRate: number,
    channels: number,
    extradata?: Uint8Array | null,
  ) {
    let extradataPointer = 0;
    const extradataSize = extradata?.byteLength ?? 0;
    if (extradata && extradataSize > 0) {
      extradataPointer = module._malloc(extradataSize);
      module.HEAPU8.set(extradata, extradataPointer);
    }
    this.handle = module._sad_open(
      CODEC_IDS[codec],
      sampleRate,
      channels,
      extradataPointer,
      extradataSize,
    );
    if (extradataPointer) module._free(extradataPointer);
    if (!this.handle) throw new Error(`FFmpeg could not open a ${codec} decoder`);
  }

  /** Decodes one packet and returns every frame it completed. */
  decode(data: Uint8Array, timestampUs: number): DecodedAudioFrame[] {
    const pointer = this.module._sad_packet_buffer(this.handle, data.byteLength);
    if (!pointer) throw new Error("FFmpeg could not allocate a packet");
    this.module.HEAPU8.set(data, pointer);
    // A rejected (damaged) packet is dropped; the decoder resynchronizes on a
    // later one, so frames it still holds are drained either way.
    this.module._sad_send_packet(this.handle, timestampUs);
    return this.receive();
  }

  /** Drains frames the decoder still holds at end of stream. */
  drain(): DecodedAudioFrame[] {
    this.module._sad_send_eof(this.handle);
    const frames = this.receive();
    this.module._sad_flush(this.handle);
    return frames;
  }

  /** Discards buffered state, for a seek. */
  reset(): void {
    this.module._sad_flush(this.handle);
  }

  close(): void {
    if (!this.handle) return;
    this.module._sad_close(this.handle);
    this.handle = 0;
  }

  private receive(): DecodedAudioFrame[] {
    const frames: DecodedAudioFrame[] = [];
    for (;;) {
      const samples = this.module._sad_receive_frame(this.handle);
      if (samples === 0 || samples === AVERROR_EOF) break;
      if (samples < 0) break;
      const channelCount = this.module._sad_frame_channels(this.handle);
      const base = this.module._sad_frame_pcm(this.handle) / Float32Array.BYTES_PER_ELEMENT;
      const heap = this.module.HEAPF32;
      const channels: Float32Array[] = [];
      for (let channel = 0; channel < channelCount; channel++) {
        const start = base + channel * samples;
        channels.push(heap.slice(start, start + samples));
      }
      const low = this.module._sad_frame_layout_low(this.handle) >>> 0;
      const high = this.module._sad_frame_layout_high(this.handle) >>> 0;
      const pts = this.module._sad_frame_pts_us(this.handle);
      frames.push({
        channels,
        sampleRate: this.module._sad_frame_sample_rate(this.handle),
        channelMask: high * 2 ** 32 + low,
        timestampUs: pts < 0 ? null : pts,
      });
    }
    return frames;
  }
}
