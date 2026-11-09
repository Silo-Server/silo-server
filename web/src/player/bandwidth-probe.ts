import { v2 } from "@/api/v2/request";
import { detachedMediaPlaying, onDetachedMediaChange } from "@/lib/detachedMedia";
import { queryClient } from "@/lib/query-client";
import { DEFAULT_PROBE_URL, measureBandwidth, ProbeRateLimited } from "./bandwidth-measure";
import {
  forgetServerReadings,
  keepOwnResult,
  keepServerReadings,
  keptReadingCurrent,
  resetPageReadings,
  withinProbeBounds,
} from "./bandwidth-readings";
import { detectFormFactor } from "./form-factor";
import { networkInformation, type NetworkInformationLike } from "./network-information";
import type { ProbeMode, ProbeResult } from "./probe-meter";

/**
 * The connection speed test (docs/design/2026-10-17-buffering-quality.md,
 * Connection speed test), for `bandwidth_estimate_kbps`.
 *
 * Browsers will not tell us: Chromium caps `navigator.connection.downlink` at
 * 10 Mbps and Safari and Firefox have no such API, while Auto quality needs to
 * know whether a viewer can sustain a 4K source at 60–100 Mbps. So the app
 * times random bytes from the server, on one connection as playback uses.
 *
 * The server keeps the reading for this device on the connection it is using
 * now. The app reads it at once on sign-in, when a hidden tab is shown again,
 * after a new connection type and when the browser is back online, so an early
 * playback start already sends it, and runs the quick test a few seconds later
 * only when no reading proves the link. There is no timer. The current readings
 * are kept (bandwidth-readings.ts) so playback starts can read them at once.
 */

export { forgetBandwidthEstimate, readMeasuredBandwidthKbps } from "./bandwidth-readings";
export { ProbeRateLimited } from "./bandwidth-measure";
export { networkInformation } from "./network-information";

/** Let the first screen settle before spending bandwidth on a test. */
const TEST_DELAY_MS = 5_000;
/**
 * A tab shown again this soon is still on the network it left, so it does
 * not read again. Matches HIDDEN_EVENTS_SOCKET_GRACE_MS, so a short tab
 * switch sends nothing anywhere in the app.
 */
const SHORT_ABSENCE_MS = 60_000;
/** How long a reading lasts when the server does not say. */
const DEFAULT_READING_MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000;
/** The query Settings reads the connection with; the schedule keeps it current. */
export const CONNECTION_QUERY_KEY = ["playback", "connection"] as const;

/**
 * Bumped on a network change and when the schedule stops: a server read still
 * in flight then describes another network or account, and is dropped.
 */
let readingEpoch = 0;

/** The test ran but the server did not keep its result. */
export class ProbeNotSaved extends Error {
  constructor() {
    super("The speed test result could not be saved");
  }
}

/** The capability fields the test reads. */
interface ProbeSchedule {
  url: string;
  targetKbps?: number;
  automatic: boolean;
  maxAgeMs: number;
  /** connection_speed_v1: results are reported and the server keeps the reading. */
  reporting: boolean;
}

async function probeSchedule(): Promise<ProbeSchedule> {
  const cap = await v2("GET /api/v2/playback/capabilities");
  const reporting = cap.features.includes("connection_speed_v1") && !!cap.bandwidth_probe_url;
  if (!reporting) {
    return {
      url: DEFAULT_PROBE_URL,
      automatic: true,
      maxAgeMs: DEFAULT_READING_MAX_AGE_MS,
      reporting,
    };
  }
  return {
    url: cap.bandwidth_probe_url!,
    targetKbps: cap.bandwidth_probe_target_kbps,
    automatic: cap.bandwidth_probe_automatic !== false,
    maxAgeMs: (cap.bandwidth_probe_max_age_seconds ?? 0) * 1000 || DEFAULT_READING_MAX_AGE_MS,
    reporting,
  };
}

/** A probe or playback sample the server keeps; a sample is always a lower bound. */
interface ServerReading {
  kbps: number;
  lowerBound: boolean;
  measuredAt: number;
}

