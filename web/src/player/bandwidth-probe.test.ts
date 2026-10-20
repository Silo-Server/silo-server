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
  mediaElementReleased,
  ProbeNotSaved,
  ProbeRateLimited,
  readMeasuredBandwidthKbps,
  resetBandwidthProbesForTests,
  scheduleBandwidthProbe,
  stopBandwidthProbes,
} from "./bandwidth-probe";
import { trackDetachedMedia } from "@/lib/detachedMedia";
import { queryClient } from "@/lib/query-client";
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

/**
 * A Web Lock manager that grants every request at once and lists what is
 * held, so tests can see this tab's media holds come and go.
 */
function fakeLocks() {
  const held: { name: string; mode: string }[] = [];
  const locks = {
    held,
    request: vi.fn(
      async (
        name: string,
        options: { mode?: string; ifAvailable?: boolean },
        callback: (lock: unknown) => unknown,
      ) => {
        const entry = { name, mode: options.mode ?? "exclusive" };
        if (options.ifAvailable && held.some((lock) => lock.name === name)) return callback(null);
        held.push(entry);
        try {
          return await callback(entry);
        } finally {
          held.splice(held.indexOf(entry), 1);
        }
      },
    ),
    query: vi.fn(async () => ({ held: [...held], pending: [] })),
  };
  vi.stubGlobal("navigator", { ...navigator, locks });
  return locks;
}

/** A media element in the page whose playing state the test sets. */
function pageVideo() {
  const video = document.createElement("video");
  let paused = true;
  Object.defineProperty(video, "paused", { get: () => paused, configurable: true });
  document.body.append(video);
  return {
    video,
    play() {
      paused = false;
      video.dispatchEvent(new Event("playing"));
    },
    pause() {
      paused = true;
      video.dispatchEvent(new Event("pause"));
    },
  };
}

/** Lets pending lock grants and releases settle. */
async function settle() {
  for (let i = 0; i < 10; i++) await Promise.resolve();
}

/** A reading this device measured itself, as a server without connection_speed_v1 leaves it. */
function keptReading(kbps: number, measuredAt: number, maxAgeMs: number, network = "unknown") {
  return JSON.stringify({
    readings: [{ kbps, measuredAt, maxAgeMs, network }],
    writtenAt: measuredAt,
  });
}

