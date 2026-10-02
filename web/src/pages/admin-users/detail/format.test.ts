import { afterEach, describe, expect, it, vi } from "vitest";

import {
  episodeCode,
  formatBytes,
  formatDayTime,
  formatLastSeen,
  formatPlayMethod,
  formatShortDate,
  formatStorageLimit,
  formatWatchTime,
  watchedFraction,
} from "./format";

afterEach(() => {
  vi.useRealTimers();
});

describe("formatWatchTime", () => {
  it.each([
    [66000, "18h 20m"],
    [2700, "45m"],
    [3600, "1h 0m"],
    [59, "0m"],
    [0, "0m"],
    [-5, "0m"],
  ])("%s seconds → %s", (seconds, text) => {
    expect(formatWatchTime(seconds)).toBe(text);
  });
});

describe("episodeCode", () => {
  it("pads season and episode", () => {
    expect(episodeCode(2, 4)).toBe("S02E04");
    expect(episodeCode(12, 110)).toBe("S12E110");
  });
  it("is empty unless both numbers are known", () => {
    expect(episodeCode(null, 4)).toBe("");
    expect(episodeCode(2, undefined)).toBe("");
  });
});

describe("formatBytes", () => {
  it.each([
    [14_200_000_000, "14.2 GB"],
    [900_000_000, "900 MB"],
    [999_960_000, "1.0 GB"],
    [3_000_000_000_000, "3.0 TB"],
    [512, "512 B"],
    [0, "0 B"],
  ])("%s bytes → %s", (bytes, text) => {
    expect(formatBytes(bytes)).toBe(text);
  });
});

describe("watchedFraction", () => {
  it("counts a completed play as all of it", () => {
    expect(watchedFraction(10, 100, true)).toBe(1);
    expect(watchedFraction(10, null, true)).toBe(1);
  });
  it("divides by the duration and clamps", () => {
    expect(watchedFraction(38, 100, false)).toBeCloseTo(0.38);
    expect(watchedFraction(150, 100, false)).toBe(1);
  });
  it("is 0 without a usable duration", () => {
    expect(watchedFraction(10, null, false)).toBe(0);
    expect(watchedFraction(10, 0, false)).toBe(0);
  });
});

describe("dates", () => {
  it("formats a short date and nothing for no date", () => {
    expect(formatShortDate("2026-09-26T18:02:00")).toMatch(/Sep 26|26 Sep/);
    expect(formatShortDate(null)).toBe("");
    expect(formatShortDate("not a date")).toBe("");
  });

  it("names today and yesterday", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-29T12:00:00"));
    expect(formatDayTime("2026-09-29T09:02:00")).toMatch(/^Today, /);
    expect(formatDayTime("2026-09-28T21:14:00")).toMatch(/^Yesterday, /);
    expect(formatDayTime("2026-09-26T18:02:00")).toMatch(/^(Sep 26|26 Sep), /);
  });

  it("shows a dash for a device never seen", () => {
    expect(formatLastSeen(null)).toBe("—");
  });
});

describe("formatPlayMethod", () => {
  it("labels canonical methods the same way on every card", () => {
    expect(formatPlayMethod("direct")).toBe("Direct Play");
    expect(formatPlayMethod("direct_stream")).toBe("Direct Stream");
    expect(formatPlayMethod("remux")).toBe("Remux");
    expect(formatPlayMethod("transcode")).toBe("Transcode");
    expect(formatPlayMethod("hls")).toBe("HLS");
  });

  it("reads an unrecognized value as itself and only a missing one as Unknown", () => {
    expect(formatPlayMethod("direct_play")).toBe("Direct Play");
    expect(formatPlayMethod("")).toBe("Unknown");
    expect(formatPlayMethod(null)).toBe("Unknown");
  });
});

describe("formatStorageLimit", () => {
  it("reads a cap in the binary gigabytes the apps store", () => {
    expect(formatStorageLimit(10 * 1024 ** 3)).toBe("10 GB");
    expect(formatStorageLimit(1.5 * 1024 ** 3)).toBe("1.5 GB");
    expect(formatStorageLimit(512 * 1024 ** 2)).toBe("512 MB");
  });
});
