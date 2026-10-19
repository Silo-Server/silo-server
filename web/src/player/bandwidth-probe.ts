import { fetchWithSession } from "@/api/client";
import { v2, V2_CLIENT_HEADERS } from "@/api/v2/request";
import { storage } from "@/utils/storage";
import {
  PROBE_FIRST_BYTE_DEADLINE_MS,
  ProbeMeter,
  type ProbeMode,
  type ProbeResult,
} from "./probe-meter";

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
 * now. The app looks for it a few seconds after sign-in, when a hidden tab is
 * shown again and after a new connection type, and runs the quick test only
 * when no reading proves the link. There is no timer. The best reading is
 * kept here so playback starts can read it at once.
 */

/** Let the first screen settle before reading or spending bandwidth. */
const LOOK_DELAY_MS = 5_000;
/** How long a reading lasts when the server does not say. */
const DEFAULT_READING_MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000;
/** The probe without connection_speed_v1, which keeps nothing for us. */
const DEFAULT_PROBE_URL = "/api/v2/playback/bandwidth-probe";

/**
 * The slice of the Network Information API we use. It is not in the DOM lib
 * and is absent in Safari and Firefox, so everything here is best effort and
 * every reader tolerates `undefined`.
 */
export interface NetworkInformationLike {
  saveData?: boolean;
  type?: string;
}

export function networkInformation(): NetworkInformationLike | undefined {
  if (typeof navigator === "undefined") return undefined;
  return (navigator as Navigator & { connection?: NetworkInformationLike }).connection;
}

/** The best reading for this connection, for playback starts to send. */
interface StoredReading {
  kbps: number;
  measuredAt: number;
  maxAgeMs: number;
}

/**
 * The entry this page last wrote. localStorage can be full or unavailable,
 * and its writes then fail silently; this copy keeps the reading in effect
 * for this page anyway.
 */
let pageEntry: string | undefined;

function parseStored(raw: string | null | undefined, now: number): StoredReading | undefined {
  if (!raw) return undefined;
  try {
    const parsed = JSON.parse(raw) as Partial<StoredReading>;
    const { kbps, measuredAt, maxAgeMs } = parsed;
    // An entry an older build wrote has no lifetime; the next look replaces it.
    if (typeof kbps !== "number" || typeof measuredAt !== "number" || typeof maxAgeMs !== "number")
      return undefined;
    if (measuredAt > now || now - measuredAt > maxAgeMs || kbps < 1) return undefined;
    return { kbps, measuredAt, maxAgeMs };
  } catch {
    return undefined;
  }
}

/** The newer of this page's entry and the shared one another tab may have written. */
function readStored(now: number): StoredReading | undefined {
  const page = parseStored(pageEntry, now);
  const shared = parseStored(storage.get(storage.KEYS.BANDWIDTH_ESTIMATE), now);
  if (!page) return shared;
  if (!shared) return page;
  return shared.measuredAt > page.measuredAt ? shared : page;
}

function writeStored(reading: StoredReading): void {
  pageEntry = JSON.stringify(reading);
  storage.set(storage.KEYS.BANDWIDTH_ESTIMATE, pageEntry);
}

/** Drops the kept reading, in this page and in localStorage. */
export function forgetBandwidthEstimate(): void {
  pageEntry = undefined;
  storage.remove(storage.KEYS.BANDWIDTH_ESTIMATE);
}

/** The kept reading in kbps, or undefined when none is within its lifetime. */
export function readMeasuredBandwidthKbps(now = Date.now()): number | undefined {
  return readStored(now)?.kbps;
}

export class ProbeRateLimited extends Error {
  constructor(readonly retryAfter: number) {
    super(`Try again in ${retryAfter} seconds`);
  }
}

/**
 * Runs one test against `url`: `size` from the mode, the clock from the first
 * byte, and the meter deciding when to stop. Returns undefined when the probe
 * failed, was aborted, or measured nothing; rejects with ProbeRateLimited for
 * a 429.
 */
