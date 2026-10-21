import { fetchWithSession } from "@/api/client";
import { V2_CLIENT_HEADERS } from "@/api/v2/request";
import { storage } from "@/utils/storage";

/**
 * Measured throughput to this server, for `bandwidth_estimate_kbps`.
 *
 * Browsers will not tell us: Chromium caps `navigator.connection.downlink` at
 * 10 Mbps and Safari and Firefox have no such API, while Auto quality needs to
 * know whether a viewer can sustain a 4K source at 60–100 Mbps. So the app
 * downloads random bytes from the server in the background, times them, and
 * caches the result for playback to read.
 */

/** Bytes requested; the time budget usually ends the transfer first. */
const PROBE_BYTES = 40_000_000;
/** How long to keep timing once bytes flow. Enough to see past TCP ramp-up. */
const PROBE_TIME_BUDGET_MS = 2_000;
/** A probe that has not delivered its first byte by then is abandoned. */
const PROBE_FIRST_BYTE_DEADLINE_MS = 10_000;
/**
 * Slack past the time budget before a read that has stopped returning is cut
 * off; the rate is then taken from the bytes that did arrive.
 */
const PROBE_STALLED_READ_GRACE_MS = 500;
/** Ramp-up excluded from the rate when the probe ran long enough to spare it. */
const PROBE_WARMUP_MS = 300;
/** Shortest timed window, or smallest byte count, that yields a usable rate. */
const PROBE_MIN_WINDOW_MS = 200;
const PROBE_MIN_BYTES = 1_000_000;
/** Let the first screen settle before spending bandwidth on a probe. */
const PROBE_START_DELAY_MS = 5_000;
/** How long a measurement stands in for the network before it is re-taken. */
export const BANDWIDTH_ESTIMATE_TTL_MS = 30 * 60 * 1000;
/** The contract bounds `bandwidth_estimate_kbps` to this range. */
const MIN_ESTIMATE_KBPS = 100;
const MAX_ESTIMATE_KBPS = 1_000_000;

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

interface StoredEstimate {
  kbps: number;
  measuredAt: number;
  /**
   * Set when playback itself showed the link could not sustain more. Unlike a
   * probe result it describes whatever link the viewer is on now, so it still
   * applies on a metered connection.
   */
  observed?: boolean;
  /**
   * The metered link an observed limit was recorded on (see meteredLink), or
   * absent for an unmetered one.
   */
  link?: string;
}

function clampKbps(kbps: number): number | undefined {
  if (!Number.isFinite(kbps) || kbps < MIN_ESTIMATE_KBPS) return undefined;
  return Math.min(Math.round(kbps), MAX_ESTIMATE_KBPS);
}

/**
 * The entry this page last wrote. localStorage can be full or unavailable,
 * and its writes then fail silently; this copy keeps a just-recorded limit
 * in effect for the replan that reads it straight back.
 */
let pageEntry: string | undefined;

function parseStored(raw: string | null | undefined, now: number): StoredEstimate | undefined {
  if (!raw) return undefined;
  try {
    const parsed = JSON.parse(raw) as Partial<StoredEstimate>;
    if (typeof parsed.kbps !== "number" || typeof parsed.measuredAt !== "number") return undefined;
    if (now - parsed.measuredAt > BANDWIDTH_ESTIMATE_TTL_MS || parsed.measuredAt > now)
      return undefined;
    const kbps = clampKbps(parsed.kbps);
    if (kbps === undefined) return undefined;
    return {
      kbps,
      measuredAt: parsed.measuredAt,
      observed: parsed.observed === true,
      ...(typeof parsed.link === "string" ? { link: parsed.link } : {}),
    };
  } catch {
    return undefined;
  }
}

/** The newer of this page's entry and the shared one another tab may have written. */
function readStored(now: number): StoredEstimate | undefined {
  const page = parseStored(pageEntry, now);
  const shared = parseStored(storage.get(storage.KEYS.BANDWIDTH_ESTIMATE), now);
  if (!page) return shared;
  if (!shared) return page;
  return shared.measuredAt > page.measuredAt ? shared : page;
}

