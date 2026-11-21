import { describe, expect, it } from "vitest";
import { accentDeclarations } from "./accentColor";

/** Cinema Dark's labels on primary surfaces, drawn when the accent keeps the dark label. */
const THEME_LABEL = "#141417";
const THEME_SIDEBAR_LABEL = "#0f0f12";
/** Cinema Dark's lightest surface under text (--surface-raised), and the same under high contrast. */
const LIGHTEST_SURFACE = "#28282e";
const LIGHTEST_SURFACE_HIGH_CONTRAST = "#535358";

function luminance(hex: string): number {
  const value = parseInt(hex.slice(1), 16);
  const channel = (shift: number) => {
    const c = ((value >> shift) & 0xff) / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(16) + 0.7152 * channel(8) + 0.0722 * channel(0);
}

/** `bg-primary/20` over `surface`: the strongest accent tint text sits on. */
function tint(accent: string, surface: string): string {
  const a = parseInt(accent.slice(1), 16);
  const b = parseInt(surface.slice(1), 16);
  const channel = (shift: number) =>
    Math.round(((a >> shift) & 0xff) * 0.2 + ((b >> shift) & 0xff) * 0.8)
      .toString(16)
      .padStart(2, "0");
  return `#${channel(16)}${channel(8)}${channel(0)}`;
}

function contrast(a: string, b: string): number {
  const [light, dark] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (light! + 0.05) / (dark! + 0.05);
}

describe("accentDeclarations", () => {
  // Mid-tones where neither white nor the dark label reaches 4.5:1: the violet
  // preset, and grays where white or the dark label is the closer miss.
  // Light and dark accents keep the white and dark labels.
  it.each(["#8b5cf6", "#777777", "#7d7d7d", "#4f46e5", "#f59e0b"])(
    "draws %s as stored with labels at 4.5:1 or more",
    (accent) => {
      const declarations = accentDeclarations(accent)!;
      expect(declarations.primary).toBe(accent);
      const label = declarations["primary-foreground"] ?? THEME_LABEL;
      const sidebarLabel = declarations["sidebar-primary-foreground"] ?? THEME_SIDEBAR_LABEL;
      expect(contrast(accent, label)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(accent, sidebarLabel)).toBeGreaterThanOrEqual(4.5);
    },
  );

  // Every shipped preset, a gray that takes the black label where white reads
  // better than the dark one, and an accent whose fade toward the page passes
  // only once rounded to whole channels (the browser's color-mix keeps the
  // fractions, so the accent must set the hex it checked).
  it.each([
    "#4f46e5",
    "#0ea5e9",
    "#10b981",
    "#f59e0b",
    "#ef4444",
    "#ec4899",
    "#8b5cf6",
    "#64748b",
    "#777777",
    "#7d87b2",
  ])("keeps the label on %s at 4.5:1 or more on hover", (accent) => {
    const declarations = accentDeclarations(accent)!;
    const label = declarations["primary-foreground"] ?? THEME_LABEL;
    const hover = declarations["primary-hover"];
    expect(hover).toBeDefined();
    expect(hover).not.toBe(accent);
    expect(contrast(hover!, label)).toBeGreaterThanOrEqual(4.5);
  });

  // Dark presets that fail as text, one that already reads, and the extremes.
  it.each(["#4f46e5", "#8b5cf6", "#f59e0b", "#000000", "#ffffff"])(
    "draws %s as text at 4.5:1 or more on Cinema Dark",
    (accent) => {
      const declarations = accentDeclarations(accent)!;
      const variants: [string | undefined, string][] = [
        [declarations["primary-text"], LIGHTEST_SURFACE],
        [declarations["primary-text-high-contrast"], LIGHTEST_SURFACE_HIGH_CONTRAST],
      ];
      for (const [text, surface] of variants) {
        expect(text).toBeDefined();
        expect(contrast(text!, surface)).toBeGreaterThanOrEqual(4.5);
        expect(contrast(text!, tint(accent, surface))).toBeGreaterThanOrEqual(4.5);
      }
    },
  );
});