/**
 * Reads what the server keeps for this connection, keeps every current reading
 * for playback starts, shares the read with Settings, and says whether one
 * proves the link: a settled rate, or a lower bound at least the current
 * target.
 */
async function readServerReading(schedule: ProbeSchedule): Promise<boolean> {
  const epoch = readingEpoch;
  const connection = await v2("GET /api/v2/playback/connection");
  if (epoch !== readingEpoch) throw new Error("The connection changed during the read");
  queryClient.setQueryData(CONNECTION_QUERY_KEY, connection);
  const readings: ServerReading[] = [];
  if (connection.probe)
    readings.push({
      kbps: connection.probe.kbps,
      lowerBound: connection.probe.lower_bound,
      measuredAt: Date.parse(connection.probe.measured_at),
    });
  if (connection.playback_sample)
    readings.push({
      kbps: connection.playback_sample.kbps,
      lowerBound: true,
      measuredAt: Date.parse(connection.playback_sample.measured_at),
    });
  const now = Date.now();
  const current = readings.filter(
    (reading) => withinProbeBounds(reading.kbps) && now - reading.measuredAt <= schedule.maxAgeMs,
  );
  // The server's clock dates the reading; one behind this device's must not
  // read as from the future and be dropped.
  keepServerReadings(
    current.map((reading) => ({
      kbps: reading.kbps,
      measuredAt: Math.min(reading.measuredAt, now),
      maxAgeMs: schedule.maxAgeMs,
    })),
  );
  // Without a target the server has nothing to measure against, and a failed
  // target lookup must not send every client into a test.
  return current.some(
    (reading) =>
      !reading.lowerBound ||
      schedule.targetKbps === undefined ||
      reading.kbps >= schedule.targetKbps,
  );
}

/**
 * Runs one test and keeps what it measured: on the server, and here for starts.
 * Rejects with ProbeNotSaved when the server did not take the result.
 */
async function collectProbe(
  signal: AbortSignal,
  mode: ProbeMode,
  schedule: ProbeSchedule,
): Promise<ProbeResult | undefined> {
  const result = await measureBandwidth(signal, mode, schedule.targetKbps, schedule.url);
  if (!result || signal.aborted) return undefined;
  if (!schedule.reporting) {
    keepOwnResult(result.kbps, schedule.maxAgeMs);
    return result;
  }
  const type = networkInformation()?.type;
  const transport = type === "wifi" || type === "ethernet" || type === "cellular" ? type : "other";
  try {
    // Only Test speed runs the accurate test. The viewer asked for it, so a
    // result above the learned limit lifts the limit, as Reset would.
    await v2("POST /api/v2/playback/bandwidth-probe/results", {
      body: { ...result, transport, ...(mode === "accurate" ? { manual: true } : {}) },
      signal,
    });
  } catch {
    throw new ProbeNotSaved();
  }
  // The server kept the result; if reading it back fails, the next look does.
  // Test speed's result replaced the server's playback sample, so until then
  // starts send nothing rather than an older, higher kept sample; the server
  // plans with its own reading.
  await readServerReading(schedule).catch(() => {
    if (mode === "accurate") forgetServerReadings();
  });
  return result;
}

/**
 * Why a running test was stopped: media began playing or starting, the tab was
 * hidden, the link became metered, the network changed, the schedule stopped,
 * or Test speed replaced an automatic test.
 */
type ProbeStop = "playback" | "hidden" | "metered" | "network" | "stopped" | "replaced";

/** The test measuring now, automatic or Test speed. */
let activeProbe: AbortController | undefined;
/** The automatic test from its planned start until its lock is released. */
let automaticRun: Promise<void> | undefined;
/** The Test speed in progress, which a second tap joins. */
let speedTest: Promise<ProbeResult | SpeedTestSkipped> | undefined;

function stopProbe(reason: ProbeStop): void {
  activeProbe?.abort(reason);
}

