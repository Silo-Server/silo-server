/// <reference types="@webgpu/types" />
import type { VideoFitMode } from "../types";
import { videoContentRect } from "../utils/videoFit";
import {
  chromaOrigin,
  DEFAULT_SOURCE_PEAK_NITS,
  LUMA_WEIGHTS,
  SDR_WHITE_NITS,
  TO_BT709,
  YUV_TO_RGB,
} from "./colorMath";
import { isSoftwareAdapter } from "./gpuAdapter";
import { PRESENTER_WGSL } from "./hdrShader";
import { DEFAULT_PICTURE_COLOR, IMPORT_COLOR_TAG, type PictureColor } from "./pictureColor";
import type { EnginePlaneFrame } from "./protocol";

/** A decoded picture as the presenter receives it. */
export type EnginePicture =
  | { kind: "frame"; frame: VideoFrame; timestamp: number; duration: number }
  | { kind: "planes"; planes: EnginePlaneFrame; timestamp: number; duration: number };

export interface PictureSize {
  /** Display size of the picture, after the container's aspect ratio and rotation. */
  width: number;
  height: number;
  /** Clockwise turn, in degrees, that puts the decoded picture upright. */
  rotation: 0 | 90 | 180 | 270;
  /** Mirror the turned picture horizontally. */
  flip: boolean;
}

/** Where a picture is drawn: the fit mode and, when known, the box's exact device pixels. */
export interface Viewport {
  fit: VideoFitMode;
  devicePixels: { width: number; height: number } | null;
}

export interface FrameRenderer {
  /**
   * Draws `picture` letterboxed for the viewport. Never takes ownership of it.
   * Throws when the picture cannot be presented.
   */
  draw(picture: EnginePicture, size: PictureSize, viewport: Viewport): void;
  /** The engine failed: keep the picture on screen, but start nothing new. */
  stop(): void;
  destroy(): void;
}

function sizeCanvas(
  canvas: HTMLCanvasElement,
  viewport: Viewport,
  maxDimension = 16384,
): { width: number; height: number } {
  // Without the exact figure, CSS pixels times the ratio is off by at most a
  // pixel, which only softens the picture slightly. They come from the box the
  // canvas fills: a layer about to be shown again is still hidden, and so
  // measures nothing itself.
  const ratio = window.devicePixelRatio || 1;
  const exact = viewport.devicePixels;
  const box = canvas.parentElement ?? canvas;
  const width = Math.max(
    1,
    Math.min(maxDimension, exact ? exact.width : Math.round(box.clientWidth * ratio)),
  );
  const height = Math.max(
    1,
    Math.min(maxDimension, exact ? exact.height : Math.round(box.clientHeight * ratio)),
  );
  if (canvas.width !== width) canvas.width = width;
  if (canvas.height !== height) canvas.height = height;
  return { width, height };
}

/** A canvas filling `surface`; each context type needs its own canvas. */
function addLayer(surface: HTMLElement): HTMLCanvasElement {
  const canvas = document.createElement("canvas");
  canvas.style.display = "block";
  canvas.style.width = "100%";
  canvas.style.height = "100%";
  surface.appendChild(canvas);
  return canvas;
}

/**
 * Presents SDR frames with the 2D canvas, which hands the browser's own
 * color-managed, GPU-backed VideoFrame straight to the compositor.
 */
export class Canvas2DRenderer implements FrameRenderer {
  private readonly context: CanvasRenderingContext2D;

  constructor(private readonly canvas: HTMLCanvasElement) {
    const context = canvas.getContext("2d", { alpha: false });
    if (!context) throw new Error("2D canvas is unavailable");
    this.context = context;
  }

  /** A renderer on a new canvas of its own inside `surface`. */
  static in(surface: HTMLElement): Canvas2DRenderer {
    const canvas = addLayer(surface);
    try {
      return new Canvas2DRenderer(canvas);
    } catch (error) {
      canvas.remove();
      throw error;
    }
  }

  get element(): HTMLCanvasElement {
    return this.canvas;
  }

  draw(picture: EnginePicture, size: PictureSize, viewport: Viewport): void {
    if (picture.kind !== "frame") throw new Error("The 2D canvas cannot present uploaded planes.");
    const { width, height } = sizeCanvas(this.canvas, viewport);
    const rect = videoContentRect(width, height, size.width, size.height, viewport.fit);
    this.context.fillStyle = "#000";
    this.context.fillRect(0, 0, width, height);
    this.context.imageSmoothingEnabled = true;
    this.context.imageSmoothingQuality = "high";
    // Draw the decoded picture around the rect's center, turned and then mirrored.
    const sideways = size.rotation % 180 !== 0;
    const drawWidth = sideways ? rect.height : rect.width;
    const drawHeight = sideways ? rect.width : rect.height;
    this.context.translate(rect.x + rect.width / 2, rect.y + rect.height / 2);
    if (size.flip) this.context.scale(-1, 1);
    this.context.rotate((size.rotation * Math.PI) / 180);
    this.context.drawImage(picture.frame, -drawWidth / 2, -drawHeight / 2, drawWidth, drawHeight);
    this.context.resetTransform();
  }

  stop(): void {}

  destroy(): void {
    this.context.clearRect(0, 0, this.canvas.width, this.canvas.height);
    this.canvas.remove();
  }
}

