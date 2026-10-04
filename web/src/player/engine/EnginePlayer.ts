import type { FailureV3 } from "../protocol-v3";
import type { VideoFitMode } from "../types";
import { EngineAudioOutput } from "./audioOutput";
import { EngineClock } from "./clock";
import type {
  EngineMediaInfo,
  PageToWorkerMessage,
  WorkerToPageMessage,
  WorkletToPageMessage,
} from "./protocol";
import {
  browserMapsToSDR,
  Canvas2DRenderer,
  WebGPURenderer,
  type EnginePicture,
  type FrameRenderer,
} from "./renderers";
import type { EngineSourceSpec } from "./source";
import { EngineTextTrack } from "./textTracks";

const TIMEUPDATE_INTERVAL_MS = 250;
const PROGRESS_INTERVAL_MS = 1_000;
/** Frames this close to the clock are due now. */
const PRESENT_AHEAD_SECONDS = 0.005;
const STARTUP_TIMEOUT_MS = 20_000;
/** While the tab is hidden, due frames are still released at this cadence. */
const HIDDEN_TICK_MS = 100;
/** Video further behind the clock than this on return to the tab is re-synced. */
const RESYNC_LAG_SECONDS = 2;
/** Without audio, a frame this overdue means the decoder is starved. */
const VIDEO_STALL_SECONDS = 0.25;

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
  /** Box the engine draws into; it owns the canvas inside. */
  surface: HTMLElement;
  /** Receives media events and supplies the fit mode, like the element it stands in for. */
  element: HTMLVideoElement;
  volume: number;
  muted: boolean;
  onFailure: (failure: FailureV3) => void;
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
  private readonly surface: HTMLElement;
  private canvas: HTMLCanvasElement;
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

  private frameRequest = 0;
  private hiddenTimer: ReturnType<typeof setInterval> | null = null;
  private startupTimer: ReturnType<typeof setTimeout> | null = null;
  private lastTimeupdate = 0;
  private lastProgress = 0;
  private callbacks = new Map<number, VideoFrameRequestCallback>();
  private nextCallbackId = 1;
  private readonly tracks: EngineTextTrack[] = [];
  private fit: VideoFitMode = "contain";
  private readonly resizeObserver: ResizeObserver;
  private readonly classObserver: MutationObserver;
  private wakeLock: WakeLockSentinel | "pending" | null = null;

  constructor(options: EnginePlayerOptions) {
    this.source = options.source;
    this.surface = options.surface;
    this.element = options.element;
    this.onFailure = options.onFailure;
    this.volumeValue = options.volume;
    this.mutedValue = options.muted;
    this.seekTarget = options.source.startSeconds;
    this.resizeObserver = new ResizeObserver(() => this.relayout());
    this.canvas = this.replaceCanvas();
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
  }

  get playbackRate(): number {
    return this.rateValue;
  }

  set playbackRate(value: number) {
    const rate = Math.min(4, Math.max(0.25, value || 1));
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
    if (this.audio && this.audio.context.state !== "running" && !hasUserActivation()) {
      throw autoplayRefused();
    }
    if (this.endedValue) this.seek(0);
    if (!this.pausedValue) return;
    this.pausedValue = false;
    this.dispatch("play");
    if (this.audio && !(await this.audio.resume())) {
      if (!this.pausedValue) this.settlePaused();
      throw autoplayRefused();
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
    this.endedValue = false;
    this.ready = Math.min(this.ready, 1);
    this.dispatch("seeking");
    this.beginGeneration(target);
    this.dispatch("timeupdate");
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
    this.startupTimer = setTimeout(() => {
      if (this.ready < 2) {
        this.fail(ENGINE_PAGE_FAILURES.startup, "The browser engine showed no picture in time.");
      }
    }, STARTUP_TIMEOUT_MS);
    this.post({ type: "open", source: this.source });
  }

  destroy(): void {
    if (this.disposed) return;
    this.disposed = true;
    cancelAnimationFrame(this.frameRequest);
    if (this.hiddenTimer !== null) clearInterval(this.hiddenTimer);
    if (this.startupTimer !== null) clearTimeout(this.startupTimer);
    document.removeEventListener("visibilitychange", this.onVisibilityChange);
    this.resizeObserver.disconnect();
    this.classObserver.disconnect();
    for (const picture of this.queue) this.release(picture);
    this.queue = [];
    if (this.shown) this.release(this.shown);
    this.shown = null;
    const worker = this.worker;
    this.worker = null;
    if (worker) {
      worker.postMessage({ type: "close" } satisfies PageToWorkerMessage);
      setTimeout(() => worker.terminate(), 1_000);
    }
    this.audio?.close();
    this.audio = null;
    this.renderer?.destroy();
    this.renderer = null;
    this.canvas.remove();
    this.callbacks.clear();
    this.updatePresence();
    if ("mediaSession" in navigator) {
      navigator.mediaSession.setActionHandler("play", null);
      navigator.mediaSession.setActionHandler("pause", null);
    }
  }

  // -- Internals ---------------------------------------------------------------

  private post(message: PageToWorkerMessage, transfer: Transferable[] = []): void {
    this.worker?.postMessage(message, transfer);
  }

  private dispatch(type: string): void {
    if (!this.disposed) this.element.dispatchEvent(new Event(type));
  }

  private fail(classification: string, message: string): void {
    if (this.failed || this.disposed) return;
    this.failed = true;
    this.audio?.setPlaying(false);
    this.clock?.setRunning(false);
    this.onFailure({ classification, message, decoder_name: "silo-web-engine" });
  }

  private onWorker(message: WorkerToPageMessage): void {
    if (this.disposed) {
      if (message.type === "frame") message.frame.close();
      return;
    }
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
          ? new Canvas2DRenderer(this.canvas)
          : await WebGPURenderer.create(this.canvas, transfer, (reason) =>
              this.fail(ENGINE_PAGE_FAILURES.presentation, reason),
            );
      if (this.disposed) {
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
    if (this.disposed) return;

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
      if (this.disposed) {
        this.audio.close();
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
        // Video longer than its audio keeps going on wall time.
        if (!this.videoEnded) this.clock.detachAudio(!this.pausedValue && !this.seekingValue);
        return;
    }
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
    this.audio?.setGeneration(this.generation);
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
    if (browserMapsToSDR(picture) && !(this.renderer instanceof Canvas2DRenderer)) {
      this.useCanvas2D();
    }
    const size = this.pictureSize(picture);
    try {
      this.renderer?.draw(picture, size, this.fit);
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

  /**
   * A fresh canvas in the surface: a canvas keeps the first context type it
   * hands out, so changing renderer means changing canvas.
   */
  private replaceCanvas(): HTMLCanvasElement {
    const canvas = document.createElement("canvas");
    canvas.style.display = "block";
    canvas.style.width = "100%";
    canvas.style.height = "100%";
    this.surface.replaceChildren(canvas);
    this.resizeObserver.disconnect();
    this.resizeObserver.observe(canvas);
    return canvas;
  }

  /**
   * Hardware-decoded HDR frames keep their PQ/HLG tag, and Chromium's WebGPU
   * import of those desaturates them, while its 2D canvas applies the same tone
   * mapping a <video> element does. Such sessions present through the 2D canvas.
   */
  private useCanvas2D(): void {
    this.renderer?.destroy();
    this.canvas = this.replaceCanvas();
    this.renderer = new Canvas2DRenderer(this.canvas);
  }

  private pictureSize(picture: QueuedPicture): { width: number; height: number } {
    if (this.info && this.info.video.displayWidth > 0) {
      return { width: this.info.video.displayWidth, height: this.info.video.displayHeight };
    }
    return picture.kind === "frame"
      ? { width: picture.frame.displayWidth, height: picture.frame.displayHeight }
      : { width: picture.planes.width, height: picture.planes.height };
  }

  private readonly tick = (): void => {
    if (this.disposed) return;
    this.advance(true);
    this.frameRequest = requestAnimationFrame(this.tick);
  };

  /** One presentation step; `draw` is false while the tab is hidden. */
  private advance(draw: boolean): void {
    if (this.failed) return;
    const clock = this.clock;
    if (!clock) return;
    const firstOfGeneration = !this.shown || this.shown.generation !== this.generation;
    if (firstOfGeneration) {
      const first = this.queue.shift();
      if (first) this.present(first);
    } else if (!this.pausedValue && !this.seekingValue) {
      const now = clock.now();
      let due: QueuedPicture | null = null;
      while (this.queue.length > 0 && this.queue[0]!.timestamp <= now + PRESENT_AHEAD_SECONDS) {
        if (due) this.release(due);
        due = this.queue.shift()!;
      }
      if (due) {
        if (draw) this.present(due);
        else {
          if (this.shown) this.release(this.shown);
          this.shown = due;
        }
      }
      if (!this.audio && this.shown) {
        const overdue =
          this.queue.length === 0 &&
          !this.videoEnded &&
          now > this.shown.timestamp + Math.max(this.shown.duration, 0.04) + VIDEO_STALL_SECONDS;
        clock.setRunning(!overdue);
        this.setWaiting(overdue);
      }
    }

    this.settleReadiness();

    if (
      !this.pausedValue &&
      !this.seekingValue &&
      this.videoEnded &&
      this.queue.length === 0 &&
      (!this.audio || this.audioDrained)
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
      if (this.startupTimer !== null) {
        clearTimeout(this.startupTimer);
        this.startupTimer = null;
      }
      if (!this.loadedData) {
        this.loadedData = true;
        this.dispatch("loadeddata");
      }
    }
    if (this.ready >= 4) return;
    const audioReady = !this.audio || this.audioBuffered || this.audioEnded || this.videoEnded;
    if (!audioReady) return;
    this.ready = 4;
    if (this.seekingValue) {
      this.seekingValue = false;
      this.dispatch("timeupdate");
      this.dispatch("seeked");
    }
    this.dispatch("canplay");
    this.dispatch("canplaythrough");
    if (!this.pausedValue) this.startFlow();
  }

  private startFlow(): void {
    // play() already resumed the context when it could; a context created
    // after that call (playback requested before the file opened) starts
    // here, and settles paused if the browser refuses it.
    const audio = this.audio;
    if (audio && audio.context.state !== "running") {
      void audio.resume().then((running) => {
        if (!running && !this.pausedValue && !this.disposed) this.settlePaused();
      });
    }
    this.audio?.setPlaying(true);
    this.clock?.setRunning(true);
    this.waitingValue = false;
    this.updatePresence();
    this.dispatch("playing");
  }

  /**
   * What a playing media element gets from the browser for free: the screen
   * stays awake and hardware media keys reach it.
   */
  private updatePresence(): void {
    const playing = !this.pausedValue && !this.disposed;
    if ("mediaSession" in navigator) {
      navigator.mediaSession.playbackState = playing ? "playing" : "paused";
    }
    const wakeLock = (navigator as Navigator & { wakeLock?: WakeLock }).wakeLock;
    if (playing && !this.wakeLock && wakeLock && document.visibilityState === "visible") {
      this.wakeLock = "pending";
      wakeLock
        .request("screen")
        .then((lock) => {
          if (this.pausedValue || this.disposed) {
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
    if (this.shown && this.renderer && !this.failed) {
      try {
        this.renderer.draw(this.shown, this.pictureSize(this.shown), this.fit);
      } catch {
        // The next frame repaints anyway.
      }
    }
  }

  private readonly onVisibilityChange = (): void => {
    if (this.disposed) return;
    if (document.hidden) {
      if (this.hiddenTimer === null) {
        this.hiddenTimer = setInterval(() => this.advance(false), HIDDEN_TICK_MS);
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
