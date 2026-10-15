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
  downlink?: number;
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
    return { kbps, measuredAt: parsed.measuredAt, observed: parsed.observed === true };
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
  const entry: StoredEstimate = observed
    ? { kbps, measuredAt: now, observed }
    : { kbps, measuredAt: now };
  pageEntry = JSON.stringify(entry);
  storage.set(storage.KEYS.BANDWIDTH_ESTIMATE, pageEntry);
}

/** Drops the cached estimate, in this page and in localStorage. */
export function forgetBandwidthEstimate(): void {
  pageEntry = undefined;
  storage.remove(storage.KEYS.BANDWIDTH_ESTIMATE);
}

/** The cached measurement in kbps, or undefined when none is fresh. */
export function readMeasuredBandwidthKbps(now = Date.now()): number | undefined {
  return readStored(now)?.kbps;
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
  if (current?.observed && current.kbps <= observed) return;
  // A lower probe result still caps it, but the entry becomes observed so a
  // metered link honors it.
  writeStored(Math.min(observed, current?.kbps ?? observed), now, true);
}

/**
 * The cached limit playback observed, or undefined when there is none or the
 * cached value is only a probe result.
 */
export function readObservedBandwidthLimitKbps(now = Date.now()): number | undefined {
  const stored = readStored(now);
  return stored?.observed ? stored.kbps : undefined;
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

function probeAllowed(): boolean {
  // A probe beside a playing stream would both slow it and under-measure.
  return !onMeteredConnection() && !mediaPlaying();
}

/**
 * Keeps a measurement cached while the app is signed in. Starts once the first
 * screen has had time to settle, measures again when the cached one expires,
 * and while a probe is not allowed (metered link, media playing) looks again
 * every few minutes. Returns a function that stops the schedule.
 */
export function scheduleBandwidthProbe(delayMs = PROBE_START_DELAY_MS): () => void {
  stopBandwidthProbes();
  const generation = scheduleGeneration;
  const schedule = (ms: number) => {
    if (generation !== scheduleGeneration) return;
    scheduledTimer = setTimeout(run, Math.max(0, ms));
  };
  // Schedules the next look for when a fresh cached result expires.
  const awaitExpiry = (): boolean => {
    const now = Date.now();
    const stored = readStored(now);
    if (!stored) return false;
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
    const locks = typeof navigator === "undefined" ? undefined : navigator.locks;
    if (!locks) {
      void probe();
      return;
    }
    locks
      .request(PROBE_LOCK_NAME, { ifAvailable: true }, async (lock) => {
        if (!lock) {
          // Another tab is measuring; its result is waiting on the next look.
          schedule(PROBE_RETRY_MS);
          return;
        }
        // The tab that held the lock may have just stored a result.
        if (awaitExpiry() || generation !== scheduleGeneration) return;
        await probe();
      })
      .catch(() => schedule(PROBE_RETRY_MS));
  };
  schedule(delayMs);
  return stopBandwidthProbes;
}

/**
 * Stops a running probe so it does not compete with playback. The schedule
 * continues and tries again later.
 */
export function cancelBandwidthProbe(): void {
  activeProbe?.abort();
}

/** Stops the schedule and any running probe, e.g. on sign-out. */
export function stopBandwidthProbes(): void {
  scheduleGeneration++;
  if (scheduledTimer !== undefined) {
    clearTimeout(scheduledTimer);
    scheduledTimer = undefined;
  }
  const probe = activeProbe;
  activeProbe = undefined;
  probe?.abort();
}