/**
 * Which presentation path a decoded HDR picture needs.
 *
 * - `planes`: 10/12-bit codes uploaded by the worker; the shader converts them.
 * - `frame`: a GPU-backed frame that kept the worker's BT.709/sRGB re-tag, so
 *   importing it applies only the Y'CbCr matrix and leaves the PQ/HLG codes
 *   for the shader.
 * - `browser`: a frame that kept its PQ/HLG tag, because the hardware decoder
 *   ignored the re-tag (VideoToolbox, D3D11, VA-API). Only the browser knows
 *   how to map it: a WebGPU import desaturates it in Chromium, while a 2D
 *   canvas tone-maps it the way a <video> element does, in the browsers whose
 *   canvas does that at all.
 * - `unsupported`: any other tag. Its codes cannot be interpreted safely.
 */
export type PictureRoute = "planes" | "frame" | "browser" | "unsupported";

export function routePicture(picture: EnginePicture): PictureRoute {
  if (picture.kind === "planes") return "planes";
  // TypeScript's DOM types predate the "pq" and "hlg" transfer values.
  const colorSpace = picture.frame.colorSpace as
    | { transfer?: string | null; primaries?: string | null }
    | undefined;
  const transfer = colorSpace?.transfer;
  // Untagged primaries import as BT.709 too, so those frames keep their codes as well.
  const primaries = colorSpace?.primaries ?? IMPORT_COLOR_TAG.primaries;
  if (transfer === IMPORT_COLOR_TAG.transfer && primaries === IMPORT_COLOR_TAG.primaries) {
    return "frame";
  }
  if (transfer === "pq" || transfer === "hlg") return "browser";
  return "unsupported";
}

interface PresenterPipelines {
  /** The render target format the pipelines draw into. */
  format: GPUTextureFormat;
  external: GPURenderPipeline;
  copied: GPURenderPipeline;
  planes: GPURenderPipeline;
  params: GPUBindGroupLayout;
  externalPicture: GPUBindGroupLayout;
  copiedPicture: GPUBindGroupLayout;
  planePictures: GPUBindGroupLayout;
}

/** The first compiler error, which says more than "the pipeline is invalid". */
async function compilerError(module: GPUShaderModule): Promise<string | null> {
  try {
    const info = await module.getCompilationInfo();
    const error = info.messages.find((message) => message.type === "error");
    return error ? `${error.message} (line ${error.lineNum})` : null;
  } catch {
    return null;
  }
}

/**
 * Compiles the presenter for `format`. WebGPU reports these failures through
 * error scopes and rejected promises rather than by throwing, so both are
 * collected here and turned into one exception.
 */
async function createPresenterPipelines(
  device: GPUDevice,
  format: GPUTextureFormat,
): Promise<PresenterPipelines> {
  device.pushErrorScope("validation");
  const module = device.createShaderModule({ code: PRESENTER_WGSL });
  const params = device.createBindGroupLayout({
    entries: [
      { binding: 0, visibility: GPUShaderStage.VERTEX | GPUShaderStage.FRAGMENT, buffer: {} },
      { binding: 1, visibility: GPUShaderStage.FRAGMENT, sampler: {} },
    ],
  });
  const externalPicture = device.createBindGroupLayout({
    entries: [{ binding: 0, visibility: GPUShaderStage.FRAGMENT, externalTexture: {} }],
  });
  const copiedPicture = device.createBindGroupLayout({
    entries: [
      { binding: 0, visibility: GPUShaderStage.FRAGMENT, texture: { sampleType: "float" } },
    ],
  });
  const planeEntry = (binding: number): GPUBindGroupLayoutEntry => ({
    binding,
    visibility: GPUShaderStage.FRAGMENT,
    texture: { sampleType: "uint" },
  });
  const planePictures = device.createBindGroupLayout({
    entries: [planeEntry(0), planeEntry(1), planeEntry(2)],
  });
  const pipeline = (layout: GPUBindGroupLayout, entryPoint: string) =>
    device.createRenderPipelineAsync({
      layout: device.createPipelineLayout({ bindGroupLayouts: [params, layout] }),
      vertex: { module, entryPoint: "vertexMain" },
      fragment: { module, entryPoint, targets: [{ format }] },
      primitive: { topology: "triangle-list" },
    });
  let pipelines: [GPURenderPipeline, GPURenderPipeline, GPURenderPipeline] | null = null;
  let failure: unknown = null;
  try {
    pipelines = await Promise.all([
      pipeline(externalPicture, "fragmentExternal"),
      pipeline(copiedPicture, "fragmentCopied"),
      pipeline(planePictures, "fragmentPlanes"),
    ]);
  } catch (error) {
    failure = error;
  }
  if (!failure && pipelines) {
    // Every object above feeds the pipelines, so their success means the
    // scope is clean. Its answer can take a second on some drivers; not waited for.
    void device.popErrorScope().catch(() => {});
    return {
      format,
      external: pipelines[0],
      copied: pipelines[1],
      planes: pipelines[2],
      params,
      externalPicture,
      copiedPicture,
      planePictures,
    };
  }
  const scoped = await device.popErrorScope().catch(() => null);
  const reason =
    (await compilerError(module)) ??
    scoped?.message ??
    (failure instanceof Error ? failure.message : String(failure));
  throw new Error(`The HDR presenter could not be built: ${reason}`);
}

/**
 * A fresh device for the presenter. The default adapter is the one the
 * browser composites with; asking for the high-performance one can wake a
 * discrete GPU and move every frame between GPUs.
 */
async function requestPresenterDevice(): Promise<GPUDevice> {
  const gpu = (navigator as Navigator & { gpu?: GPU }).gpu;
  if (!gpu) throw new Error("WebGPU is unavailable");
  const adapter = await gpu.requestAdapter();
  if (!adapter) throw new Error("No WebGPU adapter");
  if (isSoftwareAdapter(adapter)) throw new Error("Only a software WebGPU adapter is available");
  return adapter.requestDevice();
}

