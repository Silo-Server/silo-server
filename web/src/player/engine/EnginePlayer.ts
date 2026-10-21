import type { FailureV3 } from "../protocol-v3";
import type { VideoFitMode } from "../types";
import { EngineAudioOutput } from "./audioOutput";
import { EngineClock } from "./clock";
import {
  ENGINE_READ_FAILED,
  ENGINE_VIDEO_DECODE_FAILED,
  type EngineMediaInfo,
  type PageToWorkerMessage,
  type WorkerToPageMessage,
  type WorkletToPageMessage,
} from "./protocol";
import {
  Canvas2DRenderer,
  HDRPresenter,
  type EnginePicture,
  type FrameRenderer,
  type PictureSize,
  type Viewport,
} from "./renderers";
import type { EngineSourceSpec } from "./source";
import { EngineTextTrack } from "./textTracks";

const TIMEUPDATE_INTERVAL_MS = 250;
const PROGRESS_INTERVAL_MS = 1_000;
/** Frames this close to the clock are due now. */
const PRESENT_AHEAD_SECONDS = 0.005;
/** How long a start or a seek may take to become playable before it fails. */
const READINESS_TIMEOUT_MS = 20_000;
/**
 * How long playback may wait for data before the connection counts as lost:
 * a read that never answers (a node gone, a half-open connection) is reported
 * as a read failure, which reconnects instead of waiting forever.
 */
const STALL_TIMEOUT_MS = 20_000;
/** While the tab is hidden, due frames are still released at this cadence. */
const HIDDEN_TICK_MS = 100;
// Paused with its picture up, the engine has nothing to present, so the loop
// checks at this pace instead of every frame; play(), seek() and any message
// from the worker or the audio worklet wake it.
const IDLE_TICK_MS = 250;
/** Video further behind the clock than this on return to the tab is re-synced. */
const RESYNC_LAG_SECONDS = 2;
/** Without audio, a frame this overdue means the decoder is starved. */
const VIDEO_STALL_SECONDS = 0.25;
/** Video decoder errors one session restarts decoding after before failing over. */
export const MAX_DECODER_RESTARTS = 2;
/**
 * Wait before a decoder restart, times the restarts made so far: a hardware
 * decoder dies with the GPU process, which takes a moment to come back.
 */
export const DECODER_RESTART_DELAY_MS = 750;

/** Typed failures the server records against the plan (see routing.ts). */
export const ENGINE_PAGE_FAILURES = {
  worker: "browser_engine_worker_failed",
  startup: "browser_engine_startup_timeout",
  presentation: "browser_engine_presentation_failed",
  audioOutput: "browser_engine_audio_output_failed",
} as const;

type QueuedPicture = EnginePicture & { generation: number };

function hasUserActivation(): boolean {
  const activation = (navigator as Navigator & { userActivation?: { hasBeenActive: boolean } })
    .userActivation;
  // Browsers without the API decide in AudioContext.resume() instead.
  return activation ? activation.hasBeenActive : true;
}

function autoplayRefused(): DOMException {
  return new DOMException(
    "play() failed because the user didn't interact with the document first.",
    "NotAllowedError",
  );
}

class EngineTimeRanges implements TimeRanges {
  constructor(private readonly ranges: Array<[number, number]>) {}
  get length(): number {
    return this.ranges.length;
  }
  start(index: number): number {
    const range = this.ranges[index];
    if (!range) throw new DOMException("Index out of range", "IndexSizeError");
    return range[0];
  }
  end(index: number): number {
    const range = this.ranges[index];
    if (!range) throw new DOMException("Index out of range", "IndexSizeError");
    return range[1];
  }
}

export interface EnginePlayerOptions {
  source: EngineSourceSpec;
  /** Box the engine draws into; each session owns everything inside it. */
  surface: HTMLElement;
  /** Receives media events and supplies the fit mode, like the element it stands in for. */
  element: HTMLVideoElement;
  volume: number;
  muted: boolean;
  /** The speed playback starts at, as a media element's defaultPlaybackRate. */
  playbackRate?: number;
  onFailure: (failure: FailureV3) => void;
}

function clampRate(value: number): number {
  return Math.min(4, Math.max(0.25, value || 1));
}

/**
 * Plays one original file with the browser decode engine and exposes the
 * HTMLMediaElement behaviour the player relies on: the same properties, the
 * same events in the same order, `requestVideoFrameCallback`, and text tracks.
 *
 * Audio is the master clock when the file has audio. Video frames are pulled
 * against it on every animation frame; a frame that is already late is
 * released without being drawn.
 */
