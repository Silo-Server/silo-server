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
  v2: vi.fn(async () => ({ features: [] })),
}));
vi.mock("@/api/client", () => ({
  fetchWithSession: (...args: unknown[]) => fetchWithSession(...args),
}));

import {
  testConnectionSpeed,
  cancelBandwidthProbe,
  forgetBandwidthEstimate,
  readMeasuredBandwidthKbps,
  scheduleBandwidthProbe,
  stopBandwidthProbes,
} from "./bandwidth-probe";
import { v2 } from "@/api/v2/request";
import { storage } from "@/utils/storage";

const DAY = 24 * 60 * 60 * 1000;
const PROBE_URL = "/api/v2/playback/bandwidth-probe?media_path=true";

/** A body that yields `chunks` of `chunkBytes`, advancing a fake clock `stepMs` per chunk. */
function timedBody(chunks: number, chunkBytes: number, stepMs: number) {
  let time = 0;
  let sent = 0;
  const cancel = vi.fn(async () => undefined);
  const reader = {
    read: async () => {
      if (sent >= chunks) return { done: true, value: undefined };
      if (sent > 0) time += stepMs;
      sent++;
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

/** A probe request that never answers until it is aborted. */
function hangingProbe() {
  fetchWithSession.mockImplementation(
    (_url: string, init: RequestInit) =>
      new Promise((_resolve, reject) => {
        init.signal?.addEventListener("abort", () =>
          reject(new DOMException("aborted", "AbortError")),
        );
      }),
  );
}

interface ServerReading {
  probe?: { kbps: number; lower_bound: boolean; ageMs: number };
  sample?: { kbps: number; ageMs: number };
}

/**
 * Answers the capability and connection reads, with the speed test schedule;
 * a reported result becomes the probe the next connection read returns.
 */
function server(options: {
  target?: number;
  automatic?: boolean;
  maxAgeSeconds?: number;
  reading?: ServerReading;
}) {
  let reading = options.reading ?? {};
  const posted: Record<string, unknown>[] = [];
  vi.mocked(v2).mockImplementation((async (operation: string, init?: { body?: unknown }) => {
    if (operation === "GET /api/v2/playback/capabilities")
      return {
        features: ["buffering_quality_v1", "connection_speed_v1"],
        bandwidth_probe_url: PROBE_URL,
        bandwidth_probe_target_kbps: options.target,
        bandwidth_probe_automatic: options.automatic ?? true,
        bandwidth_probe_max_age_seconds: options.maxAgeSeconds ?? 604800,
      };
    if (operation === "GET /api/v2/playback/connection") {
      const at = (ageMs: number) => new Date(Date.now() - ageMs).toISOString();
      return {
        connection: "remote",
        learned_limit: null,
        ...(reading.probe && {
          probe: {
            kbps: reading.probe.kbps,
            lower_bound: reading.probe.lower_bound,
            measured_at: at(reading.probe.ageMs),
            transport: "wifi",
          },
        }),
        ...(reading.sample && {
          playback_sample: { kbps: reading.sample.kbps, measured_at: at(reading.sample.ageMs) },
        }),
      };
    }
    if (operation === "POST /api/v2/playback/bandwidth-probe/results") {
      const body = init!.body as { kbps: number; lower_bound: boolean };
      posted.push(body as Record<string, unknown>);
      reading = { ...reading, probe: { kbps: body.kbps, lower_bound: body.lower_bound, ageMs: 0 } };
      return undefined;
    }
    throw new Error(`unexpected ${operation}`);
  }) as never);
  return { posted };
}

/** How long the schedule lets the first screen settle before it tests. */
const TEST_DELAY_MS = 5_000;

/** Lets the schedule read the server's reading, then waits out the test delay. */
async function readThenTest() {
  await vi.advanceTimersByTimeAsync(0);
  await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
}

function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", { value: state, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

/** Hides the tab for `ms`, then shows it again. */
async function awayFor(ms: number) {
  setVisibility("hidden");
  await vi.advanceTimersByTimeAsync(ms);
  setVisibility("visible");
}

beforeEach(() => {
  // Re-stubbed per test because tests unstub all globals afterwards.
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel);
  forgetBandwidthEstimate();
  fetchWithSession.mockReset();
  vi.mocked(v2).mockReset();
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("kept reading", () => {
  it("lasts its own lifetime", () => {
    const now = 1_000_000_000;
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: 250_000, measuredAt: now, maxAgeMs: 7 * DAY }),
    );
    expect(readMeasuredBandwidthKbps(now + 6 * DAY)).toBe(250_000);
    expect(readMeasuredBandwidthKbps(now + 7 * DAY + 1)).toBeUndefined();
  });

  it("ignores malformed entries and an older build's", () => {
    storage.set(storage.KEYS.BANDWIDTH_ESTIMATE, "{not json");
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: "fast", measuredAt: Date.now(), maxAgeMs: DAY }),
    );
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
    // A 30-minute probe result from before the redesign has no lifetime.
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: 4200, measuredAt: Date.now() }),
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

  it("keeps the server's settled reading and does not test", async () => {
    server({
      target: 115_000,
      reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: 2 * DAY } },
    });
    scheduleBandwidthProbe();
    await readThenTest();

    expect(fetchWithSession).not.toHaveBeenCalled();
    expect(readMeasuredBandwidthKbps()).toBe(48_000);
  });

  it("does not test while a lower bound reaches the target, and sends the best reading", async () => {
    server({
      target: 115_000,
      reading: {
        probe: { kbps: 115_000, lower_bound: true, ageMs: DAY },
        sample: { kbps: 140_000, ageMs: DAY },
      },
    });
    scheduleBandwidthProbe();
    await readThenTest();

    expect(fetchWithSession).not.toHaveBeenCalled();
    expect(readMeasuredBandwidthKbps()).toBe(140_000);
  });

  it("reads the server's reading at once and runs the quick test later when a lower bound is below the target", async () => {
    const { posted } = server({
      target: 200_000,
      reading: { sample: { kbps: 60_000, ageMs: DAY } },
    });
    hangingProbe();
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    // A playback start in the first seconds already sends the reading.
    expect(readMeasuredBandwidthKbps()).toBe(60_000);
    expect(fetchWithSession).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    expect(fetchWithSession.mock.calls[0]![0]).toBe(`${PROBE_URL}&size=100000000`);
    expect(posted).toEqual([]);
    // Until a test finishes, starts send the sample.
    expect(readMeasuredBandwidthKbps()).toBe(60_000);
  });

  it("treats any current reading as proof when the server has no target", async () => {
    server({ reading: { probe: { kbps: 48_000, lower_bound: true, ageMs: DAY } } });
    scheduleBandwidthProbe();
    await readThenTest();

    expect(fetchWithSession).not.toHaveBeenCalled();
    expect(readMeasuredBandwidthKbps()).toBe(48_000);
  });

  it("tests when the reading has outlived its lifetime", async () => {
    server({
      maxAgeSeconds: 86400,
      reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: 2 * DAY } },
    });
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();

    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
  });

  it("reports a test and keeps the reading the server returns", async () => {
    const { posted } = server({ target: 115_000 });
    const body = timedBody(1000, 1_000_000, 10);
    fetchWithSession.mockResolvedValue({ res: body.res });
    const clock = vi.spyOn(performance, "now").mockImplementation(body.clock);
    try {
      scheduleBandwidthProbe();
      await readThenTest();
      await vi.waitFor(() => expect(posted).toHaveLength(1));
    } finally {
      clock.mockRestore();
    }
    expect(posted[0]).toEqual({
      kbps: 115_000,
      lower_bound: true,
      bytes: 50_000_000,
      duration_ms: 500,
      transport: "other",
    });
    await vi.waitFor(() => expect(readMeasuredBandwidthKbps()).toBe(115_000));
  });

  it("runs no automatic test when the administrator turned them off", async () => {
    server({ automatic: false });
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).not.toHaveBeenCalled();
  });

  it("looks again only when the tab is shown, never on a timer", async () => {
    server({});
    fetchWithSession.mockResolvedValue({ res: { ok: false, body: null } });
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(30 * DAY);
    expect(fetchWithSession).toHaveBeenCalledTimes(1);

    await awayFor(60_000);
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(2);
  });

  it("sends nothing after a quick tab switch when the reading proves the link", async () => {
    server({ reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: DAY } } });
    scheduleBandwidthProbe();
    await readThenTest();
    const reads = vi.mocked(v2).mock.calls.length;

    await awayFor(59_000);
    await readThenTest();
    expect(vi.mocked(v2).mock.calls.length).toBe(reads);
    expect(readMeasuredBandwidthKbps()).toBe(48_000);
  });

  it("resumes after a quick tab switch the test the switch stopped", async () => {
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(1_000);

    await awayFor(5_000);
    expect(fetchWithSession).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    const signal = (fetchWithSession.mock.calls[0]![1] as RequestInit).signal!;

    // Hiding again stops the test it resumed; the next quick return resumes it once more.
    await awayFor(5_000);
    expect(signal.aborted).toBe(true);
    await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
    expect(fetchWithSession).toHaveBeenCalledTimes(2);
  });

  it("reads after a quick tab switch that came before the first read", async () => {
    server({ reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: DAY } } });
    scheduleBandwidthProbe();
    await awayFor(5_000);
    await vi.advanceTimersByTimeAsync(0);
    expect(readMeasuredBandwidthKbps()).toBe(48_000);
  });

  it("neither looks nor tests in a hidden tab, and hiding stops a running test", async () => {
    server({});
    hangingProbe();
    setVisibility("hidden");
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(v2).not.toHaveBeenCalled();

    setVisibility("visible");
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    const signal = (fetchWithSession.mock.calls[0]![1] as RequestInit).signal!;
    setVisibility("hidden");
    await vi.waitFor(() => expect(signal.aborted).toBe(true));
  });

  it("waits out a 429's Retry-After before testing again", async () => {
    server({});
    fetchWithSession.mockResolvedValue({
      res: { status: 429, headers: new Headers({ "Retry-After": "600" }) },
    });
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);

    await awayFor(60_000);
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);

    await awayFor(600_000);
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(2);
  });

  it("stops a running test when Data Saver turns on without a new network", async () => {
    const connection = Object.assign(new EventTarget(), { type: "wifi", saveData: false });
    vi.stubGlobal("navigator", { ...navigator, connection });
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    const signal = (fetchWithSession.mock.calls[0]![1] as RequestInit).signal!;

    connection.saveData = true;
    connection.dispatchEvent(new Event("change"));
    expect(signal.aborted).toBe(true);
  });

  it("looks again on a new network, not on a new estimate", async () => {
    const connection = Object.assign(new EventTarget(), { type: "wifi", downlink: 10 });
    vi.stubGlobal("navigator", { ...navigator, connection });
    server({ reading: { probe: { kbps: 250_000, lower_bound: false, ageMs: DAY } } });
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    expect(readMeasuredBandwidthKbps()).toBe(250_000);
    const reads = vi.mocked(v2).mock.calls.length;

    connection.downlink = 1.5;
    connection.dispatchEvent(new Event("change"));
    await vi.advanceTimersByTimeAsync(60_000);
    expect(vi.mocked(v2).mock.calls.length).toBe(reads);

    connection.type = "ethernet";
    connection.dispatchEvent(new Event("change"));
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.mocked(v2).mock.calls.length).toBeGreaterThan(reads);
    expect(readMeasuredBandwidthKbps()).toBe(250_000);
  });

  it("leaves the test to the tab that holds the lock", async () => {
    server({});
    const request = vi.fn(
      async (_name: string, _options: unknown, callback: (lock: unknown) => Promise<void>) =>
        callback(null),
    );
    vi.stubGlobal("navigator", { ...navigator, locks: { request } });
    scheduleBandwidthProbe();
    await readThenTest();

    expect(request).toHaveBeenCalledWith(
      "silo-bandwidth-probe",
      { ifAvailable: true },
      expect.any(Function),
    );
    expect(fetchWithSession).not.toHaveBeenCalled();
  });

  it("keeps a refused lock request from escaping the delayed test", async () => {
    server({});
    const request = vi.fn(async () => {
      throw new DOMException("denied", "SecurityError");
    });
    vi.stubGlobal("navigator", { ...navigator, locks: { request } });
    scheduleBandwidthProbe();
    await readThenTest();

    expect(request).toHaveBeenCalled();
    expect(fetchWithSession).not.toHaveBeenCalled();
  });

  it("does not start a test when playback starts during the media-lock check", async () => {
    server({});
    const request = vi.fn(
      async (_name: string, _options: unknown, callback: (lock: unknown) => Promise<void>) =>
        callback({ name: "silo-bandwidth-probe" }),
    );
    const query = vi.fn(async () => {
      cancelBandwidthProbe();
      return { held: [], pending: [] };
    });
    vi.stubGlobal("navigator", { ...navigator, locks: { request, query } });
    scheduleBandwidthProbe();
    await readThenTest();

    expect(query).toHaveBeenCalled();
    expect(fetchWithSession).not.toHaveBeenCalled();
  });

  it("ignores a reading under 100 kbps, which no start could send", async () => {
    server({ reading: { sample: { kbps: 99, ageMs: DAY } } });
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();

    expect(readMeasuredBandwidthKbps()).toBeUndefined();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
  });

  it("keeps a reading the server dated ahead of this device's clock", async () => {
    server({ reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: -60_000 } } });
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);

    expect(readMeasuredBandwidthKbps()).toBe(48_000);
  });

  it("does not test while another tab holds the media lock", async () => {
    server({});
    const request = vi.fn(
      async (_name: string, _options: unknown, callback: (lock: unknown) => Promise<void>) =>
        callback({ name: "silo-bandwidth-probe" }),
    );
    const query = vi.fn(async () => ({
      held: [{ name: "silo-media-active", mode: "shared" }],
      pending: [],
    }));
    vi.stubGlobal("navigator", { ...navigator, locks: { request, query } });
    scheduleBandwidthProbe();
    await readThenTest();

    expect(query).toHaveBeenCalled();
    expect(fetchWithSession).not.toHaveBeenCalled();
  });

  it("does not start a test whose lock arrives after playback started", async () => {
    server({});
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
    scheduleBandwidthProbe();
    await readThenTest();
    expect(request).toHaveBeenCalledTimes(1);

    cancelBandwidthProbe();
    await grant!();
    expect(fetchWithSession).not.toHaveBeenCalled();
    expect(request).toHaveBeenCalledWith(
      "silo-media-active",
      { mode: "shared" },
      expect.any(Function),
    );
  });

  it("holds off while playback is starting", async () => {
    server({});
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    cancelBandwidthProbe();
    await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
    expect(fetchWithSession).not.toHaveBeenCalled();
  });

  it("abandons a running test when another tab starts playback", async () => {
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
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

  it("answers Test speed with busy while an automatic test measures", async () => {
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    expect(await testConnectionSpeed()).toBe("busy");
  });

  it("does nothing after it is stopped", async () => {
    server({});
    const stop = scheduleBandwidthProbe(1000);
    stop();
    await vi.advanceTimersByTimeAsync(30 * DAY);
    expect(v2).not.toHaveBeenCalled();
  });
});

