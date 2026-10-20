import type {
  CardOverlayPrefs,
  OverlayFontWeight,
  OverlayPreset,
  OverlayShadow,
  OverlayShape,
  OverlayStyle,
  WatchStatusMode,
} from "./types";

// 200 apart, since closer weights look alike at badge sizes. 800 is also the
// heaviest weight the UI font has.
const FONT_WEIGHTS: Record<OverlayFontWeight, number> = {
  regular: 400,
  semibold: 600,
  extrabold: 800,
};

const SHAPE_RADIUS: Record<OverlayShape, number | "full"> = {
  pill: "full",
  rounded: 6,
  square: 2,
};

// The shadow a style's shadow switch adds to a preset that has none.
const STYLE_SHADOW: OverlayShadow = { x: 0, y: 2, blur: 6, spread: 0, color: "rgb(0 0 0 / 0.35)" };

// The least padding, as a share of the font size, a badge gets when the style
// gives it a fill or a border. Minimal and Edge band draw bare text with
// little or no padding; framed as they are, the letters run to the frame's
// edge and a pill's rounded ends cut into them.
const FRAMED_PADDING_INLINE = 0.6;
const FRAMED_PADDING_BLOCK = 0.2;

// From this opacity a fill, not the artwork, sits behind the text.
const SOLID_FILL_ALPHA = 0.5;

// The least contrast ratio at which accent-coloured text stays on a solid fill.
const ACCENT_TEXT_CONTRAST = 3;

// The alpha of a #RRGGBBAA colour, from 0 to 1. #RRGGBB is opaque.
function alphaOf(hex: string): number {
  return hex.length === 9 ? Number.parseInt(hex.slice(7), 16) / 255 : 1;
}

// A preset with the viewer's style overrides applied. The result is another
// preset, so the renderer and its card-relative scaling stay unaware of where
// a value came from. Per-badge accents keep working: a custom background is
// tinted by the accent in presets that fill with it, and a custom text or
// border colour yields to the accent in presets that paint the accent there.
// A solid fill without a chosen text colour gets black or white text, since
// the preset's own colour was picked to read on the artwork. An accent the
// preset writes as text that would not read on that fill tints it instead.
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
  const fillAlpha = background ? alphaOf(background) : 0;
  if (fillAlpha > 0 || (style.borderWidth ?? 0) > 0) {
    next.paddingInline = Math.max(next.paddingInline, next.fontSize * FRAMED_PADDING_INLINE);
    next.paddingBlock = Math.max(next.paddingBlock, next.fontSize * FRAMED_PADDING_BLOCK);
  }
  const solidFill = fillAlpha >= SOLID_FILL_ALPHA ? background : undefined;
  // A shadow drawn to lift text off the artwork only muddies it on a fill.
  if (solidFill) next.textShadow = undefined;
  if (background || textColor || borderColor || style.borderWidth !== undefined) {
    const strategy = preset.accentStrategy;
    next.badgeStyle = (accent) => {
      const css = { ...preset.badgeStyle(accent) };
      const accentText = Boolean(
        accent &&
        strategy === "text" &&
        (!solidFill || contrastRatio(accent, solidFill) >= ACCENT_TEXT_CONTRAST),
      );
      const accentFill = Boolean(
        accent && (strategy === "bg" || (strategy === "text" && !accentText)),
      );
      if (background) {
        css.background = accentFill
          ? `color-mix(in srgb, ${accent} 30%, ${background})`
          : background;
      }
      if (solidFill) css.textShadow = undefined;
      const text = textColor ?? (solidFill ? readableTextOn(solidFill) : undefined);
      if (text && !accentText) css.color = text;
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

// The relative luminance of a #RRGGBB or #RRGGBBAA colour, ignoring alpha;
// null for anything else.
function luminanceOf(hex: string): number | null {
  const match = /^#([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})/.exec(hex);
  if (!match) return null;
  const linear = (part: string | undefined) => {
    const channel = Number.parseInt(part ?? "ff", 16) / 255;
    return channel <= 0.03928 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * linear(match[1]) + 0.7152 * linear(match[2]) + 0.0722 * linear(match[3]);
}

// The WCAG contrast ratio between two colours. A colour that isn't hex counts
// as legible, so an accent the parser can't read keeps its place.
function contrastRatio(a: string, b: string): number {
  const la = luminanceOf(a);
  const lb = luminanceOf(b);
  if (la === null || lb === null) return Number.POSITIVE_INFINITY;
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

// Black or white, whichever reads better on a hex fill. Used for the rating
// source chips the plate preset draws in the accent colour, and for text on
// a solid style fill.
export function readableTextOn(hex: string): "#000000" | "#ffffff" {
  return (luminanceOf(hex) ?? 0) > 0.4 ? "#000000" : "#ffffff";
}