export class EnginePlayer {
  private readonly source: EngineSourceSpec;
  /** This session's box inside the surface, holding its canvases. */
  private readonly layers: HTMLElement;
  private readonly element: HTMLVideoElement;
  private readonly onFailure: (failure: FailureV3) => void;
  private worker: Worker | null = null;
  private audio: EngineAudioOutput | null = null;
  private renderer: FrameRenderer | null = null;
  private clock: EngineClock | null = null;
  private info: EngineMediaInfo | null = null;

  private generation = 0;
  private queue: QueuedPicture[] = [];
  private shown: QueuedPicture | null = null;
  private presentedFrames = 0;
  private decoderRestarts = 0;
  private restartTimer: ReturnType<typeof setTimeout> | null = null;
  /** Decoded frames skipped because a later one was already due. */
  private droppedFrames = 0;
  private seekTarget: number;
  private videoEnded = false;
  private audioEnded = false;
  private audioDrained = false;
  private audioBuffered = false;

  private pausedValue = true;
  private endedValue = false;
  private seekingValue = false;
  private waitingValue = false;
  private ready = 0;
  private loadedData = false;
  private volumeValue: number;
  private mutedValue: boolean;
  private rateValue = 1;
  private failed = false;
  private disposed = false;
  /**
   * Playing muted on wall time because the browser keeps audio suspended
   * until a user gesture, as a media element may autoplay muted.
   */
  private silent = false;

  private frameRequest = 0;
  private hiddenTimer: ReturnType<typeof setInterval> | null = null;
  private idleTimer: ReturnType<typeof setTimeout> | null = null;
  private readinessTimer: ReturnType<typeof setTimeout> | null = null;
  /** When the current wait for data began, while playing; null otherwise. */
  private waitingSince: number | null = null;
  private lastTimeupdate = 0;
  private lastProgress = 0;
  private callbacks = new Map<number, VideoFrameRequestCallback>();
  private nextCallbackId = 1;
  private readonly tracks: EngineTextTrack[] = [];
  private fit: VideoFitMode = "contain";
  /** The surface's size in device pixels, where the browser reports it exactly. */
  private devicePixels: { width: number; height: number } | null = null;
  /** Fallback trigger for a device pixel ratio change, re-armed after each one. */
  private resolutionQuery: MediaQueryList | null = null;
  private readonly resizeObserver: ResizeObserver;
  private readonly classObserver: MutationObserver;
  private wakeLock: WakeLockSentinel | "pending" | null = null;

  constructor(options: EnginePlayerOptions) {
    this.source = options.source;
    // A fresh box replaces whatever an earlier session left in the surface,
    // including canvases of a presenter that is still being created.
    this.layers = document.createElement("div");
    this.layers.style.width = "100%";
    this.layers.style.height = "100%";
    options.surface.replaceChildren(this.layers);
    this.element = options.element;
    this.onFailure = options.onFailure;
    this.volumeValue = options.volume;
    this.mutedValue = options.muted;
    this.rateValue = clampRate(options.playbackRate ?? 1);
    this.seekTarget = options.source.startSeconds;
    this.resizeObserver = new ResizeObserver((entries) => {
      const box = entries[entries.length - 1]?.devicePixelContentBoxSize?.[0];
      this.devicePixels = box ? { width: box.inlineSize, height: box.blockSize } : null;
      // Without the exact size, a ratio change alone would not reach us.
      if (!box && !this.resolutionQuery) this.watchResolution();
      this.relayout();
    });
    this.observeSurface();
    this.classObserver = new MutationObserver(() => this.relayout());
    this.classObserver.observe(this.element, {
      attributes: true,
      attributeFilter: ["class", "style"],
    });
    this.readFit();
    document.addEventListener("visibilitychange", this.onVisibilityChange);
    if ("mediaSession" in navigator) {
      navigator.mediaSession.setActionHandler("play", () => void this.play().catch(() => {}));
      navigator.mediaSession.setActionHandler("pause", () => this.pause());
    }
  }

  // -- HTMLMediaElement surface ------------------------------------------------

  get currentTime(): number {
    if (this.seekingValue || !this.clock) return this.seekTarget;
    const duration = this.duration;
    const time = this.clock.now();
    return Math.max(0, Number.isFinite(duration) ? Math.min(time, duration) : time);
  }

  set currentTime(value: number) {
    this.seek(value);
  }

  get duration(): number {
    return this.info ? this.info.durationSeconds : Number.NaN;
  }

  get paused(): boolean {
    return this.pausedValue;
  }

  get ended(): boolean {
    return this.endedValue;
  }

  get seeking(): boolean {
    return this.seekingValue;
  }

  get readyState(): number {
    return this.ready;
  }

  get videoWidth(): number {
    return this.info?.video.displayWidth ?? 0;
  }

  get videoHeight(): number {
    return this.info?.video.displayHeight ?? 0;
  }

