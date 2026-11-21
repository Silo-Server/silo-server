import { describe, expect, it } from "vitest";
import { accentDeclarations } from "./accentColor";

/** Cinema Dark's labels on primary surfaces, drawn when the accent keeps the dark label. */
const THEME_LABEL = "#141417";
const THEME_SIDEBAR_LABEL = "#0f0f12";

function luminance(hex: string): number {
  const value = parseInt(hex.slice(1), 16);
  const channel = (shift: number) => {
    const c = ((value >> shift) & 0xff) / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(16) + 0.7152 * channel(8) + 0.0722 * channel(0);
}

/** The theme's --primary-hover: the primary faded a tenth toward the page (#141417). */
function fadedTowardPage(hex: string): string {
  const from = parseInt(hex.slice(1), 16);
  const channel = (shift: number) => {
    const c = (from >> shift) & 0xff;
    return Math.round(c + (((0x141417 >> shift) & 0xff) - c) * 0.1)
      .toString(16)
      .padStart(2, "0");
  };
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

  // Every shipped preset, and a gray that takes the black label where white
  // reads better than the dark one. The fill changes on hover either way.
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
  ])("keeps the label on %s at 4.5:1 or more on hover", (accent) => {
    const declarations = accentDeclarations(accent)!;
    const label = declarations["primary-foreground"] ?? THEME_LABEL;
    const hover = declarations["primary-hover"] ?? fadedTowardPage(accent);
    expect(hover).not.toBe(accent);
    expect(contrast(hover, label)).toBeGreaterThanOrEqual(4.5);
  });
});