export async function measureBandwidth(
  signal: AbortSignal,
  mode: ProbeMode,
  targetKbps?: number,
  url = DEFAULT_PROBE_URL,
  clock: () => number = () => performance.now(),
): Promise<ProbeResult | undefined> {
  const meter = new ProbeMeter(mode, targetKbps);
  // The caller's signal cancels the probe; the first-byte deadline abandons a
  // request or body that never starts flowing.
  const controller = new AbortController();
  const forwardAbort = () => controller.abort(signal.reason);
  if (signal.aborted) forwardAbort();
  else signal.addEventListener("abort", forwardAbort, { once: true });
  let firstByteTimer: ReturnType<typeof setTimeout> | undefined = setTimeout(
    () => controller.abort(),
    PROBE_FIRST_BYTE_DEADLINE_MS,
  );
  let ticker: ReturnType<typeof setInterval> | undefined;
  let start: number | undefined;
  try {
    const { res } = await fetchWithSession(
      `${url}${url.includes("?") ? "&" : "?"}size=${meter.limits.bytes}`,
      {
        method: "GET",
        headers: {
          ...V2_CLIENT_HEADERS,
          Accept: "application/octet-stream",
          "X-Profile-Token": "",
        },
        cache: "no-store",
        signal: controller.signal,
      },
    );
    if (res.status === 429) {
      await res.body?.cancel().catch(() => undefined);
      throw new ProbeRateLimited(Number(res.headers.get("Retry-After")) || 30);
    }
    if (!res.ok || !res.body) {
      await res.body?.cancel().catch(() => undefined);
      return undefined;
    }
    const reader = res.body.getReader();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        const now = clock();
        if (start === undefined) {
          start = now;
          clearTimeout(firstByteTimer);
          firstByteTimer = undefined;
          // Windows close on the clock, not only when bytes arrive: a read
          // that stops returning still ends the test at its limit, and the
          // silence counts against the rate.
          const begun = start;
          ticker = setInterval(() => {
            if (meter.tick(clock() - begun)) void reader.cancel().catch(() => undefined);
          }, 100);
        }
        if (meter.add(now - start, value.byteLength)) break;
      }
    } finally {
      // Closing the body ends the transfer; the server stops at its next write.
      await reader.cancel().catch(() => undefined);
    }
  } finally {
    clearTimeout(firstByteTimer);
    clearInterval(ticker);
    signal.removeEventListener("abort", forwardAbort);
  }
  if (start === undefined || signal.aborted) return undefined;
  return (meter.outcome ?? meter.finish(clock() - start)).result ?? undefined;
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
 * Reads what the server keeps for this connection, keeps the best reading
 * within its lifetime for playback starts, and says whether one proves the
 * link: a settled rate, or a lower bound at least the current target.
 */
async function readServerReading(schedule: ProbeSchedule): Promise<boolean> {
  const connection = await v2("GET /api/v2/playback/connection");
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
  const current = readings.filter((reading) => now - reading.measuredAt <= schedule.maxAgeMs);
  const best = current.reduce<ServerReading | undefined>(
    (top, reading) => (!top || reading.kbps > top.kbps ? reading : top),
    undefined,
  );
  if (best)
    writeStored({ kbps: best.kbps, measuredAt: best.measuredAt, maxAgeMs: schedule.maxAgeMs });
  else forgetBandwidthEstimate();
  return current.some(
    (reading) =>
      !reading.lowerBound ||
      (schedule.targetKbps !== undefined && reading.kbps >= schedule.targetKbps),
  );
}

/** Runs one test and keeps what it measured: on the server, and here for starts. */
async function collectProbe(
  signal: AbortSignal,
  mode: ProbeMode,
  schedule: ProbeSchedule,
): Promise<ProbeResult | undefined> {
  const result = await measureBandwidth(signal, mode, schedule.targetKbps, schedule.url);
  if (!result || signal.aborted) return undefined;
  if (!schedule.reporting) {
    writeStored({ kbps: result.kbps, measuredAt: Date.now(), maxAgeMs: schedule.maxAgeMs });
    return result;
  }
  const type = networkInformation()?.type;
  const transport = type === "wifi" || type === "ethernet" || type === "cellular" ? type : "other";
  await v2("POST /api/v2/playback/bandwidth-probe/results", { body: { ...result, transport } });
  await readServerReading(schedule);
  return result;
}

/** Why Test speed did not run: media is playing or starting, or another probe is measuring. */
export type SpeedTestSkipped = "playing" | "busy";

/**
 * Settings → Test speed: runs the accurate test now, under the same playback
 * and cross-tab guards as the automatic one. Resolves to what it measured, or
 * to why it could not run. Rejects when it ran and measured nothing, with
 * ProbeRateLimited for a 429.
 */
export async function testConnectionSpeed(): Promise<ProbeResult | SpeedTestSkipped> {
  if (!probeAllowed()) return "playing";
  if (activeProbe) return "busy";
  const controller = new AbortController();
  activeProbe = controller;
  const run = async (): Promise<ProbeResult | SpeedTestSkipped> => {
    if (controller.signal.aborted || !probeAllowed()) return "playing";
    const locks = lockManager();
    if (locks && (await mediaActiveInAnyTab(locks))) return "playing";
    const result = await collectProbe(controller.signal, "accurate", await probeSchedule());
    if (result) return result;
    // Playback that starts mid-test cancels it; that is not a failed measurement.
    if (controller.signal.aborted || !probeAllowed()) return "playing";
    throw new Error("The connection speed could not be measured");
  };
  try {
    const locks = lockManager();
    return locks
      ? await locks.request(PROBE_LOCK_NAME, { ifAvailable: true }, (lock) =>
          lock ? run() : "busy",
        )
      : await run();
  } finally {
    if (activeProbe === controller) activeProbe = undefined;
  }
}