function preferredCanvasFormat(): GPUTextureFormat {
  return (navigator as Navigator & { gpu: GPU }).gpu.getPreferredCanvasFormat();
}

/** The renderer drawing with a device, told when the device fails. */
interface DeviceHolder {
  deviceLost(info: GPUDeviceLostInfo): void;
  deviceError(message: string): void;
}

/**
 * A device with the presenter compiled for one canvas format. It subscribes
 * to its loss and uncaptured errors once and forwards them to whichever
 * renderer holds it, so a renderer that lets go leaves nothing behind on it.
 * Both are also kept, for a renderer that takes the device after they came.
 */
interface PresenterDevice {
  device: GPUDevice;
  pipelines: PresenterPipelines;
  format: GPUTextureFormat;
  lost: GPUDeviceLostInfo | null;
  /** The first error it reported; such a device is not handed to another playback. */
  error: string | null;
  holder: DeviceHolder | null;
}

function adopt(
  device: GPUDevice,
  pipelines: PresenterPipelines,
  format: GPUTextureFormat,
): PresenterDevice {
  const entry: PresenterDevice = {
    device,
    pipelines,
    format,
    lost: null,
    error: null,
    holder: null,
  };
  void device.lost.then((info) => {
    entry.lost = info;
    if (spare === entry) spare = null;
    entry.holder?.deviceLost(info);
  });
  device.addEventListener("uncapturederror", (event) => {
    const message = (event as GPUUncapturedErrorEvent).error.message;
    entry.error ??= message;
    entry.holder?.deviceError(message);
  });
  return entry;
}

/**
 * The page keeps one presenter device. Creating a device and compiling the
 * presenter takes milliseconds on some systems and several seconds on others
 * (Linux Vulkan drivers), so the capability probe offers its device as soon
 * as the presenter compiles, and each playback hands it on when it ends,
 * unless it was lost or reported an error.
 */
let spare: PresenterDevice | null = null;
/** The probe while it builds its device; a playback starting meanwhile waits for that device. */
let probing: { done: Promise<void>; deadline: number } | null = null;
let spareWaiters: Array<() => void> = [];
/** How long after the probe starts a playback still waits for its device. */
const PROBE_WAIT_MS = 10_000;

function wakeSpareWaiters(): void {
  const waiters = spareWaiters;
  spareWaiters = [];
  for (const wake of waiters) wake();
}

/** Hands a device to the page for the next playback, or releases it. */
function offer(entry: PresenterDevice): void {
  entry.holder = null;
  if (entry.lost || entry.error !== null) {
    entry.device.destroy();
    return;
  }
  if (spare && spare !== entry) spare.device.destroy();
  spare = entry;
  wakeSpareWaiters();
}

/**
 * The page's device for `format`, if it has one. While the probe is still
 * building its device, waits for it rather than building a second one, up
 * to a deadline counted from the probe's start, so a probe that hangs delays
 * playback only once.
 */
async function takeSpare(format: GPUTextureFormat): Promise<PresenterDevice | null> {
  const pending = probing;
  if (!spare && pending && performance.now() < pending.deadline) {
    let wake!: () => void;
    let timer: ReturnType<typeof setTimeout> | undefined;
    await Promise.race([
      new Promise<void>((resolve) => {
        wake = resolve;
        spareWaiters.push(resolve);
      }),
      pending.done,
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, pending.deadline - performance.now());
      }),
    ]);
    clearTimeout(timer);
    spareWaiters = spareWaiters.filter((waiter) => waiter !== wake);
  }
  const entry = spare;
  spare = null;
  if (!entry) return null;
  // An error can be reported after its playback handed the device back.
  if (entry.lost || entry.error !== null || entry.format !== format) {
    entry.device.destroy();
    return null;
  }
  return entry;
}

const UNIFORM_FLOATS = 40;

/** How a GPU-backed frame reaches the shader. */
type FrameTransfer = "import" | "copy";

type FramePicture = Extract<EnginePicture, { kind: "frame" }>;

/** Side, in pixels, of the square the two ways of drawing a frame are compared at. */
const FRAME_CHECK_SIZE = 8;
/**
 * Mean difference, in 8-bit levels, above which the two ways disagree. Both
 * apply the same Y'CbCr matrix, so where the import is right they differ by
 * a level or two of filtering. WebKit's misread 10-bit frames differ by far more.
 */
const FRAME_CHECK_TOLERANCE = 6;
/**
 * Spread, in 8-bit levels, the copied check picture needs before the check
 * counts. A dark or flat frame (a fade from black, a title card) comes out
 * alike both ways even where the import is wrong.
 */
const FRAME_CHECK_MIN_SPREAD = 32;
/** Frames drawn, by copy, before a check that saw too little is tried again. */
export const FRAME_CHECK_RETRY_FRAMES = 30;

/**
 * Draws pictures with a compiled presenter into any render target: the
 * canvas while playing, a small texture while probing.
 */
class GPUPresenter {
  private readonly data = new ArrayBuffer(UNIFORM_FLOATS * 4);
  private readonly floats = new Float32Array(this.data);
  private readonly uints = new Uint32Array(this.data);
  private readonly uniforms: GPUBuffer;
  private readonly paramsBindGroup: GPUBindGroup;
  private planeTextures: { luma: GPUTexture; cb: GPUTexture; cr: GPUTexture; key: string } | null =
    null;
  private planeBindGroup: GPUBindGroup | null = null;
  /**
   * How frames reach the shader. Importing costs nothing, but WebKit imports
   * 10-bit hardware-decoded frames wrongly, so a frame is drawn both ways and
   * the results compared (`checkFrameImport`). Until there is an answer, and
   * wherever the two disagree, frames are copied.
   */
  private frameTransfer: FrameTransfer | null = null;
  private frameChecking = false;
  /** Frames to draw before the next check; Infinity once checking gave up. */
  private framesUntilCheck = 0;
  private copied: { texture: GPUTexture; bindGroup: GPUBindGroup } | null = null;
  private destroyed = false;

