import type { CSSProperties } from "react";

// ─── Types ──────────────────────────────────────────────────────────────────

export interface SubtitleAppearance {
  fontSize: "small" | "medium" | "large" | "xlarge" | "xxlarge";
  fontFamily: "sans-serif" | "serif" | "monospace";
  fontColor: string;
  backgroundColor: string;
  backgroundStyle: "box" | "shadow" | "outline" | "none";
  backgroundOpacity: number;
  textOutline: boolean;
  textOutlineColor: string;
  position: "bottom" | "lower-third" | "top";
}

// ─── Defaults ───────────────────────────────────────────────────────────────

export const DEFAULT_SUBTITLE_APPEARANCE: SubtitleAppearance = {
  fontSize: "large",
  fontFamily: "sans-serif",
  fontColor: "#ffffff",
  backgroundColor: "#000000",
  backgroundStyle: "shadow",
  backgroundOpacity: 75,
  textOutline: false,
  textOutlineColor: "#000000",
  position: "bottom",
};

// ─── Option Arrays ──────────────────────────────────────────────────────────

export const FONT_SIZE_OPTIONS = [
  { value: "small" as const, label: "Small" },
  { value: "medium" as const, label: "Medium" },
  { value: "large" as const, label: "Large" },
  { value: "xlarge" as const, label: "X-Large" },
  { value: "xxlarge" as const, label: "XX-Large" },
];

export const FONT_FAMILY_OPTIONS = [
  { value: "sans-serif" as const, label: "Sans-serif" },
  { value: "serif" as const, label: "Serif" },
  { value: "monospace" as const, label: "Monospace" },
];

export const BACKGROUND_STYLE_OPTIONS = [
  { value: "box" as const, label: "Box" },
  { value: "shadow" as const, label: "Drop Shadow" },
  { value: "outline" as const, label: "Outline" },
  { value: "none" as const, label: "None" },
];

export const POSITION_OPTIONS = [
  { value: "bottom" as const, label: "Bottom" },
  { value: "lower-third" as const, label: "Lower Third" },
  { value: "top" as const, label: "Top" },
];

// ─── Color Palettes ─────────────────────────────────────────────────────────

interface ColorSwatch {
  hex: string;
  label: string;
}

export const FONT_COLOR_PALETTE: ColorSwatch[] = [
  { hex: "#ffffff", label: "White" },
  { hex: "#facc15", label: "Yellow" },
  { hex: "#22c55e", label: "Green" },
  { hex: "#06b6d4", label: "Cyan" },
  { hex: "#d946ef", label: "Magenta" },
  { hex: "#ef4444", label: "Red" },
  { hex: "#3b82f6", label: "Blue" },
  { hex: "#000000", label: "Black" },
];

export const BG_COLOR_PALETTE: ColorSwatch[] = [
  { hex: "#000000", label: "Black" },
  { hex: "#374151", label: "Dark Gray" },
  { hex: "#1e3a5f", label: "Navy" },
  { hex: "#7f1d1d", label: "Dark Red" },
  { hex: "#14532d", label: "Dark Green" },
];

// ─── Parser ─────────────────────────────────────────────────────────────────

const VALID_FONT_SIZES: Set<string> = new Set(FONT_SIZE_OPTIONS.map((o) => o.value));
const VALID_FONT_FAMILIES: Set<string> = new Set(FONT_FAMILY_OPTIONS.map((o) => o.value));
const VALID_BG_STYLES: Set<string> = new Set(BACKGROUND_STYLE_OPTIONS.map((o) => o.value));
const VALID_POSITIONS: Set<string> = new Set(POSITION_OPTIONS.map((o) => o.value));

