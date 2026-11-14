import { afterEach, describe, expect, it, vi } from "vitest";
import {
  BITMAP_SUBTITLE_PAGE_SECONDS,
  createSubtitleWindowFetcher,
  nextSubtitleRetryDelay,
  planSubtitleWindow,
  SUBTITLE_COVERAGE_HEADER,
  SUBTITLE_FETCH_RETRY_MAX_BACKOFF_MS,
  subtitleCoverageEnd,
  subtitleWindowUrl,
  type SubtitleWindowFetcherOptions,
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

  it("covers the in-flight window by its own end, not a fixed window length", () => {
    const inflight = { ...covered, inflightStart: 695, inflightEnd: 750 };
    expect(planSubtitleWindow(inflight, 740, 0, 2)).toEqual({ kind: "none" });
    expect(planSubtitleWindow(inflight, 760, 0, 2)).toEqual({ kind: "fresh", start: 758 });
  });

  it("holds off inside a failure backoff", () => {
    const failing = { ...covered, hasFetched: false, failedAt: 1_000, retryDelayMs: 5_000 };
    expect(planSubtitleWindow(failing, 10, 3_000, 2)).toEqual({ kind: "none" });
    expect(planSubtitleWindow(failing, 10, 7_000, 2).kind).toBe("fresh");
  });
});

describe("subtitleWindowUrl", () => {
  it("uses the requested bitmap page length", () => {
    expect(
      subtitleWindowUrl("https://example.test/sub", 10, "", BITMAP_SUBTITLE_PAGE_SECONDS),
    ).toBe("https://example.test/sub?position=10&duration=120");
  });
});

describe("nextSubtitleRetryDelay", () => {
  it("doubles up to the cap", () => {
    expect(nextSubtitleRetryDelay(0)).toBe(5_000);
    expect(nextSubtitleRetryDelay(5_000)).toBe(10_000);
    expect(nextSubtitleRetryDelay(40_000)).toBe(SUBTITLE_FETCH_RETRY_MAX_BACKOFF_MS);
  });
});

describe("subtitleCoverageEnd", () => {
  it("reads the produced end of a bounded response", () => {
    expect(subtitleCoverageEnd("0.000-126.543")).toBe(126.543);
    expect(subtitleCoverageEnd("120.000-240.000")).toBe(240);
  });

  it("treats a missing, open-ended, or unreadable header as no range", () => {
    expect(subtitleCoverageEnd(null)).toBeNull();
    expect(subtitleCoverageEnd("")).toBeNull();
    expect(subtitleCoverageEnd("600.000-*")).toBeNull();
    expect(subtitleCoverageEnd("garbage")).toBeNull();
  });
});