beforeEach(() => {
  // Re-stubbed per test because tests unstub all globals afterwards.
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel);
  resetBandwidthProbesForTests();
  forgetBandwidthEstimate();
  queryClient.clear();
  fetchWithSession.mockReset();
  vi.mocked(v2).mockReset();
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe("kept reading", () => {
  it("lasts its own lifetime", () => {
    const now = 1_000_000_000;
    storage.set(storage.KEYS.BANDWIDTH_ESTIMATE, keptReading(250_000, now, 7 * DAY));
    expect(readMeasuredBandwidthKbps(now + 6 * DAY)).toBe(250_000);
    expect(readMeasuredBandwidthKbps(now + 7 * DAY + 1)).toBeUndefined();
  });

  it("ignores malformed entries and an older build's", () => {
    storage.set(storage.KEYS.BANDWIDTH_ESTIMATE, "{not json");
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({
        readings: [{ kbps: "fast", measuredAt: Date.now(), maxAgeMs: DAY, network: "unknown" }],
        writtenAt: Date.now(),
      }),
    );
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
    // A 30-minute probe result from before the redesign has no lifetime, and
    // a single reading from before the reading list is replaced by the next look.
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: 4200, measuredAt: Date.now() }),
    );
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({ kbps: 250_000, measuredAt: Date.now(), maxAgeMs: DAY }),
    );
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
  });

  // WEB-SCHEDULE-12: against a server that keeps nothing, a result describes
  // the connection type it was measured on.
  it("keeps a result this device measured for the connection type it was taken on", () => {
    const now = Date.now();
    vi.stubGlobal("navigator", { ...navigator, connection: { type: "wifi" } });
    storage.set(storage.KEYS.BANDWIDTH_ESTIMATE, keptReading(250_000, now, DAY, "ethernet"));
    expect(readMeasuredBandwidthKbps(now)).toBeUndefined();
    storage.set(storage.KEYS.BANDWIDTH_ESTIMATE, keptReading(250_000, now, DAY, "wifi"));
    expect(readMeasuredBandwidthKbps(now)).toBe(250_000);
  });

  // WEB-SCHEDULE-3 and -11: a server's reading from an earlier session may
  // belong to another network or account.
  it("does not send a server's reading kept from before this page read the server", () => {
    storage.set(
      storage.KEYS.BANDWIDTH_ESTIMATE,
      JSON.stringify({
        readings: [{ kbps: 250_000, measuredAt: Date.now(), maxAgeMs: DAY }],
        writtenAt: Date.now(),
      }),
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

  // WEB-SCHEDULE-9: two tests at once would only split the link.
  it("lets Test speed replace a running automatic test", async () => {
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    const automatic = (fetchWithSession.mock.calls[0]![1] as RequestInit).signal!;

    const result = testConnectionSpeed();
    await vi.waitFor(() => expect(fetchWithSession).toHaveBeenCalledTimes(2));
    expect(automatic.aborted).toBe(true);
    expect(fetchWithSession.mock.calls[1]![0]).toBe(`${PROBE_URL}&size=1000000000`);
    cancelBandwidthProbe();
    await expect(result).resolves.toBe("playing");
  });

  // WEB-SCHEDULE-4 and -5: the server's hold is not this schedule's to lift.
  it("holds automatic tests after Test speed's 429, across a schedule restart", async () => {
    server({});
    fetchWithSession.mockResolvedValue({
      res: { status: 429, headers: new Headers({ "Retry-After": "600" }) },
    });
    await expect(testConnectionSpeed()).rejects.toBeInstanceOf(ProbeRateLimited);
    expect(fetchWithSession).toHaveBeenCalledTimes(1);

    // A profile switch restarts the schedule.
    stopBandwidthProbes();
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(600_000);
    await awayFor(60_000);
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(2);
  });

  // WEB-SCHEDULE-3: a browser that cannot report its connection type still
  // says when it is back online, perhaps on another network.
  it("reads again when the browser is back online", async () => {
    server({ reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: DAY } } });
    scheduleBandwidthProbe();
    await readThenTest();
    const reads = vi.mocked(v2).mock.calls.length;

    window.dispatchEvent(new Event("online"));
    // Until the read, the reading may belong to the network the browser left.
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.mocked(v2).mock.calls.length).toBeGreaterThan(reads);
    expect(readMeasuredBandwidthKbps()).toBe(48_000);
  });

  // WEB-SCHEDULE-11: a start after a sign-in reads the server before it sends
  // a reading.
  it("sends no reading after a stop until it has read the server again", async () => {
    server({ reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: DAY } } });
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    expect(readMeasuredBandwidthKbps()).toBe(48_000);

    stopBandwidthProbes();
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
  });

  // H9: every browser on iOS and iPadOS, and Firefox, cannot report its link.
  it("runs no automatic test on a phone that cannot tell its link type", async () => {
    vi.stubGlobal("navigator", { ...navigator, userAgent: "Mozilla/5.0 (iPhone) Mobile" });
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).not.toHaveBeenCalled();

    // Test speed and the reading still count the link as unmetered.
    const result = testConnectionSpeed();
    await vi.waitFor(() => expect(fetchWithSession).toHaveBeenCalledTimes(1));
    cancelBandwidthProbe();
    await expect(result).resolves.toBe("playing");
  });

  it("runs no automatic test on an iPad that asks for desktop sites", async () => {
    vi.stubGlobal("navigator", {
      ...navigator,
      userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Version/17.0 Safari/605.1.15",
      maxTouchPoints: 5,
    });
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).not.toHaveBeenCalled();
  });

  it("runs the automatic test on a desktop that cannot tell its link type", async () => {
    vi.stubGlobal("navigator", { ...navigator, userAgent: "Mozilla/5.0 (Macintosh) Firefox" });
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
  });

  // WEB-SCHEDULE-7: a short return resumes only a test the hide stopped.
  it("does not resume a test that playback refused, after a short tab switch", async () => {
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    const media = pageVideo();
    media.play();
    await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
    expect(fetchWithSession).not.toHaveBeenCalled();
    media.pause();

    await awayFor(5_000);
    await readThenTest();
    expect(fetchWithSession).not.toHaveBeenCalled();
  });

  it("resumes the test a hide stopped without reading again first", async () => {
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    const reads = vi.mocked(v2).mock.calls.length;

    await awayFor(5_000);
    await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
    expect(fetchWithSession).toHaveBeenCalledTimes(2);
    expect(vi.mocked(v2).mock.calls.length).toBe(reads);
  });

  it("does not resume a test that media stopped, after a short tab switch", async () => {
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    const media = pageVideo();
    media.play();
    media.pause();

    await awayFor(5_000);
    await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
  });

  // WEB-SCHEDULE-8: media that starts or resumes takes the link back.
  it("stops a running test when media in the page starts playing", async () => {
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    const signal = (fetchWithSession.mock.calls[0]![1] as RequestInit).signal!;

    pageVideo().play();
    expect(signal.aborted).toBe(true);
  });

  // WEB-MEASURE NEW-1: theme music plays outside the document.
  it("counts theme music as media: it stops a running test and holds new ones off", async () => {
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    const signal = (fetchWithSession.mock.calls[0]![1] as RequestInit).signal!;

    const theme = Object.assign(new EventTarget(), { paused: true, ended: false });
    const untrack = trackDetachedMedia(theme as unknown as HTMLMediaElement);
    theme.paused = false;
    theme.dispatchEvent(new Event("play"));
    expect(signal.aborted).toBe(true);

    await awayFor(60_000);
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
    untrack();
  });

  // WEB-SETTINGS-3: Settings shows what the schedule read, and a new network
  // makes it read again.
  it("shares its connection read with Settings and drops it on a new network", async () => {
    const connection = Object.assign(new EventTarget(), { type: "wifi" });
    vi.stubGlobal("navigator", { ...navigator, connection });
    server({ reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: DAY } } });
    queryClient.setQueryDefaults(["playback", "connection"], { gcTime: Infinity });
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    expect(queryClient.getQueryData(["playback", "connection"])).toMatchObject({
      probe: { kbps: 48_000 },
    });

    connection.type = "ethernet";
    connection.dispatchEvent(new Event("change"));
    expect(queryClient.getQueryState(["playback", "connection"])?.isInvalidated).toBe(true);
  });

  // WEB-WIRE-4: the best reading can expire before another current one.
  it("sends the other current reading once the best one expires", async () => {
    server({
      maxAgeSeconds: 7 * 86400,
      reading: {
        probe: { kbps: 48_000, lower_bound: false, ageMs: DAY },
        sample: { kbps: 140_000, ageMs: 6 * DAY },
      },
    });
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    expect(readMeasuredBandwidthKbps()).toBe(140_000);
    expect(readMeasuredBandwidthKbps(Date.now() + 2 * DAY)).toBe(48_000);
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

  // WEB-SCHEDULE-9: a second tap joins the test in progress.
  it("joins a running Test speed", async () => {
    server({});
    hangingProbe();
    const first = testConnectionSpeed();
    const second = testConnectionSpeed();
    expect(second).toBe(first);
    await vi.waitFor(() => expect(fetchWithSession).toHaveBeenCalledTimes(1));
    cancelBandwidthProbe();
    await expect(first).resolves.toBe("playing");
  });

  // WEB-SCHEDULE-10: only playback is the viewer's to stop.
  it("says why it stopped: playback, a metered link, or nothing measured", async () => {
    server({});
    hangingProbe();
    const connection = Object.assign(new EventTarget(), { type: "wifi", saveData: false });
    vi.stubGlobal("navigator", { ...navigator, connection });
    scheduleBandwidthProbe();

    const hidden = testConnectionSpeed();
    await vi.waitFor(() => expect(fetchWithSession).toHaveBeenCalled());
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
    await expect(hidden).rejects.toThrow("could not be measured");
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });

    const metered = testConnectionSpeed();
    await vi.waitFor(() => expect(fetchWithSession).toHaveBeenCalledTimes(2));
    connection.saveData = true;
    connection.dispatchEvent(new Event("change"));
    await expect(metered).resolves.toBe("metered");
    expect(await testConnectionSpeed()).toBe("metered");
    stopBandwidthProbes();
  });

  // D27: a result the server did not keep is not this device's to keep.
  it("rejects with ProbeNotSaved when the server refuses the result", async () => {
    server({});
    const body = timedBody(1000, 1_000_000, 10);
    fetchWithSession.mockResolvedValue({ res: body.res });
    const clock = vi.spyOn(performance, "now").mockImplementation(body.clock);
    const read = vi.mocked(v2).getMockImplementation()!;
    vi.mocked(v2).mockImplementation((async (operation: string, init?: unknown) => {
      if (operation === "POST /api/v2/playback/bandwidth-probe/results")
        throw new Error("validation failed");
      return read(operation as never, init as never);
    }) as never);
    try {
      await expect(testConnectionSpeed()).rejects.toBeInstanceOf(ProbeNotSaved);
    } finally {
      clock.mockRestore();
    }
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
  });
});

