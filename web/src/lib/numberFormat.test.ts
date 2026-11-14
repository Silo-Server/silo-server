// @vitest-environment node

import { afterEach, describe, expect, it } from "vitest";

import { formatBytes } from "@/pages/admin-users/detail/format";
import { limitLabel } from "@/pages/admin-backups/uploadModel";
import { formatSyncOffset } from "@/player/utils/subtitleSync";
import { formatPlaybackSpeed } from "@/player/utils/playbackSpeed";

import { ENGLISH_APP_LOCALE, setAppLocale } from "./appLocale";
import { formatBitrateKbps, formatConnectionRateKbps } from "./bitrateOptions";
import { formatFileSize, formatMbpsFromKbps } from "./mediaFormat";
import { formatDecimal } from "./numberFormat";
import { formatTaskProgress } from "./taskProgress";

const US_ENGLISH = { ...ENGLISH_APP_LOCALE, formatLocale: "en-US" };
const SPANISH = {
  ...ENGLISH_APP_LOCALE,
  language: "es" as const,
  catalog: "es",
  formatLocale: "es",
};

afterEach(() => setAppLocale(ENGLISH_APP_LOCALE));

describe("formatDecimal", () => {
  // [value, max, min, US English (what toFixed printed), Spanish]
  it.each([
    [1.005, 2, 2, "1.00", "1,00"], // toFixed rounds the binary value; Intl alone gives 1.01
    [2.55, 1, 1, "2.5", "2,5"],
    [1234.56, 1, 1, "1234.6", "1234,6"], // never grouped
    [42.9, 1, 0, "42.9", "42,9"],
    [3, 1, 0, "3", "3"], // a zero decimal drops when min is 0
    [-0.04, 1, 1, "-0.0", "-0,0"],
  ])("formats %s (max %s, min %s) as %s and %s", (value, max, min, english, spanish) => {
    setAppLocale(US_ENGLISH);
    expect(formatDecimal(value, max, min)).toBe(english);
    setAppLocale(SPANISH);
    expect(formatDecimal(value, max, min)).toBe(spanish);
  });
});

describe("helpers that show decimals", () => {
  it.each<[string, () => string, string, string]>([
    [
      "formatFileSize",
      () => formatFileSize(46_063_288_320, { iecUnits: true }),
      "42.9 GiB",
      "42,9 GiB",
    ],
    [
      "formatFileSize, zero decimal trimmed",
      () => formatFileSize(64 * 1024 ** 2, { trimZeroDecimal: true }),
      "64 MB",
      "64 MB",
    ],
    ["formatMbpsFromKbps", () => formatMbpsFromKbps(1500), "1.5 Mbps", "1,5 Mbps"],
    ["formatBitrateKbps", () => formatBitrateKbps(1500), "1.5 Mbps", "1,5 Mbps"],
    ["formatConnectionRateKbps", () => formatConnectionRateKbps(2345), "2.3 Mbps", "2,3 Mbps"],
    ["formatPlaybackSpeed", () => formatPlaybackSpeed(0.5), "0.5×", "0,5×"],
    ["formatSyncOffset", () => formatSyncOffset(-400), "−0.4 s", "−0,4 s"],
    ["formatTaskProgress", () => formatTaskProgress(99.97), "99.9%", "99,9%"],
    ["formatBytes (account detail)", () => formatBytes(14_200_000_000), "14.2 GB", "14,2 GB"],
    ["limitLabel (backup upload)", () => limitLabel(1.5 * 1024 ** 3), "1.5 GB", "1,5 GB"],
  ])("%s in US English and Spanish", (_name, format, english, spanish) => {
    setAppLocale(US_ENGLISH);
    expect(format()).toBe(english);
    setAppLocale(SPANISH);
    expect(format()).toBe(spanish);
  });
});
