import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EnginePlayer } from "./EnginePlayer";
import { WebGPURenderer } from "./renderers";

const audioOutput = vi.hoisted(() => ({
  resumes: true,
  state: "suspended" as AudioContextState,
  contextTime: 0,
}));

vi.mock("./audioOutput", () => ({
  EngineAudioOutput: {
    create: vi.fn(async () => ({
      context: {
        get state() {
          return audioOutput.state;
        },
        sampleRate: 48_000,
      },
      channels: 2,
      connect: () => new MessageChannel().port2,
      setGeneration: vi.fn(),
      setPlaying: vi.fn(),
      setRate: vi.fn(),
      setVolume: vi.fn(),
      resume: vi.fn(async () => {
        if (audioOutput.resumes) audioOutput.state = "running";
        return audioOutput.resumes;
      }),
      audibleContextTime: () => audioOutput.contextTime,
      close: vi.fn(),
    })),
  },
}));
import type { PageToWorkerMessage, WorkerToPageMessage } from "./protocol";
import type { EngineSourceSpec } from "./source";

class FakeWorker {
  static last: FakeWorker | null = null;
  readonly posted: PageToWorkerMessage[] = [];
  onmessage: ((event: MessageEvent<WorkerToPageMessage>) => void) | null = null;
  onerror: ((event: ErrorEvent) => void) | null = null;
  constructor() {
    FakeWorker.last = this;
  }
  postMessage(message: PageToWorkerMessage) {
    this.posted.push(message);
  }
  terminate() {}
  emit(message: WorkerToPageMessage) {
    this.onmessage?.({ data: message } as MessageEvent<WorkerToPageMessage>);
  }
  /** Generation of the last decode run the page started. */
  get generation(): number {
    const starts = this.posted.filter((m) => m.type === "start");
    return (starts[starts.length - 1] as { generation: number }).generation;
  }
}

function fakeFrame(seconds: number, durationSeconds = 0.04) {
  return {
    timestamp: seconds * 1e6,
    duration: durationSeconds * 1e6,
    displayWidth: 1920,
    displayHeight: 1080,
    close: vi.fn(),
  } as unknown as VideoFrame;
}

const source: EngineSourceSpec = {
  url: "/stream",
  headers: {},
  container: "mkv",
  durationSeconds: 10,
  startSeconds: 0,
  video: { codec: "h264", width: 1920, height: 1080, transfer: "sdr", stripDolbyVision: false },
  audio: null,
};

async function settle() {
  await vi.advanceTimersByTimeAsync(50);
}

