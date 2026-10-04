import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { PlayerSubtitleInfo } from "../types";
import type { SubtitleSyncJob, SubtitleSyncState } from "../utils/subtitleSync";
import type { SubtitleSync, SubtitleSyncEntry } from "./useSubtitleSync";
import {
  SYNC_APPLY_TIMEOUT_MS,
  SYNC_NOTICE_VISIBLE_MS,
  useSubtitleSyncFeedback,
} from "./useSubtitleSyncFeedback";

const KEY = "external-" + "d".repeat(64);
const OTHER = "stored-8";

const tracks: PlayerSubtitleInfo[] = [
  { index: 0, language: "en", label: "Movie.en.srt", source: "external", sync_key: KEY, url: "/a" },
  { index: 1, language: "fr", label: "French", source: "downloaded", sync_key: OTHER, url: "/b" },
];

function job(
  status: SubtitleSyncJob["status"],
  extra: Partial<SubtitleSyncJob> = {},
): SubtitleSyncJob {
  return {
    id: "70",
    status,
    trigger: "manual",
    confidence: null,
    created_at: "2026-01-02T03:04:05.000Z",
    finished_at: null,
    ...extra,
  };
}

function entry(
  key: string,
  sync: SubtitleSyncJob,
  watched = true,
  timing = { offset_ms: 0, scale: 1 },
): SubtitleSyncEntry {
  const state: SubtitleSyncState = {
    key,
    media_file_id: "42",
    source: key === KEY ? "external" : "downloaded",
    language: "en",
    format: "srt",
    label: "x",
    timing,
    sync,
  };
  return { state, watchedJobId: watched ? sync.id : undefined };
}

function controller(entries: Record<string, SubtitleSyncEntry>): SubtitleSync {
  return {
    syncAvailable: true,
    entries,
    requestSync: vi.fn(),
    resetTiming: vi.fn(),
    remember: vi.fn(),
    timingChanged: vi.fn(),
    syncUpdated: vi.fn(),
    reload: vi.fn(),
  };
}

interface Props {
  entries: Record<string, SubtitleSyncEntry>;
  activeKey: string | null;
  revision: number;
  loadState: string;
}

