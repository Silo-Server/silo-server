import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { PlayerConfig } from "../context/PlayerConfigContext";
import { PlayerFetchError } from "../player-fetch";
import type { PlaybackSubtitleSyncUpdatedPayload } from "../realtime-protocol";
import type { StoredSubtitle, SubtitleSyncJob, SubtitleSyncState } from "../utils/subtitleSync";
import {
  SYNC_POLL_INTERVAL_MS,
  SYNC_POLL_LIMIT_MS,
  SYNC_PUSH_FRESH_MS,
  useSubtitleSync,
} from "./useSubtitleSync";

const v2 = vi.hoisted(() => vi.fn());
vi.mock("../player-v2", () => ({ playerV2: v2 }));

const config: PlayerConfig = {
  apiBaseUrl: "/api/v2",
  getAccessToken: () => "token",
  getProfileId: () => "profile-1",
  getDeviceId: () => "device",
};

const KEY = "stored-7";
const SIDECAR = "external-" + "b".repeat(64);

function job(
  status: SubtitleSyncJob["status"],
  result?: SubtitleSyncJob["result"],
  extra?: Partial<SubtitleSyncJob>,
): SubtitleSyncJob {
  return {
    id: "50",
    status,
    trigger: "auto",
    confidence: null,
    created_at: "2026-01-02T03:04:05.000Z",
    finished_at: null,
    result,
    ...extra,
  };
}

