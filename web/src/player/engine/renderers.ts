/// <reference types="@webgpu/types" />
import type { VideoFitMode } from "../types";
import { videoContentRect } from "../utils/videoFit";
import { DEFAULT_SOURCE_PEAK_NITS, SDR_WHITE_NITS } from "./colorMath";
import { PRESENTER_WGSL } from "./hdrShader";
import type { EnginePlaneFrame } from "./protocol";

/** A decoded picture as the presenter receives it. */
export type EnginePicture =
  | { kind: "frame"; frame: VideoFrame; timestamp: number; duration: number }
  | { kind: "planes"; planes: EnginePlaneFrame; timestamp: number; duration: number };

export interface PictureSize {
  /** Display size of the picture, after the container's aspect ratio. */
  width: number;
  height: number;
}

export interface FrameRenderer {
  /** Draws `picture` letterboxed for `fit`. Never takes ownership of it. */
  draw(picture: EnginePicture, size: PictureSize, fit: VideoFitMode): void;
  destroy(): void;
}

function sizeCanvas(
  canvas: HTMLCanvasElement,
  maxDimension = 16384,
): { width: number; height: number } {
  const ratio = window.devicePixelRatio || 1;
  const width = Math.max(1, Math.min(maxDimension, Math.round(canvas.clientWidth * ratio)));
  const height = Math.max(1, Math.min(maxDimension, Math.round(canvas.clientHeight * ratio)));
  if (canvas.width !== width) canvas.width = width;
  if (canvas.height !== height) canvas.height = height;
  return { width, height };
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

  draw(picture: EnginePicture, size: PictureSize, fit: VideoFitMode): void {
    if (picture.kind !== "frame") return;
    const { width, height } = sizeCanvas(this.canvas);
    const rect = videoContentRect(width, height, size.width, size.height, fit);
    this.context.fillStyle = "#000";
    this.context.fillRect(0, 0, width, height);
    this.context.imageSmoothingEnabled = true;
    this.context.imageSmoothingQuality = "high";
    this.context.drawImage(picture.frame, rect.x, rect.y, rect.width, rect.height);
  }

  destroy(): void {
    this.context.clearRect(0, 0, this.canvas.width, this.canvas.height);
  }
}

const UNIFORM_BYTES = 64;

/**
 * Whether the browser must present this picture itself.
 *
 * The decoder is configured with an sRGB tag so imported frames keep their
 * PQ/HLG code values for the engine's own tone map. Hardware decoders
 * (VideoToolbox, D3D11, VA-API) ignore that override and keep the bitstream's
 * PQ/HLG tag. Chromium's WebGPU import of such a frame desaturates it, while
 * its 2D canvas tone-maps it exactly as a <video> element does, so these
 * frames go through Canvas2DRenderer.
 */
export function browserMapsToSDR(picture: EnginePicture): boolean {
  if (picture.kind !== "frame") return false;
  // TypeScript's DOM types predate the "pq" and "hlg" transfer values.
  const transfer: string | null | undefined = picture.frame.colorSpace?.transfer;
  return transfer === "pq" || transfer === "hlg";
}

/**
 * Presents HDR pictures tone-mapped to SDR with WebGPU. Software-decoded
 * high-bit-depth frames arrive as planes and keep their 10/12-bit codes;
 * GPU-backed frames are imported as external textures.
 */
export class WebGPURenderer implements FrameRenderer {
  private planeTextures: { luma: GPUTexture; cb: GPUTexture; cr: GPUTexture; key: string } | null =
    null;
  private planeBindGroup: GPUBindGroup | null = null;
  private lost = false;

  private constructor(
    private readonly canvas: HTMLCanvasElement,
    private readonly device: GPUDevice,
    private readonly context: GPUCanvasContext,
    private readonly externalPipeline: GPURenderPipeline,
    private readonly planesPipeline: GPURenderPipeline,
    private readonly uniforms: GPUBuffer,
    private readonly paramsBindGroup: GPUBindGroup,
    private readonly transfer: 1 | 2,
    onLost: (reason: string) => void,
  ) {
    void device.lost.then((info) => {
      this.lost = true;
      if (info.reason !== "destroyed") onLost(info.message || "The GPU device was lost.");
    });
  }

