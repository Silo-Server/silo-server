// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { PlayerSubtitleInfo } from "../types";
import {
  describeSyncScale,
  storedSyncState,
  syncFailureMessage,
  syncKeyOf,
  syncPhaseLabel,
  syncProgressPercent,
  syncStatusLabel,
  type StoredSubtitle,
  type SubtitleSyncJob,
} from "./subtitleSync";

function track(overrides: Partial<PlayerSubtitleInfo>): PlayerSubtitleInfo {
  return {
    index: 3,
    language: "en",
    label: "English",
    source: "external",
    sync_key: "external-" + "a".repeat(64),
    url: "https://silo.example/api/v1/stream/s1/subtitles/3.vtt?file_id=42&token=t",
    ...overrides,
  };
}

function job(
  status: SubtitleSyncJob["status"],
  result?: SubtitleSyncJob["result"],
  extra?: Partial<SubtitleSyncJob>,
): SubtitleSyncJob {
  return {
    id: "1",
    status,
    trigger: "manual",
    confidence: 0.9,
    created_at: "2026-01-02T03:04:05.000Z",
    finished_at: null,
    result,
    ...extra,
  };
}

describe("syncKeyOf", () => {
  it("reads the sync key the inventory publishes", () => {
    expect(syncKeyOf(track({}))).toBe("external-" + "a".repeat(64));
    expect(syncKeyOf(track({ source: "downloaded", sync_key: "stored-7" }))).toBe("stored-7");
  });

  it("reads a stored subtitle's key from its URL in a plan without sync keys", () => {
    const legacy = track({
      source: "downloaded",
      sync_key: undefined,
      url: "/api/v2/playback/sessions/s1/subtitles/2.vtt?downloaded_subtitle_id=7",
    });
    expect(syncKeyOf(legacy)).toBe("stored-7");
    expect(syncKeyOf({ ...legacy, url: "/subtitles/2.vtt?downloaded_subtitle_id=0" })).toBeNull();
    expect(syncKeyOf({ ...legacy, source: "external" })).toBeNull();
  });

  it.each([
    ["embedded", track({ source: "embedded", sync_key: undefined })],
    ["live", track({ live: true })],
    ["unsyncable", track({ sync_key: undefined })],
  ])("returns null for a %s track", (_name, input) => {
    expect(syncKeyOf(input)).toBeNull();
  });
});

describe("storedSyncState", () => {
  it("names a downloaded or uploaded subtitle by its sync key", () => {
    const subtitle = {
      id: "7",
      media_file_id: "42",
      provider: "upload",
      language: "en",
      format: "srt",
      release_name: "",
      score: 0,
      hearing_impaired: false,
      created_at: "2026-01-02T03:04:05.000Z",
      timing: { offset_ms: 0, scale: 1 },
      sync: { ...job("pending"), subtitle_id: "7" },
    } satisfies StoredSubtitle;
    expect(storedSyncState(subtitle)).toMatchObject({
      key: "stored-7",
      source: "downloaded",
      stored_subtitle_id: "7",
      label: "upload",
      sync: { id: "1", status: "pending" },
    });
  });
});

describe("sync progress", () => {
  it("describes an active job's phase and progress", () => {
    expect(syncPhaseLabel(job("pending", undefined, { phase: "queued", progress: 0 }))).toBe(
      "Waiting to start…",
    );
    expect(syncPhaseLabel(job("running", undefined, { phase: "analyzing", progress: 0.4 }))).toBe(
      "Listening to the audio…",
    );
    expect(syncPhaseLabel(job("running", undefined, { phase: "matching", progress: 0.95 }))).toBe(
      "Matching lines to speech…",
    );
    expect(syncProgressPercent(job("running", undefined, { progress: 0.456 }))).toBe(46);
    expect(syncPhaseLabel(job("synced"))).toBeNull();
    expect(syncProgressPercent(job("synced"))).toBeNull();
  });

  it("explains each failure", () => {
    expect(syncFailureMessage("no_audio")).toMatch(/no audio/);
    expect(syncFailureMessage("unavailable")).toMatch(/busy/);
    expect(syncFailureMessage("subtitle_changed")).toMatch(/changed/);
    expect(syncFailureMessage(undefined)).toBe("Sync failed. Try again.");
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
      "the progress while running",
      { timing: identity, sync: job("running", undefined, { phase: "analyzing", progress: 0.4 }) },
      "Syncing… 40%",
    ],
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
