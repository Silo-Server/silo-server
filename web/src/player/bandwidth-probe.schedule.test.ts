import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { QueryObserver } from "@tanstack/react-query";

vi.mock("@/api/v2/request", () => ({
  V2_CLIENT_HEADERS: {},
  v2: vi.fn(async () => ({ features: [] })),
}));
vi.mock("@/api/client", () => ({ fetchWithSession: vi.fn() }));

import {
  testConnectionSpeed,
  cancelBandwidthProbe,
  ProbeRateLimited,
  readMeasuredBandwidthKbps,
  scheduleBandwidthProbe,
  stopBandwidthProbes,
} from "./bandwidth-probe";
import { trackDetachedMedia } from "@/lib/detachedMedia";
import { queryClient } from "@/lib/query-client";
import { v2 } from "@/api/v2/request";
import {
  DAY,
  PROBE_URL,
  timedBody,
  hangingProbe,
  server,
  connectionReads,
  holdNextConnectionRead,
  TEST_DELAY_MS,
  readThenTest,
  setVisibility,
  awayFor,
  fakeLocks,
  pageVideo,
  FakeBroadcastChannel,
  fetchWithSession,
  installProbeTestHooks,
} from "./bandwidth-probe.test-support";

installProbeTestHooks();

describe("scheduleBandwidthProbe", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000_000_000);
  });
  afterEach(() => {
    stopBandwidthProbes();
    vi.useRealTimers();
  });

  // Whether the server's reading proves the link, so the schedule keeps it
  // for starts and runs no test.
  it.each<[string, Parameters<typeof server>[0], number | undefined]>([
    [
      "keeps a settled reading",
      { target: 115_000, reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: 2 * DAY } } },
      48_000,
    ],
    [
      "keeps the best reading while a lower bound reaches the target",
      {
        target: 115_000,
        reading: {
          probe: { kbps: 115_000, lower_bound: true, ageMs: DAY },
          sample: { kbps: 140_000, ageMs: DAY },
        },
      },
      140_000,
    ],
    [
      "keeps any current reading when the server has no target",
      { reading: { probe: { kbps: 48_000, lower_bound: true, ageMs: DAY } } },
      48_000,
    ],
    [
      "tests when the reading has outlived its lifetime",
      {
        maxAgeSeconds: 86400,
        reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: 2 * DAY } },
      },
      undefined,
    ],
  ])("%s", async (_name, options, kept) => {
    server(options);
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();

    expect(fetchWithSession).toHaveBeenCalledTimes(kept === undefined ? 1 : 0);
    expect(readMeasuredBandwidthKbps()).toBe(kept);
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
    const locks = fakeLocks();
    locks.held.push({ name: "silo-bandwidth-probe", mode: "exclusive" });
    scheduleBandwidthProbe();
    await readThenTest();

    expect(locks.request).toHaveBeenCalledWith(
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
    const locks = fakeLocks();
    locks.held.push({ name: "silo-media-active", mode: "shared" });
    scheduleBandwidthProbe();
    await readThenTest();

    expect(locks.query).toHaveBeenCalled();
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

  // What stops a test while Test speed waits for the automatic one to end stops Test speed.
  it("stops Test speed when the tab is hidden while the automatic test it replaced ends", async () => {
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    const release = holdNextConnectionRead();
    await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);

    const result = testConnectionSpeed();
    const outcome = expect(result).rejects.toThrow("The connection speed could not be measured");
    setVisibility("hidden");
    release();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchWithSession).not.toHaveBeenCalled();
    await outcome;
  });

  it("stops Test speed when Data Saver turns on while the automatic test it replaced ends", async () => {
    const connection = Object.assign(new EventTarget(), { type: "wifi", saveData: false });
    vi.stubGlobal("navigator", { ...navigator, connection });
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    const release = holdNextConnectionRead();
    await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);

    const result = testConnectionSpeed();
    connection.saveData = true;
    connection.dispatchEvent(new Event("change"));
    release();
    await expect(result).resolves.toBe("metered");
    expect(fetchWithSession).not.toHaveBeenCalled();
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
  // A handheld on an unknown link may be metered, so the schedule leaves it.
  it.each<[string, Record<string, unknown>, boolean]>([
    ["a phone that cannot tell its link type", { userAgent: "Mozilla/5.0 (iPhone) Mobile" }, false],
    [
      "a phone whose browser reports an unknown link",
      {
        userAgent:
          "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/128.0 Mobile Safari/537.36",
        connection: { type: "unknown", saveData: false },
      },
      false,
    ],
    [
      "an iPad that asks for desktop sites",
      {
        userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Version/17.0 Safari/605.1.15",
        maxTouchPoints: 5,
      },
      false,
    ],
    [
      "an Android tablet that names neither Mobile nor Tablet",
      {
        userAgent:
          "Mozilla/5.0 (Linux; Android 14; SM-X710) AppleWebKit/537.36 Chrome/128.0 Safari/537.36",
      },
      false,
    ],
    [
      "a desktop that cannot tell its link type",
      { userAgent: "Mozilla/5.0 (Macintosh) Firefox" },
      true,
    ],
  ])("on %s, runs the automatic test: %s", async (_name, device, tests) => {
    vi.stubGlobal("navigator", { ...navigator, ...device });
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(tests ? 1 : 0);
  });

  it("lets Test speed run on a handheld that cannot tell its link type", async () => {
    vi.stubGlobal("navigator", { ...navigator, userAgent: "Mozilla/5.0 (iPhone) Mobile" });
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await readThenTest();
    const result = testConnectionSpeed();
    await vi.waitFor(() => expect(fetchWithSession).toHaveBeenCalledTimes(1));
    cancelBandwidthProbe();
    await expect(result).resolves.toBe("playing");
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
    onTestFinished(untrack);
    theme.paused = false;
    theme.dispatchEvent(new Event("play"));
    expect(signal.aborted).toBe(true);

    await awayFor(60_000);
    await readThenTest();
    expect(fetchWithSession).toHaveBeenCalledTimes(1);
  });

  // WEB-SETTINGS-3: Settings shows what the schedule read, and a new network
  // makes it read again.
  it("shares its connection read with Settings and drops it on a new network", async () => {
    const connection = Object.assign(new EventTarget(), { type: "wifi" });
    vi.stubGlobal("navigator", { ...navigator, connection });
    server({ reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: DAY } } });
    const defaults = queryClient.getQueryDefaults(["playback", "connection"]);
    onTestFinished(() => queryClient.setQueryDefaults(["playback", "connection"], defaults));
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

  // A read that answers after a network change describes the network left.
  it("drops a read that answers after the network changed", async () => {
    const connection = Object.assign(new EventTarget(), { type: "wifi" });
    vi.stubGlobal("navigator", { ...navigator, connection });
    server({ reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: DAY } } });
    const release = holdNextConnectionRead();
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);

    server({ reading: { probe: { kbps: 9_000, lower_bound: false, ageMs: DAY } } });
    connection.type = "ethernet";
    connection.dispatchEvent(new Event("change"));
    await vi.advanceTimersByTimeAsync(0);
    expect(readMeasuredBandwidthKbps()).toBe(9_000);

    release();
    await vi.advanceTimersByTimeAsync(0);
    expect(readMeasuredBandwidthKbps()).toBe(9_000);
  });

  // Settings' first read, still out when the network changes, describes the network left.
  it("cancels a Settings read with nothing cached when the network changes", async () => {
    const connection = Object.assign(new EventTarget(), { type: "wifi" });
    vi.stubGlobal("navigator", { ...navigator, connection });
    server({ reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: DAY } } });
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    queryClient.removeQueries({ queryKey: ["playback", "connection"] });
    const release = holdNextConnectionRead();
    const unsubscribe = new QueryObserver(queryClient, {
      queryKey: ["playback", "connection"],
      queryFn: () => v2("GET /api/v2/playback/connection"),
    }).subscribe(() => undefined);
    await vi.advanceTimersByTimeAsync(0);

    server({ reading: { probe: { kbps: 9_000, lower_bound: false, ageMs: DAY } } });
    connection.type = "ethernet";
    connection.dispatchEvent(new Event("change"));
    await vi.advanceTimersByTimeAsync(0);
    release();
    await vi.advanceTimersByTimeAsync(0);
    expect(queryClient.getQueryData(["playback", "connection"])).toMatchObject({
      probe: { kbps: 9_000 },
    });
    unsubscribe();
  });

  it("reads the new network at once when it changes during a test's read", async () => {
    const connection = Object.assign(new EventTarget(), { type: "wifi" });
    vi.stubGlobal("navigator", { ...navigator, connection });
    server({});
    hangingProbe();
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    const release = holdNextConnectionRead();
    await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
    const reads = connectionReads();

    connection.type = "ethernet";
    connection.dispatchEvent(new Event("change"));
    await vi.advanceTimersByTimeAsync(0);
    expect(connectionReads()).toBe(reads + 1);
    release();
  });

  // D23: a change of metering is a network change.
  it("reads again when Data Saver turns off", async () => {
    const connection = Object.assign(new EventTarget(), { type: "wifi", saveData: true });
    vi.stubGlobal("navigator", { ...navigator, connection });
    server({ reading: { probe: { kbps: 48_000, lower_bound: false, ageMs: DAY } } });
    scheduleBandwidthProbe();
    await vi.advanceTimersByTimeAsync(0);
    const reads = connectionReads();

    connection.saveData = false;
    connection.dispatchEvent(new Event("change"));
    await vi.advanceTimersByTimeAsync(0);
    expect(connectionReads()).toBe(reads + 1);
  });

  it("does nothing after it is stopped", async () => {
    server({});
    const stop = scheduleBandwidthProbe(1000);
    stop();
    await vi.advanceTimersByTimeAsync(30 * DAY);
    expect(v2).not.toHaveBeenCalled();
  });
});
