import { afterEach, describe, expect, it } from "vitest";
import type { MessageFormatElement } from "react-intl";

import { ENGLISH_APP_LOCALE, setAppLocale } from "@/lib/appLocale";
import { formatDate } from "@/lib/datetime";
import { formatEpisodeCode } from "@/lib/episodeCardLabels";

import { createAppIntl, formatWith, serverTextWith, setAppIntl } from "./format";

const FSI = "\u2068";
const PDI = "\u2069";

// Hand-built ASTs in the generated catalog's shape.
const text = (value: string): MessageFormatElement => ({ type: 0, value });
const arg = (value: string): MessageFormatElement => ({ type: 1, value });

const translated = {
  "home.section.recentlyAddedIn": [text("Añadido en "), arg("library")],
  "home.section.continueWatching": [text("Seguir viendo")],
  "web.calendar.dayHeading": [
    arg("weekday"),
    text(" "),
    arg("day"),
    text(" "),
    {
      type: 5,
      value: "day",
      options: { "1": { value: [text("primero")] }, other: { value: [text("día")] } },
    } as MessageFormatElement,
  ],
};

afterEach(() => {
  setAppIntl(createAppIntl("en", {}));
  setAppLocale(ENGLISH_APP_LOCALE);
});

describe("serverTextWith", () => {
  const spanish = createAppIntl("es", translated);
  it.each([
    {
      name: "English shows the server's text even when the key is known",
      intl: createAppIntl("en", {}),
      key: "home.section.continueWatching",
      args: undefined,
      expected: "Continue Watching",
    },
    {
      name: "a translated key wins",
      intl: spanish,
      key: "home.section.continueWatching",
      args: undefined,
      expected: "Seguir viendo",
    },
    {
      name: "arguments fill the translation",
      intl: spanish,
      key: "home.section.recentlyAddedIn",
      args: { library: "Películas" },
      expected: "Añadido en Películas",
    },
    {
      name: "a missing argument falls back to the server's text",
      intl: spanish,
      key: "home.section.recentlyAddedIn",
      args: {} as Record<string, string>,
      expected: "Continue Watching",
    },
    {
      name: "a key the catalog lacks falls back",
      intl: spanish,
      key: "home.section.somethingNew",
      args: undefined,
      expected: "Continue Watching",
    },
    {
      name: "no key falls back",
      intl: spanish,
      key: undefined,
      args: undefined,
      expected: "Continue Watching",
    },
  ])("$name", ({ intl, key, args, expected }) => {
    expect(serverTextWith(intl, key, args, "Continue Watching")).toBe(expected);
  });

  it("isolates printed arguments in a right-to-left language", () => {
    // The right-to-left pseudo-locale is always generated; `ar` is listed only once it ships.
    const arabic = createAppIntl("ar-XB", translated);
    expect(serverTextWith(arabic, "home.section.recentlyAddedIn", { library: "Movies" }, "x")).toBe(
      `Añadido en ${FSI}Movies${PDI}`,
    );
  });
});

describe("formatWith", () => {
  it("formats a message the language lacks in English, with English plural rules", () => {
    const japanese = createAppIntl("ja", {});
    expect(formatWith(japanese, "web.home.rows.addToOtherLibraries.confirm", { count: 1 })).toBe(
      "Add to 1 page",
    );
    expect(formatWith(japanese, "web.home.rows.addToOtherLibraries.confirm", { count: 3 })).toBe(
      "Add to 3 pages",
    );
  });

  it("keeps English bytes and selects on the raw value in a right-to-left language", () => {
    const values = { weekday: "Monday", month: "April", day: "1" };
    expect(formatWith(createAppIntl("en", {}), "web.calendar.dayHeading", values)).toBe(
      "Monday, April 1st",
    );
    // `day` is printed and selected on: the select must still see "1".
    expect(formatWith(createAppIntl("ar-XB", translated), "web.calendar.dayHeading", values)).toBe(
      `${FSI}Monday${PDI} 1 primero`,
    );
  });

  it.each([
    // An isolate around "01" would split "S01E03" and render it as "03 S01E".
    { name: "bare numbers", season: "01", episode: "03", expected: "S01E03" },
    // Next to Arabic text, an unisolated "2-3" would show as "3-2" and "8+" as "+8".
    { name: "a range", season: "2-3", episode: "03", expected: `S${FSI}2-3${PDI}E03` },
    { name: "a signed number", season: "01", episode: "8+", expected: `S01E${FSI}8+${PDI}` },
  ])(
    "isolates $name in a right-to-left language only where needed",
    ({ season, episode, expected }) => {
      const arabic = createAppIntl("ar-XB", {});
      expect(formatWith(arabic, "web.detail.episodeCode", { season, episode })).toBe(expected);
    },
  );
});

