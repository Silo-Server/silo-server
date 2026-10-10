import type { VideoFitMode } from "../types";

export interface FitRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

/**
 * Where a picture of `videoWidth`x`videoHeight` sits inside a box for a fit
 * mode, matching CSS `object-fit: contain` and `cover`. Falls back to the
 * whole box before the picture size is known.
 */
export function videoContentRect(
  boxWidth: number,
  boxHeight: number,
  videoWidth: number,
  videoHeight: number,
  fit: VideoFitMode,
): FitRect {
  if (boxWidth <= 0 || boxHeight <= 0 || videoWidth <= 0 || videoHeight <= 0) {
    return { x: 0, y: 0, width: boxWidth, height: boxHeight };
  }
  const scale =
    fit === "cover"
      ? Math.max(boxWidth / videoWidth, boxHeight / videoHeight)
      : Math.min(boxWidth / videoWidth, boxHeight / videoHeight);
  const width = videoWidth * scale;
  const height = videoHeight * scale;
  return { x: (boxWidth - width) / 2, y: (boxHeight - height) / 2, width, height };
}