  get buffered(): TimeRanges {
    if (!this.info) return new EngineTimeRanges([]);
    const last = this.queue[this.queue.length - 1];
    const end = Math.max(this.currentTime, last ? last.timestamp + last.duration : 0);
    return new EngineTimeRanges([[Math.min(this.seekTarget, this.currentTime), end]]);
  }

  get seekable(): TimeRanges {
    return new EngineTimeRanges(this.info ? [[0, this.info.durationSeconds]] : []);
  }

  get volume(): number {
    return this.volumeValue;
  }

  set volume(value: number) {
    if (!(value >= 0 && value <= 1)) {
      throw new DOMException("The volume provided is outside the range [0, 1].", "IndexSizeError");
    }
    if (value === this.volumeValue) return;
    this.volumeValue = value;
    this.audio?.setVolume(this.volumeValue, this.mutedValue);
    this.dispatch("volumechange");
  }

  get muted(): boolean {
    return this.mutedValue;
  }

  set muted(value: boolean) {
    if (value === this.mutedValue) return;
    this.mutedValue = value;
    this.audio?.setVolume(this.volumeValue, this.mutedValue);
    this.dispatch("volumechange");
    if (!value && this.silent) this.endSilence();
  }

  get playbackRate(): number {
    return this.rateValue;
  }

  set playbackRate(value: number) {
    const rate = clampRate(value);
    if (rate === this.rateValue) return;
    this.rateValue = rate;
    this.audio?.setRate(rate);
    this.clock?.setRate(rate);
    this.dispatch("ratechange");
  }

  async play(): Promise<void> {
    if (this.failed || this.disposed) {
      throw new DOMException("The media could not be played.", "NotSupportedError");
    }
    // Like a media element, refuse before changing state when the page has
    // never had a user gesture: no play/pause events for a blocked attempt.
    // Muted playback is allowed and runs without audio until it can start.
    if (this.audio && this.audio.context.state !== "running" && !hasUserActivation()) {
      if (!this.mutedValue) throw autoplayRefused();
      this.silent = true;
    }
    if (this.endedValue) this.seek(0);
    if (!this.pausedValue) return;
    this.pausedValue = false;
    this.wake();
    this.dispatch("play");
    if (this.audio && !this.silent && !(await this.audio.resume())) {
      if (!this.mutedValue) {
        if (!this.pausedValue) this.settlePaused();
        throw autoplayRefused();
      }
      this.silent = true;
    }
    if (this.pausedValue || this.disposed) return;
    if (!this.seekingValue && this.ready >= 4) this.startFlow();
  }

  pause(): void {
    if (this.pausedValue) return;
    this.settlePaused();
    this.dispatch("timeupdate");
  }

  private settlePaused(): void {
    this.pausedValue = true;
    this.audio?.setPlaying(false);
    this.clock?.setRunning(false);
    this.waitingValue = false;
    this.updatePresence();
    this.dispatch("pause");
  }

  seek(position: number): void {
    if (this.disposed || !Number.isFinite(position)) return;
    const duration = this.duration;
    const target = Math.max(0, Number.isFinite(duration) ? Math.min(position, duration) : position);
    if (!this.info) {
      this.seekTarget = target;
      this.source.startSeconds = target;
      return;
    }
    this.seekingValue = true;
    this.clearDecoderRestart();
    this.wake();
    this.endedValue = false;
    this.ready = Math.min(this.ready, 1);
    // A read that never answers (a node gone, a half-open connection) would
    // otherwise leave the seek pending for good. Reported as a read failure,
    // it reconnects once the stream has played.
    this.armReadinessDeadline(() =>
      this.fail(ENGINE_READ_FAILED, "The browser engine could not resume after a seek in time."),
    );
    this.dispatch("seeking");
    this.beginGeneration(target);
    this.dispatch("timeupdate");
  }

  getVideoPlaybackQuality(): VideoPlaybackQuality {
    return {
      creationTime: performance.now(),
      totalVideoFrames: this.presentedFrames + this.droppedFrames,
      droppedVideoFrames: this.droppedFrames,
      corruptedVideoFrames: 0,
    } as VideoPlaybackQuality;
  }

  requestVideoFrameCallback(callback: VideoFrameRequestCallback): number {
    const id = this.nextCallbackId++;
    this.callbacks.set(id, callback);
    return id;
  }

  cancelVideoFrameCallback(id: number): void {
    this.callbacks.delete(id);
  }

  addTextTrack(kind: TextTrackKind, label = "", language = ""): TextTrack {
    const track = new EngineTextTrack(kind, label, language);
    this.tracks.push(track);
    return track as unknown as TextTrack;
  }

  // -- Lifecycle ---------------------------------------------------------------

