import type { SubtitleAppearance } from "../../lib/subtitleAppearance";

// Geometry and placement math for PGS (bitmap) subtitle cues, mirroring the
// tvOS/iOS implementation (silo-apple AVPlayerBackend.bitmapCuePlacement) so
// bitmap subtitles honor the same appearance settings on every platform.
//
// Only the geometric/compositing preferences apply to bitmaps: size (scale),
// vertical position preset, and the background box. Font family, text color,
// and outline are baked into the source pixels and deliberately ignored.

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface PgsCuePlacement {
  /** Source rect on the decoded PGS frame, in source-canvas pixels. */
  src: Rect;
  /** Destination rect on the display canvas, in display pixels. */
  dest: Rect;
  /** Backing box behind the cue (only when backgroundStyle is "box"). */
  background: { rect: Rect; cornerRadius: number } | null;
}

// Authored PGS bitmaps typically render larger than the text subtitle track
// at the same preset; shrink the whole ladder so the default preset visually
// matches the text render. Mirrors silo-apple's authoredSizeCompensation.
const AUTHORED_SIZE_COMPENSATION = 0.85;

// Scale ladder derived from the text overlay's font-size map relative to the
// "large" default (see FONT_SIZE_MAP in lib/subtitleAppearance.ts).
const FONT_SIZE_SCALE: Record<SubtitleAppearance["fontSize"], number> = {
  small: 0.625,
  medium: 0.8,
  large: 1,
  xlarge: 1.25,
  xxlarge: 1.5,
};

// Cues whose authored bottom edge sits in this lower band are "dialogue" and
// follow the position preset; cues above it (floating signs, top captions)
// keep their authored placement. Mirrors bitmapBottomAnchorBand.
const BOTTOM_ANCHOR_BAND = 0.75;

// Position offsets as fractions of the 16:9 reference frame height — the
// same anchors the text overlay uses (POSITION_OFFSETS in
// lib/subtitleAppearance.ts), so PGS dialogue lands where SRT text does.
const POSITION_OFFSET: Record<SubtitleAppearance["position"], number> = {
  bottom: 0.07,
  "lower-third": 0.18,
  top: 0.07,
};

/**
 * The 16:9 reference frame the text overlay anchors to (see
 * computeSubtitlePositionStyle in lib/subtitleAppearance.ts): matched to the
 * video's shorter dimension and centered on it, so for wider-than-16:9
 * content the frame extends into the letterbox — subtitles landing there is
 * the intended, text-consistent behavior.
 */
