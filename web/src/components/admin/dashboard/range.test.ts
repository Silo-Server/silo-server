import { afterEach, describe, expect, it } from "vitest";

import { createAppIntl, setAppIntl, type Messages } from "@/i18n";
import english from "@/locales/en.json";

import { rangeTitle } from "./range";

const NBSP = "\u00a0";
// CI's web tests check out only the English catalogs, so the other languages
// carry their shipped message for this key inline.
const DAY_TITLE = "web.admin.dashboard.range.title.day";
const CATALOGS = {
  en: english as Messages,
  ar: {
    [DAY_TITLE]: [
      { type: 1, value: "title" },
      { type: 0, value: " · آخر 24 س" },
    ],
  } as Messages,
  ja: {
    [DAY_TITLE]: [
      { type: 1, value: "title" },
      { type: 0, value: "・過去24時間" },
    ],
  } as Messages,
} satisfies Record<string, Messages>;

afterEach(() => {
  setAppIntl(createAppIntl("en", CATALOGS.en));
});

describe("rangeTitle", () => {
  // A narrow card may wrap the title, but only between the widget's name and
  // its window: "Playback activity" / "· last 24 h", never inside "last 24 h".
  it.each([
    {
      tag: "en",
      title: "Playback activity",
      range: "day",
      expected: `Playback activity ·${NBSP}last${NBSP}24${NBSP}h`,
    },
    {
      tag: "ar",
      title: "Egress",
      range: "day",
      expected: `\u2068Egress\u2069 ·${NBSP}آخر${NBSP}24${NBSP}س`,
    },
    { tag: "ja", title: "再生", range: "day", expected: "再生・過去24時間" },
  ] as const)("$tag: $title", ({ tag, title, range, expected }) => {
    setAppIntl(createAppIntl(tag, CATALOGS[tag]));
    expect(rangeTitle(title, range)).toBe(expected);
  });
});
