import { storage } from "@/utils/storage";
import { currentNetwork } from "./network-information";
import { MAX_PROBE_KBPS, MIN_PROBE_KBPS } from "./probe-meter";

/**
 * The connection speed readings the app keeps for playback starts: the
 * server's, after this page has read them, and results this device measured
 * against a server that keeps nothing. The speed test schedule
 * (bandwidth-probe.ts) keeps them current.
 */

/** A reading kept for playback starts. */
export interface KeptReading {
  kbps: number;
  measuredAt: number;
  maxAgeMs: number;
  /**
   * The connection type a result this device measured itself was taken on,
   * against a server without connection_speed_v1. A server's reading has none:
   * it is for the connection the server saw.
   */
  network?: string;
}

/**
 * What the app keeps for playback starts: every current reading, so one that
 * outlives the best still counts once the best expires.
 */
interface StoredEntry {
  readings: KeptReading[];
  writtenAt: number;
}

/**
 * The entry this page last wrote. localStorage can be full or unavailable,
 * and its writes then fail silently; this copy keeps the reading in effect
 * for this page anyway.
 */
let pageEntry: string | undefined;
/**
 * Whether this page has read the server since its schedule started. Until it
 * has, a server's reading in storage may be the last session's, taken on
 * another network or for another account, so starts do not send it.
 */
let serverReadingConfirmed = false;

/** Whether `kbps` is a rate a start may send: within the probe's bounds. */
export function withinProbeBounds(kbps: number): boolean {
  return kbps >= MIN_PROBE_KBPS && kbps <= MAX_PROBE_KBPS;
}

function parseStored(raw: string | null | undefined): StoredEntry | undefined {
  if (!raw) return undefined;
  try {
    const parsed = JSON.parse(raw) as Partial<StoredEntry>;
    // An entry an older build wrote has no reading list; the next look replaces it.
    if (!Array.isArray(parsed.readings) || typeof parsed.writtenAt !== "number") return undefined;
    const readings = parsed.readings.filter(
      (reading): reading is KeptReading =>
        typeof reading?.kbps === "number" &&
        typeof reading.measuredAt === "number" &&
        typeof reading.maxAgeMs === "number" &&
        (reading.network === undefined || typeof reading.network === "string"),
    );
    return { readings, writtenAt: parsed.writtenAt };
  } catch {
    return undefined;
  }
}

/** The newer of this page's entry and the shared one another tab may have written. */
function readStored(): StoredEntry | undefined {
  const page = parseStored(pageEntry);
  const shared = parseStored(storage.get(storage.KEYS.BANDWIDTH_ESTIMATE));
  if (!page) return shared;
  if (!shared) return page;
  return shared.writtenAt > page.writtenAt ? shared : page;
}

function writeStored(readings: KeptReading[]): void {
  pageEntry = JSON.stringify({ readings, writtenAt: Date.now() } satisfies StoredEntry);
  storage.set(storage.KEYS.BANDWIDTH_ESTIMATE, pageEntry);
}

/**
 * The highest kept reading that is current at `now`: within its lifetime, not
 * dated ahead of `now`, within the estimate's bounds, and, for one this
 * device measured itself, taken on the connection type in use.
 */
function bestKeptKbps(
  readings: readonly KeptReading[],
  now: number,
  include: (reading: KeptReading) => boolean = () => true,
): number | undefined {
  const network = currentNetwork();
  let best: number | undefined;
  for (const reading of readings) {
    if (reading.measuredAt > now || now - reading.measuredAt > reading.maxAgeMs) continue;
    if (!withinProbeBounds(reading.kbps) || !include(reading)) continue;
    if (reading.network !== undefined && reading.network !== network) continue;
    if (best === undefined || reading.kbps > best) best = reading.kbps;
  }
  return best;
}

/** Drops the kept readings, in this page and in localStorage. */
export function forgetBandwidthEstimate(): void {
  pageEntry = undefined;
  storage.remove(storage.KEYS.BANDWIDTH_ESTIMATE);
}

/**
 * Drops the server's readings, keeping results this device measured per
 * network, until this page reads the server again.
 */
export function forgetServerReadings(): void {
  const entry = readStored();
  const own = entry?.readings.filter((reading) => reading.network !== undefined) ?? [];
  if (own.length) writeStored(own);
  else forgetBandwidthEstimate();
  serverReadingConfirmed = false;
}

/** The highest current reading in kbps, for a playback start to send. */
export function readMeasuredBandwidthKbps(now = Date.now()): number | undefined {
  return bestKeptKbps(
    readStored()?.readings ?? [],
    now,
    (reading) => reading.network !== undefined || serverReadingConfirmed,
  );
}

/**
 * Keeps the server's current readings for this connection, or drops them when
 * there are none, and lets starts send them.
 */
export function keepServerReadings(readings: KeptReading[]): void {
  if (readings.length) writeStored(readings);
  else forgetBandwidthEstimate();
  serverReadingConfirmed = true;
}

/**
 * Forgets this page's copy of the readings until it reads the server again,
 * as when the schedule stops for a sign-out or a profile switch.
 */
export function resetPageReadings(): void {
  serverReadingConfirmed = false;
  pageEntry = undefined;
}

/** Keeps a result this device measured against a server that keeps nothing, per network. */
export function keepOwnResult(kbps: number, maxAgeMs: number): void {
  const network = currentNetwork();
  const others = (readStored()?.readings ?? []).filter(
    (reading) => reading.network !== undefined && reading.network !== network,
  );
  writeStored([...others, { kbps, measuredAt: Date.now(), maxAgeMs, network }]);
}

/** Whether any kept reading is current at `now`. */
export function keptReadingCurrent(now = Date.now()): boolean {
  return bestKeptKbps(readStored()?.readings ?? [], now) !== undefined;
}