function writeStored(kbps: number, now: number, observed = false): void {
  const entry: StoredEstimate = { kbps, measuredAt: now };
  if (observed) {
    entry.observed = true;
    const link = meteredLink();
    if (link) entry.link = link;
  }
  pageEntry = JSON.stringify(entry);
  storage.set(storage.KEYS.BANDWIDTH_ESTIMATE, pageEntry);
}

/** Drops the cached estimate, in this page and in localStorage. */
export function forgetBandwidthEstimate(): void {
  pageEntry = undefined;
  storage.remove(storage.KEYS.BANDWIDTH_ESTIMATE);
}

/**
 * The cached measurement in kbps for an unmetered link, or undefined when none
 * is fresh or the cached value was recorded on a metered link.
 */
export function readMeasuredBandwidthKbps(now = Date.now()): number | undefined {
  const stored = readStored(now);
  // A limit recorded on a metered link describes that link only.
  return stored && !stored.link ? stored.kbps : undefined;
}

/**
 * Records that playback could not sustain `kbps`. The cache only ever moves
 * down this way, so later starts and replans in the TTL plan for the slower
 * link instead of the optimistic probe.
 */
export function recordObservedBandwidthLimitKbps(kbps: number, now = Date.now()): void {
  const observed = clampKbps(kbps);
  if (observed === undefined) return;
  const current = readStored(now);
  // Nothing to add when a lower limit is already recorded for this kind of
  // link. Otherwise keep the lower value but record it for the current link,
  // so a metered link honors it.
  const sameLink = current?.link === meteredLink();
  // A cached value caps the new limit unless it came from another kind of
  // metered link, which does not describe this one. Writing even when the
  // lower value is kept restarts its TTL: the link just confirmed it.
  const cap = current && (sameLink || !current.link) ? current.kbps : observed;
  writeStored(Math.min(observed, cap), now, true);
}

/**
 * Names the kind of metered link the viewer is on, from what the browser
 * reports: its connection type and whether Data Saver is on. Undefined on an
 * unmetered link. Not a network identity, but enough to keep a limit seen on
 * cellular from steering a Data Saver Wi-Fi network, and the reverse.
 */
function meteredLink(): string | undefined {
  if (!onMeteredConnection()) return undefined;
  const connection = networkInformation();
  return `${connection?.type ?? "unknown"}:${connection?.saveData === true ? "save-data" : "full"}`;
}

/**
 * The cached limit playback observed on this kind of metered link, for
 * planning on it. A limit observed on Wi-Fi or another kind of metered link
 * does not describe this one, and a probe result never applies here.
 */
export function readMeteredObservedLimitKbps(now = Date.now()): number | undefined {
  const link = meteredLink();
  const stored = readStored(now);
  return link && stored?.observed && stored.link === link ? stored.kbps : undefined;
}

/**
 * Times one probe download. Returns undefined when the probe failed, was
 * aborted, or moved too little data in too short a window to mean anything.
 */
