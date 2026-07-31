/**
 * Playback route-event reporting.
 *
 * Route events are diagnostics, never control: the server answers `202` with no
 * body and the client's behaviour must not depend on the outcome. A `429` means
 * "drop this event", never "retry it", which is why every send here is
 * fire-and-forget and swallows its own errors.
 *
 * The point of wiring web up to the same endpoint as the apps is that a web
 * playback failure shows up in the same telemetry as an Android or Apple one
 * instead of dying in a browser console nobody reads.
 */

import type { PlayerConfig } from "./context/PlayerConfigContext";
import { playerFetch } from "./player-fetch";
import { PROTOCOL_V3, type RouteEventNameV3, type RouteEventV3 } from "./protocol-v3";

/** The server drops unknown keys and truncates values; keep the payload small anyway. */
const MAX_DIAGNOSTIC_ENTRIES = 32;
const MAX_DIAGNOSTIC_VALUE_LENGTH = 256;

export interface RouteEventInput {
  event: RouteEventNameV3;
  playbackAttemptId: string;
  sessionId?: string | null;
  planId?: string | null;
  planAttemptId?: string | null;
  planAttemptKey?: string | null;
  failureClassification?: string;
  fallbackReason?: string;
  diagnostics?: Record<string, string | number | boolean | undefined | null>;
}

function sanitizeDiagnostics(diagnostics: RouteEventInput["diagnostics"]): Record<string, string> {
  const out: Record<string, string> = {};
  if (!diagnostics) return out;
  for (const [key, value] of Object.entries(diagnostics)) {
    if (Object.keys(out).length >= MAX_DIAGNOSTIC_ENTRIES) break;
    if (value === undefined || value === null) continue;
    const text = String(value);
    if (text.length === 0) continue;
    out[key] = text.slice(0, MAX_DIAGNOSTIC_VALUE_LENGTH);
  }
  return out;
}

/**
 * Reports one route event. Returns a promise that always resolves — callers use
 * `void reportRouteEventV3(...)` and carry on with playback regardless.
 */
export async function reportRouteEventV3(
  config: PlayerConfig,
  input: RouteEventInput,
): Promise<void> {
  // The server bounds this at 8..128 characters; without an attempt id there is
  // nothing to correlate the event against, so there is no event worth sending.
  if (!input.playbackAttemptId || input.playbackAttemptId.length < 8) return;

  const body: RouteEventV3 = {
    protocol_version: PROTOCOL_V3,
    playback_attempt_id: input.playbackAttemptId,
    event: input.event,
    diagnostics: sanitizeDiagnostics(input.diagnostics),
    ...(input.sessionId ? { session_id: input.sessionId } : {}),
    ...(input.planId ? { plan_id: input.planId } : {}),
    ...(input.planAttemptId ? { plan_attempt_id: input.planAttemptId } : {}),
    ...(input.planAttemptKey ? { plan_attempt_key: input.planAttemptKey } : {}),
    ...(input.failureClassification
      ? { failure_classification: input.failureClassification.slice(0, 64) }
      : {}),
    ...(input.fallbackReason ? { fallback_reason: input.fallbackReason.slice(0, 64) } : {}),
  };

  try {
    await playerFetch<void>(config, "/playback/route-events", {
      method: "POST",
      body: JSON.stringify(body),
    });
  } catch {
    // Diagnostics must never affect playback. A rate-limited or rejected event
    // is dropped, not retried.
  }
}