/** Whether a test is measuring and has not been stopped. */
function probeRunning(): boolean {
  return activeProbe !== undefined && !activeProbe.signal.aborted;
}

function stopReason(signal: AbortSignal): ProbeStop | undefined {
  return signal.aborted ? (signal.reason as ProbeStop) : undefined;
}

/**
 * Why Test speed did not run or measure: media is playing or starting, the
 * link is metered (Settings shows the metered note instead), or another tab
 * is testing.
 */
export type SpeedTestSkipped = "playing" | "metered" | "busy";

/** What a stopped Test speed tells the viewer; other stops measured nothing. */
function speedTestStopped(signal: AbortSignal): SpeedTestSkipped {
  const reason = stopReason(signal);
  if (reason === "playback") return "playing";
  if (reason === "metered") return "metered";
  throw new Error("The connection speed could not be measured");
}

/**
 * Settings → Test speed: runs the accurate test now, whenever no media plays
 * or loads. It replaces a running automatic test, and a second call joins the
 * one in progress. Resolves to what it measured, or to why it could not run.
 * Rejects when it ran and measured nothing, with ProbeRateLimited for a 429
 * and ProbeNotSaved when the server refused the result.
 */
export function testConnectionSpeed(): Promise<ProbeResult | SpeedTestSkipped> {
  speedTest ??= runSpeedTest().finally(() => {
    speedTest = undefined;
  });
  return speedTest;
}

async function runSpeedTest(): Promise<ProbeResult | SpeedTestSkipped> {
  if (onMeteredConnection()) return "metered";
  if (mediaActiveHere()) return "playing";
  const replaced = automaticRun;
  if (replaced) {
    // The automatic test would only split the link with this one.
    cancelEpoch++;
    stopProbe("replaced");
  }
  // Registered before waiting for the replaced test to end, so what stops a
  // test meanwhile (a hide, metering, a new network, playback) stops this one.
  const probe = new AbortController();
  const { signal } = probe;
  activeProbe = probe;
  const run = async (): Promise<ProbeResult | SpeedTestSkipped> => {
    if (signal.aborted) return speedTestStopped(signal);
    const locks = lockManager();
    if (locks && (await mediaActiveInAnyTab(locks))) return "playing";
    let result: ProbeResult | undefined;
    try {
      result = await collectProbe(signal, "accurate", await probeSchedule());
    } catch (error) {
      // Playback that starts mid-test cancels it; that is not a failed measurement.
      if (signal.aborted) return speedTestStopped(signal);
      if (error instanceof ProbeRateLimited) holdAutomaticTests(error.retryAfter);
      throw error;
    }
    if (result) return result;
    if (signal.aborted) return speedTestStopped(signal);
    throw new Error("The connection speed could not be measured");
  };
  try {
    if (replaced) await replaced.catch(() => undefined);
    const locks = lockManager();
    return locks
      ? await locks.request(PROBE_LOCK_NAME, { ifAvailable: true }, (lock) =>
          lock ? run() : "busy",
        )
      : await run();
  } finally {
    if (activeProbe === probe) activeProbe = undefined;
  }
}

let scheduleCleanup: (() => void) | undefined;
/**
 * Bumped by cancelBandwidthProbe and when Test speed takes over, so a test
 * still waiting for the cross-tab lock does not begin once the lock arrives.
 */
let cancelEpoch = 0;
/**
 * When playback last started in this tab, and when another tab said it did.
 * Automatic tests hold off for a while after a start, since a stream that has
 * just started is still loading; Test speed does not.
 */
let lastPlaybackStartAt: number | undefined;
let otherTabPlaybackStartAt: number | undefined;
/** Counts starts and source loads, so only the latest can end the hold. */
let startGeneration = 0;
/** How long after a playback start an automatic test holds off, at most. */
const PLAYBACK_START_QUIET_MS = 60_000;
/**
 * A 429's Retry-After: no automatic test before then. It holds across profile
 * and account changes for as long as the page runs.
 */
let notBefore = 0;
/** Web Lock that lets one tab at a time run a test. */
const PROBE_LOCK_NAME = "silo-bandwidth-probe";