  load(): void {
    this.dispatch("loadstart");
    const worker = new Worker(new URL("./engine.worker.ts", import.meta.url), { type: "module" });
    this.worker = worker;
    worker.onmessage = (event: MessageEvent<WorkerToPageMessage>) => this.onWorker(event.data);
    worker.onerror = (event) =>
      this.fail(ENGINE_PAGE_FAILURES.worker, event.message || "The decode worker failed.");
    // Runs until the position is playable, not just until the first picture:
    // audio that never arrives would otherwise hold playback with no failure.
    this.armReadinessDeadline(() =>
      this.fail(ENGINE_PAGE_FAILURES.startup, this.startupTimeoutMessage()),
    );
    this.post({ type: "open", source: this.source });
  }

  destroy(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.stopPresentationTimers();
    this.clearReadinessDeadline();
    this.clearDecoderRestart();
    document.removeEventListener("visibilitychange", this.onVisibilityChange);
    this.resizeObserver.disconnect();
    this.resolutionQuery?.removeEventListener("change", this.onResolutionChange);
    this.resolutionQuery = null;
    this.classObserver.disconnect();
    for (const picture of this.queue) this.release(picture);
    this.queue = [];
    if (this.shown) this.release(this.shown);
    this.shown = null;
    this.stopWorker();
    this.audio?.close();
    this.audio = null;
    this.renderer?.destroy();
    this.renderer = null;
    this.layers.remove();
    this.callbacks.clear();
    this.updatePresence();
    if ("mediaSession" in navigator) {
      navigator.mediaSession.setActionHandler("play", null);
      navigator.mediaSession.setActionHandler("pause", null);
    }
  }

  // -- Internals ---------------------------------------------------------------

  /** Media time at which the picture on screen stops being current. */
  private shownUntil(): number {
    return this.shown ? this.shown.timestamp + Math.max(0, this.shown.duration) : 0;
  }

  private stopWorker(): void {
    const worker = this.worker;
    this.worker = null;
    if (!worker) return;
    worker.postMessage({ type: "close" } satisfies PageToWorkerMessage);
    setTimeout(() => worker.terminate(), 1_000);
  }

  private post(message: PageToWorkerMessage, transfer: Transferable[] = []): void {
    this.worker?.postMessage(message, transfer);
  }

  private dispatch(type: string): void {
    if (!this.disposed) this.element.dispatchEvent(new Event(type));
  }

  /** Destroyed, or failed: either way nothing new may start. */
  private get stopped(): boolean {
    return this.disposed || this.failed;
  }

  private fail(classification: string, message: string): void {
    if (this.stopped) return;
    this.failed = true;
    const wasPlaying = !this.pausedValue;
    this.audio?.setPlaying(false);
    this.clock?.setRunning(false);
    // This engine is done even if no other route takes over: stop reading and
    // decoding, free queued pictures and audio, and let the screen sleep. The
    // last picture stays up until the player replaces or destroys the engine.
    this.stopPresentationTimers();
    this.clearReadinessDeadline();
    this.clearDecoderRestart();
    this.renderer?.stop();
    for (const picture of this.queue) this.release(picture);
    this.queue = [];
    this.stopWorker();
    this.audio?.close();
    this.audio = null;
    this.updatePresence();
    // Report while `paused` still says whether it was playing: the player
    // reads it to decide whether a reconnect resumes. Then, since the player
    // follows media events, a playing engine says it stopped.
    this.onFailure({ classification, message, decoder_name: "silo-web-engine" });
    if (wasPlaying && !this.disposed && !this.pausedValue) this.settlePaused();
  }

  private onWorker(message: WorkerToPageMessage): void {
    if (this.stopped) {
      if (message.type === "frame") message.frame.close();
      return;
    }
    // Pictures, buffered audio and the end of the stream can change what the
    // loop presents or reports, so an idle loop runs again.
    this.wake();
    switch (message.type) {
      case "ready":
        void this.onReady(message.info);
        return;
      case "frame":
      case "planes": {
        const picture: QueuedPicture =
          message.type === "frame"
            ? {
                kind: "frame",
                frame: message.frame,
                timestamp: message.frame.timestamp / 1e6,
                duration: (message.frame.duration ?? 0) / 1e6,
                generation: message.generation,
              }
            : {
                kind: "planes",
                planes: message.planes,
                timestamp: message.planes.timestamp,
                duration: message.planes.duration,
                generation: message.generation,
              };
        if (message.generation !== this.generation) {
          this.release(picture);
          return;
        }
        this.queue.push(picture);
        // A hidden tab presents on a slow timer, and each picture held for it
        // keeps the decoder waiting for a credit. Releasing due pictures as
        // they arrive keeps video up with fast playback.
        if (this.hiddenTimer !== null && !this.idle()) this.advance(false);
        return;
      }
      case "video-ended":
        if (message.generation === this.generation) this.videoEnded = true;
        return;
      case "audio-ended":
        if (message.generation === this.generation) this.audioEnded = true;
        return;
      case "audio-buffered":
        if (message.generation === this.generation) this.audioBuffered = true;
        return;
      case "error":
        if (message.generation === null || message.generation === this.generation) {
          if (message.classification === ENGINE_VIDEO_DECODE_FAILED && this.restartDecoding()) {
            return;
          }
          this.fail(message.classification, message.message);
        }
        return;
    }
  }