export async function measureBandwidthKbps(
  signal: AbortSignal,
  clock: () => number = () => performance.now(),
): Promise<number | undefined> {
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
  let budgetTimer: ReturnType<typeof setTimeout> | undefined;
  const clearTimers = () => {
    clearTimeout(firstByteTimer);
    clearTimeout(budgetTimer);
    firstByteTimer = undefined;
    budgetTimer = undefined;
  };

  // The clock starts at the first chunk, so request latency and server
  // setup do not count against the link.
  let start: number | undefined;
  let warm: { at: number; bytes: number } | undefined;
  let bytes = 0;
  let now = 0;
  try {
    const { res } = await fetchWithSession(`/api/v2/playback/bandwidth-probe?size=${PROBE_BYTES}`, {
      method: "GET",
      headers: { ...V2_CLIENT_HEADERS, Accept: "application/octet-stream" },
      cache: "no-store",
      signal: controller.signal,
    });
    if (!res.ok || !res.body) {
      await res.body?.cancel().catch(() => undefined);
      return undefined;
    }
    const reader = res.body.getReader();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        now = clock();
        if (start === undefined) {
          start = now;
          clearTimeout(firstByteTimer);
          firstByteTimer = undefined;
          // Ends a read that stops returning, which the check below would
          // never reach: cancelling resolves the pending read as done. The
          // silent stretch counts against the rate, so the window ends now
          // rather than at the last chunk.
          budgetTimer = setTimeout(() => {
            now = clock();
            void reader.cancel().catch(() => undefined);
          }, PROBE_TIME_BUDGET_MS + PROBE_STALLED_READ_GRACE_MS);
          continue;
        }
        bytes += value.byteLength;
        if (!warm && now - start >= PROBE_WARMUP_MS) warm = { at: now, bytes };
        if (now - start >= PROBE_TIME_BUDGET_MS) break;
      }
    } finally {
      // Closing the body ends the transfer; the server stops at its next write.
      await reader.cancel().catch(() => undefined);
    }
  } finally {
    clearTimers();
    signal.removeEventListener("abort", forwardAbort);
  }
  if (start === undefined || now <= start) return undefined;
  const elapsed = now - start;
  if (elapsed < PROBE_MIN_WINDOW_MS && bytes < PROBE_MIN_BYTES) return undefined;
  // Prefer the rate after ramp-up when at least as much time followed it.
  if (warm && now - warm.at >= warm.at - start) {
    return clampKbps(((bytes - warm.bytes) * 8) / (now - warm.at));
  }
  // bytes * 8 bits / ms = kbit/s.
  return clampKbps((bytes * 8) / elapsed);
}

let scheduledTimer: ReturnType<typeof setTimeout> | undefined;
/** Bumped by stopBandwidthProbes so a stopped schedule never re-arms. */
let scheduleGeneration = 0;
/**
 * Bumped by cancelBandwidthProbe, so a probe still waiting for the cross-tab
 * lock when playback starts does not begin once the lock arrives.
 */
let cancelEpoch = 0;
/**
 * When playback last started. Media is still loading, and its element still
 * paused, for a while after a start, so the playing-media check alone would
 * let a timer that fires then probe during startup.
 */
let lastPlaybackStartAt: number | undefined;
/** How long after a playback start a probe holds off. */
const PLAYBACK_START_QUIET_MS = 60_000;
/** Web Lock that lets one tab at a time run a probe. */
const PROBE_LOCK_NAME = "silo-bandwidth-probe";
let activeProbe: AbortController | undefined;

/** How soon to look again when a probe could not run or did not finish. */
const PROBE_RETRY_MS = 5 * 60 * 1000;

function mediaPlaying(): boolean {
  if (typeof document === "undefined") return false;
  return Array.from(document.querySelectorAll("video, audio")).some(
    (media) => !(media as HTMLMediaElement).paused && !(media as HTMLMediaElement).ended,
  );
}

/**
 * Whether the viewer pays for this link by the byte: Data Saver is on or the
 * browser reports cellular. Such a link is not probed, and a measurement
 * taken on another link does not describe it.
 */
export function onMeteredConnection(): boolean {
  const connection = networkInformation();
  return connection?.saveData === true || connection?.type === "cellular";
}

/**
 * Shared Web Lock a tab holds while its media plays or playback is starting,
 * so a probe in another tab can tell the link is busy. Shared mode lets any
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

/** Tells other tabs that playback started here, so a running probe yields. */
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
  // A probe beside a playing or starting stream would both slow it and
  // under-measure.
  const startingPlayback =
    lastPlaybackStartAt !== undefined && Date.now() - lastPlaybackStartAt < PLAYBACK_START_QUIET_MS;
  return !onMeteredConnection() && !mediaPlaying() && !startingPlayback;
}

