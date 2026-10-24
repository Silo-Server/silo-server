import { describe, expect, it, vi } from "vitest";

vi.mock("@/api/v2/request", () => ({
  V2_CLIENT_HEADERS: {},
  v2: vi.fn(async () => ({ features: [] })),
}));
vi.mock("@/api/client", () => ({ fetchWithSession: vi.fn() }));

import {
  testConnectionSpeed,
  cancelBandwidthProbe,
  mediaElementReleased,
  playbackStartEnded,
  ProbeNotSaved,
  readMeasuredBandwidthKbps,
  scheduleBandwidthProbe,
  sourceLoadStarted,
  stopBandwidthProbes,
} from "./bandwidth-probe";
import { v2 } from "@/api/v2/request";
import { storage } from "@/utils/storage";
import {
  DAY,
  PROBE_URL,
  timedBody,
  hangingProbe,
  server,
  serverAnswer,
  setVisibility,
  TEST_DELAY_MS,
  fakeLocks,
  pageVideo,
  keptReading,
  fetchWithSession,
  installProbeTestHooks,
} from "./bandwidth-probe.test-support";

installProbeTestHooks();

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
    // A single reading from before the reading list is replaced by the next look.
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

describe("Test speed", () => {
  it("runs the accurate test, even with a current reading, and reports it", async () => {
    const { posted } = server({
      target: 115_000,
      reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: 0 } },
    });
    // The schedule's first look keeps the server's reading for starts.
    scheduleBandwidthProbe();
    await vi.waitFor(() => expect(readMeasuredBandwidthKbps()).toBe(48_000));
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
      manual: true,
    });
    expect(readMeasuredBandwidthKbps()).toBe(800_000);
    stopBandwidthProbes();
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

  // The server kept the result, so it is the viewer's even if reading it back
  // fails. The result replaced the server's playback sample, so the next start
  // must not send the higher sample kept before the test.
  it("reports a saved result whose read-back fails, and drops the kept sample", async () => {
    server({ reading: { sample: { kbps: 900_000, ageMs: 0 } } });
    scheduleBandwidthProbe();
    await vi.waitFor(() => expect(readMeasuredBandwidthKbps()).toBe(900_000));
    const answer = serverAnswer();
    vi.mocked(v2).mockImplementation((async (operation: string, init?: unknown) => {
      if (operation === "GET /api/v2/playback/connection") throw new Error("offline");
      return answer(operation, init);
    }) as never);
    const body = timedBody(1000, 1_000_000, 10);
    fetchWithSession.mockResolvedValue({ res: body.res });
    const clock = vi.spyOn(performance, "now").mockImplementation(body.clock);
    try {
      expect(await testConnectionSpeed()).toMatchObject({ kbps: 800_000 });
    } finally {
      clock.mockRestore();
    }
    expect(readMeasuredBandwidthKbps()).toBeUndefined();
    stopBandwidthProbes();
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
    setVisibility("hidden");
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
    const read = serverAnswer();
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
    expect(locks.held.map((lock) => lock.name)).toEqual(["silo-media-active"]);

    media.video.remove();
    mediaElementReleased(media.video);
    await vi.waitFor(() => expect(locks.held).toEqual([]));
  });

  // Both exit paths release a start that never loaded media: its player going
  // away, and a refused start or one the viewer left before it loaded.
  it.each([
    [
      "its player goes away",
      (_endStart: () => void) => mediaElementReleased(document.createElement("video")),
    ],
    ["it ends without loading media", (endStart: () => void) => endStart()],
  ])("lets Test speed run once %s", async (_reason, releaseStart) => {
    server({});
    const locks = fakeLocks();
    hangingProbe();
    const endStart = cancelBandwidthProbe();
    expect(locks.held.map((lock) => lock.name)).toEqual(["silo-media-active"]);
    expect(await testConnectionSpeed()).toBe("playing");

    releaseStart(endStart);
    await vi.waitFor(() => expect(locks.held).toEqual([]));
    const result = testConnectionSpeed();
    await vi.waitFor(() => expect(fetchWithSession).toHaveBeenCalledTimes(1));
    stopBandwidthProbes();
    await expect(result).rejects.toThrow("could not be measured");
  });

  // A start the viewer replaced may fail after its replacement began.
  it("keeps the hold of the start that replaced one that failed", async () => {
    server({});
    fakeLocks();
    hangingProbe();
    const endFirst = cancelBandwidthProbe();
    cancelBandwidthProbe();
    endFirst();
    expect(await testConnectionSpeed()).toBe("playing");
  });

  it("ends every start's hold when the player goes away", async () => {
    server({});
    const locks = fakeLocks();
    hangingProbe();
    cancelBandwidthProbe();
    expect(locks.held.map((lock) => lock.name)).toEqual(["silo-media-active"]);
    playbackStartEnded();
    await vi.waitFor(() => expect(locks.held).toEqual([]));
  });

  // A source that loaded and stays paused no longer loads anything.
  it("lets Test speed run here and in other tabs once a paused source has loaded", async () => {
    vi.useFakeTimers();
    try {
      server({});
      const locks = fakeLocks();
      hangingProbe();
      scheduleBandwidthProbe();
      await vi.advanceTimersByTimeAsync(0);
      pageVideo();
      const sourceLoaded = sourceLoadStarted();
      expect(await testConnectionSpeed()).toBe("playing");

      sourceLoaded();
      await vi.waitFor(() => expect(locks.held).toEqual([]));
      // An automatic test still waits out the quiet period.
      await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
      expect(fetchWithSession).not.toHaveBeenCalled();
      const result = testConnectionSpeed();
      await vi.waitFor(() => expect(fetchWithSession).toHaveBeenCalledTimes(1));
      stopBandwidthProbes();
      await expect(result).rejects.toThrow("could not be measured");
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the hold of a start that followed a source that loaded paused", async () => {
    server({});
    const locks = fakeLocks();
    hangingProbe();
    const sourceLoaded = sourceLoadStarted();
    cancelBandwidthProbe();
    sourceLoaded();
    // A released lock is gone by the next task.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(locks.held.map((lock) => lock.name)).toEqual(["silo-media-active"]);
    expect(await testConnectionSpeed()).toBe("playing");
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