function stored(overrides: Partial<SubtitleSyncState> = {}): SubtitleSyncState {
  return {
    key: KEY,
    media_file_id: "42",
    source: "downloaded",
    stored_subtitle_id: "7",
    language: "en",
    format: "srt",
    label: "Synthetic",
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

const LIST = "GET /api/v2/subtitles/{media_file_id}/sync";
const STATUS = "GET /api/v2/subtitles/sync/status";
const READ = "GET /api/v2/subtitles/{media_file_id}/sync/{key}";
const START = "POST /api/v2/subtitles/{media_file_id}/sync/{key}";
const TIMING = "PUT /api/v2/subtitles/{media_file_id}/sync/{key}/timing";

beforeEach(() => {
  vi.useFakeTimers();
  v2.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

function renderSync(props: { mediaFileId?: number; onTimingChanged?: (key: string) => void } = {}) {
  return renderHook(
    ({ mediaFileId, onTimingChanged }) =>
      useSubtitleSync({
        playerConfig: config,
        mediaFileId,
        sessionId: "session-1",
        syncKeys: [KEY, SIDECAR],
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

describe("useSubtitleSync", () => {
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
    expect(result.current.entries[KEY]?.state.sync?.status).toBe("pending");

    await tick();
    expect(calls(READ)).toHaveLength(1);
    expect(result.current.entries[KEY]?.state.sync?.status).toBe("running");
    expect(onTimingChanged).not.toHaveBeenCalled();

    await tick();
    expect(result.current.entries[KEY]?.state.sync?.status).toBe("synced");
    expect(onTimingChanged).toHaveBeenCalledExactlyOnceWith(KEY);

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
    expect(result.current.entries[KEY]?.pollExpired).toBe(true);
    await tick(SYNC_POLL_INTERVAL_MS * 10);
    expect(calls(READ)).toHaveLength(polled);

    // Reopening the menu re-arms polling with a fresh limit.
    act(() => result.current.reload());
    await flush();
    await tick();
    expect(calls(READ)).toHaveLength(polled + 1);
    await tick(SYNC_POLL_LIMIT_MS + SYNC_POLL_INTERVAL_MS);
    expect(result.current.entries[KEY]?.pollExpired).toBe(true);
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

  it("resets timing with the sync read's ETag as If-Match", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [stored({ timing: { offset_ms: 2300, scale: 1 } })] }),
      [READ]: (options) => {
        (options.onResponse as (r: Response) => void)(
          new Response(null, { headers: { ETag: '"rev-3"' } }),
        );
        return { subtitle: stored({ timing: { offset_ms: 2300, scale: 1 } }) };
      },
      [TIMING]: () => ({ subtitle: stored() }),
    });
    const onTimingChanged = vi.fn();
    const { result } = renderSync({ onTimingChanged });
    await flush();

    await act(async () => {
      await result.current.resetTiming(KEY);
    });

    expect(calls(TIMING)[0]?.[2]).toEqual({
      path: { media_file_id: "42", key: KEY },
      headers: { "If-Match": '"rev-3"' },
      body: { offset_ms: 0, scale: 1 },
    });
    expect(result.current.entries[KEY]?.state.timing).toEqual({ offset_ms: 0, scale: 1 });
    expect(result.current.entries[KEY]?.busy).toBe(false);
    expect(onTimingChanged).toHaveBeenCalledExactlyOnceWith(KEY);
  });

  it("marks the subtitle forbidden when the server refuses a sync", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [stored()] }),
      [START]: () => {
        throw new PlayerFetchError(403, "Forbidden", "forbidden");
      },
    });
    const { result } = renderSync();
    await flush();
    await act(async () => {
      await result.current.requestSync(KEY);
    });
    expect(result.current.entries[KEY]).toMatchObject({ forbidden: true, busy: false });
    expect(result.current.entries[KEY]?.error).toBeUndefined();
  });

  it("starts polling the job a sync request returns", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [stored({ sync: job("no_match") })] }),
      [START]: () => ({ subtitle: stored({ sync: job("pending", undefined, { id: "51" }) }) }),
      [READ]: () => ({ subtitle: stored({ sync: job("already_synced") }) }),
    });
    const { result } = renderSync();
    await flush();
    await act(async () => {
      await result.current.requestSync(KEY);
    });
    expect(result.current.entries[KEY]?.state.sync?.status).toBe("pending");
    // The viewer started this job: the player follows it for them.
    expect(result.current.entries[KEY]?.watchedJobId).toBe("51");
    await tick();
    expect(result.current.entries[KEY]?.state.sync?.status).toBe("already_synced");
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
    act(() => result.current.timingChanged(KEY));
    await flush();
    expect(onTimingChanged).toHaveBeenCalledExactlyOnceWith(KEY);
    expect(result.current.entries[KEY]?.state.timing.offset_ms).toBe(-400);
  });

  it("follows realtime progress and polls only when it goes quiet", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({
        subtitles: [
          stored(),
          stored({
            key: SIDECAR,
            source: "external",
            stored_subtitle_id: undefined,
            sync: job("pending"),
          }),
        ],
      }),
      [READ]: () => ({ subtitle: onServer }),
    });
    let onServer = stored({ key: SIDECAR, source: "external", sync: job("running") });
    const onTimingChanged = vi.fn();
    const { result } = renderSync({ onTimingChanged });
    await flush();
    const update = (
      status: SubtitleSyncJob["status"],
      extra: Partial<SubtitleSyncJob>,
      timing = { offset_ms: 0, scale: 1 },
    ): PlaybackSubtitleSyncUpdatedPayload => ({
      session_id: "session-1",
      file_id: 42,
      sync_key: SIDECAR,
      timing,
      job: { ...job(status), trigger: "manual", ...extra },
    });

    act(() => result.current.syncUpdated(update("running", { phase: "analyzing", progress: 0.3 })));
    expect(result.current.entries[SIDECAR]?.state.sync).toMatchObject({
      phase: "analyzing",
      progress: 0.3,
    });
    // A fresh push makes the poll redundant.
    await tick();
    expect(calls(READ)).toHaveLength(0);
    await tick(SYNC_PUSH_FRESH_MS);
    expect(calls(READ)).toHaveLength(1);

    const corrected = { offset_ms: 900, scale: 1 };
    const synced = update("synced", { result: corrected }, corrected);
    onServer = stored({ key: SIDECAR, source: "external", sync: synced.job, timing: corrected });
    act(() => result.current.syncUpdated(synced));
    expect(result.current.entries[SIDECAR]?.state.sync?.status).toBe("synced");
    // The result reloads the cues, even if the timing event never arrives;
    // the timing event that follows it reuses that reload.
    expect(onTimingChanged).toHaveBeenCalledExactlyOnceWith(SIDECAR);
    act(() => result.current.timingChanged(SIDECAR));
    await flush();
    expect(onTimingChanged).toHaveBeenCalledOnce();

    // A timing event about a newer change than the result still reloads,
    // once its read shows a timing other than the one just loaded.
    const later = { offset_ms: 1100, scale: 1 };
    onServer = { ...onServer, timing: { offset_ms: 1200, scale: 1 } };
    act(() => result.current.syncUpdated(update("synced", { result: later }, later)));
    expect(onTimingChanged).toHaveBeenCalledTimes(2);
    act(() => result.current.timingChanged(SIDECAR));
    await flush();
    expect(onTimingChanged).toHaveBeenCalledTimes(3);
  });

  it("keeps a pushed result over the answer to a read sent before it", async () => {
    let answer: (value: unknown) => void = () => {};
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({
        subtitles: [stored({ key: SIDECAR, source: "external", sync: job("running") })],
      }),
      [READ]: () =>
        new Promise((resolve) => {
          answer = resolve;
        }),
    });
    const onTimingChanged = vi.fn();
    const { result } = renderSync({ onTimingChanged });
    await flush();
    await tick();
    expect(calls(READ)).toHaveLength(1);

    const timing = { offset_ms: 900, scale: 1 };
    act(() =>
      result.current.syncUpdated({
        session_id: "session-1",
        file_id: 42,
        sync_key: SIDECAR,
        timing,
        job: job("synced", timing),
      }),
    );
    await act(async () => {
      answer({ subtitle: stored({ key: SIDECAR, source: "external", sync: job("running") }) });
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(result.current.entries[SIDECAR]?.state.sync?.status).toBe("synced");
    expect(result.current.entries[SIDECAR]?.state.timing).toEqual(timing);
    // The result reloaded the cues once; the stale answer's old timing did
    // not read as a second change.
    expect(onTimingChanged).toHaveBeenCalledExactlyOnceWith(SIDECAR);
  });

  it("ignores a late realtime update about an older job", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({
        subtitles: [stored({ key: SIDECAR, source: "external", sync: job("synced") })],
      }),
      [READ]: () => ({
        subtitle: stored({ key: SIDECAR, source: "external", sync: job("running") }),
      }),
    });
    const onTimingChanged = vi.fn();
    const { result } = renderSync({ onTimingChanged });
    await flush();
    const push = (job: SubtitleSyncJob, timing = { offset_ms: 0, scale: 1 }) =>
      act(() =>
        result.current.syncUpdated({
          session_id: "session-1",
          file_id: 42,
          sync_key: SIDECAR,
          timing,
          job,
        }),
      );
    const newer = job("running", undefined, {
      id: "51",
      created_at: "2026-01-02T03:05:00.000Z",
      phase: "analyzing",
      progress: 0.4,
    });
    push(newer);
    push(job("synced", { offset_ms: 700, scale: 1 }), { offset_ms: 700, scale: 1 });
    // The finished job cannot start running again either.
    push(job("running", undefined, { id: "51", created_at: newer.created_at }));
    push({ ...newer, status: "synced", progress: undefined });
    push({ ...newer, status: "running", progress: 0.5 });

    expect(result.current.entries[SIDECAR]?.state.sync).toMatchObject({
      id: "51",
      status: "synced",
    });
    expect(result.current.entries[SIDECAR]?.state.timing).toEqual({ offset_ms: 0, scale: 1 });
    expect(onTimingChanged).not.toHaveBeenCalled();
  });

  it("forgets a subtitle the server no longer has", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({
        subtitles: [stored(), stored({ key: SIDECAR, source: "external", sync: job("running") })],
      }),
      [READ]: () => {
        throw new PlayerFetchError(404, "Not Found", "not_found");
      },
    });
    const { result } = renderSync();
    await flush();
    await tick();
    expect(calls(READ)).toHaveLength(1);
    expect(result.current.entries[SIDECAR]).toBeUndefined();
    expect(result.current.entries[KEY]).toBeDefined();
  });

  it("forgets a subtitle a later list no longer names", async () => {
    let listed = [stored(), stored({ key: SIDECAR, source: "external", sync: job("running") })];
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: listed }),
      [READ]: () => ({ subtitle: listed[1] }),
    });
    const { result } = renderSync();
    await flush();
    expect(result.current.entries[SIDECAR]).toBeDefined();
    listed = [stored()];
    act(() => result.current.reload());
    await flush();
    expect(result.current.entries[SIDECAR]).toBeUndefined();
    expect(result.current.entries[KEY]).toBeDefined();
  });

  it("keeps a subtitle a realtime update named after a read that found it gone", async () => {
    let fail: (err: unknown) => void = () => {};
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({
        subtitles: [stored({ key: SIDECAR, source: "external", sync: job("running") })],
      }),
      [READ]: () =>
        new Promise((_, reject) => {
          fail = reject;
        }),
    });
    const { result } = renderSync();
    await flush();
    await tick();
    expect(calls(READ)).toHaveLength(1);
    act(() =>
      result.current.syncUpdated({
        session_id: "session-1",
        file_id: 42,
        sync_key: SIDECAR,
        timing: { offset_ms: 0, scale: 1 },
        job: job("running", undefined, { phase: "matching", progress: 0.95 }),
      }),
    );
    await act(async () => {
      fail(new PlayerFetchError(404, "Not Found", "not_found"));
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(result.current.entries[SIDECAR]?.state.sync?.phase).toBe("matching");
  });

  it("reads again for a timing event that came while a read was in flight", async () => {
    let answerFirst: (value: unknown) => void = () => {};
    const running = stored({ key: SIDECAR, source: "external", sync: job("running") });
    let reads = 0;
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [running] }),
      [READ]: () => {
        reads++;
        if (reads === 1) {
          return new Promise((resolve) => {
            answerFirst = resolve;
          });
        }
        return {
          subtitle: { ...running, sync: job("synced"), timing: { offset_ms: 1200, scale: 1 } },
        };
      },
    });
    const onTimingChanged = vi.fn();
    const { result } = renderSync({ onTimingChanged });
    await flush();
    await tick();
    expect(calls(READ)).toHaveLength(1);

    // The result reloads the track; its timing event, coalesced with that
    // reload, comes while the poll is in flight.
    const timing = { offset_ms: 900, scale: 1 };
    act(() =>
      result.current.syncUpdated({
        session_id: "session-1",
        file_id: 42,
        sync_key: SIDECAR,
        timing,
        job: job("synced", timing),
      }),
    );
    act(() => result.current.timingChanged(SIDECAR));
    expect(onTimingChanged).toHaveBeenCalledOnce();
    await act(async () => {
      answerFirst({ subtitle: running });
      await vi.advanceTimersByTimeAsync(0);
    });
    // The poll's answer predates the result and is dropped; the read that
    // follows it finds a newer timing, which reloads the track again.
    expect(calls(READ)).toHaveLength(2);
    expect(result.current.entries[SIDECAR]?.state.timing).toEqual({ offset_ms: 1200, scale: 1 });
    expect(onTimingChanged).toHaveBeenCalledTimes(2);
  });

  it("keeps a timing set after a reset it was waiting on", async () => {
    let answerReset: (value: unknown) => void = () => {};
    const synced = stored({
      key: SIDECAR,
      source: "external",
      sync: job("synced"),
      timing: { offset_ms: 900, scale: 1 },
    });
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [synced] }),
      [READ]: (options) => {
        (options.onResponse as (r: Response) => void)?.(
          new Response(null, { headers: { ETag: '"v1"' } }),
        );
        return { subtitle: synced };
      },
      [TIMING]: () =>
        new Promise((resolve) => {
          answerReset = resolve;
        }),
    });
    const { result } = renderSync();
    await flush();
    let reset: Promise<void> = Promise.resolve();
    act(() => {
      reset = result.current.resetTiming(SIDECAR);
    });
    await flush();
    expect(calls(TIMING)).toHaveLength(1);

    // Another viewer sets a timing after the reset; its update arrives first.
    const later = { offset_ms: 300, scale: 1 };
    act(() =>
      result.current.syncUpdated({
        session_id: "session-1",
        file_id: 42,
        sync_key: SIDECAR,
        timing: later,
        job: job("synced"),
      }),
    );
    await act(async () => {
      answerReset({ subtitle: { ...synced, timing: { offset_ms: 0, scale: 1 } } });
      await reset;
    });
    expect(result.current.entries[SIDECAR]?.state.timing).toEqual(later);
    expect(result.current.entries[SIDECAR]?.busy).toBe(false);
  });

  it("ignores a queued update that arrives after the job ran", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [stored({ key: SIDECAR, source: "external" })] }),
    });
    const { result } = renderSync();
    await flush();
    const push = (status: SubtitleSyncJob["status"], extra: Partial<SubtitleSyncJob>) =>
      act(() =>
        result.current.syncUpdated({
          session_id: "session-1",
          file_id: 42,
          sync_key: SIDECAR,
          timing: { offset_ms: 0, scale: 1 },
          job: job(status, undefined, extra),
        }),
      );
    push("running", { phase: "analyzing", progress: 0.2 });
    push("pending", { phase: "queued", progress: 0 });
    expect(result.current.entries[SIDECAR]?.state.sync).toMatchObject({
      status: "running",
      progress: 0.2,
    });
  });

  it("asks the server about known subtitles when the inventory has none left", async () => {
    let listed = [stored({ key: SIDECAR, source: "external", sync: job("running") })];
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: listed }),
    });
    const { result, rerender } = renderHook(
      ({ keys }: { keys: string[] }) =>
        useSubtitleSync({
          playerConfig: config,
          mediaFileId: 42,
          sessionId: "session-1",
          syncKeys: keys,
        }),
      { initialProps: { keys: [SIDECAR] } },
    );
    await flush();
    expect(result.current.entries[SIDECAR]).toBeDefined();
    listed = [];
    rerender({ keys: [] });
    await flush();
    expect(calls(LIST)).toHaveLength(2);
    expect(result.current.entries[SIDECAR]).toBeUndefined();
  });

  it("reads all subtitles again for an update about one it has not loaded", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [stored()] }),
    });
    const { result } = renderSync();
    await flush();
    expect(calls(LIST)).toHaveLength(1);
    act(() =>
      result.current.syncUpdated({
        session_id: "session-1",
        file_id: 42,
        sync_key: SIDECAR,
        timing: { offset_ms: 0, scale: 1 },
        job: job("pending"),
      }),
    );
    await flush();
    expect(calls(LIST)).toHaveLength(2);
  });

  it("follows the automatic sync of a subtitle this viewer added", async () => {
    serve({
      [STATUS]: () => ({ state: "available" }),
      [LIST]: () => ({ subtitles: [] }),
    });
    const { result } = renderSync();
    await flush();
    const added: StoredSubtitle = {
      id: "7",
      media_file_id: "42",
      provider: "opensubtitles",
      language: "en",
      format: "srt",
      release_name: "Synthetic",
      score: 0,
      hearing_impaired: false,
      created_at: "2026-01-02T03:04:05.000Z",
      timing: { offset_ms: 0, scale: 1 },
      sync: { ...job("pending", undefined, { id: "60" }), subtitle_id: "7" },
    };
    act(() => result.current.remember(added));
    expect(result.current.entries[KEY]).toMatchObject({ watchedJobId: "60", state: { key: KEY } });
  });
});
