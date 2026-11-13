/**
 * Copies high-bit-depth software frames out of the decode worker as planes,
 * reusing the buffers the page hands back.
 */
import type { EnginePlaneFrame } from "./protocol";

/** Buffers the page returned after drawing, for the next copies. */
export const planePool: ArrayBuffer[] = [];

/** Software frame formats copied out as planes. */
export const RAW_PLANE_FORMATS = /^I4(20|22|44)P1[02]$/;

function planeBuffer(size: number): ArrayBuffer {
  const index = planePool.findIndex((buffer) => buffer.byteLength >= size);
  if (index >= 0) return planePool.splice(index, 1)[0]!;
  return new ArrayBuffer(size);
}

/**
 * Copies a high-bit-depth software frame out as planes. GPU paths quantize an
 * imported frame to 8 bits; HDR needs the 10-bit codes for clean gradients.
 */
export async function copyPlanes(frame: VideoFrame): Promise<EnginePlaneFrame> {
  const format = frame.format!;
  const rect = frame.visibleRect!;
  const bitDepth = format.endsWith("P12") ? 12 : 10;
  const chromaWidth = format.startsWith("I444") ? rect.width : Math.ceil(rect.width / 2);
  const chromaHeight = format.startsWith("I420") ? Math.ceil(rect.height / 2) : rect.height;
  const lumaBytes = rect.width * rect.height * 2;
  const chromaBytes = chromaWidth * chromaHeight * 2;
  const buffer = planeBuffer(lumaBytes + chromaBytes * 2);
  const offsets: [number, number, number] = [0, lumaBytes, lumaBytes + chromaBytes];
  const strides: [number, number, number] = [rect.width * 2, chromaWidth * 2, chromaWidth * 2];
  try {
    await frame.copyTo(buffer, {
      rect,
      layout: [
        { offset: offsets[0], stride: strides[0] },
        { offset: offsets[1], stride: strides[1] },
        { offset: offsets[2], stride: strides[2] },
      ],
    });
  } catch (error) {
    // The caller falls back to handing the frame over; keep the buffer for the next copy.
    planePool.push(buffer);
    throw error;
  }
  return {
    timestamp: frame.timestamp / 1e6,
    duration: (frame.duration ?? 0) / 1e6,
    bitDepth,
    width: rect.width,
    height: rect.height,
    chromaWidth,
    chromaHeight,
    buffer,
    offsets,
    strides,
  };
}
