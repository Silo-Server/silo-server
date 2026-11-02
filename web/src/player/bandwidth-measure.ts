import { fetchWithSession } from "@/api/client";
import { V2_CLIENT_HEADERS } from "@/api/v2/request";
import {
  PROBE_FIRST_BYTE_DEADLINE_MS,
  ProbeMeter,
  type ProbeMode,
  type ProbeResult,
} from "./probe-meter";

/**
 * One connection speed test's transfer and timing. The schedule and Test speed
 * that decide when to run one are in bandwidth-probe.ts.
 */

/** The probe without connection_speed_v1, which keeps nothing for us. */
export const DEFAULT_PROBE_URL = "/api/v2/playback/bandwidth-probe";
/** How long a 429 without a usable Retry-After holds automatic tests. */
const DEFAULT_RETRY_AFTER_SECONDS = 30;

export class ProbeRateLimited extends Error {
  constructor(readonly retryAfter: number) {
    super(`Try again in ${retryAfter} ${retryAfter === 1 ? "second" : "seconds"}`);
  }
}

/** A 429's wait: its Retry-After in seconds, 30 when missing or unreadable, and at least 1. */
function retryAfterSeconds(header: string | null): number {
  const seconds = header === null || header.trim() === "" ? Number.NaN : Number(header);
  if (!Number.isFinite(seconds)) return DEFAULT_RETRY_AFTER_SECONDS;
  return Math.max(1, Math.ceil(seconds));
}

/**
 * Runs one test against `url`: `size` from the mode, the clock from the first
 * byte, and the meter deciding when to stop. Resolves to undefined when the
 * probe answered without a usable body or measured nothing, or when an abort
 * landed after the transfer. Rejects when it is aborted mid-transfer,
 * abandoned at the first-byte deadline or its body fails, and with
 * ProbeRateLimited for a 429.
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
  let end = 0;
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
      throw new ProbeRateLimited(retryAfterSeconds(res.headers.get("Retry-After")));
    }
    if (!res.ok || !res.body) {
      await res.body?.cancel().catch(() => undefined);
      return undefined;
    }
    const reader = res.body.getReader();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        const now = clock();
        if (done) {
          // The transfer ends when the body does, which can be after its last byte.
          if (start !== undefined) end = now - start;
          break;
        }
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
        end = now - start;
        if (meter.add(end, value.byteLength)) break;
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
  // Without an outcome, neither the time limit nor an early stop ended the
  // read: the body ended.
  return (meter.outcome ?? meter.finish(end, true)).result ?? undefined;
}
