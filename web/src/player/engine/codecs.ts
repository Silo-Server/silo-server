/**
 * Codec vocabulary shared by the engine's capability probe and its worker. It
 * is kept apart from the decoders so the probe can name them without pulling
 * the WebAssembly glue into the page bundle.
 */

/** Codecs the bundled FFmpeg build decodes (see web/wasm/audio-decoders). */
export const FFMPEG_AUDIO_CODECS = ["ac3", "eac3", "dts", "truehd", "mlp"] as const;
export type FFmpegAudioCodec = (typeof FFMPEG_AUDIO_CODECS)[number];

export function isFFmpegAudioCodec(codec: string): codec is FFmpegAudioCodec {
  return (FFMPEG_AUDIO_CODECS as readonly string[]).includes(codec);
}

/** Uncompressed PCM the worker converts itself, without a decoder. */
export const ENGINE_PCM_CODECS = [
  "pcm_s16le",
  "pcm_s16be",
  "pcm_s24le",
  "pcm_s24be",
  "pcm_s32le",
  "pcm_s32be",
  "pcm_f32le",
  "pcm_f64le",
] as const;
