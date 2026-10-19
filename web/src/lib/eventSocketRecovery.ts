import { V2ProblemError } from "@/api/v2/request";

/** Browser upgrade failures hide headers; a failed ticket mint can expose this floor. */
export function eventSocketRetryAfterMs(error: unknown): number {
  const seconds = error instanceof V2ProblemError ? error.retryAfterSeconds : null;
  return seconds !== null && Number.isFinite(seconds) && seconds > 0 ? seconds * 1_000 : 0;
}

/** Generic events only. Known work refusals and input-shield recovery take precedence. */
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
      if (Number.isFinite(retryAfterMs)) floor = Math.max(floor, retryAfterMs);
      return floor + Math.floor(floor * 0.5 * Math.min(1, Math.max(0, random())));
    },
  };
}