  constructor(
    private readonly device: GPUDevice,
    private readonly pipelines: PresenterPipelines,
    transfer: "pq" | "hlg",
    private readonly color: PictureColor,
  ) {
    this.uniforms = device.createBuffer({
      size: this.data.byteLength,
      usage: GPUBufferUsage.UNIFORM | GPUBufferUsage.COPY_DST,
    });
    const sampler = device.createSampler({ magFilter: "linear", minFilter: "linear" });
    this.paramsBindGroup = device.createBindGroup({
      layout: pipelines.params,
      entries: [
        { binding: 0, resource: { buffer: this.uniforms } },
        { binding: 1, resource: sampler },
      ],
    });
    // Everything about the source that does not change from picture to picture.
    this.uints[4] = transfer === "hlg" ? 2 : 1;
    this.floats.set([DEFAULT_SOURCE_PEAK_NITS, SDR_WHITE_NITS, 0, 0], 8);
    this.floats.set(YUV_TO_RGB[color.matrix], 20);
    this.floats.set([...LUMA_WEIGHTS[color.primaries], 0], 24);
    const gamut = TO_BT709[color.primaries];
    for (let row = 0; row < 3; row++) {
      this.floats.set([gamut[row * 3]!, gamut[row * 3 + 1]!, gamut[row * 3 + 2]!, 0], 28 + row * 4);
    }
  }

  /** Draws `picture`; a frame goes the way `transfer` says, or the way found to work. */
  draw(
    picture: EnginePicture,
    size: PictureSize,
    fit: VideoFitMode,
    target: GPUTextureView,
    width: number,
    height: number,
    transfer?: FrameTransfer,
  ): void {
    // Before this draw sets its own uniforms: the check draws with its own.
    if (picture.kind === "frame" && !transfer && !this.frameTransfer && !this.frameChecking) {
      if (this.framesUntilCheck > 0) {
        this.framesUntilCheck -= 1;
      } else {
        this.frameChecking = true;
        void this.checkFrameImport(picture, size);
      }
    }
    const rect = videoContentRect(width, height, size.width, size.height, fit);
    const x0 = (rect.x / width) * 2 - 1;
    const x1 = ((rect.x + rect.width) / width) * 2 - 1;
    // Texture v grows downwards while clip-space y grows upwards.
    const y0 = 1 - (rect.y / height) * 2;
    const y1 = 1 - ((rect.y + rect.height) / height) * 2;
    this.floats.set([x0, y0, x1, y1], 0);
    this.uints[5] = picture.kind === "planes" ? picture.planes.bitDepth : 0;
    this.uints[6] = size.rotation / 90;
    this.uints[7] = size.flip ? 1 : 0;

    let pictureBindGroup: GPUBindGroup;
    let pipeline: GPURenderPipeline;
    if (picture.kind === "planes") {
      const planes = picture.planes;
      const subsampleX = planes.chromaWidth < planes.width ? 2 : 1;
      const subsampleY = planes.chromaHeight < planes.height ? 2 : 1;
      this.floats.set([planes.width, planes.height, planes.chromaWidth, planes.chromaHeight], 12);
      this.floats.set(
        [...chromaOrigin(this.color.chromaSiting, subsampleX, subsampleY), subsampleX, subsampleY],
        16,
      );
      pictureBindGroup = this.uploadPlanes(planes);
      pipeline = this.pipelines.planes;
    } else {
      this.floats.set([0, 0, 0, 0, 0, 0, 1, 1], 12);
      const copied =
        (transfer ?? this.frameTransfer ?? "copy") === "copy"
          ? this.copyFrame(picture.frame)
          : null;
      if (copied) {
        pictureBindGroup = copied;
        pipeline = this.pipelines.copied;
      } else {
        pictureBindGroup = this.device.createBindGroup({
          layout: this.pipelines.externalPicture,
          entries: [
            { binding: 0, resource: this.device.importExternalTexture({ source: picture.frame }) },
          ],
        });
        pipeline = this.pipelines.external;
      }
    }
    this.device.queue.writeBuffer(this.uniforms, 0, this.data);

    const encoder = this.device.createCommandEncoder();
    const pass = encoder.beginRenderPass({
      colorAttachments: [
        {
          view: target,
          loadOp: "clear",
          storeOp: "store",
          clearValue: { r: 0, g: 0, b: 0, a: 1 },
        },
      ],
    });
    pass.setPipeline(pipeline);
    pass.setBindGroup(0, this.paramsBindGroup);
    pass.setBindGroup(1, pictureBindGroup);
    pass.draw(6);
    pass.end();
    this.device.queue.submit([encoder.finish()]);
  }