describe("EnginePlayer", () => {
  let element: HTMLVideoElement;
  let events: string[];

  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal("Worker", FakeWorker);
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) =>
      setTimeout(() => callback(performance.now()), 16),
    );
    vi.stubGlobal("cancelAnimationFrame", (id: number) => clearTimeout(id));
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(
      () =>
        ({
          fillRect: vi.fn(),
          drawImage: vi.fn(),
          clearRect: vi.fn(),
          translate: vi.fn(),
          scale: vi.fn(),
          rotate: vi.fn(),
          resetTransform: vi.fn(),
        }) as never,
    );
    element = document.createElement("video");
    events = [];
    for (const type of [
      "loadstart",
      "durationchange",
      "loadedmetadata",
      "loadeddata",
      "canplay",
      "play",
      "playing",
      "pause",
      "seeking",
      "seeked",
      "ended",
    ]) {
      element.addEventListener(type, () => events.push(type));
    }
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  function start(onFailure = vi.fn(), muted = false) {
    const player = new EnginePlayer({
      source: { ...source },
      surface: document.createElement("div"),
      element,
      volume: 1,
      muted,
      onFailure,
    });
    player.load();
    const worker = FakeWorker.last!;
    return { player, worker, onFailure };
  }

  async function open(worker: FakeWorker, withAudio = false) {
    worker.emit({
      type: "ready",
      info: {
        durationSeconds: 10,
        video: {
          codedWidth: 1920,
          codedHeight: 1080,
          displayWidth: 1920,
          displayHeight: 1080,
          rotation: 0,
          flip: false,
        },
        audio: withAudio ? { sampleRate: 48_000, channels: 6 } : null,
      },
    });
    await settle();
  }

  it("reports metadata, the first picture and playability in media element order", async () => {
    const { player, worker } = start();
    expect(worker.posted[0]).toMatchObject({ type: "open" });
    await open(worker);
    expect(player.readyState).toBe(1);
    expect(player.duration).toBe(10);
    expect(player.videoWidth).toBe(1920);

    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0.04) });
    await settle();
    expect(events).toEqual([
      "loadstart",
      "durationchange",
      "loadedmetadata",
      "loadeddata",
      "canplay",
    ]);
    expect(player.readyState).toBe(4);
    player.destroy();
  });

  it("plays on a wall clock without audio and presents frames as they come due", async () => {
    const { player, worker } = start();
    await open(worker);
    const first = fakeFrame(0);
    const second = fakeFrame(0.04);
    worker.emit({ type: "frame", generation: worker.generation, frame: first });
    worker.emit({ type: "frame", generation: worker.generation, frame: second });
    await settle();
    await player.play();
    expect(events.slice(-2)).toEqual(["play", "playing"]);
    await vi.advanceTimersByTimeAsync(200);
    expect(player.currentTime).toBeGreaterThan(0.1);
    // The first frame was replaced on screen and handed back to the worker.
    expect(first.close).toHaveBeenCalled();
    expect(worker.posted.some((m) => m.type === "frame-released")).toBe(true);
    player.destroy();
  });

  it("counts the frames it skips as dropped in the playback quality", async () => {
    const { player, worker } = start();
    await open(worker);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    await settle();
    await player.play();
    await vi.advanceTimersByTimeAsync(200);
    // Three frames arrive late together: only the newest is drawn.
    for (const timestamp of [0.04, 0.08, 0.12]) {
      worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(timestamp) });
    }
    await vi.advanceTimersByTimeAsync(50);
    const quality = player.getVideoPlaybackQuality();
    expect(quality.droppedVideoFrames).toBe(2);
    expect(quality.totalVideoFrames).toBe(4);
    player.destroy();
  });

  it("drops frames from a superseded run and completes the seek on the new one", async () => {
    const { player, worker } = start();
    await open(worker);
    const before = worker.generation;
    worker.emit({ type: "frame", generation: before, frame: fakeFrame(0) });
    worker.emit({ type: "frame", generation: before, frame: fakeFrame(0.04) });
    await settle();

    player.seek(5);
    expect(player.seeking).toBe(true);
    expect(player.currentTime).toBe(5);
    expect(worker.generation).toBe(before + 1);
    const stale = fakeFrame(0.04);
    worker.emit({ type: "frame", generation: before, frame: stale });
    expect(stale.close).toHaveBeenCalled();

    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(5) });
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(5.04) });
    await settle();
    expect(player.seeking).toBe(false);
    expect(events).toContain("seeked");
    player.destroy();
  });

  it("fails a seek that never becomes playable as a read failure", async () => {
    const onFailure = vi.fn();
    const { player, worker } = start(onFailure);
    await open(worker);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0.04) });
    await settle();

    // A seek that completes clears the deadline.
    player.seek(5);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(5) });
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(5.04) });
    await settle();
    await vi.advanceTimersByTimeAsync(20_001);
    expect(onFailure).not.toHaveBeenCalled();

    // A seek whose reads never answer fails over instead of seeking forever.
    player.seek(30);
    await vi.advanceTimersByTimeAsync(20_001);
    expect(player.seeking).toBe(true);
    expect(onFailure).toHaveBeenCalledWith(
      expect.objectContaining({ classification: "browser_engine_read_failed" }),
    );
    player.destroy();
  });

  it("finishes a seek past the last frame and then ends", async () => {
    const { player, worker } = start();
    await open(worker);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    await settle();
    await player.play();

    player.seek(10);
    worker.emit({ type: "video-ended", generation: worker.generation });
    await settle();
    expect(player.seeking).toBe(false);
    expect(events).toContain("seeked");
    expect(player.ended).toBe(true);
    expect(player.paused).toBe(true);
    expect(events.slice(-2)).toEqual(["pause", "ended"]);
    player.destroy();
  });

  it("ends only after the last picture's own duration", async () => {
    const { player, worker } = start();
    await open(worker);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0, 2) });
    worker.emit({ type: "video-ended", generation: worker.generation });
    await settle();
    await player.play();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(player.ended).toBe(false);
    await vi.advanceTimersByTimeAsync(1_500);
    expect(player.ended).toBe(true);
    player.destroy();
  });

  it("treats a long wait for data while playing as a lost connection", async () => {
    const onFailure = vi.fn();
    const { player, worker } = start(onFailure);
    await open(worker);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0.04) });
    await settle();
    await player.play();
    // No more frames arrive, so playback waits; a short wait is not a failure.
    await vi.advanceTimersByTimeAsync(5_000);
    expect(onFailure).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(onFailure).toHaveBeenCalledWith(
      expect.objectContaining({ classification: "browser_engine_read_failed" }),
    );
    // A failed engine stops its worker even when no other route takes over.
    expect(worker.posted.some((m) => m.type === "close")).toBe(true);
    player.destroy();
  });

  it("still notices a video stall after the audio track has ended", async () => {
    audioOutput.resumes = true;
    audioOutput.state = "suspended";
    const onFailure = vi.fn();
    const { player, worker } = start(onFailure);
    await open(worker, true);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0.04) });
    worker.emit({ type: "audio-buffered", generation: worker.generation });
    await settle();
    await player.play();
    // The audio runs out first; the clock carries on from wall time.
    (player as unknown as { onWorklet(message: unknown): void }).onWorklet({
      type: "drained",
      generation: worker.generation,
    });
    await vi.advanceTimersByTimeAsync(25_000);
    expect(onFailure).toHaveBeenCalledWith(
      expect.objectContaining({ classification: "browser_engine_read_failed" }),
    );
    player.destroy();
  });

  it("fails over when video falls far behind audio that is still playing", async () => {
    audioOutput.resumes = true;
    audioOutput.state = "suspended";
    audioOutput.contextTime = 0;
    const onFailure = vi.fn();
    const { player, worker } = start(onFailure);
    await open(worker, true);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "audio-buffered", generation: worker.generation });
    await settle();
    await player.play();
    const worklet = player as unknown as { onWorklet(message: unknown): void };
    worklet.onWorklet({
      type: "clock",
      generation: worker.generation,
      contextTime: 0,
      mediaTime: 0,
      advancing: true,
    });
    // Audio plays on for 10 s with no new video: still within the limit.
    audioOutput.contextTime = 10;
    await vi.advanceTimersByTimeAsync(100);
    expect(onFailure).not.toHaveBeenCalled();
    // 25 s behind the audio clock, the video reads have stopped.
    audioOutput.contextTime = 25;
    await vi.advanceTimersByTimeAsync(100);
    expect(onFailure).toHaveBeenCalledWith(
      expect.objectContaining({ classification: "browser_engine_read_failed" }),
    );
    audioOutput.contextTime = 0;
    player.destroy();
  });

  it("stops the clock on pause once the audio has drained, even after decoding ended", async () => {
    audioOutput.resumes = true;
    audioOutput.state = "suspended";
    audioOutput.contextTime = 0;
    const { player, worker } = start();
    await open(worker, true);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "audio-buffered", generation: worker.generation });
    await settle();
    await player.play();
    const worklet = player as unknown as { onWorklet(message: unknown): void };
    worklet.onWorklet({
      type: "clock",
      generation: worker.generation,
      contextTime: 0,
      mediaTime: 0,
      advancing: true,
    });
    // Decoding is done but pictures are still queued when the audio runs out.
    for (const timestamp of [5, 5.04, 5.08]) {
      worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(timestamp) });
    }
    worker.emit({ type: "video-ended", generation: worker.generation });
    audioOutput.contextTime = 1;
    worklet.onWorklet({ type: "drained", generation: worker.generation });
    player.pause();
    const paused = player.currentTime;
    // The audio context keeps running; a paused player must not.
    audioOutput.contextTime = 3;
    await vi.advanceTimersByTimeAsync(100);
    expect(player.currentTime).toBe(paused);
    audioOutput.contextTime = 0;
    player.destroy();
  });

  it("hands a worker failure to the player once, with its classification", async () => {
    const onFailure = vi.fn();
    const { player, worker } = start(onFailure);
    worker.emit({
      type: "error",
      generation: null,
      classification: "browser_engine_open_failed",
      message: "nope",
    });
    worker.emit({ type: "error", generation: null, classification: "other", message: "again" });
    expect(onFailure).toHaveBeenCalledTimes(1);
    expect(onFailure).toHaveBeenCalledWith(
      expect.objectContaining({ classification: "browser_engine_open_failed" }),
    );
    await expect(player.play()).rejects.toMatchObject({ name: "NotSupportedError" });
    player.destroy();
  });

  it("stops its animation frames and hidden-tab ticks once it fails", async () => {
    const frames = vi.fn((callback: FrameRequestCallback) =>
      setTimeout(() => callback(performance.now()), 16),
    );
    vi.stubGlobal("requestAnimationFrame", frames);
    const { player, worker } = start();
    await open(worker);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0.04) });
    await settle();
    Object.defineProperty(document, "hidden", { configurable: true, get: () => true });
    try {
      document.dispatchEvent(new Event("visibilitychange"));
      worker.emit({ type: "error", generation: null, classification: "x_failed", message: "boom" });
      // Past the worker's terminate grace, nothing is left scheduled.
      await vi.advanceTimersByTimeAsync(1_100);
      const scheduled = frames.mock.calls.length;
      await vi.advanceTimersByTimeAsync(1_000);
      expect(frames.mock.calls.length).toBe(scheduled);
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      delete (document as { hidden?: boolean }).hidden;
    }
    player.destroy();
  });

  it("reports itself paused when it fails while playing", async () => {
    // The player reads `paused` in onFailure to decide whether a reconnect resumes.
    let pausedWhenReported: boolean | null = null;
    const onFailure = vi.fn(() => {
      pausedWhenReported = player.paused;
    });
    const { player, worker } = start(onFailure);
    await open(worker);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0.04) });
    await settle();
    await player.play();
    expect(player.paused).toBe(false);
    worker.emit({ type: "error", generation: null, classification: "x_failed", message: "boom" });
    expect(pausedWhenReported).toBe(false);
    expect(player.paused).toBe(true);
    expect(events.filter((event) => event === "pause")).toHaveLength(1);
    player.destroy();
  });

  it("slows its presentation loop while paused and wakes on play", async () => {
    const frames = vi.fn((callback: FrameRequestCallback) =>
      setTimeout(() => callback(performance.now()), 16),
    );
    vi.stubGlobal("requestAnimationFrame", frames);
    const { player, worker } = start();
    await open(worker);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0.04) });
    await settle();
    await vi.advanceTimersByTimeAsync(100);
    // Paused with the first picture up: a second of idling asks for few frames.
    const before = frames.mock.calls.length;
    await vi.advanceTimersByTimeAsync(1_000);
    expect(frames.mock.calls.length - before).toBeLessThanOrEqual(5);
    await player.play();
    const playing = frames.mock.calls.length;
    await vi.advanceTimersByTimeAsync(200);
    expect(frames.mock.calls.length - playing).toBeGreaterThan(5);
    player.destroy();
  });

  it("reports when no picture arrives in time", async () => {
    const onFailure = vi.fn();
    const { player } = start(onFailure);
    await vi.advanceTimersByTimeAsync(20_001);
    expect(onFailure).toHaveBeenCalledWith(
      expect.objectContaining({ classification: "browser_engine_startup_timeout" }),
    );
    player.destroy();
  });

  it("reports when a picture arrives but its audio never does", async () => {
    const onFailure = vi.fn();
    const { player, worker } = start(onFailure);
    await open(worker, true);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    await settle();
    expect(player.readyState).toBe(2);
    await vi.advanceTimersByTimeAsync(20_001);
    expect(onFailure).toHaveBeenCalledWith(
      expect.objectContaining({ classification: "browser_engine_startup_timeout" }),
    );
    player.destroy();
  });

  it("waits for the worker's audio before reporting the position playable", async () => {
    audioOutput.resumes = true;
    audioOutput.state = "suspended";
    const { player, worker } = start();
    await open(worker, true);
    expect(worker.posted.some((m) => m.type === "audio-output")).toBe(true);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0.04) });
    await settle();
    expect(player.readyState).toBe(2);
    expect(events).not.toContain("canplay");
    worker.emit({ type: "audio-buffered", generation: worker.generation });
    await settle();
    expect(player.readyState).toBe(4);
    expect(events).toContain("canplay");
    player.destroy();
  });

  it("waits for a picture after the first before it starts the clock", async () => {
    audioOutput.resumes = true;
    audioOutput.state = "suspended";
    const { player, worker } = start();
    await open(worker, true);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "audio-buffered", generation: worker.generation });
    await settle();
    await player.play();
    // The first picture is up and audio is queued, but nothing follows it yet.
    expect(player.readyState).toBe(2);
    expect(events).not.toContain("canplay");
    expect(events).not.toContain("playing");

    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0.04) });
    await settle();
    expect(player.readyState).toBe(4);
    expect(events.slice(-2)).toEqual(["canplay", "playing"]);
    player.destroy();
  });

  it("plays a video of a single picture once decoding has ended", async () => {
    const { player, worker } = start();
    await open(worker);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0, 2) });
    worker.emit({ type: "video-ended", generation: worker.generation });
    await settle();
    expect(player.readyState).toBe(4);
    player.destroy();
  });

  it("reports a start that never decodes past its first picture", async () => {
    const onFailure = vi.fn();
    const { player, worker } = start(onFailure);
    await open(worker);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    await vi.advanceTimersByTimeAsync(20_001);
    expect(onFailure).toHaveBeenCalledWith(
      expect.objectContaining({
        classification: "browser_engine_startup_timeout",
        message: "The browser engine decoded no picture after the first in time.",
      }),
    );
    player.destroy();
  });

  it("settles paused and rejects play() when the browser refuses to start audio", async () => {
    audioOutput.resumes = false;
    audioOutput.state = "suspended";
    const { player, worker } = start();
    await open(worker, true);
    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(0) });
    worker.emit({ type: "audio-buffered", generation: worker.generation });
    await settle();
    const attempt = player.play();
    const rejection = expect(attempt).rejects.toMatchObject({ name: "NotAllowedError" });
    await vi.advanceTimersByTimeAsync(2_000);
    await rejection;
    expect(player.paused).toBe(true);
    expect(events.slice(-2)).toEqual(["play", "pause"]);
    player.destroy();
  });

  it("refuses play() without events on a page that never had a user gesture", async () => {
    audioOutput.resumes = false;
    audioOutput.state = "suspended";
    vi.stubGlobal("navigator", { ...navigator, userActivation: { hasBeenActive: false } });
    const { player, worker } = start();
    await open(worker, true);
    const before = events.length;
    await expect(player.play()).rejects.toMatchObject({ name: "NotAllowedError" });
    expect(events.length).toBe(before);
    expect(player.paused).toBe(true);
    player.destroy();
  });

  /** Frames for the first `seconds` of the current run, with audio buffered. */
  function feed(worker: FakeWorker, seconds: number, from = 0) {
    for (let time = from; time < from + seconds; time += 0.04) {
      worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(time) });
    }
    worker.emit({ type: "audio-buffered", generation: worker.generation });
  }

  for (const when of ["before", "after"] as const) {
    it(`plays muted without a user gesture on wall time, play() ${when} the file opens`, async () => {
      audioOutput.resumes = false;
      audioOutput.state = "suspended";
      vi.stubGlobal("navigator", { ...navigator, userActivation: { hasBeenActive: false } });
      const { player, worker } = start(vi.fn(), true);
      if (when === "before") await player.play();
      await open(worker, true);
      feed(worker, 2);
      await settle();
      if (when === "after") await player.play();
      expect(events).toContain("play");
      expect(events[events.length - 1]).toBe("playing");
      await vi.advanceTimersByTimeAsync(1_000);
      expect(player.paused).toBe(false);
      expect(player.currentTime).toBeGreaterThan(0.9);
      player.destroy();
    });
  }

  it("brings audio back in step from the current position when unmuted", async () => {
    audioOutput.resumes = false;
    audioOutput.state = "suspended";
    vi.stubGlobal("navigator", { ...navigator, userActivation: { hasBeenActive: false } });
    const { player, worker } = start(vi.fn(), true);
    await open(worker, true);
    feed(worker, 3);
    await settle();
    await player.play();
    await vi.advanceTimersByTimeAsync(1_000);

    // The unmute click is the gesture the browser was waiting for.
    audioOutput.resumes = true;
    const before = worker.generation;
    const position = player.currentTime;
    player.muted = false;
    await settle();
    expect(worker.generation).toBe(before + 1);
    expect(worker.posted[worker.posted.length - 1]).toMatchObject({ type: "start" });
    expect((worker.posted[worker.posted.length - 1] as { position: number }).position).toBeCloseTo(
      position,
      1,
    );
    expect(player.paused).toBe(false);
    player.destroy();
  });

  it("pauses when unmuted while the browser still refuses audio", async () => {
    audioOutput.resumes = false;
    audioOutput.state = "suspended";
    vi.stubGlobal("navigator", { ...navigator, userActivation: { hasBeenActive: false } });
    const { player, worker } = start(vi.fn(), true);
    await open(worker, true);
    feed(worker, 2);
    await settle();
    await player.play();
    player.muted = false;
    await settle();
    expect(player.paused).toBe(true);
    expect(events[events.length - 1]).toBe("pause");
    player.destroy();
  });

  it("starts nothing new when it fails while the renderer is still being created", async () => {
    const gpu = { draw: vi.fn(), destroy: vi.fn() };
    let resolveRenderer!: (renderer: WebGPURenderer) => void;
    vi.spyOn(WebGPURenderer, "create").mockReturnValue(
      new Promise((resolve) => (resolveRenderer = resolve)),
    );
    const onFailure = vi.fn();
    const player = new EnginePlayer({
      source: { ...source, video: { ...source.video, transfer: "pq" } },
      surface: document.createElement("div"),
      element,
      volume: 1,
      muted: false,
      onFailure,
    });
    player.load();
    const worker = FakeWorker.last!;
    await open(worker);
    worker.emit({ type: "error", generation: null, classification: "x_failed", message: "boom" });
    expect(onFailure).toHaveBeenCalledTimes(1);

    resolveRenderer(gpu as unknown as WebGPURenderer);
    await settle();
    expect(gpu.destroy).toHaveBeenCalled();
    expect(worker.posted.some((m) => m.type === "start")).toBe(false);
    player.destroy();
  });

  it("presents hardware HDR frames that kept their PQ tag through the 2D canvas", async () => {
    const gpu = { draw: vi.fn(), destroy: vi.fn() };
    vi.spyOn(WebGPURenderer, "create").mockResolvedValue(gpu as unknown as WebGPURenderer);
    const surface = document.createElement("div");
    const player = new EnginePlayer({
      source: { ...source, video: { ...source.video, transfer: "pq" } },
      surface,
      element,
      volume: 1,
      muted: false,
      onFailure: vi.fn(),
    });
    player.load();
    const worker = FakeWorker.last!;
    await open(worker);
    const firstCanvas = surface.querySelector("canvas");
    const frame = Object.assign(fakeFrame(0), {
      colorSpace: { transfer: "pq", primaries: "bt2020", matrix: "bt2020-ncl" },
    });
    worker.emit({ type: "frame", generation: worker.generation, frame });
    await settle();
    expect(gpu.destroy).toHaveBeenCalled();
    expect(gpu.draw).not.toHaveBeenCalled();
    expect(surface.querySelector("canvas")).not.toBe(firstCanvas);
    expect(surface.querySelectorAll("canvas")).toHaveLength(1);
    player.destroy();
    expect(surface.querySelector("canvas")).toBeNull();
  });

  it("closes queued frames and the worker on destroy", async () => {
    const { player, worker } = start();
    await open(worker);
    const queued = fakeFrame(0);
    worker.emit({ type: "frame", generation: worker.generation, frame: queued });
    player.destroy();
    expect(queued.close).toHaveBeenCalled();
    expect(worker.posted[worker.posted.length - 1]).toMatchObject({ type: "close" });
  });
});