describe("Test speed", () => {
  it("answers playing when playback stops it mid-test", async () => {
    stopBandwidthProbes();
    server({ target: 115_000 });
    hangingProbe();
    const result = testConnectionSpeed();
    await vi.waitFor(() => expect(fetchWithSession).toHaveBeenCalledTimes(1));

    cancelBandwidthProbe();
    await expect(result).resolves.toBe("playing");
    stopBandwidthProbes();
  });

  it("runs the accurate test, even with a current reading, and reports it", async () => {
    stopBandwidthProbes();
    const { posted } = server({
      target: 115_000,
      reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: 0 } },
    });
    const body = timedBody(1000, 1_000_000, 10);
    fetchWithSession.mockResolvedValue({ res: body.res });
    const clock = vi.spyOn(performance, "now").mockImplementation(body.clock);
    try {
      expect(await testConnectionSpeed()).toMatchObject({ kbps: 800_000, lower_bound: false });
    } finally {
      clock.mockRestore();
    }
    expect(fetchWithSession.mock.calls[0]?.[0]).toBe(`${PROBE_URL}&size=1000000000`);
    expect(posted[0]).toMatchObject({
      kbps: 800_000,
      lower_bound: false,
      transport: "other",
      duration_ms: 3000,
    });
    expect(readMeasuredBandwidthKbps()).toBe(800_000);
  });

  it("keeps the reading for this page when localStorage refuses the write", async () => {
    server({});
    const body = timedBody(1000, 1_000_000, 10);
    fetchWithSession.mockResolvedValue({ res: body.res });
    const clock = vi.spyOn(performance, "now").mockImplementation(body.clock);
    const set = vi.spyOn(storage, "set").mockImplementation(() => undefined);
    try {
      await testConnectionSpeed();
      expect(readMeasuredBandwidthKbps()).toBe(800_000);
    } finally {
      set.mockRestore();
      clock.mockRestore();
    }
  });

  it("rejects a test that ran and measured nothing", async () => {
    server({});
    fetchWithSession.mockResolvedValue({ res: { ok: false, status: 503, body: null } });
    await expect(testConnectionSpeed()).rejects.toThrow("could not be measured");
  });
});