  private uploadPlanes(planes: EnginePlaneFrame): GPUBindGroup {
    const key = `${planes.width}x${planes.height}:${planes.chromaWidth}x${planes.chromaHeight}`;
    if (!this.planeTextures || this.planeTextures.key !== key) {
      this.destroyPlaneTextures();
      const texture = (width: number, height: number) =>
        this.device.createTexture({
          size: [width, height],
          format: "r16uint",
          usage: GPUTextureUsage.TEXTURE_BINDING | GPUTextureUsage.COPY_DST,
        });
      this.planeTextures = {
        luma: texture(planes.width, planes.height),
        cb: texture(planes.chromaWidth, planes.chromaHeight),
        cr: texture(planes.chromaWidth, planes.chromaHeight),
        key,
      };
      this.planeBindGroup = this.device.createBindGroup({
        layout: this.pipelines.planePictures,
        entries: [
          { binding: 0, resource: this.planeTextures.luma.createView() },
          { binding: 1, resource: this.planeTextures.cb.createView() },
          { binding: 2, resource: this.planeTextures.cr.createView() },
        ],
      });
    }
    const textures = this.planeTextures;
    const write = (texture: GPUTexture, plane: 0 | 1 | 2, width: number, height: number) =>
      this.device.queue.writeTexture(
        { texture },
        planes.buffer,
        { offset: planes.offsets[plane], bytesPerRow: planes.strides[plane], rowsPerImage: height },
        [width, height],
      );
    write(textures.luma, 0, planes.width, planes.height);
    write(textures.cb, 1, planes.chromaWidth, planes.chromaHeight);
    write(textures.cr, 2, planes.chromaWidth, planes.chromaHeight);
    return this.planeBindGroup!;
  }

  /** Whether `frame` fits in a texture on this device, so that it can be copied. */
  private fitsTexture(frame: VideoFrame): boolean {
    const limit = this.device.limits.maxTextureDimension2D;
    return frame.displayWidth <= limit && frame.displayHeight <= limit;
  }

  /**
   * Copies `frame` into the presenter's half-float texture, keeping its
   * R'G'B' codes. Null where the frame is larger than a texture can be (that
   * frame is imported), or the browser cannot copy a VideoFrame at all
   * (Firefox), which leaves importing as the only way.
   */
  private copyFrame(frame: VideoFrame): GPUBindGroup | null {
    if (!this.fitsTexture(frame)) return null;
    const width = frame.displayWidth;
    const height = frame.displayHeight;
    if (
      !this.copied ||
      this.copied.texture.width !== width ||
      this.copied.texture.height !== height
    ) {
      this.copied?.texture.destroy();
      const texture = this.device.createTexture({
        size: [width, height],
        format: "rgba16float",
        usage:
          GPUTextureUsage.TEXTURE_BINDING |
          GPUTextureUsage.COPY_DST |
          GPUTextureUsage.RENDER_ATTACHMENT,
      });
      this.copied = {
        texture,
        bindGroup: this.device.createBindGroup({
          layout: this.pipelines.copiedPicture,
          entries: [{ binding: 0, resource: texture.createView() }],
        }),
      };
    }
    try {
      this.device.queue.copyExternalImageToTexture(
        { source: frame },
        { texture: this.copied.texture, colorSpace: "srgb" },
        [width, height],
      );
    } catch (error) {
      if (!(error instanceof TypeError)) throw error;
      this.useImport();
      return null;
    }
    return this.copied.bindGroup;
  }

  /** Imports frames from now on, and lets go of the copy's texture. */
  private useImport(): void {
    this.frameTransfer = "import";
    this.copied?.texture.destroy();
    this.copied = null;
  }

  /**
   * Draws `picture` both ways into small textures and compares them: frames
   * are imported from then on where the two agree, and copied where not. A
   * picture with too little in it decides nothing; a later frame is tried.
   */
  private async checkFrameImport(picture: FramePicture, size: PictureSize): Promise<void> {
    if (!this.fitsTexture(picture.frame)) {
      // A frame that cannot be copied cannot be compared.
      this.framesUntilCheck = FRAME_CHECK_RETRY_FRAMES;
      this.frameChecking = false;
      return;
    }
    const side = FRAME_CHECK_SIZE;
    const target = () =>
      this.device.createTexture({
        size: [side, side],
        format: this.pipelines.format,
        usage: GPUTextureUsage.RENDER_ATTACHMENT | GPUTextureUsage.COPY_SRC,
      });
    const imported = target();
    const copied = target();
    try {
      this.draw(picture, size, "cover", copied.createView(), side, side, "copy");
      // The browser cannot copy frames: importing is the only way.
      if (this.frameTransfer === "import") return;
      this.draw(picture, size, "cover", imported.createView(), side, side, "import");
      const [a, b] = await Promise.all([this.readBack(imported), this.readBack(copied)]);
      let difference = 0;
      let low = 255;
      let high = 0;
      for (let pixel = 0; pixel < side * side; pixel++) {
        const offset = Math.floor(pixel / side) * 256 + (pixel % side) * 4;
        for (let channel = 0; channel < 3; channel++) {
          const value = b[offset + channel]!;
          difference += Math.abs(a[offset + channel]! - value);
          low = Math.min(low, value);
          high = Math.max(high, value);
        }
      }
      if (this.destroyed) return;
      if (high - low < FRAME_CHECK_MIN_SPREAD) {
        this.framesUntilCheck = FRAME_CHECK_RETRY_FRAMES;
      } else if (difference / (side * side * 3) > FRAME_CHECK_TOLERANCE) {
        this.frameTransfer = "copy";
      } else {
        this.useImport();
      }
    } catch {
      // Without an answer frames stay copied, which is right everywhere it works.
      this.framesUntilCheck = Infinity;
    } finally {
      this.frameChecking = false;
      imported.destroy();
      copied.destroy();
    }
  }

