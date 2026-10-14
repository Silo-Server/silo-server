import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createSubtitleWindowFetcher,
  nextSubtitleRetryDelay,
  planSubtitleWindow,
  SUBTITLE_FETCH_RETRY_MAX_BACKOFF_MS,
  type SubtitleWindowState,
} from "./subtitleWindows";

const covered: SubtitleWindowState = {
  hasFetched: true,
  coverageStart: 100,
  windowEnd: 700,
  atEOF: false,
  inflightStart: null,
  failedAt: 0,
  retryDelayMs: 0,
};

describe("planSubtitleWindow", () => {
  it("starts fresh before anything is loaded and after a seek out of coverage", () => {
    expect(planSubtitleWindow({ ...covered, hasFetched: false }, 50, 0, 2)).toEqual({
      kind: "fresh",
      start: 48,
    });
    expect(planSubtitleWindow(covered, 90, 0, 10)).toEqual({ kind: "fresh", start: 80 });
    expect(planSubtitleWindow(covered, 800, 0, 2)).toEqual({ kind: "fresh", start: 798 });
  });

  it("extends near the end of coverage unless at end of file", () => {
    expect(planSubtitleWindow(covered, 680, 0, 2)).toEqual({ kind: "extend", start: 695 });
    expect(planSubtitleWindow({ ...covered, atEOF: true }, 680, 0, 2)).toEqual({ kind: "none" });
    expect(planSubtitleWindow(covered, 300, 0, 2)).toEqual({ kind: "none" });
  });

  it("extends from the end of coverage when the playhead has already passed it", () => {
    expect(planSubtitleWindow(covered, 700.8, 0, 2)).toEqual({ kind: "extend", start: 695 });
  });

  it("waits for a window on the wire that covers the playhead, and replaces one that does not", () => {
    expect(planSubtitleWindow({ ...covered, inflightStart: 695 }, 690, 0, 2)).toEqual({
      kind: "none",
    });
    expect(planSubtitleWindow({ ...covered, inflightStart: 695 }, 2000, 0, 2)).toEqual({
      kind: "fresh",
      start: 1998,
    });
  });

  it("holds off inside a failure backoff", () => {
    const failing = { ...covered, hasFetched: false, failedAt: 1_000, retryDelayMs: 5_000 };
    expect(planSubtitleWindow(failing, 10, 3_000, 2)).toEqual({ kind: "none" });
    expect(planSubtitleWindow(failing, 10, 7_000, 2).kind).toBe("fresh");
  });
});

describe("nextSubtitleRetryDelay", () => {
  it("doubles up to the cap", () => {
    expect(nextSubtitleRetryDelay(0)).toBe(5_000);
    expect(nextSubtitleRetryDelay(5_000)).toBe(10_000);
    expect(nextSubtitleRetryDelay(40_000)).toBe(SUBTITLE_FETCH_RETRY_MAX_BACKOFF_MS);
  });
});

describe("createSubtitleWindowFetcher", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  /** A streamed response that yields `chunks` in order. */
  function respond(chunks: string[], ok = true) {
    const encoder = new TextEncoder();
    const pending = [...chunks];
    return Promise.resolve({
      ok,
      status: ok ? 200 : 503,
      body: ok
        ? {
            getReader: () => ({
              read: async () => {
                const next = pending.shift();
                return next === undefined
                  ? { done: true, value: undefined }
                  : { done: false, value: encoder.encode(next) };
              },
            }),
          }
        : null,
    });
  }

  function makeFetcher(playhead: number) {
    const events: string[] = [];
    const fetcher = createSubtitleWindowFetcher({
      windowUrl: (start) => `/sub?position=${start}`,
      playhead: () => playhead,
      duration: () => 0,
      seekBackoffSeconds: 10,
      logTag: "test",
      onWindowStart: (fresh) => events.push(fresh ? "start fresh" : "start extend"),
      onChunk: (chunk) => events.push(`chunk ${new TextDecoder().decode(chunk)}`),
      onWindowCommitted: () => events.push("committed"),
      onWindowFailed: () => events.push("failed"),
    });
    return { fetcher, events };
  }

  it("commits a window's coverage only once it has streamed in whole", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => respond(["a", "b"])),
    );
    const { fetcher, events } = makeFetcher(100);
    fetcher.maybeFetch();
    await vi.waitFor(() => expect(events).toContain("committed"));
    expect(events).toEqual(["start fresh", "chunk a", "chunk b", "committed"]);
    expect(fetcher.coverage()).toMatchObject({
      hasFetched: true,
      coverageStart: 90,
      windowEnd: 690,
    });
    fetcher.dispose();
  });

  it("leaves a failed window uncovered and backs off before retrying", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn(() => respond([], false));
      vi.stubGlobal("fetch", fetchMock);
      vi.spyOn(console, "error").mockImplementation(() => {});
      const { fetcher, events } = makeFetcher(100);
      fetcher.maybeFetch();
      await vi.waitFor(() => expect(events).toContain("failed"));
      expect(fetcher.coverage()).toMatchObject({ hasFetched: false, windowEnd: 90 });
      fetcher.maybeFetch();
      expect(fetchMock).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(5_000);
      expect(fetchMock).toHaveBeenCalledTimes(2);
      fetcher.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("starts from carried-over coverage without refetching it", () => {
    const fetchMock = vi.fn(() => respond([]));
    vi.stubGlobal("fetch", fetchMock);
    const fetcher = createSubtitleWindowFetcher({
      windowUrl: (start) => `/sub?position=${start}`,
      playhead: () => 300,
      duration: () => 0,
      seekBackoffSeconds: 10,
      initial: { hasFetched: true, coverageStart: 100, windowEnd: 700, atEOF: false },
      logTag: "test",
      onWindowStart: () => {},
      onChunk: () => {},
      onWindowCommitted: () => {},
      onWindowFailed: () => {},
    });
    fetcher.maybeFetch();
    expect(fetchMock).not.toHaveBeenCalled();
    fetcher.dispose();
  });
});
