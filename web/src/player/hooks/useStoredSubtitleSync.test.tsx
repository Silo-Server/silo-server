import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { PlayerConfig } from "../context/PlayerConfigContext";
import { PlayerFetchError } from "../player-fetch";
import type { StoredSubtitle, SubtitleSyncJob } from "../utils/storedSubtitleSync";
import {
  SYNC_POLL_INTERVAL_MS,
  SYNC_POLL_LIMIT_MS,
  useStoredSubtitleSync,
} from "./useStoredSubtitleSync";

const v2 = vi.hoisted(() => vi.fn());
vi.mock("../player-v2", () => ({ playerV2: v2 }));

const config: PlayerConfig = {
  apiBaseUrl: "/api/v2",
  getAccessToken: () => "token",
  getProfileId: () => "profile-1",
  getDeviceId: () => "device",
};

function job(
  status: SubtitleSyncJob["status"],
  result?: SubtitleSyncJob["result"],
): SubtitleSyncJob {
  return {
    id: "50",
    subtitle_id: "7",
    status,
    trigger: "auto",
    confidence: null,
    created_at: "2026-01-02T03:04:05.000Z",
    finished_at: null,
    result,
  };
}

function stored(overrides: Partial<StoredSubtitle> = {}): StoredSubtitle {
  return {
    id: "7",
    media_file_id: "42",
    provider: "upload",
    language: "en",
    format: "srt",
    release_name: "Synthetic",
    score: 0,
    hearing_impaired: false,
    created_at: "2026-01-02T03:04:05.000Z",
    timing: { offset_ms: 0, scale: 1 },
    ...overrides,
  };
}

type Routes = Record<string, (options: Record<string, unknown>) => unknown>;

function serve(routes: Routes) {
  v2.mockImplementation(async (_config: PlayerConfig, route: string, options) => {
    const handler = routes[route];
    if (!handler) throw new Error(`unexpected ${route}`);
    return handler(options as Record<string, unknown>);
  });
}

function calls(route: string) {
  return v2.mock.calls.filter(([, key]) => key === route);
}

const LIST = "GET /api/v2/subtitles/{media_file_id}";
const STATUS = "GET /api/v2/subtitles/sync/status";
const READ = "GET /api/v2/subtitles/stored/{id}/sync";