  /** The 8-bit pixels of a small render target, rows 256 bytes apart. */
  private async readBack(texture: GPUTexture): Promise<Uint8Array> {
    const bytesPerRow = 256;
    const buffer = this.device.createBuffer({
      size: bytesPerRow * texture.height,
      usage: GPUBufferUsage.COPY_DST | GPUBufferUsage.MAP_READ,
    });
    const encoder = this.device.createCommandEncoder();
    encoder.copyTextureToBuffer({ texture }, { buffer, bytesPerRow }, [
      texture.width,
      texture.height,
    ]);
    this.device.queue.submit([encoder.finish()]);
    try {
      await buffer.mapAsync(GPUMapMode.READ);
      return new Uint8Array(buffer.getMappedRange().slice(0));
    } finally {
      buffer.destroy();
    }
  }

  private destroyPlaneTextures(): void {
    this.planeTextures?.luma.destroy();
    this.planeTextures?.cb.destroy();
    this.planeTextures?.cr.destroy();
    this.planeTextures = null;
    this.planeBindGroup = null;
  }

  destroy(): void {
    this.destroyed = true;
    this.destroyPlaneTextures();
    this.copied?.texture.destroy();
    this.copied = null;
    this.uniforms.destroy();
  }
}

/**
 * Whether `adapter` can present HDR: a device, the presenter compiled, and
 * one picture drawn through each path without a validation error. Answers
 * null when that says nothing about the presenter: the device was lost or
 * could not be created (a GPU process restarting, say).
 */
export function probePresenter(adapter: GPUAdapter): Promise<boolean | null> {
  const run = testPresenter(adapter);
  const done = run.then(
    () => {},
    () => {},
  );
  probing = { done, deadline: performance.now() + PROBE_WAIT_MS };
  void done.then(() => {
    if (probing?.done === done) probing = null;
    wakeSpareWaiters();
  });
  return run;
}

async function testPresenter(adapter: GPUAdapter): Promise<boolean | null> {
  const format = preferredCanvasFormat();
  let device: GPUDevice;
  try {
    device = await adapter.requestDevice();
  } catch {
    return null;
  }
  let lost = false;
  void device.lost.then(() => (lost = true));
  let pipelines: PresenterPipelines;
  try {
    pipelines = await createPresenterPipelines(device, format);
  } catch {
    // Lost while compiling says nothing; anything else is a broken presenter.
    device.destroy();
    return lost ? null : false;
  }
  // Offered at once: the test draw below can take seconds to report on some
  // drivers, and a playback that takes the device meanwhile catches the same
  // errors in its own draws.
  const entry = adopt(device, pipelines, format);
  offer(entry);
  // A playback waiting for the device connects first: importing even this
  // small frame can hold the main thread for a second on some drivers.
  await new Promise((resolve) => setTimeout(resolve, 0));
  const presenter = new GPUPresenter(device, pipelines, "pq", DEFAULT_PICTURE_COLOR);
  const target = device.createTexture({
    size: [4, 4],
    format,
    usage: GPUTextureUsage.RENDER_ATTACHMENT,
  });
  let frame: VideoFrame | null = null;
  let threw = false;
  let error: GPUError | null = null;
  device.pushErrorScope("validation");
  try {
    const size: PictureSize = { width: 2, height: 2, rotation: 0, flip: false };
    // A 2x2 I420 frame tagged the way the worker tags HDR frames for import.
    frame = new VideoFrame(new Uint8Array([64, 64, 64, 64, 128, 128]), {
      format: "I420",
      codedWidth: 2,
      codedHeight: 2,
      timestamp: 0,
      colorSpace: { ...IMPORT_COLOR_TAG, matrix: "bt709", fullRange: false },
    });
    const planes: EnginePlaneFrame = {
      timestamp: 0,
      duration: 0,
      bitDepth: 10,
      width: 2,
      height: 2,
      chromaWidth: 1,
      chromaHeight: 1,
      buffer: new Uint16Array([64, 64, 64, 64, 512, 512]).buffer,
      offsets: [0, 8, 10],
      strides: [4, 2, 2],
    };
    const view = target.createView();
    presenter.draw(
      { kind: "frame", frame, timestamp: 0, duration: 0 },
      size,
      "contain",
      view,
      4,
      4,
    );
    presenter.draw(
      { kind: "planes", planes, timestamp: 0, duration: 0 },
      size,
      "contain",
      view,
      4,
      4,
    );
  } catch {
    threw = true;
  } finally {
    error = await device.popErrorScope().catch(() => null);
    frame?.close();
    presenter.destroy();
    target.destroy();
  }
  if (entry.lost) return null;
  if (threw || error) {
    entry.error ??= error?.message ?? "The presenter's test draw failed.";
    // Withdraw it, unless a playback has already taken it.
    if (spare === entry) {
      spare = null;
      device.destroy();
    }
    return false;
  }
  return true;
}

/** How long a lost device may take to come back before playback fails over. */
export const DEVICE_RECOVERY_TIMEOUT_MS = 4_000;
/** Device losses one session recovers from before failing over. */
export const MAX_DEVICE_RECOVERIES = 2;

export interface PresenterEvents {
  /** The picture can no longer be presented; the engine should fail over. */
  onFailure: (message: string) => void;
  /** A lost device was replaced; the picture on screen needs drawing again. */
  onRestored: () => void;
}

/**
 * Presents HDR pictures tone-mapped to SDR with WebGPU on a canvas.
 *
 * Validation errors arrive asynchronously, so every draw runs in an error
 * scope whose result reports a failure once; uncaptured errors (allocation
 * failures among them) are a second net. A lost device is replaced in place
 * with a fresh one, within a deadline and a per-session budget.
 */
export class WebGPURenderer implements FrameRenderer, DeviceHolder {
  private entry: PresenterDevice | null = null;
  private presenter: GPUPresenter | null = null;
  /** Bumped whenever a device replacement in flight stops being wanted. */
  private generation = 0;
  private recoveries = 0;
  /** The presenter itself failed and reported it. */
  private failed = false;
  /** The engine stopped (for whatever reason): nothing new starts. */
  private halted = false;
  private disposed = false;

