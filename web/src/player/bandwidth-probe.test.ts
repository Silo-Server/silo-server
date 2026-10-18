import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Node delivers BroadcastChannel messages between test workers, so another
// test file starting playback could reach this suite. An in-file fake keeps
// cross-tab messages to the channels created here.
class FakeBroadcastChannel {
  static open: FakeBroadcastChannel[] = [];
  onmessage: ((event: MessageEvent) => void) | null = null;
  constructor(readonly name: string) {
    FakeBroadcastChannel.open.push(this);
  }
  postMessage(data: unknown): void {
    for (const peer of FakeBroadcastChannel.open) {
      if (peer !== this && peer.name === this.name) {
        queueMicrotask(() => peer.onmessage?.(new MessageEvent("message", { data })));
      }
    }
  }
  close(): void {
    FakeBroadcastChannel.open = FakeBroadcastChannel.open.filter((peer) => peer !== this);
  }
}
const fetchWithSession = vi.fn();
vi.mock("@/api/v2/request", () => ({
  V2_CLIENT_HEADERS: {},
  v2: vi.fn(async () => ({ features: ["bandwidth_probe_v1"] })),
}));
vi.mock("@/api/client", () => ({
  fetchWithSession: (...args: unknown[]) => fetchWithSession(...args),
}));

import {
  testConnectionSpeed,
  ProbeRateLimited,
  BANDWIDTH_ESTIMATE_TTL_MS,
  cancelBandwidthProbe,
  forgetBandwidthEstimate,
  measureBandwidth,
  readMeasuredBandwidthKbps,
  scheduleBandwidthProbe,
  stopBandwidthProbes,
} from "./bandwidth-probe";
import { v2 } from "@/api/v2/request";
import { storage } from "@/utils/storage";

