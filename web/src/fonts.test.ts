// @vitest-environment node

import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/**
 * fonts.css copies each @fontsource-variable package's @font-face rules by
 * hand, only renaming "<Family> Variable" to the plain family name. A package
 * bump that renames a file fails the build, but one that changes a subset's
 * unicode range or the weight axis would leave fonts.css quietly stale. This
 * compares the two, face by face.
 */
const require = createRequire(import.meta.url);
const fontsCss = readFileSync(fileURLToPath(new URL("./fonts.css", import.meta.url)), "utf8");

interface Face {
  family: string;
  style: string;
  weight: string;
  display: string;
  file: string;
  unicodeRange: string;
}

function descriptor(block: string, name: string): string {
  const value = new RegExp(`${name}\\s*:\\s*([^;]+);`).exec(block)?.[1] ?? "";
  return value
    .replace(/\s+/g, " ")
    .replace(/\s*,\s*/g, ",")
    .trim();
}

function parseFaces(css: string): Face[] {
  const withoutComments = css.replace(/\/\*[\s\S]*?\*\//g, "");
  return [...withoutComments.matchAll(/@font-face\s*\{([^}]*)\}/g)].map(([, block = ""]) => ({
    family: descriptor(block, "font-family").replace(/^["']|["']$/g, ""),
    style: descriptor(block, "font-style"),
    weight: descriptor(block, "font-weight"),
    display: descriptor(block, "font-display"),
    file: /url\(\s*["']?[^"')]*\/([^/"')]+)["']?\s*\)/.exec(block)?.[1] ?? "",
    unicodeRange: descriptor(block, "unicode-range"),
  }));
}

function sortFaces(faces: Face[]): Face[] {
  return [...faces].sort((a, b) => a.file.localeCompare(b.file));
}

/** The families fonts.css self-hosts: only Cinema Dark's UI font. */
const UI_FONTS = ["Plus Jakarta Sans"];

const localFaces = parseFaces(fontsCss);

/** "Plus Jakarta Sans" is published as @fontsource-variable/plus-jakarta-sans. */
function packageSlug(family: string): string {
  return family.toLowerCase().replace(/ /g, "-");
}

describe("fonts.css", () => {
  it("declares exactly the UI fonts", () => {
    expect([...new Set(localFaces.map((face) => face.family))].sort()).toEqual(
      [...UI_FONTS].sort(),
    );
  });

  it("swaps in every face instead of hiding text while it loads", () => {
    for (const face of localFaces) expect(face.display, face.file).toBe("swap");
  });

  it.each(UI_FONTS)("matches the @fontsource-variable package for %s", (family) => {
    const packageCss = readFileSync(
      require.resolve(`@fontsource-variable/${packageSlug(family)}/wght.css`),
      "utf8",
    );
    const packageFaces = parseFaces(packageCss).map((face) => ({
      ...face,
      family: face.family.replace(/ Variable$/, ""),
    }));
    expect(packageFaces.length).toBeGreaterThan(0);
    expect(sortFaces(localFaces.filter((face) => face.family === family))).toEqual(
      sortFaces(packageFaces),
    );
  });

  it("loads every face from its own package", () => {
    const sources = [...fontsCss.matchAll(/url\(\s*"([^"]+)"\s*\)/g)].map(([, src]) => src);
    expect(sources).toHaveLength(localFaces.length);
    for (const src of sources) {
      expect(src).toMatch(
        /^@fontsource-variable\/([a-z]+(?:-[a-z]+)*)\/files\/\1-[a-z-]+-wght-normal\.woff2$/,
      );
    }
  });
});
