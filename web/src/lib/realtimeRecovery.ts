import { V2ProblemError } from "@/api/v2/request";

export interface WorkAdmission {
  version: 1;
  backend: "memory" | "redis";
  scope: string;
  epoch: string;
}

/** Optional on older servers. Never infer support from a socket opening. */
export function readWorkAdmission(value: unknown): WorkAdmission | null {
  if (!value || typeof value !== "object") return null;
  const admission = (value as Record<string, unknown>).work_admission;
  if (!admission || typeof admission !== "object") return null;
  const fields = admission as Record<string, unknown>;
  return fields.version === 1 &&
    (fields.backend === "memory" || fields.backend === "redis") &&
    typeof fields.scope === "string" &&
    fields.scope.length > 0 &&
    typeof fields.epoch === "string" &&
    fields.epoch.length > 0
    ? { version: 1, backend: fields.backend, scope: fields.scope, epoch: fields.epoch }
    : null;
}

export interface WorkRefusal {
  code: "realtime_rate_limited" | "realtime_unavailable";
  retryAfterMs: number;
  requestId?: string;
}

function retryFloor(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? Math.max(250, value)
    : null;
}

/** Only this explicit pre-work contract can authorize replay of a one-shot. */
export function readWorkRefusal(value: Record<string, unknown>): WorkRefusal | null {
  const retryAfterMs = retryFloor(value.retry_after_ms);
  if (
    value.type !== "error" ||
    value.work_started !== false ||
    retryAfterMs === null ||
    (value.code !== "realtime_rate_limited" && value.code !== "realtime_unavailable")
  )
    return null;
  return {
    code: value.code,
    retryAfterMs,
    requestId: typeof value.request_id === "string" ? value.request_id : undefined,
  };
}

export function ticketWorkRefusal(error: unknown): WorkRefusal | null {
  if (!(error instanceof V2ProblemError) || ![429, 503].includes(error.status)) return null;
  const problem = error.problem as unknown as Record<string, unknown>;
  const headerMs = error.retryAfterSeconds === null ? 0 : error.retryAfterSeconds * 1_000;
  const metadataMs = retryFloor(problem.retry_after_ms) ?? 0;
  return {
    code: error.status === 503 ? "realtime_unavailable" : "realtime_rate_limited",
    retryAfterMs: Math.max(250, headerMs, metadataMs),
  };
}

/** Backoff belongs to an engagement, not to each new socket. */
export function createRealtimeBackoff(random = Math.random, now = () => performance.now()) {
  let unavailable = 0;
  let opaque = 0;
  let shield = 0;
  let usefulSince: number | null = null;
  // Quiet accepted sockets can renew without receiving a second useful frame.
  const resetIfHealthy = () => {
    if (usefulSince !== null && now() - usefulSince >= 30_000) unavailable = opaque = shield = 0;
  };
  const jitter = () => Math.floor(Math.min(1, Math.max(0, random())) * 250);
  const stepped = (steps: number[], attempt: number) => steps[Math.min(attempt, steps.length - 1)]!;
  return {
    refusal(refusal: WorkRefusal) {
      resetIfHealthy();
      usefulSince = null;
      const outageMs =
        refusal.code === "realtime_unavailable"
          ? stepped([2_000, 4_000, 8_000, 16_000, 30_000], unavailable++)
          : 0;
      return Math.max(refusal.retryAfterMs, outageMs) + jitter();
    },
    reconnect(inputShield = false) {
      resetIfHealthy();
      usefulSince = null;
      return (
        (inputShield
          ? stepped([5_000, 10_000, 20_000, 30_000], shield++)
          : stepped([1_000, 2_000, 4_000, 8_000, 16_000, 30_000], opaque++)) + jitter()
      );
    },
    useful() {
      usefulSince ??= now();
      resetIfHealthy();
    },
    interrupted() {
      resetIfHealthy();
      usefulSince = null;
    },
  };
}

export function isInputShieldClose(event: Pick<CloseEvent, "code" | "reason">) {
  return event.code === 1008 && event.reason === "realtime input shield";
}