export function parseSubtitleAppearance(json: string | null): SubtitleAppearance {
  if (!json) return { ...DEFAULT_SUBTITLE_APPEARANCE };
  try {
    const p = JSON.parse(json) as Record<string, unknown>;
    return {
      fontSize: VALID_FONT_SIZES.has(p.fontSize as string)
        ? (p.fontSize as SubtitleAppearance["fontSize"])
        : DEFAULT_SUBTITLE_APPEARANCE.fontSize,
      fontFamily: VALID_FONT_FAMILIES.has(p.fontFamily as string)
        ? (p.fontFamily as SubtitleAppearance["fontFamily"])
        : DEFAULT_SUBTITLE_APPEARANCE.fontFamily,
      fontColor:
        typeof p.fontColor === "string" && /^#[0-9a-fA-F]{6}$/.test(p.fontColor)
          ? p.fontColor
          : DEFAULT_SUBTITLE_APPEARANCE.fontColor,
      backgroundColor:
        typeof p.backgroundColor === "string" && /^#[0-9a-fA-F]{6}$/.test(p.backgroundColor)
          ? p.backgroundColor
          : DEFAULT_SUBTITLE_APPEARANCE.backgroundColor,
      backgroundStyle: VALID_BG_STYLES.has(p.backgroundStyle as string)
        ? (p.backgroundStyle as SubtitleAppearance["backgroundStyle"])
        : DEFAULT_SUBTITLE_APPEARANCE.backgroundStyle,
      backgroundOpacity:
        typeof p.backgroundOpacity === "number" &&
        p.backgroundOpacity >= 0 &&
        p.backgroundOpacity <= 100
          ? p.backgroundOpacity
          : DEFAULT_SUBTITLE_APPEARANCE.backgroundOpacity,
      textOutline:
        typeof p.textOutline === "boolean"
          ? p.textOutline
          : DEFAULT_SUBTITLE_APPEARANCE.textOutline,
      textOutlineColor:
        typeof p.textOutlineColor === "string" && /^#[0-9a-fA-F]{6}$/.test(p.textOutlineColor)
          ? p.textOutlineColor
          : DEFAULT_SUBTITLE_APPEARANCE.textOutlineColor,
      position: VALID_POSITIONS.has(p.position as string)
        ? (p.position as SubtitleAppearance["position"])
        : DEFAULT_SUBTITLE_APPEARANCE.position,
    };
  } catch {
    return { ...DEFAULT_SUBTITLE_APPEARANCE };
  }
}

// ─── Style Computation ──────────────────────────────────────────────────────

// Base cue font sizes in px at the 16:9 reference frame height below. The
// player scales these proportionally with the rendered video so subtitles
// keep the same relative size as the window grows or shrinks.
const FONT_SIZE_MAP: Record<SubtitleAppearance["fontSize"], number> = {
  small: 20,
  medium: 26,
  large: 32,
  xlarge: 40,
  xxlarge: 48,
};

/** Reference frame height (px) at which FONT_SIZE_MAP values apply as-is. */
export const SUBTITLE_REFERENCE_HEIGHT = 720;

/** Floor so cues stay legible in very small windows. */
const MIN_SUBTITLE_FONT_PX = 12;

export function computeSubtitleFontSize(
  fontSize: SubtitleAppearance["fontSize"],
  fontScale = 1,
): string {
  const px = Math.max(MIN_SUBTITLE_FONT_PX, Math.round(FONT_SIZE_MAP[fontSize] * fontScale));
  return `${px}px`;
}

function hexToRgb(hex: string): { r: number; g: number; b: number } {
  const clean = hex.replace("#", "");
  return {
    r: parseInt(clean.substring(0, 2), 16),
    g: parseInt(clean.substring(2, 4), 16),
    b: parseInt(clean.substring(4, 6), 16),
  };
}

function buildTextShadow(settings: SubtitleAppearance): string | undefined {
  const shadows: string[] = [];
  const outlineColor = settings.textOutlineColor;

  if (settings.backgroundStyle === "shadow") {
    shadows.push("2px 2px 4px rgba(0,0,0,0.9)");
  }

  if (settings.backgroundStyle === "outline") {
    // 1px cardinal + 2px diagonal for a visible, rounded outline
    const offsets = [
      [-1, 0],
      [1, 0],
      [0, -1],
      [0, 1],
      [-2, -2],
      [-2, 2],
      [2, -2],
      [2, 2],
    ];
    for (const [x, y] of offsets) {
      shadows.push(`${x}px ${y}px 0 ${outlineColor}`);
    }
  }

  if (settings.textOutline) {
    shadows.push(`0 0 3px ${outlineColor}`, `0 0 3px ${outlineColor}`);
  }

  return shadows.length > 0 ? shadows.join(", ") : undefined;
}

export interface SubtitleStyles {
  containerStyle: CSSProperties;
  cueStyle: CSSProperties;
}

