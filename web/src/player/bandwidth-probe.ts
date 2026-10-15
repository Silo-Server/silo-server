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
}

function clampKbps(kbps: number): number | undefined {
  if (!Number.isFinite(kbps) || kbps < MIN_ESTIMATE_KBPS) return undefined;
  return Math.min(Math.round(kbps), MAX_ESTIMATE_KBPS);
}

function readStored(now: number): StoredEstimate | undefined {
  const raw = storage.get(storage.KEYS.BANDWIDTH_ESTIMATE);
  if (!raw) return undefined;
  try {
    const parsed = JSON.parse(raw) as Partial<StoredEstimate>;
    if (typeof parsed.kbps !== "number" || typeof parsed.measuredAt !== "number") return undefined;
    if (now - parsed.measuredAt > BANDWIDTH_ESTIMATE_TTL_MS || parsed.measuredAt > now)
      return undefined;
    const kbps = clampKbps(parsed.kbps);
    return kbps === undefined ? undefined : { kbps, measuredAt: parsed.measuredAt };
  } catch {
    return undefined;
  }
}

function writeStored(kbps: number, now: number): void {
  storage.set(storage.KEYS.BANDWIDTH_ESTIMATE, JSON.stringify({ kbps, measuredAt: now }));
}

/** The cached measurement in kbps, or undefined when none is fresh. */
export function readMeasuredBandwidthKbps(now = Date.now()): number | undefined {
  return readStored(now)?.kbps;
}

/**
 * Times one probe download. Returns undefined when the probe failed, was
 * aborted, or moved too little data in too short a window to mean anything.
 */
export async function measureBandwidthKbps(
  signal: AbortSignal,
  clock: () => number = () => performance.now(),
): Promise<number | undefined> {
  const { res } = await fetchWithSession(`/api/v2/playback/bandwidth-probe?size=${PROBE_BYTES}`, {
    method: "GET",
    headers: { ...V2_CLIENT_HEADERS, Accept: "application/octet-stream" },
    cache: "no-store",
    signal,
  });
  if (!res.ok || !res.body) {
    await res.body?.cancel().catch(() => undefined);
    return undefined;
  }
  const reader = res.body.getReader();
  // The clock starts at the first chunk, so request latency and server
  // setup do not count against the link.
  let start: number | undefined;
  let warm: { at: number; bytes: number } | undefined;
  let bytes = 0;
  let now = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      now = clock();
      if (start === undefined) {
        start = now;
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
let activeProbe: AbortController | undefined;

function mediaPlaying(): boolean {
  if (typeof document === "undefined") return false;
  return Array.from(document.querySelectorAll("video, audio")).some(
    (media) => !(media as HTMLMediaElement).paused && !(media as HTMLMediaElement).ended,
  );
}

function probeAllowed(): boolean {
  const connection = networkInformation();
  // Data Saver and cellular are links the viewer pays for by the byte, and a
  // probe beside a playing stream would both slow it and under-measure.
  return connection?.saveData !== true && connection?.type !== "cellular" && !mediaPlaying();
}

/**
 * Measures in the background unless a fresh measurement is cached. Runs once
 * the first screen has had time to settle, and never alongside playback: a
 * playback start calls cancelBandwidthProbe. Returns a cleanup function.
 */
export function scheduleBandwidthProbe(delayMs = PROBE_START_DELAY_MS): () => void {
  cancelBandwidthProbe();
  scheduledTimer = setTimeout(() => {
    scheduledTimer = undefined;
    if (readMeasuredBandwidthKbps() !== undefined || !probeAllowed()) return;
    const controller = new AbortController();
    activeProbe = controller;
    void measureBandwidthKbps(controller.signal)
      .then((kbps) => {
        if (kbps !== undefined && !controller.signal.aborted) writeStored(kbps, Date.now());
      })
      .catch(() => {
        // Best effort: without a measurement playback plans as it did before.
      })
      .finally(() => {
        if (activeProbe === controller) activeProbe = undefined;
      });
  }, delayMs);
  return cancelBandwidthProbe;
}

/** Stops a pending or running probe so it does not compete with playback. */
export function cancelBandwidthProbe(): void {
  if (scheduledTimer !== undefined) {
    clearTimeout(scheduledTimer);
    scheduledTimer = undefined;
  }
  activeProbe?.abort();
  activeProbe = undefined;
}