function holdAutomaticTests(retryAfterSeconds: number): void {
  notBefore = Math.max(notBefore, Date.now() + retryAfterSeconds * 1000);
}

/** Whether media in this page plays, or has been asked to play and is loading. */
function mediaPlaying(): boolean {
  if (detachedMediaPlaying()) return true;
  if (typeof document === "undefined") return false;
  return Array.from(document.querySelectorAll("video, audio")).some(
    (media) => !(media as HTMLMediaElement).paused && !(media as HTMLMediaElement).ended,
  );
}

/** Whether this tab's media plays or a playback start is still loading. */
function mediaActiveHere(): boolean {
  return mediaPlaying() || releaseStartHold !== undefined;
}

function tabHidden(): boolean {
  return typeof document !== "undefined" && document.visibilityState === "hidden";
}

/**
 * Whether the viewer pays for this link by the byte: Data Saver is on or the
 * browser reports cellular. Such a link is not tested, and a reading taken on
 * another link does not describe it. A browser that cannot report its link
 * type counts as unmetered.
 */
export function onMeteredConnection(): boolean {
  const connection = networkInformation();
  return connection?.saveData === true || connection?.type === "cellular";
}

/**
 * Whether a phone or tablet cannot tell what link it is on (every browser on
 * iOS and iPadOS, Firefox, and Chromium when it reports "unknown"). It might
 * be on mobile data, so it runs no automatic test; Test speed stays available.
 */
function handheldOnUnknownLink(): boolean {
  const type = networkInformation()?.type;
  if (type !== undefined && type !== "unknown") return false;
  const formFactor = detectFormFactor();
  if (formFactor === "phone" || formFactor === "tablet") return true;
  if (formFactor === "tv" || typeof navigator === "undefined") return false;
  const ua = navigator.userAgent ?? "";
  // Two handhelds pass for desktops: iPadOS Safari asks for desktop sites and
  // so sends a Mac user agent, which its touch screen gives away, and an
  // Android tablet's browser may name neither "Mobile" nor "Tablet".
  return (
    (/\bmacintosh\b/i.test(ua) && (navigator.maxTouchPoints ?? 0) > 1) || /\bandroid\b/i.test(ua)
  );
}

/**
 * Shared Web Lock a tab holds while its media plays or a playback start is
 * loading, so a test in another tab can tell the link is busy. Shared mode
 * lets any number of tabs hold it at once.
 */
const MEDIA_LOCK_NAME = "silo-media-active";

function lockManager(): LockManager | undefined {
  return typeof navigator === "undefined" ? undefined : navigator.locks;
}

/** Holds the media lock until `until` settles. */
function holdMediaLock(until: Promise<void>): void {
  void lockManager()
    ?.request(MEDIA_LOCK_NAME, { mode: "shared" }, () => until)
    .catch(() => undefined);
}

let releasePlayingHold: (() => void) | undefined;
/** Releases the hold a playback start takes while its media loads. */
let releaseStartHold: (() => void) | undefined;
let mediaTrackingInstalled = false;

/**
 * Holds the media lock while any media in this tab is playing. Media that
 * starts playing also stops a running test, and takes over from the hold of
 * the playback start that loaded it.
 */
function syncPlayingHold(): void {
  if (mediaPlaying()) {
    stopProbe("playback");
    if (!releasePlayingHold) {
      holdMediaLock(
        new Promise<void>((resolve) => {
          releasePlayingHold = resolve;
        }),
      );
    }
    releaseStartHold?.();
    return;
  }
  releasePlayingHold?.();
  releasePlayingHold = undefined;
}

/** Holds the media lock while a playback start loads, for at most the quiet period. */
function holdPlaybackStart(): void {
  releaseStartHold?.();
  let resolveHold: () => void = () => undefined;
  holdMediaLock(
    new Promise<void>((resolve) => {
      resolveHold = resolve;
    }),
  );
  const timer = setTimeout(() => release(), PLAYBACK_START_QUIET_MS);
  const release = () => {
    clearTimeout(timer);
    if (releaseStartHold === release) releaseStartHold = undefined;
    resolveHold();
  };
  releaseStartHold = release;
}

