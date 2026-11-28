import { afterEach, describe, expect, it } from "vitest";

import { ENGLISH_APP_LOCALE, setAppLocale } from "@/lib/appLocale";
import { formatTime, setDateTimeFormatPreferences } from "@/lib/datetime";

const HALF_PAST_MIDNIGHT = new Date(2026, 0, 1, 0, 30);
const HALF_PAST_ONE_PM = new Date(2026, 0, 1, 13, 30);

afterEach(() => {
  setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "auto" });
  setAppLocale(ENGLISH_APP_LOCALE);
});

// A 24-hour clock keeps its leading zero ("00:30") where the locale's own
// short time has one, on the automatic clock as on the 24-hour setting
// (#5169). Intl's hour: "numeric" alone drops it ("0:30").
describe("formatTime", () => {
  it.each([
    { formatLocale: "en-GB", timeFormat: "auto", want: "00:30" },
    { formatLocale: "de-DE", timeFormat: "auto", want: "00:30" },
    { formatLocale: "fr-FR", timeFormat: "auto", want: "00:30" },
    { formatLocale: "en-US", timeFormat: "24h", want: "00:30" },
    { formatLocale: "en-GB", timeFormat: "24h", want: "00:30" },
    // Japanese writes its short time with a one-digit hour.
    { formatLocale: "ja-JP", timeFormat: "auto", want: "0:30" },
  ] as const)("$formatLocale, $timeFormat clock: $want", ({ formatLocale, timeFormat, want }) => {
    setAppLocale({ ...ENGLISH_APP_LOCALE, formatLocale });
    setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat });
    expect(formatTime(HALF_PAST_MIDNIGHT)).toBe(want);
  });

  it("keeps a 12-hour clock's hour without a leading zero", () => {
    setAppLocale({ ...ENGLISH_APP_LOCALE, formatLocale: "en-US" });
    expect(formatTime(HALF_PAST_ONE_PM)).toMatch(/^1:30\sPM$/);
    setAppLocale({ ...ENGLISH_APP_LOCALE, formatLocale: "en-GB" });
    setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "12h" });
    expect(formatTime(HALF_PAST_ONE_PM)).toMatch(/^1:30\spm$/);
  });

  it("counts a one-digit hour written in digits outside the BMP as one digit", () => {
    // Mathematical bold digits take two UTF-16 units each; the locale writes "𝟏:𝟑𝟎 ص".
    setAppLocale({ ...ENGLISH_APP_LOCALE, formatLocale: "ar-u-nu-mathbold" });
    expect(formatTime(new Date(2026, 0, 1, 1, 30))).toBe("\u{1D7CF}:\u{1D7D1}\u{1D7CE} \u0635");
  });

  it("leaves an explicit hour option to the caller", () => {
    setAppLocale({ ...ENGLISH_APP_LOCALE, formatLocale: "en-GB" });
    expect(formatTime(HALF_PAST_MIDNIGHT, { hour: "numeric" })).toBe("0:30");
  });
});