  private constructor(
    private readonly canvas: HTMLCanvasElement,
    private readonly context: GPUCanvasContext,
    private readonly format: GPUTextureFormat,
    private readonly transfer: "pq" | "hlg",
    private readonly color: PictureColor,
    private readonly events: PresenterEvents,
    entry: PresenterDevice,
  ) {
    this.hold(entry);
  }

  static async create(
    canvas: HTMLCanvasElement,
    transfer: "pq" | "hlg",
    color: PictureColor,
    events: PresenterEvents,
  ): Promise<WebGPURenderer> {
    const context = canvas.getContext("webgpu");
    if (!context) throw new Error("WebGPU canvas context is unavailable");
    const format = preferredCanvasFormat();
    const entry = await WebGPURenderer.connect(context, format, await takeSpare(format));
    return new WebGPURenderer(canvas, context, format, transfer, color, events, entry);
  }

  /**
   * Configures the canvas with `kept`, or with a fresh device. A connection
   * that is no longer wanted when it completes hands its device back.
   */
  private static async connect(
    context: GPUCanvasContext,
    format: GPUTextureFormat,
    kept: PresenterDevice | null,
    isCurrent: () => boolean = () => true,
  ): Promise<PresenterDevice> {
    let entry = kept;
    if (!entry) {
      const device = await requestPresenterDevice();
      try {
        entry = adopt(device, await createPresenterPipelines(device, format), format);
      } catch (error) {
        device.destroy();
        throw error;
      }
    }
    if (!isCurrent()) {
      offer(entry);
      throw new Error("superseded");
    }
    context.configure({ device: entry.device, format, alphaMode: "opaque" });
    return entry;
  }

  get element(): HTMLCanvasElement {
    return this.canvas;
  }

  deviceError(message: string): void {
    this.reportFailure(`Drawing a frame failed: ${message}`);
  }

  private reportFailure(message: string): void {
    if (this.disposed || this.failed || this.halted) return;
    this.failed = true;
    this.events.onFailure(message);
  }

  deviceLost(info: GPUDeviceLostInfo): void {
    if (this.disposed || this.failed || this.halted) return;
    // destroy() is ours: the renderer released or replaced the device.
    if (info.reason === "destroyed") return;
    this.release();
    const reason = info.message || "The GPU device was lost.";
    if (this.recoveries >= MAX_DEVICE_RECOVERIES) {
      this.reportFailure(reason);
      return;
    }
    this.recoveries += 1;
    this.generation += 1;
    void this.recover(this.generation, reason);
  }

  /**
   * Draws with `entry` from now on. It can be lost or report an error
   * between being built or handed over and this point, with nobody holding
   * it to hear, so that is handled here as if it had just happened.
   */
  private hold(entry: PresenterDevice): void {
    this.entry = entry;
    this.presenter = new GPUPresenter(entry.device, entry.pipelines, this.transfer, this.color);
    entry.holder = this;
    if (entry.lost) this.deviceLost(entry.lost);
    else if (entry.error !== null) this.deviceError(entry.error);
  }

  /** Lets go of the current device: lost or failed ones are destroyed, sound ones kept. */
  private release(): void {
    this.presenter?.destroy();
    this.presenter = null;
    if (this.entry) offer(this.entry);
    this.entry = null;
  }

  private async recover(generation: number, reason: string): Promise<void> {
    const isCurrent = () =>
      !this.disposed && !this.failed && !this.halted && generation === this.generation;
    const deadline = setTimeout(() => {
      if (!isCurrent()) return;
      // Whatever the rebuild produces from here on is handed back.
      this.generation += 1;
      this.reportFailure(`${reason} A new GPU device was not ready in time.`);
    }, DEVICE_RECOVERY_TIMEOUT_MS);
    try {
      // A fresh device: whatever took this one down probably took the
      // page's spare with it.
      const entry = await WebGPURenderer.connect(this.context, this.format, null, isCurrent);
      clearTimeout(deadline);
      if (!isCurrent()) {
        offer(entry);
        return;
      }
      this.hold(entry);
      // Unless the new device was itself already lost or failed.
      if (this.entry === entry && !this.failed) this.events.onRestored();
    } catch (error) {
      clearTimeout(deadline);
      if (!isCurrent()) return;
      this.reportFailure(`${reason} It could not be replaced: ${String(error)}`);
    }
  }

  /** Draws `picture`; false when there is nothing to draw with (a device being replaced). */
  draw(picture: EnginePicture, size: PictureSize, viewport: Viewport): boolean {
    const entry = this.entry;
    const presenter = this.presenter;
    if (!entry || !presenter || this.failed || this.halted) return false;
    const { device } = entry;
    const { width, height } = sizeCanvas(
      this.canvas,
      viewport,
      device.limits.maxTextureDimension2D,
    );
    device.pushErrorScope("validation");
    try {
      presenter.draw(
        picture,
        size,
        viewport.fit,
        this.context.getCurrentTexture().createView(),
        width,
        height,
      );
    } finally {
      void device.popErrorScope().then(
        (error) => {
          if (!error) return;
          entry.error ??= error.message;
          if (entry === this.entry) this.deviceError(error.message);
        },
        // A lost device rejects the pop; the loss itself is handled above.
        () => {},
      );
    }
    return true;
  }

  stop(): void {
    this.halted = true;
    // A device replacement still in flight finds itself unwanted and hands
    // its device back.
    this.generation += 1;
  }

  destroy(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.generation += 1;
    this.context.unconfigure();
    this.canvas.remove();
    this.release();
  }
}