/** Tells other tabs that playback started or ended here, so their tests yield or resume. */
const PLAYBACK_CHANNEL_NAME = "silo-playback-start";
let playbackChannel: BroadcastChannel | undefined;

function onOtherTabPlayback(event: MessageEvent): void {
  if (event.data === "end") {
    otherTabPlaybackStartAt = undefined;
    return;
  }
  otherTabPlaybackStartAt = Date.now();
  stopProbe("playback");
}

function installMediaTracking(): void {
  if (!playbackChannel && typeof BroadcastChannel !== "undefined") {
    try {
      playbackChannel = new BroadcastChannel(PLAYBACK_CHANNEL_NAME);
      playbackChannel.onmessage = onOtherTabPlayback;
    } catch {
      // Cross-tab notice is optional; without it each tab guards itself.
    }
  }
  if (mediaTrackingInstalled || typeof document === "undefined") return;
  mediaTrackingInstalled = true;
  // Media events do not bubble; capturing on the document sees them all.
  for (const type of ["play", "playing", "pause", "ended", "emptied"]) {
    document.addEventListener(type, syncPlayingHold, true);
  }
  onDetachedMediaChange(syncPlayingHold);
}

/**
 * A playback start ended without media to load: it failed, the viewer left
 * before it loaded, or its player went away. The quiet period and the
 * start's hold end with it.
 */
export function playbackStartEnded(): void {
  if (lastPlaybackStartAt === undefined && !releaseStartHold) return;
  lastPlaybackStartAt = undefined;
  releaseStartHold?.();
  try {
    playbackChannel?.postMessage("end");
  } catch {
    // Cross-tab notice is optional.
  }
}

/**
 * A player reset or removed its media element. A removed element's media
 * events no longer reach the document, so the hold is synced here, and a
 * removed one also ends the playback start it was loading.
 */
export function mediaElementReleased(element: HTMLMediaElement): void {
  if (!element.isConnected) playbackStartEnded();
  syncPlayingHold();
}

/** Whether some tab, this one included, is playing or starting media. */
async function mediaActiveInAnyTab(locks: LockManager): Promise<boolean> {
  // Media removed without a release may have left this tab's own hold behind.
  syncPlayingHold();
  if (typeof locks.query !== "function") return false;
  const state = await locks.query();
  return state.held?.some((lock) => lock.name === MEDIA_LOCK_NAME) ?? false;
}

/** Whether nothing about the link or the media stops an automatic test now. */
function automaticTestAllowed(): boolean {
  const now = Date.now();
  const quiet = (at: number | undefined) => at !== undefined && now - at < PLAYBACK_START_QUIET_MS;
  return (
    !onMeteredConnection() &&
    !handheldOnUnknownLink() &&
    !mediaActiveHere() &&
    !quiet(lastPlaybackStartAt) &&
    !quiet(otherTabPlaybackStartAt)
  );
}

/**
 * Keeps a current reading while the app is signed in: reads the server's at
 * once on sign-in, when a tab hidden for a minute or more is shown again,
 * after the browser reports a new connection type and when it is back online,
 * and runs the quick test `testDelayMs` later only when none proves the link.
 * A tab back from a shorter switch sends nothing unless the hide stopped a
 * test it owed. A hidden tab neither reads nor tests. Returns a function that
 * stops the schedule.
 */