beforeEach(() => {
  vi.useFakeTimers();
  v2.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

function renderSync(props: { mediaFileId?: number; onTimingChanged?: (id: string) => void } = {}) {
  return renderHook(
    ({ mediaFileId, onTimingChanged }) =>
      useStoredSubtitleSync({
        playerConfig: config,
        mediaFileId,
        sessionId: "session-1",
        storedIds: ["7"],
        onTimingChanged,
      }),
    {
      initialProps: {
        mediaFileId: props.mediaFileId ?? 42,
        onTimingChanged: props.onTimingChanged,
      },
    },
  );
}

async function flush() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
}

async function tick(ms = SYNC_POLL_INTERVAL_MS) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

describe("useStoredSubtitleSync", () => {
  it("polls a running sync until it ends and reloads cues for the new timing once", async () => {
    const reads = [
      stored({ sync: job("running") }),
      stored({
        timing: { offset_ms: 2300, scale: 1 },
        sync: job("synced", { offset_ms: 2300, scale: 1 }),
      }),
    ];
    serve({
      [STATUS]: () => ({ state: "available", auto_sync: true, allowed: true, revision: "1" }),
      [LIST]: () => ({ subtitles: [stored({ sync: job("pending") })] }),
      [READ]: () => ({ subtitle: reads.shift() ?? reads[0] }),
    });
    const onTimingChanged = vi.fn();
    const { result } = renderSync({ onTimingChanged });
    await flush();
    expect(result.current.syncAvailable).toBe(true);
    expect(result.current.entries["7"]?.subtitle.sync?.status).toBe("pending");

    await tick();
    expect(calls(READ)).toHaveLength(1);
    expect(result.current.entries["7"]?.subtitle.sync?.status).toBe("running");
    expect(onTimingChanged).not.toHaveBeenCalled();

    await tick();
    expect(result.current.entries["7"]?.subtitle.sync?.status).toBe("synced");
    expect(onTimingChanged).toHaveBeenCalledExactlyOnceWith("7");

    await tick(SYNC_POLL_INTERVAL_MS * 5);
    expect(calls(READ)).toHaveLength(2);
  });

  it("gives up polling after the limit", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [stored({ sync: job("running") })] }),
      [READ]: () => ({ subtitle: stored({ sync: job("running") }) }),
    });
    const { result } = renderSync();
    await flush();
    await tick(SYNC_POLL_LIMIT_MS + SYNC_POLL_INTERVAL_MS);
    const polled = calls(READ).length;
    expect(polled).toBeGreaterThan(0);
    expect(result.current.entries["7"]?.pollExpired).toBe(true);
    await tick(SYNC_POLL_INTERVAL_MS * 10);
    expect(calls(READ)).toHaveLength(polled);

    // Reopening the menu re-arms polling with a fresh limit.
    act(() => result.current.reload());
    await flush();
    await tick();
    expect(calls(READ)).toHaveLength(polled + 1);
    await tick(SYNC_POLL_LIMIT_MS + SYNC_POLL_INTERVAL_MS);
    expect(result.current.entries["7"]?.pollExpired).toBe(true);
  });

  it("stops polling and drops late results when the file changes", async () => {
    let finish!: (value: unknown) => void;
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: (options) =>
        (options.path as { media_file_id: string }).media_file_id === "42"
          ? { subtitles: [stored({ sync: job("running") })] }
          : { subtitles: [] },
      [READ]: () => new Promise((resolve) => (finish = resolve)),
    });
    const onTimingChanged = vi.fn();
    const { result, rerender } = renderSync({ onTimingChanged });
    await flush();
    await tick();
    expect(calls(READ)).toHaveLength(1);

    rerender({ mediaFileId: 43, onTimingChanged });
    await act(async () => {
      finish({ subtitle: stored({ timing: { offset_ms: 900, scale: 1 }, sync: job("synced") }) });
    });
    await tick(SYNC_POLL_INTERVAL_MS * 3);
    expect(result.current.entries).toEqual({});
    expect(onTimingChanged).not.toHaveBeenCalled();
    expect(calls(READ)).toHaveLength(1);
  });

  it("resets timing with the metadata ETag as If-Match", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [stored({ timing: { offset_ms: 2300, scale: 1 } })] }),
      "GET /api/v2/subtitles/stored/{id}/metadata": (options) => {
        (options.onResponse as (r: Response) => void)(
          new Response(null, { headers: { ETag: '"rev-3"' } }),
        );
        return stored({ timing: { offset_ms: 2300, scale: 1 } });
      },
      "PUT /api/v2/subtitles/stored/{id}/timing": () => ({ subtitle: stored() }),
    });
    const onTimingChanged = vi.fn();
    const { result } = renderSync({ onTimingChanged });
    await flush();

    await act(async () => {
      await result.current.resetTiming("7");
    });

    expect(calls("PUT /api/v2/subtitles/stored/{id}/timing")[0]?.[2]).toEqual({
      path: { id: "7" },
      headers: { "If-Match": '"rev-3"' },
      body: { offset_ms: 0, scale: 1 },
    });
    expect(result.current.entries["7"]?.subtitle.timing).toEqual({ offset_ms: 0, scale: 1 });
    expect(result.current.entries["7"]?.busy).toBe(false);
    expect(onTimingChanged).toHaveBeenCalledExactlyOnceWith("7");
  });

  it("marks the subtitle forbidden when the server refuses a sync", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [stored()] }),
      "POST /api/v2/subtitles/stored/{id}/sync": () => {
        throw new PlayerFetchError(403, "Forbidden", "forbidden");
      },
    });
    const { result } = renderSync();
    await flush();
    await act(async () => {
      await result.current.requestSync("7");
    });
    expect(result.current.entries["7"]).toMatchObject({ forbidden: true, busy: false });
    expect(result.current.entries["7"]?.error).toBeUndefined();
  });

  it("starts polling the job a sync request returns", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [stored({ sync: job("no_match") })] }),
      "POST /api/v2/subtitles/stored/{id}/sync": () => ({ job: job("pending") }),
      [READ]: () => ({ subtitle: stored({ sync: job("already_synced") }) }),
    });
    const { result } = renderSync();
    await flush();
    await act(async () => {
      await result.current.requestSync("7");
    });
    expect(result.current.entries["7"]?.subtitle.sync?.status).toBe("pending");
    await tick();
    expect(result.current.entries["7"]?.subtitle.sync?.status).toBe("already_synced");
  });

  it("reloads cues once for a realtime timing change, whatever the follow-up read shows", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [stored()] }),
      [READ]: () => ({
        subtitle: stored({
          timing: { offset_ms: -400, scale: 1 },
          sync: job("synced", { offset_ms: -400, scale: 1 }),
        }),
      }),
    });
    const onTimingChanged = vi.fn();
    const { result } = renderSync({ onTimingChanged });
    await flush();
    act(() => result.current.timingChanged("7"));
    await flush();
    expect(onTimingChanged).toHaveBeenCalledExactlyOnceWith("7");
    expect(result.current.entries["7"]?.subtitle.timing.offset_ms).toBe(-400);
  });
});