  private async onReady(info: EngineMediaInfo): Promise<void> {
    this.info = info;
    this.dispatch("durationchange");
    this.ready = 1;
    this.dispatch("loadedmetadata");
    this.dispatch("resize");

    const transfer = this.source.video.transfer;
    try {
      const renderer =
        transfer === "sdr"
          ? Canvas2DRenderer.in(this.layers)
          : await HDRPresenter.create(this.layers, transfer, info.video.color, {
              onFailure: (reason) => this.fail(ENGINE_PAGE_FAILURES.presentation, reason),
              onRestored: () => this.relayout(),
            });
      if (this.stopped) {
        renderer.destroy();
        return;
      }
      this.renderer = renderer;
    } catch (error) {
      this.fail(
        ENGINE_PAGE_FAILURES.presentation,
        `The picture could not be presented: ${String(error)}`,
      );
      return;
    }
    if (this.stopped) return;

    if (info.audio) {
      try {
        this.audio = await EngineAudioOutput.create(
          info.audio.sampleRate,
          info.audio.channels,
          (m) => this.onWorklet(m),
        );
      } catch (error) {
        this.fail(
          ENGINE_PAGE_FAILURES.audioOutput,
          `Audio output could not start: ${String(error)}`,
        );
        return;
      }
      if (this.stopped) {
        this.audio.close();
        this.audio = null;
        return;
      }
      this.audio.setVolume(this.volumeValue, this.mutedValue);
      this.audio.setRate(this.rateValue);
      const port = this.audio.connect();
      this.post(
        {
          type: "audio-output",
          port,
          channels: this.audio.channels,
          sampleRate: this.audio.context.sampleRate,
        },
        [port],
      );
    }
    const audio = this.audio;
    this.clock = new EngineClock(audio ? () => audio.audibleContextTime() : null, this.seekTarget);
    this.clock.setRate(this.rateValue);
    this.beginGeneration(this.seekTarget);
    this.frameRequest = requestAnimationFrame(this.tick);
    if (document.hidden) this.onVisibilityChange();
  }

  private onWorklet(message: WorkletToPageMessage): void {
    if (message.generation !== this.generation || !this.clock) return;
    this.wake();
    switch (message.type) {
      case "clock":
        this.clock.onAnchor(message);
        if (message.advancing) this.setWaiting(false);
        return;
      case "underrun":
        this.clock.onAnchor({
          contextTime: message.contextTime,
          mediaTime: message.mediaTime ?? this.clock.now(),
          advancing: false,
        });
        if (!this.pausedValue && !this.seekingValue && !this.audioEnded) this.setWaiting(true);
        return;
      case "drained":
        this.audioDrained = true;
        // Whatever video is left (still decoding, or decoded and queued) plays
        // on wall time, which pause and seek can stop.
        this.clock.detachAudio(!this.pausedValue && !this.seekingValue);
        return;
    }
  }

  /** Calls `onExpire` unless the position becomes playable in time. */
  private armReadinessDeadline(onExpire: () => void): void {
    this.clearReadinessDeadline();
    this.readinessTimer = setTimeout(() => {
      this.readinessTimer = null;
      if (this.ready < 4) onExpire();
    }, READINESS_TIMEOUT_MS);
  }

  private clearReadinessDeadline(): void {
    if (this.readinessTimer === null) return;
    clearTimeout(this.readinessTimer);
    this.readinessTimer = null;
  }

  /**
   * Starts a new decode run at the current position after a video decoder
   * error, after a short wait. A hardware decoder dies when the GPU process
   * restarts, and a new run opens a new one; a stream that is itself broken
   * fails again and fails over when the restarts run out. Unlike a seek, the
   * page sees only waiting and playing, as when a media element's decoder
   * recovers.
   */
  private restartDecoding(): boolean {
    // The dead run may report again before the restart; that is the same error.
    if (this.restartTimer !== null) return true;
    if (!this.info || this.decoderRestarts >= MAX_DECODER_RESTARTS) return false;
    this.decoderRestarts += 1;
    this.restartTimer = setTimeout(() => {
      this.restartTimer = null;
      if (this.stopped) return;
      const position = this.currentTime;
      this.ready = Math.min(this.ready, 2);
      // A run that never shows a picture failed to decode: that fails over,
      // where a read failure would reconnect to this same route.
      this.armReadinessDeadline(() =>
        this.fail(ENGINE_VIDEO_DECODE_FAILED, "The video decoder did not recover in time."),
      );
      if (!this.pausedValue && !this.seekingValue) this.setWaiting(true);
      this.beginGeneration(position);
      this.wake();
    }, DECODER_RESTART_DELAY_MS * this.decoderRestarts);
    return true;
  }