export function scheduleBandwidthProbe(testDelayMs = TEST_DELAY_MS): () => void {
  stopBandwidthProbes();
  try {
    installMediaTracking();
  } catch {
    // Cross-tab coordination is optional.
  }
  // Set when the schedule stops, so it never looks again.
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const later = (ms: number, step: () => Promise<void>) => {
    if (stopped) return;
    clearTimeout(timer);
    timer = setTimeout(
      () => {
        timer = undefined;
        void step();
      },
      Math.max(0, ms),
    );
  };
  /** Whether nothing stops an automatic test from running now. */
  const testMayRun = () =>
    !probeRunning() &&
    !speedTest &&
    !tabHidden() &&
    !stopped &&
    automaticTestAllowed() &&
    Date.now() >= notBefore;
  const readingProvesLink = (schedule: ProbeSchedule) =>
    schedule.reporting ? readServerReading(schedule) : Promise.resolve(keptReadingCurrent());
  // Until a look starts on this network, even a quick tab switch reads.
  let lookDue = true;
  // A look found nothing proving the link, and no test has reached an answer
  // since: the hide stopped it, or the tab was hidden before it ran.
  let owedTest: ProbeSchedule | undefined;
  let hiddenAt: number | undefined;
  /** A test skipped or refused for anything but a hide is not owed. */
  const skipTest = () => {
    if (!tabHidden()) owedTest = undefined;
  };
  // The test is best effort too: one that fails waits for the next look. A
  // test resumed after a short absence does not read again first.
  const planTest = (schedule: ProbeSchedule, readFirst = true) =>
    later(testDelayMs, () => testUnderLock(schedule, readFirst).catch(() => undefined));
  const look = async () => {
    // A test already running keeps what it measures when it settles; one that
    // was stopped, as by a network change, measures nothing.
    if (probeRunning() || tabHidden() || stopped) return;
    lookDue = false;
    owedTest = undefined;
    try {
      const schedule = await probeSchedule();
      if (await readingProvesLink(schedule)) return;
      if (!schedule.automatic) return;
      owedTest = schedule;
      if (testMayRun()) planTest(schedule);
      else skipTest();
    } catch {
      // Best effort: a failed read changes nothing kept here, and the next
      // look tries again.
    }
  };
  const testUnderLock = (schedule: ProbeSchedule, readFirst: boolean) => {
    if (!testMayRun()) {
      skipTest();
      return Promise.resolve();
    }
    // Tabs share the reading, so only one of them tests at a time: two tests
    // at once would split the link and both read low.
    const epoch = cancelEpoch;
    const test = async () => {
      if (epoch !== cancelEpoch || !testMayRun()) return skipTest();
      const locks = lockManager();
      // Another tab may be playing; this tab's own check cannot see it.
      if (locks && (await mediaActiveInAnyTab(locks))) return skipTest();
      if (epoch !== cancelEpoch || !testMayRun()) return skipTest();
      const probe = new AbortController();
      activeProbe = probe;
      try {
        // Another tab, or another device, may have tested meanwhile.
        if (readFirst && (await readingProvesLink(schedule))) return;
        if (probe.signal.aborted) return;
        await collectProbe(probe.signal, "quick", schedule);
      } catch (error) {
        if (error instanceof ProbeRateLimited) holdAutomaticTests(error.retryAfter);
      } finally {
        if (activeProbe === probe) activeProbe = undefined;
        // Only a hide leaves the test owed for the tab's return.
        if (stopReason(probe.signal) !== "hidden") owedTest = undefined;
      }
    };
    const locks = lockManager();
    const run = locks
      ? // Another tab holding the lock is testing and will share its reading.
        locks
          .request(PROBE_LOCK_NAME, { ifAvailable: true }, (lock) => (lock ? test() : skipTest()))
          .then(() => undefined)
      : test();
    automaticRun = run;
    return run.finally(() => {
      if (automaticRun === run) automaticRun = undefined;
    });
  };
  // A new network means a different reading on the server; the browser also
  // fires `change` when only its own downlink or RTT estimate moves, which is
  // not a new network.
  const network = networkInformation() as (NetworkInformationLike & EventTarget) | undefined;
  let networkType = network?.type;
  let metered = onMeteredConnection();
  const networkChanged = () => {
    cancelEpoch++;
    readingEpoch++;
    stopProbe("network");
    forgetServerReadings();
    // A Settings read still out describes the network left. Invalidating
    // alone would join a first read rather than replace it.
    void queryClient.cancelQueries({ queryKey: CONNECTION_QUERY_KEY });
    void queryClient.invalidateQueries({ queryKey: CONNECTION_QUERY_KEY });
    lookDue = true;
    later(0, look);
  };
  const onNetworkChange = () => {
    // Data Saver can turn on or off without a new link type; that changes the
    // metering, which counts as a new network too.
    const nowMetered = onMeteredConnection();
    if (nowMetered) stopProbe("metered");
    if (network?.type === networkType && nowMetered === metered) return;
    networkType = network?.type;
    metered = nowMetered;
    networkChanged();
  };
  const onVisibility = () => {
    if (tabHidden()) {
      hiddenAt = Date.now();
      // A hidden tab does not test; one that is running stops and measured nothing.
      clearTimeout(timer);
      timer = undefined;
      cancelEpoch++;
      stopProbe("hidden");
      return;
    }
    const shortAbsence = hiddenAt !== undefined && Date.now() - hiddenAt < SHORT_ABSENCE_MS;
    hiddenAt = undefined;
    if (lookDue || !shortAbsence) later(0, look);
    else if (owedTest) planTest(owedTest, false);
  };
  network?.addEventListener?.("change", onNetworkChange);
  document.addEventListener("visibilitychange", onVisibility);
  // A browser that cannot report network changes still says when it is back
  // online, which may be on another network.
  window.addEventListener("online", networkChanged);
  scheduleCleanup = () => {
    stopped = true;
    clearTimeout(timer);
    network?.removeEventListener?.("change", onNetworkChange);
    document.removeEventListener("visibilitychange", onVisibility);
    window.removeEventListener("online", networkChanged);
  };
  if (!tabHidden()) later(0, look);
  return stopBandwidthProbes;
}