describe("createSubtitleWindowFetcher", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  /** A streamed response that yields `chunks` in order. */
  function respond(
    chunks: string[],
    ok = true,
    init: { status?: number; headers?: Record<string, string>; json?: unknown } = {},
  ) {
    const encoder = new TextEncoder();
    const pending = [...chunks];
    return Promise.resolve({
      ok,
      status: init.status ?? (ok ? 200 : 503),
      headers: new Headers(init.headers),
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
      json: async () => init.json ?? {},
    });
  }

  /** A response whose body never delivers a chunk; only aborting it ends it. */
  function neverDelivers() {
    return Promise.resolve({
      ok: true,
      status: 200,
      headers: new Headers(),
      body: { getReader: () => ({ read: () => new Promise(() => {}) }) },
      json: async () => ({}),
    });
  }

  function makeFetcher(
    playhead: () => number,
    overrides: Partial<SubtitleWindowFetcherOptions> = {},
  ) {
    const events: string[] = [];
    const fetcher = createSubtitleWindowFetcher({
      windowUrl: (start, duration) => `/sub?position=${start}&duration=${duration}`,
      playhead,
      duration: () => 0,
      seekBackoffSeconds: 10,
      logTag: "test",
      onWindowStart: (fresh) => events.push(fresh ? "start fresh" : "start extend"),
      onChunk: (chunk) => events.push(`chunk ${new TextDecoder().decode(chunk)}`),
      onWindowCommitted: () => events.push("committed"),
      onWindowFailed: () => events.push("failed"),
      ...overrides,
    });
    return { fetcher, events };
  }

  it("commits a window's coverage only once it has streamed in whole", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => respond(["a", "b"])),
    );
    const { fetcher, events } = makeFetcher(() => 100);
    fetcher.maybeFetch();
    await vi.waitFor(() => expect(events).toContain("committed"));
    expect(events).toEqual(["start fresh", "chunk a", "chunk b", "committed"]);
    expect(fetcher.coverage()).toMatchObject({
      hasFetched: true,
      coverageStart: 90,
      windowEnd: 210,
    });
    fetcher.dispose();
  });

  it("leaves a failed window uncovered and backs off before retrying", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn(() => respond([], false));
      vi.stubGlobal("fetch", fetchMock);
      vi.spyOn(console, "error").mockImplementation(() => {});
      const { fetcher, events } = makeFetcher(() => 100);
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

  it("signals a source change once and never retries it", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn(() =>
        respond(["{}"], false, {
          status: 409,
          json: { error: "subtitle_source_changed" },
        }),
      );
      vi.stubGlobal("fetch", fetchMock);
      vi.spyOn(console, "error").mockImplementation(() => {});
      const onSourceChanged = vi.fn();
      const { fetcher, events } = makeFetcher(() => 100, { onSourceChanged });
      fetcher.maybeFetch();
      await vi.waitFor(() => expect(onSourceChanged).toHaveBeenCalledTimes(1));
      expect(events).not.toContain("failed");
      // A later tick of the playhead must not re-fetch a URL that can never
      // succeed, and no retry timer may be pending.
      fetcher.maybeFetch();
      await vi.advanceTimersByTimeAsync(120_000);
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(onSourceChanged).toHaveBeenCalledTimes(1);
      fetcher.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("waits at least Retry-After before retrying a retryable failure", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn(() =>
        respond([], false, { status: 503, headers: { "Retry-After": "10" } }),
      );
      vi.stubGlobal("fetch", fetchMock);
      vi.spyOn(console, "error").mockImplementation(() => {});
      const { fetcher, events } = makeFetcher(() => 100);
      fetcher.maybeFetch();
      await vi.waitFor(() => expect(events).toContain("failed"));
      // The normal backoff is 5 s, but the server asked for 10.
      await vi.advanceTimersByTimeAsync(9_000);
      expect(fetchMock).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(fetchMock).toHaveBeenCalledTimes(2);
      fetcher.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops retrying once the hard failure budget is spent", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn(() => respond([], false, { status: 500 }));
      vi.stubGlobal("fetch", fetchMock);
      vi.spyOn(console, "error").mockImplementation(() => {});
      const onTerminal = vi.fn();
      const { fetcher, events } = makeFetcher(() => 100, { onTerminal });
      fetcher.maybeFetch();
      await vi.waitFor(() => expect(events.filter((e) => e === "failed")).toHaveLength(1));
      await vi.advanceTimersByTimeAsync(5_000);
      await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
      await vi.advanceTimersByTimeAsync(10_000);
      await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
      await vi.advanceTimersByTimeAsync(20_000);
      await vi.waitFor(() => expect(onTerminal).toHaveBeenCalledTimes(1));
      expect(fetchMock).toHaveBeenCalledTimes(4);
      // The playhead keeps moving; a terminal fetcher must stay quiet.
      fetcher.maybeFetch();
      await vi.advanceTimersByTimeAsync(180_000);
      expect(fetchMock).toHaveBeenCalledTimes(4);
      expect(onTerminal).toHaveBeenCalledTimes(1);
      fetcher.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("fetches a short first window and a long extend", async () => {
    const fetchMock = vi.fn((_url: string, _init?: RequestInit) => respond([]));
    vi.stubGlobal("fetch", fetchMock);
    const { fetcher } = makeFetcher(() => 95, {
      firstWindowSeconds: 30,
      windowSeconds: 120,
    });
    fetcher.maybeFetch();
    await vi.waitFor(() => expect(fetcher.coverage().windowEnd).toBe(115));
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/sub?position=85&duration=30");
    fetcher.maybeFetch();
    await vi.waitFor(() => expect(fetcher.coverage().windowEnd).toBe(230));
    expect(fetchMock.mock.calls[1]?.[0]).toBe("/sub?position=110&duration=120");
    fetcher.dispose();
  });

  it("walks whole pages on the page grid instead of playhead-anchored slices", async () => {
    let playhead = 100;
    const fetchMock = vi.fn((_url: string, _init?: RequestInit) => respond([]));
    vi.stubGlobal("fetch", fetchMock);
    const { fetcher } = makeFetcher(() => playhead, {
      seekBackoffSeconds: 0,
      windowSeconds: BITMAP_SUBTITLE_PAGE_SECONDS,
      pageSpanSeconds: BITMAP_SUBTITLE_PAGE_SECONDS,
      prefetchLeadSeconds: 30,
    });

    // The page that contains the playhead, fetched whole: a partial page is a
    // range no later request computes a key for.
    fetcher.maybeFetch();
    await vi.waitFor(() => expect(fetcher.coverage().windowEnd).toBe(120));
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/sub?position=0&duration=120");

    // Nearing the end of coverage the next page is the page after it, not the
    // page containing the playhead (which the client already holds).
    playhead = 95;
    fetcher.maybeFetch();
    await vi.waitFor(() => expect(fetcher.coverage().windowEnd).toBe(240));
    expect(fetchMock.mock.calls[1]?.[0]).toBe("/sub?position=120&duration=120");

    // A seek inside the loaded page keeps the same window on the wire.
    playhead = 200;
    fetcher.maybeFetch();
    expect(fetchMock).toHaveBeenCalledTimes(2);

    // A seek into another page asks for that page whole.
    playhead = 1_000;
    fetcher.maybeFetch();
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock.mock.calls[2]?.[0]).toBe("/sub?position=960&duration=120");
    fetcher.dispose();
  });

  it("commits only the range a partly produced page says it covered and asks for that page again", async () => {
    let playhead = 5;
    const fetchMock = vi.fn((_url: string, _init?: RequestInit) =>
      respond(["a"], true, { headers: { [SUBTITLE_COVERAGE_HEADER]: "0.000-18.000" } }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const { fetcher } = makeFetcher(() => playhead, {
      seekBackoffSeconds: 0,
      windowSeconds: BITMAP_SUBTITLE_PAGE_SECONDS,
      pageSpanSeconds: BITMAP_SUBTITLE_PAGE_SECONDS,
      prefetchLeadSeconds: 30,
    });
    fetcher.maybeFetch();
    await vi.waitFor(() => expect(fetcher.coverage().windowEnd).toBe(18));
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/sub?position=0&duration=120");

    // The page the server has only partly parsed is asked for again, not
    // skipped: committing the requested end would leave 18-120 s marked as
    // covered while nothing there ever arrived.
    playhead = 10;
    fetcher.maybeFetch();
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1]?.[0]).toBe("/sub?position=0&duration=120");
    fetcher.dispose();
  });

  it("keeps a page whole when the response carries no coverage header", async () => {
    const fetchMock = vi.fn((_url: string, _init?: RequestInit) => respond([]));
    vi.stubGlobal("fetch", fetchMock);
    const { fetcher } = makeFetcher(() => 5, {
      seekBackoffSeconds: 0,
      windowSeconds: BITMAP_SUBTITLE_PAGE_SECONDS,
      pageSpanSeconds: BITMAP_SUBTITLE_PAGE_SECONDS,
      prefetchLeadSeconds: 30,
    });
    fetcher.maybeFetch();
    await vi.waitFor(() => expect(fetcher.coverage().windowEnd).toBe(120));
    fetcher.dispose();
  });

  it("keeps a fresh 30/60 window on the wire while the playhead advances", () => {
    let playhead = 1000;
    const fetchMock = vi.fn(() => neverDelivers());
    vi.stubGlobal("fetch", fetchMock);
    const { fetcher } = makeFetcher(() => playhead, {
      seekBackoffSeconds: 30,
      firstWindowSeconds: 60,
      windowSeconds: 120,
    });

    fetcher.maybeFetch();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    // The production 30s backoff + 30s forward window ends at T+30. A
    // playhead that advances during extraction must stay covered, or every
    // timeupdate aborts the request and restarts the same window.
    playhead = 1005;
    fetcher.maybeFetch();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    fetcher.dispose();
  });

  it("revives a terminal fetcher on an explicit seek", async () => {
    let playhead = 100;
    let fail = true;
    const fetchMock = vi.fn(() =>
      fail ? respond([], false, { status: 500 }) : respond(["recovered"]),
    );
    vi.stubGlobal("fetch", fetchMock);
    vi.spyOn(console, "error").mockImplementation(() => {});
    const onTerminal = vi.fn();
    const { fetcher, events } = makeFetcher(() => playhead, {
      maxConsecutiveFailures: 1,
      onTerminal,
    });

    fetcher.maybeFetch();
    await vi.waitFor(() => expect(onTerminal).toHaveBeenCalledTimes(1));
    expect(fetchMock).toHaveBeenCalledTimes(1);

    fail = false;
    playhead = 2000;
    fetcher.maybeFetch(true);
    await vi.waitFor(() => expect(events).toContain("committed"));
    expect(fetchMock).toHaveBeenCalledTimes(2);

    fetcher.dispose();
  });

  it("aborts an in-flight window and starts a fresh short window on a seek", async () => {
    let playhead = 100;
    const signals: AbortSignal[] = [];
    const fetchMock = vi.fn((_url: string, init?: RequestInit) => {
      if (init?.signal) signals.push(init.signal);
      return neverDelivers();
    });
    vi.stubGlobal("fetch", fetchMock);
    const durations: string[] = [];
    const { fetcher } = makeFetcher(() => playhead, {
      firstWindowSeconds: 30,
      windowSeconds: 120,
      windowUrl: (start, duration) => {
        durations.push(`${start}/${duration}`);
        return `/sub?position=${start}&duration=${duration}`;
      },
    });
    fetcher.maybeFetch();
    expect(durations).toEqual(["90/30"]);
    playhead = 2_000;
    fetcher.maybeFetch();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(signals[0]?.aborted).toBe(true);
    expect(durations).toEqual(["90/30", "1990/30"]);
    fetcher.dispose();
  });

  it("logs and retries a stalled window instead of swallowing the abort", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn((_url: string, init?: RequestInit) => {
        const signal = init?.signal;
        return Promise.resolve({
          ok: true,
          status: 200,
          headers: new Headers(),
          body: {
            getReader: () => ({
              read: () =>
                new Promise((_resolve, reject) => {
                  signal?.addEventListener("abort", () =>
                    reject(new DOMException("Aborted", "AbortError")),
                  );
                }),
            }),
          },
          json: async () => ({}),
        });
      });
      vi.stubGlobal("fetch", fetchMock);
      const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
      const { fetcher, events } = makeFetcher(() => 100, { stallTimeoutMs: 1_000 });
      fetcher.maybeFetch();
      await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
      await vi.advanceTimersByTimeAsync(1_000);
      await vi.waitFor(() => expect(events).toContain("failed"));
      expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining("stalled"));
      // A stall is transient, so it retries rather than spending the hard budget.
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