let lookTimer: ReturnType<typeof setTimeout> | undefined;
/** Bumped by stopBandwidthProbes so a stopped schedule never looks again. */
let scheduleGeneration = 0;
let scheduleCleanup: (() => void) | undefined;
/**
 * Bumped by cancelBandwidthProbe, so a test still waiting for the cross-tab
 * lock when playback starts does not begin once the lock arrives.
 */
let cancelEpoch = 0;
/**
 * When playback last started. Media is still loading, and its element still
 * paused, for a while after a start, so the playing-media check alone would
 * let a look that comes then test during startup.
 */
let lastPlaybackStartAt: number | undefined;
/** How long after a playback start a test holds off. */
const PLAYBACK_START_QUIET_MS = 60_000;
/** A 429's Retry-After: no automatic test before then. */
let notBefore = 0;
/** Web Lock that lets one tab at a time run a test. */
const PROBE_LOCK_NAME = "silo-bandwidth-probe";
let activeProbe: AbortController | undefined;

function mediaPlaying(): boolean {
  if (typeof document === "undefined") return false;
  return Array.from(document.querySelectorAll("video, audio")).some(
    (media) => !(media as HTMLMediaElement).paused && !(media as HTMLMediaElement).ended,
  );
}

function tabHidden(): boolean {
  return typeof document !== "undefined" && document.visibilityState === "hidden";
}

/**
 * Whether the viewer pays for this link by the byte: Data Saver is on or the
 * browser reports cellular. Such a link is not tested, and a reading taken on
 * another link does not describe it.
 */
export function onMeteredConnection(): boolean {
  const connection = networkInformation();
  return connection?.saveData === true || connection?.type === "cellular";
}

/**
 * Shared Web Lock a tab holds while its media plays or playback is starting,
 * so a test in another tab can tell the link is busy. Shared mode lets any
 * number of tabs hold it at once.
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
let mediaTrackingInstalled = false;

/** Holds the media lock while any media element in this tab is playing. */
function syncPlayingHold(): void {
  if (mediaPlaying()) {
    if (!releasePlayingHold) {
      holdMediaLock(
        new Promise<void>((resolve) => {
          releasePlayingHold = resolve;
        }),
      );
    }
    return;
  }
  releasePlayingHold?.();
  releasePlayingHold = undefined;
}

/** Tells other tabs that playback started here, so a running test yields. */
const PLAYBACK_CHANNEL_NAME = "silo-playback-start";
let playbackChannel: BroadcastChannel | undefined;

function onOtherTabPlaybackStart(): void {
  lastPlaybackStartAt = Date.now();
  activeProbe?.abort();
}

function installMediaTracking(): void {
  if (!playbackChannel && typeof BroadcastChannel !== "undefined") {
    try {
      playbackChannel = new BroadcastChannel(PLAYBACK_CHANNEL_NAME);
      playbackChannel.onmessage = onOtherTabPlaybackStart;
    } catch {
      // Cross-tab notice is optional; without it each tab guards itself.
    }
  }
  if (mediaTrackingInstalled || typeof document === "undefined" || !lockManager()) return;
  mediaTrackingInstalled = true;
  // Media events do not bubble; capturing on the document sees them all.
  for (const type of ["playing", "pause", "ended", "emptied"]) {
    document.addEventListener(type, syncPlayingHold, true);
  }
}

/** Whether some tab, this one included, is playing or starting media. */
async function mediaActiveInAnyTab(locks: LockManager): Promise<boolean> {
  if (typeof locks.query !== "function") return false;
  const state = await locks.query();
  return state.held?.some((lock) => lock.name === MEDIA_LOCK_NAME) ?? false;
}

function probeAllowed(): boolean {
  // A test beside a playing or starting stream would both slow it and
  // under-measure.
  const startingPlayback =
    lastPlaybackStartAt !== undefined && Date.now() - lastPlaybackStartAt < PLAYBACK_START_QUIET_MS;
  return !onMeteredConnection() && !mediaPlaying() && !startingPlayback;
}

/**
 * Looks for a current reading while the app is signed in: a few seconds
 * after it starts, when a hidden tab is shown again, and after the browser
 * reports a new connection type. Each look reads the server's reading first
 * and runs the quick test only when none proves the link. A hidden tab
 * neither looks nor tests. Returns a function that stops the schedule.
 */
