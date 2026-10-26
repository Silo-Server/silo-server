import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DEFAULT_PICTURE_COLOR } from "./pictureColor";
import type { EnginePlaneFrame } from "./protocol";
import {
  Canvas2DRenderer,
  canvasMapsHDR,
  DEVICE_RECOVERY_TIMEOUT_MS,
  FRAME_CHECK_RETRY_FRAMES,
  HDRPresenter,
  MAX_DEVICE_RECOVERIES,
  probePresenter,
  resetPresenterStateForTests,
  routePicture,
  WebGPURenderer,
  type EnginePicture,
  type PresenterEvents,
  type Viewport,
} from "./renderers";

const viewport: Viewport = { fit: "contain", devicePixels: null };
const size = { width: 1920, height: 1080, rotation: 0 as const, flip: false };

const frame = (
  colorSpace: { transfer?: string | null; primaries?: string | null },
  displayWidth = 1920,
  displayHeight = 1080,
) =>
  ({
    kind: "frame",
    frame: {
      colorSpace,
      displayWidth,
      displayHeight,
      close: vi.fn(),
    } as unknown as VideoFrame,
    timestamp: 0,
    duration: 0,
  }) satisfies EnginePicture;

const RETAGGED = { transfer: "iec61966-2-1", primaries: "bt709" };
const HARDWARE_PQ = { transfer: "pq", primaries: "bt2020" };

function planes(width = 4, height = 4, chromaWidth = 2, chromaHeight = 2): EnginePicture {
  const plane: EnginePlaneFrame = {
    timestamp: 0,
    duration: 0,
    bitDepth: 10,
    width,
    height,
    chromaWidth,
    chromaHeight,
    buffer: new ArrayBuffer(64),
    offsets: [0, 32, 48],
    strides: [width * 2, chromaWidth * 2, chromaWidth * 2],
  };
  return { kind: "planes", planes: plane, timestamp: 0, duration: 0 };
}

// -- A WebGPU stand-in --------------------------------------------------------

class FakeDevice {
  readonly lost: Promise<GPUDeviceLostInfo>;
  private resolveLost!: (info: GPUDeviceLostInfo) => void;
  /** Explicit `lost.then` subscriptions, to check nothing piles up on the device. */
  lostSubscriptions = 0;
  destroyed = false;
  /** Results popErrorScope hands out, in order; null when it runs out. */
  scopeErrors: Array<{ message: string } | null> = [];
  /** When set, popErrorScope answers with this instead, as a slow driver would. */
  pendingPop: Promise<{ message: string } | null> | null = null;
  pipelineFailure: Error | null = null;
  compilerMessages: Array<{ type: string; message: string; lineNum: number }> = [];
  readonly limits = { maxTextureDimension2D: 8192 };
  readonly uniforms: Float32Array[] = [];
  /** What successive read-back buffers hold: the frame check reads the import, then the copy. */
  readbacks: Uint8Array[] = [];
  /** Set to make copying a VideoFrame throw, as Firefox does. */
  copyUnsupported = false;
  readonly queue = {
    writeBuffer: vi.fn((_buffer: unknown, _offset: number, data: ArrayBuffer) =>
      this.uniforms.push(new Float32Array(data.slice(0))),
    ),
    writeTexture: vi.fn(),
    copyExternalImageToTexture: vi.fn(() => {
      if (this.copyUnsupported) throw new TypeError("'source' could not be converted");
    }),
    submit: vi.fn(),
    onSubmittedWorkDone: async () => {},
  };
  readonly importExternalTexture = vi.fn(() => ({}));
  readonly listeners = new Set<(event: Event) => void>();

  constructor() {
    const lost = new Promise<GPUDeviceLostInfo>((resolve) => (this.resolveLost = resolve));
    const then = lost.then.bind(lost);
    lost.then = ((...args: Parameters<typeof then>) => {
      this.lostSubscriptions += 1;
      return then(...args);
    }) as typeof lost.then;
    this.lost = lost;
  }

