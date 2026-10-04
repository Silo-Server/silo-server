// Types for the Emscripten module built by web/wasm/audio-decoders/build.sh.

export interface SiloAudioDecodersModule {
  HEAPU8: Uint8Array;
  HEAPF32: Float32Array;
  _malloc(size: number): number;
  _free(pointer: number): void;
  _sad_open(
    codec: number,
    sampleRate: number,
    channels: number,
    extradata: number,
    extradataSize: number,
  ): number;
  _sad_packet_buffer(decoder: number, size: number): number;
  _sad_send_packet(decoder: number, ptsMicros: number): number;
  _sad_send_eof(decoder: number): number;
  _sad_receive_frame(decoder: number): number;
  _sad_frame_pcm(decoder: number): number;
  _sad_frame_channels(decoder: number): number;
  _sad_frame_sample_rate(decoder: number): number;
  _sad_frame_layout_low(decoder: number): number;
  _sad_frame_layout_high(decoder: number): number;
  _sad_frame_pts_us(decoder: number): number;
  _sad_flush(decoder: number): void;
  _sad_close(decoder: number): void;
}

export interface SiloAudioDecodersOptions {
  locateFile?: (path: string, prefix: string) => string;
  wasmBinary?: ArrayBuffer | Uint8Array;
}

export default function createSiloAudioDecoders(
  options?: SiloAudioDecodersOptions,
): Promise<SiloAudioDecodersModule>;