  private clearDecoderRestart(): void {
    if (this.restartTimer === null) return;
    clearTimeout(this.restartTimer);
    this.restartTimer = null;
  }

  private beginGeneration(position: number): void {
    this.generation += 1;
    this.seekTarget = position;
    for (const picture of this.queue) this.release(picture);
    this.queue = [];
    this.videoEnded = false;
    this.audioEnded = false;
    this.audioDrained = false;
    this.audioBuffered = false;
    this.clock?.reset(position);
    this.audio?.setPlaying(false);
    this.audio?.setGeneration(this.generation, position);
    this.post({ type: "start", generation: this.generation, position });
  }

  private release(picture: QueuedPicture): void {
    if (picture.kind === "frame") {
      picture.frame.close();
      this.post({ type: "frame-released", generation: picture.generation });
    } else {
      this.post({ type: "frame-released", generation: picture.generation });
      this.post({ type: "planes-released", buffer: picture.planes.buffer }, [
        picture.planes.buffer,
      ]);
    }
  }

  private present(picture: QueuedPicture): void {
    const size = this.pictureSize(picture);
    try {
      this.renderer?.draw(picture, size, this.viewport());
    } catch (error) {
      this.fail(ENGINE_PAGE_FAILURES.presentation, `Drawing a frame failed: ${String(error)}`);
    }
    if (this.shown) this.release(this.shown);
    this.shown = picture;
    this.presentedFrames += 1;
    if (this.callbacks.size === 0) return;
    const callbacks = this.callbacks;
    this.callbacks = new Map();
    const now = performance.now();
    const metadata: VideoFrameCallbackMetadata = {
      presentationTime: now,
      expectedDisplayTime: now + 1000 / 60,
      width: size.width,
      height: size.height,
      mediaTime: picture.timestamp,
      presentedFrames: this.presentedFrames,
      processingDuration: 0,
    };
    for (const callback of callbacks.values()) {
      try {
        callback(now, metadata);
      } catch (error) {
        console.error("[engine] requestVideoFrameCallback handler failed", error);
      }
    }
  }

  private viewport(): Viewport {
    return { fit: this.fit, devicePixels: this.devicePixels };
  }

  /**
   * Follows the surface's size in device pixels, so the canvases match the
   * screen pixel for pixel. Where the browser cannot report that size (Safari),
   * a change of device pixel ratio alone (a window moved to another screen,
   * a zoom) would not resize the box, so a resolution query stands in for it.
   */
  private observeSurface(): void {
    try {
      this.resizeObserver.observe(this.layers, { box: "device-pixel-content-box" });
    } catch {
      this.resizeObserver.observe(this.layers);
      this.watchResolution();
    }
  }