  createShaderModule() {
    return { getCompilationInfo: async () => ({ messages: this.compilerMessages }) };
  }
  createBindGroupLayout() {
    return {};
  }
  createPipelineLayout() {
    return {};
  }
  createRenderPipelineAsync() {
    return this.pipelineFailure ? Promise.reject(this.pipelineFailure) : Promise.resolve({});
  }
  pushErrorScope() {}
  popErrorScope() {
    return this.pendingPop ?? Promise.resolve(this.scopeErrors.shift() ?? null);
  }
  createBuffer({ usage }: { usage: number }) {
    const readable = (usage & GPUBufferUsage.MAP_READ) !== 0;
    const contents = (readable && this.readbacks.shift()) || new Uint8Array(256 * 8);
    return {
      destroy: vi.fn(),
      mapAsync: async () => {},
      getMappedRange: () => contents.buffer,
    };
  }
  createSampler() {
    return {};
  }
  createBindGroup() {
    return {};
  }
  /** Every texture made, to check that frame copies are let go. */
  readonly textures: Array<{ format: string; destroy: ReturnType<typeof vi.fn> }> = [];
  createTexture({ size, format }: { size: [number, number]; format: string }) {
    const texture = {
      width: size[0],
      height: size[1],
      format,
      createView: () => ({}),
      destroy: vi.fn(),
    };
    this.textures.push(texture);
    return texture;
  }
  /** Whether every texture a frame was copied into has been let go. */
  copiesReleased() {
    return this.textures
      .filter((texture) => texture.format === "rgba16float")
      .every((texture) => texture.destroy.mock.calls.length > 0);
  }
  createCommandEncoder() {
    return {
      beginRenderPass: () => ({ setPipeline() {}, setBindGroup() {}, draw() {}, end() {} }),
      copyTextureToBuffer() {},
      finish: () => ({}),
    };
  }
  addEventListener(_type: string, listener: (event: Event) => void) {
    this.listeners.add(listener);
  }
  removeEventListener(_type: string, listener: (event: Event) => void) {
    this.listeners.delete(listener);
  }
  emitUncapturedError(message: string) {
    for (const listener of this.listeners) listener({ error: { message } } as unknown as Event);
  }
  lose(message = "The GPU process went away.") {
    this.resolveLost({ reason: "unknown", message } as GPUDeviceLostInfo);
  }
  destroy() {
    this.destroyed = true;
    this.resolveLost({ reason: "destroyed", message: "" } as GPUDeviceLostInfo);
  }
}

/** A read-back check picture with detail in it (a gradient), as a real frame gives. */
const detailed = () => Uint8Array.from({ length: 256 * 8 }, (_, index) => (index * 7) % 256);
/** Lets the frame check's read-backs and comparison finish. */
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

/** How many times a frame reached `device`, imported or copied. */
const framesSent = (device: FakeDevice) =>
  device.importExternalTexture.mock.calls.length +
  device.queue.copyExternalImageToTexture.mock.calls.length;

let devices: Array<FakeDevice | Promise<FakeDevice>>;
let adapter: { info: { isFallbackAdapter: boolean }; requestDevice: ReturnType<typeof vi.fn> };
let gpuContexts: Array<{
  configure: ReturnType<typeof vi.fn>;
  unconfigure: ReturnType<typeof vi.fn>;
}>;
let context2d: { drawImage: ReturnType<typeof vi.fn> } & Record<string, unknown>;
/** What a canvas draws (green, 0-255) for a grey frame of this code with this transfer tag. */
let canvasModel: (transfer: string, code: number) => number;

const raw = (code: number) => Math.round(code * 255);

/**
 * Measured with 16x16 grey frames in Chrome 155 and Safari 26 (WebKit), which
 * tone-map, and Firefox 157, which draws the raw codes for every tag.
 */
const MEASURED: Record<string, Record<string, number>> = {
  pq: { "0.3": 44, "0.58": 187, "0.75": 254 },
  hlg: { "0.3": 54, "0.75": 187, "0.9": 233 },
};
const chromiumCanvas = (transfer: string, code: number) =>
  MEASURED[transfer]?.[String(Math.round(code * 100) / 100)] ?? raw(code);

class FakeVideoFrame {
  readonly colorSpace: VideoColorSpaceInit;
  readonly code: number;
  readonly displayWidth: number;
  readonly displayHeight: number;
  constructor(
    data: Uint8Array,
    init: { colorSpace: VideoColorSpaceInit; codedWidth: number; codedHeight: number },
  ) {
    this.colorSpace = init.colorSpace;
    this.code = (data[0]! - 16) / 219;
    this.displayWidth = init.codedWidth;
    this.displayHeight = init.codedHeight;
  }
  close() {}
}

