// @vitest-environment node

import { afterEach, describe, expect, it } from "vitest";

import { ENGLISH_APP_LOCALE, setAppLocale } from "./appLocale";
import { formatShortDay } from "./calendarWeek";

function formatIn(locale: string) {
  setAppLocale({ ...ENGLISH_APP_LOCALE, formatLocale: locale });
}

describe("formatShortDay", () => {
  afterEach(() => setAppLocale(ENGLISH_APP_LOCALE));

  // 2026-11-25 is a Wednesday.
  it.each([
    // Short names that fit a phone-width day button stay (four characters at most).
    ["pt-BR", "qua.", "qua."],
    // Long short names fall back to the narrow name on phones.
    ["pt-PT", "quarta", "Q"],
    // One long name ("niedz.") switches the whole week, short ones included.
    ["pl", "śr.", "Ś"],
    // Narrow names that repeat still beat long short names ("Alats").
    ["mg", "Alar", "A"],
  ])("%s: label %s, compact label %s", (locale, label, compactLabel) => {
    formatIn(locale);
    expect(formatShortDay("2026-11-25")).toEqual({ label, compactLabel, day: 25 });
  });
});