describe("Arabic digits follow the format locale", () => {
  const plural = (forms: Record<string, MessageFormatElement[]>): MessageFormatElement =>
    ({
      type: 6,
      value: "count",
      options: Object.fromEntries(Object.entries(forms).map(([form, value]) => [form, { value }])),
      offset: 0,
      pluralType: "cardinal",
    }) as MessageFormatElement;
  const pound = { type: 7 } as MessageFormatElement;
  // One message on screen: a plural count, a date, two counts passed as
  // numbers, and a title that is only digits (a string, so never changed).
  const arabic = {
    "web.home.rows.addToOtherLibraries.confirm": [
      plural({
        few: [text("أضف إلى "), pound, text(" صفحات")],
        other: [text("أضف إلى "), pound, text(" صفحة")],
      }),
      text(" · "),
      arg("date"),
      text(" · "),
      arg("count_text"),
      text(" · "),
      arg("minutes"),
      text(" · "),
      arg("title"),
    ],
    "web.item.dialog.yearInParens": [text("("), arg("year"), text(")")],
  };
  const values = { count: 3, count_text: 12, minutes: 45, title: "1917" };
  const ARABIC_INDIC = /^[^0-9]*[٠-٩][^0-9]*$/;
  const LATIN = /^[^٠-٩]*[0-9][^٠-٩]*$/;

  it.each([
    {
      name: "ar on ar-EG uses Arabic-Indic digits, as its dates do",
      tag: "ar",
      formatLocale: "ar-EG",
      dateDigits: ARABIC_INDIC,
      message: (date: string) => `أضف إلى ٣ صفحات · ${FSI}${date}${PDI} · ١٢ · ٤٥ · 1917`,
      year: "(١٩٩٩)",
    },
    {
      name: "ar on a Latin-default locale uses Latin digits",
      tag: "ar",
      formatLocale: "ar-AE",
      dateDigits: LATIN,
      message: (date: string) => `أضف إلى 3 صفحات · ${FSI}${date}${PDI} · 12 · 45 · 1917`,
      year: "(1999)",
    },
    {
      name: "English keeps its bytes on an Arabic-region format locale",
      tag: "en",
      formatLocale: "ar-EG",
      dateDigits: ARABIC_INDIC,
      message: () => "Add to 3 pages",
      year: "(1999)",
    },
  ])("$name", ({ tag, formatLocale, dateDigits, message, year }) => {
    const intl = createAppIntl(tag, tag === "en" ? {} : arabic, formatLocale);
    setAppLocale({ ...ENGLISH_APP_LOCALE, formatLocale });
    const date = formatDate(new Date(2026, 10, 7));
    expect(date).toMatch(dateDigits);
    expect(formatWith(intl, "web.home.rows.addToOtherLibraries.confirm", { ...values, date })).toBe(
      message(date),
    );
    expect(formatWith(intl, "web.item.dialog.yearInParens", { year: 1999 })).toBe(year);
  });

  it("keeps Latin digits in an English fallback, whose plural # is Latin", () => {
    const intl = createAppIntl("ar", {}, "ar-EG");
    expect(formatWith(intl, "web.home.rows.addToOtherLibraries.confirm", { count: 3 })).toBe(
      "Add to 3 pages",
    );
    expect(formatWith(intl, "web.item.dialog.yearInParens", { year: 1999 })).toBe("(1999)");
  });

  it("keeps an episode code Latin beside a year in Arabic-Indic digits", () => {
    const intl = createAppIntl(
      "ar",
      {
        ...arabic,
        "web.detail.episodeCode": [text("م"), arg("season"), text("ح"), arg("episode")],
      },
      "ar-EG",
    );
    setAppIntl(intl);
    expect(formatEpisodeCode(1, 5)).toBe("م01ح05");
    expect(formatWith(intl, "web.item.dialog.yearInParens", { year: 1999 })).toBe("(١٩٩٩)");
  });
});