beforeEach(() => {
  devices = [];
  adapter = {
    info: { isFallbackAdapter: false },
    requestDevice: vi.fn(async () => {
      const next = devices.shift();
      if (!next) throw new Error("no device");
      return next;
    }),
  };
  vi.stubGlobal("navigator", {
    ...navigator,
    gpu: {
      requestAdapter: vi.fn(async () => adapter),
      getPreferredCanvasFormat: () => "bgra8unorm",
    },
  });
  vi.stubGlobal("GPUShaderStage", { VERTEX: 1, FRAGMENT: 2 });
  vi.stubGlobal("GPUBufferUsage", { UNIFORM: 64, COPY_DST: 8, MAP_READ: 1 });
  vi.stubGlobal("GPUMapMode", { READ: 1 });
  vi.stubGlobal("GPUTextureUsage", {
    COPY_SRC: 1,
    COPY_DST: 2,
    TEXTURE_BINDING: 4,
    RENDER_ATTACHMENT: 16,
  });
  vi.stubGlobal("VideoFrame", FakeVideoFrame);
  vi.stubGlobal("devicePixelRatio", 1);
  gpuContexts = [];
  canvasModel = (_transfer, code) => raw(code);
  let drawn: FakeVideoFrame | null = null;
  context2d = {
    fillRect: vi.fn(),
    clearRect: vi.fn(),
    translate: vi.fn(),
    scale: vi.fn(),
    rotate: vi.fn(),
    resetTransform: vi.fn(),
    drawImage: vi.fn((source: FakeVideoFrame) => (drawn = source)),
    getImageData: () => ({
      data: [0, drawn ? canvasModel(String(drawn.colorSpace.transfer), drawn.code) : 0, 0, 255],
    }),
  };
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(function (
    this: HTMLCanvasElement,
    type: string,
  ) {
    if (type === "2d") return context2d as never;
    const context = {
      configure: vi.fn(),
      unconfigure: vi.fn(),
      getCurrentTexture: () => ({ createView: () => ({}) }),
    };
    gpuContexts.push(context);
    return context as never;
  });
});

