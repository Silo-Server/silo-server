import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fetchWithSession = vi.fn();
vi.mock("@/api/client", () => ({
  fetchWithSession: (...args: unknown[]) => fetchWithSession(...args),
}));

import {
  BANDWIDTH_ESTIMATE_TTL_MS,
  cancelBandwidthProbe,
  measureBandwidthKbps,
  readMeasuredBandwidthKbps,
  recordObservedBandwidthLimitKbps,
  scheduleBandwidthProbe,
  stopBandwidthProbes,
} from "./bandwidth-probe";
import { storage } from "@/utils/storage";

/** A body that yields `chunks` of `chunkBytes`, advancing a fake clock `stepMs` per chunk. */
function timedBody(chunks: number, chunkBytes: number, stepMs: number) {
  let time = 0;
  let sent = 0;
  const cancel = vi.fn(async () => undefined);
  const reader = {
    read: async () => {
      if (sent >= chunks) return { done: true, value: undefined };
      sent++;
      time += stepMs;
      return { done: false, value: new Uint8Array(chunkBytes) };
    },
    cancel,
  };
  return {
    res: { ok: true, body: { getReader: () => reader } },
    clock: () => time,
    cancel,
    sent: () => sent,
  };
}

beforeEach(() => {
  storage.remove(storage.KEYS.BANDWIDTH_ESTIMATE);
  fetchWithSession.mockReset();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("measureBandwidthKbps", () => {
  it("times the transfer after ramp-up and stops at the time budget", async () => {
    // 1 MB every 80 ms = 100 Mbps.
    const body = timedBody(1000, 1_000_000, 80);
    fetchWithSession.mockResolvedValue({ res: body.res });

    const kbps = await measureBandwidthKbps(new AbortController().signal, body.clock);

    expect(kbps).toBe(100_000);
    expect(body.sent()).toBeLessThan(1000);
    expect(body.cancel).toHaveBeenCalled();
    expect(fetchWithSession.mock.calls[0]![0]).toMatch(
      /^\/api\/v2\/playback\/bandwidth-probe\?size=\d+$/,
    );
  });

  it("reads a fast link that finishes the whole payload", async () => {
    // 40 MB in 160 ms is 2 Gbps; the contract bound caps it at 1 Gbps.
    const body = timedBody(41, 1_000_000, 4);
    fetchWithSession.mockResolvedValue({ res: body.res });

    expect(await measureBandwidthKbps(new AbortController().signal, body.clock)).toBe(1_000_000);
  });

  it("returns nothing for a failed or too-short probe", async () => {
    const cancel = vi.fn(async () => undefined);
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: { cancel } } });
    expect(await measureBandwidthKbps(new AbortController().signal)).toBeUndefined();
    expect(cancel).toHaveBeenCalled();

    const tiny = timedBody(3, 1000, 10);
    fetchWithSession.mockResolvedValue({ res: tiny.res });
    expect(await measureBandwidthKbps(new AbortController().signal, tiny.clock)).toBeUndefined();
  });
});

describe("measureBandwidthKbps deadlines", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("cuts off a read that stops returning and keeps the sample", async () => {
    // 1 MB every 80 ms (100 Mbps) for 1.2 s, then the body goes silent.
    let time = 0;
    let sent = 0;
    let resolveStalled: ((r: { done: boolean }) => void) | undefined;
    const reader = {
      read: () => {
        if (sent < 16) {
          sent++;
          time += 80;
          return Promise.resolve({ done: false, value: new Uint8Array(1_000_000) });
        }
        return new Promise<{ done: boolean; value?: Uint8Array }>((resolve) => {
          resolveStalled = resolve;
        });
      },
      cancel: vi.fn(async () => resolveStalled?.({ done: true })),
    };
    fetchWithSession.mockResolvedValue({ res: { ok: true, body: { getReader: () => reader } } });

    const result = measureBandwidthKbps(new AbortController().signal, () => time);
    await vi.advanceTimersByTimeAsync(3_000);

    expect(await result).toBe(100_000);
  });

  it("abandons a probe whose first byte never arrives", async () => {
    fetchWithSession.mockImplementation(
      (_url: string, init: RequestInit) =>
        new Promise((_resolve, reject) => {
          init.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );

    const result = measureBandwidthKbps(new AbortController().signal);
    const settled = expect(result).rejects.toThrow();
    await vi.advanceTimersByTimeAsync(10_000);
    await settled;
  });
});

describe("measured bandwidth cache", () => {
  it("expires after the TTL", () => {
    const now = 1_000_000_000;
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: 250_000, measuredAt: now }),
    );

    expect(readMeasuredBandwidthKbps(now + 1000)).toBe(250_000);
    expect(readMeasuredBandwidthKbps(now + BANDWIDTH_ESTIMATE_TTL_MS + 1)).toBeUndefined();
  });

  it("ignores malformed entries", () => {
    storage.set(storage.KEYS.BANDWIDTH_ESTIMATE, "{not json");
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: "fast", measuredAt: Date.now() }),
    );
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
  });

  it("only lowers the estimate when playback could not keep up", () => {
    const now = Date.now();
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: 200_000, measuredAt: now }),
    );

    recordObservedBandwidthLimitKbps(56_000, now);
    expect(readMeasuredBandwidthKbps(now)).toBe(56_000);

    recordObservedBandwidthLimitKbps(90_000, now);
    expect(readMeasuredBandwidthKbps(now)).toBe(56_000);
  });

  it("records a limit when nothing was measured", () => {
    recordObservedBandwidthLimitKbps(14_000);
    expect(readMeasuredBandwidthKbps()).toBe(14_000);
  });
});

describe("scheduleBandwidthProbe", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000_000_000);
  });
  afterEach(() => {
    stopBandwidthProbes();
    vi.useRealTimers();
  });

  it("waits for a fresh measurement to expire, then measures again", async () => {
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: 250_000, measuredAt: Date.now() }),
    );
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    scheduleBandwidthProbe(0);

    await vi.advanceTimersByTimeAsync(BANDWIDTH_ESTIMATE_TTL_MS - 1000);
    expect(fetchWithSession).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(5_000);
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
  });

  it("keeps trying after a probe is cancelled for playback", async () => {
    let release: (value: unknown) => void = () => undefined;
    fetchWithSession.mockImplementationOnce(
      (_url: string, init: RequestInit) =>
        new Promise((resolve, reject) => {
          release = resolve;
          init.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    scheduleBandwidthProbe(0);
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchWithSession).toHaveBeenCalledTimes(1);

    cancelBandwidthProbe();
    release(undefined);
    await vi.advanceTimersByTimeAsync(5 * 60 * 1000);
    expect(fetchWithSession).toHaveBeenCalledTimes(2);
  });

  it("does nothing after it is stopped", async () => {
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    const stop = scheduleBandwidthProbe(1000);
    stop();
    await vi.advanceTimersByTimeAsync(BANDWIDTH_ESTIMATE_TTL_MS * 2);
    expect(fetchWithSession).not.toHaveBeenCalled();
  });
});
