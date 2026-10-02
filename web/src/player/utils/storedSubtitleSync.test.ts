import { describe, expect, it } from "vitest";

import type { PlayerSubtitleInfo } from "../types";
import {
  describeSyncScale,
  storedSubtitleIdOf,
  syncStatusLabel,
  type SubtitleSyncJob,
} from "./storedSubtitleSync";

function track(overrides: Partial<PlayerSubtitleInfo>): PlayerSubtitleInfo {
  return {
    index: 3,
    language: "en",
    label: "English",
    source: "downloaded",
    url: "https://silo.example/api/v1/stream/s1/subtitles/3.vtt?file_id=42&downloaded_subtitle_id=7&token=t",
    ...overrides,
  };
}

function job(
  status: SubtitleSyncJob["status"],
  result?: SubtitleSyncJob["result"],
): SubtitleSyncJob {
  return {
    id: "1",
    subtitle_id: "7",
    status,
    trigger: "manual",
    confidence: 0.9,
    created_at: "2026-01-02T03:04:05.000Z",
    finished_at: null,
    result,
  };
}

describe("storedSubtitleIdOf", () => {
  it("reads the stored row pinned in a downloaded track URL", () => {
    expect(storedSubtitleIdOf(track({}))).toBe("7");
    expect(
      storedSubtitleIdOf(track({ url: "/stream/s1/subtitles/3.vtt?downloaded_subtitle_id=12" })),
    ).toBe("12");
  });

  it.each([
    ["embedded", track({ source: "embedded" })],
    ["live", track({ live: true })],
    ["unpinned", track({ url: "/stream/s1/subtitles/3.vtt?file_id=42" })],
    ["malformed", track({ url: "/stream/s1/subtitles/3.vtt?downloaded_subtitle_id=07" })],
    ["burn-in only", track({ url: "" })],
  ])("returns null for a %s track", (_name, input) => {
    expect(storedSubtitleIdOf(input)).toBeNull();
  });
});

describe("describeSyncScale", () => {
  it.each([
    [25 / 23.976, "25→23.976 fps"],
    [23.976 / 25, "23.976→25 fps"],
    [24 / 23.976, "24→23.976 fps"],
    [1.0123, "×1.0123 speed"],
    // Drift between two cuts, close to but not a frame-rate conversion.
    [1.000761, "×1.0008 speed"],
    [1, null],
  ])("describes %f as %s", (scale, expected) => {
    expect(describeSyncScale(scale)).toBe(expected);
  });
});

describe("syncStatusLabel", () => {
  const identity = { offset_ms: 0, scale: 1 };
  it.each([
    ["nothing for an untouched subtitle", { timing: identity }, null],
    ["Syncing… while pending", { timing: identity, sync: job("pending") }, "Syncing…"],
    ["Syncing… while running", { timing: identity, sync: job("running") }, "Syncing…"],
    [
      "the applied offset",
      {
        timing: { offset_ms: 2300, scale: 1 },
        sync: job("synced", { offset_ms: 2300, scale: 1 }),
      },
      "Synced +2.3 s",
    ],
    [
      "the offset and frame-rate note",
      {
        timing: { offset_ms: -1500, scale: 25 / 23.976 },
        sync: job("synced", { offset_ms: -1500, scale: 25 / 23.976 }),
      },
      "Synced −1.5 s · 25→23.976 fps",
    ],
    ["already in sync", { timing: identity, sync: job("already_synced") }, "Already in sync"],
    ["no match", { timing: identity, sync: job("no_match") }, "Doesn't match this video"],
    ["failure", { timing: identity, sync: job("failed") }, "Sync failed"],
    [
      "a reset after sync",
      { timing: identity, sync: job("synced", { offset_ms: 2300, scale: 1 }) },
      "Original timing",
    ],
    ["a manual adjustment", { timing: { offset_ms: 500, scale: 1 } }, "Timing adjusted +0.5 s"],
  ])("shows %s", (_name, subtitle, expected) => {
    expect(syncStatusLabel(subtitle)).toBe(expected);
  });
});
