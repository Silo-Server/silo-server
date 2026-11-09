import { afterEach, describe, expect, it } from "vitest";
import type { MessageFormatElement } from "react-intl";

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

afterEach(() => setAppIntl(createAppIntl("en", {})));

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
    const arabic = createAppIntl("ar", translated);
    expect(serverTextWith(arabic, "home.section.recentlyAddedIn", { library: "Movies" }, "x")).toBe(
      `Añadido en ${FSI}Movies${PDI}`,
    );
  });
});

describe("formatWith", () => {
  it("formats a message the language lacks in English, with English plural rules", () => {
    const japanese = createAppIntl("ja", {});
    expect(formatWith(japanese, "web.homeRows.addToOtherLibraries.confirm", { count: 1 })).toBe(
      "Add to 1 page",
    );
    expect(formatWith(japanese, "web.homeRows.addToOtherLibraries.confirm", { count: 3 })).toBe(
      "Add to 3 pages",
    );
  });

  it("keeps English bytes and selects on the raw value in a right-to-left language", () => {
    const values = { weekday: "Monday", month: "April", day: "1" };
    expect(formatWith(createAppIntl("en", {}), "web.calendar.dayHeading", values)).toBe(
      "Monday, April 1st",
    );
    // `day` is printed and selected on: the select must still see "1".
    expect(formatWith(createAppIntl("ar", translated), "web.calendar.dayHeading", values)).toBe(
      `${FSI}Monday${PDI} 1 primero`,
    );
  });
});
