import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fetchWithSession = vi.fn();
vi.mock("@/api/client", () => ({
  fetchWithSession: (...args: unknown[]) => fetchWithSession(...args),
}));

import {
  BANDWIDTH_ESTIMATE_TTL_MS,
  measureBandwidthKbps,
  readMeasuredBandwidthKbps,
  recordObservedBandwidthLimitKbps,
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
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    expect(await measureBandwidthKbps(new AbortController().signal)).toBeUndefined();

    const tiny = timedBody(3, 1000, 10);
    fetchWithSession.mockResolvedValue({ res: tiny.res });
    expect(await measureBandwidthKbps(new AbortController().signal, tiny.clock)).toBeUndefined();
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
