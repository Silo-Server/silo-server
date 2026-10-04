import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EnginePlayer } from "./EnginePlayer";
import { WebGPURenderer } from "./renderers";

const audioOutput = vi.hoisted(() => ({
  resumes: true,
  state: "suspended" as AudioContextState,
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
      audibleContextTime: () => 0,
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

function fakeFrame(seconds: number) {
  return {
    timestamp: seconds * 1e6,
    duration: 40_000,
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
      () => ({ fillRect: vi.fn(), drawImage: vi.fn(), clearRect: vi.fn() }) as never,
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

  function start(onFailure = vi.fn()) {
    const player = new EnginePlayer({
      source: { ...source },
      surface: document.createElement("div"),
      element,
      volume: 1,
      muted: false,
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
        video: { codedWidth: 1920, codedHeight: 1080, displayWidth: 1920, displayHeight: 1080 },
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

  it("drops frames from a superseded run and completes the seek on the new one", async () => {
    const { player, worker } = start();
    await open(worker);
    const before = worker.generation;
    worker.emit({ type: "frame", generation: before, frame: fakeFrame(0) });
    await settle();

    player.seek(5);
    expect(player.seeking).toBe(true);
    expect(player.currentTime).toBe(5);
    expect(worker.generation).toBe(before + 1);
    const stale = fakeFrame(0.04);
    worker.emit({ type: "frame", generation: before, frame: stale });
    expect(stale.close).toHaveBeenCalled();

    worker.emit({ type: "frame", generation: worker.generation, frame: fakeFrame(5) });
    await settle();
    expect(player.seeking).toBe(false);
    expect(events).toContain("seeked");
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

  it("reports when no picture arrives in time", async () => {
    const onFailure = vi.fn();
    const { player } = start(onFailure);
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
    await settle();
    expect(player.readyState).toBe(2);
    expect(events).not.toContain("canplay");
    worker.emit({ type: "audio-buffered", generation: worker.generation });
    await settle();
    expect(player.readyState).toBe(4);
    expect(events).toContain("canplay");
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
