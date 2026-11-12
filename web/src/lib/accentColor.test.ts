import { describe, expect, it } from "vitest";

import { accentDeclarations } from "./accentColor";

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
  it("keeps Cinema Dark's dark labels on a light accent", () => {
    expect(accentDeclarations("#10b981")).toEqual({
      primary: "#10b981",
      ring: "#10b981",
      "sidebar-primary": "#10b981",
      "accent-on-dark": "#10b981",
      "accent-on-dark-foreground": "#141417",
    });
  });

  // The player draws the accent on its dark panels: a check or focus ring in a
  // dark accent would vanish there.
  it("lightens a dark accent until it reads on the player's panels", () => {
    for (const accent of ["#000000", "#4f46e5", "#1e3a8a"]) {
      const onDark = accentDeclarations(accent)!["accent-on-dark"]!;
      expect(onDark, accent).not.toBe(accent);
      expect(contrast(onDark, "#101013"), accent).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("turns the labels white on a dark accent", () => {
    const cases: [accent: string, hex: string][] = [
      ["#000000", "#000000"],
      ["#4f46e5", "#4f46e5"],
      ["#64748B", "#64748b"],
      ["#00f", "#0000ff"],
    ];
    for (const [accent, hex] of cases) {
      expect(accentDeclarations(accent), accent).toMatchObject({
        primary: hex,
        "primary-foreground": "#ffffff",
        "sidebar-primary-foreground": "#ffffff",
      });
    }
  });

  // Email buttons honor only hex too, and the admin picker only writes hex.
  it("ignores a value that is not a hex color", () => {
    expect(accentDeclarations("rebeccapurple")).toBeNull();
    expect(accentDeclarations("rgb(0, 0, 0)")).toBeNull();
    expect(accentDeclarations("red;} body{display:none")).toBeNull();
    expect(accentDeclarations("#12345")).toBeNull();
  });
});
