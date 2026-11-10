import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The speed test's transfer and timing, with the network and the clock faked.
// The schedule is covered in bandwidth-probe.schedule.test.ts, Test speed in
// bandwidth-probe.test.ts.
vi.mock("@/api/v2/request", () => ({
  V2_CLIENT_HEADERS: {},
  v2: vi.fn(async () => ({ features: [] })),
}));
vi.mock("@/api/client", () => ({ fetchWithSession: vi.fn() }));

import { measureBandwidth } from "./bandwidth-measure";
import {
  fetchWithSession,
  hangingProbe,
  PROBE_URL,
  timedBody,
} from "./bandwidth-probe.test-support";

beforeEach(() => {
  fetchWithSession.mockReset();
});

describe("measureBandwidth", () => {
  it("stops a quick test once a window after ramp-up beats the target", async () => {
    // 1 MB every 10 ms is 800 Mbps; the window from 500 ms beats 115 Mbps.
    const body = timedBody(1000, 1_000_000, 10);
    fetchWithSession.mockResolvedValue({ res: body.res });

    const result = await measureBandwidth(
      new AbortController().signal,
      "quick",
      115_000,
      PROBE_URL,
      body.clock,
    );

    expect(result).toEqual({
      kbps: 115_000,
      lower_bound: true,
      bytes: 50_000_000,
      duration_ms: 500,
    });
    expect(body.sent()).toBe(101);
    expect(body.cancel).toHaveBeenCalled();
    expect(fetchWithSession.mock.calls[0]![0]).toBe(`${PROBE_URL}&size=100000000`);
  });

  it("measures a link that delivers the whole quick probe before window 1 ends", async () => {
    // 100 MB in 594 ms, about 1.35 Gbps: no window after the ramp completes.
    const body = timedBody(100, 1_000_000, 6);
    fetchWithSession.mockResolvedValue({ res: body.res });

    const withTarget = await measureBandwidth(
      new AbortController().signal,
      "quick",
      115_000,
      PROBE_URL,
      body.clock,
    );
    expect(withTarget).toEqual({
      kbps: 115_000,
      lower_bound: true,
      bytes: 100_000_000,
      duration_ms: 594,
    });

    const again = timedBody(100, 1_000_000, 6);
    fetchWithSession.mockResolvedValue({ res: again.res });
    expect(
      await measureBandwidth(
        new AbortController().signal,
        "quick",
        undefined,
        PROBE_URL,
        again.clock,
      ),
    ).toEqual({ kbps: 1_000_000, lower_bound: true, bytes: 100_000_000, duration_ms: 594 });
  });

  it("returns nothing for a failed or too-short probe", async () => {
    const cancel = vi.fn(async () => undefined);
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: { cancel } } });
    expect(await measureBandwidth(new AbortController().signal, "quick")).toBeUndefined();
    expect(cancel).toHaveBeenCalled();

    const tiny = timedBody(3, 1000, 10);
    fetchWithSession.mockResolvedValue({ res: tiny.res });
    expect(
      await measureBandwidth(
        new AbortController().signal,
        "quick",
        undefined,
        PROBE_URL,
        tiny.clock,
      ),
    ).toBeUndefined();
  });

  // WEB-MEASURE-1: the vectors' `end` is when the body ends, which can come
  // after its last byte.
  it("times a whole transfer to the end of the body", async () => {
    // 100 MB whose last byte arrives at 594 ms and whose body ends at 900 ms.
    let time = 0;
    let sent = 0;
    const reader = {
      read: async () => {
        if (sent >= 100) {
          time = 900;
          return { done: true, value: undefined };
        }
        if (sent > 0) time += 6;
        sent++;
        return { done: false, value: new Uint8Array(1_000_000) };
      },
      cancel: vi.fn(async () => undefined),
    };
    fetchWithSession.mockResolvedValue({ res: { ok: true, body: { getReader: () => reader } } });
    expect(
      await measureBandwidth(
        new AbortController().signal,
        "quick",
        undefined,
        PROBE_URL,
        () => time,
      ),
    ).toEqual({ kbps: 888_888, lower_bound: true, bytes: 100_000_000, duration_ms: 900 });
  });

  // D22: a 429 keeps its Retry-After; a missing or unreadable one holds for
  // 30 s, and a wait is at least 1 s.
  it.each([
    ["19", 19],
    [null, 30],
    ["", 30],
    ["Wed, 21 Oct 2026 07:28:00 GMT", 30],
    ["0", 1],
    ["1.5", 2],
  ])("reads Retry-After %j as %i seconds", async (header, seconds) => {
    fetchWithSession.mockResolvedValue({
      res: { status: 429, headers: new Headers(header === null ? {} : { "Retry-After": header }) },
    });
    await expect(measureBandwidth(new AbortController().signal, "quick")).rejects.toMatchObject({
      retryAfter: seconds,
    });
  });
});

describe("measureBandwidth deadlines", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("ends a stalled transfer at the time limit and counts the silence", async () => {
    // 1 MB every 80 ms (100 Mbps) for 2.6 s, then the body goes silent.
    const t0 = Date.now();
    const clock = () => Date.now() - t0;
    let sent = 0;
    let resolveStalled: ((r: { done: boolean }) => void) | undefined;
    const reader = {
      read: () =>
        new Promise<{ done: boolean; value?: Uint8Array }>((resolve) => {
          if (sent < 33) {
            setTimeout(
              () => resolve({ done: false, value: new Uint8Array(1_000_000) }),
              sent++ ? 80 : 0,
            );
          } else {
            resolveStalled = resolve;
          }
        }),
      cancel: vi.fn(async () => resolveStalled?.({ done: true })),
    };
    fetchWithSession.mockResolvedValue({ res: { ok: true, body: { getReader: () => reader } } });

    const pending = measureBandwidth(
      new AbortController().signal,
      "quick",
      undefined,
      PROBE_URL,
      clock,
    );
    await vi.advanceTimersByTimeAsync(4_000);
    const result = await pending;

    expect(reader.cancel).toHaveBeenCalled();
    // The final second, 2.0–3.0 s, holds chunks at 2.00–2.48 s and one at
    // 2.56 s, then silence: 8 MB in 1 s, not the burst's 100 Mbps.
    expect(result).toEqual({
      kbps: 64_000,
      lower_bound: false,
      bytes: 8_000_000,
      duration_ms: 1000,
    });
  });

  it("abandons a probe whose first byte never arrives", async () => {
    hangingProbe();
    const pending = measureBandwidth(new AbortController().signal, "quick");
    const settled = expect(pending).rejects.toThrow();
    await vi.advanceTimersByTimeAsync(10_000);
    await settled;
  });
});
