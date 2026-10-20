import type {
  CardOverlayPrefs,
  OverlayFontWeight,
  OverlayPreset,
  OverlayShadow,
  OverlayShape,
  OverlayStyle,
  WatchStatusMode,
} from "./types";

const FONT_WEIGHTS: Record<OverlayFontWeight, number> = {
  medium: 500,
  semibold: 600,
  bold: 700,
  black: 900,
};

const SHAPE_RADIUS: Record<OverlayShape, number | "full"> = {
  pill: "full",
  rounded: 6,
  square: 2,
};

// The shadow a style's shadow switch adds to a preset that has none.
const STYLE_SHADOW: OverlayShadow = { x: 0, y: 2, blur: 6, spread: 0, color: "rgb(0 0 0 / 0.35)" };

// A preset with the viewer's style overrides applied. The result is another
// preset, so the renderer and its card-relative scaling stay unaware of where
// a value came from. Per-badge accents keep working: a custom background is
// tinted by the accent in presets that fill with it, and a custom text or
// border colour yields to the accent in presets that paint the accent there.
export function applyOverlayStyle(
  preset: OverlayPreset,
  style: OverlayStyle | undefined,
): OverlayPreset {
  if (!style) return preset;
  const scale = style.size ?? 1;
  const next: OverlayPreset = {
    ...preset,
    fontSize: preset.fontSize * scale,
    paddingInline: preset.paddingInline * scale,
    paddingBlock: preset.paddingBlock * scale,
    iconSize: preset.iconSize * scale,
    iconGap: preset.iconGap * scale,
    stackGap: preset.stackGap * scale,
    fontWeight: style.fontWeight ? FONT_WEIGHTS[style.fontWeight] : preset.fontWeight,
    uppercase: style.uppercase ?? preset.uppercase,
  };
  if (style.shape) {
    next.borderRadius = SHAPE_RADIUS[style.shape];
    next.borderRadiusVariable = undefined;
  }
  if (style.borderWidth !== undefined) next.borderWidth = style.borderWidth;
  if (style.shadow === false) next.boxShadow = undefined;
  if (style.shadow === true) next.boxShadow = preset.boxShadow ?? STYLE_SHADOW;

  const { background, textColor, borderColor } = style;
  if (background || textColor || borderColor || style.borderWidth !== undefined) {
    const strategy = preset.accentStrategy;
    next.badgeStyle = (accent) => {
      const css = { ...preset.badgeStyle(accent) };
      if (background) {
        css.background =
          accent && strategy === "bg"
            ? `color-mix(in srgb, ${accent} 30%, ${background})`
            : background;
      }
      if (textColor && !(accent && strategy === "text")) css.color = textColor;
      if (borderColor && !(accent && strategy === "border")) css.borderColor = borderColor;
      if (style.borderWidth !== undefined && style.borderWidth > 0) css.borderStyle = "solid";
      return css;
    };
  }
  return next;
}

export function watchStatusOf(prefs: CardOverlayPrefs | null | undefined): WatchStatusMode {
  return prefs?.watchStatus ?? "remaining";
}

// Where the server serves an uploaded overlay image. The ref is
// content-addressed, so the URL never changes meaning and caches forever.
export function overlayImageUrl(ref: string): string {
  return `/api/v2/overlay-images/${ref}`;
}

// Black or white, whichever reads better on a #RRGGBB fill. Used for the
// rating source chips the plate preset draws in the accent colour.
export function readableTextOn(hex: string): "#000000" | "#ffffff" {
  const match = /^#([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})/.exec(hex);
  if (!match) return "#ffffff";
  const linear = (part: string | undefined) => {
    const channel = Number.parseInt(part ?? "ff", 16) / 255;
    return channel <= 0.03928 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
  };
  const luminance =
    0.2126 * linear(match[1]) + 0.7152 * linear(match[2]) + 0.0722 * linear(match[3]);
  return luminance > 0.4 ? "#000000" : "#ffffff";
}
