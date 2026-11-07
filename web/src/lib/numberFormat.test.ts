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

describe("formatDecimal", () => {
  afterEach(() => setAppLocale(ENGLISH_APP_LOCALE));

  it.each([
    // [value, max, min]: US English matches the toFixed form it replaces.
    [1.005, 2, 2],
    [7.35, 1, 1],
    [2.55, 1, 1],
    [0.5, 1, 1],
    [1234.56, 1, 1],
    [42.9, 1, 0],
    [3, 1, 0],
    [-0.04, 1, 1],
  ])("formats %s like toFixed in en-US (max %s, min %s)", (value, max, min) => {
    setAppLocale({ ...ENGLISH_APP_LOCALE, formatLocale: "en-US" });
    const expected = min === max ? value.toFixed(max) : String(Number(value.toFixed(max)));
    expect(formatDecimal(value, max, min)).toBe(expected);
  });

  it.each([
    [1.005, 2, 2, "1,00"],
    [1234.56, 1, 1, "1234,6"],
    [42.9, 1, 0, "42,9"],
    [3, 1, 0, "3"],
  ])("formats %s for Spanish (max %s, min %s) as %s", (value, max, min, expected) => {
    setAppLocale({ ...ENGLISH_APP_LOCALE, language: "es", catalog: "es", formatLocale: "es" });
    expect(formatDecimal(value, max, min)).toBe(expected);
  });
});

describe("helpers that show decimals", () => {
  afterEach(() => setAppLocale(ENGLISH_APP_LOCALE));

  // Their US English output is pinned by each helper's own tests.
  it.each<[string, () => string, string]>([
    ["formatFileSize", () => formatFileSize(46_063_288_320, { iecUnits: true }), "42,9 GiB"],
    ["formatMbpsFromKbps", () => formatMbpsFromKbps(1500), "1,5 Mbps"],
    ["formatBitrateKbps", () => formatBitrateKbps(1500), "1,5 Mbps"],
    ["formatConnectionRateKbps", () => formatConnectionRateKbps(2345), "2,3 Mbps"],
    ["formatPlaybackSpeed", () => formatPlaybackSpeed(0.5), "0,5×"],
    ["formatSyncOffset", () => formatSyncOffset(-400), "−0,4 s"],
    ["formatTaskProgress", () => formatTaskProgress(99.97), "99,9%"],
    ["formatBytes (account detail)", () => formatBytes(14_200_000_000), "14,2 GB"],
    ["limitLabel (backup upload)", () => limitLabel(1.5 * 1024 ** 3), "1,5 GB"],
  ])("%s uses the Spanish decimal comma", (_name, format, expected) => {
    setAppLocale({ ...ENGLISH_APP_LOCALE, language: "es", catalog: "es", formatLocale: "es" });
    expect(format()).toBe(expected);
  });
});