  private watchResolution(): void {
    if (typeof window.matchMedia !== "function") return;
    this.resolutionQuery?.removeEventListener("change", this.onResolutionChange);
    // The query matches the current ratio only, so it fires once per change.
    this.resolutionQuery = window.matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`);
    this.resolutionQuery.addEventListener("change", this.onResolutionChange);
  }

  private readonly onResolutionChange = (): void => {
    if (this.disposed) return;
    this.watchResolution();
    this.relayout();
  };

  private pictureSize(picture: QueuedPicture): PictureSize {
    const video = this.info?.video;
    if (video && video.displayWidth > 0) {
      const { displayWidth: width, displayHeight: height, rotation, flip } = video;
      return { width, height, rotation, flip };
    }
    const [width, height] =
      picture.kind === "frame"
        ? [picture.frame.displayWidth, picture.frame.displayHeight]
        : [picture.planes.width, picture.planes.height];
    return { width, height, rotation: 0, flip: false };
  }

  private readonly tick = (): void => {
    this.idleTimer = null;
    if (this.stopped) return;
    this.advance(true);
    // advance() fails the engine on a stall; a failed engine draws no more frames.
    if (this.stopped) return;
    if (this.idle()) this.idleTimer = setTimeout(this.tick, IDLE_TICK_MS);
    else this.frameRequest = requestAnimationFrame(this.tick);
  };

  /** Paused with the current picture shown: nothing changes until play or seek. */
  private idle(): boolean {
    return (
      this.pausedValue &&
      !this.seekingValue &&
      this.shown !== null &&
      this.shown.generation === this.generation
    );
  }

  private wake(): void {
    if (this.stopped || this.idleTimer === null) return;
    clearTimeout(this.idleTimer);
    this.idleTimer = null;
    this.frameRequest = requestAnimationFrame(this.tick);
  }

  private stopPresentationTimers(): void {
    cancelAnimationFrame(this.frameRequest);
    if (this.idleTimer !== null) clearTimeout(this.idleTimer);
    this.idleTimer = null;
    if (this.hiddenTimer !== null) clearInterval(this.hiddenTimer);
    this.hiddenTimer = null;
  }

  /** One presentation step; `draw` is false while the tab is hidden. */
  private advance(draw: boolean): void {
    if (this.failed) return;
    const clock = this.clock;
    if (!clock) return;
    if (this.waitingValue && !this.pausedValue && !this.seekingValue) {
      this.waitingSince ??= performance.now();
      if (performance.now() - this.waitingSince > STALL_TIMEOUT_MS) {
        this.fail(ENGINE_READ_FAILED, "The browser engine stopped receiving data.");
        return;
      }
    } else {
      this.waitingSince = null;
    }
    const firstOfGeneration = !this.shown || this.shown.generation !== this.generation;
    if (firstOfGeneration) {
      const first = this.queue.shift();
      if (first) this.present(first);
    } else if (!this.pausedValue && !this.seekingValue) {
      const now = clock.now();
      let due: QueuedPicture | null = null;
      while (this.queue.length > 0 && this.queue[0]!.timestamp <= now + PRESENT_AHEAD_SECONDS) {
        if (due) {
          this.release(due);
          this.droppedFrames += 1;
        }
        due = this.queue.shift()!;
      }
      if (due) {
        if (draw) this.present(due);
        else {
          if (this.shown) this.release(this.shown);
          this.shown = due;
        }
      }
      // Without audio, or once it has drained, the clock runs on wall time,
      // so a video read that stops coming shows up only here.
      if ((!this.audio || this.audioDrained) && this.shown) {
        const overdue =
          this.queue.length === 0 &&
          !this.videoEnded &&
          now > this.shown.timestamp + Math.max(this.shown.duration, 0.04) + VIDEO_STALL_SECONDS;
        clock.setRunning(!overdue);
        this.setWaiting(overdue);
      } else if (
        this.shown &&
        this.queue.length === 0 &&
        !this.videoEnded &&
        now - this.shownUntil() > STALL_TIMEOUT_MS / 1000
      ) {
        // Live audio keeps the clock going when video stops arriving, so the
        // stall shows as a picture that has fallen this far behind it.
        this.fail(ENGINE_READ_FAILED, "The browser engine stopped receiving video.");
        return;
      }
    }

    this.settleReadiness();

    // The last picture stays up for its own duration before the media ends.
    if (
      !this.pausedValue &&
      !this.seekingValue &&
      this.videoEnded &&
      this.queue.length === 0 &&
      (!this.audio || this.audioDrained) &&
      clock.now() >= this.shownUntil()
    ) {
      this.finish();
      return;
    }

    const wall = performance.now();
    if (!this.pausedValue && wall - this.lastTimeupdate >= TIMEUPDATE_INTERVAL_MS) {
      this.lastTimeupdate = wall;
      this.dispatch("timeupdate");
    }
    if (wall - this.lastProgress >= PROGRESS_INTERVAL_MS) {
      this.lastProgress = wall;
      this.dispatch("progress");
    }
    if (this.tracks.length > 0) {
      const time = this.currentTime;
      for (const track of this.tracks) track.update(time);
    }
  }

  /** Moves readyState on once a picture is up and enough audio is queued. */
  private settleReadiness(): void {
    if (!this.shown) return;
    // A seek at or past the last frame decodes nothing new: the previous
    // picture stays up and the seek completes on the end of the video.
    const nothingNewToShow = this.videoEnded && this.queue.length === 0;
    if (this.shown.generation !== this.generation && !nothingNewToShow) return;
    if (this.ready < 2) {
      this.ready = 2;
      if (!this.loadedData) {
        this.loadedData = true;
        this.dispatch("loadeddata");
      }
    }
    if (this.ready >= 4) return;
    if (!this.videoAhead()) return;
    const audioReady = !this.audio || this.audioBuffered || this.audioEnded || this.videoEnded;
    if (!audioReady) return;
    this.ready = 4;
    this.clearReadinessDeadline();
    if (this.seekingValue) {
      this.seekingValue = false;
      this.dispatch("timeupdate");
      this.dispatch("seeked");
    }
    this.dispatch("canplay");
    this.dispatch("canplaythrough");
    if (!this.pausedValue) this.startFlow();
  }

  /**
   * A picture after the one on screen, or the end of the video: what a media
   * element needs for HAVE_FUTURE_DATA, so playback never starts the clock
   * over a first frame whose successors are still being read or decoded.
   */
  private videoAhead(): boolean {
    return this.queue.length > 0 || this.videoEnded;
  }

  /** What a start that never became playable was still waiting for. */
  private startupTimeoutMessage(): string {
    if (this.ready < 2) return "The browser engine showed no picture in time.";
    if (!this.videoAhead()) return "The browser engine decoded no picture after the first in time.";
    return "The browser engine had no audio to play in time.";
  }

  private startFlow(): void {
    // play() already resumed the context when it could; a context created
    // after that call (playback requested before the file opened) starts
    // here, and settles paused if the browser refuses it, or plays silently
    // if muted.
    const audio = this.audio;
    if (audio && audio.context.state !== "running" && !this.silent) {
      if (this.mutedValue && !hasUserActivation()) {
        this.silent = true;
      } else {
        void audio.resume().then((running) => {
          if (running || this.pausedValue || this.stopped) return;
          if (this.mutedValue) this.playSilently();
          else this.settlePaused();
        });
      }
    }
    if (this.silent) this.playSilently();
    else this.audio?.setPlaying(true);
    this.clock?.setRunning(true);
    this.waitingValue = false;
    this.updatePresence();
    this.dispatch("playing");
  }

  /**
   * Runs the clock on wall time with the audio held, as if it had already
   * played out. A seek starts the next run on the audio clock again, so
   * startFlow() comes back here while the player stays silent.
   */
  private playSilently(): void {
    this.silent = true;
    this.audio?.setPlaying(false);
    this.audioDrained = true;
    this.clock?.detachAudio(!this.pausedValue && !this.seekingValue);
  }

  /** Unmuted while silent: start audio, then resume from here in step with it. */
  private endSilence(): void {
    const audio = this.audio;
    if (!audio) return;
    void audio.resume().then((running) => {
      if (this.stopped || !this.silent || this.mutedValue) return;
      if (!running) {
        // A media element pauses when unmuted without a gesture, too.
        if (!this.pausedValue) this.settlePaused();
        return;
      }
      this.silent = false;
      this.seek(this.currentTime);
    });
  }

  /**
   * What a playing media element gets from the browser for free: the screen
   * stays awake and hardware media keys reach it.
   */
  private updatePresence(): void {
    const playing = !this.pausedValue && !this.stopped;
    if ("mediaSession" in navigator) {
      navigator.mediaSession.playbackState = playing ? "playing" : "paused";
    }
    const wakeLock = (navigator as Navigator & { wakeLock?: WakeLock }).wakeLock;
    if (playing && !this.wakeLock && wakeLock && document.visibilityState === "visible") {
      this.wakeLock = "pending";
      wakeLock
        .request("screen")
        .then((lock) => {
          if (this.pausedValue || this.stopped) {
            void lock.release();
            this.wakeLock = null;
            return;
          }
          this.wakeLock = lock;
          lock.addEventListener("release", () => {
            if (this.wakeLock === lock) this.wakeLock = null;
          });
        })
        .catch(() => {
          this.wakeLock = null;
        });
    } else if (!playing && this.wakeLock && this.wakeLock !== "pending") {
      void this.wakeLock.release();
      this.wakeLock = null;
    }
  }

  private setWaiting(waiting: boolean): void {
    if (waiting === this.waitingValue) return;
    this.waitingValue = waiting;
    this.dispatch(waiting ? "waiting" : "playing");
  }

  private finish(): void {
    this.endedValue = true;
    this.dispatch("timeupdate");
    this.settlePaused();
    this.dispatch("ended");
  }

  private readFit(): void {
    this.fit = getComputedStyle(this.element).objectFit === "cover" ? "cover" : "contain";
  }

  private relayout(): void {
    this.readFit();
    if (this.shown && this.renderer && !this.stopped) {
      // While paused no later frame would repaint, so a failure here counts.
      try {
        this.renderer.draw(this.shown, this.pictureSize(this.shown), this.viewport());
      } catch (error) {
        this.fail(ENGINE_PAGE_FAILURES.presentation, `Drawing a frame failed: ${String(error)}`);
      }
    }
  }

  private readonly onVisibilityChange = (): void => {
    if (this.stopped) return;
    if (document.hidden) {
      if (this.hiddenTimer === null) {
        this.hiddenTimer = setInterval(() => {
          if (!this.idle()) this.advance(false);
        }, HIDDEN_TICK_MS);
      }
      return;
    }
    if (this.hiddenTimer !== null) {
      clearInterval(this.hiddenTimer);
      this.hiddenTimer = null;
    }
    const lagging =
      !this.pausedValue &&
      !this.seekingValue &&
      this.shown !== null &&
      this.currentTime - this.shown.timestamp > RESYNC_LAG_SECONDS;
    if (lagging) this.seek(this.currentTime);
    else this.relayout();
    // The browser drops a screen wake lock whenever the page is hidden.
    this.updatePresence();
  };
}