export function scheduleBandwidthProbe(delayMs = LOOK_DELAY_MS): () => void {
  stopBandwidthProbes();
  try {
    installMediaTracking();
  } catch {
    // Cross-tab coordination is optional.
  }
  const generation = scheduleGeneration;
  const lookIn = (ms: number) => {
    if (generation !== scheduleGeneration) return;
    clearTimeout(lookTimer);
    lookTimer = setTimeout(() => void look(), Math.max(0, ms));
  };
  const look = async () => {
    lookTimer = undefined;
    // A test already running keeps what it measures when it settles.
    if (activeProbe || tabHidden() || generation !== scheduleGeneration) return;
    try {
      const schedule = await probeSchedule();
      if (schedule.reporting) {
        if (await readServerReading(schedule)) return;
      } else if (readStored(Date.now())) {
        return;
      }
      if (!schedule.automatic || !probeAllowed() || Date.now() < notBefore) return;
      await testUnderLock(schedule);
    } catch {
      // Best effort: without a reading playback plans as it did before, and
      // the next look tries again.
    }
  };
  const testUnderLock = async (schedule: ProbeSchedule) => {
    // Tabs share the reading, so only one of them tests at a time: two tests
    // at once would split the link and both read low.
    const epoch = cancelEpoch;
    const test = async () => {
      if (epoch !== cancelEpoch || generation !== scheduleGeneration || tabHidden()) return;
      const locks = lockManager();
      // Another tab may be playing; this tab's own check cannot see it.
      if (locks && (await mediaActiveInAnyTab(locks))) return;
      const controller = new AbortController();
      activeProbe = controller;
      try {
        await collectProbe(controller.signal, "quick", schedule);
      } catch (error) {
        if (error instanceof ProbeRateLimited) notBefore = Date.now() + error.retryAfter * 1000;
      } finally {
        if (activeProbe === controller) activeProbe = undefined;
      }
    };
    const locks = lockManager();
    if (!locks) return test();
    // Another tab holding the lock is testing and will share its reading.
    await locks.request(PROBE_LOCK_NAME, { ifAvailable: true }, (lock) =>
      lock ? test() : undefined,
    );
  };
  // A new network means a different reading on the server; the browser also
  // fires `change` when only its own downlink or RTT estimate moves, which is
  // not a new network.
  const network = networkInformation() as (NetworkInformationLike & EventTarget) | undefined;
  let networkType = network?.type;
  const onNetworkChange = () => {
    if (network?.type === networkType) return;
    networkType = network?.type;
    cancelEpoch++;
    activeProbe?.abort();
    forgetBandwidthEstimate();
    lookIn(LOOK_DELAY_MS);
  };
  const onVisibility = () => {
    if (!tabHidden()) {
      lookIn(LOOK_DELAY_MS);
      return;
    }
    // A hidden tab does not test; one that is running stops and measured nothing.
    clearTimeout(lookTimer);
    lookTimer = undefined;
    cancelEpoch++;
    activeProbe?.abort();
  };
  network?.addEventListener?.("change", onNetworkChange);
  document.addEventListener("visibilitychange", onVisibility);
  scheduleCleanup = () => {
    network?.removeEventListener?.("change", onNetworkChange);
    document.removeEventListener("visibilitychange", onVisibility);
  };
  if (!tabHidden()) lookIn(delayMs);
  return stopBandwidthProbes;
}

/**
 * Called when playback starts: stops a running test so it does not compete
 * with playback, and holds off new ones while the media loads. The next look
 * tries again.
 */
export function cancelBandwidthProbe(): void {
  cancelEpoch++;
  lastPlaybackStartAt = Date.now();
  activeProbe?.abort();
  // Tell other tabs: a test running there stops now, and the lock keeps new
  // ones off until the playing hold takes over or the start settles. This is
  // best effort: a browser that refuses cross-tab APIs (some privacy modes
  // throw SecurityError) must never keep playback from starting.
  try {
    installMediaTracking();
    playbackChannel?.postMessage("start");
    holdMediaLock(new Promise<void>((resolve) => setTimeout(resolve, PLAYBACK_START_QUIET_MS)));
  } catch {
    // Local cancellation above already happened.
  }
}

/** Stops the schedule and any running test, e.g. on sign-out. */
export function stopBandwidthProbes(): void {
  scheduleCleanup?.();
  scheduleCleanup = undefined;
  scheduleGeneration++;
  lastPlaybackStartAt = undefined;
  notBefore = 0;
  playbackChannel?.close();
  playbackChannel = undefined;
  if (lookTimer !== undefined) {
    clearTimeout(lookTimer);
    lookTimer = undefined;
  }
  const probe = activeProbe;
  activeProbe = undefined;
  probe?.abort();
}

export type { ProbeResult } from "./probe-meter";