const measureBandwidthKbps = async (...args: Parameters<typeof measureBandwidth>) =>
  (await measureBandwidth(...args))?.kbps;

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
  // Re-stubbed per test because tests unstub all globals afterwards.
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel);
  forgetBandwidthEstimate();
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

  it("cuts off a read that stops returning and counts the silence", async () => {
    // 1 MB every 80 ms (100 Mbps) for 1.2 s, then the body goes silent.
    const t0 = Date.now();
    const clock = () => Date.now() - t0;
    let sent = 0;
    let resolveStalled: ((r: { done: boolean }) => void) | undefined;
    const reader = {
      read: () =>
        new Promise<{ done: boolean; value?: Uint8Array }>((resolve) => {
          if (sent < 16) {
            sent++;
            setTimeout(() => resolve({ done: false, value: new Uint8Array(1_000_000) }), 80);
          } else {
            resolveStalled = resolve;
          }
        }),
      cancel: vi.fn(async () => resolveStalled?.({ done: true })),
    };
    fetchWithSession.mockResolvedValue({ res: { ok: true, body: { getReader: () => reader } } });

    const result = measureBandwidthKbps(new AbortController().signal, clock);
    await vi.advanceTimersByTimeAsync(4_000);
    const kbps = await result;

    expect(reader.cancel).toHaveBeenCalled();
    // 11 MB after ramp-up over the 2.18 s from the warm mark to the cutoff,
    // not the 100 Mbps of the burst alone.
    expect(kbps).toBe(40_367);
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

  it("discards browser-local limits saved before server learning", () => {
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: 4200, measuredAt: Date.now(), observed: true }),
    );
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
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

  it("leaves the probe to the tab that holds the lock", async () => {
    const request = vi.fn(
      async (_name: string, _options: unknown, callback: (lock: unknown) => Promise<void>) =>
        callback(null),
    );
    vi.stubGlobal("navigator", { ...navigator, locks: { request } });
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    scheduleBandwidthProbe(0);
    await vi.advanceTimersByTimeAsync(0);

    expect(request).toHaveBeenCalledWith(
      "silo-bandwidth-probe",
      { ifAvailable: true },
      expect.any(Function),
    );
    expect(fetchWithSession).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("probes while holding the lock", async () => {
    const request = vi.fn(
      async (_name: string, _options: unknown, callback: (lock: unknown) => Promise<void>) =>
        callback({ name: "silo-bandwidth-probe" }),
    );
    vi.stubGlobal("navigator", { ...navigator, locks: { request } });
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    scheduleBandwidthProbe(0);
    await vi.advanceTimersByTimeAsync(0);

    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });

  it("does not probe while another tab holds the media lock", async () => {
    const request = vi.fn(
      async (_name: string, _options: unknown, callback: (lock: unknown) => Promise<void>) =>
        callback({ name: "silo-bandwidth-probe" }),
    );
    const query = vi.fn(async () => ({
      held: [{ name: "silo-media-active", mode: "shared" }],
      pending: [],
    }));
    vi.stubGlobal("navigator", { ...navigator, locks: { request, query } });
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    scheduleBandwidthProbe(0);
    await vi.advanceTimersByTimeAsync(0);

    expect(query).toHaveBeenCalled();
    expect(fetchWithSession).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("does not start a probe whose lock arrives after playback started", async () => {
    let grant: (() => Promise<void>) | undefined;
    const request = vi.fn(
      (name: string, _options: unknown, callback: (lock: unknown) => Promise<void>) =>
        name === "silo-bandwidth-probe"
          ? new Promise<void>((resolve) => {
              grant = () => callback({ name }).then(resolve);
            })
          : Promise.resolve(),
    );
    vi.stubGlobal("navigator", { ...navigator, locks: { request } });
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    scheduleBandwidthProbe(0);
    await vi.advanceTimersByTimeAsync(0);
    expect(request).toHaveBeenCalledTimes(1);

    cancelBandwidthProbe();
    await grant!();
    expect(fetchWithSession).not.toHaveBeenCalled();
    expect(request).toHaveBeenCalledWith(
      "silo-media-active",
      { mode: "shared" },
      expect.any(Function),
    );
    vi.unstubAllGlobals();
  });

  it("holds off a scheduled probe while playback is starting", async () => {
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    scheduleBandwidthProbe(5_000);
    cancelBandwidthProbe();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(fetchWithSession).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(5 * 60 * 1000);
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
  });

  it("abandons a running probe when another tab starts playback", async () => {
    fetchWithSession.mockImplementation(
      (_url: string, init: RequestInit) =>
        new Promise((_resolve, reject) => {
          init.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );
    scheduleBandwidthProbe(0);
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    const signal = (fetchWithSession.mock.calls[0]![1] as RequestInit).signal!;

    // Another tab's playback start, as this tab's channel receives it.
    const otherTab = new FakeBroadcastChannel("silo-playback-start");
    otherTab.postMessage("start");
    otherTab.close();
    await vi.waitFor(() => expect(signal.aborted).toBe(true));
  });

  it("lets playback start when the browser refuses cross-tab channels", () => {
    vi.stubGlobal(
      "BroadcastChannel",
      class {
        constructor() {
          throw new DOMException("denied", "SecurityError");
        }
      },
    );
    try {
      // A stopped schedule has no channel, so the next start creates one.
      stopBandwidthProbes();
      expect(() => cancelBandwidthProbe()).not.toThrow();
      expect(() => scheduleBandwidthProbe(1_000)).not.toThrow();
    } finally {
      vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel);
    }
  });

  it("forgets the measurement on a new network, not on a new estimate", async () => {
    const connection = Object.assign(new EventTarget(), { type: "wifi", downlink: 10 });
    vi.stubGlobal("navigator", { ...navigator, connection });
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: 250_000, measuredAt: Date.now() }),
    );
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    scheduleBandwidthProbe(0);

    connection.downlink = 1.5;
    connection.dispatchEvent(new Event("change"));
    await vi.advanceTimersByTimeAsync(60_000);
    expect(readMeasuredBandwidthKbps()).toBe(250_000);
    expect(fetchWithSession).not.toHaveBeenCalled();

    connection.type = "cellular";
    connection.dispatchEvent(new Event("change"));
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
  });

  it("does nothing after it is stopped", async () => {
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    const stop = scheduleBandwidthProbe(1000);
    stop();
    await vi.advanceTimersByTimeAsync(BANDWIDTH_ESTIMATE_TTL_MS * 2);
    expect(fetchWithSession).not.toHaveBeenCalled();
  });
});

describe("reported connection speed", () => {
  it("uses the advertised media path and reports the timed sample", async () => {
    stopBandwidthProbes();
    const body = timedBody(1000, 1_000_000, 80);
    fetchWithSession.mockResolvedValue({ res: body.res });
    const clock = vi.spyOn(performance, "now").mockImplementation(body.clock);
    vi.mocked(v2)
      .mockResolvedValueOnce({
        features: ["connection_speed_v1"],
        bandwidth_probe_url: "/api/v2/playback/bandwidth-probe?media_path=true",
      } as never)
      .mockResolvedValueOnce(undefined as never);
    expect(await testConnectionSpeed()).toBe(100000);
    expect(fetchWithSession.mock.calls[0]?.[0]).toContain("media_path=true&size=40000000");
    expect(v2).toHaveBeenCalledWith("POST /api/v2/playback/bandwidth-probe/results", {
      body: expect.objectContaining({
        kbps: 100000,
        transport: "other",
        bytes: expect.any(Number),
        duration_ms: expect.any(Number),
      }),
    });
    clock.mockRestore();
  });
  async function measureNow() {
    const body = timedBody(1000, 1_000_000, 80);
    fetchWithSession.mockResolvedValue({ res: body.res });
    const clock = vi.spyOn(performance, "now").mockImplementation(body.clock);
    vi.mocked(v2).mockResolvedValueOnce({ features: [] } as never);
    try {
      return await testConnectionSpeed();
    } finally {
      clock.mockRestore();
    }
  }
  it("keeps a measurement for this page when localStorage refuses the write", async () => {
    const set = vi.spyOn(storage, "set").mockImplementation(() => undefined);
    try {
      expect(await measureNow()).toBe(100000);
      expect(readMeasuredBandwidthKbps()).toBe(100000);
    } finally {
      set.mockRestore();
    }
  });
  it("prefers a newer result another tab stored over this page's own", async () => {
    expect(await measureNow()).toBe(100000);
    const now = Date.now();
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: 90_000, measuredAt: now + 500 }),
    );
    expect(readMeasuredBandwidthKbps(now + 1_000)).toBe(90_000);
  });
  it("rejects a test that ran and measured nothing", async () => {
    fetchWithSession.mockResolvedValue({ res: { ok: false, status: 503, body: null } });
    vi.mocked(v2).mockResolvedValueOnce({ features: [] } as never);
    await expect(testConnectionSpeed()).rejects.toThrow("could not be measured");
  });
  it("preserves Retry-After for a foreground rate-limit message", async () => {
    fetchWithSession.mockResolvedValue({
      res: { status: 429, headers: new Headers({ "Retry-After": "19" }) },
    });
    await expect(measureBandwidthKbps(new AbortController().signal)).rejects.toMatchObject({
      retryAfter: 19,
    });
    expect(new ProbeRateLimited(19).message).toBe("Try again in 19 seconds");
  });
});