function referenceFrame(videoRect: Rect): { top: number; bottom: number; height: number } {
  const videoAspect = videoRect.width / videoRect.height;
  const height = videoAspect >= 16 / 9 ? videoRect.width * (9 / 16) : videoRect.height;
  const centerY = videoRect.y + videoRect.height / 2;
  return { top: centerY - height / 2, bottom: centerY + height / 2, height };
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

/**
 * Detects disjoint cue regions on a decoded PGS frame from its alpha channel.
 *
 * `data` is RGBA pixel data (typically a downsampled copy of the frame).
 * Rows containing visible pixels are grouped into vertical bands; gaps
 * smaller than `mergeGap` (line spacing inside a multi-line cue) keep rows in
 * the same band, larger gaps (dialogue vs. a floating sign) split them. Each
 * band is then trimmed horizontally to the tight pixel bounds.
 */
export function detectCueRegions(
  data: Uint8ClampedArray,
  width: number,
  height: number,
  alphaThreshold = 16,
): Rect[] {
  // Row projection: which rows contain any visible pixel.
  const rowHasInk = new Array<boolean>(height).fill(false);
  for (let y = 0; y < height; y++) {
    const rowStart = y * width * 4;
    for (let x = 0; x < width; x++) {
      if (data[rowStart + x * 4 + 3]! > alphaThreshold) {
        rowHasInk[y] = true;
        break;
      }
    }
  }

  // Group rows into bands, merging across small inter-line gaps.
  const mergeGap = Math.max(2, Math.round(height * 0.06));
  const bands: Array<{ top: number; bottom: number }> = [];
  let bandStart = -1;
  let lastInk = -1;
  for (let y = 0; y < height; y++) {
    if (!rowHasInk[y]) continue;
    if (bandStart < 0) {
      bandStart = y;
    } else if (y - lastInk > mergeGap) {
      bands.push({ top: bandStart, bottom: lastInk });
      bandStart = y;
    }
    lastInk = y;
  }
  if (bandStart >= 0) {
    bands.push({ top: bandStart, bottom: lastInk });
  }

  // Tight horizontal bounds per band.
  const regions: Rect[] = [];
  for (const band of bands) {
    let minX = width;
    let maxX = -1;
    for (let y = band.top; y <= band.bottom; y++) {
      const rowStart = y * width * 4;
      for (let x = 0; x < minX; x++) {
        if (data[rowStart + x * 4 + 3]! > alphaThreshold) {
          minX = x;
          break;
        }
      }
      for (let x = width - 1; x > maxX; x--) {
        if (data[rowStart + x * 4 + 3]! > alphaThreshold) {
          maxX = x;
          break;
        }
      }
    }
    if (maxX >= minX) {
      regions.push({
        x: minX,
        y: band.top,
        width: maxX - minX + 1,
        height: band.bottom - band.top + 1,
      });
    }
  }
  return regions;
}

/**
 * Computes display placements for detected cue regions, applying the user's
 * size, vertical-position, and background-box preferences. Port of
 * silo-apple's bitmapCuePlacement/bitmapBottomAnchorShift.
 *
 * @param regions Cue regions in source-frame pixels.
 * @param sourceWidth/sourceHeight Decoded PGS frame dimensions.
 * @param videoRect The rendered video content box on the display canvas.
 * @param appearance The shared subtitle appearance settings.
 */
export function computePgsPlacements(
  regions: Rect[],
  sourceWidth: number,
  sourceHeight: number,
  videoRect: Rect,
  appearance: SubtitleAppearance,
): PgsCuePlacement[] {
  if (sourceWidth <= 0 || sourceHeight <= 0 || videoRect.width <= 0 || videoRect.height <= 0) {
    return [];
  }

  const scale = AUTHORED_SIZE_COMPENSATION * FONT_SIZE_SCALE[appearance.fontSize];
  const sx = videoRect.width / sourceWidth;
  const sy = videoRect.height / sourceHeight;

  // Base frames: authored region rects mapped onto the displayed video box.
  const baseFrames = regions.map((r) => ({
    x: videoRect.x + r.x * sx,
    y: videoRect.y + r.y * sy,
    width: r.width * sx,
    height: r.height * sy,
    normMidY: (r.y + r.height / 2) / sourceHeight,
    normMaxY: (r.y + r.height) / sourceHeight,
  }));

  // Bottom preset: one group-wide shift that moves the authored bottom edge
  // of the dialogue band to the text overlay's bottom anchor, computed over
  // the whole group so multi-cue compositions keep authored spacing.
  const ref = referenceFrame(videoRect);
  let bottomShift = 0;
  if (appearance.position === "bottom") {
    const band = baseFrames.filter((f) => f.normMaxY >= BOTTOM_ANCHOR_BAND);
    if (band.length > 0) {
      const groupMaxY = Math.max(...band.map((f) => f.y + f.height));
      const target = ref.bottom - ref.height * POSITION_OFFSET.bottom;
      bottomShift = target - groupMaxY;
    }
  }

  return baseFrames.map((frame, i) => {
    const region = regions[i]!;

    // Size: scale around a regional anchor — dialogue in the lower half
    // grows upward from its bottom edge, top/mid signs grow downward. Cap
    // the scale so a full-width cue can never outgrow the video box.
    const effectiveScale = Math.min(
      scale,
      frame.width > 0 ? videoRect.width / frame.width : scale,
      frame.height > 0 ? videoRect.height / frame.height : scale,
    );
    const scaledWidth = frame.width * effectiveScale;
    const scaledHeight = frame.height * effectiveScale;
    const x = frame.x + frame.width / 2 - scaledWidth / 2;
    let y = frame.normMidY >= 0.5 ? frame.y + frame.height - scaledHeight : frame.y;

    // Vertical position preset — dialogue-band cues only; floating signs
    // keep their authored placement.
    if (frame.normMaxY >= BOTTOM_ANCHOR_BAND) {
      switch (appearance.position) {
        case "bottom":
          y += bottomShift;
          break;
        case "lower-third":
          y = ref.bottom - ref.height * POSITION_OFFSET["lower-third"] - scaledHeight;
          break;
        case "top":
          y = ref.top + ref.height * POSITION_OFFSET.top;
          break;
      }
    }

    // Clamp horizontally to the video box; vertically to the reference frame
    // union, which may extend into the letterbox — same as the text overlay.
    const minY = Math.min(videoRect.y, ref.top);
    const maxY = Math.max(videoRect.y + videoRect.height, ref.bottom);
    const dest: Rect = {
      x: clamp(x, videoRect.x, videoRect.x + videoRect.width - scaledWidth),
      y: clamp(y, minY, maxY - scaledHeight),
      width: scaledWidth,
      height: scaledHeight,
    };

    let background: PgsCuePlacement["background"] = null;
    if (appearance.backgroundStyle === "box") {
      const padV = clamp(scaledHeight * 0.16, 6, 22);
      const padH = clamp(scaledHeight * 0.24, 8, 30);
      background = {
        rect: {
          x: dest.x - padH,
          y: dest.y - padV,
          width: dest.width + padH * 2,
          height: dest.height + padV * 2,
        },
        cornerRadius: Math.min(8, padV * 0.5),
      };
    }

    return { src: region, dest, background };
  });
}

/**
 * Computes the rendered video content box inside the display canvas for
 * object-fit: contain, in display-canvas pixels.
 */
export function computeVideoRect(
  canvasWidth: number,
  canvasHeight: number,
  videoAspect: number,
): Rect {
  if (!Number.isFinite(videoAspect) || videoAspect <= 0 || canvasWidth <= 0 || canvasHeight <= 0) {
    return { x: 0, y: 0, width: canvasWidth, height: canvasHeight };
  }
  const canvasAspect = canvasWidth / canvasHeight;
  if (canvasAspect > videoAspect) {
    const width = canvasHeight * videoAspect;
    return { x: (canvasWidth - width) / 2, y: 0, width, height: canvasHeight };
  }
  const height = canvasWidth / videoAspect;
  return { x: 0, y: (canvasHeight - height) / 2, width: canvasWidth, height };
}