describe("media holds", () => {
  // H7: a removed element's own events never reach the document.
  it("releases the hold of a player removed while it played", async () => {
    server({});
    const locks = fakeLocks();
    scheduleBandwidthProbe();
    const media = pageVideo();
    media.play();
    await settle();
    expect(locks.held.map((lock) => lock.name)).toEqual(["silo-media-active"]);

    media.video.remove();
    mediaElementReleased(media.video);
    await settle();
    expect(locks.held).toEqual([]);
  });

  // WEB-SCHEDULE-6: the start's hold and quiet period end with its player.
  it("lets Test speed run once the player that was starting goes away", async () => {
    server({});
    const locks = fakeLocks();
    hangingProbe();
    cancelBandwidthProbe();
    await settle();
    expect(await testConnectionSpeed()).toBe("playing");

    const video = document.createElement("video");
    mediaElementReleased(video);
    await settle();
    expect(locks.held).toEqual([]);
    const result = testConnectionSpeed();
    await vi.waitFor(() => expect(fetchWithSession).toHaveBeenCalledTimes(1));
    stopBandwidthProbes();
    await expect(result).rejects.toThrow("could not be measured");
  });

  it("holds only automatic tests in the quiet period after a start that played", async () => {
    vi.useFakeTimers();
    try {
      server({});
      fakeLocks();
      hangingProbe();
      scheduleBandwidthProbe();
      await vi.advanceTimersByTimeAsync(0);
      cancelBandwidthProbe();
      const media = pageVideo();
      media.play();
      media.pause();
      await settle();

      // An automatic test still waits out the quiet period.
      await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
      expect(fetchWithSession).not.toHaveBeenCalled();
      // Test speed does not: nothing plays or loads.
      const result = testConnectionSpeed();
      await vi.waitFor(() => expect(fetchWithSession).toHaveBeenCalledTimes(1));
      stopBandwidthProbes();
      await expect(result).rejects.toThrow("could not be measured");
    } finally {
      vi.useRealTimers();
    }
  });
});