export function computeSubtitleStyles(settings: SubtitleAppearance, fontScale = 1): SubtitleStyles {
  const containerStyle: CSSProperties = computePositionStyle(settings.position);
  const cueStyle: CSSProperties = {};

  // Font
  cueStyle.fontSize = computeSubtitleFontSize(settings.fontSize, fontScale);
  cueStyle.fontFamily = settings.fontFamily;
  cueStyle.color = settings.fontColor;

  // Background
  if (settings.backgroundStyle === "box") {
    const { r, g, b } = hexToRgb(settings.backgroundColor);
    cueStyle.backgroundColor = `rgba(${r}, ${g}, ${b}, ${settings.backgroundOpacity / 100})`;
  }

  // Text shadow (handles shadow, outline, and textOutline — concatenated)
  const textShadow = buildTextShadow(settings);
  if (textShadow) {
    cueStyle.textShadow = textShadow;
  }

  return { containerStyle, cueStyle };
}

// ─── Position (aspect-aware) ────────────────────────────────────────────────

// Position offsets as a fraction of their anchor height. "Bottom" uses the
// player window; "Lower Third" and "Top" use the 16:9 video reference frame.
const POSITION_OFFSETS: Record<SubtitleAppearance["position"], number> = {
  bottom: 0.07,
  "lower-third": 0.12,
  top: 0.07,
};

/**
 * Percentage-of-container fallback used before the video's intrinsic aspect
 * ratio is known (or in the preview pane where there's no real video).
 */
function computePositionStyle(position: SubtitleAppearance["position"]): CSSProperties {
  if (position === "top") return { top: "8%", bottom: "auto" };
  if (position === "lower-third") return { bottom: "12%" };
  return { bottom: "7%" };
}

/**
 * Height (px) of a 16:9 reference frame centered on the actually-rendered
 * video area (object-fit: contain), or null before measurements are known.
 * The frame matches the shorter dimension of the video so it never contracts
 * inside it; for wider-than-16:9 content it extends into the letterbox.
 */
function resolveSubtitleReferenceHeight(
  playerWidth: number,
  playerHeight: number,
  videoAspect: number,
): number | null {
  if (!Number.isFinite(videoAspect) || videoAspect <= 0 || playerWidth <= 0 || playerHeight <= 0) {
    return null;
  }

  // Rendered video dimensions inside the player (object-fit: contain).
  const playerAspect = playerWidth / playerHeight;
  const videoHeight = playerAspect > videoAspect ? playerHeight : playerWidth / videoAspect;
  const videoWidth = playerAspect > videoAspect ? playerHeight * videoAspect : playerWidth;

  return videoAspect >= 16 / 9 ? videoWidth * (9 / 16) : videoHeight;
}

/**
 * Font scale factor for the rendered video size: 1 at the 720px reference
 * height, growing/shrinking proportionally with the window so subtitles keep
 * the same size relative to the video. Falls back to 1 until measured.
 */
export function computeSubtitleFontScale(
  playerWidth: number,
  playerHeight: number,
  videoAspect: number,
): number {
  const refHeight = resolveSubtitleReferenceHeight(playerWidth, playerHeight, videoAspect);
  return refHeight === null ? 1 : refHeight / SUBTITLE_REFERENCE_HEIGHT;
}

/**
 * Aspect-aware positioning. "Bottom" is anchored to the player window so it
 * can use the available letterbox space. "Lower Third" and "Top" are anchored
 * to a 16:9 reference frame centered on the actually-rendered video area
 * (object-fit: contain), keeping those positions attached to the video frame
 * regardless of whether content is 16:9, 4:3, or 2.35:1.
 */
export function computeSubtitlePositionStyle(
  position: SubtitleAppearance["position"],
  playerWidth: number,
  playerHeight: number,
  videoAspect: number,
): CSSProperties {
  if (position === "bottom") {
    if (playerHeight <= 0) return computePositionStyle(position);
    return { bottom: `${POSITION_OFFSETS.bottom * playerHeight}px` };
  }

  const refHeight = resolveSubtitleReferenceHeight(playerWidth, playerHeight, videoAspect);
  if (refHeight === null) {
    return computePositionStyle(position);
  }

  // Reference frame is centered on the video, which is itself centered in
  // the player container — so the reference is centered in the player too.
  const refBottomFromPlayerBottom = (playerHeight - refHeight) / 2;
  const offset = POSITION_OFFSETS[position] * refHeight;

  if (position === "top") {
    return { top: `${refBottomFromPlayerBottom + offset}px`, bottom: "auto" };
  }
  return { bottom: `${refBottomFromPlayerBottom + offset}px` };
}
