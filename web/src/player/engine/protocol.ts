import type { PictureColor } from "./pictureColor";
import type { EngineSourceSpec } from "./source";

/**
 * Classification of a read that failed after retries. Once the stream has
 * played it means the connection went away, not that the route cannot work.
 */
export const ENGINE_READ_FAILED = "browser_engine_read_failed";

/**
 * The video decoder reported an error. A hardware decoder also fails this way
 * when the GPU process restarts under it, so the page retries a new decode run
 * before treating it as final.
 */
export const ENGINE_VIDEO_DECODE_FAILED = "browser_engine_video_decode_failed";

/**
 * Messages between the page, the engine worker, and the audio worklet.
 *
 * Every decode run carries a generation. A seek starts a new one; anything
 * stamped with an older generation is stale and dropped wherever it lands, so
 * no ordering is needed between the three message channels.
 */

export interface EngineMediaInfo {
  /** Seconds; the container's own figure, used when the plan has none. */
  durationSeconds: number;
  video: {
    codedWidth: number;
    codedHeight: number;
    displayWidth: number;
    displayHeight: number;
    /** Clockwise turn, in degrees, that puts decoded pictures upright. */
    rotation: 0 | 90 | 180 | 270;
    /** Mirror the turned picture horizontally. */
    flip: boolean;
    /** How the HDR presenter reads the stream's samples. */
    color: PictureColor;
  };
  audio: { sampleRate: number; channels: number } | null;
}

/** Planar YUV copied out of a decoded frame for exact high-bit-depth presentation. */
export interface EnginePlaneFrame {
  timestamp: number;
  duration: number;
  /** Bits per sample: 8, 10 or 12. */
  bitDepth: number;
  width: number;
  height: number;
  chromaWidth: number;
  chromaHeight: number;
  /** Y, U and V, each tightly packed rows of 1 (8-bit) or 2 (10/12-bit) bytes per sample. */
  buffer: ArrayBuffer;
  offsets: [number, number, number];
  strides: [number, number, number];
}

export type PageToWorkerMessage =
  | { type: "open"; source: EngineSourceSpec }
  | {
      type: "audio-output";
      port: MessagePort;
      channels: number;
      sampleRate: number;
    }
  | { type: "start"; generation: number; position: number }
  | { type: "frame-released"; generation: number }
  | { type: "planes-released"; buffer: ArrayBuffer }
  | { type: "close" };

export type WorkerToPageMessage =
  | { type: "ready"; info: EngineMediaInfo }
  | { type: "frame"; generation: number; frame: VideoFrame }
  | { type: "planes"; generation: number; planes: EnginePlaneFrame }
  | { type: "video-ended"; generation: number }
  | { type: "audio-ended"; generation: number }
  /**
   * Enough audio for this generation has been sent to the worklet to start.
   * Readiness waits on this rather than on the worklet, which does not run
   * while the AudioContext is suspended for lack of a user gesture.
   */
  | { type: "audio-buffered"; generation: number }
  | {
      type: "error";
      generation: number | null;
      classification: string;
      message: string;
    };

export type WorkerToWorkletMessage =
  /** Interleaved samples at the output rate and channel count. */
  | { type: "pcm"; generation: number; timestamp: number; samples: Float32Array }
  | { type: "end"; generation: number };

export type WorkletToWorkerMessage = {
  type: "consumed";
  generation: number;
  /** Seconds of this generation's audio rendered so far. */
  consumedSeconds: number;
};

export type PageToWorkletMessage =
  | { type: "connect"; port: MessagePort }
  /** A new decode run, starting at `position` on the media timeline. */
  | { type: "generation"; generation: number; position: number }
  | { type: "playing"; playing: boolean }
  | { type: "rate"; rate: number };

export type WorkletToPageMessage =
  | {
      type: "clock";
      generation: number;
      /** Context time at which the first sample of `mediaTime` was rendered. */
      contextTime: number;
      mediaTime: number;
      /** False when the worklet output silence for this quantum. */
      advancing: boolean;
      /** Media seconds the worklet plays per second from this anchor on. */
      rate: number;
    }
  | {
      type: "underrun";
      generation: number;
      /** Context time at which output ran dry, and the media time reached. */
      contextTime: number;
      mediaTime: number | null;
    }
  | { type: "drained"; generation: number };
