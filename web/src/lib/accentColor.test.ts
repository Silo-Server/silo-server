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
});
