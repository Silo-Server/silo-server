import { V2ProblemError, V2TransportError } from "@/api/v2/request";

/** Browser upgrade failures hide headers; a failed ticket mint can expose this floor. */
export function eventSocketRetryAfterMs(error: unknown): number {
  const seconds =
    error instanceof V2ProblemError || error instanceof V2TransportError
      ? error.retryAfterSeconds
      : null;
  return seconds !== null && Number.isFinite(seconds) && seconds > 0 ? seconds * 1_000 : 0;
}

/** Keep long readable floors from overflowing the browser's signed 32-bit timer. */
export function scheduleEventSocketReconnect(callback: () => void, delayMs: number): () => void {
  const deadline = performance.now() + delayMs;
  let timer: number;
  const check = () => {
    const remaining = deadline - performance.now();
    if (remaining > 0) {
      timer = window.setTimeout(check, Math.min(remaining, 2_147_483_647));
    } else {
      callback();
    }
  };
  timer = window.setTimeout(check, Math.min(delayMs, 2_147_483_647));
  return () => window.clearTimeout(timer);
}

/** Generic events only; integrating work-rate recovery must preserve its reason-specific floors. */
export function createEventSocketRecovery(random = Math.random, now = () => performance.now()) {
  let failures = 0;
  let acceptedAt: number | null = null;
  return {
    attempt() {
      acceptedAt = null;
    },
    accepted() {
      acceptedAt ??= now();
    },
    reconnect(retryAfterMs = 0) {
      let floor: number;
      if (acceptedAt !== null && now() - acceptedAt >= 30_000) {
        failures = 0;
        floor = 1_000;
      } else {
        floor = [5_000, 10_000, 20_000, 30_000][Math.min(failures, 3)]!;
        failures = Math.min(failures + 1, 3);
      }
      acceptedAt = null;
      // A finite seconds header can overflow milliseconds; keep that floor deferred.
      if (retryAfterMs > 0) floor = Math.max(floor, retryAfterMs);
      if (!Number.isFinite(floor)) return floor;
      return floor + Math.floor(floor * 0.5 * Math.min(1, Math.max(0, random())));
    },
  };
}