/**
 * Called when playback starts and when a player loads a new source: stops a
 * running test so it does not compete with playback, and holds tests off
 * while the media loads. The next look tries again. The returned function
 * ends the hold for a start that loads nothing, unless a later start or
 * source load has taken it over.
 */
export function cancelBandwidthProbe(): () => void {
  cancelEpoch++;
  const generation = ++startGeneration;
  lastPlaybackStartAt = Date.now();
  stopProbe("playback");
  // Tell other tabs: a test running there stops now, and the lock keeps new
  // ones off until the playing hold takes over, the player goes away or the
  // start settles. This is best effort: a browser that refuses cross-tab APIs
  // (some privacy modes throw SecurityError) must never keep playback from
  // starting.
  try {
    installMediaTracking();
    playbackChannel?.postMessage("start");
    holdPlaybackStart();
  } catch {
    // Local cancellation above already happened.
  }
  return () => {
    if (generation === startGeneration) playbackStartEnded();
  };
}

/**
 * Called when a player loads a new source: holds tests off as
 * cancelBandwidthProbe does, and returns the function to call once the
 * source has loaded and stays paused. That ends the hold, so Test speed may
 * run here and in other tabs, but neither the quiet period that holds
 * automatic tests nor the hold of a later start or source load.
 */
export function sourceLoadStarted(): () => void {
  cancelBandwidthProbe();
  const generation = startGeneration;
  return () => {
    if (generation === startGeneration) releaseStartHold?.();
  };
}

/**
 * Stops the schedule and any running test, e.g. on sign-out or a profile
 * switch. The next schedule reads the server before starts send its reading
 * again. A 429's hold stays, since it is the server's.
 */
export function stopBandwidthProbes(): void {
  scheduleCleanup?.();
  scheduleCleanup = undefined;
  readingEpoch++;
  lastPlaybackStartAt = undefined;
  otherTabPlaybackStartAt = undefined;
  resetPageReadings();
  releaseStartHold?.();
  playbackChannel?.close();
  playbackChannel = undefined;
  stopProbe("stopped");
}

/** Clears the schedule, media hold and 429 hold so each test starts clean. */
export function resetBandwidthProbesForTests(): void {
  stopBandwidthProbes();
  releasePlayingHold?.();
  releasePlayingHold = undefined;
  notBefore = 0;
}