/**
 * Keeps a measurement cached while the app is signed in. Starts once the first
 * screen has had time to settle, measures again when the cached one expires,
 * and while a probe is not allowed (metered link, media playing) looks again
 * every few minutes. Returns a function that stops the schedule.
 */
export function scheduleBandwidthProbe(delayMs = PROBE_START_DELAY_MS): () => void {
  stopBandwidthProbes();
  try {
    installMediaTracking();
  } catch {
    // Cross-tab coordination is optional.
  }
  const generation = scheduleGeneration;
  const schedule = (ms: number) => {
    if (generation !== scheduleGeneration) return;
    scheduledTimer = setTimeout(run, Math.max(0, ms));
  };
  // Schedules the next look for when a fresh cached result expires.
  const awaitExpiry = (): boolean => {
    const now = Date.now();
    const stored = readStored(now);
    // A limit from a metered link says nothing about an unmetered one, so it
    // does not stand in for a probe there.
    if (!stored || (stored.link && !meteredLink())) return false;
    schedule(stored.measuredAt + BANDWIDTH_ESTIMATE_TTL_MS - now + 1000);
    return true;
  };
  const probe = async () => {
    const controller = new AbortController();
    activeProbe = controller;
    try {
      const kbps = await measureBandwidthKbps(controller.signal);
      if (kbps !== undefined && !controller.signal.aborted) writeStored(kbps, Date.now());
    } catch {
      // Best effort: without a measurement playback plans as it did before.
    } finally {
      if (activeProbe === controller) activeProbe = undefined;
      // A stored result schedules its own expiry; a failed or aborted probe
      // waits out the retry interval.
      if (!awaitExpiry()) schedule(PROBE_RETRY_MS);
    }
  };
  const run = () => {
    scheduledTimer = undefined;
    if (awaitExpiry()) return;
    if (!probeAllowed()) {
      schedule(PROBE_RETRY_MS);
      return;
    }
    // Tabs share the cached result, so only one of them measures at a time:
    // two probes at once would split the link and both read low.
    const locks = lockManager();
    if (!locks) {
      void probe();
      return;
    }
    const epoch = cancelEpoch;
    locks
      .request(PROBE_LOCK_NAME, { ifAvailable: true }, async (lock) => {
        if (!lock || epoch !== cancelEpoch) {
          // Another tab is measuring, or playback started while the lock
          // was on its way; look again later.
          schedule(PROBE_RETRY_MS);
          return;
        }
        // The tab that held the lock may have just stored a result.
        if (awaitExpiry() || generation !== scheduleGeneration) return;
        // Another tab may be playing; this tab's own check cannot see it.
        if (await mediaActiveInAnyTab(locks)) {
          schedule(PROBE_RETRY_MS);
          return;
        }
        await probe();
      })
      .catch(() => schedule(PROBE_RETRY_MS));
  };
  schedule(delayMs);
  return stopBandwidthProbes;
}

/**
 * Called when playback starts: stops a running probe so it does not compete
 * with playback, and holds off new ones while the media loads. The schedule
 * continues and tries again later.
 */
export function cancelBandwidthProbe(): void {
  cancelEpoch++;
  lastPlaybackStartAt = Date.now();
  activeProbe?.abort();
  // Tell other tabs: a probe running there stops now, and the lock keeps new
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

/** Stops the schedule and any running probe, e.g. on sign-out. */
export function stopBandwidthProbes(): void {
  scheduleGeneration++;
  lastPlaybackStartAt = undefined;
  playbackChannel?.close();
  playbackChannel = undefined;
  if (scheduledTimer !== undefined) {
    clearTimeout(scheduledTimer);
    scheduledTimer = undefined;
  }
  const probe = activeProbe;
  activeProbe = undefined;
  probe?.abort();
}
