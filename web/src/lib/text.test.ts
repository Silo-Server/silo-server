import { afterEach, describe, expect, it, vi } from "vitest";
import { ENGLISH_APP_LOCALE, setAppLocale } from "./appLocale";
import { getInitial, getInitials } from "./text";

// Names that start with a character outside the Basic Multilingual Plane are two
// UTF-16 code units; cutting between them draws a replacement glyph.
describe("getInitials", () => {
  it.each([
    ["\u{1F43B} Bear", "\u{1F43B}B"],
    ["\u{1F43B}Bear", "\u{1F43B}B"],
  ])("keeps %j's first character whole", (name, initials) => {
    expect(getInitials(name)).toBe(initials);
  });
});

// The admin account page's avatar upper-cases with CSS under the page's lang,
// so every other avatar has to follow the interface language too.
describe("getInitial", () => {
  afterEach(() => setAppLocale(ENGLISH_APP_LOCALE));

  it("upper-cases by the interface language", () => {
    expect(getInitial("irem")).toBe("I");
    setAppLocale({ ...ENGLISH_APP_LOCALE, language: "tr", catalog: "tr" });
    expect(getInitial("irem")).toBe("İ");
    expect(getInitials("irem")).toBe("İR");
  });
});

// Firefox before 125 has no Intl.Segmenter; the code-point fallback must still
// keep a flag, a skin tone and a tag sequence with the emoji they belong to.
describe("graphemeStarts without Intl.Segmenter", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  it("doesn't start a character inside a flag or a modified emoji", async () => {
    vi.stubGlobal("Intl", { ...Intl, Segmenter: undefined });
    vi.resetModules();
    const { graphemeStarts } = await import("./text");
    const england = "\u{1f3f4}\u{e0067}\u{e0062}\u{e0065}\u{e006e}\u{e0067}\u{e007f}";
    const text = `a\u{1f1fa}\u{1f1f8}\u{1f1e9}\u{1f1ea}b\u{1f44d}\u{1f3fd}c${england}`;
    expect([...graphemeStarts(text)]).toEqual([0, 1, 5, 9, 10, 14, 15]);
  });
});
