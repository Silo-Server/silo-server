import { describe, expect, it } from "vitest";
import { READER_THEMES, legacyThemeFor, readerPalette, themeFromLegacy } from "./readerThemes";

const HEX = /^#[0-9a-f]{6}$/;

describe("READER_THEMES", () => {
  it("defines ten themes with valid hex palettes in both variants", () => {
    const names = Object.keys(READER_THEMES);
    expect(names).toHaveLength(10);
    for (const theme of Object.values(READER_THEMES)) {
      for (const variant of ["light", "dark"] as const) {
        const palette = theme[variant];
        expect(palette.background).toMatch(HEX);
        expect(palette.foreground).toMatch(HEX);
        expect(palette.link).toMatch(HEX);
      }
    }
  });

  it("aliases AMOLED's light variant to its dark palette", () => {
    expect(READER_THEMES.amoled.darkOnly).toBe(true);
    expect(readerPalette("amoled", "light")).toEqual(readerPalette("amoled", "dark"));
    expect(readerPalette("amoled", "light").scheme).toBe("dark");
  });

  it("keeps the existing default and sepia palettes stable", () => {
    expect(readerPalette("default", "light").background).toBe("#ffffff");
    expect(readerPalette("sepia", "light")).toMatchObject({
      background: "#f4ecd8",
      foreground: "#2f261b",
    });
  });
});

describe("legacy mapping", () => {
  it("maps stored legacy themes onto palette pairs", () => {
    expect(themeFromLegacy("light")).toEqual({
      themeName: "default",
      themeVariant: "light",
    });
    expect(themeFromLegacy("sepia")).toEqual({
      themeName: "sepia",
      themeVariant: "light",
    });
    expect(themeFromLegacy("dark")).toEqual({
      themeName: "default",
      themeVariant: "dark",
    });
    expect(themeFromLegacy("nonsense")).toEqual({
      themeName: "default",
      themeVariant: "light",
    });
  });

  it("derives the legacy field for backward-compatible persistence", () => {
    expect(legacyThemeFor("sepia", "light")).toBe("sepia");
    expect(legacyThemeFor("ocean", "dark")).toBe("dark");
    expect(legacyThemeFor("amoled", "light")).toBe("dark");
    expect(legacyThemeFor("meadow", "light")).toBe("light");
  });
});