function renderFeedback(initial: Props) {
  return renderHook(
    ({ entries, activeKey, revision, loadState }: Props) =>
      useSubtitleSyncFeedback({
        sync: controller(entries),
        tracks,
        activeKey,
        cueRevisions: activeKey ? { [activeKey]: revision } : {},
        loadState,
      }),
    { initialProps: initial },
  );
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("useSubtitleSyncFeedback", () => {
  it("follows a sync this viewer started until the corrected cues show", () => {
    const running = entry(KEY, job("running", { phase: "analyzing", progress: 0.42 }));
    const { result, rerender } = renderFeedback({
      entries: { [KEY]: running },
      activeKey: KEY,
      revision: 0,
      loadState: "ready",
    });
    expect(result.current.notice).toMatchObject({
      tone: "progress",
      title: "Syncing English subtitles",
      detail: "Listening to the audio…",
      percent: 42,
    });

    // The job applied its result; the track's cues reload.
    const synced = entry(KEY, job("synced", { result: { offset_ms: 2300, scale: 1 } }), true, {
      offset_ms: 2300,
      scale: 1,
    });
    rerender({ entries: { [KEY]: synced }, activeKey: KEY, revision: 1, loadState: "ready" });
    rerender({ entries: { [KEY]: synced }, activeKey: KEY, revision: 1, loadState: "loading" });
    expect(result.current.notice).toMatchObject({
      tone: "progress",
      title: "Applying new timing…",
    });
    rerender({ entries: { [KEY]: synced }, activeKey: KEY, revision: 1, loadState: "ready" });
    expect(result.current.notice).toMatchObject({
      tone: "success",
      title: "Subtitles synced",
      detail: "+2.3 s",
    });

    act(() => vi.advanceTimersByTime(SYNC_NOTICE_VISIBLE_MS));
    expect(result.current.notice).toBeNull();
  });

  it("says a sync of a track not on screen is done at once", () => {
    const synced = entry(OTHER, job("synced", { result: { offset_ms: -500, scale: 1 } }), true, {
      offset_ms: -500,
      scale: 1,
    });
    const { result } = renderFeedback({
      entries: { [OTHER]: synced },
      activeKey: KEY,
      revision: 0,
      loadState: "ready",
    });
    expect(result.current.notice).toMatchObject({
      tone: "success",
      title: "French subtitles synced",
      detail: "−0.5 s",
    });
  });

  it.each([
    ["already_synced", {}, "info", "English subtitles already match the audio"],
    ["no_match", {}, "warning", "English subtitles don't match the audio"],
    ["failed", { failure: "unavailable" as const }, "warning", "Couldn't sync English subtitles"],
  ])("reports %s", (status, extra, tone, title) => {
    const finished = entry(KEY, job(status as SubtitleSyncJob["status"], extra));
    const { result } = renderFeedback({
      entries: { [KEY]: finished },
      activeKey: KEY,
      revision: 0,
      loadState: "ready",
    });
    expect(result.current.notice).toMatchObject({ tone, title });
  });

  it("stays quiet about jobs this viewer did not start, until the subtitle on screen changes", () => {
    const others = entry(KEY, job("running", { progress: 0.5 }), false);
    const { result, rerender } = renderFeedback({
      entries: { [KEY]: others },
      activeKey: KEY,
      revision: 0,
      loadState: "ready",
    });
    expect(result.current.notice).toBeNull();

    const synced = entry(KEY, job("synced"), false, { offset_ms: 900, scale: 1 });
    rerender({ entries: { [KEY]: synced }, activeKey: KEY, revision: 1, loadState: "loading" });
    expect(result.current.notice).toBeNull();
    rerender({ entries: { [KEY]: synced }, activeKey: KEY, revision: 1, loadState: "ready" });
    expect(result.current.notice).toMatchObject({ tone: "info", title: "Subtitle timing updated" });
  });

  it("says nothing when a subtitle on screen is synced automatically", () => {
    const unsynced = entry(KEY, job("running", { trigger: "auto", progress: 0.4 }), false);
    const { result, rerender } = renderFeedback({
      entries: { [KEY]: unsynced },
      activeKey: KEY,
      revision: 0,
      loadState: "ready",
    });
    const corrected = { offset_ms: 900, scale: 1 };
    const synced = entry(
      KEY,
      job("synced", { trigger: "auto", result: corrected }),
      false,
      corrected,
    );
    rerender({ entries: { [KEY]: synced }, activeKey: KEY, revision: 1, loadState: "refreshing" });
    rerender({ entries: { [KEY]: synced }, activeKey: KEY, revision: 1, loadState: "ready" });
    expect(result.current.notice).toBeNull();

    // A change someone makes afterwards is still mentioned.
    const reset = entry(KEY, job("synced", { trigger: "auto", result: corrected }), false);
    rerender({ entries: { [KEY]: reset }, activeKey: KEY, revision: 2, loadState: "refreshing" });
    rerender({ entries: { [KEY]: reset }, activeKey: KEY, revision: 2, loadState: "ready" });
    expect(result.current.notice).toMatchObject({ tone: "info", title: "Subtitle timing updated" });
  });

  it("drops the notice of a sync whose file or session went away", () => {
    const running = entry(KEY, job("running", { progress: 0.3 }));
    const { result, rerender } = renderFeedback({
      entries: { [KEY]: running },
      activeKey: KEY,
      revision: 0,
      loadState: "ready",
    });
    expect(result.current.notice).toMatchObject({ tone: "progress" });
    rerender({ entries: {}, activeKey: null, revision: 0, loadState: "idle" });
    expect(result.current.notice).toBeNull();

    // Nor does one waiting for its corrected cues come back later.
    const synced = entry(KEY, job("synced", { id: "71" }), true, { offset_ms: 400, scale: 1 });
    rerender({ entries: { [KEY]: synced }, activeKey: KEY, revision: 1, loadState: "loading" });
    expect(result.current.notice).toMatchObject({ title: "Applying new timing…" });
    rerender({ entries: {}, activeKey: null, revision: 0, loadState: "idle" });
    act(() => vi.advanceTimersByTime(SYNC_APPLY_TIMEOUT_MS));
    expect(result.current.notice).toBeNull();
  });

  it("can be dismissed", () => {
    const finished = entry(KEY, job("no_match"));
    const { result } = renderFeedback({
      entries: { [KEY]: finished },
      activeKey: KEY,
      revision: 0,
      loadState: "ready",
    });
    act(() => result.current.dismiss());
    expect(result.current.notice).toBeNull();
  });
});
