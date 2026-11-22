import { afterEach, describe, expect, it } from "vitest";
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