  static async create(
    canvas: HTMLCanvasElement,
    transfer: "pq" | "hlg",
    onLost: (reason: string) => void,
  ): Promise<WebGPURenderer> {
    const gpu = (navigator as Navigator & { gpu?: GPU }).gpu;
    if (!gpu) throw new Error("WebGPU is unavailable");
    const adapter = await gpu.requestAdapter({ powerPreference: "high-performance" });
    if (!adapter) throw new Error("No WebGPU adapter");
    const device = await adapter.requestDevice();
    const context = canvas.getContext("webgpu");
    if (!context) throw new Error("WebGPU canvas context is unavailable");
    const format = gpu.getPreferredCanvasFormat();
    context.configure({ device, format, alphaMode: "opaque" });

    const module = device.createShaderModule({ code: PRESENTER_WGSL });
    const paramsLayout = device.createBindGroupLayout({
      entries: [
        { binding: 0, visibility: GPUShaderStage.VERTEX | GPUShaderStage.FRAGMENT, buffer: {} },
        { binding: 1, visibility: GPUShaderStage.FRAGMENT, sampler: {} },
      ],
    });
    const externalLayout = device.createBindGroupLayout({
      entries: [{ binding: 0, visibility: GPUShaderStage.FRAGMENT, externalTexture: {} }],
    });
    const planeEntry = (binding: number): GPUBindGroupLayoutEntry => ({
      binding,
      visibility: GPUShaderStage.FRAGMENT,
      texture: { sampleType: "uint" },
    });
    const planesLayout = device.createBindGroupLayout({
      entries: [planeEntry(0), planeEntry(1), planeEntry(2)],
    });
    const pipeline = (layout: GPUBindGroupLayout, entryPoint: string) =>
      device.createRenderPipeline({
        layout: device.createPipelineLayout({ bindGroupLayouts: [paramsLayout, layout] }),
        vertex: { module, entryPoint: "vertexMain" },
        fragment: { module, entryPoint, targets: [{ format }] },
        primitive: { topology: "triangle-list" },
      });
    const uniforms = device.createBuffer({
      size: UNIFORM_BYTES,
      usage: GPUBufferUsage.UNIFORM | GPUBufferUsage.COPY_DST,
    });
    const sampler = device.createSampler({ magFilter: "linear", minFilter: "linear" });
    const paramsBindGroup = device.createBindGroup({
      layout: paramsLayout,
      entries: [
        { binding: 0, resource: { buffer: uniforms } },
        { binding: 1, resource: sampler },
      ],
    });
    return new WebGPURenderer(
      canvas,
      device,
      context,
      pipeline(externalLayout, "fragmentExternal"),
      pipeline(planesLayout, "fragmentPlanes"),
      uniforms,
      paramsBindGroup,
      transfer === "hlg" ? 2 : 1,
      onLost,
    );
  }

  draw(picture: EnginePicture, size: PictureSize, fit: VideoFitMode): void {
    if (this.lost) return;
    const { width, height } = sizeCanvas(this.canvas, this.device.limits.maxTextureDimension2D);
    const rect = videoContentRect(width, height, size.width, size.height, fit);
    const x0 = (rect.x / width) * 2 - 1;
    const x1 = ((rect.x + rect.width) / width) * 2 - 1;
    // Texture v grows downwards while clip-space y grows upwards.
    const y0 = 1 - (rect.y / height) * 2;
    const y1 = 1 - ((rect.y + rect.height) / height) * 2;

    const data = new ArrayBuffer(UNIFORM_BYTES);
    const floats = new Float32Array(data);
    const uints = new Uint32Array(data);
    floats.set([x0, y0, x1, y1], 0);
    uints[4] = this.transfer;
    uints[5] = picture.kind === "planes" ? picture.planes.bitDepth : 0;
    floats.set([DEFAULT_SOURCE_PEAK_NITS, SDR_WHITE_NITS, 0, 0], 8);

    let pictureBindGroup: GPUBindGroup;
    let pipeline: GPURenderPipeline;
    if (picture.kind === "planes") {
      const planes = picture.planes;
      floats.set([planes.width, planes.height, planes.chromaWidth, planes.chromaHeight], 12);
      pictureBindGroup = this.uploadPlanes(planes);
      pipeline = this.planesPipeline;
    } else {
      pictureBindGroup = this.device.createBindGroup({
        layout: this.externalPipeline.getBindGroupLayout(1),
        entries: [
          { binding: 0, resource: this.device.importExternalTexture({ source: picture.frame }) },
        ],
      });
      pipeline = this.externalPipeline;
    }
    this.device.queue.writeBuffer(this.uniforms, 0, data);

    const encoder = this.device.createCommandEncoder();
    const pass = encoder.beginRenderPass({
      colorAttachments: [
        {
          view: this.context.getCurrentTexture().createView(),
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
      this.planeTextures?.luma.destroy();
      this.planeTextures?.cb.destroy();
      this.planeTextures?.cr.destroy();
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
        layout: this.planesPipeline.getBindGroupLayout(1),
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

  destroy(): void {
    this.planeTextures?.luma.destroy();
    this.planeTextures?.cb.destroy();
    this.planeTextures?.cr.destroy();
    this.uniforms.destroy();
    this.context.unconfigure();
    this.device.destroy();
  }
}
