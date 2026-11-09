import { afterEach, describe, expect, it } from "vitest";

import { createAppIntl, setAppIntl } from "@/i18n";

import { formatTime } from "./formatTime";

afterEach(() => setAppIntl(createAppIntl("en", {})));

describe("formatTime", () => {
  it.each([
    {
      name: "English keeps its bytes",
      tag: "en",
      formatLocale: undefined,
      expected: ["0:00", "1:05", "1:30:41"],
    },
    {
      name: "Arabic on ar-EG uses Arabic-Indic digits",
      tag: "ar",
      formatLocale: "ar-EG",
      expected: ["٠:٠٠", "١:٠٥", "١:٣٠:٤١"],
    },
    {
      name: "Arabic on ar-AE keeps Latin digits",
      tag: "ar",
      formatLocale: "ar-AE",
      expected: ["0:00", "1:05", "1:30:41"],
    },
  ])("$name", ({ tag, formatLocale, expected }) => {
    setAppIntl(createAppIntl(tag, {}, formatLocale));
    expect([0, 65, 5441].map(formatTime)).toEqual(expected);
  });
});