/** The green value a 2D canvas draws for a 16x16 grey I420 frame of `code` with this tag. */
function canvasGreen(
  context: CanvasRenderingContext2D,
  code: number,
  colorSpace: VideoColorSpaceInit,
): number {
  const size = 16;
  const data = new Uint8Array((size * size * 3) / 2);
  data.fill(Math.round(16 + 219 * code), 0, size * size);
  data.fill(128, size * size);
  const frame = new VideoFrame(data, {
    format: "I420",
    codedWidth: size,
    codedHeight: size,
    timestamp: 0,
    colorSpace,
  });
  try {
    context.drawImage(frame, 0, 0);
    return context.getImageData(size / 2, size / 2, 1, 1).data[1]!;
  } finally {
    frame.close();
  }
}

/** Codes probed per transfer: a dark one, reference white, and a highlight. */
const CANVAS_PROBE_CODES = {
  // About 5, 203 and 1000 nits.
  pq: [0.3, 0.58, 0.75],
  hlg: [0.3, 0.75, 0.9],
} as const;

const canvasMapping: Partial<Record<"pq" | "hlg", boolean>> = {};

/**
 * Whether the 2D canvas tone-maps frames of this transfer the way a <video>
 * element does (Chromium and Safari do; Firefox draws the raw codes). The dark
 * code must not come out as if it were sRGB, reference white must stay below
 * clipping (a conversion without tone mapping clips it), and the three codes
 * must keep their order. Measured once per page and transfer, on a canvas set
 * up like the presenter's own, and only when a session first needs it: each
 * draw reads pixels back, which takes a second on some Linux GPU stacks.
 */
export function canvasMapsHDR(transfer: "pq" | "hlg"): boolean {
  return (canvasMapping[transfer] ??= measureCanvasMapping(transfer));
}

function measureCanvasMapping(transfer: "pq" | "hlg"): boolean {
  try {
    const canvas = document.createElement("canvas");
    canvas.width = 16;
    canvas.height = 16;
    const context = canvas.getContext("2d", { alpha: false });
    if (!context || typeof VideoFrame === "undefined") return false;
    const tag = {
      primaries: "bt2020" as VideoColorPrimaries,
      transfer: transfer as VideoTransferCharacteristics,
      matrix: "bt2020-ncl" as VideoMatrixCoefficients,
      fullRange: false,
    };
    const codes = CANVAS_PROBE_CODES[transfer];
    const [dark, white, highlight] = codes.map((code) => canvasGreen(context, code, tag));
    const darkAsSRGB = canvasGreen(context, codes[0], {
      primaries: "bt709",
      transfer: "iec61966-2-1",
      matrix: "bt709",
      fullRange: false,
    });
    return (
      Math.abs(dark! - darkAsSRGB) > 8 && white! < 245 && dark! < white! && white! < highlight!
    );
  } catch {
    return false;
  }
}

export function resetPresenterStateForTests(): void {
  delete canvasMapping.pq;
  delete canvasMapping.hlg;
  spare?.device.destroy();
  spare = null;
  probing = null;
  spareWaiters = [];
}

/**
 * Presents an HDR session. Each picture goes to the path its kind and color
 * tag call for (see `routePicture`): the WebGPU tone map, or the browser's 2D
 * canvas for frames only the browser can map. The two draw on separate
 * canvases because a canvas keeps the first context type it hands out; the
 * one that drew last is shown.
 */
export class HDRPresenter implements FrameRenderer {
  private canvasLayer: Canvas2DRenderer | null = null;

  private constructor(
    private readonly surface: HTMLElement,
    private readonly gpu: WebGPURenderer,
    private readonly transfer: "pq" | "hlg",
  ) {}

  static async create(
    surface: HTMLElement,
    transfer: "pq" | "hlg",
    color: PictureColor,
    events: PresenterEvents,
  ): Promise<HDRPresenter> {
    const canvas = addLayer(surface);
    try {
      const gpu = await WebGPURenderer.create(canvas, transfer, color, events);
      return new HDRPresenter(surface, gpu, transfer);
    } catch (error) {
      canvas.remove();
      throw error;
    }
  }

  draw(picture: EnginePicture, size: PictureSize, viewport: Viewport): void {
    switch (routePicture(picture)) {
      case "planes":
      case "frame":
        // While a lost device is being replaced, the last picture stays up.
        if (this.gpu.draw(picture, size, viewport)) this.show(this.gpu.element);
        return;
      case "browser": {
        if (!canvasMapsHDR(this.transfer)) {
          throw new Error(
            "This browser cannot map a hardware-decoded HDR frame for an SDR screen.",
          );
        }
        this.canvasLayer ??= Canvas2DRenderer.in(this.surface);
        this.canvasLayer.draw(picture, size, viewport);
        this.show(this.canvasLayer.element);
        return;
      }
      case "unsupported": {
        const colorSpace = (picture as { frame: VideoFrame }).frame.colorSpace;
        throw new Error(
          `A decoded HDR frame has a color tag the engine cannot interpret ` +
            `(transfer ${colorSpace?.transfer ?? "none"}, primaries ${colorSpace?.primaries ?? "none"}).`,
        );
      }
    }
  }

  stop(): void {
    this.gpu.stop();
  }

  private show(canvas: HTMLCanvasElement): void {
    for (const layer of [this.gpu.element, this.canvasLayer?.element]) {
      if (layer) layer.style.display = layer === canvas ? "block" : "none";
    }
  }

  destroy(): void {
    this.gpu.destroy();
    this.canvasLayer?.destroy();
    this.canvasLayer = null;
  }
}