afterEach(() => {
  resetPresenterStateForTests();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function events() {
  return {
    onFailure: vi.fn<PresenterEvents["onFailure"]>(),
    onRestored: vi.fn<PresenterEvents["onRestored"]>(),
  };
}

async function renderer(on = events()) {
  const canvas = document.createElement("canvas");
  document.body.appendChild(canvas);
  const gpu = await WebGPURenderer.create(canvas, "pq", DEFAULT_PICTURE_COLOR, on);
  return { gpu, on, canvas };
}

// -- Tests -------------------------------------------------------------------

describe("routePicture", () => {
  it("sends each picture to the path its kind and tag call for", () => {
    expect(routePicture(planes())).toBe("planes");
    expect(routePicture(frame(RETAGGED))).toBe("frame");
    // Untagged primaries import as BT.709 as well.
    expect(routePicture(frame({ transfer: "iec61966-2-1", primaries: null }))).toBe("frame");
    // Hardware decoders keep the stream's own tag; only the browser can map it.
    expect(routePicture(frame(HARDWARE_PQ))).toBe("browser");
    expect(routePicture(frame({ transfer: "hlg", primaries: "bt2020" }))).toBe("browser");
  });

  it("refuses frames whose codes it cannot interpret", () => {
    expect(routePicture(frame({ transfer: null, primaries: null }))).toBe("unsupported");
    expect(routePicture(frame({ transfer: "bt709", primaries: "bt709" }))).toBe("unsupported");
    // An sRGB transfer with other primaries would be gamut-converted on import.
    expect(routePicture(frame({ transfer: "iec61966-2-1", primaries: "bt2020" }))).toBe(
      "unsupported",
    );
  });
});

describe("Canvas2DRenderer", () => {
  it("turns and mirrors a rotated picture to fill its upright letterbox", () => {
    const calls: unknown[][] = [];
    const record =
      (name: string) =>
      (...args: unknown[]) =>
        calls.push([name, ...args]);
    Object.assign(context2d, {
      translate: record("translate"),
      scale: record("scale"),
      rotate: record("rotate"),
      drawImage: (_frame: unknown, ...args: unknown[]) => calls.push(["drawImage", ...args]),
      resetTransform: record("resetTransform"),
    });
    const canvas = document.createElement("canvas");
    Object.defineProperty(canvas, "clientWidth", { value: 1600 });
    Object.defineProperty(canvas, "clientHeight", { value: 900 });

    // A 1920x1080 phone recording stored landscape, shown portrait: 1080x1920.
    new Canvas2DRenderer(canvas).draw(
      { kind: "frame", frame: {} as VideoFrame, timestamp: 0, duration: 0 },
      { width: 1080, height: 1920, rotation: 90, flip: true },
      viewport,
    );

    // The portrait letterbox is 506.25x900 centered at (800, 450); the
    // landscape picture is drawn 900x506.25 into it, turned a quarter.
    expect(calls).toEqual([
      ["translate", 800, 450],
      ["scale", -1, 1],
      ["rotate", Math.PI / 2],
      ["drawImage", -450, -253.125, 900, 506.25],
      ["resetTransform"],
    ]);
  });

  it("sizes its canvas to the box's exact device pixels when the browser reports them", () => {
    const canvas = document.createElement("canvas");
    Object.defineProperty(canvas, "clientWidth", { value: 1302 });
    Object.defineProperty(canvas, "clientHeight", { value: 774 });
    vi.stubGlobal("devicePixelRatio", 1.25);
    const renderer = new Canvas2DRenderer(canvas);
    // Measured in Chrome at a 1.25 device pixel ratio.
    renderer.draw(frame(RETAGGED), size, {
      fit: "contain",
      devicePixels: { width: 1627, height: 967 },
    });
    expect([canvas.width, canvas.height]).toEqual([1627, 967]);
    // Without the exact figure it falls back to CSS pixels times the ratio.
    renderer.draw(frame(RETAGGED), size, viewport);
    expect([canvas.width, canvas.height]).toEqual([1628, 968]);
  });

  it("refuses uploaded planes instead of skipping them", () => {
    const renderer = new Canvas2DRenderer(document.createElement("canvas"));
    expect(() => renderer.draw(planes(), size, viewport)).toThrow(/planes/);
  });

  it("leaves no canvas behind when the 2D context is unavailable", () => {
    vi.mocked(HTMLCanvasElement.prototype.getContext).mockReturnValue(null);
    const surface = document.createElement("div");
    expect(() => Canvas2DRenderer.in(surface)).toThrow(/unavailable/);
    expect(surface.querySelector("canvas")).toBeNull();
  });
});

describe("WebGPURenderer", () => {
  it("turns a shader the browser rejects into an error naming the compiler's complaint", async () => {
    const device = new FakeDevice();
    device.pipelineFailure = new Error("Invalid RenderPipeline");
    device.compilerMessages = [
      { type: "error", message: "Capability TEXTURE_EXTERNAL is required", lineNum: 191 },
    ];
    devices.push(device);
    await expect(renderer()).rejects.toThrow(/TEXTURE_EXTERNAL is required \(line 191\)/);
    expect(device.destroyed).toBe(true);
  });

  it("asks for the default adapter and refuses a software one", async () => {
    adapter.info.isFallbackAdapter = true;
    await expect(renderer()).rejects.toThrow(/software/);
    const requestAdapter = (navigator as Navigator & { gpu: GPU }).gpu.requestAdapter;
    expect(requestAdapter).toHaveBeenCalledWith();
  });

  it("fails once when a draw's error scope reports a validation error", async () => {
    const device = new FakeDevice();
    devices.push(device);
    const { gpu, on } = await renderer();
    device.scopeErrors = [{ message: "Invalid BindGroup" }, { message: "again" }];
    gpu.draw(frame(RETAGGED), size, viewport);
    gpu.draw(frame(RETAGGED), size, viewport);
    await vi.waitFor(() => expect(on.onFailure).toHaveBeenCalled());
    expect(on.onFailure).toHaveBeenCalledTimes(1);
    expect(on.onFailure.mock.calls[0]![0]).toMatch(/Invalid BindGroup/);
  });

  it("fails on an uncaptured device error too", async () => {
    const device = new FakeDevice();
    devices.push(device);
    const { on } = await renderer();
    device.emitUncapturedError("Out of memory");
    expect(on.onFailure).toHaveBeenCalledTimes(1);
  });

  it("copies frames until a check shows the import matches the copy, then imports them", async () => {
    const device = new FakeDevice();
    device.readbacks = [detailed(), detailed()];
    devices.push(device);
    const { gpu, on } = await renderer();
    gpu.draw(frame(RETAGGED), size, viewport);
    // The first frame goes both ways into the check, then by copy to the screen.
    expect(device.queue.copyExternalImageToTexture).toHaveBeenCalledTimes(2);
    expect(device.importExternalTexture).toHaveBeenCalledTimes(1);
    await settle();
    gpu.draw(frame(RETAGGED), size, viewport);
    expect(device.importExternalTexture).toHaveBeenCalledTimes(2);
    expect(device.queue.copyExternalImageToTexture).toHaveBeenCalledTimes(2);
    expect(device.copiesReleased()).toBe(true);
    expect(on.onFailure).not.toHaveBeenCalled();
  });

  it("keeps copying frames where the import disagrees with the copy, as in WebKit", async () => {
    const device = new FakeDevice();
    // The import of a 10-bit frame comes out far off what the copy holds.
    device.readbacks = [new Uint8Array(256 * 8).fill(200), detailed()];
    devices.push(device);
    const { gpu } = await renderer();
    gpu.draw(frame(RETAGGED), size, viewport);
    await settle();
    gpu.draw(frame(RETAGGED), size, viewport);
    expect(device.queue.copyExternalImageToTexture).toHaveBeenCalledTimes(3);
    expect(device.importExternalTexture).toHaveBeenCalledTimes(1);
    expect(device.copiesReleased()).toBe(false);
  });

  it("decides nothing on a black first frame, and checks a later one", async () => {
    const device = new FakeDevice();
    // A fade from black: both ways come out black even where the import is wrong.
    const black = () => new Uint8Array(256 * 8);
    device.readbacks = [black(), black(), detailed(), detailed()];
    devices.push(device);
    const { gpu } = await renderer();
    gpu.draw(frame(RETAGGED), size, viewport);
    await settle();
    for (let index = 0; index < FRAME_CHECK_RETRY_FRAMES; index++) {
      gpu.draw(frame(RETAGGED), size, viewport);
    }
    // Still copying, and the copy texture kept.
    expect(device.importExternalTexture).toHaveBeenCalledTimes(1);
    expect(device.copiesReleased()).toBe(false);
    gpu.draw(frame(RETAGGED), size, viewport);
    await settle();
    gpu.draw(frame(RETAGGED), size, viewport);
    // The second check saw detail and found the two agreeing.
    expect(device.importExternalTexture).toHaveBeenCalledTimes(3);
    expect(device.copiesReleased()).toBe(true);
  });

  it("imports a frame larger than a texture can be, and checks the frames that fit", async () => {
    const device = new FakeDevice();
    Object.assign(device.limits, { maxTextureDimension2D: 1024 });
    device.readbacks = [detailed(), detailed()];
    devices.push(device);
    const { gpu, on } = await renderer();
    expect(gpu.draw(frame(RETAGGED, 1920, 1080), size, viewport)).toBe(true);
    // Imported, with nothing copied or settled.
    expect(device.queue.copyExternalImageToTexture).not.toHaveBeenCalled();
    expect(device.textures.some((texture) => texture.format === "rgba16float")).toBe(false);
    expect(device.importExternalTexture).toHaveBeenCalledTimes(1);
    // Frames that fit are copied until a later check finds the import right.
    for (let index = 0; index <= FRAME_CHECK_RETRY_FRAMES; index++) {
      gpu.draw(frame(RETAGGED, 960, 540), size, viewport);
    }
    expect(device.queue.copyExternalImageToTexture).toHaveBeenCalled();
    await settle();
    gpu.draw(frame(RETAGGED, 960, 540), size, viewport);
    expect(device.copiesReleased()).toBe(true);
    expect(on.onFailure).not.toHaveBeenCalled();
  });

  it("imports frames where the browser cannot copy them, as in Firefox", async () => {
    const device = new FakeDevice();
    device.copyUnsupported = true;
    devices.push(device);
    const { gpu, on } = await renderer();
    expect(gpu.draw(frame(RETAGGED), size, viewport)).toBe(true);
    gpu.draw(frame(RETAGGED), size, viewport);
    // Tried once, then only imported.
    expect(device.queue.copyExternalImageToTexture).toHaveBeenCalledTimes(1);
    expect(device.importExternalTexture).toHaveBeenCalledTimes(3);
    expect(device.copiesReleased()).toBe(true);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(on.onFailure).not.toHaveBeenCalled();
  });

  it("replaces a lost device with a fresh one and asks for the picture again", async () => {
    const first = new FakeDevice();
    const second = new FakeDevice();
    devices.push(first, second);
    const { gpu, on } = await renderer();
    first.lose();
    await vi.waitFor(() => expect(on.onRestored).toHaveBeenCalledTimes(1));
    expect(on.onFailure).not.toHaveBeenCalled();
    expect(gpuContexts[0]!.configure).toHaveBeenLastCalledWith(
      expect.objectContaining({ device: second }),
    );
    expect(gpu.draw(frame(RETAGGED), size, viewport)).toBe(true);
    expect(framesSent(second)).toBeGreaterThan(0);
    expect(framesSent(first)).toBe(0);
    // Whatever the old device still reports is no longer this renderer's business.
    first.emitUncapturedError("late");
    expect(on.onFailure).not.toHaveBeenCalled();
  });

  it("draws nothing while a lost device is being replaced", async () => {
    const first = new FakeDevice();
    let deliver!: (device: FakeDevice) => void;
    devices.push(first, new Promise((resolve) => (deliver = resolve)));
    const { gpu, on } = await renderer();
    first.lose();
    await vi.waitFor(() => expect(adapter.requestDevice).toHaveBeenCalledTimes(2));
    expect(gpu.draw(frame(RETAGGED), size, viewport)).toBe(false);
    deliver(new FakeDevice());
    await vi.waitFor(() => expect(on.onRestored).toHaveBeenCalled());
  });

  it(`fails over once ${MAX_DEVICE_RECOVERIES} replacements have been lost as well`, async () => {
    const lineup = Array.from({ length: MAX_DEVICE_RECOVERIES + 1 }, () => new FakeDevice());
    devices.push(...lineup);
    const { on } = await renderer();
    for (const [index, device] of lineup.entries()) {
      device.lose();
      if (index < MAX_DEVICE_RECOVERIES) {
        await vi.waitFor(() => expect(on.onRestored).toHaveBeenCalledTimes(index + 1));
      }
    }
    await vi.waitFor(() => expect(on.onFailure).toHaveBeenCalledTimes(1));
    expect(on.onRestored).toHaveBeenCalledTimes(MAX_DEVICE_RECOVERIES);
  });

  it("replaces a device that was lost before it reached the renderer", async () => {
    // Lost while its presenter compiled: the loss arrives before anyone holds it.
    const lostEarly = new FakeDevice();
    lostEarly.lose();
    const fresh = new FakeDevice();
    devices.push(lostEarly, fresh);
    const { gpu, on } = await renderer();
    await vi.waitFor(() => expect(on.onRestored).toHaveBeenCalledTimes(1));
    expect(gpuContexts[0]!.configure).toHaveBeenLastCalledWith(
      expect.objectContaining({ device: fresh }),
    );
    expect(gpu.draw(frame(RETAGGED), size, viewport)).toBe(true);
    expect(framesSent(fresh)).toBeGreaterThan(0);
    expect(on.onFailure).not.toHaveBeenCalled();
  });

  it("counts a replacement that was lost before it was used against the budget", async () => {
    const lineup = Array.from({ length: MAX_DEVICE_RECOVERIES + 1 }, () => new FakeDevice());
    for (const device of lineup.slice(1)) device.lose();
    devices.push(...lineup);
    const { on } = await renderer();
    lineup[0]!.lose();
    await vi.waitFor(() => expect(on.onFailure).toHaveBeenCalledTimes(1));
    expect(adapter.requestDevice).toHaveBeenCalledTimes(MAX_DEVICE_RECOVERIES + 1);
    expect(on.onRestored).not.toHaveBeenCalled();
  });

  it("fails over when a new device is not ready in time, and keeps it for later when it comes", async () => {
    vi.useFakeTimers();
    const first = new FakeDevice();
    const late = new FakeDevice();
    let deliver!: (device: FakeDevice) => void;
    devices.push(first, new Promise((resolve) => (deliver = resolve)));
    const { on } = await renderer();
    first.lose();
    await vi.waitFor(() => expect(adapter.requestDevice).toHaveBeenCalledTimes(2));
    await vi.advanceTimersByTimeAsync(DEVICE_RECOVERY_TIMEOUT_MS);
    expect(on.onFailure).toHaveBeenCalledTimes(1);
    expect(on.onFailure.mock.calls[0]![0]).toMatch(/not ready in time/);
    deliver(late);
    // Compiled and handed back rather than used here.
    await vi.waitFor(() => expect(late.listeners.size).toBe(1));
    expect(on.onRestored).not.toHaveBeenCalled();
    // The device that came too late is sound: the next playback takes it.
    await renderer();
    expect(adapter.requestDevice).toHaveBeenCalledTimes(2);
    expect(gpuContexts[1]!.configure).toHaveBeenCalledWith(
      expect.objectContaining({ device: late }),
    );
  });

  it("hands back a replacement that arrives after the renderer was destroyed or stopped", async () => {
    for (const end of ["destroy", "stop"] as const) {
      const first = new FakeDevice();
      const late = new FakeDevice();
      let deliver!: (device: FakeDevice) => void;
      devices.push(first, new Promise((resolve) => (deliver = resolve)));
      const { gpu, on } = await renderer();
      first.lose();
      await vi.waitFor(() => expect(adapter.requestDevice).toHaveBeenCalled());
      gpu[end]();
      deliver(late);
      await vi.waitFor(() => expect(late.listeners.size).toBe(1));
      // It goes to the next playback rather than to this one.
      const next = await renderer();
      expect(gpuContexts.at(-1)!.configure).toHaveBeenCalledWith(
        expect.objectContaining({ device: late }),
      );
      expect(on.onRestored).not.toHaveBeenCalled();
      expect(on.onFailure).not.toHaveBeenCalled();
      expect(late.destroyed).toBe(false);
      next.gpu.destroy();
      gpu.destroy();
      resetPresenterStateForTests();
      adapter.requestDevice.mockClear();
    }
  });

  it("hands a sound device to the next playback, but not one that reported an error", async () => {
    const device = new FakeDevice();
    devices.push(device);
    const first = await renderer();
    first.gpu.destroy();
    expect(device.destroyed).toBe(false);
    const second = await renderer();
    expect(adapter.requestDevice).toHaveBeenCalledTimes(1);
    expect(gpuContexts[1]!.configure).toHaveBeenCalledWith(expect.objectContaining({ device }));

    device.emitUncapturedError("broken");
    second.gpu.destroy();
    expect(device.destroyed).toBe(true);
  });

  it("does not hand on a device whose error was reported after the engine stopped", async () => {
    const device = new FakeDevice();
    const fresh = new FakeDevice();
    devices.push(device, fresh);
    const { gpu, on } = await renderer();
    let report!: (error: { message: string } | null) => void;
    device.pendingPop = new Promise((resolve) => (report = resolve));
    gpu.draw(frame(RETAGGED), size, viewport);
    gpu.stop();
    gpu.destroy();
    report({ message: "Invalid BindGroup" });
    await device.pendingPop;
    // The next playback gets a fresh device instead.
    await renderer();
    expect(gpuContexts[1]!.configure).toHaveBeenCalledWith(
      expect.objectContaining({ device: fresh }),
    );
    expect(device.destroyed).toBe(true);
    expect(on.onFailure).not.toHaveBeenCalled();
  });

  it("subscribes to a shared device once, however many playbacks use it", async () => {
    const device = new FakeDevice();
    devices.push(device);
    for (let playback = 0; playback < 3; playback++) (await renderer()).gpu.destroy();
    expect(device.lostSubscriptions).toBe(1);
    expect(device.listeners.size).toBe(1);
  });

  it("takes the probe's device as soon as it compiles, without waiting for its test draw", async () => {
    const device = new FakeDevice();
    let report!: (error: { message: string } | null) => void;
    device.pendingPop = new Promise((resolve) => (report = resolve));
    devices.push(device);
    const probe = probePresenter(adapter as unknown as GPUAdapter);
    const { gpu } = await renderer();
    expect(adapter.requestDevice).toHaveBeenCalledTimes(1);
    expect(gpuContexts[0]!.configure).toHaveBeenCalledWith(expect.objectContaining({ device }));
    // The playback connected before the test draw's import, which can stall.
    expect(gpuContexts[0]!.configure.mock.invocationCallOrder[0]).toBeLessThan(
      device.importExternalTexture.mock.invocationCallOrder[0] ?? Infinity,
    );
    // The probe failing now does not pull the device from under the playback.
    report({ message: "late" });
    await expect(probe).resolves.toBe(false);
    expect(device.destroyed).toBe(false);
    // But it is not handed on afterwards.
    gpu.destroy();
    expect(device.destroyed).toBe(true);
  });

  it("withdraws the device of a probe that failed before any playback took it", async () => {
    const tested = new FakeDevice();
    // The compile's own scope comes first, then the test draw's.
    tested.scopeErrors = [null, { message: "Invalid ExternalTexture" }];
    const fresh = new FakeDevice();
    devices.push(tested, fresh);
    await expect(probePresenter(adapter as unknown as GPUAdapter)).resolves.toBe(false);
    expect(tested.destroyed).toBe(true);
    await renderer();
    expect(gpuContexts[0]!.configure).toHaveBeenCalledWith(
      expect.objectContaining({ device: fresh }),
    );
  });

  it("answers the probe with no verdict when the device is lost or cannot be made", async () => {
    await expect(probePresenter(adapter as unknown as GPUAdapter)).resolves.toBeNull();
    const device = new FakeDevice();
    let report!: (error: { message: string } | null) => void;
    device.pendingPop = new Promise((resolve) => (report = resolve));
    devices.push(device);
    const probe = probePresenter(adapter as unknown as GPUAdapter);
    await vi.waitFor(() => expect(adapter.requestDevice).toHaveBeenCalledTimes(2));
    device.lose();
    report(null);
    await expect(probe).resolves.toBeNull();
  });

  it("encodes the source's gamut, matrix and chroma siting for the shader", async () => {
    const device = new FakeDevice();
    devices.push(device);
    const canvas = document.createElement("canvas");
    const gpu = await WebGPURenderer.create(
      canvas,
      "hlg",
      { primaries: "bt709", matrix: "bt709", chromaSiting: "top-left" },
      events(),
    );
    gpu.draw(planes(5, 3, 3, 2), size, viewport);
    const params = device.uniforms[0]!;
    // mode.x: HLG; mode.y: bit depth.
    expect(new Uint32Array(params.buffer).slice(4, 6)).toEqual(new Uint32Array([2, 10]));
    // sizes: luma and chroma.
    expect([...params.slice(12, 16)]).toEqual([5, 3, 3, 2]);
    // chroma: top-left 4:2:0 sits on the first luma sample.
    expect([...params.slice(16, 20)]).toEqual([0, 0, 2, 2]);
    // yuv: BT.709 coefficients; luma: BT.709 weights; gamut: identity.
    expect(params[20]).toBeCloseTo(1.5748, 4);
    expect(params[24]).toBeCloseTo(0.2126, 4);
    expect([...params.slice(28, 31)]).toEqual([1, 0, 0]);
    expect([...params.slice(32, 35)]).toEqual([0, 1, 0]);
  });
});

describe("canvasMapsHDR", () => {
  it("is true where the canvas tone-maps the transfer, as Chrome and Safari do", () => {
    canvasModel = chromiumCanvas;
    expect(canvasMapsHDR("pq")).toBe(true);
    expect(canvasMapsHDR("hlg")).toBe(true);
  });

  it("is false where the canvas shows the raw codes, as Firefox's does", () => {
    canvasModel = (_transfer, code) => raw(code);
    expect(canvasMapsHDR("pq")).toBe(false);
    expect(canvasMapsHDR("hlg")).toBe(false);
  });

  it("is false where the canvas converts PQ without tone mapping, so white nearly clips", () => {
    // A plain conversion with SDR white just above 203 nits: ordered, but no room above white.
    const converted = (code: number) => (code < 0.4 ? 30 : code < 0.65 ? 250 : 255);
    canvasModel = (transfer, code) => (transfer === "pq" ? converted(code) : raw(code));
    expect(canvasMapsHDR("pq")).toBe(false);
  });

  it("judges each transfer on its own", () => {
    canvasModel = (transfer, code) =>
      transfer === "hlg" ? raw(code) : chromiumCanvas(transfer, code);
    expect(canvasMapsHDR("pq")).toBe(true);
    expect(canvasMapsHDR("hlg")).toBe(false);
  });
});

describe("HDRPresenter", () => {
  async function presenter(transfer: "pq" | "hlg" = "pq", canvasMaps = true) {
    canvasModel = canvasMaps ? chromiumCanvas : (_transfer, code) => raw(code);
    const device = new FakeDevice();
    const on = events();
    devices.push(device);
    const surface = document.createElement("div");
    const hdr = await HDRPresenter.create(surface, transfer, DEFAULT_PICTURE_COLOR, on);
    const shown = () =>
      [...surface.querySelectorAll("canvas")].map((canvas) => canvas.style.display);
    return { hdr, surface, device, on, shown };
  }

  it("draws each picture on the layer its route needs, switching both ways", async () => {
    const { hdr, surface, device, shown } = await presenter();

    hdr.draw(planes(), size, viewport);
    expect(device.queue.writeTexture).toHaveBeenCalled();
    expect(shown()).toEqual(["block"]);

    const hardware = frame(HARDWARE_PQ);
    hdr.draw(hardware, size, viewport);
    expect(context2d.drawImage).toHaveBeenLastCalledWith(
      hardware.frame,
      expect.any(Number),
      expect.any(Number),
      expect.any(Number),
      expect.any(Number),
    );
    expect(shown()).toEqual(["none", "block"]);

    hdr.draw(frame(RETAGGED), size, viewport);
    expect(framesSent(device)).toBeGreaterThan(0);
    expect(shown()).toEqual(["block", "none"]);

    hdr.destroy();
    expect(surface.querySelector("canvas")).toBeNull();
  });

  it("sizes a layer from the session's box when it comes back from hiding", async () => {
    const { hdr, surface, shown } = await presenter();
    // Without exact device pixels (Safari), sizes come from CSS pixels; a
    // hidden canvas measures nothing itself.
    Object.defineProperty(surface, "clientWidth", { value: 1280 });
    Object.defineProperty(surface, "clientHeight", { value: 720 });
    hdr.draw(planes(), size, viewport);
    hdr.draw(frame(HARDWARE_PQ), size, viewport);
    hdr.draw(planes(), size, viewport);
    hdr.draw(frame(HARDWARE_PQ), size, viewport);
    expect(shown()).toEqual(["none", "block"]);
    const canvases = [...surface.querySelectorAll("canvas")];
    expect(canvases.map((canvas) => [canvas.width, canvas.height])).toEqual([
      [1280, 720],
      [1280, 720],
    ]);
  });

  it("keeps the last picture up while its device is being replaced", async () => {
    const { hdr, device, on, shown } = await presenter();
    devices.push(new Promise(() => {}));
    hdr.draw(frame(HARDWARE_PQ), size, viewport);
    expect(shown()).toEqual(["none", "block"]);
    device.lose();
    await vi.waitFor(() => expect(adapter.requestDevice).toHaveBeenCalledTimes(2));
    hdr.draw(planes(), size, viewport);
    expect(shown()).toEqual(["none", "block"]);
    expect(on.onFailure).not.toHaveBeenCalled();
  });

  it("refuses a hardware frame where the canvas cannot map its transfer", async () => {
    const { hdr } = await presenter("hlg", false);
    expect(() => hdr.draw(frame({ transfer: "hlg", primaries: "bt2020" }), size, viewport)).toThrow(
      /cannot map/,
    );
  });

  it("refuses a frame whose tag it cannot interpret", async () => {
    const { hdr } = await presenter();
    expect(() => hdr.draw(frame({ transfer: null, primaries: null }), size, viewport)).toThrow(
      /cannot interpret/,
    );
  });
});
